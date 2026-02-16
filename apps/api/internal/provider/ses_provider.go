package provider

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// SESProvider implements EmailProvider using AWS SES v2
type SESProvider struct {
	client           *sesv2.Client
	region           string
	configurationSet string
}

// SESConfig holds configuration for the SES provider
type SESConfig struct {
	Region           string
	AccessKeyID      string
	SecretAccessKey  string
	ConfigurationSet string
}

// NewSESProvider creates a new SES provider
func NewSESProvider(ctx context.Context, cfg *SESConfig) (*SESProvider, error) {
	// Create AWS config with explicit credentials
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.AccessKeyID,
			cfg.SecretAccessKey,
			"",
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := sesv2.NewFromConfig(awsCfg)

	return &SESProvider{
		client:           client,
		region:           cfg.Region,
		configurationSet: cfg.ConfigurationSet,
	}, nil
}

// Name returns the provider name
func (p *SESProvider) Name() string {
	return "ses"
}

// SendEmail sends an email via SES
func (p *SESProvider) SendEmail(ctx context.Context, msg *EmailMessage) (*SendResult, error) {
	// Build destination
	destination := &types.Destination{
		ToAddresses:  msg.To,
		CcAddresses:  msg.Cc,
		BccAddresses: msg.Bcc,
	}

	// Build email content
	var body types.Body
	if msg.HTMLBody != "" {
		body.Html = &types.Content{
			Data:    aws.String(msg.HTMLBody),
			Charset: aws.String("UTF-8"),
		}
	}
	if msg.TextBody != "" {
		body.Text = &types.Content{
			Data:    aws.String(msg.TextBody),
			Charset: aws.String("UTF-8"),
		}
	}

	content := &types.EmailContent{
		Simple: &types.Message{
			Subject: &types.Content{
				Data:    aws.String(msg.Subject),
				Charset: aws.String("UTF-8"),
			},
			Body: &body,
		},
	}

	// Build send request
	input := &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(msg.From),
		Destination:      destination,
		Content:          content,
	}

	// Add Reply-To if specified
	if msg.ReplyTo != "" {
		input.ReplyToAddresses = []string{msg.ReplyTo}
	}

	// Add configuration set if specified
	if p.configurationSet != "" {
		input.ConfigurationSetName = aws.String(p.configurationSet)
	}

	// Add custom headers (but NOT Message-ID - SES Simple format doesn't support it)
	// SES will generate its own Message-ID which we capture from the response
	if len(msg.Headers) > 0 {
		headers := make([]types.MessageHeader, 0)
		for k, v := range msg.Headers {
			// Skip Message-ID as SES doesn't allow it in Simple format
			if strings.EqualFold(k, "Message-ID") {
				continue
			}
			headers = append(headers, types.MessageHeader{
				Name:  aws.String(k),
				Value: aws.String(v),
			})
		}
		if len(headers) > 0 {
			input.Content.Simple.Headers = headers
		}
	}

	// Send email
	result, err := p.client.SendEmail(ctx, input)
	if err != nil {
		return &SendResult{
			ProviderName: "ses",
			Success:      false,
			Error:        err,
		}, err
	}

	return &SendResult{
		MessageID:    aws.ToString(result.MessageId),
		ProviderName: "ses",
		Success:      true,
	}, nil
}

// SendRawEmail sends a raw MIME message via SES
func (p *SESProvider) SendRawEmail(ctx context.Context, from string, to []string, rawMessage []byte) (*SendResult, error) {
	input := &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(from),
		Destination: &types.Destination{
			ToAddresses: to,
		},
		Content: &types.EmailContent{
			Raw: &types.RawMessage{
				Data: rawMessage,
			},
		},
	}

	if p.configurationSet != "" {
		input.ConfigurationSetName = aws.String(p.configurationSet)
	}

	result, err := p.client.SendEmail(ctx, input)
	if err != nil {
		return &SendResult{
			ProviderName: "ses",
			Success:      false,
			Error:        err,
		}, err
	}

	return &SendResult{
		MessageID:    aws.ToString(result.MessageId),
		ProviderName: "ses",
		Success:      true,
	}, nil
}

