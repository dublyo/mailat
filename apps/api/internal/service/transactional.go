package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/dublyo/mailat/api/internal/worker"
)

// TransactionalService handles transactional email sending
type TransactionalService struct {
	db            *sql.DB
	cfg           *config.Config
	redis         *redis.Client
	queueClient   *worker.QueueClient
	emailProvider provider.EmailProvider
}

// NewTransactionalService creates a new transactional service
func NewTransactionalService(db *sql.DB, cfg *config.Config, redisClient *redis.Client) *TransactionalService {
	// Initialize queue client for async email sending
	queueClient, err := worker.NewQueueClient(cfg)
	if err != nil {
		fmt.Printf("Warning: failed to create queue client, falling back to sync sending: %v\n", err)
	}

	svc := &TransactionalService{
		db:          db,
		cfg:         cfg,
		redis:       redisClient,
		queueClient: queueClient,
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
			svc.emailProvider = sesProvider
			fmt.Println("TransactionalService initialized with AWS SES provider")
		}
	} else {
		svc.emailProvider = provider.NewSMTPProvider(&provider.SMTPConfig{
			Host:          cfg.SMTPHost,
			Port:          cfg.SMTPPort,
			Username:      cfg.SMTPUser,
			Password:      cfg.SMTPPassword,
			UseTLS:        cfg.SMTPTLS,
			SkipTLSVerify: !cfg.SMTPTLS,
		})
		fmt.Println("TransactionalService initialized with SMTP provider")
	}

	return svc
}

// SendEmail sends a single transactional email
func (s *TransactionalService) SendEmail(ctx context.Context, orgID int64, req *model.SendEmailRequest) (*model.SendEmailResponse, error) {
	return s.SendEmailForUser(ctx, orgID, 0, req)
}

