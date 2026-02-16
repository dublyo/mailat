package worker

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/smtp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/dublyo/mailat/api/internal/config"
)

const (
	// BatchSize is the number of contacts to process per batch
	BatchSize = 100

	// RateLimit is the maximum emails per second per campaign
	RateLimit = 50

	// MaxConcurrentCampaigns is the maximum number of campaigns to process concurrently
	MaxConcurrentCampaigns = 5
)

// CampaignHandler handles campaign processing tasks
type CampaignHandler struct {
	db              *sql.DB
	cfg             *config.Config
	activeCampaigns sync.Map // campaignID -> cancel function
}

// campaignState tracks the state of an active campaign
type campaignState struct {
	cancel context.CancelFunc
	paused bool
}

// NewCampaignHandler creates a new campaign handler
func NewCampaignHandler(db *sql.DB, cfg *config.Config) *CampaignHandler {
	return &CampaignHandler{
		db:  db,
		cfg: cfg,
	}
}

// HandleCampaignProcess processes a campaign by batching and sending emails
// This implements a listmonk-style pipe-based processing pattern
func (h *CampaignHandler) HandleCampaignProcess(ctx context.Context, task *asynq.Task) error {
	payload, err := UnmarshalCampaignProcessPayload(task.Payload())
	if err != nil {
		return fmt.Errorf("failed to unmarshal payload: %w", err)
	}

	// Create cancellable context for this campaign
	campaignCtx, cancel := context.WithCancel(ctx)
	h.activeCampaigns.Store(payload.CampaignID, &campaignState{cancel: cancel})
	defer h.activeCampaigns.Delete(payload.CampaignID)

	// Get campaign details
	campaign, err := h.getCampaign(ctx, payload.CampaignID)
	if err != nil {
		return fmt.Errorf("failed to get campaign: %w", err)
	}

	// Check if campaign should be processed
	if campaign.Status != "sending" && campaign.Status != "scheduled" {
		return nil // Campaign was paused or cancelled
	}

	// Update status to sending
	if campaign.Status == "scheduled" {
		h.updateCampaignStatus(ctx, payload.CampaignID, "sending")
	}

	// Get active contacts from list (excluding suppressed)
	contacts, err := h.getCampaignContacts(ctx, campaign)
	if err != nil {
		return fmt.Errorf("failed to get contacts: %w", err)
	}

	if len(contacts) == 0 {
		h.completeCampaign(ctx, payload.CampaignID)
		return nil
	}

	// Check IP warmup limits
	warmupLimit := h.getWarmupLimit(ctx, campaign.OrgID)
	dailySentCount := h.getDailySentCount(ctx, campaign.OrgID)

	// Process contacts in batches with rate limiting
	rateLimiter := time.NewTicker(time.Second / RateLimit)
	defer rateLimiter.Stop()

	sentCount := 0
	for i := 0; i < len(contacts); i++ {
		select {
		case <-campaignCtx.Done():
			// Campaign was cancelled or paused
			return nil
		case <-rateLimiter.C:
			// Check if campaign is still active
			state, ok := h.activeCampaigns.Load(payload.CampaignID)
			if ok && state.(*campaignState).paused {
				return nil
			}

			// Check warmup limit
			if warmupLimit > 0 && (dailySentCount+sentCount) >= warmupLimit {
				fmt.Printf("Campaign %d paused: warmup daily limit reached (%d/%d)\n",
					payload.CampaignID, dailySentCount+sentCount, warmupLimit)
				h.updateCampaignStatus(ctx, payload.CampaignID, "paused")
				h.updateCampaignProgress(ctx, payload.CampaignID, sentCount)
				// Create alert
				h.createWarmupAlert(ctx, campaign.OrgID, warmupLimit)
				return nil
			}

			// Send email to contact
			contact := contacts[i]
			err := h.sendCampaignEmail(ctx, campaign, contact)
			if err != nil {
				// Log error but continue with other contacts
				fmt.Printf("Failed to send to %s: %v\n", contact.Email, err)
				continue
			}

			sentCount++

			// Update progress every 100 emails
			if sentCount%100 == 0 {
				h.updateCampaignProgress(ctx, payload.CampaignID, sentCount)
			}
		}
	}

	// Update warmup day progress if applicable
	if warmupLimit > 0 {
		h.updateWarmupProgress(ctx, campaign.OrgID)
	}

	// Update final counts and mark as complete
	h.updateCampaignProgress(ctx, payload.CampaignID, sentCount)
	h.completeCampaign(ctx, payload.CampaignID)

	return nil
}

