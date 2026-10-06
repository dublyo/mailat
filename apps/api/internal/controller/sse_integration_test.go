package controller

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/internal/testutil"
)

type sseFixture struct {
	db   *sql.DB
	cfg  *config.Config
	base string
	hub  *SSEController
}

const sseFixtureUser = 735102 // distinct from other packages' fixtures sharing the NOTIFY channel

func newSSEFixture(t *testing.T) *sseFixture {
	t.Helper()
	db := testutil.Database(t)
	cfg := &config.Config{JWTSecret: "sse-fixture-secret-not-a-live-secret", EmailProvider: "ses"}
	oldDB, oldCfg := database.DB, config.Cfg
	database.DB, config.Cfg = db, cfg
	t.Cleanup(func() { database.DB, config.Cfg = oldDB, oldCfg })
	hash, _ := bcrypt.GenerateFromPassword([]byte("fixture-password"), bcrypt.MinCost)
	for _, q := range []string{
		`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Fixture','fixture',now())`,
		fmt.Sprintf(`INSERT INTO users(id,org_id,email,password_hash,name,role,updated_at) VALUES(%d,1,'live@fixture.test','%s','Live','owner',now())`, sseFixtureUser, hash),
		`INSERT INTO domains(id,org_id,name,verification_token,status,updated_at) VALUES(1,1,'fixture.test','fixture','active',now())`,
		fmt.Sprintf(`INSERT INTO identities(id,user_id,domain_id,email,updated_at) VALUES(1,%d,1,'live@fixture.test',now())`, sseFixtureUser),
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	notifier := service.NewMailboxNotifier(os.Getenv("MAILAT_TEST_DATABASE_URL"))
	hub := NewSSEController(notifier, service.NewInboxService(db, cfg, service.NewIdentityService(db, cfg)), 100)
	hub.heartbeat, hub.credentialCheck = 200*time.Millisecond, 200*time.Millisecond
	var running sync.WaitGroup
	running.Add(1)
	go func() { defer running.Done(); notifier.Run(ctx) }()

	auth := NewAuthController(service.NewAuthService(db, cfg), service.NewRateLimiter(db, cfg))
	s := ghttp.GetServer(fmt.Sprintf("sse-live-%d", time.Now().UnixNano()))
	s.SetAddr("127.0.0.1:0")
	s.SetDumpRouterMap(false)
	s.SetAccessLogEnabled(false)
	s.Group("/api/v1", func(g *ghttp.RouterGroup) {
		g.Middleware(middleware.Auth)
		g.POST("/auth/stream-token", auth.StreamToken)
		g.GET("/sse/connect", hub.Connect)
	})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Streams and the listener stop before the schema is dropped.
		deadline := time.Now().Add(5 * time.Second)
		for hub.GetTotalConnections() > 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		s.Shutdown()
		cancel()
		running.Wait()
	})
	deadline := time.Now().Add(10 * time.Second)
	for !notifier.Healthy() {
		if time.Now().After(deadline) {
			t.Fatal("listener never connected")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return &sseFixture{db: db, cfg: cfg, base: fmt.Sprintf("http://127.0.0.1:%d", s.GetListenedPort()), hub: hub}
}

func (f *sseFixture) session(t *testing.T) string {
	t.Helper()
	res, err := service.NewAuthService(f.db, f.cfg).Login(context.Background(), &model.LoginRequest{Email: "live@fixture.test", Password: "fixture-password"})
	if err != nil {
		t.Fatal(err)
	}
	return res.Token
}

func (f *sseFixture) ticket(t *testing.T, session string) string {
	t.Helper()
	req, _ := http.NewRequest("POST", f.base+"/api/v1/auth/stream-token", nil)
	req.Header.Set("Authorization", "Bearer "+session)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err = json.NewDecoder(res.Body).Decode(&out); err != nil || out.Data.Token == "" {
		t.Fatalf("stream ticket: status %d %v", res.StatusCode, err)
	}
	return out.Data.Token
}

// shortTicket is a valid stream ticket that expires after ttl.
func (f *sseFixture) shortTicket(t *testing.T, session string, ttl time.Duration) string {
	t.Helper()
	sum := sha256.Sum256([]byte(session))
	claims := model.AccessClaims{UserID: sseFixtureUser, OrgID: 1, Purpose: "stream", SessionHash: hex.EncodeToString(sum[:]), RegisteredClaims: jwt.RegisteredClaims{
		Issuer: "mailat", Audience: jwt.ClaimStrings{"mailat-sse"}, ID: uuid.NewString(), IssuedAt: jwt.NewNumericDate(time.Now()), ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
	}}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(f.cfg.JWTSecret))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

type liveStream struct {
	res    *http.Response
	events chan wireEvent
	closed chan struct{}
}

func (f *sseFixture) open(t *testing.T, ticket, cursor string) (*liveStream, int) {
	t.Helper()
	url := f.base + "/api/v1/sse/connect?token=" + ticket
	if cursor != "" {
		url += "&cursor=" + cursor
	}
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		res.Body.Close()
		return nil, res.StatusCode
	}
	s := &liveStream{res: res, events: make(chan wireEvent, 64), closed: make(chan struct{})}
	t.Cleanup(func() { res.Body.Close(); <-s.closed })
	go func() {
		defer close(s.closed)
		reader := bufio.NewReader(res.Body)
		var frame []string
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSuffix(line, "\n")
			if line != "" {
				frame = append(frame, line)
				continue
			}
			events := parseWire(t, strings.Join(frame, "\n")+"\n\n")
			frame = nil
			if events[0].Event != "heartbeat" {
				s.events <- events[0]
			}
		}
	}()
	return s, 200
}

