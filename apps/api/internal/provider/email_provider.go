package provider

import (
	"context"
)

// EmailMessage represents an email to be sent
type EmailMessage struct {
	From        string
	To          []string
	Cc          []string
	Bcc         []string
	ReplyTo     string
	Subject     string
	TextBody    string
	HTMLBody    string
	MessageID   string
	Headers     map[string]string
	Attachments []Attachment
}

// Attachment represents an email attachment
type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

// SendResult contains the result of sending an email
type SendResult struct {
	MessageID    string // Provider-specific message ID (e.g., SES message ID)
	ProviderName string // "smtp" or "ses"
	Success      bool
	Error        error
}

// DomainIdentity represents a domain for email sending
type DomainIdentity struct {
	Domain            string
	VerificationToken string
	DKIMTokens        []string // For SES Easy DKIM
	Verified          bool
	MailFromDomain    string // Custom MAIL FROM domain (e.g., bounce.example.com)
	MailFromVerified  bool   // Whether MAIL FROM is verified
}

// DomainVerificationResult contains DNS records needed for domain verification
type DomainVerificationResult struct {
	Domain          string
	DKIMRecords     []DNSRecord
	SPFRecord       *DNSRecord
	DMARCRecord     *DNSRecord
	VerifyRecord    *DNSRecord
	MailFromRecords []DNSRecord // MX and SPF for custom MAIL FROM domain
	MailFromDomain  string      // The MAIL FROM subdomain (e.g., bounce.example.com)
}

// DNSRecord represents a DNS record for domain verification
type DNSRecord struct {
	Type     string // TXT, CNAME, MX
	Name     string
	Value    string
	Priority int // For MX records
}

// EmailProvider defines the interface for email sending providers
type EmailProvider interface {
	// Name returns the provider name ("smtp" or "ses")
	Name() string

	// SendEmail sends an email and returns the result
	SendEmail(ctx context.Context, msg *EmailMessage) (*SendResult, error)

	// SendRawEmail sends a raw MIME message (useful for complex emails)
	SendRawEmail(ctx context.Context, from string, to []string, rawMessage []byte) (*SendResult, error)

	// VerifyDomain initiates domain verification with the provider
	// Returns DNS records that need to be configured
	VerifyDomain(ctx context.Context, domain string) (*DomainVerificationResult, error)

	// CheckDomainVerification checks if a domain has been verified
	CheckDomainVerification(ctx context.Context, domain string) (*DomainIdentity, error)

	// DeleteDomainIdentity removes a domain identity from the provider
	DeleteDomainIdentity(ctx context.Context, domain string) error

	// VerifyEmailIdentity verifies a single email address (for SES sandbox mode)
	VerifyEmailIdentity(ctx context.Context, email string) error

	// GetSendQuota returns the current sending quota/limits
	GetSendQuota(ctx context.Context) (*SendQuota, error)

	// GetSendStatistics returns sending statistics
	GetSendStatistics(ctx context.Context) (*SendStatistics, error)

	// IsHealthy checks if the provider connection is healthy
	IsHealthy(ctx context.Context) bool

	// Close cleans up any resources
	Close() error
}

// SendQuota represents email sending limits
type SendQuota struct {
	Max24HourSend   float64 // Maximum emails allowed in 24 hours
	MaxSendRate     float64 // Maximum emails per second
	SentLast24Hours float64 // Emails sent in the last 24 hours
}

// SendStatistics contains email sending statistics
type SendStatistics struct {
	DeliveryAttempts int64
	Bounces          int64
	Complaints       int64
	Rejects          int64
}

// ProviderFactory creates email providers based on configuration
type ProviderFactory struct {
	providers map[string]EmailProvider
}

// NewProviderFactory creates a new provider factory
func NewProviderFactory() *ProviderFactory {
	return &ProviderFactory{
		providers: make(map[string]EmailProvider),
	}
}

// Register adds a provider to the factory
func (f *ProviderFactory) Register(provider EmailProvider) {
	f.providers[provider.Name()] = provider
}

// Get returns a provider by name
func (f *ProviderFactory) Get(name string) (EmailProvider, bool) {
	p, ok := f.providers[name]
	return p, ok
}

// Close cleans up all registered providers
func (f *ProviderFactory) Close() error {
	for _, p := range f.providers {
		if err := p.Close(); err != nil {
			return err
		}
	}
	return nil
}
