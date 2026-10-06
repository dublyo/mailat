package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/dublyo/mailat/api/internal/worker"
)

// systemKeyPrefix is the idempotency namespace reserved for SendAutomated, so a
// user-chosen key can never replay or block a system send.
const systemKeyPrefix = "mailat:"

var (
	errReservedSubmissionKey = &provider.MailValidationError{Message: "Idempotency-Key values starting with \"mailat:\" are reserved"}
	// ErrProviderNotConfigured means no mail provider is available; retrying cannot help.
	ErrProviderNotConfigured = errors.New("email provider is not configured")
	// ErrSystemRecipientSuppressed means the recipient is on the transactional suppression list.
	ErrSystemRecipientSuppressed = &provider.MailValidationError{Message: "the recipient is on the suppression list"}
)

var systemSendKinds = map[string]bool{"auto_reply": true, "forward": true, "forward_verify": true, "invite": true}

func isSystemSubmissionKey(key string) bool {
	return strings.HasPrefix(strings.ToLower(key), systemKeyPrefix)
}

// S3Attachment is an already stored object sent by reference; the worker
// loads its bytes at send time.
type S3Attachment struct {
	Bucket, Key, Name, ContentType, ContentID string
	Size                                      int
	Inline                                    bool
}

// AutomatedSend is one system message (auto-reply, forward, verification or
// invite). It never records a Sent copy.
type AutomatedSend struct {
	OrgID, IdentityID, ActingUserID int64
	To                              string
	Subject, Text, HTML             string
	ReplyTo, FromName               string
	Headers                         map[string]string
	S3Attachments                   []S3Attachment
	Kind, Ref                       string
	// DedupeKey must start with "mailat:"; a replay returns the first response.
	DedupeKey string
}

// durableSend is everything enqueueDurable writes for one queued message.
type durableSend struct {
	OrgID, IdentityID, DomainID, OwnerID int64
	From, FromAddress, FromName          string
	To, Cc, Bcc                          []string
	ReplyTo, Subject, HTML, Text         string
	Tags, Metadata                       string
	Headers                              map[string]string
	Attachments                          []preparedAttachment
	S3Attachments                        []S3Attachment
	MessageID                            string
	IdempotencyKey, RequestHash          string
	ScheduledFor                         *time.Time
	RecordSentCopy                       bool
	SystemKind, SystemRef                string
	SystemUserID                         int64
}

