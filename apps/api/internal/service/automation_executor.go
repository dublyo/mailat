package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
)

// The automation executor polls Postgres like eventoutbox.Run and needs no
// Redis. EnrollPending turns trigger events into enrollments; StepDue leases
// due enrollments and runs one node per transaction, so a node's side effect,
// its step run and the enrollment's advance commit together. Leases are
// claim-token guarded: a stale worker can never release, retry or advance an
// enrollment another worker has reclaimed.

const (
	automationTick          = 2 * time.Second
	automationLease         = 2 * time.Minute
	automationEventBatch    = 200
	automationClaimBatch    = 50
	automationMaxChain      = 25
	automationCleanupEvery  = time.Hour
	automationEventRetain   = 7 * 24 * time.Hour
	automationCleanupBatch  = 500
	automationReentryWindow = "24 hours"
)

// automationBackoff is the wait before each retry of a transiently failing step.
var automationBackoff = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 6 * time.Hour}

// automationRetryDelay returns the wait before the next attempt after a
// transient failure at retryCount retries so far; fail is true once every
// retry has been used.
func automationRetryDelay(retryCount int) (delay time.Duration, fail bool) {
	if retryCount < 0 {
		retryCount = 0
	}
	if retryCount >= len(automationBackoff) {
		return 0, true
	}
	return automationBackoff[retryCount], false
}

// automationEligibleSQL is the contact predicate shared by enrollment and the
// email step: an active contact on neither suppression table (M3's predicate).
var automationEligibleSQL = "c.status='active' AND NOT " + campaignSuppressedSQL("c.org_id", "c.email")

// AutomationMailSender queues an automation email inside the step transaction.
// It must only write rows in tx and never call the provider before commit; a
// separate leased runner sends afterwards. It must be idempotent per
// (enrollment, node) and return before writing anything when it returns
// ErrAutomationRecipientSuppressed or *provider.MailValidationError.
type AutomationMailSender interface {
	EnqueueAutomationEmail(ctx context.Context, tx *sql.Tx, m AutomationEmail) (messageID int64, err error)
}

type AutomationEmail struct {
	OrgID, SenderUserID, ContactID, IdentityID, TemplateID, AutomationID  int64
	EnrollmentID, VersionID                                               int64
	TemplateUUID, SubjectOverride, IdempotencyKey, EnrollmentUUID, NodeID string
	TrackOpens, TrackClicks                                               bool
}

var ErrAutomationRecipientSuppressed = errors.New("recipient suppressed")

type AutomationExecutor struct {
	db     *sql.DB
	cfg    *config.Config
	sender AutomationMailSender
	graphs sync.Map // version id -> *CompiledGraph; versions are immutable
	now    func() time.Time
}

func NewAutomationExecutor(db *sql.DB, cfg *config.Config, sender AutomationMailSender) *AutomationExecutor {
	return &AutomationExecutor{db: db, cfg: cfg, sender: sender, now: time.Now}
}

// RunAutomationExecutor enrolls and steps until ctx is cancelled. It runs in
// every API process whatever WORKER_ENABLED says; SKIP LOCKED makes replicas safe.
func RunAutomationExecutor(ctx context.Context, db *sql.DB, cfg *config.Config, sender AutomationMailSender) {
	x := NewAutomationExecutor(db, cfg, sender)
	lastCleanup := time.Now()
	wait := automationTick
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		enrolled, err := x.EnrollPending(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("Automation enroller will retry: %v", err)
		}
		stepped, err := x.StepDue(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("Automation executor will retry: %v", err)
		}
		wait = automationTick
		if enrolled >= automationEventBatch || stepped >= automationClaimBatch {
			wait = 0
		}
		if time.Since(lastCleanup) > automationCleanupEvery {
			x.cleanup(ctx)
			lastCleanup = time.Now()
		}
	}
}

