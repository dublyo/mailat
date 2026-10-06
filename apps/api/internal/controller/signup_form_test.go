package controller

import (
	"github.com/dublyo/mailat/api/internal/config"
	"github.com/gogf/gf/v2/net/ghttp"
	"net/http/httptest"
	"testing"
)

func TestSignupIPTrustBoundary(t *testing.T) {
	previous := config.Cfg
	config.Cfg = &config.Config{TrustedProxyCIDRs: "127.0.0.1/32,10.0.0.0/8"}
	t.Cleanup(func() { config.Cfg = previous })
	for _, tc := range []struct{ peer, forwarded, want string }{
		{"192.0.2.3:9000", "203.0.113.7", "192.0.2.3"},
		{"127.0.0.1:9000", "198.51.100.99, 192.0.2.3, 10.1.1.1", "192.0.2.3"},
		{"127.0.0.1:9000", "garbage, 192.0.2.3", "192.0.2.3"},
		{"127.0.0.1:9000", "", "127.0.0.1"},
	} {
		r := httptest.NewRequest("POST", "http://localhost/", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Forwarded-For", tc.forwarded)
		if got := signupIP(&ghttp.Request{Request: r}); got != tc.want {
			t.Fatalf("%+v got %s", tc, got)
		}
	}
}
