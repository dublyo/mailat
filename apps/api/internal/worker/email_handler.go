package worker

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/smtp"
	"regexp"
	"strings"
	"time"

	"github.com/hibiken/asynq"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/eventoutbox"
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

// NewEmailHandlerWithProvider lets direct dispatch and tests use exactly the queue send path.
func NewEmailHandlerWithProvider(db *sql.DB, cfg *config.Config, p provider.EmailProvider) *EmailHandler {
	return &EmailHandler{db: db, cfg: cfg, emailProvider: p}
}

func (h *EmailHandler) HandleEmailSend(ctx context.Context, t *asynq.Task) error {
	payload, err := UnmarshalEmailSendPayload(t.Payload())
	if err != nil {
		return fmt.Errorf("invalid email job: %w", err)
	}
	return h.ProcessEmail(ctx, payload)
}

// ProcessEmail claims once, then records an outcome and event in one transaction.
// A task retry cannot repeat a provider submission whose outcome may be uncertain.
func (h *EmailHandler) ProcessEmail(ctx context.Context, payload *EmailSendPayload) error {
	if h.emailProvider == nil {
		return fmt.Errorf("email provider is not configured")
	}
	var durable []byte
	err := h.db.QueryRowContext(ctx, `UPDATE transactional_emails SET status='sending',updated_at=NOW() WHERE id=$1 AND org_id=$2 AND status='queued' AND (scheduled_for IS NULL OR scheduled_for<=NOW()) RETURNING send_payload`, payload.EmailID, payload.OrgID).Scan(&durable)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if len(durable) > 0 {
		if err = json.Unmarshal(durable, payload); err != nil {
			return h.finishEmail(payload.EmailID, "failed", "invalid stored send payload", "", nil)
		}
	}
	msg := &provider.EmailMessage{From: payload.From, To: payload.To, Cc: payload.Cc, Bcc: payload.Bcc, ReplyTo: payload.ReplyTo, Subject: payload.Subject, TextBody: payload.TextBody, HTMLBody: payload.HTMLBody, MessageID: payload.MessageID, Headers: map[string]string{}}
	if payload.MessageUUID != "" {
		msg.Headers["X-Mailat-Message-ID"] = payload.MessageUUID
	}
	for _, a := range payload.Attachments {
		msg.Attachments = append(msg.Attachments, provider.Attachment{Filename: a.Name, ContentType: a.Type, Data: a.Data, ContentID: a.CID, Inline: a.Disposition == "inline"})
	}
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	result, sendErr := h.emailProvider.SendEmail(sendCtx, msg)
	cancel()
	providerID := ""
	if result != nil {
		providerID = result.MessageID
	}
	status, details := "sent", "Email accepted by the provider"
	if sendErr != nil {
		status = "unknown"
		details = "Provider outcome is uncertain; do not automatically resubmit"
		if provider.IsDefinitiveSendError(sendErr) {
			status = "failed"
			details = "Provider rejected the message"
		}
	}
	return h.finishEmail(payload.EmailID, status, details, providerID, payload)
}

func (h *EmailHandler) finishEmail(id int64, status, details, providerID string, payload *EmailSendPayload) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var org, user, identity int64
	var messageUUID, current string
	err = tx.QueryRowContext(ctx, `UPDATE transactional_emails SET
 status=CASE WHEN status IN ('delivered','bounced','complained','opened','clicked') THEN status ELSE $2 END,
 sent_at=CASE WHEN $2='sent' THEN COALESCE(sent_at,NOW()) ELSE sent_at END,
 provider_message_id=COALESCE(NULLIF($3,''),provider_message_id),email_provider=$4,updated_at=NOW()
 WHERE id=$1 RETURNING org_id,uuid,status,COALESCE(identity_id,0)`, id, status, providerID, h.emailProvider.Name()).Scan(&org, &messageUUID, &current, &identity)
	if err != nil {
		return err
	}
	if payload != nil {
		user = payload.UserID
	}
	if user == 0 && identity > 0 {
		_ = tx.QueryRowContext(ctx, `SELECT user_id FROM identities WHERE id=$1`, identity).Scan(&user)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO transactional_delivery_events(email_id,event_type,details) VALUES($1,$2,$3)`, id, status, details); err != nil {
		return err
	}
	data := map[string]any{"status": current, "messageUuid": messageUUID, "providerMessageId": providerID}
	if payload != nil {
		data["messageId"] = payload.MessageID
		data["from"] = payload.From
		data["to"] = payload.To
		data["subject"] = payload.Subject
	}
	if err = eventoutbox.Emit(ctx, tx, eventoutbox.Event{Type: "email." + status, OrgID: org, UserID: user, IdentityID: identity, MessageUUID: messageUUID, DedupeKey: status + ":" + messageUUID, Data: data}); err != nil {
		return err
	}
	return tx.Commit()
}

// RecoverPending processes only due, never-attempted jobs. Multiple instances may
// discover the same row; ProcessEmail's atomic claim permits one provider attempt.
func (h *EmailHandler) RecoverPending(ctx context.Context) error {
	rows, err := h.db.QueryContext(ctx, `SELECT id,org_id FROM transactional_emails WHERE status='queued' AND send_payload IS NOT NULL AND (scheduled_for IS NULL OR scheduled_for<=NOW()) ORDER BY id LIMIT 25`)
	if err != nil {
		return err
	}
	var jobs []*EmailSendPayload
	for rows.Next() {
		p := &EmailSendPayload{}
		if err = rows.Scan(&p.EmailID, &p.OrgID); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range jobs {
		if err = h.ProcessEmail(ctx, p); err != nil {
			return err
		}
	}
	// A process may die after the durable claim and after SES accepted. Never reset
	// that claim to queued; expose uncertainty for operator reconciliation instead.
	rows, err = h.db.QueryContext(ctx, `SELECT id FROM transactional_emails WHERE status='sending' AND updated_at<NOW()-INTERVAL '10 minutes' LIMIT 25`)
	if err != nil {
		return err
	}
	var stale []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		stale = append(stale, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range stale {
		if err = h.finishEmail(id, "unknown", "Send worker stopped before recording an outcome", "", nil); err != nil {
			return err
		}
	}
	return nil
}
func (h *EmailHandler) RunPending(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		if err := h.RecoverPending(ctx); err != nil && ctx.Err() == nil {
			fmt.Printf("Pending send recovery: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
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
