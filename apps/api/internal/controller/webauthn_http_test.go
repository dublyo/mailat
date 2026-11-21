package controller_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/controller"
	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/internal/testutil"
)

func webauthnServer(t *testing.T, enabled bool) func(method, path, body string) (int, string) {
	t.Helper()
	db := testutil.Database(t)
	cfg := &config.Config{JWTSecret: "webauthn-http-fixture-not-a-live-secret", AppDomain: "example.test", AppName: "Mailat", WebAuthnEnabled: enabled}
	ctrl := controller.NewPhase5Controller(service.NewWebAuthnService(db, cfg), nil, nil, nil, nil, service.NewAuditLogService(db, cfg), service.NewRateLimiter(db, cfg))
	s := ghttp.GetServer(fmt.Sprintf("webauthn-http-%v-%d", enabled, time.Now().UnixNano()))
	s.SetAddr("127.0.0.1:0")
	s.SetDumpRouterMap(false)
	s.SetAccessLogEnabled(false)
	s.Group("/api/v1", func(g *ghttp.RouterGroup) {
		g.Middleware(func(r *ghttp.Request) {
			r.SetCtx(context.WithValue(r.Context(), middleware.ClaimsContextKey, &model.JWTClaims{UserID: 7, OrgID: 1, Email: "user@example.test"}))
			r.Middleware.Next()
		})
		g.POST("/security/webauthn/register/begin", ctrl.BeginWebAuthnRegistration)
		g.POST("/security/webauthn/register/finish", ctrl.FinishWebAuthnRegistration)
		g.POST("/security/webauthn/authenticate/begin", ctrl.BeginWebAuthnAuthentication)
		g.POST("/security/webauthn/authenticate/finish", ctrl.FinishWebAuthnAuthentication)
		g.GET("/security/webauthn/credentials", ctrl.ListWebAuthnCredentials)
	})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Shutdown() })
	base := fmt.Sprintf("http://127.0.0.1:%d/api/v1", s.GetListenedPort())
	return func(method, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
}

// Off by default: every ceremony answers 404 while the credential list works.
func TestWebAuthnDisabledHTTP(t *testing.T) {
	do := webauthnServer(t, false)
	for _, path := range []string{"/security/webauthn/register/begin", "/security/webauthn/register/finish", "/security/webauthn/authenticate/begin", "/security/webauthn/authenticate/finish"} {
		// More calls than the begin limit: a disabled server never counts them.
		for i := 0; i <= service.RuleWebAuthnBeginUser.Limit; i++ {
			status, body := do(http.MethodPost, path, `{}`)
			if status != 404 || !strings.Contains(body, "Passkeys are not available on this server yet") {
				t.Fatalf("%s: %d %s", path, status, body)
			}
		}
	}
	if status, body := do(http.MethodGet, "/security/webauthn/credentials", ""); status != 200 {
		t.Fatalf("credentials: %d %s", status, body)
	}
}

// Enabled, begin is limited per user across both ceremonies.
func TestWebAuthnBeginRateLimitedHTTP(t *testing.T) {
	do := webauthnServer(t, true)
	for i := 0; i < service.RuleWebAuthnBeginUser.Limit; i++ {
		if status, body := do(http.MethodPost, "/security/webauthn/register/begin", `{}`); status != 200 {
			t.Fatalf("begin %d: %d %s", i+1, status, body)
		}
	}
	for _, path := range []string{"/security/webauthn/register/begin", "/security/webauthn/authenticate/begin"} {
		if status, body := do(http.MethodPost, path, `{}`); status != 429 {
			t.Fatalf("%s not limited: %d %s", path, status, body)
		}
	}
}
