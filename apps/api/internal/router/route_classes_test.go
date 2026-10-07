package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gogf/gf/v2/net/ghttp"
	"golang.org/x/crypto/bcrypt"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/internal/testutil"
)

const mailboxGateMessage = "Not available for mailbox accounts"

// startRouter builds and starts the real server; routes are bound on start.
func startRouter(t *testing.T, name string, cfg *config.Config) *ghttp.Server {
	t.Helper()
	s := ghttp.GetServer(fmt.Sprintf("%s-%d", name, time.Now().UnixNano()))
	s.SetAddr("127.0.0.1:0")
	s.SetDumpRouterMap(false)
	s.SetAccessLogEnabled(false)
	ctx, cancel := context.WithCancel(context.Background())
	SetupWithContext(ctx, s, cfg)
	if err := s.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	// Cleanups run last-in first-out: stop the server, then its background loops.
	t.Cleanup(cancel)
	t.Cleanup(func() { s.Shutdown() })
	return s
}

// apiRoutes returns the registered /api/v1 handlers keyed like routeClasses.
func apiRoutes(s *ghttp.Server) map[string]ghttp.RouterItem {
	out := map[string]ghttp.RouterItem{}
	for _, r := range s.GetRoutes() {
		if r.Type == ghttp.HandlerTypeHandler && strings.HasPrefix(r.Route, "/api/v1/") {
			out[r.Method+" "+r.Route] = r
		}
	}
	return out
}

func funcPtr(f any) uintptr { return reflect.ValueOf(f).Pointer() }

func hasMiddleware(item ghttp.RouterItem, f any) bool {
	for _, m := range item.Handler.Middleware {
		if funcPtr(m) == funcPtr(f) {
			return true
		}
	}
	return false
}

// hasRoleClosure finds a RequireRole closure by name: inlining gives each
// call site its own code pointer.
func hasRoleClosure(item ghttp.RouterItem) bool {
	for _, m := range item.Handler.Middleware {
		if strings.Contains(runtime.FuncForPC(funcPtr(m)).Name(), "RequireRole") {
			return true
		}
	}
	return false
}

// TestEveryRouteIsClassified fails closed: a new route without a class, or a
// class that disagrees with the route's middleware, fails CI.
func TestEveryRouteIsClassified(t *testing.T) {
	cfg := &config.Config{JWTSecret: "local-route-class-fixture-only", JWTExpiresIn: "24h", EmailProvider: "ses", AWSRegion: "us-east-1", AWSAccessKeyID: "fixture", AWSSecretAccessKey: "fixture"}
	routes := apiRoutes(startRouter(t, "route-classes", cfg))
	if len(routes) < 200 {
		t.Fatalf("only %d routes registered", len(routes))
	}
	mailbox := map[string]bool{}
	for key, item := range routes {
		class, ok := routeClasses[key]
		if !ok {
			t.Errorf("%s has no entry in routeClasses", key)
			continue
		}
		auth, gate := hasMiddleware(item, middleware.Auth), hasMiddleware(item, middleware.MailboxGate)
		orgAdmin, human := hasMiddleware(item, middleware.RequireOrgAdmin), hasRoleClosure(item)
		switch class {
		case "public":
			if auth || len(item.Handler.Middleware) != 0 {
				t.Errorf("%s is public but has middleware", key)
			}
		case "mailbox", "staff", "admin", "humanAdmin":
			if !auth || !gate {
				t.Errorf("%s (%s) lacks Auth or MailboxGate", key, class)
			}
			if wantAdmin := class == "admin" || class == "humanAdmin"; orgAdmin != wantAdmin {
				t.Errorf("%s (%s): RequireOrgAdmin=%v", key, class, orgAdmin)
			}
			if human != (class == "humanAdmin") {
				t.Errorf("%s (%s): RequireRole=%v", key, class, human)
			}
			if class == "mailbox" {
				mailbox[key] = true
			}
		default:
			t.Errorf("%s has unknown class %q", key, class)
		}
	}
	for key := range routeClasses {
		if _, ok := routes[key]; !ok {
			t.Errorf("routeClasses lists %s, which is not registered", key)
		}
	}
	allow := middleware.MailboxRouteKeys()
	got := make([]string, 0, len(mailbox))
	for k := range mailbox {
		got = append(got, k)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, allow) {
		t.Errorf("mailbox class and middleware allowlist differ:\nclass: %v\nallow: %v", got, allow)
	}
}

// fillParams replaces route parameters with values that match nothing.
func fillParams(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, ":") {
			if p == ":id" || strings.HasSuffix(p, "Id") || p == ":ip" {
				parts[i] = "1"
			} else {
				parts[i] = "00000000-0000-0000-0000-000000000000"
			}
		}
	}
	return strings.Join(parts, "/")
}

