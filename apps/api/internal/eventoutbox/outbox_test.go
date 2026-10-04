package eventoutbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/dublyo/mailat/api/internal/testutil"
	"github.com/google/uuid"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func seed(t *testing.T) (*sql.DB, int64, int64) {
	t.Helper()
	db := testutil.Database(t)
	var org, user int64
	if err := db.QueryRow(`INSERT INTO organizations(name,slug,updated_at) VALUES('test','test',NOW()) RETURNING id`).Scan(&org); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO users(org_id,email,password_hash,updated_at) VALUES($1,'owner@example.com','unused',NOW()) RETURNING id`, org).Scan(&user); err != nil {
		t.Fatal(err)
	}
	return db, org, user
}
func TestOutboxAtomicFiltersRetryReplayAndIsolation(t *testing.T) {
	db, org, user := seed(t)
	ctx := context.Background()
	var webhook int64
	if err := db.QueryRow(`INSERT INTO webhooks(org_id,user_id,name,url,secret,events,updated_at) VALUES($1,$2,'sink','https://hook.example.com/receive','secret',ARRAY['email.received'],NOW()) RETURNING id`, org, user).Scan(&webhook); err != nil {
		t.Fatal(err)
	}
	for _, filter := range []string{`{"folder":"inbox"}`, `{"folder":"spam"}`} {
		if _, err := db.Exec(`INSERT INTO webhook_triggers(org_id,user_id,name,trigger_type,filters,webhook_url,secret,active) VALUES($1,$2,'trigger','email_received',$3,'https://hook.example.com/receive','secret',true)`, org, user, filter); err != nil {
			t.Fatal(err)
		}
	}
	event := Event{Type: "email.received", OrgID: org, UserID: user, MessageUUID: uuid.NewString(), DedupeKey: "mail:1", Data: map[string]any{"folder": "inbox"}}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = Emit(ctx, tx, event); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM webhook_events`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback count=%d err=%v", count, err)
	}
	for i := 0; i < 2; i++ {
		tx, _ = db.BeginTx(ctx, nil)
		if err = Emit(ctx, tx, event); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM webhook_deliveries`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("dedupe/filter count=%d err=%v", count, err)
	}
	var delivery, eventID string
	if err = db.QueryRow(`SELECT id,event_id FROM webhook_deliveries WHERE webhook_id=$1`, webhook).Scan(&delivery, &eventID); err != nil {
		t.Fatal(err)
	}
	seen := []string{}
	status := 500
	d := NewDispatcher(db)
	d.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if !Verify(body, r.Header.Get("X-Webhook-Signature"), "secret", time.Minute) {
			t.Error("invalid actual signature")
		}
		var e Envelope
		if err := json.Unmarshal(body, &e); err != nil {
			t.Fatal(err)
		}
		if e.ID != eventID || e.Data["messageUuid"] != event.MessageUUID {
			t.Errorf("wrong stable identifiers %+v", e)
		}
		seen = append(seen, string(body))
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("fixture\x00\xff")), Header: http.Header{}}, nil
	})}
	for i := 1; i <= 8; i++ {
		if _, err = db.Exec(`UPDATE webhook_deliveries SET next_attempt_at=NOW() WHERE id=$1`, delivery); err != nil {
			t.Fatal(err)
		}
		result, err := d.DispatchOne(ctx, delivery)
		if err != nil {
			t.Fatal(err)
		}
		want := "retry"
		if i == 8 {
			want = "dead_letter"
		}
		if result.Status != want || result.HTTPStatus != 500 {
			t.Fatalf("attempt %d %+v", i, result)
		}
	}
	if _, _, err = Get(ctx, db, org, user+1, delivery); err == nil {
		t.Error("other user read delivery")
	}
	if err = Replay(ctx, db, org, user+1, delivery); err == nil {
		t.Error("other user replayed delivery")
	}
	if err = Replay(ctx, db, org, user, delivery); err != nil {
		t.Fatal(err)
	}
	status = 204
	result, err := d.DispatchOne(ctx, delivery)
	if err != nil || result.Status != "delivered" {
		t.Fatalf("replay %+v %v", result, err)
	}
	_, attempts, err := Get(ctx, db, org, user, delivery)
	if err != nil || len(attempts) != 9 {
		t.Fatalf("attempt history=%d %v", len(attempts), err)
	}
	for _, body := range seen {
		if body != seen[0] {
			t.Fatal("payload changed across retry/replay")
		}
	}
	// An abandoned worker lease is recovered without creating a new event.
	if err = Replay(ctx, db, org, user, delivery); err != nil {
		t.Fatal(err)
	}
	if _, err = d.claim(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE webhook_deliveries SET locked_until=NOW()-INTERVAL '1 second' WHERE id=$1`, delivery); err != nil {
		t.Fatal(err)
	}
	if result, err = d.DispatchOne(ctx, delivery); err != nil || result.Status != "delivered" {
		t.Fatalf("lease recovery %+v %v", result, err)
	}
}
func TestSignatureAndDestinationSafety(t *testing.T) {
	body := []byte(`{"id":"stable"}`)
	signature := Sign(body, "secret", time.Now())
	if !Verify(body, signature, "secret", time.Minute) {
		t.Fatal("real HMAC rejected")
	}
	for _, s := range []string{signature + ",v1=abc", "t=," + signature, "v1=," + signature, signature + ",unknown=value", Sign(body, "secret", time.Now().Add(-10*time.Minute)), Sign(body, "secret", time.Now().Add(10*time.Minute)), signature + "x"} {
		if Verify(body, s, "secret", time.Minute) {
			t.Errorf("accepted malformed or stale signature %s", s)
		}
	}
	if Verify([]byte("changed"), signature, "secret", time.Minute) {
		t.Fatal("accepted modified payload")
	}
	for _, url := range []string{"http://public.example/", "https://localhost/", "https://127.0.0.1/", "https://[::1]/", "https://[::ffff:127.0.0.1]/", "https://169.254.169.254/", "https://10.1.1.1/", "https://100.64.1.1/", "https://user:pass@example.com/", "https://service.local/", "https://192.0.2.2/"} {
		if ValidateDestination(url) == nil {
			t.Errorf("unsafe destination accepted %s", url)
		}
	}
	if err := ValidateDestination("https://hooks.example.com/n8n"); err != nil {
		t.Fatal(err)
	}
	if SafeClient().CheckRedirect(&http.Request{}, nil) == nil {
		t.Fatal("redirect allowed")
	}
}
