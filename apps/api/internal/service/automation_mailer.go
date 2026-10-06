package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/time/rate"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/provider"
)

// Automation email is two-phase so SES is never called inside, or before the
// commit of, a step transaction. EnqueueAutomationEmail only inserts an
// automation_messages row in the step's transaction (unique per enrollment and
// node, so a replayed step gets the same row back). The mail runner then
// leases pending rows with SKIP LOCKED and sends each through the M3 pipeline:
// the campaign eligibility and suppression predicate, sender revalidation,
// monthly quota reservation, the escaping renderer with footer,
// List-Unsubscribe and tracking, and SES error classification. A row that may
// have reached SES is never sent again (sending -> unknown on a crash).
const (
	automationMailTick        = 2 * time.Second
	automationMailBatch       = 20
	automationMailLease       = "5 minutes"
	automationMailStale       = "10 minutes"
	automationMailRetryWait   = 30 * time.Minute
	automationMailQuotaWait   = time.Hour
	automationMailMaxAttempts = 10
)

// AutomationMailer is the executor's AutomationMailSender.
type AutomationMailer struct {
	db  *sql.DB
	cfg *config.Config
}

func NewAutomationMailer(db *sql.DB, cfg *config.Config) *AutomationMailer {
	return &AutomationMailer{db: db, cfg: cfg}
}

var errAutomationNeedsSES = &provider.MailValidationError{Message: "Automation emails send only through Amazon SES; set EMAIL_PROVIDER=ses"}

