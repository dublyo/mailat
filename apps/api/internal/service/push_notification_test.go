package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dublyo/mailat/api/internal/config"
)

// RFC 8291 example keys: the server pair signs, the user agent pair receives.
const (
	testVAPIDPublic  = "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"
	testVAPIDPrivate = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	testUAPublic     = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	testUAPrivate    = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	testUAAuth       = "BTBZMqHH6r4Tts7J_aSIgg"
)

type pushRequest struct {
	path    string
	header  http.Header
	payload newEmailPush
}

// pushServer is a fake push service; status maps a path to its reply.
type pushServer struct {
	*httptest.Server
	mu       sync.Mutex
	status   map[string]int
	received []pushRequest
}

func newPushServer(t *testing.T) *pushServer {
	t.Helper()
	ua, err := ecdh.P256().NewPrivateKey(mustB64(t, testUAPrivate))
	if err != nil {
		t.Fatal(err)
	}
	auth := mustB64(t, testUAAuth)
	ps := &pushServer{status: map[string]int{}}
	ps.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p newEmailPush
		if plain, ok := openWebPush(ua, auth, body); ok {
			_ = json.Unmarshal(plain, &p)
		}
		ps.mu.Lock()
		ps.received = append(ps.received, pushRequest{r.URL.Path, r.Header.Clone(), p})
		code := ps.status[r.URL.Path]
		ps.mu.Unlock()
		if code == 0 {
			code = http.StatusCreated
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(ps.Close)
	return ps
}

func (ps *pushServer) requests() []pushRequest {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return append([]pushRequest(nil), ps.received...)
}

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// openWebPush decrypts a single-record aes128gcm body as the browser would.
func openWebPush(ua *ecdh.PrivateKey, auth, body []byte) ([]byte, bool) {
	if len(body) < 86 || body[20] != 65 {
		return nil, false
	}
	salt, asPublic := body[:16], body[21:86]
	as, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		return nil, false
	}
	shared, _ := ua.ECDH(as)
	prkKey, _ := hkdf.Extract(sha256.New, shared, auth)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(ua.PublicKey().Bytes())+string(asPublic), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, body[86:], nil)
	if err != nil || len(plain) == 0 || plain[len(plain)-1] != 2 {
		return nil, false
	}
	return plain[:len(plain)-1], true
}

func testPushService(t *testing.T, f *arrivalFixture, ps *pushServer) *PushNotificationService {
	t.Helper()
	cfg := &config.Config{VAPIDPublicKey: testVAPIDPublic, VAPIDPrivateKey: testVAPIDPrivate, VAPIDSubject: "mailto:ops@arrival.test"}
	s := NewPushNotificationService(f.db, cfg)
	if !s.Enabled() {
		t.Fatal("push should be enabled")
	}
	// The fake push service is local; production keeps the SSRF-safe client
	// and the public push-service allowlist.
	s.httpClient = ps.Client()
	s.suffixes = []string{"127.0.0.1"}
	return s
}

