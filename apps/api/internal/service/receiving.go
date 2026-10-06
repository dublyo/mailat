package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/mail"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dublyo/mailat/api/internal/eventoutbox"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/lib/pq"
)

type ReceivingService struct {
	db                    *sql.DB
	receivingProvider     *provider.ReceivingProvider
	storage               incomingStorage
	notify                func(int64, *model.ReceivedEmail)
	webhookTriggerService *WebhookTriggerService
}

func NewReceivingService(db *sql.DB, region, accessKeyID, secretAccessKey, webhookBaseURL string) (*ReceivingService, error) {
	rp, err := provider.NewReceivingProvider(&provider.ReceivingConfig{Region: region, AccessKeyID: accessKeyID, SecretAccessKey: secretAccessKey, WebhookBaseURL: webhookBaseURL})
	if err != nil {
		return nil, err
	}
	return &ReceivingService{db: db, receivingProvider: rp, storage: rp}, nil
}
func (s *ReceivingService) DB() *sql.DB { return s.db }
func (s *ReceivingService) SetWebhookTriggerService(svc *WebhookTriggerService) {
	s.webhookTriggerService = svc
}
func (s *ReceivingService) SetNotifier(fn func(int64, *model.ReceivedEmail)) { s.notify = fn }

var ErrWebhookAuthorization = errors.New("invalid webhook authorization")

type incomingStorage interface {
	GetEmailFromS3(context.Context, string, string) ([]byte, error)
	PutAttachment(context.Context, string, string, string, []byte) error
	DeleteObject(context.Context, string, string) error
	GenerateDownloadURL(context.Context, string, string, string) (string, error)
}

type ReceivingAuthorization struct {
	OrgID                    int64
	TopicARN, Bucket, Region string
	SendingOnly              bool
}

func (s *ReceivingService) AuthorizeNotification(ctx context.Context, topic, secret string) (*ReceivingAuthorization, error) {
	var auth ReceivingAuthorization
	var expected string
	err := s.db.QueryRowContext(ctx, `SELECT org_id,sns_topic_arn,s3_bucket,s3_region,webhook_secret,false FROM receiving_configs WHERE sns_topic_arn=$1 AND status IN ('active','pending') UNION ALL SELECT org_id,sns_topic_arn,s3_bucket,s3_region,webhook_secret,true FROM sending_configs WHERE sns_topic_arn=$1 AND status IN ('active','pending')`, topic).Scan(&auth.OrgID, &auth.TopicARN, &auth.Bucket, &auth.Region, &expected, &auth.SendingOnly)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == sql.ErrNoRows || secret == "" || subtle.ConstantTimeCompare([]byte(secret), []byte(expected)) != 1 {
		return nil, ErrWebhookAuthorization
	}
	return &auth, nil
}