// SendEmailForUser adds the actor boundary for HTTP users and user-bound API keys.
func (s *TransactionalService) SendEmailForUser(ctx context.Context, orgID, userID int64, req *model.SendEmailRequest) (*model.SendEmailResponse, error) {
	if s.emailProvider == nil {
		return nil, fmt.Errorf("email provider is not configured; sending has not been attempted")
	}

	// A request key is scoped to its organization and the complete request body.
	requestCopy := *req
	requestCopy.IdempotencyKey = ""
	requestBody, _ := json.Marshal(requestCopy)
	requestSum := sha256.Sum256(requestBody)
	requestHash := hex.EncodeToString(requestSum[:])
	if req.IdempotencyKey != "" {
		if len(req.IdempotencyKey) > 128 {
			return nil, fmt.Errorf("idempotency key is too long")
		}
	}

	// Validate sender domain ownership
	fromEmail := req.From
	fromAddress, err := mail.ParseAddress(fromEmail)
	if err != nil || strings.ContainsAny(fromEmail, "\r\n") {
		return nil, fmt.Errorf("invalid From address")
	}
	normalizedFrom := strings.ToLower(fromAddress.Address)
	domainName := extractDomain(normalizedFrom)
	if userID > 0 {
		var foreign bool
		if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM identities WHERE LOWER(email)=$1 AND user_id<>$2)`, normalizedFrom, userID).Scan(&foreign); err != nil {
			return nil, err
		}
		if foreign {
			return nil, fmt.Errorf("that From address belongs to another user")
		}
		var ownsDomain bool
		if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM identities i JOIN domains d ON d.id=i.domain_id WHERE i.user_id=$1 AND i.can_send=true AND d.org_id=$2 AND LOWER(d.name)=$3)`, userID, orgID, domainName).Scan(&ownsDomain); err != nil {
			return nil, err
		}
		if !ownsDomain {
			return nil, fmt.Errorf("no authorized sending identity for this domain")
		}
	}

	var domainID int64
	var domainStatus string
	var sesVerified bool
	err = s.db.QueryRowContext(ctx, `
		SELECT id, status, COALESCE(ses_verified,false) FROM domains WHERE LOWER(name) = $1 AND org_id = $2
	`, domainName, orgID).Scan(&domainID, &domainStatus, &sesVerified)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("sender domain not verified for your organization")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to verify sender domain: %w", err)
	}
	if domainStatus != "active" || (s.cfg.EmailProvider == "ses" && !sesVerified) {
		return nil, fmt.Errorf("sender domain is not active")
	}

	// Find identity for the sender
	var identityID int64
	var canSend bool
	err = s.db.QueryRowContext(ctx, `
		SELECT i.id, i.can_send FROM identities i
		JOIN users u ON i.user_id = u.id
		WHERE LOWER(i.email) = $1 AND u.org_id = $2
	`, normalizedFrom, orgID).Scan(&identityID, &canSend)
	if err == sql.ErrNoRows {
		// Create a system identity or use default
		identityID = 0 // Owned-domain aliases do not need a separate identity row.
	} else if err != nil {
		return nil, err
	} else if !canSend {
		return nil, fmt.Errorf("sending is disabled for this identity")
	}

	if req.IdempotencyKey != "" {
		existing, err := s.loadSubmission(ctx, orgID, req.IdempotencyKey, requestHash)
		if err != nil || existing != nil {
			return existing, err
		}
	}

	var scheduledFor *time.Time
	if req.ScheduledFor != nil {
		scheduled, err := time.Parse(time.RFC3339, *req.ScheduledFor)
		if err != nil || !scheduled.After(time.Now()) {
			return nil, fmt.Errorf("scheduledFor must be a future RFC3339 timestamp")
		}
		if s.queueClient == nil {
			return nil, fmt.Errorf("scheduled sending is unavailable without the job queue")
		}
		scheduledFor = &scheduled
	}

	// Check suppression list
	for _, to := range append(append(append([]string{}, req.To...), req.Cc...), req.Bcc...) {
		suppressed, err := s.isEmailSuppressed(ctx, orgID, to)
		if err != nil {
			return nil, fmt.Errorf("failed to check suppression list: %w", err)
		}
		if suppressed {
			return nil, fmt.Errorf("recipient %s is on suppression list", to)
		}
	}

	// Render template if provided
	subject := req.Subject
	htmlBody := req.HTML
	textBody := req.Text

	if req.TemplateID != "" {
		template, err := s.getTemplateByUUID(ctx, orgID, req.TemplateID)
		if err != nil {
			return nil, fmt.Errorf("template not found: %w", err)
		}
		subject = s.renderTemplate(template.Subject, req.Variables)
		htmlBody = s.renderTemplate(template.HTMLBody, req.Variables)
		textBody = s.renderTemplate(template.TextBody, req.Variables)
	} else if req.Variables != nil {
		// Apply variables to subject and body
		subject = s.renderTemplate(subject, req.Variables)
		htmlBody = s.renderTemplate(htmlBody, req.Variables)
		textBody = s.renderTemplate(textBody, req.Variables)
	}

	// Validate headers/recipient limits before persisting or reserving quota.
	if _, _, err = provider.BuildMailMIME(&provider.EmailMessage{From: fromEmail, To: req.To, Cc: req.Cc, Bcc: req.Bcc, ReplyTo: req.ReplyTo, Subject: subject, TextBody: textBody, HTMLBody: htmlBody}); err != nil {
		return nil, err
	}

	// Generate Message-ID
	messageID := s.generateMessageID(domainName)

	// Create email record
	emailUUID := uuid.New().String()
	var emailID int64

	tagsJSON, _ := json.Marshal(req.Tags)
	metadataJSON, _ := json.Marshal(req.Metadata)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if req.IdempotencyKey != "" {
		claim, err := tx.ExecContext(ctx, `INSERT INTO email_submission_keys(org_id,submission_key,request_hash) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, orgID, req.IdempotencyKey, requestHash)
		if err != nil {
			return nil, err
		}
		affected, _ := claim.RowsAffected()
		if affected == 0 {
			tx.Rollback()
			return s.loadSubmission(ctx, orgID, req.IdempotencyKey, requestHash)
		}
	}
	if err := s.checkRateLimits(ctx, orgID); err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `
  INSERT INTO transactional_emails (
			uuid, org_id, identity_id, message_id, from_address, to_addresses,
			cc_addresses, bcc_addresses, reply_to, subject, html_body, text_body,
			tags, metadata, status, idempotency_key, scheduled_for, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, NOW())
		RETURNING id
	`, emailUUID, orgID, identityID, messageID, fromEmail, strings.Join(req.To, ","),
		strings.Join(req.Cc, ","), strings.Join(req.Bcc, ","), req.ReplyTo,
		subject, htmlBody, textBody, string(tagsJSON), string(metadataJSON),
		"queued", req.IdempotencyKey, scheduledFor,
	).Scan(&emailID)
	if err != nil {
		return nil, fmt.Errorf("failed to create email record: %w", err)
	}

	// Create initial delivery event
	_, err = tx.ExecContext(ctx, `
		INSERT INTO transactional_delivery_events (email_id, event_type, details)
		VALUES ($1, 'queued', 'Email accepted for delivery')
	`, emailID)
	if err != nil {
		fmt.Printf("Warning: failed to create delivery event: %v\n", err)
	}

	if req.IdempotencyKey != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE email_submission_keys SET email_uuid=$3,status='ready' WHERE org_id=$1 AND submission_key=$2`, orgID, req.IdempotencyKey, emailUUID); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}

	// Queue for sending via asynq job queue
	if s.queueClient != nil {
		payload := worker.NewEmailSendPayload(emailID, orgID, fromEmail, req.To, subject, htmlBody, textBody, messageID)
		payload.Cc = req.Cc
		payload.Bcc = req.Bcc
		payload.ReplyTo = req.ReplyTo

		// Check for scheduled sending
		if scheduledFor != nil {
			_, err = s.queueClient.EnqueueEmailSendScheduled(payload, *scheduledFor)
			if err != nil {
				// Queue uncertainty must never change the requested delivery time.
				_, _ = s.db.ExecContext(context.Background(), `UPDATE transactional_emails SET status='failed',updated_at=NOW() WHERE id=$1 AND status='queued'`, emailID)
				return &model.SendEmailResponse{ID: emailUUID, MessageID: messageID, Status: "failed", AcceptedAt: time.Now()}, nil
			}
		} else {
			_, err = s.queueClient.EnqueueEmailSend(payload)
			if err != nil {
				fmt.Printf("Warning: failed to enqueue email: %v\n", err)
				// Fallback to sync sending
				go s.processEmail(context.Background(), emailID, fromEmail, req.To, req.Cc, req.Bcc, subject, htmlBody, textBody, messageID, req.ReplyTo)
			}
		}
	} else {
		// Fallback to goroutine if queue client not available
		go s.processEmail(context.Background(), emailID, fromEmail, req.To, req.Cc, req.Bcc, subject, htmlBody, textBody, messageID, req.ReplyTo)
	}

	response := &model.SendEmailResponse{
		ID:         emailUUID,
		MessageID:  messageID,
		Status:     "queued",
		AcceptedAt: time.Now(),
	}

	return response, nil
}

