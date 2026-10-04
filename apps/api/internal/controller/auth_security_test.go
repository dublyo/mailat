package controller_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/controller"
	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/internal/testutil"
	"github.com/dublyo/mailat/api/pkg/response"
	"github.com/gogf/gf/v2/net/ghttp"
	"golang.org/x/crypto/bcrypt"
)

type authFixture struct {
	db   *sql.DB
	cfg  *config.Config
	base string
	auth *service.AuthService
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	db := testutil.Database(t)
	cfg := &config.Config{JWTSecret: "integration-fixture-not-a-live-secret", EmailProvider: "ses"}
	oldDB, oldCfg := database.DB, config.Cfg
	database.DB = db
	config.Cfg = cfg
	t.Cleanup(func() { database.DB = oldDB; config.Cfg = oldCfg })
	hash, _ := bcrypt.GenerateFromPassword([]byte("fixture-password"), bcrypt.MinCost)
	_, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Fixture','fixture',now()),(2,'Other','other',now());`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO users(id,org_id,email,password_hash,name,role,updated_at) VALUES(1,1,'owner@example.test',$1,'Owner','owner',now()),(2,1,'member@example.test',$1,'Member','member',now()),(3,2,'other@example.test',$1,'Other','owner',now())`, string(hash))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO domains(id,org_id,name,verification_token,status,updated_at) VALUES(1,1,'fixture.test','fixture','active',now()),(2,2,'other.test','other','active',now());INSERT INTO identities(id,user_id,domain_id,email,updated_at) VALUES(1,1,1,'me@fixture.test',now()),(2,3,2,'other@other.test',now());INSERT INTO received_emails(uuid,org_id,domain_id,identity_id,message_id,from_email,subject,updated_at) VALUES('00000000-0000-0000-0000-000000000010',1,1,1,'fixture-1','sender@example.test','Fixture message',now()),('00000000-0000-0000-0000-000000000020',2,2,2,'fixture-2','sender@example.test','Other message',now());`)
	if err != nil {
		t.Fatal(err)
	}
	auth := service.NewAuthService(db, cfg)
	ac := controller.NewAuthController(auth)
	inbox := controller.NewReceivedInboxController(service.NewInboxService(db, cfg, service.NewIdentityService(db, cfg)), nil)
	s := ghttp.GetServer(fmt.Sprintf("auth-security-%d", time.Now().UnixNano()))
	s.SetAddr("127.0.0.1:0")
	s.SetDumpRouterMap(false)
	s.SetAccessLogEnabled(false)
	s.Group("/api/v1", func(g *ghttp.RouterGroup) {
		g.POST("/auth/login", ac.Login)
		g.POST("/auth/2fa/challenge", ac.CompleteChallenge)
		g.Group("/", func(g *ghttp.RouterGroup) {
			g.Middleware(middleware.Auth)
			g.POST("/auth/logout", ac.Logout)
			g.POST("/auth/stream-token", ac.StreamToken)
			g.GET("/auth/me", ac.Me)
			g.GET("/api-keys", ac.ListAPIKeys)
			g.POST("/api-keys", ac.CreateAPIKey)
			g.DELETE("/api-keys/:uuid", ac.DeleteAPIKey)
			g.GET("/inbox/received/:uuid", inbox.GetEmail)
			g.POST("/inbox/received/mark", inbox.MarkEmails)
			g.GET("/sse/connect", func(r *ghttp.Request) { response.Success(r, map[string]bool{"connected": true}) })
			g.GET("/settings", func(r *ghttp.Request) { response.Success(r, nil) })
			g.POST("/security/2fa/setup", func(r *ghttp.Request) { response.Success(r, nil) })
		})
	})
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Shutdown() })
	return &authFixture{db: db, cfg: cfg, base: fmt.Sprintf("http://127.0.0.1:%d", s.GetListenedPort()), auth: auth}
}

type authHTTPResult struct {
	status int
	header http.Header
	body   []byte
}

