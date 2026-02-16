package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
)

type AutomationService struct {
	db  *sql.DB
	cfg *config.Config
}

func NewAutomationService(db *sql.DB, cfg *config.Config) *AutomationService {
	return &AutomationService{db: db, cfg: cfg}
}

// CreateAutomation creates a new automation
func (s *AutomationService) CreateAutomation(ctx context.Context, orgID int64, req *model.CreateAutomationRequest) (*model.Automation, error) {
	automationUUID := uuid.New().String()
	now := time.Now()

	// Convert workflow to JSON
	workflowJSON, err := json.Marshal(req.Workflow)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize workflow: %w", err)
	}

	query := `
		INSERT INTO automations (uuid, org_id, name, description, trigger_type, trigger_config, workflow, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'draft', $8, $8)
		RETURNING id, uuid, org_id, name, description, trigger_type, trigger_config, workflow, status, created_at, updated_at
	`

	triggerConfigJSON, _ := json.Marshal(req.TriggerConfig)

	var automation model.Automation
	var workflowBytes []byte
	var triggerConfigBytes []byte
	err = s.db.QueryRowContext(ctx, query,
		automationUUID, orgID, req.Name, req.Description, req.TriggerType, triggerConfigJSON, workflowJSON, now,
	).Scan(
		&automation.ID, &automation.UUID, &automation.OrgID, &automation.Name, &automation.Description,
		&automation.TriggerType, &triggerConfigBytes, &workflowBytes, &automation.Status,
		&automation.CreatedAt, &automation.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create automation: %w", err)
	}

	json.Unmarshal(workflowBytes, &automation.Workflow)
	json.Unmarshal(triggerConfigBytes, &automation.TriggerConfig)

	return &automation, nil
}

// GetAutomation retrieves an automation by UUID
func (s *AutomationService) GetAutomation(ctx context.Context, orgID int64, automationUUID string) (*model.Automation, error) {
	query := `
		SELECT id, uuid, org_id, name, description, trigger_type, trigger_config, workflow, status,
		       enrolled_count, completed_count, in_progress_count, created_at, updated_at
		FROM automations
		WHERE uuid = $1 AND org_id = $2
	`

	var automation model.Automation
	var workflowBytes []byte
	var triggerConfigBytes []byte
	err := s.db.QueryRowContext(ctx, query, automationUUID, orgID).Scan(
		&automation.ID, &automation.UUID, &automation.OrgID, &automation.Name, &automation.Description,
		&automation.TriggerType, &triggerConfigBytes, &workflowBytes, &automation.Status,
		&automation.EnrolledCount, &automation.CompletedCount, &automation.InProgressCount,
		&automation.CreatedAt, &automation.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("automation not found")
		}
		return nil, fmt.Errorf("failed to get automation: %w", err)
	}

	json.Unmarshal(workflowBytes, &automation.Workflow)
	json.Unmarshal(triggerConfigBytes, &automation.TriggerConfig)

	return &automation, nil
}

// ListAutomations retrieves automations with pagination
func (s *AutomationService) ListAutomations(ctx context.Context, orgID int64, page, pageSize int, status string) (*model.AutomationListResult, error) {
	offset := (page - 1) * pageSize

	// Count query
	countQuery := `SELECT COUNT(*) FROM automations WHERE org_id = $1`
	args := []interface{}{orgID}
	if status != "" {
		countQuery += " AND status = $2"
		args = append(args, status)
	}

	var total int
	err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total)
	if err != nil {
		return nil, fmt.Errorf("failed to count automations: %w", err)
	}

	// Main query
	query := `
		SELECT id, uuid, org_id, name, description, trigger_type, trigger_config, status,
		       enrolled_count, completed_count, in_progress_count, created_at, updated_at
		FROM automations
		WHERE org_id = $1
	`
	args = []interface{}{orgID}
	if status != "" {
		query += " AND status = $2"
		args = append(args, status)
	}
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT %d OFFSET %d", pageSize, offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list automations: %w", err)
	}
	defer rows.Close()

	var automations []model.AutomationSummary
	for rows.Next() {
		var a model.AutomationSummary
		var triggerConfigBytes []byte
		err := rows.Scan(
			&a.ID, &a.UUID, &a.OrgID, &a.Name, &a.Description, &a.TriggerType, &triggerConfigBytes,
			&a.Status, &a.EnrolledCount, &a.CompletedCount, &a.InProgressCount,
			&a.CreatedAt, &a.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan automation: %w", err)
		}
		json.Unmarshal(triggerConfigBytes, &a.TriggerConfig)
		automations = append(automations, a)
	}

	return &model.AutomationListResult{
		Automations: automations,
		Total:       total,
		Page:        page,
		PageSize:    pageSize,
	}, nil
}