// BatchSendEmail sends multiple emails in batch
func (s *TransactionalService) BatchSendEmail(ctx context.Context, orgID int64, req *model.BatchSendRequest) (*model.BatchSendResponse, error) {
	return s.BatchSendEmailForUser(ctx, orgID, 0, req)
}
func (s *TransactionalService) BatchSendEmailForUser(ctx context.Context, orgID, userID int64, req *model.BatchSendRequest) (*model.BatchSendResponse, error) {
	if len(req.Emails) > 100 {
		return nil, fmt.Errorf("batch size exceeds maximum of 100 emails")
	}

	results := make([]model.BatchEmailResult, len(req.Emails))

	for i, emailReq := range req.Emails {
		resp, err := s.SendEmailForUser(ctx, orgID, userID, &emailReq)
		if err != nil {
			results[i] = model.BatchEmailResult{
				Index:  i,
				Status: "failed",
				Error:  err.Error(),
			}
		} else {
			results[i] = model.BatchEmailResult{
				Index:     i,
				ID:        resp.ID,
				MessageID: resp.MessageID,
				Status:    resp.Status,
			}
		}
	}

	return &model.BatchSendResponse{Results: results}, nil
}

// GetEmailStatus retrieves the status of a sent email
func (s *TransactionalService) GetEmailStatus(ctx context.Context, orgID int64, emailUUID string) (*model.GetEmailStatusResponse, error) {
	var email struct {
		ID          int64
		MessageID   string
		From        string
		To          string
		Subject     string
		Status      string
		CreatedAt   time.Time
		SentAt      sql.NullTime
		DeliveredAt sql.NullTime
	}

	err := s.db.QueryRowContext(ctx, `
		SELECT id, message_id, from_address, to_addresses, subject, status,
		       created_at, sent_at, delivered_at
		FROM transactional_emails
		WHERE uuid = $1 AND org_id = $2
	`, emailUUID, orgID).Scan(
		&email.ID, &email.MessageID, &email.From, &email.To, &email.Subject,
		&email.Status, &email.CreatedAt, &email.SentAt, &email.DeliveredAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("email not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get email: %w", err)
	}

	// Get delivery events
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, email_id, event_type, created_at, details, ip_address, user_agent
		FROM transactional_delivery_events
		WHERE email_id = $1
		ORDER BY created_at ASC
	`, email.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get delivery events: %w", err)
	}
	defer rows.Close()

	var events []model.DeliveryEvent
	for rows.Next() {
		var event model.DeliveryEvent
		var ipAddr, userAgent sql.NullString
		if err := rows.Scan(&event.ID, &event.EmailID, &event.EventType, &event.Timestamp, &event.Details, &ipAddr, &userAgent); err != nil {
			continue
		}
		if ipAddr.Valid {
			event.IPAddress = ipAddr.String
		}
		if userAgent.Valid {
			event.UserAgent = userAgent.String
		}
		events = append(events, event)
	}

	response := &model.GetEmailStatusResponse{
		ID:        emailUUID,
		MessageID: email.MessageID,
		From:      email.From,
		To:        strings.Split(email.To, ","),
		Subject:   email.Subject,
		Status:    email.Status,
		Events:    events,
		CreatedAt: email.CreatedAt,
	}

	if email.SentAt.Valid {
		response.SentAt = &email.SentAt.Time
	}
	if email.DeliveredAt.Valid {
		response.DeliveredAt = &email.DeliveredAt.Time
	}

	return response, nil
}

// CancelEmail cancels a scheduled email
func (s *TransactionalService) CancelEmail(ctx context.Context, orgID int64, emailUUID string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE transactional_emails
		SET status = 'cancelled', updated_at = NOW()
		WHERE uuid = $1 AND org_id = $2 AND status = 'queued'
	`, emailUUID, orgID)
	if err != nil {
		return fmt.Errorf("failed to cancel email: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("email not found or cannot be cancelled")
	}

	return nil
}