// HandleCampaignBatch processes a batch of campaign emails
func (h *CampaignHandler) HandleCampaignBatch(ctx context.Context, task *asynq.Task) error {
	payload, err := UnmarshalCampaignBatchPayload(task.Payload())
	if err != nil {
		return fmt.Errorf("failed to unmarshal payload: %w", err)
	}

	// Get campaign details
	campaign, err := h.getCampaign(ctx, payload.CampaignID)
	if err != nil {
		return fmt.Errorf("failed to get campaign: %w", err)
	}

	// Check if campaign is still sending
	if campaign.Status != "sending" {
		return nil
	}

	// Get contacts for this batch
	contacts, err := h.getContactsByIDs(ctx, payload.OrgID, payload.ContactIDs)
	if err != nil {
		return fmt.Errorf("failed to get contacts: %w", err)
	}

	// Rate limiter for this batch
	rateLimiter := time.NewTicker(time.Second / RateLimit)
	defer rateLimiter.Stop()

	sentCount := 0
	for _, contact := range contacts {
		<-rateLimiter.C

		err := h.sendCampaignEmail(ctx, campaign, contact)
		if err != nil {
			fmt.Printf("Failed to send to %s: %v\n", contact.Email, err)
			continue
		}
		sentCount++
	}

	// Update progress
	h.updateCampaignProgress(ctx, payload.CampaignID, sentCount)

	return nil
}

// PauseCampaign pauses an active campaign
func (h *CampaignHandler) PauseCampaign(campaignID int) {
	if state, ok := h.activeCampaigns.Load(campaignID); ok {
		state.(*campaignState).paused = true
		state.(*campaignState).cancel()
	}
}

// campaignInfo holds campaign data for processing
type campaignInfo struct {
	ID          int
	OrgID       int64
	Subject     string
	HTMLContent string
	TextContent string
	FromName    string
	FromEmail   string
	ReplyTo     string
	ListID      int
	Status      string
}

// contactInfo holds contact data for sending
type contactInfo struct {
	ID         int64
	Email      string
	FirstName  string
	LastName   string
	Attributes map[string]any
}

// getCampaign retrieves campaign details
func (h *CampaignHandler) getCampaign(ctx context.Context, campaignID int) (*campaignInfo, error) {
	var campaign campaignInfo
	var replyTo sql.NullString

	err := h.db.QueryRowContext(ctx, `
		SELECT id, org_id, subject, html_content, text_content, from_name, from_email, reply_to, list_id, status
		FROM campaigns WHERE id = $1
	`, campaignID).Scan(
		&campaign.ID, &campaign.OrgID, &campaign.Subject,
		&campaign.HTMLContent, &campaign.TextContent,
		&campaign.FromName, &campaign.FromEmail, &replyTo,
		&campaign.ListID, &campaign.Status,
	)
	if err != nil {
		return nil, err
	}

	if replyTo.Valid {
		campaign.ReplyTo = replyTo.String
	}

	return &campaign, nil
}

// getCampaignContacts retrieves active contacts for a campaign
func (h *CampaignHandler) getCampaignContacts(ctx context.Context, campaign *campaignInfo) ([]contactInfo, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT c.id, c.email, c.first_name, c.last_name, c.attributes
		FROM contacts c
		JOIN list_contacts lc ON lc.contact_id = c.id
		WHERE lc.list_id = $1
		AND c.status = 'active'
		AND c.email NOT IN (SELECT email FROM suppressions WHERE org_id = $2)
		ORDER BY c.id
	`, campaign.ListID, campaign.OrgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contacts []contactInfo
	for rows.Next() {
		var c contactInfo
		var attributesJSON []byte
		if err := rows.Scan(&c.ID, &c.Email, &c.FirstName, &c.LastName, &attributesJSON); err != nil {
			continue
		}
		if len(attributesJSON) > 0 {
			json.Unmarshal(attributesJSON, &c.Attributes)
		}
		contacts = append(contacts, c)
	}

	return contacts, nil
}

// getContactsByIDs retrieves contacts by their IDs
func (h *CampaignHandler) getContactsByIDs(ctx context.Context, orgID int64, contactIDs []int64) ([]contactInfo, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT id, email, first_name, last_name, attributes
		FROM contacts
		WHERE id = ANY($1) AND org_id = $2 AND status = 'active'
	`, contactIDs, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contacts []contactInfo
	for rows.Next() {
		var c contactInfo
		var attributesJSON []byte
		if err := rows.Scan(&c.ID, &c.Email, &c.FirstName, &c.LastName, &attributesJSON); err != nil {
			continue
		}
		if len(attributesJSON) > 0 {
			json.Unmarshal(attributesJSON, &c.Attributes)
		}
		contacts = append(contacts, c)
	}

	return contacts, nil
}

