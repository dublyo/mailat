package worker

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net"
	"net/smtp"
	"regexp"
	"strings"
	"time"

	"github.com/hibiken/asynq"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/provider"
)

// sendMailWithTLS sends email using SMTP with proper TLS handling for internal Docker networks.
// When SMTP_TLS is false, it allows InsecureSkipVerify for STARTTLS connections.
// heloDomain is the domain to use in EHLO command (e.g., "mail.yourdomain.com")
func sendMailWithTLS(addr, host string, auth smtp.Auth, from string, to []string, msg []byte, skipTLSVerify bool, heloDomain string) error {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer client.Close()

	// Send EHLO with proper FQDN (must be called explicitly, otherwise Go uses os.Hostname() which returns "localhost" in Docker)
	if heloDomain == "" {
		heloDomain = "localhost"
	}
	if err = client.Hello(heloDomain); err != nil {
		return fmt.Errorf("EHLO failed: %w", err)
	}

	// Check if server supports STARTTLS
	if ok, _ := client.Extension("STARTTLS"); ok {
		tlsConfig := &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: skipTLSVerify,
		}
		if err = client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("STARTTLS failed: %w", err)
		}
	}

	// Authenticate if provided
	if auth != nil {
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

// EmailHandler handles email sending tasks
type EmailHandler struct {
	db                    *sql.DB
	cfg                   *config.Config
	emailProvider         provider.EmailProvider
	webhookTriggerService webhookTriggerFirer
}

// webhookTriggerFirer is a minimal interface for firing webhook triggers
type webhookTriggerFirer interface {
	Fire(ctx context.Context, orgID int64, triggerType string, data map[string]interface{}) error
}

// NewEmailHandler creates a new email handler
func NewEmailHandler(db *sql.DB, cfg *config.Config) *EmailHandler {
	handler := &EmailHandler{
		db:  db,
		cfg: cfg,
	}

	// Initialize email provider based on config
	ctx := context.Background()
	if cfg.EmailProvider == "ses" {
		sesProvider, err := provider.NewSESProvider(ctx, &provider.SESConfig{
			Region:           cfg.AWSRegion,
			AccessKeyID:      cfg.AWSAccessKeyID,
			SecretAccessKey:  cfg.AWSSecretAccessKey,
			ConfigurationSet: cfg.SESConfigurationSet,
		})
		if err != nil {
			fmt.Printf("Warning: SES sending is unavailable: %v\n", err)
		} else {
			handler.emailProvider = sesProvider
			fmt.Println("Email handler initialized with AWS SES provider")
		}
	} else {
		handler.emailProvider = provider.NewSMTPProvider(&provider.SMTPConfig{
			Host:          cfg.SMTPHost,
			Port:          cfg.SMTPPort,
			Username:      cfg.SMTPUser,
			Password:      cfg.SMTPPassword,
			UseTLS:        cfg.SMTPTLS,
			SkipTLSVerify: !cfg.SMTPTLS,
		})
		fmt.Println("Email handler initialized with SMTP provider")
	}

	return handler
}

// SetWebhookTriggerService sets the webhook trigger service for firing trigger events
func (h *EmailHandler) SetWebhookTriggerService(svc webhookTriggerFirer) {
	h.webhookTriggerService = svc
}

// HandleEmailSend processes a single email send task
func (h *EmailHandler) HandleEmailSend(ctx context.Context, t *asynq.Task) error {
	if h.emailProvider == nil {
		return fmt.Errorf("email provider is not configured; sending has not been attempted")
	}
	payload, err := UnmarshalEmailSendPayload(t.Payload())
	if err != nil {
		return fmt.Errorf("failed to unmarshal payload: %w", err)
	}

	// Atomically claim only a queued message. Retried jobs must not submit an
	// already accepted, sending, failed, or uncertain attempt to the provider again.
	claimed, err := h.db.ExecContext(ctx, `UPDATE transactional_emails SET status='sending',updated_at=NOW() WHERE id=$1 AND status='queued'`, payload.EmailID)
	if err != nil {
		return fmt.Errorf("claim email send: %w", err)
	}
	affected, err := claimed.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return nil
	}

	// Record sending event
	h.recordEvent(ctx, payload.EmailID, "sending", "Email processing started")

	// Build email message for provider
	emailMsg := &provider.EmailMessage{
		From:      payload.From,
		To:        payload.To,
		Cc:        payload.Cc,
		Bcc:       payload.Bcc,
		ReplyTo:   payload.ReplyTo,
		Subject:   payload.Subject,
		TextBody:  payload.TextBody,
		HTMLBody:  payload.HTMLBody,
		MessageID: payload.MessageID,
	}

	// SES has no submission idempotency token. A network retry may send twice,
	// so persist an unknown outcome instead of repeating an ambiguous submission.
	sendResult, sendErr := h.emailProvider.SendEmail(ctx, emailMsg)

	// Store provider message ID if available
	if sendResult != nil && sendResult.MessageID != "" {
		_, err = h.db.ExecContext(ctx, `
			UPDATE transactional_emails
			SET provider_message_id = $2, email_provider = $3, updated_at = NOW()
			WHERE id = $1
		`, payload.EmailID, sendResult.MessageID, h.emailProvider.Name())
		if err != nil {
			fmt.Printf("Warning: failed to update provider message ID: %v\n", err)
		}
	}

	if sendErr != nil {
		status := "unknown"
		if provider.IsDefinitiveSendError(sendErr) {
			status = "failed"
		}
		persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, persistErr := h.db.ExecContext(persistCtx, `UPDATE transactional_emails SET status=$2,updated_at=NOW() WHERE id=$1 AND status='sending'`, payload.EmailID, status)
		if persistErr != nil {
			fmt.Printf("Failed to persist send outcome for email %d: %v\n", payload.EmailID, persistErr)
		}
		h.recordEvent(persistCtx, payload.EmailID, status, sendErr.Error())
		return nil // A queue retry cannot safely determine whether SES accepted the prior call.
	}

	// A fast SNS event may already report delivery/bounce after the provider-ID write.
	// Acceptance persistence must not regress that more informative terminal result.
	now := time.Now()
	persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = h.db.ExecContext(persistCtx, `
		UPDATE transactional_emails
		SET status = CASE WHEN status IN ('delivered','bounced','complained','opened','clicked') THEN status ELSE 'sent' END, sent_at = COALESCE(sent_at,$2), updated_at = NOW()
		WHERE id = $1
	`, payload.EmailID, now)
	if err != nil {
		fmt.Printf("Warning: failed to update email status: %v\n", err)
	}

	h.recordEvent(ctx, payload.EmailID, "sent", "Email sent successfully")

	// Trigger webhook delivery for 'sent' event
	go h.triggerWebhook(ctx, payload.OrgID, payload.EmailID, "sent")

	// Fire webhook trigger (n8n / Zapier integration)
	if h.webhookTriggerService != nil {
		go h.webhookTriggerService.Fire(context.Background(), payload.OrgID, "email_sent", map[string]interface{}{
			"email_id": payload.EmailID,
			"to":       payload.To,
			"subject":  payload.Subject,
			"from":     payload.From,
		})
	}

	return nil
}