// Template methods

// CreateTemplate creates a new email template
func (s *TransactionalService) CreateTemplate(ctx context.Context, orgID int64, req *model.CreateTemplateRequest) (*model.EmailTemplate, error) {
	templateUUID := uuid.New().String()

	// Extract variables from template
	variables := s.extractVariables(req.Subject + req.HTML + req.Text)
	variablesJSON, _ := json.Marshal(variables)

	var template model.EmailTemplate
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO email_templates (uuid, org_id, name, description, subject, html_body, text_body, variables, is_active, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, true, NOW())
		RETURNING id, uuid, org_id, name, description, subject, html_body, text_body, is_active, created_at, updated_at
	`, templateUUID, orgID, req.Name, req.Description, req.Subject, req.HTML, req.Text, string(variablesJSON)).Scan(
		&template.ID, &template.UUID, &template.OrgID, &template.Name, &template.Description,
		&template.Subject, &template.HTMLBody, &template.TextBody, &template.IsActive,
		&template.CreatedAt, &template.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create template: %w", err)
	}

	template.Variables = variables
	return &template, nil
}

// GetTemplate retrieves a template by UUID
func (s *TransactionalService) GetTemplate(ctx context.Context, orgID int64, templateUUID string) (*model.EmailTemplate, error) {
	return s.getTemplateByUUID(ctx, orgID, templateUUID)
}

// ListTemplates returns all templates for an organization
func (s *TransactionalService) ListTemplates(ctx context.Context, orgID int64) ([]*model.EmailTemplate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, uuid, org_id, name, description, subject, html_body, text_body, variables, is_active, created_at, updated_at
		FROM email_templates
		WHERE org_id = $1
		ORDER BY name ASC
	`, orgID)
	if err != nil {
		return nil, fmt.Errorf("failed to list templates: %w", err)
	}
	defer rows.Close()

	var templates []*model.EmailTemplate
	for rows.Next() {
		var template model.EmailTemplate
		var variablesJSON string
		var desc sql.NullString
		if err := rows.Scan(&template.ID, &template.UUID, &template.OrgID, &template.Name, &desc,
			&template.Subject, &template.HTMLBody, &template.TextBody, &variablesJSON,
			&template.IsActive, &template.CreatedAt, &template.UpdatedAt); err != nil {
			continue
		}
		if desc.Valid {
			template.Description = desc.String
		}
		json.Unmarshal([]byte(variablesJSON), &template.Variables)
		templates = append(templates, &template)
	}

	return templates, nil
}

