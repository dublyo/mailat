package middleware

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/gogf/gf/v2/net/ghttp"
)

func clientIPFor(peer, forwarded string) string {
	r := httptest.NewRequest("POST", "http://localhost/", nil)
	r.RemoteAddr = peer
	if forwarded != "" {
		r.Header.Set("X-Forwarded-For", forwarded)
	}
	return ClientIP(&ghttp.Request{Request: r})
}

func TestClientIPTrustBoundary(t *testing.T) {
	previous := config.Cfg
	t.Cleanup(func() { config.Cfg = previous })
	nets, err := config.ParseProxyNets("127.0.0.1/32,10.0.0.0/8,::1/128,fd00::/8")
	if err != nil {
		t.Fatal(err)
	}
	config.Cfg = &config.Config{TrustedProxyNets: nets}
	for _, tc := range []struct{ peer, forwarded, want string }{
		{"192.0.2.3:9000", "203.0.113.7", "192.0.2.3"},                             // untrusted peer: header ignored
		{"127.0.0.1:9000", "198.51.100.99, 192.0.2.3, 10.1.1.1", "192.0.2.3"},      // stops at first untrusted hop
		{"127.0.0.1:9000", "198.51.100.99, 192.0.2.3, 203.0.113.9", "203.0.113.9"}, // untrusted middle hop wins over spoofed prefix
		{"127.0.0.1:9000", "garbage, 192.0.2.3", "192.0.2.3"},
		{"127.0.0.1:9000", "", "127.0.0.1"},
		{"127.0.0.1:9000", "10.0.0.5, 10.0.0.6", "10.0.0.5"}, // whole chain trusted: leftmost valid hop
		{"[::1]:9000", "2001:db8::7", "2001:db8::7"},         // IPv6 peer and client
		{"[fd00::2]:443", "2001:db8::1, fd00::9", "2001:db8::1"},
		{"[2001:db8::5]:443", "203.0.113.7", "2001:db8::5"},
		{"192.0.2.3", "203.0.113.7", "192.0.2.3"}, // peer without a port
	} {
		if got := clientIPFor(tc.peer, tc.forwarded); got != tc.want {
			t.Errorf("peer=%s xff=%q got %s want %s", tc.peer, tc.forwarded, got, tc.want)
		}
	}
}

func TestClientIPJoinsMultipleForwardedHeaders(t *testing.T) {
	previous := config.Cfg
	t.Cleanup(func() { config.Cfg = previous })
	config.Cfg = &config.Config{TrustedProxyCIDRs: "10.0.0.0/8"}
	// The visitor's forged line comes first; the proxy adds the real address
	// on its own line.
	r := httptest.NewRequest("POST", "http://localhost/", nil)
	r.RemoteAddr = "10.0.0.2:4000"
	r.Header.Add("X-Forwarded-For", "1.2.3.4")
	r.Header.Add("X-Forwarded-For", "203.0.113.50")
	if got := ClientIP(&ghttp.Request{Request: r}); got != "203.0.113.50" {
		t.Fatalf("got %s want the proxy-reported address", got)
	}
}

func TestClientIPTrustedNetworkFallbacks(t *testing.T) {
	previous := config.Cfg
	t.Cleanup(func() { config.Cfg = previous })

	// Directly built configs (tests, tools) use the raw CIDR string.
	config.Cfg = &config.Config{TrustedProxyCIDRs: "10.0.0.0/8"}
	if got := clientIPFor("10.2.3.4:1", "203.0.113.7"); got != "203.0.113.7" {
		t.Fatalf("raw CIDR fallback: got %s", got)
	}
	if got := clientIPFor("127.0.0.1:1", "203.0.113.7"); got != "127.0.0.1" {
		t.Fatalf("loopback must not be trusted when CIDRs exclude it: got %s", got)
	}
	// Nothing configured: loopback only.
	for _, cfg := range []*config.Config{nil, {}} {
		config.Cfg = cfg
		if got := clientIPFor("127.0.0.1:1", "203.0.113.7"); got != "203.0.113.7" {
			t.Fatalf("loopback default: got %s", got)
		}
		if got := clientIPFor("10.2.3.4:1", "203.0.113.7"); got != "10.2.3.4" {
			t.Fatalf("private peer is untrusted by default: got %s", got)
		}
	}
}

func TestSessionHash(t *testing.T) {
	if got := SessionHash(context.Background()); got != "" {
		t.Fatalf("unauthenticated: %q", got)
	}
	jwtCtx := context.WithValue(context.Background(), credentialContextKey, credential{SessionHash: "abc"})
	if got := SessionHash(jwtCtx); got != "abc" {
		t.Fatalf("session: %q", got)
	}
	keyCtx := context.WithValue(context.Background(), credentialContextKey, credential{KeyID: 7})
	if got := SessionHash(keyCtx); got != "" {
		t.Fatalf("api key: %q", got)
	}
}