// A mailbox session reaches only allowlisted routes; members keep their
// staff routes; keys owned by a mailbox user are rejected outright.
func TestMailboxRoleMatrixHTTP(t *testing.T) {
	db := testutil.Database(t)
	cfg := &config.Config{JWTSecret: "local-mailbox-matrix-fixture-only", JWTExpiresIn: "24h", EmailProvider: "ses", AWSRegion: "us-east-1", AWSAccessKeyID: "fixture", AWSSecretAccessKey: "fixture", DisableAppLimits: true}
	previousDB, previousCfg := database.DB, config.Cfg
	database.DB, config.Cfg = db, cfg
	t.Cleanup(func() { database.DB, config.Cfg = previousDB, previousCfg })
	password, _ := bcrypt.GenerateFromPassword([]byte("mailbox-matrix-password"), bcrypt.MinCost)
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Boxes','boxes',now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,org_id,email,password_hash,name,role,updated_at) VALUES
			(1,1,'owner@boxes.test',$1,'Owner','owner',now()),(2,1,'member@boxes.test',$1,'Member','member',now()),(3,1,'box@boxes.test',$1,'Box','mailbox',now())`, string(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO domains(id,org_id,name,status,verification_token,ses_verified,updated_at) VALUES(1,1,'boxes.test','active','token',true,now());
		INSERT INTO identities(id,user_id,domain_id,email,can_send,updated_at) VALUES
			(1,1,1,'owner@boxes.test',true,now()),(2,2,1,'member@boxes.test',true,now()),(3,3,1,'box@boxes.test',true,now());
		INSERT INTO mailbox_accounts(user_id,org_id,identity_id,domain_id) VALUES(3,1,3,1);
		SELECT setval(pg_get_serial_sequence('users','id'),100);`); err != nil {
		t.Fatal(err)
	}
	s := startRouter(t, "mailbox-matrix", cfg)
	base := fmt.Sprintf("http://127.0.0.1:%d/api/v1", s.GetListenedPort())
	type result struct {
		status  int
		body    string
		timeout bool
	}
	call := func(method, path, token string) result {
		t.Helper()
		// SSE streams stay open once past the gate; a timeout means it passed.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader([]byte("{}")))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return result{timeout: true}
			}
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		if err != nil && errors.Is(err, context.DeadlineExceeded) {
			return result{status: res.StatusCode, timeout: true}
		}
		return result{status: res.StatusCode, body: string(b)}
	}
	login := func(email string) string {
		t.Helper()
		data, _ := json.Marshal(map[string]string{"email": email, "password": "mailbox-matrix-password"})
		res, err := http.Post(base+"/auth/login", "application/json", bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out struct {
			Data model.LoginResponse `json:"data"`
		}
		if err = json.NewDecoder(res.Body).Decode(&out); err != nil || res.StatusCode != 200 || out.Data.Token == "" {
			t.Fatalf("login %s: %d %v", email, res.StatusCode, err)
		}
		return out.Data.Token
	}
	box, member := login("box@boxes.test"), login("member@boxes.test")

	keys := make([]string, 0, len(routeClasses))
	for k := range routeClasses {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// These end the caller's session, so they run last.
	sessionEnders := map[string]bool{"POST /api/v1/auth/logout": true, "POST /api/v1/auth/sessions/revoke-all": true, "POST /api/v1/security/sessions/revoke-all": true}
	var last []string
	for _, key := range keys {
		class := routeClasses[key]
		if class == "public" {
			continue
		}
		if sessionEnders[key] {
			last = append(last, key)
			continue
		}
		method, path, _ := strings.Cut(key, " ")
		path = fillParams(strings.TrimPrefix(path, "/api/v1"))
		got := call(method, path, box)
		gated := got.status == 403 && strings.Contains(got.body, mailboxGateMessage)
		if class == "mailbox" && (gated || got.status == 401) {
			t.Errorf("mailbox session refused on allowlisted %s: %d %s", key, got.status, got.body)
		}
		if class != "mailbox" && !gated {
			t.Errorf("mailbox session reached %s (%s): %d %s", key, class, got.status, got.body)
		}
		if class == "staff" {
			// Members keep every staff route; the gate never applies to them.
			m := call(method, path, member)
			if m.status == 401 || strings.Contains(m.body, mailboxGateMessage) || (m.status == 403 && !memberForbiddenByController[key]) {
				t.Errorf("member refused on staff %s: %d %s", key, m.status, m.body)
			}
		}
	}
	for _, key := range last {
		method, path, _ := strings.Cut(key, " ")
		if got := call(method, strings.TrimPrefix(path, "/api/v1"), box); got.status == 403 || got.status == 401 {
			t.Errorf("mailbox session refused on %s: %d %s", key, got.status, got.body)
		}
		box = login("box@boxes.test")
	}

	// A raw path variant still resolves to the same route and stays gated.
	if got := call("GET", "/campaigns/", box); got.status != 403 || !strings.Contains(got.body, mailboxGateMessage) {
		t.Errorf("trailing slash: %d %s", got.status, got.body)
	}

	// A key whose owner became a mailbox user is rejected at authentication.
	key, err := service.NewAuthService(db, cfg).CreateAPIKey(context.Background(), 1, 1, &model.CreateApiKeyRequest{Name: "Owner key", Permissions: []string{"email:read", "identities:read"}, RateLimit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if got := call("GET", "/identities", key.Key); got.status != 200 {
		t.Fatalf("owner key: %d %s", got.status, got.body)
	}
	if _, err = db.Exec(`UPDATE users SET role='mailbox' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/identities", "/inbox/received"} {
		if got := call("GET", path, key.Key); got.status != 403 || !strings.Contains(got.body, mailboxGateMessage) {
			t.Errorf("mailbox-owned key on %s: %d %s", path, got.status, got.body)
		}
	}
}

// Staff routes whose controllers refuse members on purpose.
var memberForbiddenByController = map[string]bool{
	"GET /api/v1/security/audit-logs": true,
	"GET /api/v1/security/events":     true,
	"PUT /api/v1/campaign-settings":   true,
}