// UpdateTemplate updates a template
func (s *TransactionalService) UpdateTemplate(ctx context.Context, orgID int64, templateUUID string, req *model.UpdateTemplateRequest) (*model.EmailTemplate, error) {
	// Build dynamic update query
	updates := []string{}
	args := []interface{}{}
	argIndex := 1

	if req.Name != "" {
		updates = append(updates, fmt.Sprintf("name = $%d", argIndex))
		args = append(args, req.Name)
		argIndex++
	}
	if req.Description != "" {
		updates = append(updates, fmt.Sprintf("description = $%d", argIndex))
		args = append(args, req.Description)
		argIndex++
	}
	if req.Subject != "" {
		updates = append(updates, fmt.Sprintf("subject = $%d", argIndex))
		args = append(args, req.Subject)
		argIndex++
	}
	if req.HTML != "" {
		updates = append(updates, fmt.Sprintf("html_body = $%d", argIndex))
		args = append(args, req.HTML)
		argIndex++
	}
	if req.Text != "" {
		updates = append(updates, fmt.Sprintf("text_body = $%d", argIndex))
		args = append(args, req.Text)
		argIndex++
	}
	if req.IsActive != nil {
		updates = append(updates, fmt.Sprintf("is_active = $%d", argIndex))
		args = append(args, *req.IsActive)
		argIndex++
	}

	if len(updates) == 0 {
		return s.GetTemplate(ctx, orgID, templateUUID)
	}

	updates = append(updates, "updated_at = NOW()")

	query := fmt.Sprintf(`
		UPDATE email_templates SET %s
		WHERE uuid = $%d AND org_id = $%d
	`, strings.Join(updates, ", "), argIndex, argIndex+1)
	args = append(args, templateUUID, orgID)

	_, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to update template: %w", err)
	}

	return s.GetTemplate(ctx, orgID, templateUUID)
}