// EnqueueAutomationEmail queues one message in tx. Problems no retry can fix
// (no SES, no postal address, an empty template) are MailValidationErrors; an
// ineligible recipient returns ErrAutomationRecipientSuppressed. Nothing is
// written in either case.
func (m *AutomationMailer) EnqueueAutomationEmail(ctx context.Context, tx *sql.Tx, e AutomationEmail) (int64, error) {
	if m.cfg.EmailProvider != "ses" {
		return 0, errAutomationNeedsSES
	}
	if err := checkCampaignLinkBases(m.cfg.APIUrl, m.cfg.WebUrl); err != nil {
		return 0, err
	}
	if err := requirePostalAddress(ctx, tx, e.OrgID); err != nil {
		return 0, err
	}
	var hasBody bool
	err := tx.QueryRowContext(ctx, `SELECT html_body<>'' OR COALESCE(text_body,'')<>'' FROM email_templates WHERE id=$1 AND org_id=$2 AND is_active`,
		e.TemplateID, e.OrgID).Scan(&hasBody)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, permanentStep("The template of step %s is no longer available", e.NodeID)
	}
	if err != nil {
		return 0, err
	}
	if !hasBody {
		return 0, &provider.MailValidationError{Message: "The template has no content"}
	}
	var eligible bool
	if err := tx.QueryRowContext(ctx, `SELECT `+automationEligibleSQL+` FROM contacts c WHERE c.id=$1 AND c.org_id=$2`,
		e.ContactID, e.OrgID).Scan(&eligible); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if !eligible {
		return 0, ErrAutomationRecipientSuppressed
	}
	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO automation_messages (org_id, automation_id, version_id, enrollment_id, node_id, contact_id, email,
			sender_user_id, identity_id, template_id, subject_override, track_opens, track_clicks)
		SELECT $1, $2, $3, $4, $5, c.id, c.email, $7, $8, $9, NULLIF($10,''), $11, $12 FROM contacts c WHERE c.id=$6 AND c.org_id=$1
		ON CONFLICT (enrollment_id, node_id) DO UPDATE SET updated_at = automation_messages.updated_at
		RETURNING id`,
		e.OrgID, e.AutomationID, e.VersionID, e.EnrollmentID, e.NodeID, e.ContactID, e.SenderUserID, e.IdentityID, e.TemplateID,
		sanitizeSubject(e.SubjectOverride), e.TrackOpens, e.TrackClicks).Scan(&id)
	return id, err
}

// RunAutomationMailer sends queued automation emails until ctx ends. Like the
// campaign sender it runs regardless of WORKER_ENABLED and idles without SES.
func RunAutomationMailer(ctx context.Context, db *sql.DB, cfg *config.Config) {
	p := NewCampaignProvider(ctx, cfg)
	if p == nil {
		<-ctx.Done()
		return
	}
	defer p.Close()
	r := newAutomationMailRunner(db, cfg, p)
	for {
		n, err := r.runOnce(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("Automation mailer will retry: %v", err)
		}
		wait := automationMailTick
		if n >= automationMailBatch && err == nil {
			wait = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

type automationMailRunner struct {
	db       *sql.DB
	cfg      *config.Config
	provider provider.EmailProvider
	now      func() time.Time
	run      string
	limiter  *rate.Limiter
	quota    *provider.SendQuota
	quotaAt  time.Time
}

func newAutomationMailRunner(db *sql.DB, cfg *config.Config, p provider.EmailProvider) *automationMailRunner {
	return &automationMailRunner{db: db, cfg: cfg, provider: p, now: time.Now, run: uuid.NewString(), limiter: rate.NewLimiter(1, 1)}
}

// runOnce recovers stale rows, then claims and sends one batch. It returns
// the number of rows claimed.
func (r *automationMailRunner) runOnce(ctx context.Context) (int, error) {
	if err := r.recoverStale(ctx); err != nil {
		return 0, err
	}
	q := r.sendQuota(ctx)
	if q != nil && q.Max24HourSend >= 0 && q.SentLast24Hours >= q.Max24HourSend {
		return 0, nil
	}
	// Automations share the account with campaigns (which take 80% of the
	// SES rate) and compose: they use a tenth of it, at least 1/s.
	perSecond := 1.0
	if q != nil && q.MaxSendRate > 0 {
		perSecond = max(math.Floor(0.1*q.MaxSendRate), 1)
	}
	if float64(r.limiter.Limit()) != perSecond {
		r.limiter.SetLimit(rate.Limit(perSecond))
	}
	rows, err := r.db.QueryContext(ctx, `UPDATE automation_messages SET status='claimed', lease_owner=$1::uuid,
			lease_expires_at=now()+interval '`+automationMailLease+`', updated_at=now()
		WHERE id IN (SELECT m.id FROM automation_messages m WHERE m.status='pending' AND m.next_attempt_at<=now()
			AND EXISTS (SELECT 1 FROM automations a WHERE a.id=m.automation_id AND a.status='active')
			ORDER BY m.next_attempt_at, m.id LIMIT $2 FOR UPDATE OF m SKIP LOCKED)
		RETURNING id`, r.run, automationMailBatch)
	if err != nil {
		return 0, fmt.Errorf("claim automation emails: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	defer r.releaseOwned()
	for _, id := range ids {
		if ctx.Err() != nil || r.limiter.Wait(ctx) != nil {
			break
		}
		if err := r.sendOne(ctx, id); err != nil && ctx.Err() == nil {
			log.Printf("Automation email %d: %v", id, err)
		}
	}
	return len(ids), nil
}

// recoverStale returns expired claims to pending and resolves sends that never
// finished: sent when SNS already proved delivery, otherwise unknown.
func (r *automationMailRunner) recoverStale(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE automation_messages SET status='pending', lease_owner=NULL, lease_expires_at=NULL, updated_at=now()
		WHERE status='claimed' AND lease_expires_at<now()`); err != nil {
		return fmt.Errorf("recover automation email claims: %w", err)
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE automation_messages SET
			status=CASE WHEN delivery_status IS NOT NULL THEN 'sent' ELSE 'unknown' END,
			sent_at=CASE WHEN delivery_status IS NOT NULL THEN COALESCE(sent_at,now()) ELSE sent_at END,
			error=CASE WHEN delivery_status IS NULL THEN 'send was interrupted; outcome unknown' ELSE error END,
			lease_owner=NULL, lease_expires_at=NULL, updated_at=now()
		WHERE status='sending' AND attempt_started_at<now()-interval '`+automationMailStale+`'`); err != nil {
		return fmt.Errorf("recover interrupted automation emails: %w", err)
	}
	return nil
}

// releaseOwned returns rows this runner claimed but did not start.
func (r *automationMailRunner) releaseOwned() {
	ctx, cancel := context.WithTimeout(context.Background(), campaignFinishTimeout)
	defer cancel()
	if _, err := r.db.ExecContext(ctx, `UPDATE automation_messages SET status='pending', lease_owner=NULL, lease_expires_at=NULL, updated_at=now()
		WHERE status='claimed' AND lease_owner=$1::uuid`, r.run); err != nil {
		log.Printf("Automation mailer could not release claimed rows (their lease expires): %v", err)
	}
}

// automationSend is a started automation message and what renders it.
type automationSend struct {
	id, orgID int64
	snap      campaignSnapshot
	rcpt      eligibleRecipient
}

// sendOne starts, sends and finishes one claimed row.
func (r *automationMailRunner) sendOne(ctx context.Context, id int64) error {
	startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), campaignFinishTimeout)
	defer cancel()
	s, err := r.start(startCtx, id)
	if err != nil || s == nil {
		return err
	}
	var result *provider.SendResult
	msg, _, err := renderCampaignMessage(s.snap, s.rcpt, renderOptions{APIURL: r.cfg.APIUrl, WebURL: r.cfg.WebUrl, Secret: r.cfg.JWTSecret, Mode: renderSend})
	if err == nil {
		sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), campaignSendTimeout)
		result, err = r.provider.SendEmail(sendCtx, msg)
		cancel()
	}
	messageID := ""
	if err == nil && result != nil {
		messageID = result.MessageID
	}
	finishCtx, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), campaignFinishTimeout)
	defer cancelFinish()
	// On failure the row stays sending and recovery makes it unknown: never resent.
	return r.finish(finishCtx, s, classifySESSendError(err), messageID, err)
}

// start rechecks the claimed row in one transaction (automation, recipient
// eligibility, sender, footer prerequisites, template), reserves monthly
// quota and marks it sending. It returns nil when the row was finished
// without sending (skipped, failed, cancelled) or is no longer ours.
func (r *automationMailRunner) start(ctx context.Context, id int64) (*automationSend, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	s := &automationSend{id: id}
	var userID, identityID, templateID sql.NullInt64
	var subject sql.NullString
	var reserved bool
	var automationStatus, enrollmentStatus string
	err = tx.QueryRowContext(ctx, `SELECT m.org_id, m.automation_id, m.contact_id, m.email, m.message_uuid::text, m.sender_user_id, m.identity_id,
			m.template_id, m.subject_override, m.track_opens, m.track_clicks, m.quota_reserved, a.status, e.status
		FROM automation_messages m JOIN automations a ON a.id=m.automation_id JOIN automation_enrollments e ON e.id=m.enrollment_id
		WHERE m.id=$1 AND m.status='claimed' AND m.lease_owner=$2::uuid AND m.lease_expires_at>now() FOR UPDATE OF m`, id, r.run).
		Scan(&s.orgID, &s.snap.ID, &s.rcpt.ContactID, &s.rcpt.Email, &s.rcpt.MessageUUID, &userID, &identityID, &templateID,
			&subject, &s.snap.TrackOpens, &s.snap.TrackClicks, &reserved, &automationStatus, &enrollmentStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.snap.OrgID, s.snap.Automation, s.rcpt.ID = s.orgID, true, id
	end := func(status, reason, msg string) (*automationSend, error) {
		if _, err := tx.ExecContext(ctx, `UPDATE automation_messages SET status=$2, skip_reason=NULLIF($3,''), error=NULLIF($4,''),
				quota_reserved=false, lease_owner=NULL, lease_expires_at=NULL, updated_at=now() WHERE id=$1`, id, status, reason, msg); err != nil {
			return nil, err
		}
		if reserved {
			if err := refundMonthlySendsTx(ctx, tx, s.orgID, 1); err != nil {
				return nil, err
			}
		}
		return nil, tx.Commit()
	}
	// Archive and manual cancel both cancel the enrollment; paused waits.
	if automationStatus == "archived" || enrollmentStatus == "cancelled" {
		return end("cancelled", "", "")
	}
	if automationStatus != "active" {
		if _, err := tx.ExecContext(ctx, `UPDATE automation_messages SET status='pending', lease_owner=NULL, lease_expires_at=NULL, updated_at=now()
				WHERE id=$1`, id); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}

	// The recipient: same rules (and skip reasons) as a campaign recipient.
	var cEmail, cStatus string
	var suppressed bool
	var attrs []byte
	err = tx.QueryRowContext(ctx, `SELECT c.email, c.status, `+campaignSuppressedSQL("c.org_id", "c.email")+`,
			COALESCE(c.first_name,''), COALESCE(c.last_name,''), COALESCE(c.attributes,'{}'::jsonb)
		FROM contacts c WHERE c.id=$1 AND c.org_id=$2`, s.rcpt.ContactID, s.orgID).
		Scan(&cEmail, &cStatus, &suppressed, &s.rcpt.FirstName, &s.rcpt.LastName, &attrs)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return end("skipped", "contact_deleted", "")
	case err != nil:
		return nil, err
	case !strings.EqualFold(cEmail, s.rcpt.Email):
		return end("skipped", "email_changed", "")
	case cStatus != "active":
		return end("skipped", "inactive", "")
	case suppressed:
		return end("skipped", "suppressed", "")
	}
	if json.Unmarshal(attrs, &s.rcpt.Attributes) != nil || s.rcpt.Attributes == nil {
		s.rcpt.Attributes = map[string]any{}
	}

	// The sender: still the publisher's can_send identity on an active,
	// SES-verified domain with feedback set up (the campaign sender rule).
	var domainID int64
	err = tx.QueryRowContext(ctx, `SELECT i.email, COALESCE(i.display_name,''), d.id FROM identities i JOIN users u ON u.id=i.user_id JOIN domains d ON d.id=i.domain_id
		WHERE i.id=$1 AND i.user_id=$2 AND i.kind='personal' AND `+campaignSenderPredicate, identityID, userID, s.orgID).Scan(&s.snap.FromEmail, &s.snap.FromName, &domainID)
	if errors.Is(err, sql.ErrNoRows) {
		return end("failed", "", "The sending identity is no longer available")
	}
	if err != nil {
		return nil, err
	}
	var invalid *provider.MailValidationError
	if err := requireFeedbackReady(ctx, tx, domainID); errors.As(err, &invalid) {
		return end("failed", "", "Sending setup (bounce/complaint feedback) is not finished for the domain")
	} else if err != nil {
		return nil, err
	}
	if err := checkCampaignLinkBases(r.cfg.APIUrl, r.cfg.WebUrl); err != nil {
		return end("failed", "", "API_URL and WEB_URL must be absolute http(s) URLs")
	}
	err = tx.QueryRowContext(ctx, `SELECT o.name, COALESCE(o.postal_address,'') FROM organizations o WHERE o.id=$1`, s.orgID).
		Scan(&s.snap.OrgName, &s.snap.PostalAddress)
	if err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `SELECT subject, html_body, COALESCE(text_body,'') FROM email_templates WHERE id=$1 AND org_id=$2 AND is_active`,
		templateID, s.orgID).Scan(&s.snap.Subject, &s.snap.HTMLContent, &s.snap.TextContent)
	if errors.Is(err, sql.ErrNoRows) {
		return end("failed", "", "The template is no longer available")
	}
	if err != nil {
		return nil, err
	}
	if subject.String != "" {
		s.snap.Subject = subject.String
	}

	if !reserved {
		granted, err := reserveMonthlySendsTx(ctx, tx, r.cfg, s.orgID, 1)
		if err != nil {
			return nil, err
		}
		if granted == 0 {
			_, err := tx.ExecContext(ctx, `UPDATE automation_messages SET status='pending', error='Monthly send quota exceeded; will retry',
					next_attempt_at=now()+$2*interval '1 second', lease_owner=NULL, lease_expires_at=NULL, updated_at=now() WHERE id=$1`,
				id, int64(automationMailQuotaWait/time.Second))
			if err != nil {
				return nil, err
			}
			return nil, tx.Commit()
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE automation_messages SET status='sending', quota_reserved=true, attempts=attempts+1,
			attempt_started_at=now(), updated_at=now() WHERE id=$1`, id); err != nil {
		return nil, err
	}
	return s, tx.Commit()
}