// sendCampaignEmail sends an email to a contact
func (h *CampaignHandler) sendCampaignEmail(ctx context.Context, campaign *campaignInfo, contact contactInfo) error {
	// Generate unique message ID
	messageID := fmt.Sprintf("<%s@%s>", uuid.New().String(), h.extractDomain(campaign.FromEmail))

	// Personalize content
	subject := h.personalizeContent(campaign.Subject, contact)
	htmlContent := h.personalizeContent(campaign.HTMLContent, contact)
	textContent := h.personalizeContent(campaign.TextContent, contact)

	// Insert email record
	var emailID int64
	err := h.db.QueryRowContext(ctx, `
		INSERT INTO emails (
			org_id, message_id, identity_id, from_email, from_name,
			to_emails, subject, html_content, text_content,
			source, domain_id, campaign_id, contact_id,
			status, created_at, updated_at
		)
		SELECT $1, $2, 0, $3, $4, $5, $6, $7, $8, 'campaign',
			(SELECT id FROM domains WHERE org_id = $1 AND name = $9 LIMIT 1),
			$10, $11, 'queued', NOW(), NOW()
		RETURNING id
	`,
		campaign.OrgID, messageID, campaign.FromEmail, campaign.FromName,
		[]string{contact.Email}, subject, htmlContent, textContent,
		h.extractDomain(campaign.FromEmail), campaign.ID, contact.ID,
	).Scan(&emailID)
	if err != nil {
		return fmt.Errorf("failed to create email record: %w", err)
	}

	// Add delivery event
	h.db.ExecContext(ctx, `
		INSERT INTO delivery_events (email_id, event_type, data, occurred_at)
		VALUES ($1, 'queued', '{}', NOW())
	`, emailID)

	// Build email message
	fromHeader := campaign.FromEmail
	if campaign.FromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", campaign.FromName, campaign.FromEmail)
	}

	// Build MIME message
	boundary := uuid.New().String()
	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("From: %s\r\n", fromHeader))
	msg.WriteString(fmt.Sprintf("To: %s\r\n", contact.Email))
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	msg.WriteString(fmt.Sprintf("Message-ID: %s\r\n", messageID))
	msg.WriteString("MIME-Version: 1.0\r\n")

	// Add Reply-To if set
	if campaign.ReplyTo != "" {
		msg.WriteString(fmt.Sprintf("Reply-To: %s\r\n", campaign.ReplyTo))
	}

	// Add List-Unsubscribe header (RFC 8058)
	unsubToken := fmt.Sprintf("%d-%d-%d", contact.ID, campaign.OrgID, emailID)
	msg.WriteString(fmt.Sprintf("List-Unsubscribe: <%s/api/v1/unsubscribe/%s>\r\n", h.cfg.APIUrl, unsubToken))
	msg.WriteString(fmt.Sprintf("List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n"))

	if htmlContent != "" && textContent != "" {
		// Multipart alternative
		msg.WriteString(fmt.Sprintf("Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n", boundary))
		msg.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		msg.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\n\r\n")
		msg.WriteString(textContent)
		msg.WriteString(fmt.Sprintf("\r\n--%s\r\n", boundary))
		msg.WriteString("Content-Type: text/html; charset=\"utf-8\"\r\n\r\n")
		msg.WriteString(htmlContent)
		msg.WriteString(fmt.Sprintf("\r\n--%s--\r\n", boundary))
	} else if htmlContent != "" {
		msg.WriteString("Content-Type: text/html; charset=\"utf-8\"\r\n\r\n")
		msg.WriteString(htmlContent)
	} else {
		msg.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\n\r\n")
		msg.WriteString(textContent)
	}

	// Send via SMTP
	sendErr := h.sendViaSMTP(campaign.FromEmail, contact.Email, msg.String())
	if sendErr != nil {
		// Mark as failed
		h.db.ExecContext(ctx, `
			UPDATE emails SET status = 'failed', updated_at = NOW()
			WHERE id = $1
		`, emailID)

		h.db.ExecContext(ctx, `
			INSERT INTO delivery_events (email_id, event_type, data, occurred_at)
			VALUES ($1, 'failed', $2, NOW())
		`, emailID, fmt.Sprintf(`{"error": "%s"}`, sendErr.Error()))

		return sendErr
	}

	// Mark as sent
	h.db.ExecContext(ctx, `
		UPDATE emails SET status = 'sent', sent_at = NOW(), updated_at = NOW()
		WHERE id = $1
	`, emailID)

	h.db.ExecContext(ctx, `
		INSERT INTO delivery_events (email_id, event_type, data, occurred_at)
		VALUES ($1, 'sent', '{}', NOW())
	`, emailID)

	return nil
}