// recordEvent records a delivery event
func (h *EmailHandler) recordEvent(ctx context.Context, emailID int64, eventType, details string) {
	_, err := h.db.ExecContext(ctx, `
		INSERT INTO transactional_delivery_events (email_id, event_type, details)
		VALUES ($1, $2, $3)
	`, emailID, eventType, details)
	if err != nil {
		fmt.Printf("Warning: failed to record event: %v\n", err)
	}
}

// triggerWebhook queues webhook delivery for an event
func (h *EmailHandler) triggerWebhook(ctx context.Context, orgID, emailID int64, eventType string) {
	// Get active webhooks for this org that listen to this event type
	eventName := "email." + eventType // e.g., "email.sent", "email.delivered"
	rows, err := h.db.QueryContext(ctx, `
		SELECT id, url, secret FROM webhooks
		WHERE org_id = $1 AND active = true AND $2 = ANY(events)
	`, orgID, eventName)
	if err != nil {
		fmt.Printf("Failed to get webhooks: %v\n", err)
		return
	}
	defer rows.Close()

	// Create queue client
	queueClient, err := NewQueueClient(h.cfg)
	if err != nil {
		fmt.Printf("Failed to create queue client for webhooks: %v\n", err)
		return
	}
	defer queueClient.Close()

	for rows.Next() {
		var webhookID int64
		var url, secret string
		if err := rows.Scan(&webhookID, &url, &secret); err != nil {
			continue
		}

		payload := &WebhookDeliverPayload{
			WebhookID:  webhookID,
			OrgID:      orgID,
			URL:        url,
			Secret:     secret,
			EventType:  eventName,
			EmailID:    emailID,
			MaxRetries: 5,
		}

		_, err := queueClient.EnqueueWebhookDeliver(payload)
		if err != nil {
			fmt.Printf("Failed to enqueue webhook: %v\n", err)
		}
	}
}

// handlePermanentFailure handles permanent delivery failures
func (h *EmailHandler) handlePermanentFailure(ctx context.Context, payload *EmailSendPayload) {
	// Add recipients to suppression list
	for _, recipient := range payload.To {
		_, err := h.db.ExecContext(ctx, `
			INSERT INTO suppression_list (org_id, email, reason, source)
			VALUES ($1, $2, $3, 'bounce')
			ON CONFLICT (org_id, email) DO NOTHING
		`, payload.OrgID, strings.ToLower(recipient), "Permanent delivery failure")
		if err != nil {
			fmt.Printf("Warning: failed to add to suppression list: %v\n", err)
		}
	}
}