// enqueueDurable claims the idempotency key, reserves monthly quota and writes
// the queued transactional row with its durable payload inside tx. A replayed
// key returns the stored response and a nil payload. The caller commits, then
// passes the payload to Dispatch; RecoverPending covers a lost dispatch.
func (s *TransactionalService) enqueueDurable(ctx context.Context, tx *sql.Tx, p durableSend) (*model.SendEmailResponse, *worker.EmailSendPayload, error) {
	if p.IdempotencyKey != "" {
		claim, err := tx.ExecContext(ctx, `INSERT INTO email_submission_keys(org_id,submission_key,request_hash) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, p.OrgID, p.IdempotencyKey, p.RequestHash)
		if err != nil {
			return nil, nil, err
		}
		if affected, _ := claim.RowsAffected(); affected == 0 {
			existing, err := loadSubmissionFrom(ctx, tx, p.OrgID, p.IdempotencyKey, p.RequestHash)
			if err == nil && existing == nil {
				err = ErrSubmissionConflict
			}
			return existing, nil, err
		}
	}
	// Reserved in tx, so a rolled-back send gives its quota unit back.
	granted, err := reserveMonthlySendsTx(ctx, tx, s.cfg, p.OrgID, 1)
	if err != nil {
		return nil, nil, err
	}
	if granted == 0 {
		return nil, nil, ErrMonthlySendQuota
	}

	emailUUID := uuid.New().String()
	var emailID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO transactional_emails (
			uuid, org_id, identity_id, message_id, from_address, to_addresses,
			cc_addresses, bcc_addresses, reply_to, subject, html_body, text_body,
			tags, metadata, status, idempotency_key, scheduled_for,
			system_kind, system_ref, system_user_id, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 'queued', $15, $16,
			NULLIF($17,''), NULLIF($18,'')::uuid, NULLIF($19,0), NOW())
		RETURNING id`,
		emailUUID, p.OrgID, p.IdentityID, p.MessageID, p.From, strings.Join(p.To, ","),
		strings.Join(p.Cc, ","), strings.Join(p.Bcc, ","), p.ReplyTo,
		p.Subject, p.HTML, p.Text, p.Tags, p.Metadata, p.IdempotencyKey, p.ScheduledFor,
		p.SystemKind, p.SystemRef, p.SystemUserID,
	).Scan(&emailID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create email record: %w", err)
	}

	if p.RecordSentCopy {
		var mailboxID int64
		err = tx.QueryRowContext(ctx, `INSERT INTO received_emails(uuid,org_id,domain_id,identity_id,message_id,from_email,from_name,to_emails,cc_emails,bcc_emails,reply_to,subject,text_body,html_body,snippet,has_attachments,folder,is_read,direction,send_status,submitter_user_id,updated_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,'outbox',true,'outbound','queued',$17,NOW()) RETURNING id`, emailUUID, p.OrgID, p.DomainID, p.IdentityID, p.MessageID, p.FromAddress, p.FromName, pq.Array(p.To), pq.Array(p.Cc), pq.Array(p.Bcc), p.ReplyTo, p.Subject, p.Text, p.HTML, composeSnippet(p.Text), len(p.Attachments) > 0, p.OwnerID).Scan(&mailboxID)
		if err != nil {
			return nil, nil, err
		}
		if err = insertMailboxAttachments(ctx, tx, mailboxID, p.Attachments); err != nil {
			return nil, nil, err
		}
	}

	payload := worker.NewEmailSendPayload(emailID, p.OrgID, p.From, p.To, p.Subject, p.HTML, p.Text, p.MessageID)
	payload.Cc, payload.Bcc, payload.ReplyTo = p.Cc, p.Bcc, p.ReplyTo
	payload.MessageUUID, payload.UserID, payload.IdentityID = emailUUID, p.OwnerID, p.IdentityID
	payload.ScheduledFor = p.ScheduledFor
	if len(p.Headers) > 0 {
		payload.Headers = p.Headers
	}
	for _, a := range p.Attachments {
		disposition := "attachment"
		if a.inline {
			disposition = "inline"
		}
		payload.Attachments = append(payload.Attachments, worker.AttachmentInfo{Name: a.name, Type: a.contentType, Data: a.data, Size: a.size, CID: a.contentID, Disposition: disposition})
	}
	for _, a := range p.S3Attachments {
		disposition := "attachment"
		if a.Inline {
			disposition = "inline"
		}
		payload.Attachments = append(payload.Attachments, worker.AttachmentInfo{Name: a.Name, Type: a.ContentType, Size: a.Size, CID: a.ContentID, Disposition: disposition, S3Bucket: a.Bucket, S3Key: a.Key})
		if _, err = tx.ExecContext(ctx, `INSERT INTO send_attachment_refs(transactional_email_id,s3_bucket,s3_key) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, emailID, a.Bucket, a.Key); err != nil {
			return nil, nil, err
		}
	}
	payloadJSON, err := payload.Marshal()
	if err != nil {
		return nil, nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE transactional_emails SET send_payload=$2 WHERE id=$1`, emailID, string(payloadJSON)); err != nil {
		return nil, nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO transactional_delivery_events (email_id, event_type, details) VALUES ($1, 'queued', 'Email accepted for delivery')`, emailID); err != nil {
		return nil, nil, err
	}
	if p.IdempotencyKey != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE email_submission_keys SET email_uuid=$3,status='ready' WHERE org_id=$1 AND submission_key=$2`, p.OrgID, p.IdempotencyKey, emailUUID); err != nil {
			return nil, nil, err
		}
	}
	return &model.SendEmailResponse{ID: emailUUID, MessageID: p.MessageID, Status: "queued", AcceptedAt: time.Now()}, payload, nil
}

// Dispatch hands a committed payload to the job queue, or sends it directly
// when the queue is unavailable. The row is already durable: a failed enqueue
// leaves queued work for RecoverPending, and the worker's atomic claim permits
// one provider attempt.
func (s *TransactionalService) Dispatch(payload *worker.EmailSendPayload) {
	if payload == nil {
		return
	}
	var err error
	if s.queueClient != nil {
		if payload.ScheduledFor != nil {
			_, err = s.queueClient.EnqueueEmailSendScheduled(payload, *payload.ScheduledFor)
		} else {
			_, err = s.queueClient.EnqueueEmailSend(payload)
		}
	}
	if payload.ScheduledFor == nil && (s.queueClient == nil || err != nil) {
		go func() {
			_ = worker.NewEmailHandlerWithProvider(s.db, s.cfg, s.emailProvider).WithAttachmentStorage(s.attachments).ProcessEmail(context.Background(), payload)
		}()
	}
}

// SendAutomated queues one system message from an org identity. With a
// caller-supplied tx it writes inside it and returns the payload, which the
// caller passes to Dispatch after commit. With a nil tx it commits and
// dispatches itself and returns a nil payload.
func (s *TransactionalService) SendAutomated(ctx context.Context, tx *sql.Tx, in *AutomatedSend) (*model.SendEmailResponse, *worker.EmailSendPayload, error) {
	if s.emailProvider == nil {
		return nil, nil, ErrProviderNotConfigured
	}
	if !systemSendKinds[in.Kind] {
		return nil, nil, fmt.Errorf("unknown system send kind %q", in.Kind)
	}
	if !strings.HasPrefix(in.DedupeKey, systemKeyPrefix) || len(in.DedupeKey) > 128 || strings.ContainsAny(in.DedupeKey, "\r\n") {
		return nil, nil, fmt.Errorf("system send key must start with %q and be at most 128 characters", systemKeyPrefix)
	}
	if in.Ref != "" {
		if _, err := uuid.Parse(in.Ref); err != nil {
			return nil, nil, fmt.Errorf("system send ref must be a UUID")
		}
	}
	for k := range in.Headers {
		if !provider.AllowedMailHeader(k) || strings.EqualFold(k, "X-Mailat-Message-ID") {
			return nil, nil, fmt.Errorf("header %q cannot be set on a system send", k)
		}
	}
	to, err := mail.ParseAddress(in.To)
	if err != nil || strings.ContainsAny(in.To, "\r\n") {
		return nil, nil, &provider.MailValidationError{Message: "invalid recipient email address"}
	}
	if len(in.S3Attachments) > 50 {
		return nil, nil, &provider.MailValidationError{Message: "maximum 50 attachments"}
	}
	total := 0
	for _, a := range in.S3Attachments {
		total += a.Size
		if a.Bucket == "" || a.Key == "" || a.Size < 0 {
			return nil, nil, fmt.Errorf("attachment reference is incomplete")
		}
	}
	if total > provider.MaxAttachmentBytes {
		return nil, nil, &provider.MailValidationError{Message: "attachments exceed the 10 MiB total limit"}
	}

	own := tx == nil
	if own {
		if tx, err = s.db.BeginTx(ctx, nil); err != nil {
			return nil, nil, err
		}
		defer tx.Rollback()
	}
	var identityEmail, displayName, domainName, domainStatus string
	var domainID int64
	var canSend, sesVerified bool
	err = tx.QueryRowContext(ctx, `SELECT lower(i.email),COALESCE(i.display_name,''),COALESCE(i.can_send,false),d.id,lower(d.name),COALESCE(d.status,''),COALESCE(d.ses_verified,false)
		FROM identities i JOIN domains d ON d.id=i.domain_id WHERE i.id=$1 AND d.org_id=$2`, in.IdentityID, in.OrgID).
		Scan(&identityEmail, &displayName, &canSend, &domainID, &domainName, &domainStatus, &sesVerified)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, &provider.MailValidationError{Message: "sending identity not found"}
	}
	if err != nil {
		return nil, nil, err
	}
	if !canSend {
		return nil, nil, &provider.MailValidationError{Message: "sending is disabled for this identity"}
	}
	if domainStatus != "active" || (s.cfg.EmailProvider == "ses" && !sesVerified) {
		return nil, nil, &provider.MailValidationError{Message: "sender domain is not active"}
	}
	var suppressed bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM suppression_list WHERE org_id=$1 AND email=$2)`, in.OrgID, strings.ToLower(to.Address)).Scan(&suppressed); err != nil {
		return nil, nil, fmt.Errorf("failed to check suppression list: %w", err)
	}
	if suppressed {
		return nil, nil, ErrSystemRecipientSuppressed
	}

	name := displayName
	if in.FromName != "" {
		name = in.FromName
	}
	from := (&mail.Address{Name: name, Address: identityEmail}).String()
	msg := &provider.EmailMessage{From: from, To: []string{to.String()}, ReplyTo: in.ReplyTo, Subject: in.Subject, TextBody: in.Text, HTMLBody: in.HTML, Headers: in.Headers}
	for _, a := range in.S3Attachments {
		msg.Attachments = append(msg.Attachments, provider.Attachment{Filename: a.Name, ContentType: a.ContentType, ContentID: a.ContentID, Inline: a.Inline})
	}
	// Bytes load at send time; validate everything else about the MIME now.
	if _, _, err = provider.BuildMailMIME(msg); err != nil {
		return nil, nil, err
	}

	body, _ := json.Marshal(in)
	sum := sha256.Sum256(body)
	response, payload, err := s.enqueueDurable(ctx, tx, durableSend{
		OrgID: in.OrgID, IdentityID: in.IdentityID, DomainID: domainID, OwnerID: in.ActingUserID,
		From: from, FromAddress: identityEmail, FromName: name,
		To: []string{to.String()}, ReplyTo: in.ReplyTo,
		Subject: in.Subject, HTML: in.HTML, Text: in.Text, Headers: in.Headers,
		S3Attachments: in.S3Attachments, MessageID: s.generateMessageID(domainName),
		IdempotencyKey: in.DedupeKey, RequestHash: hex.EncodeToString(sum[:]),
		SystemKind: in.Kind, SystemRef: in.Ref, SystemUserID: in.ActingUserID,
	})
	if err != nil || !own {
		return response, payload, err
	}
	if payload != nil {
		if err = tx.Commit(); err != nil {
			return nil, nil, err
		}
		s.Dispatch(payload)
	}
	return response, nil, nil
}