// cleanup deletes trigger events processed more than a week ago, in batches.
func (x *AutomationExecutor) cleanup(ctx context.Context) {
	for ctx.Err() == nil {
		res, err := x.db.ExecContext(ctx, `DELETE FROM automation_trigger_events WHERE id IN (
			SELECT id FROM automation_trigger_events WHERE processed_at < now() - $1::interval LIMIT $2)`,
			fmt.Sprintf("%d seconds", int(automationEventRetain.Seconds())), automationCleanupBatch)
		if err != nil {
			log.Printf("Automation trigger event cleanup failed: %v", err)
			return
		}
		if n, _ := res.RowsAffected(); n < automationCleanupBatch {
			return
		}
	}
}

// graph returns the compiled graph of an immutable published version.
func (x *AutomationExecutor) graph(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, versionID int64) (*CompiledGraph, error) {
	if g, ok := x.graphs.Load(versionID); ok {
		return g.(*CompiledGraph), nil
	}
	var raw []byte
	if err := q.QueryRowContext(ctx, `SELECT workflow FROM automation_versions WHERE id=$1`, versionID).Scan(&raw); err != nil {
		return nil, err
	}
	var w model.Workflow
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, permanentStep("The published version cannot be read")
	}
	g, errs := CompileForPublish(&w, "", nil)
	if len(errs) > 0 {
		return nil, permanentStep("The published version is not valid")
	}
	x.graphs.Store(versionID, g)
	return g, nil
}

type automationEvent struct {
	id, orgID, contactID int64
	eventType, source    string
	listID               sql.NullInt64
	occurredAt           time.Time
}

type automationCandidate struct {
	id, orgID, versionID int64
	triggerType, reentry string
	activatedAt          sql.NullTime
}

