package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/eventoutbox"
	"github.com/dublyo/mailat/api/internal/model"
)

type AutomationService struct {
	db  *sql.DB
	cfg *config.Config
}

func NewAutomationService(db *sql.DB, cfg *config.Config) *AutomationService {
	return &AutomationService{db: db, cfg: cfg}
}

var (
	ErrAutomationNotFound = errors.New("automation not found")
	// ErrAutomationConflict is a 409: the contact already has an active enrollment.
	ErrAutomationConflict = errors.New("contact already has an active enrollment")
)

// AutomationError is a 400 whose message is safe to show.
type AutomationError struct{ Message string }

func (e *AutomationError) Error() string { return e.Message }

func automationErr(format string, args ...any) error {
	return &AutomationError{Message: fmt.Sprintf(format, args...)}
}

// AutomationStatuses are the stored lifecycle states.
var AutomationStatuses = map[string]bool{"draft": true, "active": true, "paused": true, "archived": true}

const (
	maxAutomationPageSize = 100
	maxAutomationNameLen  = 255
)

// ClampAutomationPage bounds pagination so any page/pageSize is valid SQL.
func ClampAutomationPage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > maxAutomationPageSize {
		pageSize = maxAutomationPageSize
	}
	return page, pageSize
}

// automationSelect joins the published version and live enrollment counts.
// "waiting" is the subset of active enrollments whose next step is in the future.
const automationSelect = `
	SELECT a.id, a.uuid, a.org_id, a.name, COALESCE(a.description,''), COALESCE(a.trigger_type,''),
	       COALESCE(a.trigger_config,'{}'::jsonb), COALESCE(a.workflow,'{}'::jsonb), a.status, a.reentry_policy,
	       v.version, v.graph_hash, v.reentry_policy, a.activated_at, a.archived_at, a.created_at, a.updated_at,
	       n.enrolled, n.active, n.waiting, n.completed, n.exited, n.failed, n.cancelled
	FROM automations a
	LEFT JOIN automation_versions v ON v.id = a.published_version_id
	LEFT JOIN LATERAL (
		SELECT count(*) AS enrolled,
		       count(*) FILTER (WHERE e.status='active') AS active,
		       count(*) FILTER (WHERE e.status='active' AND e.next_run_at > now()) AS waiting,
		       count(*) FILTER (WHERE e.status='completed') AS completed,
		       count(*) FILTER (WHERE e.status='exited') AS exited,
		       count(*) FILTER (WHERE e.status='failed') AS failed,
		       count(*) FILTER (WHERE e.status='cancelled') AS cancelled
		FROM automation_enrollments e WHERE e.automation_id = a.id
	) n ON true`

func scanAutomation(row rowScanner) (*model.Automation, error) {
	var a model.Automation
	var triggerCfg, workflow []byte
	var version sql.NullInt64
	var hash, versionReentry sql.NullString
	err := row.Scan(&a.ID, &a.UUID, &a.OrgID, &a.Name, &a.Description, &a.TriggerType, &triggerCfg, &workflow, &a.Status, &a.ReentryPolicy,
		&version, &hash, &versionReentry, &a.ActivatedAt, &a.ArchivedAt, &a.CreatedAt, &a.UpdatedAt,
		&a.Stats.Enrolled, &a.Stats.Active, &a.Stats.Waiting, &a.Stats.Completed, &a.Stats.Exited, &a.Stats.Failed, &a.Stats.Cancelled)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(triggerCfg, &a.TriggerConfig)
	a.Workflow = &model.Workflow{}
	_ = json.Unmarshal(workflow, a.Workflow)
	if version.Valid {
		v := int(version.Int64)
		a.PublishedVersion = &v
		a.HasUnpublishedChanges = draftHash(a.Workflow) != hash.String || a.ReentryPolicy != versionReentry.String
	}
	a.EnrolledCount, a.CompletedCount, a.InProgressCount = a.Stats.Enrolled, a.Stats.Completed, a.Stats.Active
	return &a, nil
}