// DeleteTemplate deletes a template
func (s *TransactionalService) DeleteTemplate(ctx context.Context, orgID int64, templateUUID string) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM email_templates WHERE uuid = $1 AND org_id = $2
	`, templateUUID, orgID)
	if err != nil {
		return fmt.Errorf("failed to delete template: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("template not found")
	}

	return nil
}

// PreviewTemplate renders a template with variables
func (s *TransactionalService) PreviewTemplate(ctx context.Context, orgID int64, templateUUID string, variables map[string]string) (*model.PreviewTemplateResponse, error) {
	template, err := s.GetTemplate(ctx, orgID, templateUUID)
	if err != nil {
		return nil, err
	}

	return &model.PreviewTemplateResponse{
		Subject: s.renderTemplate(template.Subject, variables),
		HTML:    s.renderTemplate(template.HTMLBody, variables),
		Text:    s.renderTemplate(template.TextBody, variables),
	}, nil
}

// Helper methods

func (s *TransactionalService) getTemplateByUUID(ctx context.Context, orgID int64, templateUUID string) (*model.EmailTemplate, error) {
	var template model.EmailTemplate
	var variablesJSON string
	var desc sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, uuid, org_id, name, description, subject, html_body, text_body, variables, is_active, created_at, updated_at
		FROM email_templates
		WHERE uuid = $1 AND org_id = $2
	`, templateUUID, orgID).Scan(
		&template.ID, &template.UUID, &template.OrgID, &template.Name, &desc,
		&template.Subject, &template.HTMLBody, &template.TextBody, &variablesJSON,
		&template.IsActive, &template.CreatedAt, &template.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("template not found")
	}
	if err != nil {
		return nil, err
	}

	if desc.Valid {
		template.Description = desc.String
	}
	json.Unmarshal([]byte(variablesJSON), &template.Variables)

	return &template, nil
}

func (s *TransactionalService) renderTemplate(template string, variables map[string]string) string {
	if variables == nil {
		return template
	}

	result := template
	for key, value := range variables {
		placeholder := "{{" + key + "}}"
		result = strings.ReplaceAll(result, placeholder, value)
	}
	return result
}

func (s *TransactionalService) extractVariables(content string) []string {
	re := regexp.MustCompile(`\{\{(\w+)\}\}`)
	matches := re.FindAllStringSubmatch(content, -1)

	seen := make(map[string]bool)
	var variables []string
	for _, match := range matches {
		if len(match) > 1 && !seen[match[1]] {
			seen[match[1]] = true
			variables = append(variables, match[1])
		}
	}
	return variables
}

func (s *TransactionalService) generateMessageID(domain string) string {
	randomBytes := make([]byte, 16)
	rand.Read(randomBytes)
	return fmt.Sprintf("<%s@%s>", hex.EncodeToString(randomBytes), domain)
}

func extractDomain(email string) string {
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return ""
	}
	return parts[1]
}

func (s *TransactionalService) checkRateLimits(ctx context.Context, orgID int64) error {
	return reserveMonthlySend(ctx, s.db, s.cfg, orgID)
}