// EnrollPending turns up to one batch of trigger events into enrollments and
// marks them processed, in one transaction. It returns the events handled.
func (x *AutomationExecutor) EnrollPending(ctx context.Context) (int, error) {
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, org_id, event_type, contact_id, list_id, source, occurred_at
		FROM automation_trigger_events WHERE processed_at IS NULL ORDER BY id FOR UPDATE SKIP LOCKED LIMIT $1`, automationEventBatch)
	if err != nil {
		return 0, fmt.Errorf("claim trigger events: %w", err)
	}
	var events []automationEvent
	var ids []int64
	var orgs []int64
	for rows.Next() {
		var e automationEvent
		if err := rows.Scan(&e.id, &e.orgID, &e.eventType, &e.contactID, &e.listID, &e.source, &e.occurredAt); err != nil {
			rows.Close()
			return 0, err
		}
		events = append(events, e)
		ids = append(ids, e.id)
		if !slices.Contains(orgs, e.orgID) {
			orgs = append(orgs, e.orgID)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(events) == 0 {
		return 0, nil
	}

	// FOR SHARE serializes with archive/pause (FOR UPDATE / UPDATE), so no
	// enrollment is added to an automation archived concurrently.
	rows, err = tx.QueryContext(ctx, `SELECT a.id, a.org_id, v.id, v.trigger_type, v.reentry_policy, a.activated_at
		FROM automations a JOIN automation_versions v ON v.id = a.published_version_id
		WHERE a.status='active' AND a.org_id = ANY($1) AND v.trigger_type IN ('contact.created','contact.subscribed')
		ORDER BY a.id FOR SHARE OF a`, pq.Array(orgs))
	if err != nil {
		return 0, fmt.Errorf("load automations: %w", err)
	}
	var candidates []automationCandidate
	for rows.Next() {
		var c automationCandidate
		if err := rows.Scan(&c.id, &c.orgID, &c.versionID, &c.triggerType, &c.reentry, &c.activatedAt); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	listIDs := map[string]sql.NullInt64{} // "org:uuid" -> list id
	for _, c := range candidates {
		g, err := x.graph(ctx, tx, c.versionID)
		if err != nil {
			log.Printf("Automation %d version %d cannot enroll: %v", c.id, c.versionID, err)
			continue
		}
		trig, first := g.Trigger(), g.Next[g.TriggerID]
		if first == "" {
			continue
		}
		var listID sql.NullInt64
		if trig.ListUUID != "" {
			key := fmt.Sprint(c.orgID, ":", trig.ListUUID)
			id, ok := listIDs[key]
			if !ok {
				err := tx.QueryRowContext(ctx, `SELECT id FROM lists WHERE uuid=$1 AND org_id=$2`, trig.ListUUID, c.orgID).Scan(&id)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return 0, err
				}
				listIDs[key] = id
			}
			if !id.Valid {
				continue
			}
			listID = id
		}
		for _, e := range events {
			if e.orgID != c.orgID || e.eventType != c.triggerType || !slices.Contains(trig.Sources, e.source) ||
				(listID.Valid && (!e.listID.Valid || e.listID.Int64 != listID.Int64)) ||
				!c.activatedAt.Valid || e.occurredAt.Before(c.activatedAt.Time) {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO automation_enrollments (automation_id, contact_id, org_id, status, version_id, current_node_id,
					trigger_event_id, next_run_at, enrolled_at, updated_at)
				SELECT $1, c.id, c.org_id, 'active', $2, $3, $4, now(), now(), now() FROM contacts c
				WHERE c.id=$5 AND c.org_id=$6 AND `+automationEligibleSQL+`
				  AND NOT EXISTS (SELECT 1 FROM automation_enrollments x WHERE x.automation_id=$1 AND x.contact_id=c.id
				      AND ($7='never' OR x.status='active' OR x.enrolled_at > now() - interval '`+automationReentryWindow+`'))
				ON CONFLICT DO NOTHING`,
				c.id, c.versionID, first, e.id, e.contactID, c.orgID, c.reentry); err != nil {
				return 0, fmt.Errorf("enroll contact %d in automation %d: %w", e.contactID, c.id, err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE automation_trigger_events SET processed_at=now() WHERE id = ANY($1)`, pq.Array(ids)); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(events), nil
}

// StepDue leases up to one batch of due enrollments and runs each until it
// waits, ends or has run automationMaxChain nodes. It returns the number claimed.
func (x *AutomationExecutor) StepDue(ctx context.Context) (int, error) {
	token := uuid.NewString()
	rows, err := x.db.QueryContext(ctx, `
		UPDATE automation_enrollments SET locked_until = now() + $2::interval, claim_token = $1
		WHERE id IN (SELECT e.id FROM automation_enrollments e JOIN automations a ON a.id = e.automation_id
			WHERE e.status='active' AND a.status='active' AND e.next_run_at <= now()
			  AND (e.locked_until IS NULL OR e.locked_until < now())
			ORDER BY e.next_run_at, e.id FOR UPDATE OF e SKIP LOCKED LIMIT $3)
		RETURNING id`, token, automationLeaseInterval, automationClaimBatch)
	if err != nil {
		return 0, fmt.Errorf("claim enrollments: %w", err)
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
	slices.Sort(ids)
	for _, id := range ids {
		if ctx.Err() != nil {
			break // leases expire and another worker resumes
		}
		if err := x.processEnrollment(ctx, id, token); err != nil && ctx.Err() == nil {
			log.Printf("Automation enrollment %d: %v", id, err)
		}
	}
	return len(ids), nil
}

var automationLeaseInterval = fmt.Sprintf("%d seconds", int(automationLease.Seconds()))

// stepError is a node failure; permanent ones fail the enrollment at once.
type stepError struct {
	msg       string
	permanent bool
}

func (e *stepError) Error() string { return e.msg }

func permanentStep(format string, args ...any) error {
	return &stepError{msg: fmt.Sprintf(format, args...), permanent: true}
}

// classifyStepError decides whether a failed step is retried. Constraint
// violations and invalid mail are permanent; other errors are transient.
func classifyStepError(err error) (msg string, permanent bool) {
	var se *stepError
	var mv *provider.MailValidationError
	var pg *pq.Error
	switch {
	case errors.As(err, &se):
		return se.msg, se.permanent
	case errors.As(err, &mv):
		return truncateRunes(mv.Message, 300), true
	case errors.As(err, &pg) && pg.Code.Class() == "23":
		return "The step violated a database constraint", true
	}
	return "Temporary error; the step will be retried", false
}

// enrollmentStep is the leased enrollment a step transaction works on.
type enrollmentStep struct {
	id, automationID, contactID, orgID, versionID int64
	uuid, nodeID                                  string
	retryCount                                    int
	publishedBy                                   sql.NullInt64
	kind                                          NodeKind
}

// stepResult is what running one node produced.
type stepResult struct {
	status    string // succeeded, skipped, waiting
	outcome   string
	resumeAt  *time.Time
	messageID *int64
	webhookID *string
	exit      string // non-empty: the enrollment exits with this reason
}

func (x *AutomationExecutor) processEnrollment(ctx context.Context, id int64, token string) error {
	defer x.release(id, token)
	for i := 0; i < automationMaxChain; i++ {
		more, st, err := x.step(ctx, id, token)
		if err != nil {
			if ctx.Err() != nil {
				return nil // not the step's fault; the lease expires and it resumes
			}
			return x.fail(ctx, st, token, err)
		}
		if !more {
			return nil
		}
	}
	return nil
}

// release drops the lease if this worker still holds it.
func (x *AutomationExecutor) release(id int64, token string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := x.db.ExecContext(ctx, `UPDATE automation_enrollments SET locked_until=NULL, claim_token=NULL WHERE id=$1 AND claim_token=$2`, id, token); err != nil {
		log.Printf("Automation enrollment %d release failed: %v", id, err)
	}
}

// step runs the enrollment's current node in one transaction and reports
// whether the next node may run immediately.
func (x *AutomationExecutor) step(ctx context.Context, id int64, token string) (bool, *enrollmentStep, error) {
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return false, nil, err
	}
	defer tx.Rollback()
	st := &enrollmentStep{id: id}
	err = tx.QueryRowContext(ctx, `SELECT uuid, automation_id, contact_id, org_id, version_id, current_node_id, retry_count
		FROM automation_enrollments WHERE id=$1 AND claim_token=$2 AND status='active' FOR UPDATE`, id, token).
		Scan(&st.uuid, &st.automationID, &st.contactID, &st.orgID, &st.versionID, &st.nodeID, &st.retryCount)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, nil // ended, cancelled or reclaimed by another worker
	}
	if err != nil {
		return false, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE automation_enrollments SET locked_until = now() + $2::interval WHERE id=$1`, id, automationLeaseInterval); err != nil {
		return false, st, err
	}
	// The automation row is read without a lock so the executor never
	// deadlocks with archive, which locks it before the enrollments.
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT a.status, v.published_by FROM automations a, automation_versions v WHERE a.id=$1 AND v.id=$2`,
		st.automationID, st.versionID).Scan(&status, &st.publishedBy); err != nil {
		return false, st, err
	}
	if status != "active" {
		return false, nil, nil
	}
	g, err := x.graph(ctx, tx, st.versionID)
	if err != nil {
		return false, st, err
	}
	node := g.Nodes[st.nodeID]
	if node == nil {
		return false, st, permanentStep("Step %s does not exist in version %d", st.nodeID, st.versionID)
	}
	st.kind = node.Kind

	var cStatus string
	var suppressed bool
	var facts ContactFacts
	var attrs []byte
	err = tx.QueryRowContext(ctx, `SELECT c.status, `+campaignSuppressedSQL("c.org_id", "c.email")+`, c.engagement_score, COALESCE(c.attributes,'{}'::jsonb)
		FROM contacts c WHERE c.id=$1 AND c.org_id=$2`, st.contactID, st.orgID).Scan(&cStatus, &suppressed, &facts.EngagementScore, &attrs)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, st, x.endCommit(ctx, tx, st, "cancelled", "contact_deleted")
	case err != nil:
		return false, st, err
	case suppressed:
		return false, st, x.endCommit(ctx, tx, st, "exited", "suppressed")
	case cStatus == "unsubscribed":
		return false, st, x.endCommit(ctx, tx, st, "exited", "unsubscribed")
	case cStatus != "active":
		return false, st, x.endCommit(ctx, tx, st, "exited", "inactive")
	}
	_ = json.Unmarshal(attrs, &facts.Attributes)

	// A node whose run already committed is never repeated (crash replay).
	var prevStatus, prevOutcome sql.NullString
	var prevResume sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT status, outcome, resume_at FROM automation_step_runs WHERE enrollment_id=$1 AND node_id=$2`,
		id, st.nodeID).Scan(&prevStatus, &prevOutcome, &prevResume)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, st, err
	}
	var res *stepResult
	switch {
	case prevStatus.String == "succeeded" || prevStatus.String == "skipped":
		res = &stepResult{status: prevStatus.String, outcome: prevOutcome.String}
	case node.Kind == KindDelay && prevStatus.String == "waiting" && prevResume.Valid:
		if x.now().Before(prevResume.Time) {
			t := prevResume.Time
			res = &stepResult{status: "waiting", resumeAt: &t}
		} else {
			res = &stepResult{status: "succeeded"}
		}
	default:
		if res, err = x.runNode(ctx, tx, st, g, node, facts); err != nil {
			return false, st, err
		}
	}
	more, err := x.advance(ctx, tx, st, g, res)
	if err != nil {
		return false, st, err
	}
	return more, st, tx.Commit()
}

