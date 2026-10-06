package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	// Server
	Port              int
	Env               string
	APIUrl            string
	WebUrl            string
	AppDomain         string // Domain for WebAuthn and email (e.g., "mailat.co")
	TrustedProxyCIDRs string // Explicit networks allowed to supply the client-IP forwarding chain.
	AppName           string // Application name for branding (e.g., "Mailat")

	// Derived from the raw settings above by Load after validation.
	TrustedProxyNets []*net.IPNet
	CORSOrigins      []string      // CORS_ORIGINS: extra browser origins allowed besides WEB_URL
	SessionTTL       time.Duration // parsed JWT_EXPIRES_IN

	// Database
	DatabaseURL string

	// Redis
	RedisURL      string
	RedisPassword string

	// Typesense
	TypesenseURL    string
	TypesenseAPIKey string

	// Stalwart
	StalwartURL        string
	StalwartAdminToken string

	// JWT
	JWTSecret    string
	JWTExpiresIn string

	// Encryption
	EncryptionKey string

	// Worker
	WorkerEnabled    bool
	AutoMigrate      bool
	DisableAppLimits bool

	// Email Provider ("smtp" or "ses")
	EmailProvider string

	// SMTP (for sending emails - used when EmailProvider is "smtp")
	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPassword string
	SMTPFromName string
	SMTPTLS      bool

	// AWS SES (used when EmailProvider is "ses")
	AWSRegion           string
	AWSAccessKeyID      string
	AWSSecretAccessKey  string
	SESConfigurationSet string

	// OAuth2 Providers (Phase 5.3)
	GoogleClientID        string
	GoogleClientSecret    string
	GitHubClientID        string
	GitHubClientSecret    string
	MicrosoftClientID     string
	MicrosoftClientSecret string

	// Organization Limits (configurable defaults per org)
	DefaultMaxDomains        int
	DefaultMonthlyEmailLimit int
	DefaultMaxIdentities     int
	DefaultMaxContacts       int
}

var Cfg *Config

