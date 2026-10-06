package middleware

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/gogf/gf/v2/net/ghttp"
)

// startCORSServer runs the middleware in a real GoFrame server so preflight
// routing (OPTIONS resolved against the requested method) is exercised too.
func startCORSServer(t *testing.T) string {
	t.Helper()
	previous := config.Cfg
	t.Cleanup(func() { config.Cfg = previous })
	config.Cfg = &config.Config{
		WebUrl:      "https://app.example.com/",
		CORSOrigins: []string{"https://admin.example.org", "http://localhost:5173"},
	}
	s := ghttp.GetServer(fmt.Sprintf("cors-%d", time.Now().UnixNano()))
	s.SetAddr("127.0.0.1:0")
	s.SetDumpRouterMap(false)
	s.SetAccessLogEnabled(false)
	s.Use(CORS)
	s.Group("/api/v1", func(g *ghttp.RouterGroup) {
		ok := func(r *ghttp.Request) { r.Response.Write("ok") }
		g.GET("/contacts", ok)
		g.POST("/contacts", ok)
		g.POST("/public/forms/:uuid/submit", ok)
		g.GET("/tracking/open/:token", ok)
	})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Shutdown() })
	return fmt.Sprintf("http://127.0.0.1:%d", s.GetListenedPort())
}

func corsRequest(t *testing.T, method, url, origin, preflightMethod string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if preflightMethod != "" {
		req.Header.Set("Access-Control-Request-Method", preflightMethod)
		req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestCORSAllowlist(t *testing.T) {
	base := startCORSServer(t)

	for _, tc := range []struct {
		name, method, path, origin, preflight string
		wantStatus                            int
		wantACAO                              string
		wantVary                              bool
	}{
		{"web origin reflected", "GET", "/api/v1/contacts", "https://app.example.com", "", 200, "https://app.example.com", true},
		{"listed origin reflected", "GET", "/api/v1/contacts", "http://localhost:5173", "", 200, "http://localhost:5173", true},
		{"foreign origin refused", "GET", "/api/v1/contacts", "https://evil.example.net", "", 200, "", true},
		{"lookalike origin refused", "GET", "/api/v1/contacts", "https://app.example.com.evil.net", "", 200, "", true},
		{"no origin", "GET", "/api/v1/contacts", "", "", 200, "", true},
		{"public form wildcard", "POST", "/api/v1/public/forms/abc/submit", "https://blog.example.net", "", 200, "*", false},
		{"tracking wildcard", "GET", "/api/v1/tracking/open/tok", "https://webmail.example.net", "", 200, "*", false},
		{"allowed preflight", "OPTIONS", "/api/v1/contacts", "https://admin.example.org", "POST", 204, "https://admin.example.org", true},
		{"foreign preflight", "OPTIONS", "/api/v1/contacts", "https://evil.example.net", "POST", 204, "", true},
		{"public preflight", "OPTIONS", "/api/v1/public/forms/abc/submit", "https://blog.example.net", "POST", 204, "*", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := corsRequest(t, tc.method, base+tc.path, tc.origin, tc.preflight)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if got := resp.Header.Get("Access-Control-Allow-Origin"); got != tc.wantACAO {
				t.Fatalf("ACAO %q, want %q", got, tc.wantACAO)
			}
			if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "" {
				t.Fatalf("credentials must never be allowed, got %q", got)
			}
			if vary := resp.Header.Values("Vary"); tc.wantVary != containsFold(vary, "Origin") {
				t.Fatalf("Vary %v, want Origin=%v", vary, tc.wantVary)
			}
			if tc.wantACAO != "" {
				if got := resp.Header.Get("Access-Control-Allow-Headers"); got != corsAllowHeaders {
					t.Fatalf("allow headers %q", got)
				}
				if got := resp.Header.Get("Access-Control-Expose-Headers"); got != "Retry-After" {
					t.Fatalf("expose headers %q", got)
				}
			}
		})
	}
}

func containsFold(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