func (f *arrivalFixture) subscribe(t *testing.T, s *PushNotificationService, endpoint string) *PushSubscription {
	t.Helper()
	sub, err := s.Subscribe(context.Background(), f.user, &CreatePushSubscriptionInput{Endpoint: endpoint, P256dhKey: testUAPublic, AuthKey: testUAAuth, DeviceName: "Laptop"})
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

func TestPushSubscribeValidatesAndNeverTakesOverEndpoints(t *testing.T) {
	f := newArrivalFixture(t)
	ctx := context.Background()
	if _, err := NewPushNotificationService(f.db, &config.Config{}).Subscribe(ctx, f.user, &CreatePushSubscriptionInput{}); !errors.Is(err, ErrPushDisabled) {
		t.Fatalf("disabled push: %v", err)
	}
	if NewPushNotificationService(f.db, &config.Config{}).GetVAPIDPublicKey() != "" {
		t.Fatal("disabled push must not publish a key")
	}
	s := NewPushNotificationService(f.db, &config.Config{VAPIDPublicKey: testVAPIDPublic, VAPIDPrivateKey: testVAPIDPrivate, VAPIDSubject: "mailto:ops@arrival.test", PushEndpointHostSuffixes: []string{"fcm.googleapis.com"}})
	if s.GetVAPIDPublicKey() != testVAPIDPublic {
		t.Fatal("configured key not published")
	}
	valid := CreatePushSubscriptionInput{Endpoint: "https://fcm.googleapis.com/fcm/send/device-1", P256dhKey: testUAPublic, AuthKey: testUAAuth}
	for name, mutate := range map[string]func(*CreatePushSubscriptionInput){
		"http":          func(in *CreatePushSubscriptionInput) { in.Endpoint = "http://fcm.googleapis.com/x" },
		"foreign host":  func(in *CreatePushSubscriptionInput) { in.Endpoint = "https://fcm.googleapis.com.evil.test/x" },
		"internal host": func(in *CreatePushSubscriptionInput) { in.Endpoint = "https://169.254.169.254/latest" },
		"long":          func(in *CreatePushSubscriptionInput) { in.Endpoint += strings.Repeat("a", 1024) },
		"short p256dh":  func(in *CreatePushSubscriptionInput) { in.P256dhKey = testUAPublic[:40] },
		"short auth":    func(in *CreatePushSubscriptionInput) { in.AuthKey = "AAAA" },
	} {
		in := valid
		mutate(&in)
		var invalid *PushInputError
		if _, err := s.Subscribe(ctx, f.user, &in); !errors.As(err, &invalid) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	in := valid
	sub, err := s.Subscribe(ctx, f.user, &in)
	if err != nil || !sub.Active {
		t.Fatal(err)
	}
	var keyID string
	if err = f.db.QueryRow(`SELECT vapid_key_id FROM push_subscriptions WHERE id=$1`, sub.ID).Scan(&keyID); err != nil || keyID != s.keys.KeyID || len(keyID) != 16 {
		t.Fatalf("key id %q: %v", keyID, err)
	}
	var other int64
	if err = f.db.QueryRow(`INSERT INTO users(org_id,email,password_hash,role,updated_at) VALUES($1,'other@arrival.test','unused','member',now()) RETURNING id`, f.org).Scan(&other); err != nil {
		t.Fatal(err)
	}
	in = valid
	if _, err = s.Subscribe(ctx, other, &in); !errors.Is(err, ErrPushEndpointTaken) {
		t.Fatalf("another user's endpoint: %v", err)
	}
	var owner int64
	if err = f.db.QueryRow(`SELECT user_id FROM push_subscriptions WHERE endpoint=$1`, valid.Endpoint).Scan(&owner); err != nil || owner != f.user {
		t.Fatal("endpoint was taken over", owner, err)
	}
	// The owner re-subscribing reactivates and resets failures.
	if _, err = f.db.Exec(`UPDATE push_subscriptions SET active=false, failure_count=3`); err != nil {
		t.Fatal(err)
	}
	in = valid
	if sub, err = s.Subscribe(ctx, f.user, &in); err != nil || !sub.Active || f.count(t, `SELECT failure_count FROM push_subscriptions`) != 0 {
		t.Fatal("resubscribe did not reset", err)
	}
}

func TestPushJobDeliversEncryptedNotificationAndRecordsOutcomes(t *testing.T) {
	f := newArrivalFixture(t)
	ps := newPushServer(t)
	s := testPushService(t, f, ps)
	f.runner.SetPusher(s)
	ok := f.subscribe(t, s, ps.URL+"/ok")
	gone := f.subscribe(t, s, ps.URL+"/gone")
	flaky := f.subscribe(t, s, ps.URL+"/flaky")
	ps.status["/gone"] = http.StatusGone
	ps.status["/flaky"] = http.StatusServiceUnavailable

	f.deliver(t, "push-1", "alice@sender.test")
	f.runOnce(t)
	if status, result := f.job(t, "push-1", "push"); status != "done" {
		t.Fatal(status, result)
	}
	reqs := ps.requests()
	if len(reqs) != 3 {
		t.Fatalf("want one request per device, got %d", len(reqs))
	}
	var uuid string
	if err := f.db.QueryRow(`SELECT uuid::text FROM received_emails WHERE message_id LIKE '%push-1%'`).Scan(&uuid); err != nil {
		t.Fatal(err)
	}
	for _, r := range reqs {
		if r.payload.Type != "new_email" || r.payload.UUID != uuid || r.payload.From != "alice@sender.test" || r.payload.Subject != "Question push-1" || r.payload.Identity != "owner@arrival.test" {
			t.Fatalf("payload did not decrypt to the notice: %+v", r.payload)
		}
		h := r.header
		if h.Get("Content-Encoding") != "aes128gcm" || h.Get("TTL") != "86400" || h.Get("Urgency") != "normal" || len(h.Get("Topic")) != 22 ||
			!strings.HasPrefix(h.Get("Authorization"), "vapid t=") || !strings.HasSuffix(h.Get("Authorization"), ", k="+testVAPIDPublic) {
			t.Fatalf("headers %v", h)
		}
	}
	state := func(id int64) (active bool, failures int) {
		t.Helper()
		if err := f.db.QueryRow(`SELECT active,failure_count FROM push_subscriptions WHERE id=$1`, id).Scan(&active, &failures); err != nil {
			t.Fatal(err)
		}
		return
	}
	if a, n := state(ok.ID); !a || n != 0 {
		t.Fatal("delivered device changed", a, n)
	}
	if a, _ := state(gone.ID); a {
		t.Fatal("410 must deactivate the subscription")
	}
	if a, n := state(flaky.ID); !a || n != 1 {
		t.Fatal("transient failure not counted", a, n)
	}

	// Every remaining device failing transiently retries the job; the fifth
	// consecutive failure deactivates the device.
	ps.status["/ok"] = http.StatusBadGateway
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := s.SendNewEmailNotification(ctx, f.user, uuid, "a@b.test", "s", "owner@arrival.test"); !errors.Is(err, errPushTransient) {
			t.Fatalf("all-transient send: %v", err)
		}
	}
	if a, n := state(flaky.ID); !a || n != 4 {
		t.Fatal(a, n)
	}
	_ = s.SendNewEmailNotification(ctx, f.user, uuid, "a@b.test", "s", "owner@arrival.test")
	if a, n := state(flaky.ID); a || n != 5 {
		t.Fatal("five failures must deactivate", a, n)
	}
	ps.status["/ok"] = 0
	if err := s.SendNewEmailNotification(ctx, f.user, uuid, "a@b.test", "s", "owner@arrival.test"); err != nil {
		t.Fatal(err)
	}
	if a, n := state(ok.ID); !a || n != 0 {
		t.Fatal("success must reset failures", a, n)
	}

	// The user's setting and the device's preference both silence pushes.
	before := len(ps.requests())
	if _, err := f.db.Exec(`INSERT INTO user_settings(org_id,user_id,new_email_notifications,updated_at) VALUES($1,$2,false,now())`, f.org, f.user); err != nil {
		t.Fatal(err)
	}
	if err := s.SendNewEmailNotification(ctx, f.user, uuid, "a@b.test", "s", "x"); err != nil || len(ps.requests()) != before {
		t.Fatal("pushed despite the user setting", err)
	}
	if _, err := f.db.Exec(`UPDATE user_settings SET new_email_notifications=true; UPDATE push_subscriptions SET notify_new_email=false`); err != nil {
		t.Fatal(err)
	}
	if err := s.SendNewEmailNotification(ctx, f.user, uuid, "a@b.test", "s", "x"); err != nil || len(ps.requests()) != before {
		t.Fatal("pushed despite the device preference", err)
	}
	// No eligible device: the next arrival queues no push job.
	f.deliver(t, "push-2", "bob@sender.test")
	if c := f.count(t, `SELECT COUNT(*) FROM mail_arrival_jobs WHERE ses_message_id='push-2' AND kind='push'`); c != 0 {
		t.Fatalf("queued %d push jobs without an eligible device", c)
	}

	// A rotated VAPID key retires subscriptions made with the old one.
	if _, err := f.db.Exec(`UPDATE push_subscriptions SET notify_new_email=true, active=true, vapid_key_id='0000000000000000'`); err != nil {
		t.Fatal(err)
	}
	if err := s.SendNewEmailNotification(ctx, f.user, uuid, "a@b.test", "s", "x"); err != nil || len(ps.requests()) != before {
		t.Fatal("pushed with a retired key", err)
	}
	if c := f.count(t, `SELECT COUNT(*) FROM push_subscriptions WHERE active`); c != 0 {
		t.Fatalf("%d subscriptions still active after key rotation", c)
	}
}