// UpdateAutomation updates an automation
func (s *AutomationService) UpdateAutomation(ctx context.Context, orgID int64, automationUUID string, req *model.UpdateAutomationRequest) (*model.Automation, error) {
	now := time.Now()

	// Build update query dynamically
	updates := []string{}
	args := []interface{}{}
	argIndex := 1

	if req.Name != nil {
		updates = append(updates, fmt.Sprintf("name = $%d", argIndex))
		args = append(args, *req.Name)
		argIndex++
	}
	if req.Description != nil {
		updates = append(updates, fmt.Sprintf("description = $%d", argIndex))
		args = append(args, *req.Description)
		argIndex++
	}
	if req.TriggerType != nil {
		updates = append(updates, fmt.Sprintf("trigger_type = $%d", argIndex))
		args = append(args, *req.TriggerType)
		argIndex++
	}
	if req.TriggerConfig != nil {
		configJSON, _ := json.Marshal(req.TriggerConfig)
		updates = append(updates, fmt.Sprintf("trigger_config = $%d", argIndex))
		args = append(args, configJSON)
		argIndex++
	}
	if req.Workflow != nil {
		workflowJSON, _ := json.Marshal(req.Workflow)
		updates = append(updates, fmt.Sprintf("workflow = $%d", argIndex))
		args = append(args, workflowJSON)
		argIndex++
	}

	if len(updates) == 0 {
		return s.GetAutomation(ctx, orgID, automationUUID)
	}

	updates = append(updates, fmt.Sprintf("updated_at = $%d", argIndex))
	args = append(args, now)
	argIndex++

	args = append(args, automationUUID, orgID)

	query := fmt.Sprintf(
		"UPDATE automations SET %s WHERE uuid = $%d AND org_id = $%d",
		joinStrings(updates, ", "), argIndex, argIndex+1,
	)

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to update automation: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return nil, fmt.Errorf("automation not found")
	}

	return s.GetAutomation(ctx, orgID, automationUUID)
}

// DeleteAutomation deletes an automation
func (s *AutomationService) DeleteAutomation(ctx context.Context, orgID int64, automationUUID string) error {
	query := `DELETE FROM automations WHERE uuid = $1 AND org_id = $2`
	result, err := s.db.ExecContext(ctx, query, automationUUID, orgID)
	if err != nil {
		return fmt.Errorf("failed to delete automation: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("automation not found")
	}

	return nil
}

// ActivateAutomation activates an automation
func (s *AutomationService) ActivateAutomation(ctx context.Context, orgID int64, automationUUID string) (*model.Automation, error) {
	query := `UPDATE automations SET status = 'active', updated_at = $1 WHERE uuid = $2 AND org_id = $3`
	result, err := s.db.ExecContext(ctx, query, time.Now(), automationUUID, orgID)
	if err != nil {
		return nil, fmt.Errorf("failed to activate automation: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return nil, fmt.Errorf("automation not found")
	}

	return s.GetAutomation(ctx, orgID, automationUUID)
}

// PauseAutomation pauses an automation
func (s *AutomationService) PauseAutomation(ctx context.Context, orgID int64, automationUUID string) (*model.Automation, error) {
	query := `UPDATE automations SET status = 'paused', updated_at = $1 WHERE uuid = $2 AND org_id = $3`
	result, err := s.db.ExecContext(ctx, query, time.Now(), automationUUID, orgID)
	if err != nil {
		return nil, fmt.Errorf("failed to pause automation: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return nil, fmt.Errorf("automation not found")
	}

	return s.GetAutomation(ctx, orgID, automationUUID)
}

// GetAutomationStats retrieves automation statistics
func (s *AutomationService) GetAutomationStats(ctx context.Context, orgID int64, automationUUID string) (*model.AutomationStats, error) {
	query := `
		SELECT enrolled_count, completed_count, in_progress_count,
		       COALESCE(error_count, 0), created_at
		FROM automations
		WHERE uuid = $1 AND org_id = $2
	`

	var stats model.AutomationStats
	var createdAt time.Time
	err := s.db.QueryRowContext(ctx, query, automationUUID, orgID).Scan(
		&stats.Enrolled, &stats.Completed, &stats.InProgress, &stats.Errors, &createdAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("automation not found")
		}
		return nil, fmt.Errorf("failed to get automation stats: %w", err)
	}

	stats.AutomationUUID = automationUUID

	// Calculate completion rate
	if stats.Enrolled > 0 {
		stats.CompletionRate = float64(stats.Completed) / float64(stats.Enrolled) * 100
	}

	return &stats, nil
}

// EnrollContact enrolls a contact in an automation
func (s *AutomationService) EnrollContact(ctx context.Context, orgID int64, automationUUID string, contactUUID string) error {
	now := time.Now()
	enrollmentUUID := uuid.New().String()

	// Insert enrollment
	query := `
		INSERT INTO automation_enrollments (uuid, automation_id, contact_id, org_id, status, step_index, enrolled_at, updated_at)
		SELECT $1, a.id, c.id, $2, 'active', 0, $3, $3
		FROM automations a, contacts c
		WHERE a.uuid = $4 AND a.org_id = $2 AND c.uuid = $5 AND c.org_id = $2
	`
	_, err := s.db.ExecContext(ctx, query, enrollmentUUID, orgID, now, automationUUID, contactUUID)
	if err != nil {
		return fmt.Errorf("failed to enroll contact: %w", err)
	}

	// Increment enrolled count
	updateQuery := `UPDATE automations SET enrolled_count = enrolled_count + 1 WHERE uuid = $1 AND org_id = $2`
	_, err = s.db.ExecContext(ctx, updateQuery, automationUUID, orgID)
	if err != nil {
		return fmt.Errorf("failed to update enrollment count: %w", err)
	}

	return nil
}

// Helper function to join strings
func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}