func (f *authFixture) call(t *testing.T, method, path, token string, body interface{}) authHTTPResult {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, f.base+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return authHTTPResult{res.StatusCode, res.Header, data}
}
func (r authHTTPResult) expect(t *testing.T, status int) {
	t.Helper()
	if r.status != status {
		t.Fatalf("want status %d got %d: %s", status, r.status, r.body)
	}
}
func loginResult(t *testing.T, r authHTTPResult) *model.LoginResponse {
	t.Helper()
	r.expect(t, 200)
	var out struct {
		Data model.LoginResponse `json:"data"`
	}
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatal(err)
	}
	return &out.Data
}
func (f *authFixture) login(t *testing.T, email string) *model.LoginResponse {
	return loginResult(t, f.call(t, "POST", "/api/v1/auth/login", "", map[string]string{"email": email, "password": "fixture-password"}))
}
func (f *authFixture) key(t *testing.T, session string, scopes []string, rate int) (string, string) {
	t.Helper()
	r := f.call(t, "POST", "/api/v1/api-keys", session, map[string]interface{}{"name": "Fixture key", "permissions": scopes, "rateLimit": rate})
	r.expect(t, 200)
	var out struct {
		Data struct {
			Key  string `json:"key"`
			UUID string `json:"uuid"`
		}
	}
	json.Unmarshal(r.body, &out)
	if out.Data.Key == "" {
		t.Fatal("missing key")
	}
	return out.Data.Key, out.Data.UUID
}
func execAuth(t *testing.T, db *sql.DB, q string, args ...interface{}) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func TestAPIKeyScopeRateExpiryAndTenantHTTP(t *testing.T) {
	f := newAuthFixture(t)
	session := f.login(t, "owner@example.test").Token
	key, id := f.key(t, session, []string{"email:read"}, 100)
	path := "/api/v1/inbox/received/00000000-0000-0000-0000-000000000010"
	f.call(t, "GET", path, key, nil).expect(t, 200)
	f.call(t, "POST", "/api/v1/inbox/received/mark", key, map[string]interface{}{"emailUuids": []string{"00000000-0000-0000-0000-000000000010"}, "isRead": true}).expect(t, 403)
	f.call(t, "POST", "/api/v1/api-keys", key, map[string]interface{}{"name": "Expansion", "permissions": []string{"email:send"}}).expect(t, 403)
	for _, p := range []string{"/api/v1/api-keys", "/api/v1/settings", "/api/v1/auth/me"} {
		f.call(t, "GET", p, key, nil).expect(t, 403)
	}
	f.call(t, "POST", "/api/v1/security/2fa/setup", key, nil).expect(t, 403)
	f.call(t, "GET", "/api/v1/inbox/received/00000000-0000-0000-0000-000000000020", key, nil).expect(t, 404)
	var read bool
	if err := f.db.QueryRow(`SELECT is_read FROM received_emails WHERE identity_id=1`).Scan(&read); err != nil || read {
		t.Fatal("read-only key changed email", err)
	}
	member := f.login(t, "member@example.test").Token
	f.call(t, "POST", "/api/v1/api-keys", member, map[string]interface{}{"name": "Member", "permissions": []string{"email:read"}}).expect(t, 403)
	for _, data := range []map[string]interface{}{{"name": "Invalid", "permissions": []string{"email:read"}, "expiresAt": "invalid"}, {"name": "Invalid", "permissions": []string{"admin:*"}}, {"name": "Invalid", "permissions": []string{}}, {"name": "Invalid", "permissions": []string{"email:read"}, "rateLimit": -1}} {
		f.call(t, "POST", "/api/v1/api-keys", session, data).expect(t, 400)
	}
	limited, lid := f.key(t, session, []string{"email:read"}, 1)
	f.call(t, "GET", path, limited, nil).expect(t, 200)
	rate := f.call(t, "GET", path, limited, nil)
	rate.expect(t, 429)
	retry, err := strconv.Atoi(rate.header.Get("Retry-After"))
	if err != nil || retry < 1 || retry > 60 {
		t.Fatal("missing bounded Retry-After")
	}
	execAuth(t, f.db, `UPDATE api_keys SET request_window=now()-interval '2 minutes' WHERE uuid=$1`, lid)
	f.call(t, "GET", path, limited, nil).expect(t, 200)
	execAuth(t, f.db, `UPDATE api_keys SET expires_at=now()-interval '1 second' WHERE uuid=$1`, id)
	f.call(t, "GET", path, key, nil).expect(t, 401)
	f.call(t, "DELETE", "/api/v1/api-keys/"+lid, session, nil).expect(t, 200)
	f.call(t, "GET", path, limited, nil).expect(t, 401)
}

func TestSessionRevocationDisablePasswordAndStreamHTTP(t *testing.T) {
	f := newAuthFixture(t)
	session := f.login(t, "owner@example.test").Token
	key, _ := f.key(t, session, []string{"email:read"}, 100)
	f.call(t, "GET", "/api/v1/auth/me", session, nil).expect(t, 200)
	f.call(t, "GET", "/api/v1/auth/me?token="+session, "", nil).expect(t, 401)
	f.call(t, "GET", "/api/v1/sse/connect?token="+session, "", nil).expect(t, 401)
	ticketResponse := f.call(t, "POST", "/api/v1/auth/stream-token", session, nil)
	ticketResponse.expect(t, 200)
	var ticket struct {
		Data struct {
			Token string `json:"token"`
		}
	}
	json.Unmarshal(ticketResponse.body, &ticket)
	f.call(t, "GET", "/api/v1/sse/connect?token="+ticket.Data.Token, "", nil).expect(t, 200)
	f.call(t, "GET", "/api/v1/auth/me", ticket.Data.Token, nil).expect(t, 401)
	f.call(t, "GET", "/api/v1/sse/connect?token="+key, "", nil).expect(t, 401)
	execAuth(t, f.db, `UPDATE users SET status='disabled' WHERE id=1`)
	f.call(t, "GET", "/api/v1/auth/me", session, nil).expect(t, 401)
	f.call(t, "GET", "/api/v1/inbox/received/00000000-0000-0000-0000-000000000010", key, nil).expect(t, 401)
	execAuth(t, f.db, `UPDATE users SET status='active' WHERE id=1`)
	f.call(t, "POST", "/api/v1/auth/logout", session, nil).expect(t, 200)
	f.call(t, "GET", "/api/v1/auth/me", session, nil).expect(t, 401)
	f.call(t, "GET", "/api/v1/sse/connect?token="+ticket.Data.Token, "", nil).expect(t, 401)
	newSession := f.login(t, "owner@example.test").Token
	svc := service.NewSessionService(f.db, f.cfg)
	if err := svc.ChangePassword(context.Background(), 1, "fixture-password", "changed-password"); err != nil {
		t.Fatal(err)
	}
	f.call(t, "GET", "/api/v1/auth/me", newSession, nil).expect(t, 401)
}

