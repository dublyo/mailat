package config

import (
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	// Server
	Port      int
	Env       string
	APIUrl    string
	WebUrl    string
	AppDomain string // Domain for WebAuthn and email (e.g., "mailat.co")
	AppName   string // Application name for branding (e.g., "Mailat")

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

	Cfg = &Config{
		// Server
		Port:      port,
		Env:       getEnv("NODE_ENV", "development"),
		APIUrl:    getEnv("API_URL", "http://localhost:3001"),
		WebUrl:    getEnv("WEB_URL", "http://localhost:3000"),
		AppDomain: getEnv("APP_DOMAIN", "localhost"),
		AppName:   getEnv("APP_NAME", "Mailat"),

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
		EmailProvider: getEnv("EMAIL_PROVIDER", "smtp"),

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
	}

	return Cfg, nil
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