// VerifyDomain initiates domain verification with SES
func (p *SESProvider) VerifyDomain(ctx context.Context, domain string) (*DomainVerificationResult, error) {
	// Use bounce subdomain for custom MAIL FROM
	mailFromDomain := "bounce." + domain

	// Create domain identity with custom MAIL FROM
	input := &sesv2.CreateEmailIdentityInput{
		EmailIdentity: aws.String(domain),
		DkimSigningAttributes: &types.DkimSigningAttributes{
			// Use Easy DKIM (SES manages the keys)
			NextSigningKeyLength: types.DkimSigningKeyLengthRsa2048Bit,
		},
	}

	result, err := p.client.CreateEmailIdentity(ctx, input)
	if err != nil {
		// Check if identity already exists
		if strings.Contains(err.Error(), "AlreadyExistsException") {
			// Get existing identity info
			return p.getDomainVerificationRecords(ctx, domain)
		}
		return nil, fmt.Errorf("failed to create domain identity: %w", err)
	}

	// Configure custom MAIL FROM domain
	mailFromInput := &sesv2.PutEmailIdentityMailFromAttributesInput{
		EmailIdentity:       aws.String(domain),
		MailFromDomain:      aws.String(mailFromDomain),
		BehaviorOnMxFailure: types.BehaviorOnMxFailureUseDefaultValue,
	}
	_, err = p.client.PutEmailIdentityMailFromAttributes(ctx, mailFromInput)
	if err != nil {
		// Log but don't fail - MAIL FROM can be configured later
		fmt.Printf("Warning: failed to configure MAIL FROM: %v\n", err)
	}

	// Build DNS records from the response
	verificationResult := &DomainVerificationResult{
		Domain:         domain,
		DKIMRecords:    make([]DNSRecord, 0),
		MailFromDomain: mailFromDomain,
	}

	// Add DKIM CNAME records
	if result.DkimAttributes != nil {
		for _, token := range result.DkimAttributes.Tokens {
			verificationResult.DKIMRecords = append(verificationResult.DKIMRecords, DNSRecord{
				Type:  "CNAME",
				Name:  fmt.Sprintf("%s._domainkey.%s", token, domain),
				Value: fmt.Sprintf("%s.dkim.amazonses.com", token),
			})
		}
	}

	// Add recommended SPF record for root domain
	verificationResult.SPFRecord = &DNSRecord{
		Type:  "TXT",
		Name:  domain,
		Value: "v=spf1 include:amazonses.com ~all",
	}

	// Add recommended DMARC record
	verificationResult.DMARCRecord = &DNSRecord{
		Type:  "TXT",
		Name:  fmt.Sprintf("_dmarc.%s", domain),
		Value: fmt.Sprintf("v=DMARC1; p=quarantine; rua=mailto:dmarc@%s", domain),
	}

	// Add MAIL FROM DNS records
	verificationResult.MailFromRecords = []DNSRecord{
		{
			Type:     "MX",
			Name:     mailFromDomain,
			Value:    fmt.Sprintf("feedback-smtp.%s.amazonses.com", p.region),
			Priority: 10,
		},
		{
			Type:  "TXT",
			Name:  mailFromDomain,
			Value: "v=spf1 include:amazonses.com ~all",
		},
	}

	return verificationResult, nil
}

// getDomainVerificationRecords retrieves DNS records for an existing domain
func (p *SESProvider) getDomainVerificationRecords(ctx context.Context, domain string) (*DomainVerificationResult, error) {
	input := &sesv2.GetEmailIdentityInput{
		EmailIdentity: aws.String(domain),
	}

	result, err := p.client.GetEmailIdentity(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to get domain identity: %w", err)
	}

	// Use configured MAIL FROM or default to bounce subdomain
	mailFromDomain := "bounce." + domain
	if result.MailFromAttributes != nil && result.MailFromAttributes.MailFromDomain != nil {
		mailFromDomain = *result.MailFromAttributes.MailFromDomain
	}

	verificationResult := &DomainVerificationResult{
		Domain:         domain,
		DKIMRecords:    make([]DNSRecord, 0),
		MailFromDomain: mailFromDomain,
	}

	// Add DKIM CNAME records
	if result.DkimAttributes != nil {
		for _, token := range result.DkimAttributes.Tokens {
			verificationResult.DKIMRecords = append(verificationResult.DKIMRecords, DNSRecord{
				Type:  "CNAME",
				Name:  fmt.Sprintf("%s._domainkey.%s", token, domain),
				Value: fmt.Sprintf("%s.dkim.amazonses.com", token),
			})
		}
	}

	// Add recommended SPF record
	verificationResult.SPFRecord = &DNSRecord{
		Type:  "TXT",
		Name:  domain,
		Value: "v=spf1 include:amazonses.com ~all",
	}

	// Add recommended DMARC record
	verificationResult.DMARCRecord = &DNSRecord{
		Type:  "TXT",
		Name:  fmt.Sprintf("_dmarc.%s", domain),
		Value: fmt.Sprintf("v=DMARC1; p=quarantine; rua=mailto:dmarc@%s", domain),
	}

	// Add MAIL FROM DNS records
	verificationResult.MailFromRecords = []DNSRecord{
		{
			Type:     "MX",
			Name:     mailFromDomain,
			Value:    fmt.Sprintf("feedback-smtp.%s.amazonses.com", p.region),
			Priority: 10,
		},
		{
			Type:  "TXT",
			Name:  mailFromDomain,
			Value: "v=spf1 include:amazonses.com ~all",
		},
	}

	// If MAIL FROM is not configured yet, configure it now
	if result.MailFromAttributes == nil || result.MailFromAttributes.MailFromDomain == nil || *result.MailFromAttributes.MailFromDomain == "" {
		mailFromInput := &sesv2.PutEmailIdentityMailFromAttributesInput{
			EmailIdentity:       aws.String(domain),
			MailFromDomain:      aws.String(mailFromDomain),
			BehaviorOnMxFailure: types.BehaviorOnMxFailureUseDefaultValue,
		}
		_, err = p.client.PutEmailIdentityMailFromAttributes(ctx, mailFromInput)
		if err != nil {
			fmt.Printf("Warning: failed to configure MAIL FROM: %v\n", err)
		}
	}

	return verificationResult, nil
}