// draftHash hashes the draft as publish would; a draft that cannot be
// normalized never matches a published hash.
func draftHash(w *model.Workflow) string {
	norm, _, _, err := NormalizeWorkflow(w, "", nil)
	if err != nil {
		return ""
	}
	return WorkflowHash(norm)
}

func validAutomationUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

func cleanAutomationName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxAutomationNameLen || strings.ContainsAny(name, "\r\n") {
		return "", automationErr("name must be 1-%d characters on one line", maxAutomationNameLen)
	}
	return name, nil
}

// CreateAutomation stores a draft. The graph is normalized and structurally
// validated; full validation waits for validate/activate.
func (s *AutomationService) CreateAutomation(ctx context.Context, orgID, userID int64, req *model.CreateAutomationRequest) (*model.Automation, error) {
	name, err := cleanAutomationName(req.Name)
	if err != nil {
		return nil, err
	}
	workflow, triggerType, triggerConfig, err := PrepareAutomationDraft(req.Workflow, req.TriggerType, req.TriggerConfig)
	if err != nil {
		return nil, err
	}
	reentry := "never"
	if req.ReentryPolicy != nil {
		if reentry = *req.ReentryPolicy; !ValidReentryPolicy(reentry) {
			return nil, automationErr("reentryPolicy must be never or after_exit")
		}
	}
	workflowJSON, err := json.Marshal(workflow)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize workflow: %w", err)
	}
	triggerConfigJSON, err := json.Marshal(triggerConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize trigger config: %w", err)
	}
	var createdBy any
	if userID > 0 {
		createdBy = userID
	}
	var id string
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO automations (uuid, org_id, name, description, trigger_type, trigger_config, workflow, status, reentry_policy, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'draft', $8, $9, now(), now())
		RETURNING uuid`,
		uuid.New().String(), orgID, name, req.Description, triggerType, triggerConfigJSON, workflowJSON, reentry, createdBy,
	).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("failed to create automation: %w", err)
	}
	return s.GetAutomation(ctx, orgID, id)
}

// GetAutomation returns the draft, its published version and live counts.
func (s *AutomationService) GetAutomation(ctx context.Context, orgID int64, automationUUID string) (*model.Automation, error) {
	if !validAutomationUUID(automationUUID) {
		return nil, ErrAutomationNotFound
	}
	a, err := scanAutomation(s.db.QueryRowContext(ctx, automationSelect+` WHERE a.uuid = $1 AND a.org_id = $2`, automationUUID, orgID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAutomationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get automation: %w", err)
	}
	return a, nil
}

// ListAutomations pages automations with live counts. page and pageSize are
// clamped; an unknown status is a 400.
func (s *AutomationService) ListAutomations(ctx context.Context, orgID int64, page, pageSize int, status string) (*model.AutomationListResult, error) {
	page, pageSize = ClampAutomationPage(page, pageSize)
	if status != "" && !AutomationStatuses[status] {
		return nil, automationErr("status must be one of draft, active, paused, archived")
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM automations WHERE org_id = $1 AND ($2 = '' OR status = $2)`, orgID, status).Scan(&total); err != nil {
		return nil, fmt.Errorf("failed to count automations: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, automationSelect+`
		WHERE a.org_id = $1 AND ($2 = '' OR a.status = $2)
		ORDER BY a.created_at DESC, a.id DESC LIMIT $3 OFFSET $4`,
		orgID, status, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, fmt.Errorf("failed to list automations: %w", err)
	}
	defer rows.Close()
	automations := []model.AutomationSummary{}
	for rows.Next() {
		a, err := scanAutomation(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan automation: %w", err)
		}
		automations = append(automations, model.AutomationSummary{
			ID: a.ID, UUID: a.UUID, OrgID: a.OrgID, Name: a.Name, Description: a.Description, TriggerType: a.TriggerType,
			TriggerConfig: a.TriggerConfig, Status: a.Status, ReentryPolicy: a.ReentryPolicy, PublishedVersion: a.PublishedVersion,
			HasUnpublishedChanges: a.HasUnpublishedChanges, ActivatedAt: a.ActivatedAt, ArchivedAt: a.ArchivedAt, Stats: a.Stats,
			EnrolledCount: a.EnrolledCount, CompletedCount: a.CompletedCount, InProgressCount: a.InProgressCount,
			CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list automations: %w", err)
	}
	return &model.AutomationListResult{Automations: automations, Total: total, Page: page, PageSize: pageSize}, nil
}

// automationStatus distinguishes "not found" from a guarded write that
// matched nothing because of the automation's status.
func (s *AutomationService) automationStatus(ctx context.Context, orgID int64, automationUUID string) (string, error) {
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM automations WHERE uuid = $1 AND org_id = $2`, automationUUID, orgID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrAutomationNotFound
	}
	if err != nil {
		return "", fmt.Errorf("failed to load automation: %w", err)
	}
	return status, nil
}

