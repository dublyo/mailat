package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/dublyo/mailat/api/internal/worker"
)

// Mail arrival features (auto-reply, forward, push) never run inside SES
// ingestion. EnqueueArrivalJobs writes mail_arrival_jobs rows in the ingest
// transaction (deduplicated, so an SNS retry adds nothing); the ArrivalRunner
// leases them with SKIP LOCKED and runs each in its own transaction. A system
// send is enqueued durably in that same transaction together with the job's
// completion, so a crash either rolls everything back for a retry or leaves a
// committed transactional row for the durable email worker.
const (
	arrivalTick  = 2 * time.Second
	arrivalBatch = 20
	arrivalLease = 2 * time.Minute
	// A push handler stops before its lease ends, so no other runner can
	// reclaim a job whose notification is still being delivered.
	arrivalPushBudget  = 90 * time.Second
	arrivalMaxAttempts = 6
	arrivalBaseBackoff = 30 * time.Second
	arrivalMaxBackoff  = 6 * time.Hour
)

// ArrivalCopy is one stored mailbox copy of an arriving message.
type ArrivalCopy struct {
	OwnerID, EmailID int64
	UUID, Folder     string
}

// ArrivalInput is one identity's share of an arriving message.
type ArrivalInput struct {
	OrgID, IdentityID int64
	IdentityKind      string // personal (one copy) or shared (one copy per reader)
	IdentityEmail     string
	Recipients        []string
	Copies            []ArrivalCopy
	Header            mail.Header
	Notification      *model.SESNotification
	DMARCReport       bool
}

// arrivalAutoReply is the auto_reply job payload: what the reply needs from
// the original message, so it survives deletion of the mailbox copy.
type arrivalAutoReply struct {
	Sender     string `json:"sender"`
	Subject    string `json:"subject"`
	MessageID  string `json:"messageId,omitempty"`
	References string `json:"references,omitempty"`
}

type arrivalPush struct {
	UUID     string `json:"uuid"`
	From     string `json:"from"`
	Subject  string `json:"subject"`
	Identity string `json:"identity"`
}

