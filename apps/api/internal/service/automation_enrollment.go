package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lib/pq"

	"github.com/dublyo/mailat/api/internal/model"
)

// ErrAutomationEnrollmentNotFound is a 404 for an enrollment lookup.
var ErrAutomationEnrollmentNotFound = errors.New("enrollment not found")

// maxManualListEnroll bounds the synchronous INSERT … SELECT of a list enroll.
var maxManualListEnroll = 50000

// AutomationEnrollmentStatuses are the accepted enrollment list filters;
// waiting is the derived subset of active enrollments with a future step.
var AutomationEnrollmentStatuses = map[string]bool{"active": true, "waiting": true, "completed": true, "exited": true, "failed": true, "cancelled": true}

// Enroll manually enrolls one contact or every member of a list into the
// published version of an active automation, whatever its trigger. Contacts
// that are inactive, suppressed, already active in it or blocked by the
// version's re-entry policy are skipped.
func (s *AutomationService) Enroll(ctx context.Context, orgID int64, automationUUID string, req *model.EnrollRequest) (*model.EnrollResult, error) {
	if !validAutomationUUID(automationUUID) {
		return nil, ErrAutomationNotFound
	}
	if (req.ContactUUID == "") == (req.ListUUID == "") {
		return nil, automationErr("Send exactly one of contactUuid or listUuid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to enroll: %w", err)
	}
	defer tx.Rollback()

	// FOR SHARE serializes with archive and pause, like the trigger enroller.
	var automationID int64
	var status string
	var versionID sql.NullInt64
	var reentry sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT a.id, a.status, v.id, v.reentry_policy
		FROM automations a LEFT JOIN automation_versions v ON v.id = a.published_version_id
		WHERE a.uuid = $1 AND a.org_id = $2 FOR SHARE OF a`, automationUUID, orgID).Scan(&automationID, &status, &versionID, &reentry)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAutomationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load automation: %w", err)
	}
	if status != "active" || !versionID.Valid {
		return nil, automationErr("Only active automations can enroll contacts")
	}
	g, err := compileVersion(ctx, tx, versionID.Int64)
	if err != nil {
		return nil, fmt.Errorf("failed to load published version: %w", err)
	}
	first := g.Next[g.TriggerID]
	if first == "" {
		return nil, automationErr("The published version has no steps")
	}

	insert := `INSERT INTO automation_enrollments (automation_id, contact_id, org_id, status, version_id, current_node_id, next_run_at, enrolled_at, updated_at)
		SELECT $1, c.id, c.org_id, 'active', $2, $3, now(), now(), now() FROM contacts c %s
		WHERE c.org_id = $4 AND ` + automationEligibleSQL + ` AND ` + automationReentrySQL("$1", "$5") + ` AND %s
		ON CONFLICT DO NOTHING`
	var total int
	var res sql.Result
	if req.ContactUUID != "" {
		if !validAutomationUUID(req.ContactUUID) {
			return nil, automationErr("Contact not found")
		}
		var contactID int64
		err = tx.QueryRowContext(ctx, `SELECT id FROM contacts WHERE uuid = $1 AND org_id = $2`, req.ContactUUID, orgID).Scan(&contactID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, automationErr("Contact not found")
		}
		if err != nil {
			return nil, fmt.Errorf("failed to load contact: %w", err)
		}
		total = 1
		res, err = tx.ExecContext(ctx, fmt.Sprintf(insert, "", "c.id = $6"), automationID, versionID.Int64, first, orgID, reentry.String, contactID)
	} else {
		if !validAutomationUUID(req.ListUUID) {
			return nil, automationErr("List not found")
		}
		var listID int64
		err = tx.QueryRowContext(ctx, `SELECT id FROM lists WHERE uuid = $1 AND org_id = $2`, req.ListUUID, orgID).Scan(&listID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, automationErr("List not found")
		}
		if err != nil {
			return nil, fmt.Errorf("failed to load list: %w", err)
		}
		var eligible int
		if err = tx.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE `+automationEligibleSQL+`)
			FROM list_contacts lc JOIN contacts c ON c.id = lc.contact_id AND c.org_id = $2 WHERE lc.list_id = $1`,
			listID, orgID).Scan(&total, &eligible); err != nil {
			return nil, fmt.Errorf("failed to count list members: %w", err)
		}
		if eligible > maxManualListEnroll {
			return nil, automationErr("The list has more than %d eligible contacts; enroll a smaller list", maxManualListEnroll)
		}
		res, err = tx.ExecContext(ctx, fmt.Sprintf(insert, "JOIN list_contacts lc ON lc.contact_id = c.id", "lc.list_id = $6"),
			automationID, versionID.Int64, first, orgID, reentry.String, listID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to enroll: %w", err)
	}
	n, _ := res.RowsAffected()
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to enroll: %w", err)
	}
	return &model.EnrollResult{Enrolled: int(n), Skipped: total - int(n)}, nil
}

