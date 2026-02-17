package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
)

// ReceivingService handles email receiving operations
type ReceivingService struct {
	db                *sql.DB
	receivingProvider *provider.ReceivingProvider
}

// NewReceivingService creates a new receiving service
func NewReceivingService(db *sql.DB, region, accessKeyID, secretAccessKey, webhookBaseURL string) (*ReceivingService, error) {
	cfg := &provider.ReceivingConfig{
		Region:          region,
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
		WebhookBaseURL:  webhookBaseURL,
	}

	rp, err := provider.NewReceivingProvider(cfg)
	if err != nil {
		return nil, err
	}

	return &ReceivingService{
		db:                db,
		receivingProvider: rp,
	}, nil
}

// SetupDomainReceiving sets up email receiving for a domain
func (s *ReceivingService) SetupDomainReceiving(ctx context.Context, orgID int64, domainID int64) (*model.SetupReceivingResponse, error) {
	// Get domain
	var domain model.Domain
	err := s.db.QueryRowContext(ctx,
		`SELECT id, uuid, org_id, name, status, receiving_enabled FROM domains WHERE id = $1 AND org_id = $2`,
		domainID, orgID,
	).Scan(&domain.ID, &domain.UUID, &domain.OrgID, &domain.Name, &domain.Status, &domain.ReceivingEnabled)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("domain not found")
		}
		return nil, fmt.Errorf("domain not found: %w", err)
	}

	// Check if already set up
	if domain.ReceivingEnabled {
		return nil, fmt.Errorf("receiving is already enabled for this domain")
	}

	// Check if org has existing receiving config
	var existingConfig struct {
		S3Bucket       sql.NullString
		S3Region       sql.NullString
		SNSTopicArn    sql.NullString
		SESRuleSetName sql.NullString
		WebhookSecret  sql.NullString
	}
	err = s.db.QueryRowContext(ctx,
		`SELECT s3_bucket, s3_region, sns_topic_arn, ses_rule_set_name, webhook_secret FROM receiving_configs WHERE org_id = $1`,
		orgID,
	).Scan(&existingConfig.S3Bucket, &existingConfig.S3Region, &existingConfig.SNSTopicArn, &existingConfig.SESRuleSetName, &existingConfig.WebhookSecret)

	var result *provider.ReceivingSetupResult
	var webhookSecret string

	if err == nil && existingConfig.S3Bucket.Valid && existingConfig.S3Bucket.String != "" {
		// Use existing setup, just add a rule for this domain
		log.Printf("Using existing receiving config for org %d", orgID)
		err = s.receivingProvider.AddDomainToReceiving(ctx, existingConfig.SESRuleSetName.String, domain.Name, existingConfig.S3Bucket.String, existingConfig.SNSTopicArn.String)
		if err != nil {
			return nil, fmt.Errorf("failed to add domain to receiving: %w", err)
		}

		result = &provider.ReceivingSetupResult{
			S3Bucket:    existingConfig.S3Bucket.String,
			S3Region:    existingConfig.S3Region.String,
			SNSTopicArn: existingConfig.SNSTopicArn.String,
			RuleSetName: existingConfig.SESRuleSetName.String,
			RuleName:    fmt.Sprintf("receive-%s", strings.ReplaceAll(domain.Name, ".", "-")),
		}

		webhookSecret = existingConfig.WebhookSecret.String
	} else {
		// Create new setup
		log.Printf("Creating new receiving setup for org %d", orgID)
		result, err = s.receivingProvider.SetupReceiving(ctx, orgID, domain.Name)
		if err != nil {
			return nil, fmt.Errorf("failed to setup receiving: %w", err)
		}
		webhookSecret = result.WebhookSecret

		// Save receiving config
		_, err = s.db.ExecContext(ctx,
			`INSERT INTO receiving_configs (org_id, s3_bucket, s3_region, sns_topic_arn, ses_rule_set_name, webhook_secret, status, setup_completed_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			orgID, result.S3Bucket, result.S3Region, result.SNSTopicArn, result.RuleSetName, webhookSecret, "active", time.Now(),
		)
		if err != nil {
			log.Printf("Failed to save receiving config: %v", err)
		}
	}

	// Update domain with receiving info
	_, err = s.db.ExecContext(ctx,
		`UPDATE domains SET
			receiving_enabled = true,
			receiving_s3_bucket = $1,
			receiving_sns_topic_arn = $2,
			receiving_rule_set_name = $3,
			receiving_rule_name = $4,
			receiving_setup_at = $5
		 WHERE id = $6`,
		result.S3Bucket, result.SNSTopicArn, result.RuleSetName, result.RuleName, time.Now(), domainID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update domain: %w", err)
	}

	// Auto-add MX record for inbound receiving to domain_dns_records
	// so it appears in the DNS records list and zone file download
	mxValue := fmt.Sprintf("10 inbound-smtp.%s.amazonaws.com", result.S3Region)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO domain_dns_records (domain_id, record_type, hostname, expected_value, verified)
		 VALUES ($1, 'MX', $2, $3, false)
		 ON CONFLICT DO NOTHING`,
		domainID, domain.Name, mxValue,
	)
	if err != nil {
		log.Printf("Warning: Failed to insert MX DNS record: %v", err)
	}

	// Generate required DNS records for receiving
	mxRecords := []model.DomainDNSRecord{
		{
			RecordType: "MX",
			Hostname:   domain.Name,
			Value:      mxValue,
		},
	}

	return &model.SetupReceivingResponse{
		Success:     true,
		S3Bucket:    result.S3Bucket,
		SNSTopicArn: result.SNSTopicArn,
		RuleSetName: result.RuleSetName,
		RuleName:    result.RuleName,
		WebhookURL:  result.WebhookURL,
		RequiredDNS: mxRecords,
	}, nil
}