func TestTwoFactorChallengeOneUseAndLockoutHTTP(t *testing.T) {
	f := newAuthFixture(t)
	recovery := "ABCD-EFGH-1234"
	h := sha256.Sum256([]byte("ABCDEFGH1234"))
	execAuth(t, f.db, `UPDATE users SET totp_enabled=true,totp_secret='JBSWY3DPEHPK3PXP',backup_codes=ARRAY[$1] WHERE id=1`, hex.EncodeToString(h[:]))
	challenged := f.login(t, "owner@example.test")
	if !challenged.RequiresTwoFactor || challenged.Token != "" || challenged.ChallengeToken == "" {
		t.Fatal("password bypassed second factor")
	}
	f.call(t, "GET", "/api/v1/auth/me", challenged.ChallengeToken, nil).expect(t, 401)
	var sessions int
	f.db.QueryRow(`SELECT count(*) FROM user_sessions WHERE user_id=1`).Scan(&sessions)
	if sessions != 0 {
		t.Fatal("challenge created usable session")
	}
	// An enabled account cannot reset its factor through setup.
	if _, err := service.NewTwoFactorService(f.db, f.cfg).GenerateSetup(context.Background(), 1, "owner@example.test"); err == nil {
		t.Fatal("setup disabled active factor")
	}
	var enabled bool
	f.db.QueryRow(`SELECT totp_enabled FROM users WHERE id=1`).Scan(&enabled)
	if !enabled {
		t.Fatal("setup disabled factor")
	}
	verified := loginResult(t, f.call(t, "POST", "/api/v1/auth/2fa/challenge", "", map[string]string{"challengeToken": challenged.ChallengeToken, "code": recovery}))
	if verified.Token == "" {
		t.Fatal("missing verified session")
	}
	f.call(t, "POST", "/api/v1/auth/2fa/challenge", "", map[string]string{"challengeToken": challenged.ChallengeToken, "code": recovery}).expect(t, 401)
	f.call(t, "GET", "/api/v1/auth/me", verified.Token, nil).expect(t, 200)
	locked := f.login(t, "owner@example.test")
	for i := 0; i < 6; i++ {
		f.call(t, "POST", "/api/v1/auth/2fa/challenge", "", map[string]string{"challengeToken": locked.ChallengeToken, "code": "wrong-code"}).expect(t, 401)
	}
	var attempts int
	f.db.QueryRow(`SELECT attempts FROM auth_challenges WHERE consumed_at IS NULL`).Scan(&attempts)
	if attempts != 5 {
		t.Fatalf("unbounded attempts %d", attempts)
	}
	expired := f.login(t, "owner@example.test")
	execAuth(t, f.db, `UPDATE auth_challenges SET expires_at=now()-interval '1 second' WHERE attempts=0 AND consumed_at IS NULL`)
	f.call(t, "POST", "/api/v1/auth/2fa/challenge", "", map[string]string{"challengeToken": expired.ChallengeToken, "code": recovery}).expect(t, 401)
}

func TestAPIKeyConcurrentLimitHTTP(t *testing.T) {
	f := newAuthFixture(t)
	session := f.login(t, "owner@example.test").Token
	key, _ := f.key(t, session, []string{"email:read"}, 3)
	// Avoid a minute rollover in this short concurrency check.
	if time.Now().Second() > 57 {
		t.Skip("run away from the fixed-window boundary")
	}
	var wg sync.WaitGroup
	statuses := make(chan int, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := f.call(t, "GET", "/api/v1/inbox/received/00000000-0000-0000-0000-000000000010", key, nil)
			statuses <- r.status
		}()
	}
	wg.Wait()
	close(statuses)
	allowed, limited := 0, 0
	for status := range statuses {
		switch status {
		case 200:
			allowed++
		case 429:
			limited++
		default:
			t.Fatalf("unexpected status %d", status)
		}
	}
	if allowed != 3 || limited != 7 {
		t.Fatalf("rate race: accepted=%d limited=%d", allowed, limited)
	}
}
