package controller_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/controller"
	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/internal/testutil"
)

// noRedirect keeps 302s visible so the test can inspect Location and cookies.
var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func TestOAuthLoginLinkAndConfirmHTTP(t *testing.T) {
	db := testutil.Database(t)
	cfg := &config.Config{JWTSecret: "oauth-http-fixture-not-a-live-secret", EmailProvider: "ses", WebUrl: "http://web.test", APIUrl: "http://api.test", GoogleClientID: "client", GoogleClientSecret: "secret"}
	oldDB, oldCfg := database.DB, config.Cfg
	database.DB, config.Cfg = db, cfg
	t.Cleanup(func() { database.DB, config.Cfg = oldDB, oldCfg })
	hash, _ := bcrypt.GenerateFromPassword([]byte("fixture-password"), bcrypt.MinCost)
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'One','one',now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,org_id,email,password_hash,name,role,updated_at) VALUES(1,1,'owner@example.test',$1,'Owner','owner',now()),(2,1,'member@example.test',$1,'Member','member',now())`, string(hash)); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	profile := `{}`
	setProfile := func(p string) { mu.Lock(); profile = p; mu.Unlock() }
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			w.Write([]byte(`{"access_token":"stub-access","refresh_token":"stub-refresh","expires_in":3600}`))
		case "/userinfo":
			mu.Lock()
			defer mu.Unlock()
			w.Write([]byte(profile))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(stub.Close)
	oauth := service.NewOAuthService(db, cfg)
	google := oauth.ProviderConfig(service.ProviderGoogle)
	google.AuthURL, google.TokenURL, google.UserInfoURL = stub.URL+"/auth", stub.URL+"/token", stub.URL+"/userinfo"
	ctrl := controller.NewOAuthController(oauth, service.NewAuditLogService(db, cfg), cfg, service.NewRateLimiter(db, cfg))
	auth := controller.NewAuthController(service.NewAuthService(db, cfg), nil)

	s := ghttp.GetServer(fmt.Sprintf("oauth-http-%d", time.Now().UnixNano()))
	s.SetAddr("127.0.0.1:0")
	s.SetDumpRouterMap(false)
	s.SetAccessLogEnabled(false)
	s.Group("/api/v1", func(g *ghttp.RouterGroup) {
		g.POST("/auth/login", auth.Login)
		g.GET("/oauth/:provider", ctrl.InitiateOAuth)
		g.GET("/oauth/:provider/callback", ctrl.HandleCallback)
		g.Group("/", func(g *ghttp.RouterGroup) {
			g.Middleware(middleware.Auth)
			g.POST("/oauth/:provider/connect", ctrl.ConnectProvider)
			g.POST("/oauth/link/confirm", ctrl.ConfirmLink)
		})
	})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Shutdown() })
	base := fmt.Sprintf("http://127.0.0.1:%d", s.GetListenedPort())

	get := func(path, cookie string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("GET", base+path, nil)
		if cookie != "" {
			req.Header.Set("Cookie", "oauth_state="+cookie)
		}
		res, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	post := func(path, token string, body any) (int, []byte) {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", base+path, bytes.NewReader(b))
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
		return res.StatusCode, data
	}
	session := func(email string) string {
		t.Helper()
		status, body := post("/api/v1/auth/login", "", map[string]string{"email": email, "password": "fixture-password"})
		var out struct{ Data struct{ Token string } }
		json.Unmarshal(body, &out)
		if status != 200 || out.Data.Token == "" {
			t.Fatalf("login %s: %d %s", email, status, body)
		}
		return out.Data.Token
	}
	// startLogin returns the state from the provider redirect and checks the cookie.
	startLogin := func() string {
		t.Helper()
		res := get("/api/v1/oauth/google", "")
		loc, _ := url.Parse(res.Header.Get("Location"))
		state := loc.Query().Get("state")
		if res.StatusCode != 302 || !strings.HasPrefix(loc.String(), stub.URL+"/auth") || state == "" || loc.Query().Get("access_type") != "" {
			t.Fatalf("start: %d %s", res.StatusCode, loc)
		}
		cookie := res.Header.Get("Set-Cookie")
		for _, part := range []string{"oauth_state=" + state, "Path=/api/v1/oauth", "HttpOnly", "SameSite=Lax", "Max-Age=600"} {
			if !strings.Contains(cookie, part) {
				t.Fatalf("state cookie missing %s: %s", part, cookie)
			}
		}
		return state
	}
	wantError := func(res *http.Response, code string) {
		t.Helper()
		if want := "http://web.test/login?oauthError=" + code; res.StatusCode != 302 || res.Header.Get("Location") != want {
			t.Fatalf("got %d %q want %q", res.StatusCode, res.Header.Get("Location"), want)
		}
	}
	connections := func() int {
		var n int
		db.QueryRow(`SELECT count(*) FROM oauth_connections`).Scan(&n)
		return n
	}

	// Login without the browser-bound cookie fails.
	state := startLogin()
	wantError(get("/api/v1/oauth/google/callback?code=c&state="+state, ""), "invalid_state")
	// A verified identity whose email matches an existing user is not linked.
	setProfile(`{"id":"g-owner","email":"owner@example.test","verified_email":true,"name":"Owner"}`)
	state = startLogin()
	wantError(get("/api/v1/oauth/google/callback?code=c&state="+state, state), "not_linked")
	if connections() != 0 {
		t.Fatal("email match created a connection")
	}
	// The state is single use.
	wantError(get("/api/v1/oauth/google/callback?code=c&state="+state, state), "invalid_state")
	// Provider-side errors are reduced to a fixed code.
	state = startLogin()
	wantError(get("/api/v1/oauth/google/callback?error=access_denied&error_description=%3Cscript%3E&state="+state, state), "provider_error")

	owner, member := session("owner@example.test"), session("member@example.test")
	connect := func(token string) string {
		t.Helper()
		status, body := post("/api/v1/oauth/google/connect", token, nil)
		var out struct{ Data struct{ AuthURL string } }
		json.Unmarshal(body, &out)
		loc, _ := url.Parse(out.Data.AuthURL)
		if status != 200 || loc == nil || loc.Query().Get("state") == "" {
			t.Fatalf("connect: %d %s", status, body)
		}
		res := get("/api/v1/oauth/google/callback?code=c&state="+loc.Query().Get("state"), "")
		target, _ := url.Parse(res.Header.Get("Location"))
		if res.StatusCode != 302 || target.Host != "web.test" || target.Path != "/settings" || target.Query().Get("tab") != "security" || target.Query().Get("oauthLink") == "" {
			t.Fatalf("link callback: %d %s", res.StatusCode, target)
		}
		return target.Query().Get("oauthLink")
	}
	errorCode := func(body []byte) string {
		var out struct{ Data struct{ Error string } }
		json.Unmarshal(body, &out)
		return out.Data.Error
	}
	// A ticket confirmed by a different signed-in user is refused (link CSRF).
	ticket := connect(owner)
	if status, body := post("/api/v1/oauth/link/confirm", member, map[string]string{"ticket": ticket}); status != 403 || errorCode(body) != "link_mismatch" || connections() != 0 {
		t.Fatalf("mismatch: %d %s", status, body)
	}
	ticket = connect(owner)
	if status, body := post("/api/v1/oauth/link/confirm", owner, map[string]string{"ticket": ticket}); status != 200 || !strings.Contains(string(body), `"provider":"google"`) {
		t.Fatalf("confirm: %d %s", status, body)
	}
	var tokens int
	db.QueryRow(`SELECT count(*) FROM oauth_connections WHERE user_id=1 AND access_token IS NULL AND refresh_token IS NULL AND token_expiry IS NULL`).Scan(&tokens)
	if connections() != 1 || tokens != 1 {
		t.Fatal("link must store one connection without provider tokens")
	}
	if status, body := post("/api/v1/oauth/link/confirm", owner, map[string]string{"ticket": ticket}); status != 410 || errorCode(body) != "invalid_ticket" {
		t.Fatalf("ticket replay: %d %s", status, body)
	}
	// The linked identity now signs in; the session goes in the URL fragment.
	state = startLogin()
	res := get("/api/v1/oauth/google/callback?code=c&state="+state, state)
	if res.StatusCode != 302 || !strings.HasPrefix(res.Header.Get("Location"), "http://web.test/login#session=") {
		t.Fatalf("linked login: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if !strings.Contains(res.Header.Get("Set-Cookie"), "Max-Age=0") {
		t.Fatal("state cookie not cleared", res.Header.Get("Set-Cookie"))
	}
}

func TestHealthAndReadyHideDependencyErrorsHTTP(t *testing.T) {
	db := testutil.Database(t)
	oldDB, oldRedis := database.DB, database.Redis
	database.DB = db
	database.Redis = redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond, MaxRetries: -1})
	t.Cleanup(func() { database.Redis.Close(); database.DB, database.Redis = oldDB, oldRedis })
	h := controller.NewHealthController()
	s := ghttp.GetServer(fmt.Sprintf("health-http-%d", time.Now().UnixNano()))
	s.SetAddr("127.0.0.1:0")
	s.SetDumpRouterMap(false)
	s.SetAccessLogEnabled(false)
	s.Group("/api/v1", func(g *ghttp.RouterGroup) {
		g.GET("/health", h.Health)
		g.GET("/ready", h.Ready)
	})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Shutdown() })
	base := fmt.Sprintf("http://127.0.0.1:%d", s.GetListenedPort())
	read := func(path string) (int, []byte) {
		res, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b
	}
	status, body := read("/api/v1/health")
	var health struct {
		Data struct {
			Status  string
			Version string
			Checks  map[string]string
		}
	}
	json.Unmarshal(body, &health)
	// The Mailat.co agent reads data.status and data.version even on 503.
	if status != 503 || health.Data.Status != "unhealthy" || health.Data.Version == "" || health.Data.Checks["redis"] != "unavailable" || health.Data.Checks["postgresql"] != "ok" {
		t.Fatalf("health: %d %s", status, body)
	}
	for _, leak := range []string{"127.0.0.1:1", "refused", "dial", "error"} {
		if strings.Contains(strings.ToLower(string(body)), leak) {
			t.Fatalf("health leaks %q: %s", leak, body)
		}
	}
	status, body = read("/api/v1/ready")
	if status != 503 || !strings.Contains(string(body), `"ready":false`) {
		t.Fatalf("ready: %d %s", status, body)
	}
}