func (x *AutomationExecutor) runNode(ctx context.Context, tx *sql.Tx, st *enrollmentStep, g *CompiledGraph, n *CompiledNode, facts ContactFacts) (*stepResult, error) {
	switch n.Kind {
	case KindTrigger:
		return &stepResult{status: "succeeded"}, nil
	case KindDelay:
		t := x.now().Add(n.Delay.Total())
		return &stepResult{status: "waiting", resumeAt: &t}, nil
	case KindCondition:
		var opened, clicked bool
		if n.Cond.EmailNodeID != "" {
			err := tx.QueryRowContext(ctx, `SELECT COALESCE(m.open_count,0) > 0, COALESCE(m.click_count,0) > 0
				FROM automation_step_runs r LEFT JOIN automation_messages m ON m.id = r.message_id
				WHERE r.enrollment_id=$1 AND r.node_id=$2 AND r.status='succeeded'`, st.id, n.Cond.EmailNodeID).Scan(&opened, &clicked)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		}
		outcome := "no"
		if EvalCondition(n.Cond, facts, opened, clicked) {
			outcome = "yes"
		}
		return &stepResult{status: "succeeded", outcome: outcome}, nil
	case KindAction:
		return x.runAction(ctx, tx, st, n.Action)
	case KindEmail:
		return x.runEmail(ctx, tx, st, g, n)
	case KindWebhook:
		return nil, permanentStep("Webhook steps are not available yet")
	}
	return nil, permanentStep("Unknown step type %s", n.Kind)
}