// enrollmentSelect scopes every enrollment to its automation in the org ($1
// org, $2 automation uuid). next_run_at is shown only while active.
const enrollmentSelect = `
	SELECT e.id, e.uuid::text, c.uuid::text, c.email, e.status, e.exit_reason, v.version, e.current_node_id,
	       CASE WHEN e.status = 'active' THEN e.next_run_at END, e.retry_count, e.error_message, e.enrolled_at, e.completed_at
	FROM automation_enrollments e
	JOIN automations a ON a.id = e.automation_id AND a.org_id = $1 AND a.uuid = $2
	JOIN contacts c ON c.id = e.contact_id
	LEFT JOIN automation_versions v ON v.id = e.version_id`

func scanEnrollment(row rowScanner) (int64, *model.AutomationEnrollmentView, error) {
	var id int64
	var e model.AutomationEnrollmentView
	err := row.Scan(&id, &e.UUID, &e.ContactUUID, &e.ContactEmail, &e.Status, &e.ExitReason, &e.Version, &e.CurrentNodeID,
		&e.NextRunAt, &e.RetryCount, &e.Error, &e.EnrolledAt, &e.CompletedAt)
	return id, &e, err
}

// enrollmentStatusSQL turns a validated filter into a predicate on e.
func enrollmentStatusSQL(status string) string {
	switch status {
	case "":
		return "true"
	case "waiting":
		return "e.status = 'active' AND e.next_run_at > now()"
	}
	return "e.status = '" + status + "'"
}

// ListEnrollments pages an automation's enrollments, newest first.
func (s *AutomationService) ListEnrollments(ctx context.Context, orgID int64, automationUUID, status string, page, pageSize int) (*model.AutomationEnrollmentListResult, error) {
	if !validAutomationUUID(automationUUID) {
		return nil, ErrAutomationNotFound
	}
	if status != "" && !AutomationEnrollmentStatuses[status] {
		return nil, automationErr("status must be one of active, waiting, completed, exited, failed, cancelled")
	}
	if _, err := s.automationStatus(ctx, orgID, automationUUID); err != nil {
		return nil, err
	}
	page, pageSize = ClampAutomationPage(page, pageSize)
	where := ` WHERE ` + enrollmentStatusSQL(status)
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM automation_enrollments e
		JOIN automations a ON a.id = e.automation_id AND a.org_id = $1 AND a.uuid = $2`+where, orgID, automationUUID).Scan(&total); err != nil {
		return nil, fmt.Errorf("failed to count enrollments: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, enrollmentSelect+where+` ORDER BY e.enrolled_at DESC, e.id DESC LIMIT $3 OFFSET $4`,
		orgID, automationUUID, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, fmt.Errorf("failed to list enrollments: %w", err)
	}
	defer rows.Close()
	out := &model.AutomationEnrollmentListResult{Enrollments: []model.AutomationEnrollmentView{}, Total: total, Page: page, PageSize: pageSize}
	for rows.Next() {
		_, e, err := scanEnrollment(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan enrollment: %w", err)
		}
		out.Enrollments = append(out.Enrollments, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list enrollments: %w", err)
	}
	return out, nil
}

// enrollmentMissing tells a missing automation (404) from a missing enrollment.
func (s *AutomationService) enrollmentMissing(ctx context.Context, orgID int64, automationUUID string) error {
	if _, err := s.automationStatus(ctx, orgID, automationUUID); err != nil {
		return err
	}
	return ErrAutomationEnrollmentNotFound
}

// GetEnrollment returns one enrollment with its step runs in order.
func (s *AutomationService) GetEnrollment(ctx context.Context, orgID int64, automationUUID, enrollmentUUID string) (*model.AutomationEnrollmentView, error) {
	if !validAutomationUUID(automationUUID) {
		return nil, ErrAutomationNotFound
	}
	if !validAutomationUUID(enrollmentUUID) {
		return nil, s.enrollmentMissing(ctx, orgID, automationUUID)
	}
	id, e, err := scanEnrollment(s.db.QueryRowContext(ctx, enrollmentSelect+` WHERE e.uuid = $3`, orgID, automationUUID, enrollmentUUID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, s.enrollmentMissing(ctx, orgID, automationUUID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get enrollment: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.node_id, r.node_type, r.status, r.outcome, m.message_uuid::text, r.error,
			r.started_at, r.finished_at, r.resume_at
		FROM automation_step_runs r LEFT JOIN automation_messages m ON m.id = r.message_id
		WHERE r.enrollment_id = $1 ORDER BY r.started_at, r.id`, id)
	if err != nil {
		return nil, fmt.Errorf("failed to load steps: %w", err)
	}
	defer rows.Close()
	e.Steps = []model.AutomationStepRunView{}
	for rows.Next() {
		var r model.AutomationStepRunView
		if err := rows.Scan(&r.NodeID, &r.NodeType, &r.Status, &r.Outcome, &r.MessageUUID, &r.Error, &r.StartedAt, &r.FinishedAt, &r.ResumeAt); err != nil {
			return nil, fmt.Errorf("failed to scan step: %w", err)
		}
		e.Steps = append(e.Steps, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to load steps: %w", err)
	}
	return e, nil
}