// ProcessIncomingEmail processes an incoming email notification from SNS
func (s *ReceivingService) ProcessIncomingEmail(ctx context.Context, notification *model.SESNotification) error {
	log.Printf("Processing incoming email: %s", notification.Mail.MessageId)

	// Only handle received notifications
	if notification.NotificationType != "Received" {
		log.Printf("Skipping notification type: %s", notification.NotificationType)
		return nil
	}

	receipt := notification.Receipt
	if receipt == nil {
		return fmt.Errorf("no receipt in notification")
	}

	// Get the S3 location
	action := receipt.Action
	if action.Type != "S3" {
		log.Printf("Unexpected action type: %s", action.Type)
	}

	// Find the recipient identity
	var identity struct {
		ID       int64
		DomainID int64
		OrgID    int64
		Email    string
	}

	for _, recipient := range receipt.Recipients {
		// Try to find exact match first
		err := s.db.QueryRowContext(ctx,
			`SELECT i.id, i.domain_id, d.org_id, i.email
			 FROM identities i
			 LEFT JOIN domains d ON i.domain_id = d.id
			 WHERE i.email = $1`,
			strings.ToLower(recipient),
		).Scan(&identity.ID, &identity.DomainID, &identity.OrgID, &identity.Email)

		if err == nil && identity.ID > 0 {
			break
		}

		// Try catch-all
		parts := strings.SplitN(recipient, "@", 2)
		if len(parts) == 2 {
			domain := parts[1]
			err = s.db.QueryRowContext(ctx,
				`SELECT i.id, i.domain_id, d.org_id, i.email
				 FROM identities i
				 LEFT JOIN domains d ON i.domain_id = d.id
				 WHERE d.name = $1 AND i.is_catch_all = true`,
				domain,
			).Scan(&identity.ID, &identity.DomainID, &identity.OrgID, &identity.Email)

			if err == nil && identity.ID > 0 {
				break
			}
		}
	}

	if identity.ID == 0 {
		log.Printf("No identity found for recipients: %v", receipt.Recipients)
		return fmt.Errorf("no identity found for recipients")
	}

	// Get domain info for S3 bucket
	var receivingS3Bucket sql.NullString
	var domainName string
	err := s.db.QueryRowContext(ctx,
		`SELECT receiving_s3_bucket, name FROM domains WHERE id = $1`,
		identity.DomainID,
	).Scan(&receivingS3Bucket, &domainName)
	if err != nil {
		return fmt.Errorf("failed to get domain: %w", err)
	}

	// Determine S3 bucket and key
	s3Bucket := action.BucketName
	s3Key := action.ObjectKey
	if s3Bucket == "" && receivingS3Bucket.Valid {
		s3Bucket = receivingS3Bucket.String
	}
	if s3Key == "" && domainName != "" {
		// Construct S3 key from domain prefix and message ID
		s3Key = fmt.Sprintf("incoming/%s/%s", domainName, notification.Mail.MessageId)
	}

	// Parse email headers
	headers := notification.Mail.CommonHeaders

	// Create snippet from subject
	snippet := headers.Subject
	if len(snippet) > 200 {
		snippet = snippet[:200] + "..."
	}

	// Generate thread ID from In-Reply-To or References
	threadID := generateThreadID(notification.Mail.Headers)

	// Prepare spam verdict values
	isSpam := receipt.SpamVerdict.Status == "FAIL"
	folder := "inbox"
	if isSpam {
		folder = "spam"
	}

	// Insert the email
	var emailID int64
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO received_emails (
			org_id, domain_id, identity_id, message_id, thread_id,
			from_email, from_name, to_emails, cc_emails, subject, snippet,
			raw_s3_bucket, raw_s3_key, folder, is_read, is_starred, is_spam,
			spam_verdict, virus_verdict, spf_verdict, dkim_verdict, dmarc_verdict,
			ses_message_id, received_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10, $11,
			$12, $13, $14, $15, $16, $17,
			$18, $19, $20, $21, $22,
			$23, $24
		) RETURNING id`,
		identity.OrgID, identity.DomainID, identity.ID, headers.MessageId, threadID,
		extractEmail(headers.From), extractName(headers.From), pq.Array(headers.To), pq.Array(headers.Cc), headers.Subject, snippet,
		s3Bucket, s3Key, folder, false, false, isSpam,
		receipt.SpamVerdict.Status, receipt.VirusVerdict.Status, receipt.SPFVerdict.Status, receipt.DKIMVerdict.Status, receipt.DMARCVerdict.Status,
		notification.Mail.MessageId, parseTimestamp(receipt.Timestamp),
	).Scan(&emailID)

	if err != nil {
		// Check for duplicate
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			log.Printf("Email already processed: %s", headers.MessageId)
			return nil
		}
		return fmt.Errorf("failed to insert email: %w", err)
	}

	log.Printf("Created received email record: %d", emailID)

	// Parse email body from S3 (async)
	go s.parseEmailBody(context.Background(), emailID, s3Bucket, s3Key)

	// Apply filters
	go s.applyFilters(context.Background(), identity.OrgID, emailID)

	return nil
}

// parseEmailBody fetches and parses the email body from S3
func (s *ReceivingService) parseEmailBody(ctx context.Context, emailID int64, bucket, key string) {
	log.Printf("Parsing email body for email %d from s3://%s/%s", emailID, bucket, key)

	// Fetch from S3
	rawEmail, err := s.receivingProvider.GetEmailFromS3(ctx, bucket, key)
	if err != nil {
		log.Printf("Failed to fetch email from S3: %v", err)
		return
	}

	// Parse MIME message
	msg, err := mail.ReadMessage(bytes.NewReader(rawEmail))
	if err != nil {
		log.Printf("Failed to parse email: %v", err)
		return
	}

	// Extract body and attachments
	textBody, htmlBody, attachments, err := s.parseEmailParts(ctx, msg)
	if err != nil {
		log.Printf("Failed to parse email parts: %v", err)
		return
	}

	// Create snippet from text body
	snippet := ""
	if textBody != "" {
		snippet = textBody
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}
	}

	// Update email record
	_, err = s.db.ExecContext(ctx,
		`UPDATE received_emails SET
			text_body = $1,
			html_body = $2,
			size_bytes = $3,
			has_attachments = $4,
			snippet = CASE WHEN $5 != '' THEN $5 ELSE snippet END
		 WHERE id = $6`,
		textBody, htmlBody, len(rawEmail), len(attachments) > 0, snippet, emailID,
	)
	if err != nil {
		log.Printf("Failed to update email: %v", err)
	}

	// Save attachments
	for _, att := range attachments {
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO email_attachments (
				received_email_id, filename, content_type, size_bytes,
				s3_key, s3_bucket, content_id, is_inline, checksum
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			emailID, att.Filename, att.ContentType, att.SizeBytes,
			att.S3Key, att.S3Bucket, att.ContentID, att.IsInline, att.Checksum,
		)
		if err != nil {
			log.Printf("Failed to save attachment: %v", err)
		}
	}
}

// AttachmentInfo contains parsed attachment information
type AttachmentInfo struct {
	Filename    string
	ContentType string
	SizeBytes   int
	S3Key       string
	S3Bucket    string
	ContentID   string
	IsInline    bool
	Checksum    string
}

// parseEmailParts parses the MIME parts of an email
func (s *ReceivingService) parseEmailParts(ctx context.Context, msg *mail.Message) (textBody, htmlBody string, attachments []AttachmentInfo, err error) {
	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		// Assume plain text
		body, _ := io.ReadAll(msg.Body)
		return string(body), "", nil, nil
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		mr := multipart.NewReader(msg.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return textBody, htmlBody, attachments, err
			}

			partMediaType, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
			disposition, dispParams, _ := mime.ParseMediaType(p.Header.Get("Content-Disposition"))

			partBody, err := io.ReadAll(p)
			if err != nil {
				continue
			}

			switch {
			case partMediaType == "text/plain" && disposition != "attachment":
				textBody = string(partBody)
			case partMediaType == "text/html" && disposition != "attachment":
				htmlBody = string(partBody)
			case disposition == "attachment" || disposition == "inline":
				filename := dispParams["filename"]
				if filename == "" {
					filename = p.FileName()
				}
				if filename == "" {
					filename = "attachment"
				}

				// Calculate checksum
				hash := sha256.Sum256(partBody)
				checksum := hex.EncodeToString(hash[:])

				attachments = append(attachments, AttachmentInfo{
					Filename:    filename,
					ContentType: partMediaType,
					SizeBytes:   len(partBody),
					ContentID:   strings.Trim(p.Header.Get("Content-ID"), "<>"),
					IsInline:    disposition == "inline",
					Checksum:    checksum,
					// Note: S3 upload would happen here in production
				})
			}
		}
	} else if mediaType == "text/plain" {
		body, _ := io.ReadAll(msg.Body)
		textBody = string(body)
	} else if mediaType == "text/html" {
		body, _ := io.ReadAll(msg.Body)
		htmlBody = string(body)
	}

	return textBody, htmlBody, attachments, nil
}

// applyFilters applies user-defined filters to an email
func (s *ReceivingService) applyFilters(ctx context.Context, orgID, emailID int64) {
	// Get email
	var email model.ReceivedEmail
	var toEmailsArr, ccEmailsArr pq.StringArray
	var textBody sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, org_id, domain_id, identity_id, from_email, to_emails, cc_emails,
		        subject, text_body, has_attachments, folder, is_starred, is_read
		 FROM received_emails WHERE id = $1`,
		emailID,
	).Scan(&email.ID, &email.OrgID, &email.DomainID, &email.IdentityID, &email.FromEmail,
		&toEmailsArr, &ccEmailsArr, &email.Subject, &textBody, &email.HasAttachments,
		&email.Folder, &email.IsStarred, &email.IsRead)
	if err != nil {
		log.Printf("Failed to get email for filtering: %v", err)
		return
	}
	email.TextBody = textBody.String
	email.ToEmails = []string(toEmailsArr)
	email.CcEmails = []string(ccEmailsArr)

	// Get applicable filters
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, conditions, condition_logic, action_labels, action_folder,
		        action_star, action_mark_read, action_archive, action_trash
		 FROM inbox_filters
		 WHERE org_id = $1 AND active = true AND (identity_id IS NULL OR identity_id = $2)
		 ORDER BY priority DESC`,
		orgID, email.IdentityID,
	)
	if err != nil {
		log.Printf("Failed to get filters: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var filter struct {
			ID             int64
			Conditions     string
			ConditionLogic string
			ActionLabels   sql.NullString
			ActionFolder   sql.NullString
			ActionStar     bool
			ActionMarkRead bool
			ActionArchive  bool
			ActionTrash    bool
		}

		err := rows.Scan(&filter.ID, &filter.Conditions, &filter.ConditionLogic, &filter.ActionLabels,
			&filter.ActionFolder, &filter.ActionStar, &filter.ActionMarkRead, &filter.ActionArchive, &filter.ActionTrash)
		if err != nil {
			log.Printf("Failed to scan filter: %v", err)
			continue
		}

		var conditions []model.FilterCondition
		json.Unmarshal([]byte(filter.Conditions), &conditions)

		if s.matchesFilter(email, conditions, filter.ConditionLogic) {
			// Apply actions based on what's set
			if filter.ActionFolder.Valid && filter.ActionFolder.String != "" {
				s.db.ExecContext(ctx, `UPDATE received_emails SET folder = $1 WHERE id = $2`, filter.ActionFolder.String, emailID)
			}
			if filter.ActionStar {
				s.db.ExecContext(ctx, `UPDATE received_emails SET is_starred = true WHERE id = $1`, emailID)
			}
			if filter.ActionMarkRead {
				s.db.ExecContext(ctx, `UPDATE received_emails SET is_read = true, read_at = $1 WHERE id = $2`, time.Now(), emailID)
			}
			if filter.ActionArchive {
				s.db.ExecContext(ctx, `UPDATE received_emails SET is_archived = true WHERE id = $1`, emailID)
			}
			if filter.ActionTrash {
				s.db.ExecContext(ctx, `UPDATE received_emails SET is_trashed = true, trashed_at = $1 WHERE id = $2`, time.Now(), emailID)
			}

			// Update filter stats
			s.db.ExecContext(ctx, `UPDATE inbox_filters SET match_count = match_count + 1, last_matched_at = $1 WHERE id = $2`, time.Now(), filter.ID)

			log.Printf("Applied filter %d to email %d", filter.ID, emailID)
		}
	}
}

// matchesFilter checks if an email matches filter conditions
func (s *ReceivingService) matchesFilter(email model.ReceivedEmail, conditions []model.FilterCondition, logic string) bool {
	if len(conditions) == 0 {
		return false
	}

	for _, cond := range conditions {
		matches := s.matchesCondition(email, cond)

		if logic == "any" && matches {
			return true
		}
		if logic == "all" && !matches {
			return false
		}
	}

	return logic == "all"
}

// matchesCondition checks if an email matches a single condition
func (s *ReceivingService) matchesCondition(email model.ReceivedEmail, cond model.FilterCondition) bool {
	var value string
	switch cond.Field {
	case "from":
		value = email.FromEmail
	case "to":
		value = strings.Join(email.ToEmails, ", ")
	case "subject":
		value = email.Subject
	case "body":
		value = email.TextBody
	case "hasAttachment":
		if email.HasAttachments {
			return cond.Value == "true"
		}
		return cond.Value == "false"
	default:
		return false
	}

	value = strings.ToLower(value)
	condValue := strings.ToLower(cond.Value)

	switch cond.Operator {
	case "contains":
		return strings.Contains(value, condValue)
	case "equals":
		return value == condValue
	case "startsWith":
		return strings.HasPrefix(value, condValue)
	case "endsWith":
		return strings.HasSuffix(value, condValue)
	case "regex":
		matched, _ := regexp.MatchString(cond.Value, value)
		return matched
	}

	return false
}

// Helper functions

func extractEmail(addresses []string) string {
	if len(addresses) == 0 {
		return ""
	}
	addr := addresses[0]
	if strings.Contains(addr, "<") {
		start := strings.Index(addr, "<")
		end := strings.Index(addr, ">")
		if start >= 0 && end > start {
			return addr[start+1 : end]
		}
	}
	return addr
}

func extractName(addresses []string) string {
	if len(addresses) == 0 {
		return ""
	}
	addr := addresses[0]
	if strings.Contains(addr, "<") {
		start := strings.Index(addr, "<")
		name := strings.TrimSpace(addr[:start])
		return strings.Trim(name, "\"")
	}
	return ""
}

func generateThreadID(headers []model.SESHeader) string {
	for _, h := range headers {
		if h.Name == "In-Reply-To" || h.Name == "References" {
			// Use first reference as thread ID
			refs := strings.Fields(h.Value)
			if len(refs) > 0 {
				// Hash the reference
				hash := sha256.Sum256([]byte(refs[0]))
				return hex.EncodeToString(hash[:8])
			}
		}
	}
	return ""
}

func parseTimestamp(ts string) time.Time {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return time.Now()
	}
	return t
}
