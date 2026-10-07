package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/hibiken/asynq"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/eventoutbox"
	"github.com/dublyo/mailat/api/internal/provider"
)

// EmailHandler handles email sending tasks
type EmailHandler struct {
	db                    *sql.DB
	cfg                   *config.Config
	emailProvider         provider.EmailProvider
	attachments           provider.AttachmentStorage
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
	handler.attachments, _ = provider.NewAttachmentStorage(ctx, cfg.AWSRegion, cfg.AWSAccessKeyID, cfg.AWSSecretAccessKey)

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

// WithAttachmentStorage sets where S3-referenced attachments are loaded from.
func (h *EmailHandler) WithAttachmentStorage(s provider.AttachmentStorage) *EmailHandler {
	h.attachments = s
	return h
}

func (h *EmailHandler) HandleEmailSend(ctx context.Context, t *asynq.Task) error {
	payload, err := UnmarshalEmailSendPayload(t.Payload())
	if err != nil {
		return fmt.Errorf("invalid email job: %w", err)
	}
	// A throttled row is already re-queued with next_attempt_at; the database, not
	// an asynq retry, owns the next attempt.
	if err = h.ProcessEmail(ctx, payload); errors.Is(err, ErrSendThrottled) || errors.Is(err, ErrAttachmentDeferred) {
		return nil
	}
	return err
}

// ErrSendThrottled means SES refused the send with a rate or quota limit before
// accepting it. The row was re-queued with a backoff, so callers treat it as handled
// but should stop submitting more mail for now.
var ErrSendThrottled = errors.New("send throttled by provider; retry scheduled")

// ErrAttachmentDeferred means attachment storage failed transiently before
// anything was submitted; the row was re-queued with a backoff.
var ErrAttachmentDeferred = errors.New("attachment storage unavailable; retry scheduled")

const (
	maxSendAttempts = 10
	throttleGiveUp  = "SES throttled this message repeatedly; nothing was sent"
	storageGiveUp   = "Attachment storage stayed unavailable; nothing was sent"
)

// backoff returns the delay before attempt n+1 after the nth throttle:
// min(30s*2^(n-1), 30m) with +/-20% jitter, and at least 1h when the account's
// sending quota (rather than its rate) is exhausted.
func backoff(attempt int, quota bool, rnd func() float64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := math.Min(float64(30*time.Second)*math.Pow(2, float64(attempt-1)), float64(30*time.Minute))
	d := time.Duration(base * (0.8 + 0.4*rnd()))
	if quota && d < time.Hour {
		d = time.Hour
	}
	return d
}

// ProcessEmail claims once, then records an outcome and event in one transaction.
// A task retry cannot repeat a provider submission whose outcome may be uncertain.
func (h *EmailHandler) ProcessEmail(ctx context.Context, payload *EmailSendPayload) error {
	if h.emailProvider == nil {
		return fmt.Errorf("email provider is not configured")
	}
	var durable []byte
	err := h.db.QueryRowContext(ctx, `UPDATE transactional_emails SET status='sending',updated_at=NOW() WHERE id=$1 AND org_id=$2 AND status='queued' AND (scheduled_for IS NULL OR scheduled_for<=NOW()) AND (next_attempt_at IS NULL OR next_attempt_at<=NOW()) RETURNING send_payload`, payload.EmailID, payload.OrgID).Scan(&durable)
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
	for k, v := range payload.Headers {
		if provider.AllowedMailHeader(k) && !strings.EqualFold(k, "X-Mailat-Message-ID") {
			msg.Headers[k] = v
		}
	}
	if payload.MessageUUID != "" {
		msg.Headers["X-Mailat-Message-ID"] = payload.MessageUUID
	}
	for _, a := range payload.Attachments {
		data := a.Data
		if len(data) == 0 && a.S3Key != "" {
			if h.attachments == nil {
				return h.finishEmail(payload.EmailID, "failed", "Attachment storage is not configured", "", payload)
			}
			loadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			data, err = h.attachments.Get(loadCtx, a.S3Bucket, a.S3Key)
			cancel()
			if errors.Is(err, provider.ErrAttachmentNotFound) {
				return h.finishEmail(payload.EmailID, "failed", "Attachment unavailable", "", payload)
			}
			if err != nil {
				return h.requeueEmail(payload, "Attachment storage unavailable; retry scheduled", storageGiveUp, false, ErrAttachmentDeferred)
			}
		}
		msg.Attachments = append(msg.Attachments, provider.Attachment{Filename: a.Name, ContentType: a.Type, Data: data, ContentID: a.CID, Inline: a.Disposition == "inline"})
	}
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	result, sendErr := h.emailProvider.SendEmail(sendCtx, msg)
	cancel()
	providerID := ""
	if result != nil {
		providerID = result.MessageID
	}
	if sendErr != nil && provider.ClassifySendError(sendErr) == provider.SendThrottled {
		return h.deferEmail(ctx, payload, provider.IsQuotaExhausted(sendErr))
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
	if err = h.finishEmailTx(ctx, tx, id, status, details, providerID, payload); err != nil {
		return err
	}
	return tx.Commit()
}

// finishEmailTx records a terminal outcome, its delivery event and the outbox
// event inside the caller's transaction.
func (h *EmailHandler) finishEmailTx(ctx context.Context, tx *sql.Tx, id int64, status, details, providerID string, payload *EmailSendPayload) error {
	var org, user, identity int64
	var messageUUID, current string
	err := tx.QueryRowContext(ctx, `UPDATE transactional_emails SET
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
		// A shared identity's user_id is only its steward, never the sender.
		_ = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT system_user_id FROM transactional_emails WHERE id=$2),
 (SELECT user_id FROM identities WHERE id=$1 AND kind='personal'),0)`, identity, id).Scan(&user)
	}
	if user == 0 {
		identity = 0 // events are attributed to an identity only together with its user
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO transactional_delivery_events(email_id,event_type,details) VALUES($1,$2,$3)`, id, status, details); err != nil {
		return err
	}
	// A forward keeps running after a rejected send; the owner sees why.
	if status == "failed" || status == "unknown" {
		if _, err = tx.ExecContext(ctx, `UPDATE email_forwards SET last_error=$2,updated_at=NOW()
 WHERE uuid=(SELECT system_ref FROM transactional_emails WHERE id=$1 AND system_kind='forward')`, id, "Last forward was not delivered: "+details); err != nil {
			return err
		}
	}
	// Referenced objects are no longer needed once the outcome is final.
	if _, err = tx.ExecContext(ctx, `DELETE FROM send_attachment_refs WHERE transactional_email_id=$1`, id); err != nil {
		return err
	}
	// Invite, mailbox setup and reset mails carry a live sign-up token; keep
	// no copy of the body once the send is final.
	if _, err = tx.ExecContext(ctx, `UPDATE transactional_emails SET html_body=NULL,text_body=NULL,send_payload=NULL WHERE id=$1 AND system_kind='invite'`, id); err != nil {
		return err
	}
	data := map[string]any{"status": current, "messageUuid": messageUUID, "providerMessageId": providerID}
	if payload != nil {
		data["messageId"] = payload.MessageID
		data["from"] = payload.From
		data["to"] = payload.To
		data["subject"] = payload.Subject
	}
	return eventoutbox.Emit(ctx, tx, eventoutbox.Event{Type: "email." + status, OrgID: org, UserID: user, IdentityID: identity, MessageUUID: messageUUID, DedupeKey: status + ":" + messageUUID, Data: data})
}

// deferEmail re-queues a throttled claim with a backoff. A throttle proves SES did
// not accept the message, so a later attempt cannot duplicate it. scheduled_for
// is the user's requested time and is left alone; next_attempt_at gates retries.
func (h *EmailHandler) deferEmail(_ context.Context, payload *EmailSendPayload, quota bool) error {
	reason := "Amazon SES rate limit; retry scheduled"
	if quota {
		reason = "Amazon SES sending quota reached; retry scheduled"
	}
	return h.requeueEmail(payload, reason, throttleGiveUp, quota, ErrSendThrottled)
}

// requeueEmail puts a claimed, never-submitted row back in the queue with a
// backoff, or fails it with giveUp after too many attempts. It returns
// deferred when the row was re-queued.
func (h *EmailHandler) requeueEmail(payload *EmailSendPayload, reason, giveUp string, quota bool, deferred error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var attempts int
	var expired bool
	// Age counts from when the message became due, so a send scheduled days
	// ahead still gets the full retry window.
	err = tx.QueryRowContext(ctx, `SELECT send_attempts+1, GREATEST(created_at,COALESCE(scheduled_for,created_at))<NOW()-INTERVAL '24 hours' FROM transactional_emails WHERE id=$1 AND status='sending' FOR UPDATE`, payload.EmailID).Scan(&attempts, &expired)
	if err == sql.ErrNoRows {
		return nil // a concurrent terminal state won
	}
	if err != nil {
		return err
	}
	if attempts >= maxSendAttempts || expired {
		// One transaction: the row must never be left in 'sending', where the
		// stale sweep would wrongly mark a never-sent message 'unknown'.
		if _, err = tx.ExecContext(ctx, `UPDATE transactional_emails SET send_attempts=$2,last_deferral_reason=$3 WHERE id=$1`, payload.EmailID, attempts, reason); err != nil {
			return err
		}
		if err = h.finishEmailTx(ctx, tx, payload.EmailID, "failed", giveUp, "", payload); err != nil {
			return err
		}
		return tx.Commit()
	}
	delay := backoff(attempts, quota, rand.Float64)
	if _, err = tx.ExecContext(ctx, `UPDATE transactional_emails SET status='queued',next_attempt_at=NOW()+make_interval(secs=>$2),send_attempts=$3,last_deferral_reason=$4,updated_at=NOW() WHERE id=$1 AND status='sending'`, payload.EmailID, delay.Seconds(), attempts, reason); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO transactional_delivery_events(email_id,event_type,details) VALUES($1,'deferred',$2)`, payload.EmailID, reason); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return deferred
}

// RecoverPending processes only due, never-attempted or deferred jobs. Multiple instances may
// discover the same row; ProcessEmail's atomic claim permits one provider attempt.
func (h *EmailHandler) RecoverPending(ctx context.Context) error {
	rows, err := h.db.QueryContext(ctx, `SELECT id,org_id FROM transactional_emails WHERE status='queued' AND send_payload IS NOT NULL AND (scheduled_for IS NULL OR scheduled_for<=NOW()) AND (next_attempt_at IS NULL OR next_attempt_at<=NOW()) ORDER BY id LIMIT 25`)
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
		if err = h.ProcessEmail(ctx, p); errors.Is(err, ErrSendThrottled) {
			// SES is limiting this account; leave the rest of the batch for a later pass.
			break
		} else if err != nil && !errors.Is(err, ErrAttachmentDeferred) {
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