func (x *AutomationExecutor) runAction(ctx context.Context, tx *sql.Tx, st *enrollmentStep, a *ActionCfg) (*stepResult, error) {
	if a.Action == "update_field" {
		_, err := tx.ExecContext(ctx, `UPDATE contacts SET attributes = COALESCE(attributes,'{}'::jsonb) || jsonb_build_object($3::text, $4::text), updated_at=now()
			WHERE id=$1 AND org_id=$2`, st.contactID, st.orgID, a.Attribute, a.Value)
		if err != nil {
			return nil, err
		}
		return &stepResult{status: "succeeded", outcome: "updated"}, nil
	}
	var listID int64
	// Locking the list first makes the recount below see every committed membership.
	err := tx.QueryRowContext(ctx, `SELECT id FROM lists WHERE uuid=$1 AND org_id=$2 AND type='static' FOR NO KEY UPDATE`, a.ListUUID, st.orgID).Scan(&listID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, permanentStep("The list of step %s no longer exists", st.nodeID)
	}
	if err != nil {
		return nil, err
	}
	var res sql.Result
	outcome := [2]string{"added", "already_member"}
	if a.Action == "add_to_list" {
		res, err = tx.ExecContext(ctx, `INSERT INTO list_contacts (list_id, contact_id, source) VALUES ($1, $2, 'automation')
			ON CONFLICT (list_id, contact_id) DO NOTHING`, listID, st.contactID)
	} else {
		outcome = [2]string{"removed", "not_member"}
		res, err = tx.ExecContext(ctx, `DELETE FROM list_contacts WHERE list_id=$1 AND contact_id=$2`, listID, st.contactID)
	}
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return &stepResult{status: "succeeded", outcome: outcome[1]}, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE lists SET contact_count = (SELECT count(*) FROM list_contacts WHERE list_id=$1), updated_at=now() WHERE id=$1`, listID); err != nil {
		return nil, err
	}
	return &stepResult{status: "succeeded", outcome: outcome[0]}, nil
}

// runEmail re-checks the sender rule against the version's publisher and the
// trigger list membership, then queues the message in this transaction.
func (x *AutomationExecutor) runEmail(ctx context.Context, tx *sql.Tx, st *enrollmentStep, g *CompiledGraph, n *CompiledNode) (*stepResult, error) {
	if x.sender == nil {
		return nil, permanentStep("Email sending is not configured")
	}
	if !st.publishedBy.Valid {
		return nil, permanentStep("The user who published this version no longer exists")
	}
	var problems []string
	if err := checkAutomationIdentity(ctx, tx, st.orgID, st.publishedBy.Int64, n.ID, n.Email.IdentityUUID,
		func(_, _, msg string) { problems = append(problems, msg) }); err != nil {
		return nil, err
	}
	if len(problems) > 0 {
		return nil, permanentStep("%s", problems[0])
	}
	var identityID, templateID int64
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT id FROM identities WHERE uuid=$1`, n.Email.IdentityUUID).Scan(&identityID); err != nil {
		return nil, err
	}
	err := tx.QueryRowContext(ctx, `SELECT id, is_active FROM email_templates WHERE uuid=$1 AND org_id=$2`, n.Email.TemplateUUID, st.orgID).Scan(&templateID, &active)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, permanentStep("The template of step %s no longer exists", n.ID)
	case err != nil:
		return nil, err
	case !active:
		return nil, permanentStep("The template of step %s is inactive", n.ID)
	}
	if trig := g.Trigger(); trig.ListUUID != "" {
		var member bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM list_contacts lc JOIN lists l ON l.id = lc.list_id
			WHERE l.uuid=$1 AND l.org_id=$2 AND lc.contact_id=$3)`, trig.ListUUID, st.orgID, st.contactID).Scan(&member); err != nil {
			return nil, err
		}
		if !member {
			return &stepResult{status: "skipped", outcome: "left_list", exit: "left_list"}, nil
		}
	}
	msgID, err := x.sender.EnqueueAutomationEmail(ctx, tx, AutomationEmail{
		OrgID: st.orgID, SenderUserID: st.publishedBy.Int64, ContactID: st.contactID, IdentityID: identityID, TemplateID: templateID,
		AutomationID: st.automationID, EnrollmentID: st.id, VersionID: st.versionID, TemplateUUID: n.Email.TemplateUUID,
		SubjectOverride: n.Email.Subject, IdempotencyKey: "automation:" + st.uuid + ":" + n.ID, EnrollmentUUID: st.uuid, NodeID: n.ID,
		TrackOpens: n.Email.TrackOpens, TrackClicks: n.Email.TrackClicks,
	})
	if errors.Is(err, ErrAutomationRecipientSuppressed) {
		return &stepResult{status: "skipped", outcome: "suppressed", exit: "suppressed"}, nil
	}
	if err != nil {
		return nil, err
	}
	return &stepResult{status: "succeeded", outcome: "queued", messageID: &msgID}, nil
}

// advance records the node's run and moves the enrollment on.
func (x *AutomationExecutor) advance(ctx context.Context, tx *sql.Tx, st *enrollmentStep, g *CompiledGraph, res *stepResult) (bool, error) {
	if err := upsertStepRun(ctx, tx, st, res.status, res.outcome, res.resumeAt, res.messageID, res.webhookID, ""); err != nil {
		return false, err
	}
	switch {
	case res.exit != "":
		return false, x.end(ctx, tx, st, "exited", res.exit)
	case res.status == "waiting":
		_, err := tx.ExecContext(ctx, `UPDATE automation_enrollments SET next_run_at=$2, retry_count=0, error_message=NULL, updated_at=now() WHERE id=$1`,
			st.id, *res.resumeAt)
		return false, err
	}
	next := g.Successor(st.nodeID, res.outcome)
	if next == "" {
		_, err := tx.ExecContext(ctx, `UPDATE automation_enrollments SET status='completed', completed_at=now(), retry_count=0, error_message=NULL,
			locked_until=NULL, claim_token=NULL, updated_at=now() WHERE id=$1`, st.id)
		return false, err
	}
	_, err := tx.ExecContext(ctx, `UPDATE automation_enrollments SET current_node_id=$2, next_run_at=now(), retry_count=0, error_message=NULL, updated_at=now() WHERE id=$1`,
		st.id, next)
	return err == nil, err
}

// end finishes the enrollment with status and reason.
func (x *AutomationExecutor) end(ctx context.Context, tx *sql.Tx, st *enrollmentStep, status, reason string) error {
	_, err := tx.ExecContext(ctx, `UPDATE automation_enrollments SET status=$2, exit_reason=$3, locked_until=NULL, claim_token=NULL, updated_at=now()
		WHERE id=$1`, st.id, status, reason)
	return err
}

// endCommit ends the enrollment before its node runs (the contact changed).
func (x *AutomationExecutor) endCommit(ctx context.Context, tx *sql.Tx, st *enrollmentStep, status, reason string) error {
	if err := x.end(ctx, tx, st, status, reason); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertStepRun(ctx context.Context, tx *sql.Tx, st *enrollmentStep, status, outcome string, resumeAt *time.Time, messageID *int64, webhookID *string, errMsg string) error {
	kind := string(st.kind)
	if kind == "" {
		kind = "unknown"
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO automation_step_runs (enrollment_id, automation_id, version_id, node_id, node_type, status, outcome, resume_at,
			message_id, webhook_event_id, error, finished_at)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7,''), $8, $9, $10, NULLIF($11,''), CASE WHEN $12 THEN NULL ELSE now() END)
		ON CONFLICT (enrollment_id, node_id) DO UPDATE SET status=EXCLUDED.status, outcome=EXCLUDED.outcome,
			resume_at=COALESCE(EXCLUDED.resume_at, automation_step_runs.resume_at),
			message_id=COALESCE(EXCLUDED.message_id, automation_step_runs.message_id),
			webhook_event_id=COALESCE(EXCLUDED.webhook_event_id, automation_step_runs.webhook_event_id),
			error=EXCLUDED.error, finished_at=EXCLUDED.finished_at`,
		st.id, st.automationID, st.versionID, st.nodeID, kind, status, outcome, resumeAt, messageID, webhookID, errMsg, status == "waiting")
	return err
}

