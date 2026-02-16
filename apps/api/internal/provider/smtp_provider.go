package provider

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// SMTPProvider implements EmailProvider using traditional SMTP
type SMTPProvider struct {
	host          string
	port          int
	username      string
	password      string
	useTLS        bool
	skipTLSVerify bool
	heloDomain    string
}

// SMTPConfig holds configuration for the SMTP provider
type SMTPConfig struct {
	Host          string
	Port          int
	Username      string
	Password      string
	UseTLS        bool
	SkipTLSVerify bool
	HeloDomain    string // Domain to use in HELO/EHLO (e.g., "mail.yourdomain.com")
}

// NewSMTPProvider creates a new SMTP provider
func NewSMTPProvider(cfg *SMTPConfig) *SMTPProvider {
	heloDomain := cfg.HeloDomain
	if heloDomain == "" {
		heloDomain = "localhost"
	}
	return &SMTPProvider{
		host:          cfg.Host,
		port:          cfg.Port,
		username:      cfg.Username,
		password:      cfg.Password,
		useTLS:        cfg.UseTLS,
		skipTLSVerify: cfg.SkipTLSVerify,
		heloDomain:    heloDomain,
	}
}

// Name returns the provider name
func (p *SMTPProvider) Name() string {
	return "smtp"
}

// SendEmail sends an email via SMTP
func (p *SMTPProvider) SendEmail(ctx context.Context, msg *EmailMessage) (*SendResult, error) {
	// Build MIME message
	rawMsg, err := p.buildMIMEMessage(msg)
	if err != nil {
		return &SendResult{
			ProviderName: "smtp",
			Success:      false,
			Error:        err,
		}, err
	}

	// Combine all recipients
	allRecipients := append(append(msg.To, msg.Cc...), msg.Bcc...)

	// Send via SMTP
	err = p.sendWithTLS(msg.From, allRecipients, rawMsg)
	if err != nil {
		return &SendResult{
			ProviderName: "smtp",
			Success:      false,
			Error:        err,
		}, err
	}

	return &SendResult{
		MessageID:    msg.MessageID,
		ProviderName: "smtp",
		Success:      true,
	}, nil
}

// SendRawEmail sends a raw MIME message via SMTP
func (p *SMTPProvider) SendRawEmail(ctx context.Context, from string, to []string, rawMessage []byte) (*SendResult, error) {
	err := p.sendWithTLS(from, to, rawMessage)
	if err != nil {
		return &SendResult{
			ProviderName: "smtp",
			Success:      false,
			Error:        err,
		}, err
	}

	return &SendResult{
		ProviderName: "smtp",
		Success:      true,
	}, nil
}

// sendWithTLS sends email using SMTP with proper TLS handling
func (p *SMTPProvider) sendWithTLS(from string, to []string, msg []byte) error {
	addr := fmt.Sprintf("%s:%d", p.host, p.port)

	conn, err := net.DialTimeout("tcp", addr, 30*time.Second)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}

	client, err := smtp.NewClient(conn, p.host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer client.Close()

	// Send EHLO
	if err = client.Hello(p.heloDomain); err != nil {
		return fmt.Errorf("EHLO failed: %w", err)
	}

	// Check if server supports STARTTLS
	if ok, _ := client.Extension("STARTTLS"); ok {
		tlsConfig := &tls.Config{
			ServerName:         p.host,
			InsecureSkipVerify: p.skipTLSVerify,
		}
		if err = client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("STARTTLS failed: %w", err)
		}
	}

	// Authenticate if provided
	if p.username != "" && p.password != "" {
		auth := smtp.PlainAuth("", p.username, p.password, p.host)
		if err = client.Auth(auth); err != nil {
			return fmt.Errorf("auth failed: %w", err)
		}
	}

	// Send the email
	if err = client.Mail(from); err != nil {
		return fmt.Errorf("MAIL FROM failed: %w", err)
	}
	for _, recipient := range to {
		if err = client.Rcpt(recipient); err != nil {
			return fmt.Errorf("RCPT TO failed: %w", err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA failed: %w", err)
	}
	_, err = w.Write(msg)
	if err != nil {
		return fmt.Errorf("write failed: %w", err)
	}
	err = w.Close()
	if err != nil {
		return fmt.Errorf("close failed: %w", err)
	}
	return client.Quit()
}

// buildMIMEMessage builds a MIME email message
func (p *SMTPProvider) buildMIMEMessage(msg *EmailMessage) ([]byte, error) {
	// Use the shared BuildMIMEMessage function
	return BuildMIMEMessage(msg)
}

// VerifyDomain is not supported for SMTP provider
func (p *SMTPProvider) VerifyDomain(ctx context.Context, domain string) (*DomainVerificationResult, error) {
	return nil, fmt.Errorf("domain verification not supported for SMTP provider")
}

// CheckDomainVerification is not supported for SMTP provider
func (p *SMTPProvider) CheckDomainVerification(ctx context.Context, domain string) (*DomainIdentity, error) {
	return nil, fmt.Errorf("domain verification not supported for SMTP provider")
}

// DeleteDomainIdentity is not supported for SMTP provider
func (p *SMTPProvider) DeleteDomainIdentity(ctx context.Context, domain string) error {
	return fmt.Errorf("domain identity deletion not supported for SMTP provider")
}

// VerifyEmailIdentity is not supported for SMTP provider
func (p *SMTPProvider) VerifyEmailIdentity(ctx context.Context, email string) error {
	return fmt.Errorf("email identity verification not supported for SMTP provider")
}

// GetSendQuota returns unlimited quota for SMTP
func (p *SMTPProvider) GetSendQuota(ctx context.Context) (*SendQuota, error) {
	return &SendQuota{
		Max24HourSend:   -1, // Unlimited
		MaxSendRate:     -1, // Unlimited
		SentLast24Hours: 0,
	}, nil
}

// GetSendStatistics returns empty stats for SMTP
func (p *SMTPProvider) GetSendStatistics(ctx context.Context) (*SendStatistics, error) {
	return &SendStatistics{}, nil
}

// IsHealthy checks if SMTP connection is healthy
func (p *SMTPProvider) IsHealthy(ctx context.Context) bool {
	addr := fmt.Sprintf("%s:%d", p.host, p.port)
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Close cleans up resources
func (p *SMTPProvider) Close() error {
	return nil
}

// htmlToPlainText converts HTML to plain text
func htmlToPlainText(html string) string {
	// Simple HTML to text conversion
	text := html
	text = strings.ReplaceAll(text, "<br>", "\n")
	text = strings.ReplaceAll(text, "<br/>", "\n")
	text = strings.ReplaceAll(text, "<br />", "\n")
	text = strings.ReplaceAll(text, "</p>", "\n")
	text = strings.ReplaceAll(text, "</div>", "\n")
	text = strings.ReplaceAll(text, "&nbsp;", " ")
	text = strings.ReplaceAll(text, "&amp;", "&")
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	text = strings.ReplaceAll(text, "&quot;", "\"")

	// Remove remaining HTML tags
	var result strings.Builder
	inTag := false
	for _, r := range text {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			result.WriteRune(r)
		}
	}

	return strings.TrimSpace(result.String())
}
