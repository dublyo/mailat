package config

import (
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestNormalizeRedisURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{{"redis://", "redis://"}, {"rediss://host:6380", "rediss://host:6380"}, {"host:6379", "redis://host:6379"}} {
		if got := normalizeRedisURL(tc.in); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
	}
}

func validConfig() *Config {
	return &Config{
		JWTSecret:         "0123456789abcdef0123456789abcdef-jwt",
		EncryptionKey:     "0123456789abcdef0123456789abcdef-enc",
		JWTExpiresIn:      "7d",
		TrustedProxyCIDRs: "127.0.0.1/32,::1/128",
		EmailProvider:     "ses",
	}
}

func TestValidate(t *testing.T) {
	const jwtValue = "0123456789abcdef0123456789abcdef-jwt"
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string // substring of the error; empty means valid
	}{
		{"valid", func(*Config) {}, ""},
		{"valid smtp with origins", func(c *Config) {
			c.EmailProvider = "smtp"
			c.CORSOrigins = []string{"https://app.example.com", "http://localhost:5173"}
		}, ""},
		{"empty jwt", func(c *Config) { c.JWTSecret = "" }, "JWT_SECRET is required"},
		{"empty encryption", func(c *Config) { c.EncryptionKey = "" }, "ENCRYPTION_KEY is required"},
		{"short jwt", func(c *Config) { c.JWTSecret = "short" }, "JWT_SECRET must be at least 32 bytes"},
		{"equal secrets", func(c *Config) { c.EncryptionKey = c.JWTSecret }, "must be different"},
		{"placeholder", func(c *Config) { c.JWTSecret = "replace-with-a-long-random-jwt-secret" }, "JWT_SECRET still holds"},
		{"placeholder case", func(c *Config) { c.EncryptionKey = "CHANGEME-0123456789abcdef0123456789" }, "ENCRYPTION_KEY still holds"},
		{"bad duration", func(c *Config) { c.JWTExpiresIn = "abc" }, "JWT_EXPIRES_IN"},
		{"bad cidr", func(c *Config) { c.TrustedProxyCIDRs = "127.0.0.1/32,not-a-net" }, "TRUSTED_PROXY_CIDRS entry 2"},
		{"bad origin path", func(c *Config) { c.CORSOrigins = []string{"https://app.example.com/path"} }, "CORS_ORIGINS entry 1"},
		{"bad origin scheme", func(c *Config) { c.CORSOrigins = []string{"ftp://app.example.com"} }, "CORS_ORIGINS"},
		{"bad origin bare host", func(c *Config) { c.CORSOrigins = []string{"app.example.com"} }, "CORS_ORIGINS"},
		{"bad provider", func(c *Config) { c.EmailProvider = "sendgrid" }, "EMAIL_PROVIDER"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfig()
			tt.mutate(c)
			err := c.Validate()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %v, want substring %q", err, tt.want)
			}
			if strings.Contains(err.Error(), jwtValue) || (c.JWTSecret != "" && strings.Contains(err.Error(), c.JWTSecret)) {
				t.Fatalf("error leaks a secret value: %v", err)
			}
		})
	}
	if err := (&Config{JWTSecret: "x"}).Validate(); err == nil || !strings.Contains(err.Error(), "openssl rand -hex 32") {
		t.Fatalf("secret errors should carry the generation hint: %v", err)
	}
}

// Every JWT_SECRET/ENCRYPTION_KEY example an operator might copy must fail.
func TestDocumentedSecretExamplesFailValidate(t *testing.T) {
	assign := regexp.MustCompile(`(?m)^\s*(JWT_SECRET|ENCRYPTION_KEY)\s*=\s*"?([^"\s$]+)"?`)
	found := 0
	for _, file := range []string{"README.md", ".env.example", ".env.production.example", "docs/self-hosting-ses.md"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", file))
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for _, m := range assign.FindAllStringSubmatch(string(data), -1) {
			found++
			c := validConfig()
			if m[1] == "JWT_SECRET" {
				c.JWTSecret = m[2]
			} else {
				c.EncryptionKey = m[2]
			}
			if c.Validate() == nil {
				t.Errorf("%s: documented %s example passes Validate()", file, m[1])
			}
		}
	}
	if found < 4 {
		t.Fatalf("found only %d documented examples; the pattern no longer matches the docs", found)
	}
	// Older README examples that were once published.
	for _, v := range []string{"your-jwt-secret-min-32-chars", "a-different-random-value-min-32-chars"} {
		c := validConfig()
		c.EncryptionKey = v
		if c.Validate() == nil {
			t.Errorf("%q passes Validate()", v)
		}
	}
}

func TestLoadDefaultsAndValidation(t *testing.T) {
	previous := Cfg
	t.Cleanup(func() { Cfg = previous })
	for _, k := range []string{"EMAIL_PROVIDER", "JWT_EXPIRES_IN", "CORS_ORIGINS", "TRUSTED_PROXY_CIDRS"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("JWT_SECRET", "0123456789abcdef0123456789abcdef-jwt")
	t.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef-enc")
	t.Setenv("CORS_ORIGINS", " https://a.example.com/ , http://localhost:5173 ")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EmailProvider != "ses" || cfg.SessionTTL != 7*24*time.Hour || len(cfg.TrustedProxyNets) != 2 || Cfg != cfg {
		t.Fatalf("defaults: provider=%q ttl=%s nets=%d", cfg.EmailProvider, cfg.SessionTTL, len(cfg.TrustedProxyNets))
	}
	if want := []string{"https://a.example.com", "http://localhost:5173"}; !reflect.DeepEqual(cfg.CORSOrigins, want) {
		t.Fatalf("origins %v", cfg.CORSOrigins)
	}
	t.Setenv("JWT_SECRET", "short")
	if cfg, err := Load(); err == nil || cfg != nil || strings.Contains(err.Error(), "short") {
		t.Fatalf("weak secret should fail without echoing it: cfg=%v err=%v", cfg, err)
	}
}

func TestParseSessionDuration(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"7d", 7 * 24 * time.Hour, true}, {"30d", 30 * 24 * time.Hour, true}, {"168h", 168 * time.Hour, true},
		{"1h", time.Hour, true}, {"90d", 90 * 24 * time.Hour, true},
		{"0d", 0, false}, {"91d", 0, false}, {"abc", 0, false}, {"30m", 0, false}, {"", 0, false}, {"-1d", 0, false},
	} {
		got, err := ParseSessionDuration(tt.in)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("%q: got %s err=%v", tt.in, got, err)
		}
	}
}

func TestParseProxyNets(t *testing.T) {
	nets, err := ParseProxyNets(" 10.0.0.0/8 , 192.0.2.1, ::1 ")
	if err != nil || len(nets) != 3 {
		t.Fatalf("nets=%v err=%v", nets, err)
	}
	if !nets[1].Contains(net.ParseIP("192.0.2.1")) || nets[1].Contains(net.ParseIP("192.0.2.2")) || !nets[2].Contains(net.ParseIP("::1")) {
		t.Fatalf("bare IPs should be single-host networks: %v", nets)
	}
	if nets, err := ParseProxyNets(""); err != nil || nets != nil {
		t.Fatalf("empty: %v %v", nets, err)
	}
}