// fail handles a rolled-back step: a transient error is retried with
// backoff, a permanent one (or the last retry) fails the enrollment. Every
// write is guarded by the claim token so a stale worker changes nothing.
func (x *AutomationExecutor) fail(ctx context.Context, st *enrollmentStep, token string, stepErr error) error {
	if st == nil {
		return stepErr
	}
	msg, permanent := classifyStepError(stepErr)
	if !permanent {
		log.Printf("Automation enrollment %d step %s failed: %v", st.id, st.nodeID, stepErr)
	}
	delay, exhausted := automationRetryDelay(st.retryCount)
	errText := truncateRunes(fmt.Sprintf("Step %s: %s", st.nodeID, msg), 500)
	if !permanent && !exhausted {
		_, err := x.db.ExecContext(ctx, `UPDATE automation_enrollments SET retry_count=retry_count+1, next_run_at=now() + $3::interval,
			error_message=$4, locked_until=NULL, claim_token=NULL, updated_at=now() WHERE id=$1 AND claim_token=$2 AND status='active'`,
			st.id, token, fmt.Sprintf("%d seconds", int(delay.Seconds())), errText)
		return err
	}
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE automation_enrollments SET status='failed', error_message=$3, locked_until=NULL, claim_token=NULL, updated_at=now()
		WHERE id=$1 AND claim_token=$2 AND status='active'`, st.id, token, errText)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	if err := upsertStepRun(ctx, tx, st, "failed", "", nil, nil, nil, msg); err != nil {
		return err
	}
	return tx.Commit()
}