type recipientIdentity struct {
	ID, DomainID, OrgID, UserID int64
	Email, Domain               string
	Recipients                  []string
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

	if domain.Status != "active" {
		return nil, fmt.Errorf("domain must be verified before enabling receiving")
	}

	// Check if already set up
	if domain.ReceivingEnabled {
		return nil, fmt.Errorf("receiving is already enabled for this domain")
	}

	// Check if org has existing receiving config
	var existingConfig struct {
		S3Bucket       sql.NullString
		SNSTopicArn    sql.NullString
		SESRuleSetName sql.NullString
		WebhookSecret  sql.NullString
		S3Region       sql.NullString
	}
	err = s.db.QueryRowContext(ctx,
		`SELECT s3_bucket, sns_topic_arn, ses_rule_set_name, webhook_secret, s3_region FROM receiving_configs WHERE org_id = $1`,
		orgID,
	).Scan(&existingConfig.S3Bucket, &existingConfig.SNSTopicArn, &existingConfig.SESRuleSetName, &existingConfig.WebhookSecret, &existingConfig.S3Region)

	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("load receiving configuration: %w", err)
	}

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
			`INSERT INTO receiving_configs (org_id, s3_bucket, s3_region, sns_topic_arn, ses_rule_set_name, webhook_secret, status, setup_completed_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())`,
			orgID, result.S3Bucket, result.S3Region, result.SNSTopicArn, result.RuleSetName, webhookSecret, "pending", time.Now(),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to save receiving config: %w", err)
		}
	}

	// Persist the topic and secret before subscribing: SNS confirms asynchronously.
	if result.WebhookURL == "" {
		result.WebhookURL = s.receivingProvider.WebhookURL(webhookSecret)
	}
	if err := s.receivingProvider.SubscribeWebhook(ctx, result.SNSTopicArn, result.WebhookURL); err != nil {
		return nil, fmt.Errorf("subscribe receiving webhook: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE receiving_configs SET status = 'active', updated_at = NOW() WHERE org_id = $1", orgID); err != nil {
		return nil, err
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

	if _, err = s.db.ExecContext(ctx, `INSERT INTO domain_dns_records(domain_id,record_type,hostname,expected_value,verified) VALUES($1,'MX',$2,$3,false) ON CONFLICT(domain_id,record_type,hostname) DO UPDATE SET expected_value=EXCLUDED.expected_value,verified=false`, domainID, domain.Name, fmt.Sprintf("10 inbound-smtp.%s.amazonaws.com", result.S3Region)); err != nil {
		return nil, fmt.Errorf("save receiving DNS record: %w", err)
	}
	// Generate required DNS records for receiving
	mxRecords := []model.DomainDNSRecord{
		{
			RecordType: "MX",
			Hostname:   domain.Name,
			Value:      fmt.Sprintf("10 inbound-smtp.%s.amazonaws.com", result.S3Region),
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

// Each provider delivery is committed once, with one mailbox copy per identity.
func (s *ReceivingService) ProcessIncomingEmail(ctx context.Context, auth *ReceivingAuthorization, n *model.SESNotification) error {
	if auth.SendingOnly {
		return fmt.Errorf("sending feedback topics cannot deliver incoming mail")
	}
	if n.NotificationType != "Received" || n.Receipt == nil {
		return fmt.Errorf("invalid received notification")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,255}$`).MatchString(n.Mail.MessageId) {
		return fmt.Errorf("invalid provider message id")
	}
	if len(n.Receipt.Recipients) > 100 {
		return fmt.Errorf("too many recipients")
	}
	identities := map[int64]*recipientIdentity{}
	domains := map[string]bool{}
	for _, recipient := range n.Receipt.Recipients {
		parsed, err := mail.ParseAddress(recipient)
		if err != nil {
			continue
		}
		address := strings.ToLower(parsed.Address)
		at := strings.LastIndex(address, "@")
		if at < 1 {
			continue
		}
		domain := address[at+1:]
		var ident recipientIdentity
		err = s.db.QueryRowContext(ctx, `SELECT i.id,i.domain_id,d.org_id,i.user_id,i.email,d.name
   FROM identities i JOIN domains d ON d.id=i.domain_id
   WHERE d.org_id=$1 AND d.name=$2 AND d.status='active' AND d.receiving_enabled=true AND i.can_receive=true
   AND (lower(i.email)=$3 OR i.is_catch_all=true)
   ORDER BY (lower(i.email)=$3) DESC,i.id LIMIT 1`, auth.OrgID, domain, address).Scan(&ident.ID, &ident.DomainID, &ident.OrgID, &ident.UserID, &ident.Email, &ident.Domain)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}
		domains[domain] = true
		if existing := identities[ident.ID]; existing != nil {
			if !containsString(existing.Recipients, address) {
				existing.Recipients = append(existing.Recipients, address)
			}
		} else {
			ident.Recipients = []string{address}
			identities[ident.ID] = &ident
		}
	}
	// A message may produce separate notifications for each matched domain rule.
	// Deduplicate the recipient identity, never the whole message/topic pair.
	rows, err := s.db.QueryContext(ctx, `SELECT identity_id FROM received_ingestions WHERE topic_arn=$1 AND ses_message_id=$2`, auth.TopicARN, n.Mail.MessageId)
	if err != nil {
		return err
	}
	for rows.Next() {
		var identityID int64
		if err = rows.Scan(&identityID); err != nil {
			rows.Close()
			return err
		}
		delete(identities, identityID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Unknown addresses have no owner. Acknowledge rather than indefinitely retrying them.
	if len(identities) == 0 {
		return nil
	}
	action := n.Receipt.Action
	if action.BucketName != "" && action.BucketName != auth.Bucket {
		return fmt.Errorf("unapproved S3 bucket")
	}
	if action.TopicArn != "" && action.TopicArn != auth.TopicARN {
		return fmt.Errorf("unapproved SNS topic")
	}
	keys := []string{}
	for domain := range domains {
		key := "incoming/" + domain + "/" + n.Mail.MessageId
		if action.ObjectKey == "" || action.ObjectKey == key {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return fmt.Errorf("unapproved S3 object key")
	}
	var raw []byte
	var key string
	for _, candidate := range keys {
		raw, err = s.storage.GetEmailFromS3(ctx, auth.Bucket, candidate)
		if err == nil {
			key = candidate
			break
		}
	}
	if err != nil {
		return fmt.Errorf("fetch incoming message: %w", err)
	}
	parsed, err := parseIncomingMIME(raw)
	if err != nil {
		// A permanent MIME error cannot be repaired by SNS retries. Preserve the
		// original bytes as a private .eml attachment and make the failure visible.
		parsed = malformedIncomingFallback(raw, n, err)
	}
	// Classify once from private MIME bytes and authenticated SES verdicts. A
	// non-match (including parser limits) never interrupts ordinary receiving.
	reportDomains, _ := dmarcReportDomains(parsed, receiptDMARCVerdicts(n.Receipt))
	isDMARCReport, classificationErr := ownedDMARCReport(ctx, s.db, auth.OrgID, reportDomains)
	if classificationErr != nil {
		log.Print("DMARC classification: domain ownership unavailable; retaining normal placement")
	}
	for index := range parsed.Attachments {
		att := &parsed.Attachments[index]
		att.S3Bucket = auth.Bucket
		att.S3Key = fmt.Sprintf("attachments/%d/%s/%d-%s", auth.OrgID, n.Mail.MessageId, index, att.Checksum)
		if err := s.storage.PutAttachment(ctx, att.S3Bucket, att.S3Key, att.ContentType, att.Data); err != nil {
			return fmt.Errorf("save attachment: %w", err)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Lock all recipient owners in a stable order before inserting mail/cursors.
	users := []int64{}
	for _, ident := range identities {
		users = append(users, ident.UserID)
	}
	sort.Slice(users, func(i, j int) bool { return users[i] < users[j] })
	for i, user := range users {
		if i == 0 || user != users[i-1] {
			if err = lockMailboxLabels(ctx, tx, user); err != nil {
				return err
			}
		}
	}
	from := clipUTF8(extractEmail(parsed.Header["From"]), 255)
	name := clipUTF8(decodeMIMEHeader(extractName(parsed.Header["From"])), 255)
	subject := clipUTF8(decodeMIMEHeader(parsed.Header.Get("Subject")), 1000)
	messageID := parsed.Header.Get("Message-ID")
	if messageID == "" {
		messageID = "<" + n.Mail.MessageId + "@ses.invalid>"
	}
	refs := strings.Fields(parsed.Header.Get("References"))
	replyTo := clipUTF8(extractEmail(parsed.Header["Reply-To"]), 255)
	thread := generateThreadID(n.Mail.Headers)
	if thread == "" {
		hash := sha256.Sum256([]byte(messageID))
		thread = hex.EncodeToString(hash[:8])
	}
	folder := "inbox"
	spam := n.Receipt.SpamVerdict.Status == "FAIL" || n.Receipt.VirusVerdict.Status == "FAIL"
	if spam {
		folder = "spam"
	}
	saved := map[int64]*model.ReceivedEmail{}
	for _, ident := range identities {
		deliveryFolder := folder
		if isDMARCReport && folder == "inbox" {
			enabled, err := autoOrganizeDMARC(ctx, tx, ident.UserID)
			if err != nil {
				return err
			}
			if enabled {
				deliveryFolder = DMARCReportsFolder
			}
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO received_ingestions(org_id,topic_arn,ses_message_id,identity_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, auth.OrgID, auth.TopicARN, n.Mail.MessageId, ident.ID)
		if err != nil {
			return err
		}
		count, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			continue
		}
		var emailID int64
		var emailUUID string
		err = tx.QueryRowContext(ctx, `INSERT INTO received_emails(org_id,domain_id,identity_id,message_id,thread_id,from_email,from_name,to_emails,cc_emails,subject,snippet,raw_s3_bucket,raw_s3_key,folder,is_spam,spam_verdict,virus_verdict,spf_verdict,dkim_verdict,dmarc_verdict,ses_message_id,received_at,text_body,html_body,size_bytes,has_attachments,in_reply_to,"references",reply_to,envelope_recipients,updated_at)
   VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,NOW())
   ON CONFLICT(identity_id,ses_message_id) DO NOTHING RETURNING id,uuid`, ident.OrgID, ident.DomainID, ident.ID, clipUTF8(messageID, 500), thread, from, name, pq.Array(addressList(parsed.Header, "To")), pq.Array(addressList(parsed.Header, "Cc")), subject, clipUTF8(parsed.Text, 200), auth.Bucket, key, deliveryFolder, spam, n.Receipt.SpamVerdict.Status, n.Receipt.VirusVerdict.Status, n.Receipt.SPFVerdict.Status, n.Receipt.DKIMVerdict.Status, n.Receipt.DMARCVerdict.Status, n.Mail.MessageId, parseTimestamp(n.Receipt.Timestamp), parsed.Text, parsed.HTML, len(raw), len(parsed.Attachments) > 0, clipUTF8(parsed.Header.Get("In-Reply-To"), 500), pq.Array(refs), replyTo, pq.Array(ident.Recipients)).Scan(&emailID, &emailUUID)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return fmt.Errorf("insert mailbox delivery: %w", err)
		}
		for _, att := range parsed.Attachments {
			if _, err = tx.ExecContext(ctx, `INSERT INTO email_attachments(received_email_id,filename,content_type,size_bytes,s3_key,s3_bucket,content_id,is_inline,checksum) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, emailID, att.Filename, att.ContentType, att.SizeBytes, att.S3Key, att.S3Bucket, att.ContentID, att.IsInline, att.Checksum); err != nil {
				return err
			}
		}
		email := &model.ReceivedEmail{ID: emailID, UUID: emailUUID, OrgID: ident.OrgID, IdentityID: ident.ID, DomainID: ident.DomainID, FromEmail: from, Subject: subject, TextBody: parsed.Text, HasAttachments: len(parsed.Attachments) > 0, ToEmails: addressList(parsed.Header, "To"), CcEmails: addressList(parsed.Header, "Cc"), EnvelopeRecipients: ident.Recipients, Folder: deliveryFolder, IsSpam: spam, ReceivedAt: parseTimestamp(n.Receipt.Timestamp)}
		if err = s.applyReceivedFilters(ctx, tx, ident.UserID, email); err != nil {
			return err
		}
		// Forwarding with keepCopy=false archives the local copy; it is never deleted.
		if email.Folder, err = keepCopyArchive(ctx, tx, ident.ID, emailID, email.Folder); err != nil {
			return err
		}
		if err = eventoutbox.Emit(ctx, tx, eventoutbox.Event{Type: "email.received", OrgID: ident.OrgID, UserID: ident.UserID, IdentityID: ident.ID, MessageUUID: emailUUID, DedupeKey: fmt.Sprintf("received:%s:%d", n.Mail.MessageId, ident.ID), Data: map[string]any{"from": email.FromEmail, "to": email.ToEmails, "subject": email.Subject, "folder": email.Folder, "inReplyTo": parsed.Header.Get("In-Reply-To"), "hasAttachments": email.HasAttachments}}); err != nil {
			return err
		}
		if err = EnqueueArrivalJobs(ctx, tx, ArrivalInput{OrgID: ident.OrgID, IdentityID: ident.ID, IdentityEmail: strings.ToLower(ident.Email), Recipients: ident.Recipients,
			Copies: []ArrivalCopy{{OwnerID: ident.UserID, EmailID: emailID, UUID: emailUUID, Folder: email.Folder}}, Header: parsed.Header, Notification: n, DMARCReport: isDMARCReport}); err != nil {
			return fmt.Errorf("queue arrival jobs: %w", err)
		}
		saved[ident.ID] = email
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if s.notify != nil {
		for _, ident := range identities {
			if email := saved[ident.ID]; email != nil {
				s.notify(ident.UserID, email)
			}
		}
	}
	return nil
}
func containsString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

func (s *ReceivingService) applyReceivedFilters(ctx context.Context, tx *sql.Tx, userID int64, email *model.ReceivedEmail) error {
	// Every matching rule applies and later folders win, so blocked-sender rules
	// run last: a user rule like "from example.com -> inbox" can never un-block.
	rows, err := tx.QueryContext(ctx, `SELECT id,conditions,condition_logic,action_labels,action_folder,action_star,action_mark_read,action_archive,action_trash FROM inbox_filters WHERE org_id=$1 AND user_id=$2 AND active=true AND (identity_id IS NULL OR identity_id=$3) ORDER BY (kind='blocked_sender'),priority DESC,id`, email.OrgID, userID, email.IdentityID)
	if err != nil {
		return err
	}
	type rule struct {
		id                         int64
		conditions                 []model.FilterCondition
		logic                      string
		labels                     []string
		folder                     sql.NullString
		star, read, archive, trash bool
	}
	var rules []rule
	for rows.Next() {
		var r rule
		var conditions []byte
		if err = rows.Scan(&r.id, &conditions, &r.logic, pq.Array(&r.labels), &r.folder, &r.star, &r.read, &r.archive, &r.trash); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(conditions, &r.conditions); err != nil {
			rows.Close()
			return err
		}
		rules = append(rules, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Authenticated SES spam/virus placement wins over automatic folder rules.
	// A user may still explicitly discard it with a Trash rule.
	initialSpam := email.IsSpam
	for _, r := range rules {
		if !s.matchesFilter(*email, r.conditions, r.logic) {
			continue
		}
		folder := email.Folder
		if r.folder.Valid {
			folder = r.folder.String
		}
		if r.archive {
			folder = "archive"
		}
		if r.trash {
			folder = "trash"
		}
		if initialSpam && folder != "trash" {
			folder = "spam"
		}
		email.Folder = folder
		email.IsArchived = folder == "archive"
		email.IsTrashed = folder == "trash"
		email.IsSpam = folder == "spam"
		email.IsStarred = email.IsStarred || r.star
		email.IsRead = email.IsRead || r.read
		if _, err = tx.ExecContext(ctx, `UPDATE received_emails SET folder=$1,is_archived=$2,is_trashed=$3,is_spam=$4,is_starred=$5,is_read=$6,read_at=CASE WHEN $6 THEN NOW() ELSE read_at END,trashed_at=CASE WHEN $3 THEN NOW() ELSE NULL END,labels=(SELECT ARRAY(SELECT DISTINCT unnest(labels || $7::text[]))),updated_at=NOW() WHERE id=$8`, folder, email.IsArchived, email.IsTrashed, email.IsSpam, email.IsStarred, email.IsRead, pq.Array(r.labels), email.ID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE inbox_filters SET match_count=match_count+1,last_matched_at=NOW() WHERE id=$1`, r.id); err != nil {
			return err
		}
	}
	return nil
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
		value = strings.Join(append(append(append([]string{}, email.ToEmails...), email.CcEmails...), email.EnvelopeRecipients...), ", ")
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

	if cond.Operator == "regex" {
		matched, _ := regexp.MatchString("(?i)"+cond.Value, value)
		return matched
	}
	value = strings.ToLower(value)
	condValue := strings.ToLower(cond.Value)

	switch cond.Operator {
	case "notContains":
		return !strings.Contains(value, condValue)
	case "notEquals":
		return value != condValue
	case "contains":
		return strings.Contains(value, condValue)
	case "equals":
		return value == condValue
	case "startsWith":
		return strings.HasPrefix(value, condValue)
	case "endsWith":
		return strings.HasSuffix(value, condValue)
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