func Load() (*Config, error) {
	// Load .env file from project root
	godotenv.Load("../../.env")

	port, _ := strconv.Atoi(getEnv("PORT", "3001"))
	smtpPort, _ := strconv.Atoi(getEnv("SMTP_PORT", "587"))
	smtpTLS, _ := strconv.ParseBool(getEnv("SMTP_TLS", "true"))

	workerEnabled, _ := strconv.ParseBool(getEnv("WORKER_ENABLED", "true"))
	autoMigrate, _ := strconv.ParseBool(getEnv("AUTO_MIGRATE", "true"))
	disableAppLimits, _ := strconv.ParseBool(getEnv("DISABLE_APP_LIMITS", "true"))

	// Organization limits
	defaultMaxDomains, _ := strconv.Atoi(getEnv("DEFAULT_MAX_DOMAINS", "0"))
	defaultMonthlyEmailLimit, _ := strconv.Atoi(getEnv("DEFAULT_MONTHLY_EMAIL_LIMIT", "0"))
	defaultMaxIdentities, _ := strconv.Atoi(getEnv("DEFAULT_MAX_IDENTITIES", "0"))
	defaultMaxContacts, _ := strconv.Atoi(getEnv("DEFAULT_MAX_CONTACTS", "0"))

	cfg := &Config{
		// Server
		Port:              port,
		TrustedProxyCIDRs: getEnv("TRUSTED_PROXY_CIDRS", "127.0.0.1/32,::1/128"),
		Env:               getEnv("NODE_ENV", "development"),
		APIUrl:            getEnv("API_URL", "http://localhost:3001"),
		WebUrl:            getEnv("WEB_URL", "http://localhost:3000"),
		AppDomain:         getEnv("APP_DOMAIN", "localhost"),
		AppName:           getEnv("APP_NAME", "Mailat"),

		// Database
		DatabaseURL: getEnv("DATABASE_URL", ""),

		// Redis - pass full URL to redis.ParseURL
		RedisURL:      normalizeRedisURL(getEnv("REDIS_URL", "redis://localhost:6379")),
		RedisPassword: getEnv("REDIS_PASSWORD", ""),

		// Typesense
		TypesenseURL:    getEnv("TYPESENSE_URL", "http://localhost:8108"),
		TypesenseAPIKey: getEnv("TYPESENSE_API_KEY", ""),

		// Stalwart
		StalwartURL:        getEnv("STALWART_URL", "http://localhost:8080"),
		StalwartAdminToken: getEnv("STALWART_ADMIN_TOKEN", ""),

		// JWT
		JWTSecret:    getEnv("JWT_SECRET", ""),
		JWTExpiresIn: getEnv("JWT_EXPIRES_IN", "7d"),

		// Encryption
		EncryptionKey: getEnv("ENCRYPTION_KEY", ""),

		// Worker
		WorkerEnabled:    workerEnabled,
		AutoMigrate:      autoMigrate,
		DisableAppLimits: disableAppLimits,

		// Email Provider
		EmailProvider: getEnv("EMAIL_PROVIDER", "ses"),

		// SMTP
		SMTPHost:     getEnv("SMTP_HOST", "localhost"),
		SMTPPort:     smtpPort,
		SMTPUser:     getEnv("SMTP_USER", ""),
		SMTPPassword: getEnv("SMTP_PASSWORD", ""),
		SMTPFromName: getEnv("SMTP_FROM_NAME", "Mailat"),
		SMTPTLS:      smtpTLS,

		// AWS SES
		AWSRegion:           getEnv("AWS_REGION", "us-east-1"),
		AWSAccessKeyID:      getEnv("AWS_ACCESS_KEY_ID", ""),
		AWSSecretAccessKey:  getEnv("AWS_SECRET_ACCESS_KEY", ""),
		SESConfigurationSet: getEnv("SES_CONFIGURATION_SET", ""),

		// OAuth2 Providers
		GoogleClientID:        getEnv("GOOGLE_CLIENT_ID", ""),
		GoogleClientSecret:    getEnv("GOOGLE_CLIENT_SECRET", ""),
		GitHubClientID:        getEnv("GITHUB_CLIENT_ID", ""),
		GitHubClientSecret:    getEnv("GITHUB_CLIENT_SECRET", ""),
		MicrosoftClientID:     getEnv("MICROSOFT_CLIENT_ID", ""),
		MicrosoftClientSecret: getEnv("MICROSOFT_CLIENT_SECRET", ""),

		// Organization Limits
		DefaultMaxDomains:        defaultMaxDomains,
		DefaultMonthlyEmailLimit: defaultMonthlyEmailLimit,
		DefaultMaxIdentities:     defaultMaxIdentities,
		DefaultMaxContacts:       defaultMaxContacts,

		CORSOrigins: splitList(getEnv("CORS_ORIGINS", "")),
	}
	for i, origin := range cfg.CORSOrigins {
		cfg.CORSOrigins[i] = strings.TrimSuffix(origin, "/")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.TrustedProxyNets, _ = ParseProxyNets(cfg.TrustedProxyCIDRs)
	cfg.SessionTTL, _ = ParseSessionDuration(cfg.JWTExpiresIn)
	Cfg = cfg
	return Cfg, nil
}

var (
	dayDuration        = regexp.MustCompile(`^\d+d$`)
	secretPlaceholders = []string{"replace-with", "change-me", "changeme"}
)

// Validate fails startup on unsafe or malformed settings. The returned error
// names variables only; secret values are never included.
func (c *Config) Validate() error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	for _, s := range []struct{ name, value string }{{"JWT_SECRET", c.JWTSecret}, {"ENCRYPTION_KEY", c.EncryptionKey}} {
		lower := strings.ToLower(s.value)
		switch {
		case s.value == "":
			add("%s is required", s.name)
		case len(s.value) < 32:
			add("%s must be at least 32 bytes", s.name)
		default:
			for _, p := range secretPlaceholders {
				if strings.HasPrefix(lower, p) {
					add("%s still holds the example placeholder", s.name)
					break
				}
			}
		}
	}
	if c.JWTSecret != "" && c.JWTSecret == c.EncryptionKey {
		add("JWT_SECRET and ENCRYPTION_KEY must be different values")
	}
	if len(problems) > 0 {
		problems[len(problems)-1] += " (generate with: openssl rand -hex 32)"
	}
	if _, err := ParseSessionDuration(c.JWTExpiresIn); err != nil {
		add("JWT_EXPIRES_IN %v", err)
	}
	if _, err := ParseProxyNets(c.TrustedProxyCIDRs); err != nil {
		add("TRUSTED_PROXY_CIDRS %v", err)
	}
	for i, origin := range c.CORSOrigins {
		if !validOrigin(origin) {
			add("CORS_ORIGINS entry %d must look like https://host[:port]", i+1)
		}
	}
	if c.EmailProvider != "ses" && c.EmailProvider != "smtp" {
		add("EMAIL_PROVIDER must be ses or smtp")
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New("invalid configuration: " + strings.Join(problems, "; "))
}

// ParseSessionDuration accepts "<n>d" (days) or any time.ParseDuration value,
// bounded to 1h-90d. time.ParseDuration alone rejects the documented "7d".
func ParseSessionDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	var d time.Duration
	if dayDuration.MatchString(s) {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n > 90 {
			return 0, errors.New("must be between 1h and 90d")
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		parsed, err := time.ParseDuration(s)
		if err != nil {
			return 0, errors.New(`must be a duration such as "7d" or "168h"`)
		}
		d = parsed
	}
	if d < time.Hour || d > 90*24*time.Hour {
		return 0, errors.New("must be between 1h and 90d")
	}
	return d, nil
}

// ParseProxyNets parses a comma-separated list of CIDRs; a bare IP means that
// single address.
func ParseProxyNets(raw string) ([]*net.IPNet, error) {
	var nets []*net.IPNet
	for i, part := range splitList(raw) {
		if ip := net.ParseIP(part); ip != nil {
			bits := 128
			if ip.To4() != nil {
				ip, bits = ip.To4(), 32
			}
			nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, network, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("entry %d is not a valid CIDR", i+1)
		}
		nets = append(nets, network)
	}
	return nets, nil
}

func validOrigin(origin string) bool {
	u, err := url.Parse(origin)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.Hostname() != "" &&
		u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && !strings.HasSuffix(origin, "?") && !strings.HasSuffix(origin, "#")
}

func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

// normalizeRedisURL ensures the URL has the redis:// prefix for redis.ParseURL
// Supports formats: redis://host:port, redis://:pass@host:port, host:port
func normalizeRedisURL(url string) string {
	// Already has proper prefix
	if strings.HasPrefix(url, "redis://") || strings.HasPrefix(url, "rediss://") {
		return url
	}
	// Add redis:// prefix if missing
	return "redis://" + url
}