func insertArrivalJob(ctx context.Context, tx *sql.Tx, orgID, identityID, userID, ruleID, emailID int64, kind, sesID, dedupe string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mail_arrival_jobs(org_id,kind,identity_id,user_id,rule_id,received_email_id,ses_message_id,dedupe_key,payload)
		VALUES($1,$2,$3,NULLIF($4,0),NULLIF($5,0),NULLIF($6,0),$7,$8,$9) ON CONFLICT (dedupe_key) DO NOTHING`,
		orgID, kind, identityID, userID, ruleID, emailID, sesID, dedupe, string(body))
	return err
}

// EnqueueArrivalJobs queues the arrival work for one identity inside the
// ingest transaction: at most one auto_reply (when the cheap header guards
// pass and a rule applies), one forward per active forward, and one push per
// copy owner whose copy stayed in the inbox and who has a subscription.
func EnqueueArrivalJobs(ctx context.Context, tx *sql.Tx, in ArrivalInput) error {
	if len(in.Copies) == 0 || in.Notification == nil || in.Notification.Receipt == nil {
		return nil
	}
	sesID := in.Notification.Mail.MessageId
	sender := strings.ToLower(extractEmail(in.Header["From"]))
	subject := clipUTF8(decodeMIMEHeader(in.Header.Get("Subject")), 1000)

	// copyOf picks the given user's copy; jobs of other users use the first copy.
	copyOf := func(user int64) ArrivalCopy {
		for _, c := range in.Copies {
			if c.OwnerID == user {
				return c
			}
		}
		return in.Copies[0]
	}
	ruleID, ruleUser, err := activeRuleForIdentity(ctx, tx, in.IdentityID)
	if err != nil {
		return err
	}
	if ruleID != 0 {
		owner := copyOf(ruleUser)
		var isIdentity bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM identities WHERE lower(email)=$1)`, sender).Scan(&isIdentity); err != nil {
			return err
		}
		own := append([]string{in.IdentityEmail}, in.Recipients...)
		if isIdentity {
			own = append(own, sender)
		}
		r := in.Notification.Receipt
		ok, _ := AutoReplyEligible(in.Header, in.Notification.Mail.Source, sender, own, append([]string{in.IdentityEmail}, in.Recipients...), ArrivalVerdicts{
			Spam: r.SpamVerdict.Status, Virus: r.VirusVerdict.Status, SPF: r.SPFVerdict.Status, DKIM: r.DKIMVerdict.Status,
			DMARC: r.DMARCVerdict.Status, DMARCReport: in.DMARCReport, Folder: owner.Folder,
		})
		if ok {
			payload := arrivalAutoReply{Sender: sender, Subject: subject, MessageID: clipUTF8(strings.TrimSpace(in.Header.Get("Message-Id")), 500), References: clipUTF8(strings.Join(strings.Fields(in.Header.Get("References")), " "), 4000)}
			if err = insertArrivalJob(ctx, tx, in.OrgID, in.IdentityID, ruleUser, ruleID, owner.EmailID, "auto_reply", sesID, fmt.Sprintf("ar:%d:%s", in.IdentityID, sesID), payload); err != nil {
				return err
			}
		}
	}

	rows, err := tx.QueryContext(ctx, `SELECT id,user_id FROM email_forwards WHERE identity_id=$1 AND status='active' ORDER BY id`, in.IdentityID)
	if err != nil {
		return err
	}
	type forward struct{ id, user int64 }
	var forwards []forward
	for rows.Next() {
		var f forward
		if err = rows.Scan(&f.id, &f.user); err != nil {
			rows.Close()
			return err
		}
		forwards = append(forwards, f)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	forwardPayload := arrivalForward{LoopTokens: LoopTokens(in.Header), DMARCReport: in.DMARCReport}
	if len(forwardPayload.LoopTokens) > maxForwardHops {
		forwardPayload.LoopTokens = forwardPayload.LoopTokens[:maxForwardHops]
	}
	for _, f := range forwards {
		if err = insertArrivalJob(ctx, tx, in.OrgID, in.IdentityID, f.user, f.id, copyOf(f.user).EmailID, "forward", sesID, fmt.Sprintf("fw:%d:%s", f.id, sesID), forwardPayload); err != nil {
			return err
		}
	}

	for _, c := range in.Copies {
		if c.Folder != "inbox" {
			continue
		}
		var subscribed bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM push_subscriptions WHERE user_id=$1 AND active AND notify_new_email)`, c.OwnerID).Scan(&subscribed); err != nil {
			return err
		}
		if !subscribed {
			continue
		}
		payload := arrivalPush{UUID: c.UUID, From: sender, Subject: clipUTF8(subject, 120), Identity: in.IdentityEmail}
		if err = insertArrivalJob(ctx, tx, in.OrgID, in.IdentityID, c.OwnerID, 0, c.EmailID, "push", sesID, fmt.Sprintf("push:%s:%d:%d", sesID, in.IdentityID, c.OwnerID), payload); err != nil {
			return err
		}
	}
	return nil
}

// arrivalJob is one claimed mail_arrival_jobs row.
type arrivalJob struct {
	ID, OrgID, IdentityID     int64
	UserID, RuleID, EmailID   int64
	Kind, SESMessageID, Token string
	Payload                   []byte
	Attempts                  int
}

// arrivalResult is how a job ended. Status is done, skipped or failed; a done
// job's Payload is dispatched after commit.
type arrivalResult struct {
	Status, Reason string
	Payload        *worker.EmailSendPayload
	// After runs for a skipped or failed result, after the handler's writes
	// rolled back, in the transaction that finishes the job.
	After func(context.Context, queryer) error
}

func arrivalDone() arrivalResult { return arrivalResult{Status: "done"} }
func arrivalSkipped(reason string) arrivalResult {
	return arrivalResult{Status: "skipped", Reason: reason}
}
func arrivalFailed(reason string) arrivalResult {
	return arrivalResult{Status: "failed", Reason: reason}
}

// arrivalHandler runs one job in tx. A returned error is transient and retried
// with backoff; a skipped or failed result is final and its writes are rolled
// back.
type arrivalHandler func(ctx context.Context, tx *sql.Tx, job *arrivalJob) (arrivalResult, error)

// arrivalDirectHandler runs a job whose effect is outside the database (push),
// so it holds no transaction open during network calls.
type arrivalDirectHandler func(ctx context.Context, job *arrivalJob) (arrivalResult, error)

// ArrivalPusher delivers a new-mail push to a user's devices.
type ArrivalPusher interface {
	SendNewEmailNotification(ctx context.Context, userID int64, uuid, from, subject, identityEmail string) error
}

// ArrivalRunner executes mail_arrival_jobs. Every API replica may run one.
type ArrivalRunner struct {
	db       *sql.DB
	cfg      *config.Config
	tx       *TransactionalService
	push     ArrivalPusher
	handlers map[string]arrivalHandler
	direct   map[string]arrivalDirectHandler
	dispatch func(*worker.EmailSendPayload)
}

func NewArrivalRunner(db *sql.DB, cfg *config.Config, tx *TransactionalService) *ArrivalRunner {
	r := &ArrivalRunner{db: db, cfg: cfg, tx: tx}
	if tx != nil {
		r.dispatch = tx.Dispatch
	}
	r.handlers = map[string]arrivalHandler{"auto_reply": r.runAutoReply, "forward": r.runForward}
	r.direct = map[string]arrivalDirectHandler{"push": r.runPush}
	return r
}

// SetPusher enables push jobs; without one they finish skipped.
func (r *ArrivalRunner) SetPusher(p ArrivalPusher) { r.push = p }

// Run claims and executes due jobs until ctx ends, and prunes finished rows hourly.
func (r *ArrivalRunner) Run(ctx context.Context) {
	lastRetention := time.Time{}
	for {
		if time.Since(lastRetention) > time.Hour {
			if err := r.retention(ctx); err != nil && ctx.Err() == nil {
				log.Printf("Arrival job retention: %v", err)
			}
			lastRetention = time.Now()
		}
		n, err := r.runOnce(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("Arrival runner will retry: %v", err)
		}
		wait := arrivalTick
		if n >= arrivalBatch && err == nil {
			wait = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// runOnce claims and runs up to arrivalBatch jobs, one lease at a time so a
// slow job never lets the lease of a waiting one expire. It returns the number
// claimed.
func (r *ArrivalRunner) runOnce(ctx context.Context) (int, error) {
	// A lease that expired on its last allowed attempt will never be claimed again.
	if _, err := r.db.ExecContext(ctx, `UPDATE mail_arrival_jobs SET status='failed', result='failed:lease-expired', lease_until=NULL, updated_at=now()
		WHERE status='running' AND lease_until<now() AND attempts>=$1`, arrivalMaxAttempts); err != nil {
		return 0, fmt.Errorf("expire arrival jobs: %w", err)
	}
	n := 0
	for n < arrivalBatch && ctx.Err() == nil {
		jobs, err := r.claim(ctx, 1)
		if err != nil {
			return n, err
		}
		if len(jobs) == 0 {
			break
		}
		n++
		if err := r.processOne(ctx, &jobs[0]); err != nil && ctx.Err() == nil {
			log.Printf("Arrival job %d (%s): %v", jobs[0].ID, jobs[0].Kind, err)
		}
	}
	return n, nil
}

func (r *ArrivalRunner) claim(ctx context.Context, limit int) ([]arrivalJob, error) {
	token := uuid.NewString()
	rows, err := r.db.QueryContext(ctx, `UPDATE mail_arrival_jobs SET status='running', lease_until=now()+make_interval(secs=>$4),
			claim_token=$1, attempts=attempts+1, updated_at=now()
		WHERE id IN (SELECT id FROM mail_arrival_jobs
			WHERE status IN ('pending','running') AND next_attempt_at<=now() AND attempts<$3
				AND (lease_until IS NULL OR lease_until<now())
			ORDER BY next_attempt_at LIMIT $2 FOR UPDATE SKIP LOCKED)
		RETURNING id,kind,org_id,identity_id,COALESCE(user_id,0),COALESCE(rule_id,0),COALESCE(received_email_id,0),ses_message_id,payload,attempts`,
		token, limit, arrivalMaxAttempts, arrivalLease.Seconds())
	if err != nil {
		return nil, fmt.Errorf("claim arrival jobs: %w", err)
	}
	defer rows.Close()
	var jobs []arrivalJob
	for rows.Next() {
		j := arrivalJob{Token: token}
		if err = rows.Scan(&j.ID, &j.Kind, &j.OrgID, &j.IdentityID, &j.UserID, &j.RuleID, &j.EmailID, &j.SESMessageID, &j.Payload, &j.Attempts); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// errArrivalLeaseLost means another runner reclaimed the job; this run's work
// was rolled back.
var errArrivalLeaseLost = errors.New("arrival job lease lost")

// processOne runs a claimed job in its own transaction and finishes it with
// the claim token, so a runner whose lease expired cannot complete it.
func (r *ArrivalRunner) processOne(ctx context.Context, job *arrivalJob) error {
	if direct := r.direct[job.Kind]; direct != nil {
		result, err := direct(ctx, job)
		if err != nil {
			return r.retry(ctx, job, err)
		}
		return r.finish(ctx, r.db, job, result)
	}
	handler := r.handlers[job.Kind]
	if handler == nil {
		return r.finish(ctx, r.db, job, arrivalSkipped("unsupported"))
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := handler(ctx, tx, job)
	if err != nil {
		_ = tx.Rollback()
		return r.retry(ctx, job, err)
	}
	if result.Status != "done" {
		// Final without effects: drop any claim or counter the handler wrote.
		_ = tx.Rollback()
		if result.After == nil {
			return r.finish(ctx, r.db, job, result)
		}
		return r.finishAfter(ctx, job, result)
	}
	if err = r.finish(ctx, tx, job, result); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return r.retry(ctx, job, err)
	}
	if result.Payload != nil && r.dispatch != nil {
		r.dispatch(result.Payload)
	}
	return nil
}

// finishAfter records a skipped or failed job's side effect and completion together.
func (r *ArrivalRunner) finishAfter(ctx context.Context, job *arrivalJob, result arrivalResult) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = result.After(ctx, tx); err != nil {
		return err
	}
	if err = r.finish(ctx, tx, job, result); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *ArrivalRunner) finish(ctx context.Context, q queryer, job *arrivalJob, result arrivalResult) error {
	text := result.Status
	if result.Reason != "" {
		text += ":" + clipUTF8(result.Reason, 200)
	}
	res, err := q.ExecContext(ctx, `UPDATE mail_arrival_jobs SET status=$3, result=$4, lease_until=NULL, updated_at=now()
		WHERE id=$1 AND claim_token=$2 AND status='running'`, job.ID, job.Token, result.Status, text)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errArrivalLeaseLost
	}
	return nil
}

// arrivalBackoff is 30 s × 4^(attempts-1), capped at 6 h.
func arrivalBackoff(attempts int) time.Duration {
	d := arrivalBaseBackoff
	for i := 1; i < attempts && d < arrivalMaxBackoff; i++ {
		d *= 4
	}
	return min(d, arrivalMaxBackoff)
}

func (r *ArrivalRunner) retry(ctx context.Context, job *arrivalJob, cause error) error {
	status := "pending"
	if job.Attempts >= arrivalMaxAttempts {
		status = "failed"
	}
	_, err := r.db.ExecContext(ctx, `UPDATE mail_arrival_jobs SET status=$3, result=$4, lease_until=NULL,
			next_attempt_at=now()+make_interval(secs=>$5), updated_at=now()
		WHERE id=$1 AND claim_token=$2 AND status='running'`,
		job.ID, job.Token, status, "failed:"+clipUTF8(cause.Error(), 200), arrivalBackoff(job.Attempts).Seconds())
	if err != nil {
		return err
	}
	return cause
}

// retention deletes finished jobs: done and skipped after 30 days, failed after 90.
func (r *ArrivalRunner) retention(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM mail_arrival_jobs WHERE
		(status IN ('done','skipped') AND updated_at<now()-interval '30 days') OR (status='failed' AND updated_at<now()-interval '90 days')`)
	return err
}