// CheckDomainVerification checks if a domain has been verified
func (p *SESProvider) CheckDomainVerification(ctx context.Context, domain string) (*DomainIdentity, error) {
	input := &sesv2.GetEmailIdentityInput{
		EmailIdentity: aws.String(domain),
	}

	result, err := p.client.GetEmailIdentity(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to get domain identity: %w", err)
	}

	identity := &DomainIdentity{
		Domain:     domain,
		Verified:   result.VerifiedForSendingStatus,
		DKIMTokens: make([]string, 0),
	}

	if result.DkimAttributes != nil {
		identity.DKIMTokens = result.DkimAttributes.Tokens
	}

	// Check MAIL FROM status
	if result.MailFromAttributes != nil {
		if result.MailFromAttributes.MailFromDomain != nil {
			identity.MailFromDomain = *result.MailFromAttributes.MailFromDomain
		}
		// MAIL FROM is verified when status is SUCCESS
		identity.MailFromVerified = result.MailFromAttributes.MailFromDomainStatus == types.MailFromDomainStatusSuccess
	}

	return identity, nil
}

// DeleteDomainIdentity removes a domain identity from SES
func (p *SESProvider) DeleteDomainIdentity(ctx context.Context, domain string) error {
	input := &sesv2.DeleteEmailIdentityInput{
		EmailIdentity: aws.String(domain),
	}

	_, err := p.client.DeleteEmailIdentity(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to delete domain identity: %w", err)
	}

	return nil
}

// VerifyEmailIdentity verifies a single email address
func (p *SESProvider) VerifyEmailIdentity(ctx context.Context, email string) error {
	input := &sesv2.CreateEmailIdentityInput{
		EmailIdentity: aws.String(email),
	}

	_, err := p.client.CreateEmailIdentity(ctx, input)
	if err != nil {
		// Ignore if already exists
		if strings.Contains(err.Error(), "AlreadyExistsException") {
			return nil
		}
		return fmt.Errorf("failed to create email identity: %w", err)
	}

	return nil
}

// GetSendQuota returns the current sending quota
func (p *SESProvider) GetSendQuota(ctx context.Context) (*SendQuota, error) {
	input := &sesv2.GetAccountInput{}

	result, err := p.client.GetAccount(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to get account info: %w", err)
	}

	quota := &SendQuota{}
	if result.SendQuota != nil {
		quota.Max24HourSend = result.SendQuota.Max24HourSend
		quota.MaxSendRate = result.SendQuota.MaxSendRate
		quota.SentLast24Hours = result.SendQuota.SentLast24Hours
	}

	return quota, nil
}

// GetSendStatistics returns sending statistics
func (p *SESProvider) GetSendStatistics(ctx context.Context) (*SendStatistics, error) {
	// SES v2 doesn't have a direct statistics API like v1
	// Return empty statistics (actual stats come from CloudWatch)
	return &SendStatistics{}, nil
}

// IsHealthy checks if the SES connection is healthy
func (p *SESProvider) IsHealthy(ctx context.Context) bool {
	input := &sesv2.GetAccountInput{}
	_, err := p.client.GetAccount(ctx, input)
	return err == nil
}

// Close cleans up resources
func (p *SESProvider) Close() error {
	// AWS SDK clients don't need explicit cleanup
	return nil
}