type lockedEnrollment struct {
	id                       int64
	status, automationStatus string
	versioned                bool
}

// lockEnrollment locks one enrollment row (never the automation row, like the
// executor) so cancel and retry serialize with a running step.
func (s *AutomationService) lockEnrollment(ctx context.Context, tx *sql.Tx, orgID int64, automationUUID, enrollmentUUID string) (*lockedEnrollment, error) {
	if !validAutomationUUID(automationUUID) {
		return nil, ErrAutomationNotFound
	}
	if !validAutomationUUID(enrollmentUUID) {
		return nil, s.enrollmentMissing(ctx, orgID, automationUUID)
	}
	var e lockedEnrollment
	err := tx.QueryRowContext(ctx, `SELECT e.id, e.status, a.status, e.version_id IS NOT NULL AND e.current_node_id IS NOT NULL
		FROM automation_enrollments e JOIN automations a ON a.id = e.automation_id AND a.org_id = $1 AND a.uuid = $2
		WHERE e.uuid = $3 FOR UPDATE OF e`, orgID, automationUUID, enrollmentUUID).Scan(&e.id, &e.status, &e.automationStatus, &e.versioned)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, s.enrollmentMissing(ctx, orgID, automationUUID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load enrollment: %w", err)
	}
	return &e, nil
}

// CancelEnrollment stops an active enrollment; its queued emails that no
// sender has claimed are not sent.
func (s *AutomationService) CancelEnrollment(ctx context.Context, orgID int64, automationUUID, enrollmentUUID string) (*model.AutomationEnrollmentView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to cancel enrollment: %w", err)
	}
	defer tx.Rollback()
	e, err := s.lockEnrollment(ctx, tx, orgID, automationUUID, enrollmentUUID)
	if err != nil {
		return nil, err
	}
	if e.status != "active" {
		return nil, automationErr("Only active enrollments can be cancelled")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE automation_enrollments SET status = 'cancelled', exit_reason = 'manual',
		locked_until = NULL, claim_token = NULL, updated_at = now() WHERE id = $1`, e.id); err != nil {
		return nil, fmt.Errorf("failed to cancel enrollment: %w", err)
	}
	if err = cancelPendingAutomationMessages(ctx, tx, orgID, "enrollment_id", e.id); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to cancel enrollment: %w", err)
	}
	return s.GetEnrollment(ctx, orgID, automationUUID, enrollmentUUID)
}

// RetryEnrollment resumes a failed enrollment at its failed step on the same
// pinned version. The failed step run is overwritten when the step runs again.
func (s *AutomationService) RetryEnrollment(ctx context.Context, orgID int64, automationUUID, enrollmentUUID string) (*model.AutomationEnrollmentView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to retry enrollment: %w", err)
	}
	defer tx.Rollback()
	e, err := s.lockEnrollment(ctx, tx, orgID, automationUUID, enrollmentUUID)
	if err != nil {
		return nil, err
	}
	switch {
	case e.status != "failed":
		return nil, automationErr("Only failed enrollments can be retried")
	case e.automationStatus != "active":
		return nil, automationErr("Activate the automation before retrying enrollments")
	case !e.versioned:
		return nil, automationErr("This enrollment was created before the automation executor and cannot be retried")
	}
	_, err = tx.ExecContext(ctx, `UPDATE automation_enrollments SET status = 'active', exit_reason = NULL, retry_count = 0, next_run_at = now(),
		error_message = NULL, completed_at = NULL, locked_until = NULL, claim_token = NULL, updated_at = now() WHERE id = $1`, e.id)
	var pg *pq.Error
	if errors.As(err, &pg) && pg.Code == "23505" {
		return nil, ErrAutomationConflict
	}
	if err != nil {
		return nil, fmt.Errorf("failed to retry enrollment: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to retry enrollment: %w", err)
	}
	return s.GetEnrollment(ctx, orgID, automationUUID, enrollmentUUID)
}

// cancelPendingAutomationMessages cancels queued automation emails that no
// sender has claimed (column is automation_id or enrollment_id) and refunds
// any monthly quota a throttled earlier attempt still holds.
func cancelPendingAutomationMessages(ctx context.Context, tx *sql.Tx, orgID int64, column string, id int64) error {
	if column != "automation_id" && column != "enrollment_id" {
		return fmt.Errorf("cancel queued emails: bad column %q", column)
	}
	var reserved int64
	err := tx.QueryRowContext(ctx, `WITH c AS (
			SELECT id, quota_reserved FROM automation_messages WHERE `+column+` = $1 AND status = 'pending' FOR UPDATE)
		, u AS (UPDATE automation_messages m SET status = 'cancelled', quota_reserved = false, updated_at = now()
			FROM c WHERE m.id = c.id RETURNING c.quota_reserved)
		SELECT count(*) FILTER (WHERE quota_reserved) FROM u`, id).Scan(&reserved)
	if err != nil {
		return fmt.Errorf("failed to cancel queued emails: %w", err)
	}
	return refundMonthlySendsTx(ctx, tx, orgID, reserved)
}