// finish records a send outcome. A message SES did not accept goes back to
// pending (keeping its quota reservation) until it has used its attempts.
func (r *automationMailRunner) finish(ctx context.Context, s *automationSend, outcome, messageID string, sendErr error) error {
	errText := ""
	if sendErr != nil {
		errText = sendErrorText(sendErr, s.rcpt.Email)
	}
	var err error
	switch outcome {
	case sendOutcomeOK:
		// SNS may already have promoted the row; COALESCE keeps its values.
		_, err = r.db.ExecContext(ctx, `UPDATE automation_messages SET status='sent', provider_message_id=COALESCE(provider_message_id,NULLIF($2,'')),
			sent_at=COALESCE(sent_at,now()), error=NULL, lease_owner=NULL, lease_expires_at=NULL, updated_at=now()
			WHERE id=$1 AND status IN ('sending','unknown','sent')`, s.id, messageID)
	case sendOutcomeRejected:
		_, err = r.db.ExecContext(ctx, `UPDATE automation_messages SET status='failed', error=$2, lease_owner=NULL, lease_expires_at=NULL, updated_at=now()
			WHERE id=$1 AND status='sending'`, s.id, errText)
	case sendOutcomeUncertain:
		_, err = r.db.ExecContext(ctx, `UPDATE automation_messages SET status='unknown', error=$2, lease_owner=NULL, lease_expires_at=NULL, updated_at=now()
			WHERE id=$1 AND status='sending'`, s.id, errText)
	default: // throttle, provider_paused, sender: SES did not accept the message
		wait := automationMailRetryWait
		if outcome == sendOutcomeThrottle {
			wait = campaignThrottleDelay(sendErr)
		}
		_, err = r.db.ExecContext(ctx, `UPDATE automation_messages SET
				status=CASE WHEN attempts >= $4 THEN 'failed' ELSE 'pending' END,
				next_attempt_at=now()+$3*interval '1 second', attempt_started_at=NULL, error=$2,
				lease_owner=NULL, lease_expires_at=NULL, updated_at=now()
			WHERE id=$1 AND status='sending'`, s.id, errText, int64(wait/time.Second), automationMailMaxAttempts)
	}
	return err
}

// sendQuota returns the SES quota, refreshed every five minutes; a failed
// refresh keeps the last known value.
func (r *automationMailRunner) sendQuota(ctx context.Context) *provider.SendQuota {
	if !r.quotaAt.IsZero() && r.now().Sub(r.quotaAt) < campaignQuotaTTL {
		return r.quota
	}
	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	q, err := r.provider.GetSendQuota(qctx)
	r.quotaAt = r.now()
	if err == nil && q != nil {
		r.quota = q
	}
	return r.quota
}