func (r *ArrivalRunner) autoReplyDailyLimit() int {
	if r.cfg != nil && r.cfg.AutoReplyDailyLimit > 0 {
		return r.cfg.AutoReplyDailyLimit
	}
	return 200
}

// activeRuleForIdentity returns the oldest active auto-reply covering the
// identity and its owner, or 0. Rules belong to the personal identity's owner
// or to a can_manage member of a shared one; a rule with no identity_ids covers
// all of its owner's personal identities, never a shared identity.
func activeRuleForIdentity(ctx context.Context, q queryer, identityID int64) (id, userID int64, err error) {
	err = q.QueryRowContext(ctx, `SELECT a.id,a.user_id FROM auto_replies a JOIN identities i ON i.id=$1 AND `+identityAccessSQL("i", "a.user_id", identityCanManage)+`
		WHERE a.active AND a.start_date<=now() AND (a.end_date IS NULL OR a.end_date>=now())
			AND ((cardinality(COALESCE(a.identity_ids,'{}'))=0 AND i.kind='personal') OR $1=ANY(a.identity_ids))
		ORDER BY a.created_at, a.id LIMIT 1`, identityID).Scan(&id, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	return id, userID, err
}

// runAutoReply re-checks the rule, claims the sender for the reply interval,
// takes one unit of the rule's daily cap and enqueues the reply, all in tx.
func (r *ArrivalRunner) runAutoReply(ctx context.Context, tx *sql.Tx, job *arrivalJob) (arrivalResult, error) {
	if r.tx == nil || r.tx.emailProvider == nil {
		return arrivalFailed("provider-not-configured"), nil
	}
	var p arrivalAutoReply
	if err := json.Unmarshal(job.Payload, &p); err != nil || p.Sender == "" {
		return arrivalFailed("invalid-payload"), nil
	}
	var (
		ruleUUID, subject, html, text string
		active, inWindow, replyOnce   bool
		interval                      int
		patterns                      []string
		identityIDs                   []int64
	)
	err := tx.QueryRowContext(ctx, `SELECT uuid::text,COALESCE(active,false),start_date<=now() AND (end_date IS NULL OR end_date>=now()),
			subject,html_content,COALESCE(text_content,''),COALESCE(reply_once,true),reply_interval_days,
			COALESCE(exclude_patterns,'{}'),COALESCE(identity_ids,'{}')
		FROM auto_replies WHERE id=$1 AND user_id=$2 AND org_id=$3`, job.RuleID, job.UserID, job.OrgID).
		Scan(&ruleUUID, &active, &inWindow, &subject, &html, &text, &replyOnce, &interval, pq.Array(&patterns), pq.Array(&identityIDs))
	if errors.Is(err, sql.ErrNoRows) {
		return arrivalSkipped("rule-gone"), nil
	}
	if err != nil {
		return arrivalResult{}, err
	}
	if !active || !inWindow {
		return arrivalSkipped("rule-inactive"), nil
	}
	if len(identityIDs) > 0 && !containsInt64(identityIDs, job.IdentityID) {
		return arrivalSkipped("rule-changed"), nil
	}
	var owned, canSend bool
	err = tx.QueryRowContext(ctx, `SELECT `+identityAccessSQL("i", "$2", identityCanManage)+`, COALESCE(i.can_send,false) AND d.status='active'
		FROM identities i JOIN domains d ON d.id=i.domain_id WHERE i.id=$1 AND d.org_id=$3`, job.IdentityID, job.UserID, job.OrgID).Scan(&owned, &canSend)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !owned) {
		return arrivalSkipped("identity-not-owned"), nil
	}
	if err != nil {
		return arrivalResult{}, err
	}
	if !canSend {
		return arrivalSkipped("identity-cannot-send"), nil
	}
	if excludedSender(patterns, p.Sender) {
		return arrivalSkipped("excluded"), nil
	}
	var suppressed bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM suppression_list WHERE org_id=$1 AND email=$2)`, job.OrgID, p.Sender).Scan(&suppressed); err != nil {
		return arrivalResult{}, err
	}
	if suppressed {
		return arrivalSkipped("suppressed"), nil
	}

	// Concurrent copies from one sender serialize on the unique key; only one
	// claim succeeds per interval, and reply-once is never refreshed.
	var claimID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO auto_reply_senders(auto_reply_id,sender_email,replied_at) VALUES($1,$2,now())
		ON CONFLICT (auto_reply_id,sender_email) DO UPDATE SET replied_at=now()
		WHERE NOT $3::bool AND auto_reply_senders.replied_at < now()-make_interval(days=>$4)
		RETURNING id`, job.RuleID, p.Sender, replyOnce, interval).Scan(&claimID)
	if errors.Is(err, sql.ErrNoRows) {
		return arrivalSkipped("recently-replied"), nil
	}
	if err != nil {
		return arrivalResult{}, err
	}
	var capped int64
	err = tx.QueryRowContext(ctx, `UPDATE auto_replies SET window_count=CASE WHEN window_day=current_date THEN window_count+1 ELSE 1 END,
			window_day=current_date WHERE id=$1 AND (window_day IS DISTINCT FROM current_date OR window_count<$2) RETURNING id`,
		job.RuleID, r.autoReplyDailyLimit()).Scan(&capped)
	if errors.Is(err, sql.ErrNoRows) {
		return arrivalSkipped("daily-cap"), nil
	}
	if err != nil {
		return arrivalResult{}, err
	}

	headers := map[string]string{"Auto-Submitted": "auto-replied", "X-Auto-Response-Suppress": "All"}
	if id := strings.TrimSpace(p.MessageID); id != "" && !strings.ContainsAny(id, "\r\n") {
		headers["In-Reply-To"] = id
		headers["References"] = strings.TrimSpace(p.References + " " + id)
	}
	_, payload, err := r.tx.SendAutomated(ctx, tx, &AutomatedSend{
		OrgID: job.OrgID, IdentityID: job.IdentityID, ActingUserID: job.UserID, To: p.Sender,
		Subject: autoReplySubject(subject, p.Subject), Text: text, HTML: html, Headers: headers,
		Kind: "auto_reply", Ref: ruleUUID, DedupeKey: systemDedupeKey(fmt.Sprintf("mailat:ar:%d:", job.IdentityID), job.SESMessageID),
	})
	if err != nil {
		return systemSendFailure(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE auto_replies SET reply_count=COALESCE(reply_count,0)+1,last_replied_at=now(),last_error=NULL,updated_at=now() WHERE id=$1`, job.RuleID); err != nil {
		return arrivalResult{}, err
	}
	return arrivalResult{Status: "done", Payload: payload}, nil
}

// systemSendFailure classifies a SendAutomated error: validation problems are
// final, anything else (including the monthly quota) is retried.
func systemSendFailure(err error) (arrivalResult, error) {
	var invalid *provider.MailValidationError
	switch {
	case errors.Is(err, ErrSystemRecipientSuppressed):
		return arrivalSkipped("suppressed"), nil
	case errors.Is(err, ErrProviderNotConfigured):
		return arrivalFailed("provider-not-configured"), nil
	case errors.As(err, &invalid):
		return arrivalFailed(invalid.Message), nil
	}
	return arrivalResult{}, err
}

// runPush delivers a new-mail notification while the user's copy still sits
// in their inbox, so a deleted copy or a removed shared-mailbox member never
// sees its sender and subject. Delivery outcomes per device are the pusher's
// concern; the job retries only on an error it returns.
func (r *ArrivalRunner) runPush(ctx context.Context, job *arrivalJob) (arrivalResult, error) {
	if r.push == nil {
		return arrivalSkipped("push-not-configured"), nil
	}
	var p arrivalPush
	if err := json.Unmarshal(job.Payload, &p); err != nil || p.UUID == "" {
		return arrivalFailed("invalid-payload"), nil
	}
	var present bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM received_emails WHERE id=$1 AND mailbox_owner_id=$2 AND folder='inbox')`, job.EmailID, job.UserID).Scan(&present)
	if err != nil {
		return arrivalResult{}, err
	}
	if job.EmailID == 0 || !present {
		return arrivalSkipped("copy-gone"), nil
	}
	pushCtx, cancel := context.WithTimeout(ctx, arrivalPushBudget)
	defer cancel()
	if err := r.push.SendNewEmailNotification(pushCtx, job.UserID, p.UUID, p.From, p.Subject, p.Identity); err != nil {
		return arrivalResult{}, err
	}
	return arrivalDone(), nil
}

// systemDedupeKey joins prefix and id, hashing the id when the key would
// exceed the 128-character limit of SendAutomated.
func systemDedupeKey(prefix, id string) string {
	if len(prefix)+len(id) <= 128 {
		return prefix + id
	}
	sum := sha256.Sum256([]byte(id))
	return prefix + hex.EncodeToString(sum[:])
}

func containsInt64(values []int64, target int64) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