// UpdateAutomation changes the draft only; it never changes status or the
// published version. Archived automations are read-only.
func (s *AutomationService) UpdateAutomation(ctx context.Context, orgID int64, automationUUID string, req *model.UpdateAutomationRequest) (*model.Automation, error) {
	if !validAutomationUUID(automationUUID) {
		return nil, ErrAutomationNotFound
	}
	var name, triggerType, reentry *string
	var triggerConfigJSON, workflowJSON []byte
	if req.Name != nil {
		n, err := cleanAutomationName(*req.Name)
		if err != nil {
			return nil, err
		}
		name = &n
	}
	// The trigger step defines the trigger, so trigger fields change only with the workflow.
	if req.Workflow != nil {
		requested := ""
		if req.TriggerType != nil {
			requested = *req.TriggerType
		}
		workflow, trigger, triggerConfig, err := PrepareAutomationDraft(req.Workflow, requested, req.TriggerConfig)
		if err != nil {
			return nil, err
		}
		if workflowJSON, err = json.Marshal(workflow); err != nil {
			return nil, fmt.Errorf("failed to serialize workflow: %w", err)
		}
		if triggerConfigJSON, err = json.Marshal(triggerConfig); err != nil {
			return nil, fmt.Errorf("failed to serialize trigger config: %w", err)
		}
		triggerType = &trigger
	} else if req.TriggerType != nil || req.TriggerConfig != nil {
		return nil, automationErr("send the workflow to change the trigger")
	}
	if req.ReentryPolicy != nil {
		if !ValidReentryPolicy(*req.ReentryPolicy) {
			return nil, automationErr("reentryPolicy must be never or after_exit")
		}
		reentry = req.ReentryPolicy
	}

	result, err := s.db.ExecContext(ctx, `
		UPDATE automations SET
			name = COALESCE($3, name),
			description = COALESCE($4, description),
			trigger_type = COALESCE($5, trigger_type),
			trigger_config = COALESCE($6::jsonb, trigger_config),
			workflow = COALESCE($7::jsonb, workflow),
			reentry_policy = COALESCE($8, reentry_policy),
			updated_at = now()
		WHERE uuid = $1 AND org_id = $2 AND status <> 'archived'`,
		automationUUID, orgID, name, req.Description, triggerType, nullableJSON(triggerConfigJSON), nullableJSON(workflowJSON), reentry)
	if err != nil {
		return nil, fmt.Errorf("failed to update automation: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		if _, err := s.automationStatus(ctx, orgID, automationUUID); err != nil {
			return nil, err
		}
		return nil, automationErr("Archived automations cannot be edited")
	}
	return s.GetAutomation(ctx, orgID, automationUUID)
}

func nullableJSON(b []byte) any {
	if b == nil {
		return nil
	}
	return string(b)
}