// BuildMIMEMessage builds a raw MIME message for complex emails with attachments
func BuildMIMEMessage(msg *EmailMessage) ([]byte, error) {
	var buf bytes.Buffer

	// Generate boundary
	boundary := fmt.Sprintf("----=_Part_%d", time.Now().UnixNano())

	// Write headers
	buf.WriteString(fmt.Sprintf("From: %s\r\n", msg.From))
	buf.WriteString(fmt.Sprintf("To: %s\r\n", strings.Join(msg.To, ", ")))
	if len(msg.Cc) > 0 {
		buf.WriteString(fmt.Sprintf("Cc: %s\r\n", strings.Join(msg.Cc, ", ")))
	}
	if msg.ReplyTo != "" {
		buf.WriteString(fmt.Sprintf("Reply-To: %s\r\n", msg.ReplyTo))
	}
	buf.WriteString(fmt.Sprintf("Subject: %s\r\n", msg.Subject))
	if msg.MessageID != "" {
		buf.WriteString(fmt.Sprintf("Message-ID: %s\r\n", msg.MessageID))
	}
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString(fmt.Sprintf("Date: %s\r\n", time.Now().Format(time.RFC1123Z)))

	// Add custom headers
	for k, v := range msg.Headers {
		buf.WriteString(fmt.Sprintf("%s: %s\r\n", k, v))
	}

	if len(msg.Attachments) > 0 {
		// Multipart mixed for attachments
		buf.WriteString(fmt.Sprintf("Content-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n", boundary))

		// Alternative part for text/html
		altBoundary := fmt.Sprintf("----=_Alt_%d", time.Now().UnixNano())
		buf.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		buf.WriteString(fmt.Sprintf("Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n", altBoundary))

		// Text part
		if msg.TextBody != "" {
			buf.WriteString(fmt.Sprintf("--%s\r\n", altBoundary))
			buf.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
			buf.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
			buf.WriteString(msg.TextBody)
			buf.WriteString("\r\n")
		}

		// HTML part
		if msg.HTMLBody != "" {
			buf.WriteString(fmt.Sprintf("--%s\r\n", altBoundary))
			buf.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
			buf.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
			buf.WriteString(msg.HTMLBody)
			buf.WriteString("\r\n")
		}

		buf.WriteString(fmt.Sprintf("--%s--\r\n", altBoundary))

		// Attachments
		for _, att := range msg.Attachments {
			buf.WriteString(fmt.Sprintf("--%s\r\n", boundary))
			buf.WriteString(fmt.Sprintf("Content-Type: %s; name=\"%s\"\r\n", att.ContentType, att.Filename))
			buf.WriteString("Content-Transfer-Encoding: base64\r\n")
			buf.WriteString(fmt.Sprintf("Content-Disposition: attachment; filename=\"%s\"\r\n\r\n", att.Filename))
			// Base64 encode attachment
			encoded := make([]byte, base64Len(len(att.Data)))
			base64Encode(encoded, att.Data)
			buf.Write(encoded)
			buf.WriteString("\r\n")
		}

		buf.WriteString(fmt.Sprintf("--%s--\r\n", boundary))
	} else {
		// Simple multipart/alternative for text and html
		buf.WriteString(fmt.Sprintf("Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n", boundary))

		// Text part
		if msg.TextBody != "" {
			buf.WriteString(fmt.Sprintf("--%s\r\n", boundary))
			buf.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
			buf.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
			buf.WriteString(msg.TextBody)
			buf.WriteString("\r\n")
		}

		// HTML part
		if msg.HTMLBody != "" {
			buf.WriteString(fmt.Sprintf("--%s\r\n", boundary))
			buf.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
			buf.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
			buf.WriteString(msg.HTMLBody)
			buf.WriteString("\r\n")
		}

		buf.WriteString(fmt.Sprintf("--%s--\r\n", boundary))
	}

	return buf.Bytes(), nil
}

// base64Len returns the length of base64 encoded data
func base64Len(n int) int {
	return (n + 2) / 3 * 4
}

// base64Encode encodes data to base64
func base64Encode(dst, src []byte) {
	const encodeStd = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	di, si := 0, 0
	n := (len(src) / 3) * 3
	for si < n {
		val := uint(src[si+0])<<16 | uint(src[si+1])<<8 | uint(src[si+2])
		dst[di+0] = encodeStd[val>>18&0x3F]
		dst[di+1] = encodeStd[val>>12&0x3F]
		dst[di+2] = encodeStd[val>>6&0x3F]
		dst[di+3] = encodeStd[val&0x3F]
		si += 3
		di += 4
	}
	remain := len(src) - si
	if remain == 0 {
		return
	}
	val := uint(src[si+0]) << 16
	if remain == 2 {
		val |= uint(src[si+1]) << 8
	}
	dst[di+0] = encodeStd[val>>18&0x3F]
	dst[di+1] = encodeStd[val>>12&0x3F]
	if remain == 2 {
		dst[di+2] = encodeStd[val>>6&0x3F]
		dst[di+3] = '='
	} else {
		dst[di+2] = '='
		dst[di+3] = '='
	}
}