// sendViaSMTP sends an email via SMTP
func (h *CampaignHandler) sendViaSMTP(from, to, message string) error {
	addr := fmt.Sprintf("%s:%d", h.cfg.SMTPHost, h.cfg.SMTPPort)

	// Setup authentication if credentials provided
	var auth smtp.Auth
	if h.cfg.SMTPUser != "" && h.cfg.SMTPPassword != "" {
		auth = smtp.PlainAuth("", h.cfg.SMTPUser, h.cfg.SMTPPassword, h.cfg.SMTPHost)
	}

	// For TLS connections
	if h.cfg.SMTPTLS {
		return h.sendViaSMTPTLS(addr, auth, from, to, message)
	}

	// Standard SMTP
	return smtp.SendMail(addr, auth, from, []string{to}, []byte(message))
}

// sendViaSMTPTLS sends an email via SMTP with TLS
func (h *CampaignHandler) sendViaSMTPTLS(addr string, auth smtp.Auth, from, to, message string) error {
	// Connect to the server
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		ServerName: h.cfg.SMTPHost,
	})
	if err != nil {
		// Try STARTTLS instead
		return h.sendViaSMTPStartTLS(addr, auth, from, to, message)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, h.cfg.SMTPHost)
	if err != nil {
		return fmt.Errorf("failed to create SMTP client: %w", err)
	}
	defer client.Close()

	// Authenticate
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP auth failed: %w", err)
		}
	}

	// Send email
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("SMTP MAIL failed: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("SMTP RCPT failed: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA failed: %w", err)
	}

	_, err = w.Write([]byte(message))
	if err != nil {
		return fmt.Errorf("failed to write message: %w", err)
	}

	err = w.Close()
	if err != nil {
		return fmt.Errorf("failed to close data writer: %w", err)
	}

	return client.Quit()
}

// sendViaSMTPStartTLS sends an email via SMTP with STARTTLS
func (h *CampaignHandler) sendViaSMTPStartTLS(addr string, auth smtp.Auth, from, to, message string) error {
	client, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("failed to connect to SMTP: %w", err)
	}
	defer client.Close()

	// Send EHLO
	if err := client.Hello("localhost"); err != nil {
		return fmt.Errorf("SMTP EHLO failed: %w", err)
	}

	// Start TLS
	tlsConfig := &tls.Config{
		ServerName: h.cfg.SMTPHost,
	}
	if err := client.StartTLS(tlsConfig); err != nil {
		return fmt.Errorf("SMTP STARTTLS failed: %w", err)
	}

	// Authenticate
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP auth failed: %w", err)
		}
	}

	// Send email
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("SMTP MAIL failed: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("SMTP RCPT failed: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA failed: %w", err)
	}

	_, err = w.Write([]byte(message))
	if err != nil {
		return fmt.Errorf("failed to write message: %w", err)
	}

	err = w.Close()
	if err != nil {
		return fmt.Errorf("failed to close data writer: %w", err)
	}

	return client.Quit()
}

// personalizeContent replaces template variables with contact data
func (h *CampaignHandler) personalizeContent(content string, contact contactInfo) string {
	if content == "" {
		return content
	}

	// Replace standard variables
	content = strings.ReplaceAll(content, "{{email}}", contact.Email)
	content = strings.ReplaceAll(content, "{{firstName}}", contact.FirstName)
	content = strings.ReplaceAll(content, "{{lastName}}", contact.LastName)
	content = strings.ReplaceAll(content, "{{first_name}}", contact.FirstName)
	content = strings.ReplaceAll(content, "{{last_name}}", contact.LastName)

	// Replace custom attributes
	for key, value := range contact.Attributes {
		if strVal, ok := value.(string); ok {
			content = strings.ReplaceAll(content, "{{"+key+"}}", strVal)
		}
	}

	return content
}

// extractDomain extracts domain from email address
func (h *CampaignHandler) extractDomain(email string) string {
	parts := strings.Split(email, "@")
	if len(parts) == 2 {
		return parts[1]
	}
	return ""
}

// updateCampaignStatus updates campaign status
func (h *CampaignHandler) updateCampaignStatus(ctx context.Context, campaignID int, status string) {
	h.db.ExecContext(ctx, `
		UPDATE campaigns SET status = $1, updated_at = NOW()
		WHERE id = $2
	`, status, campaignID)
}