// buildMIMEMessage builds a MIME email message
func (h *EmailHandler) buildMIMEMessage(payload *EmailSendPayload) string {
	boundary := "----=_Part_" + generateRandomString(16)

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("From: %s\r\n", payload.From))
	msg.WriteString(fmt.Sprintf("To: %s\r\n", strings.Join(payload.To, ", ")))
	if len(payload.Cc) > 0 {
		msg.WriteString(fmt.Sprintf("Cc: %s\r\n", strings.Join(payload.Cc, ", ")))
	}
	if payload.ReplyTo != "" {
		msg.WriteString(fmt.Sprintf("Reply-To: %s\r\n", payload.ReplyTo))
	}
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", payload.Subject))
	msg.WriteString(fmt.Sprintf("Message-ID: %s\r\n", payload.MessageID))
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString(fmt.Sprintf("Date: %s\r\n", time.Now().Format(time.RFC1123Z)))
	msg.WriteString(fmt.Sprintf("Content-Type: multipart/alternative; boundary=\"%s\"\r\n", boundary))
	msg.WriteString("\r\n")

	// Determine text body - auto-generate from HTML if not provided
	textBody := payload.TextBody
	if textBody == "" && payload.HTMLBody != "" {
		textBody = htmlToPlainText(payload.HTMLBody)
	}

	// Text part (always include for better deliverability)
	if textBody != "" {
		msg.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		msg.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
		msg.WriteString("Content-Transfer-Encoding: quoted-printable\r\n")
		msg.WriteString("\r\n")
		msg.WriteString(textBody)
		msg.WriteString("\r\n")
	}

	// HTML part
	if payload.HTMLBody != "" {
		msg.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		msg.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
		msg.WriteString("Content-Transfer-Encoding: quoted-printable\r\n")
		msg.WriteString("\r\n")
		msg.WriteString(payload.HTMLBody)
		msg.WriteString("\r\n")
	}

	msg.WriteString(fmt.Sprintf("--%s--\r\n", boundary))

	return msg.String()
}

// getSMTPHost returns the SMTP host from config
func (h *EmailHandler) getSMTPHost() string {
	if h.cfg.SMTPHost != "" {
		return h.cfg.SMTPHost
	}
	// Fallback: extract host from StalwartURL
	url := h.cfg.StalwartURL
	url = strings.Replace(url, "http://", "", 1)
	url = strings.Replace(url, "https://", "", 1)
	parts := strings.Split(url, ":")
	if len(parts) > 0 {
		return parts[0]
	}
	return "localhost"
}

// getSMTPPort returns the SMTP port (submission port)
func (h *EmailHandler) getSMTPPort() string {
	if h.cfg.SMTPPort > 0 {
		return fmt.Sprintf("%d", h.cfg.SMTPPort)
	}
	return "587"
}

// isRetryableError determines if an SMTP error is retryable
func isRetryableError(err error) bool {
	errStr := err.Error()

	// Temporary errors that should be retried
	retryablePatterns := []string{
		"connection refused",
		"connection reset",
		"timeout",
		"temporary",
		"try again",
		"service unavailable",
		"421",
		"450",
		"451",
		"452",
	}

	for _, pattern := range retryablePatterns {
		if strings.Contains(strings.ToLower(errStr), pattern) {
			return true
		}
	}

	return false
}

// isPermanentFailure determines if the error indicates a permanent failure
func isPermanentFailure(err error) bool {
	errStr := err.Error()

	// Permanent errors
	permanentPatterns := []string{
		"user unknown",
		"mailbox not found",
		"invalid address",
		"550",
		"551",
		"552",
		"553",
		"554",
	}

	for _, pattern := range permanentPatterns {
		if strings.Contains(strings.ToLower(errStr), pattern) {
			return true
		}
	}

	return false
}

// generateRandomString generates a random hex string
func generateRandomString(length int) string {
	bytes := make([]byte, length/2)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

// Pre-compiled regexes for HTML to text conversion (Go's RE2 doesn't support backreferences)
var (
	scriptRe       = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	styleRe        = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	breakRe        = regexp.MustCompile(`(?i)<br\s*/?>|</p>|</div>|</li>|</tr>`)
	liRe           = regexp.MustCompile(`(?i)<li[^>]*>`)
	tagRe          = regexp.MustCompile(`<[^>]+>`)
	multiNewlineRe = regexp.MustCompile(`\n{3,}`)
)

// htmlToPlainText converts HTML to plain text by removing tags
func htmlToPlainText(html string) string {
	// Remove script and style elements
	text := scriptRe.ReplaceAllString(html, "")
	text = styleRe.ReplaceAllString(text, "")

	// Replace <br>, <br/>, </p>, </div>, </li> with newlines
	text = breakRe.ReplaceAllString(text, "\n")

	// Replace <li> with "- "
	text = liRe.ReplaceAllString(text, "- ")

	// Remove all remaining HTML tags
	text = tagRe.ReplaceAllString(text, "")

	// Decode common HTML entities
	text = strings.ReplaceAll(text, "&nbsp;", " ")
	text = strings.ReplaceAll(text, "&amp;", "&")
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	text = strings.ReplaceAll(text, "&quot;", "\"")
	text = strings.ReplaceAll(text, "&#39;", "'")

	// Collapse multiple newlines to double newline
	text = multiNewlineRe.ReplaceAllString(text, "\n\n")

	// Trim whitespace
	text = strings.TrimSpace(text)

	return text
}