// DeleteAutomation removes a draft or archived automation with its versions
// and enrollments. Live (active or paused) automations must be archived first.
func (s *AutomationService) DeleteAutomation(ctx context.Context, orgID int64, automationUUID string) error {
	if !validAutomationUUID(automationUUID) {
		return ErrAutomationNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to delete automation: %w", err)
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRowContext(ctx, `DELETE FROM automations WHERE uuid = $1 AND org_id = $2 AND status IN ('draft','archived') RETURNING id`, automationUUID, orgID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := s.automationStatus(ctx, orgID, automationUUID); err != nil {
			return err
		}
		return automationErr("Archive the automation before deleting it")
	}
	if err != nil {
		return fmt.Errorf("failed to delete automation: %w", err)
	}
	// automation_logs has no foreign key on fresh installs.
	if _, err = tx.ExecContext(ctx, `DELETE FROM automation_logs WHERE automation_id = $1`, id); err != nil {
		return fmt.Errorf("failed to delete automation logs: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("failed to delete automation: %w", err)
	}
	return nil
}

// AutomationActivation is the result of activate/publish/resume.
type AutomationActivation struct {
	Automation *model.Automation `json:"automation"`
	Version    int               `json:"version"`
	Published  bool              `json:"published"` // a new version was created
}

type automationDraftRow struct {
	id             int64
	status         string
	workflow       *model.Workflow
	reentry        string
	versionID      sql.NullInt64
	version        sql.NullInt64
	hash           sql.NullString
	versionReentry sql.NullString
	publishedBy    sql.NullInt64
}

func loadAutomationDraft(ctx context.Context, q eventoutbox.DBTX, orgID int64, automationUUID string, lock bool) (*automationDraftRow, error) {
	query := `SELECT a.id, a.status, COALESCE(a.workflow,'{}'::jsonb), a.reentry_policy, a.published_version_id,
		v.version, v.graph_hash, v.reentry_policy, v.published_by
		FROM automations a LEFT JOIN automation_versions v ON v.id = a.published_version_id
		WHERE a.uuid = $1 AND a.org_id = $2`
	if lock {
		query += ` FOR UPDATE OF a`
	}
	var d automationDraftRow
	var workflow []byte
	err := q.QueryRowContext(ctx, query, automationUUID, orgID).Scan(&d.id, &d.status, &workflow, &d.reentry, &d.versionID,
		&d.version, &d.hash, &d.versionReentry, &d.publishedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAutomationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load automation: %w", err)
	}
	d.workflow = &model.Workflow{}
	if err := json.Unmarshal(workflow, d.workflow); err != nil {
		return nil, &AutomationInvalidError{Errors: []model.AutomationValidationError{errAt("", "workflow", "The saved workflow cannot be read")}}
	}
	return &d, nil
}

// compileDraft runs full validation including the database reference checks.
func compileDraft(ctx context.Context, q eventoutbox.DBTX, orgID, userID int64, d *automationDraftRow) (*CompiledGraph, []model.AutomationValidationError, error) {
	g, errs := CompileForPublish(d.workflow, "", nil)
	if len(errs) > 0 {
		return nil, errs, nil
	}
	errs, err := checkAutomationRefs(ctx, q, orgID, userID, g)
	if err != nil || len(errs) > 0 {
		return nil, errs, err
	}
	return g, nil, nil
}

// ValidateAutomation runs the publish checks for userID without writing.
func (s *AutomationService) ValidateAutomation(ctx context.Context, orgID, userID int64, automationUUID string) ([]model.AutomationValidationError, error) {
	if !validAutomationUUID(automationUUID) {
		return nil, ErrAutomationNotFound
	}
	d, err := loadAutomationDraft(ctx, s.db, orgID, automationUUID, false)
	if err != nil {
		var invalid *AutomationInvalidError
		if errors.As(err, &invalid) {
			return invalid.Errors, nil
		}
		return nil, err
	}
	_, errs, err := compileDraft(ctx, s.db, orgID, userID, d)
	if err != nil {
		return nil, err
	}
	if errs == nil {
		errs = []model.AutomationValidationError{}
	}
	return errs, nil
}

// ActivateAutomation publishes the draft (when it changed) and makes the
// automation active, in one transaction. With publishDraft=false and an
// existing version, it resumes that version without validating the draft.
func (s *AutomationService) ActivateAutomation(ctx context.Context, orgID, userID int64, automationUUID string, publishDraft bool) (*AutomationActivation, error) {
	if !validAutomationUUID(automationUUID) {
		return nil, ErrAutomationNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to activate automation: %w", err)
	}
	defer tx.Rollback()

	d, err := loadAutomationDraft(ctx, tx, orgID, automationUUID, true)
	if err != nil {
		return nil, err
	}
	if d.status == "archived" {
		return nil, automationErr("Archived automations cannot be activated")
	}
	out := &AutomationActivation{}
	versionID := d.versionID
	if !publishDraft && d.versionID.Valid {
		out.Version = int(d.version.Int64)
	} else {
		g, errs, err := compileDraft(ctx, tx, orgID, userID, d)
		if err != nil {
			return nil, err
		}
		if len(errs) > 0 {
			return nil, &AutomationInvalidError{Errors: errs}
		}
		unchanged := d.versionID.Valid && d.hash.String == g.Hash && d.versionReentry.String == d.reentry &&
			d.publishedBy.Valid && d.publishedBy.Int64 == userID
		if unchanged {
			out.Version = int(d.version.Int64)
		} else {
			trigger := g.Trigger()
			triggerCfg := map[string]any{"event": trigger.Event}
			if trigger.ListUUID != "" {
				triggerCfg["listUuid"] = trigger.ListUUID
			}
			if trigger.Event != AutomationTriggerManual {
				triggerCfg["sources"] = trigger.Sources
			}
			cfgJSON, _ := json.Marshal(triggerCfg)
			norm, _, _, err := NormalizeWorkflow(d.workflow, "", nil)
			if err != nil {
				return nil, &AutomationInvalidError{Errors: []model.AutomationValidationError{errAt("", "triggerType", "%s", err.Error())}}
			}
			workflowJSON, err := json.Marshal(norm)
			if err != nil {
				return nil, fmt.Errorf("failed to serialize workflow: %w", err)
			}
			var publishedBy any
			if userID > 0 {
				publishedBy = userID
			}
			err = tx.QueryRowContext(ctx, `
				INSERT INTO automation_versions (automation_id, version, trigger_type, trigger_config, workflow, graph_hash, reentry_policy, resource_refs, published_by)
				SELECT $1, COALESCE(max(version),0)+1, $2, $3, $4, $5, $6, $7, $8 FROM automation_versions WHERE automation_id = $1
				RETURNING id, version`,
				d.id, trigger.Event, cfgJSON, workflowJSON, g.Hash, d.reentry, pq.Array(g.Refs), publishedBy,
			).Scan(&versionID, &out.Version)
			if err != nil {
				return nil, fmt.Errorf("failed to publish automation: %w", err)
			}
			out.Published = true
		}
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE automations SET status = 'active', published_version_id = $2, activated_at = COALESCE(activated_at, now()), updated_at = now()
		WHERE id = $1`, d.id, versionID); err != nil {
		return nil, fmt.Errorf("failed to activate automation: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to activate automation: %w", err)
	}
	if out.Automation, err = s.GetAutomation(ctx, orgID, automationUUID); err != nil {
		return nil, err
	}
	return out, nil
}

// checkAutomationRefs verifies every referenced resource exists in the org and
// is usable by userID: static lists, active templates, the user's own can_send
// identity on an active, SES-verified domain with feedback set up (the M3
// campaign sender rule), and the user's own active webhooks.
func checkAutomationRefs(ctx context.Context, q eventoutbox.DBTX, orgID, userID int64, g *CompiledGraph) ([]model.AutomationValidationError, error) {
	ids := make([]string, 0, len(g.Nodes))
	for id := range g.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var errs []model.AutomationValidationError
	add := func(nodeID, field, msg string) { errs = append(errs, errAt(nodeID, field, "%s", msg)) }
	staticList := func(nodeID, listUUID string) error {
		var listType string
		err := q.QueryRowContext(ctx, `SELECT type FROM lists WHERE uuid = $1 AND org_id = $2`, listUUID, orgID).Scan(&listType)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			add(nodeID, "listUuid", "Choose a list in this workspace")
		case err != nil:
			return err
		case listType != "static":
			add(nodeID, "listUuid", "Choose a static list")
		}
		return nil
	}
	for _, id := range ids {
		n := g.Nodes[id]
		var err error
		switch n.Kind {
		case KindTrigger:
			if n.Trigger.ListUUID != "" {
				err = staticList(id, n.Trigger.ListUUID)
			}
		case KindAction:
			if n.Action.ListUUID != "" {
				err = staticList(id, n.Action.ListUUID)
			}
		case KindEmail:
			var active bool
			err = q.QueryRowContext(ctx, `SELECT is_active FROM email_templates WHERE uuid = $1 AND org_id = $2`, n.Email.TemplateUUID, orgID).Scan(&active)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				add(id, "templateUuid", "Choose a template in this workspace")
				err = nil
			case err == nil && !active:
				add(id, "templateUuid", "The template is inactive")
			}
			if err != nil {
				break
			}
			err = checkAutomationIdentity(ctx, q, orgID, userID, id, n.Email.IdentityUUID, add)
		case KindWebhook:
			var active bool
			var owner sql.NullInt64
			err = q.QueryRowContext(ctx, `SELECT active, user_id FROM webhooks WHERE uuid = $1 AND org_id = $2`, n.Webhook.WebhookUUID, orgID).Scan(&active, &owner)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				add(id, "webhookUuid", "Choose a webhook endpoint in this workspace")
				err = nil
			case err != nil:
			case !owner.Valid || owner.Int64 != userID:
				add(id, "webhookUuid", "Choose one of your own webhook endpoints")
			case !active:
				add(id, "webhookUuid", "The webhook endpoint is inactive")
			}
		}
		if err != nil {
			return nil, fmt.Errorf("failed to check automation references: %w", err)
		}
	}
	return errs, nil
}

func checkAutomationIdentity(ctx context.Context, q eventoutbox.DBTX, orgID, userID int64, nodeID, identityUUID string, add func(nodeID, field, msg string)) error {
	var owner int64
	var canSend, domainReady, feedbackReady, userActive bool
	err := q.QueryRowContext(ctx, `
		SELECT i.user_id, i.can_send, d.status = 'active' AND d.ses_verified, COALESCE(d.sending_feedback_ready,false), u.status = 'active'
		FROM identities i JOIN domains d ON d.id = i.domain_id JOIN users u ON u.id = i.user_id
		WHERE i.uuid = $1 AND d.org_id = $2 AND u.org_id = $2`, identityUUID, orgID).Scan(&owner, &canSend, &domainReady, &feedbackReady, &userActive)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		add(nodeID, "identityUuid", "Choose a sending identity in this workspace")
	case err != nil:
		return err
	case owner != userID || !userActive:
		add(nodeID, "identityUuid", "Choose one of your own sending identities")
	case !canSend:
		add(nodeID, "identityUuid", "This identity is not allowed to send")
	case !domainReady:
		add(nodeID, "identityUuid", "The sending domain must be active and verified with SES")
	case !feedbackReady:
		add(nodeID, "identityUuid", "Finish sending setup (bounce/complaint feedback) for this domain")
	}
	return nil
}

// PauseAutomation stops new enrollments and freezes running ones.
func (s *AutomationService) PauseAutomation(ctx context.Context, orgID int64, automationUUID string) (*model.Automation, error) {
	if !validAutomationUUID(automationUUID) {
		return nil, ErrAutomationNotFound
	}
	result, err := s.db.ExecContext(ctx, `UPDATE automations SET status = 'paused', updated_at = now() WHERE uuid = $1 AND org_id = $2 AND status = 'active'`, automationUUID, orgID)
	if err != nil {
		return nil, fmt.Errorf("failed to pause automation: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		if _, err := s.automationStatus(ctx, orgID, automationUUID); err != nil {
			return nil, err
		}
		return nil, automationErr("Only active automations can be paused")
	}
	return s.GetAutomation(ctx, orgID, automationUUID)
}

// ArchiveAutomation ends an automation for good: active enrollments are
// cancelled and queued emails that have not been claimed are not sent.
func (s *AutomationService) ArchiveAutomation(ctx context.Context, orgID int64, automationUUID string) (*model.Automation, int, error) {
	if !validAutomationUUID(automationUUID) {
		return nil, 0, ErrAutomationNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to archive automation: %w", err)
	}
	defer tx.Rollback()
	var id int64
	var status string
	err = tx.QueryRowContext(ctx, `SELECT id, status FROM automations WHERE uuid = $1 AND org_id = $2 FOR UPDATE`, automationUUID, orgID).Scan(&id, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, ErrAutomationNotFound
	}
	if err != nil {
		return nil, 0, fmt.Errorf("failed to archive automation: %w", err)
	}
	if status == "archived" {
		return nil, 0, automationErr("The automation is already archived")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE automations SET status = 'archived', archived_at = now(), updated_at = now() WHERE id = $1`, id); err != nil {
		return nil, 0, fmt.Errorf("failed to archive automation: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE automation_enrollments SET status = 'cancelled', exit_reason = 'archived', locked_until = NULL, claim_token = NULL, updated_at = now()
		WHERE automation_id = $1 AND status = 'active'`, id)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to cancel enrollments: %w", err)
	}
	cancelled, _ := result.RowsAffected()
	if err = cancelPendingAutomationMessages(ctx, tx, orgID, "automation_id", id); err != nil {
		return nil, 0, err
	}
	if err = tx.Commit(); err != nil {
		return nil, 0, fmt.Errorf("failed to archive automation: %w", err)
	}
	a, err := s.GetAutomation(ctx, orgID, automationUUID)
	return a, int(cancelled), err
}

// GetAutomationStats returns live enrollment counts and per-node step counts
// for one version (default: the published version).
func (s *AutomationService) GetAutomationStats(ctx context.Context, orgID int64, automationUUID string, version *int) (*model.AutomationStats, error) {
	a, err := s.GetAutomation(ctx, orgID, automationUUID)
	if err != nil {
		return nil, err
	}
	stats := &model.AutomationStats{AutomationUUID: a.UUID, AutomationCounts: a.Stats, InProgress: a.Stats.Active, Errors: a.Stats.Failed,
		Nodes: map[string]model.AutomationNodeStats{}}
	if a.Stats.Enrolled > 0 {
		stats.CompletionRate = float64(a.Stats.Completed) / float64(a.Stats.Enrolled) * 100
	}
	if version == nil {
		version = a.PublishedVersion
	}
	if version == nil {
		return stats, nil
	}
	var versionID int64
	err = s.db.QueryRowContext(ctx, `SELECT id FROM automation_versions WHERE automation_id = $1 AND version = $2`, a.ID, *version).Scan(&versionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, automationErr("version %d does not exist", *version)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load version: %w", err)
	}
	stats.Version = *version
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.node_id, r.status, COALESCE(r.outcome,''), count(*),
		       count(*) FILTER (WHERE m.status = 'sent'),
		       count(*) FILTER (WHERE m.open_count > 0),
		       count(*) FILTER (WHERE m.click_count > 0)
		FROM automation_step_runs r LEFT JOIN automation_messages m ON m.id = r.message_id
		WHERE r.automation_id = $1 AND r.version_id = $2
		GROUP BY 1, 2, 3`, a.ID, versionID)
	if err != nil {
		return nil, fmt.Errorf("failed to load node stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var nodeID, status, outcome string
		var n, sent, opened, clicked int
		if err := rows.Scan(&nodeID, &status, &outcome, &n, &sent, &opened, &clicked); err != nil {
			return nil, fmt.Errorf("failed to scan node stats: %w", err)
		}
		ns := stats.Nodes[nodeID]
		ns.Entered += n
		switch status {
		case "waiting":
			ns.Waiting += n
		case "succeeded":
			ns.Succeeded += n
		case "skipped":
			ns.Skipped += n
		case "failed":
			ns.Failed += n
		}
		switch outcome {
		case "yes":
			ns.Yes += n
		case "no":
			ns.No += n
		}
		ns.Sent += sent
		ns.Opened += opened
		ns.Clicked += clicked
		stats.Nodes[nodeID] = ns
	}
	return stats, rows.Err()
}