// updateCampaignProgress updates campaign progress
func (h *CampaignHandler) updateCampaignProgress(ctx context.Context, campaignID int, sentCount int) {
	h.db.ExecContext(ctx, `
		UPDATE campaigns SET sent_count = sent_count + $1, updated_at = NOW()
		WHERE id = $2
	`, sentCount, campaignID)
}

// completeCampaign marks a campaign as complete
func (h *CampaignHandler) completeCampaign(ctx context.Context, campaignID int) {
	h.db.ExecContext(ctx, `
		UPDATE campaigns SET status = 'sent', completed_at = NOW(), updated_at = NOW()
		WHERE id = $1
	`, campaignID)
}

// Warmup schedules - daily limits for each day
var warmupSchedules = map[string][]int{
	"conservative": {20, 50, 100, 200, 400, 600, 800, 1000, 1200, 1400, 1600, 1800, 2000, 2400, 2800, 3200, 4000, 5000, 6000, 7000, 8000, 9000, 10000, 12000, 14000, 16000, 18000, 20000, 25000, 30000},
	"moderate":     {50, 100, 300, 600, 1000, 1500, 2000, 3000, 4000, 5000, 6000, 8000, 10000, 12000, 15000, 18000, 22000, 27000, 35000, 45000, 60000},
	"aggressive":   {100, 500, 1000, 2000, 4000, 7000, 10000, 15000, 20000, 30000, 45000, 60000, 80000, 100000},
}

// getWarmupLimit returns the daily limit for an org in warmup, or 0 if not in warmup
func (h *CampaignHandler) getWarmupLimit(ctx context.Context, orgID int64) int {
	var scheduleName string
	var currentDay int
	var status string

	err := h.db.QueryRowContext(ctx, `
		SELECT schedule_name, current_day, status FROM warmup_progress
		WHERE org_id = $1 AND status = 'active'
		LIMIT 1
	`, orgID).Scan(&scheduleName, &currentDay, &status)

	if err != nil || status != "active" {
		return 0 // Not in warmup
	}

	schedule, ok := warmupSchedules[scheduleName]
	if !ok {
		schedule = warmupSchedules["conservative"]
	}

	if currentDay <= 0 || currentDay > len(schedule) {
		return 0 // Warmup completed
	}

	return schedule[currentDay-1]
}

// getDailySentCount returns the number of emails sent today for an org
func (h *CampaignHandler) getDailySentCount(ctx context.Context, orgID int64) int {
	var count int
	h.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM emails
		WHERE org_id = $1 AND created_at >= CURRENT_DATE
	`, orgID).Scan(&count)
	return count
}

// updateWarmupProgress advances warmup day if daily limit was reached
func (h *CampaignHandler) updateWarmupProgress(ctx context.Context, orgID int64) {
	// Get current warmup status
	var scheduleName string
	var currentDay int

	err := h.db.QueryRowContext(ctx, `
		SELECT schedule_name, current_day FROM warmup_progress
		WHERE org_id = $1 AND status = 'active'
	`, orgID).Scan(&scheduleName, &currentDay)

	if err != nil {
		return
	}

	schedule, ok := warmupSchedules[scheduleName]
	if !ok {
		return
	}

	// Check if we've completed the warmup
	if currentDay >= len(schedule) {
		h.db.ExecContext(ctx, `
			UPDATE warmup_progress SET status = 'completed', completed_at = NOW(), updated_at = NOW()
			WHERE org_id = $1 AND status = 'active'
		`, orgID)
		return
	}

	// Advance to next day (done automatically at midnight via cron, but can also be done here)
	// We advance the day when processing the first email of the new day
	h.db.ExecContext(ctx, `
		UPDATE warmup_progress
		SET current_day = current_day + 1, updated_at = NOW()
		WHERE org_id = $1 AND status = 'active' AND current_day < $2
		AND NOT EXISTS (
			SELECT 1 FROM emails WHERE org_id = $1 AND created_at >= CURRENT_DATE
		)
	`, orgID, len(schedule))
}

// createWarmupAlert creates an alert when warmup limit is reached
func (h *CampaignHandler) createWarmupAlert(ctx context.Context, orgID int64, limit int) {
	alertData, _ := json.Marshal(map[string]any{"dailyLimit": limit})
	h.db.ExecContext(ctx, `
		INSERT INTO alerts (org_id, type, severity, title, message, data, acknowledged, created_at)
		VALUES ($1, 'warmup', 'info', 'Warmup Daily Limit Reached',
			'Your campaign has been paused because the daily warmup limit has been reached. It will resume tomorrow.',
			$2, false, NOW())
	`, orgID, alertData)
}