func (s *liveStream) next(t *testing.T) wireEvent {
	t.Helper()
	select {
	case e := <-s.events:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no live event")
	}
	return wireEvent{}
}

func (f *sseFixture) exec(t *testing.T, q string) {
	t.Helper()
	if _, err := f.db.Exec(q); err != nil {
		t.Fatal(err)
	}
}

func TestSSEResumesFromCursorAcrossCommitsHTTP(t *testing.T) {
	f := newSSEFixture(t)
	session := f.session(t)
	f.exec(t, `INSERT INTO received_emails(uuid,org_id,domain_id,identity_id,message_id,from_email,subject,updated_at) VALUES
		('00000000-0000-0000-0000-00000000000a',1,1,1,'m-a','s@example.test','A',now()),('00000000-0000-0000-0000-00000000000b',1,1,1,'m-b','s@example.test','B',now())`)

	ticket := f.ticket(t, session)
	stream, status := f.open(t, ticket, "now")
	if status != 200 {
		t.Fatalf("connect status %d", status)
	}
	if e := stream.next(t); e.Event != "connected" || e.Data["cursor"] != "2" {
		t.Fatalf("connected %+v", e)
	}
	if _, status = f.open(t, ticket, "2"); status != 401 {
		t.Fatalf("reused ticket got %d", status)
	}

	// Three separate commits arrive in order with their cursors.
	f.exec(t, `INSERT INTO received_emails(uuid,org_id,domain_id,identity_id,message_id,from_email,subject,folder,updated_at) VALUES('00000000-0000-0000-0000-00000000000c',1,1,1,'m-c','s@example.test','C','inbox',now())`)
	f.exec(t, `UPDATE received_emails SET is_read=true WHERE uuid='00000000-0000-0000-0000-00000000000a'`)
	f.exec(t, `DELETE FROM received_emails WHERE uuid='00000000-0000-0000-0000-00000000000b'`)
	want := []struct{ id, event string }{{"3", "new_email"}, {"4", "email_update"}, {"5", "email_deleted"}}
	check := func(s *liveStream) {
		t.Helper()
		for _, w := range want {
			e := s.next(t)
			if e.ID != w.id || e.Event != w.event {
				t.Fatalf("got %s/%s want %s/%s", e.ID, e.Event, w.id, w.event)
			}
			if summary, _ := e.Data["summary"].(map[string]any); w.event == "email_update" && summary["isRead"] != true {
				t.Fatalf("update lacks the current row: %+v", e.Data)
			}
		}
	}
	check(stream)
	if e := stream.next(t); e.Event != "counts_update" {
		t.Fatalf("want counts_update, got %+v", e)
	}

	// A reconnect with a fresh ticket replays everything after its cursor.
	resumed, status := f.open(t, f.ticket(t, session), "2")
	if status != 200 {
		t.Fatalf("resume status %d", status)
	}
	if e := resumed.next(t); e.Event != "connected" || e.Data["cursor"] != "2" {
		t.Fatalf("resume connected %+v", e)
	}
	check(resumed)
	if ahead, _ := f.open(t, f.ticket(t, session), "99"); ahead != nil {
		if e := ahead.next(t); e.Event != "connected" {
			t.Fatalf("ahead connected %+v", e)
		}
		if e := ahead.next(t); e.Event != "resync" || e.Data["reason"] != "ahead" || e.Data["cursor"] != "5" {
			t.Fatalf("cursor ahead of the server must resync: %+v", e)
		}
	}
}

func TestSSEOutlivesTicketAndEndsWithSessionHTTP(t *testing.T) {
	f := newSSEFixture(t)
	session := f.session(t)
	stream, status := f.open(t, f.shortTicket(t, session, time.Second), "")
	if status != 200 {
		t.Fatalf("connect status %d", status)
	}
	if e := stream.next(t); e.Event != "connected" {
		t.Fatalf("connected %+v", e)
	}
	// Several credential checks pass after the ticket itself has expired.
	time.Sleep(1500 * time.Millisecond)
	f.exec(t, `INSERT INTO received_emails(uuid,org_id,domain_id,identity_id,message_id,from_email,subject,updated_at) VALUES(gen_random_uuid(),1,1,1,'late','s@example.test','Late',now())`)
	if e := stream.next(t); e.Event != "new_email" {
		t.Fatalf("stream ended with its ticket: %+v", e)
	}
	f.exec(t, `UPDATE user_sessions SET active=false, revoked_at=now()`)
	select {
	case <-stream.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("stream survived session revocation")
	}
}