func (s *TransactionalService) isEmailSuppressed(ctx context.Context, orgID int64, email string) (bool, error) {
	parsed, err := mail.ParseAddress(email)
	if err != nil {
		return false, fmt.Errorf("invalid recipient email address")
	}
	email = parsed.Address
	var exists bool
	err = s.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM suppression_list
			WHERE org_id = $1 AND email = $2
		)
	`, orgID, strings.ToLower(email)).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// loadSubmission cannot return another organization's cached response.
func (s *TransactionalService) loadSubmission(ctx context.Context, orgID int64, key, hash string) (*model.SendEmailResponse, error) {
	var result model.SendEmailResponse
	var storedHash string
	err := s.db.QueryRowContext(ctx, `SELECT k.request_hash,e.uuid,e.message_id,e.status,e.created_at FROM email_submission_keys k JOIN transactional_emails e ON e.uuid=k.email_uuid AND e.org_id=k.org_id WHERE k.org_id=$1 AND k.submission_key=$2`, orgID, key).Scan(&storedHash, &result.ID, &result.MessageID, &result.Status, &result.AcceptedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if storedHash != hash {
		return nil, fmt.Errorf("idempotency key was used with different content")
	}
	return &result, nil
}

func (s *TransactionalService) processEmail(ctx context.Context, emailID int64, from string, to, cc, bcc []string, subject, htmlBody, textBody, messageID, replyTo string) {
	// Claim once. A previous sending/unknown attempt must never be blindly repeated.
	result, err := s.db.ExecContext(ctx, `UPDATE transactional_emails SET status='sending',updated_at=NOW() WHERE id=$1 AND status='queued'`, emailID)
	if err != nil {
		return
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return
	}

	// Build email message for provider
	emailMsg := &provider.EmailMessage{
		From:      from,
		To:        to,
		Cc:        cc,
		Bcc:       bcc,
		Subject:   subject,
		HTMLBody:  htmlBody,
		TextBody:  textBody,
		MessageID: messageID,
		ReplyTo:   replyTo,
	}

	// Send via email provider (SES or SMTP)
	sendResult, err := s.emailProvider.SendEmail(ctx, emailMsg)

	if err != nil {
		// Update status to failed
		s.db.ExecContext(ctx, `
			UPDATE transactional_emails SET status = $2, updated_at = NOW() WHERE id = $1 AND status='sending'
		`, emailID, sendFailureStatus(err))
		s.db.ExecContext(ctx, `
			INSERT INTO transactional_delivery_events (email_id, event_type, details) VALUES ($1, 'failed', $2)
		`, emailID, err.Error())
		fmt.Printf("Failed to send email %d: %v\n", emailID, err)

		// Trigger webhook for failed event
		s.triggerWebhooks(ctx, emailID, "email.failed", map[string]any{"error": err.Error()})
		return
	}

	// Update status to sent with provider info
	providerMsgID := ""
	if sendResult != nil && sendResult.MessageID != "" {
		providerMsgID = sendResult.MessageID
	}
	s.db.ExecContext(ctx, `
		UPDATE transactional_emails
		SET status = CASE WHEN status IN ('delivered','bounced','complained','opened','clicked') THEN status ELSE 'sent' END, sent_at = COALESCE(sent_at,NOW()), provider_message_id = $2, email_provider = $3, updated_at = NOW()
		WHERE id = $1
	`, emailID, providerMsgID, s.emailProvider.Name())
	s.db.ExecContext(ctx, `
		INSERT INTO transactional_delivery_events (email_id, event_type, details) VALUES ($1, 'sent', $2)
	`, emailID, fmt.Sprintf("Email sent via %s", s.emailProvider.Name()))

	// Trigger webhook for sent event
	s.triggerWebhooks(ctx, emailID, "email.sent", nil)
}

// triggerWebhooks enqueues webhook deliveries for an event
func (s *TransactionalService) triggerWebhooks(ctx context.Context, emailID int64, eventType string, data map[string]any) {
	// Get org_id for this email
	var orgID int64
	err := s.db.QueryRowContext(ctx, `SELECT org_id FROM transactional_emails WHERE id = $1`, emailID).Scan(&orgID)
	if err != nil {
		return
	}

	// Get active webhooks for this org that listen to this event type
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, url, secret FROM webhooks
		WHERE org_id = $1 AND active = true AND $2 = ANY(events)
	`, orgID, eventType)
	if err != nil {
		return
	}
	defer rows.Close()

	// Queue client for webhook delivery
	if s.queueClient == nil {
		return
	}

	for rows.Next() {
		var webhookID int64
		var url, secret string
		if err := rows.Scan(&webhookID, &url, &secret); err != nil {
			continue
		}

		webhookPayload := &worker.WebhookDeliverPayload{
			WebhookID:  webhookID,
			OrgID:      orgID,
			URL:        url,
			Secret:     secret,
			EventType:  eventType,
			EmailID:    emailID,
			Payload:    data,
			MaxRetries: 5,
		}

		s.queueClient.EnqueueWebhookDeliver(webhookPayload)
	}
}

func sendFailureStatus(err error) string {
	if provider.IsDefinitiveSendError(err) {
		return "failed"
	}
	return "unknown"
}
