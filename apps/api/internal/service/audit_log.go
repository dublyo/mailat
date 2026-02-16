package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
)

// AuditAction defines common audit actions
const (
	AuditActionLogin            = "login"
	AuditActionLogout           = "logout"
	AuditActionLoginFailed      = "login_failed"
	AuditActionPasswordChange   = "password_change"
	AuditAction2FAEnable        = "2fa_enable"
	AuditAction2FADisable       = "2fa_disable"
	AuditAction2FAVerify        = "2fa_verify"
	AuditActionAPIKeyCreate     = "api_key_create"
	AuditActionAPIKeyDelete     = "api_key_delete"
	AuditActionDomainCreate     = "domain_create"
	AuditActionDomainVerify     = "domain_verify"
	AuditActionDomainDelete     = "domain_delete"
	AuditActionIdentityCreate   = "identity_create"
	AuditActionIdentityDelete   = "identity_delete"
	AuditActionCampaignSend     = "campaign_send"
	AuditActionCampaignSchedule = "campaign_schedule"
	AuditActionWebhookCreate    = "webhook_create"
	AuditActionWebhookDelete    = "webhook_delete"
	AuditActionRuleCreate       = "rule_create"
	AuditActionRuleUpdate       = "rule_update"
	AuditActionRuleDelete       = "rule_delete"
	AuditActionAutoReplyCreate  = "auto_reply_create"
	AuditActionAutoReplyUpdate  = "auto_reply_update"
	AuditActionAutoReplyDelete  = "auto_reply_delete"
)

// AuditLog represents an audit log entry
type AuditLog struct {
	ID          int64              `json:"id"`
	OrgID       int                `json:"orgId"`
	UserID      *int               `json:"userId,omitempty"`
	Action      string             `json:"action"`
	Resource    string             `json:"resource"`
	ResourceID  string             `json:"resourceId,omitempty"`
	Description string             `json:"description,omitempty"`
	IPAddress   string             `json:"ipAddress,omitempty"`
	UserAgent   string             `json:"userAgent,omitempty"`
	RequestID   string             `json:"requestId,omitempty"`
	OldValues   map[string]any     `json:"oldValues,omitempty"`
	NewValues   map[string]any     `json:"newValues,omitempty"`
	Status      string             `json:"status"`
	CreatedAt   time.Time          `json:"createdAt"`
}

// AuditLogInput is the input for creating an audit log entry
type AuditLogInput struct {
	OrgID       int64
	UserID      *int64
	Action      string
	Resource    string
	ResourceID  string
	Description string
	IPAddress   string
	UserAgent   string
	RequestID   string
	OldValues   map[string]any
	NewValues   map[string]any
	Status      string
}

// AuditLogFilter is used for filtering audit logs
type AuditLogFilter struct {
	UserID     *int64
	Action     string
	Resource   string
	ResourceID string
	Status     string
	StartDate  *time.Time
	EndDate    *time.Time
	Limit      int
	Offset     int
}

// AuditLogService handles audit log operations
type AuditLogService struct {
	db  *sql.DB
	cfg *config.Config
}

// NewAuditLogService creates a new audit log service
func NewAuditLogService(db *sql.DB, cfg *config.Config) *AuditLogService {
	return &AuditLogService{db: db, cfg: cfg}
}

// Log creates a new audit log entry
func (s *AuditLogService) Log(ctx context.Context, input *AuditLogInput) error {
	var oldValuesJSON, newValuesJSON []byte
	var err error

	if input.OldValues != nil {
		oldValuesJSON, err = json.Marshal(input.OldValues)
		if err != nil {
			return fmt.Errorf("failed to marshal old values: %w", err)
		}
	}

	if input.NewValues != nil {
		newValuesJSON, err = json.Marshal(input.NewValues)
		if err != nil {
			return fmt.Errorf("failed to marshal new values: %w", err)
		}
	}

	if input.Status == "" {
		input.Status = "success"
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO audit_logs (org_id, user_id, action, resource, resource_id, description, ip_address, user_agent, request_id, old_values, new_values, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, input.OrgID, input.UserID, input.Action, input.Resource, input.ResourceID, input.Description,
		input.IPAddress, input.UserAgent, input.RequestID, oldValuesJSON, newValuesJSON, input.Status)
	if err != nil {
		return fmt.Errorf("failed to create audit log: %w", err)
	}

	return nil
}

// LogAsync creates an audit log entry asynchronously (fire and forget)
func (s *AuditLogService) LogAsync(input *AuditLogInput) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.Log(ctx, input)
	}()
}

// List retrieves audit logs with optional filtering
func (s *AuditLogService) List(ctx context.Context, orgID int64, filter *AuditLogFilter) ([]*AuditLog, int, error) {
	// Build query
	query := `
		SELECT id, org_id, user_id, action, resource, COALESCE(resource_id, ''), COALESCE(description, ''),
		       COALESCE(ip_address, ''), COALESCE(user_agent, ''), COALESCE(request_id, ''),
		       old_values, new_values, status, created_at
		FROM audit_logs
		WHERE org_id = $1
	`
	countQuery := `SELECT COUNT(*) FROM audit_logs WHERE org_id = $1`
	args := []interface{}{orgID}
	countArgs := []interface{}{orgID}
	argNum := 2

	if filter.UserID != nil {
		query += fmt.Sprintf(" AND user_id = $%d", argNum)
		countQuery += fmt.Sprintf(" AND user_id = $%d", argNum)
		args = append(args, *filter.UserID)
		countArgs = append(countArgs, *filter.UserID)
		argNum++
	}

	if filter.Action != "" {
		query += fmt.Sprintf(" AND action = $%d", argNum)
		countQuery += fmt.Sprintf(" AND action = $%d", argNum)
		args = append(args, filter.Action)
		countArgs = append(countArgs, filter.Action)
		argNum++
	}

	if filter.Resource != "" {
		query += fmt.Sprintf(" AND resource = $%d", argNum)
		countQuery += fmt.Sprintf(" AND resource = $%d", argNum)
		args = append(args, filter.Resource)
		countArgs = append(countArgs, filter.Resource)
		argNum++
	}

	if filter.ResourceID != "" {
		query += fmt.Sprintf(" AND resource_id = $%d", argNum)
		countQuery += fmt.Sprintf(" AND resource_id = $%d", argNum)
		args = append(args, filter.ResourceID)
		countArgs = append(countArgs, filter.ResourceID)
		argNum++
	}

	if filter.Status != "" {
		query += fmt.Sprintf(" AND status = $%d", argNum)
		countQuery += fmt.Sprintf(" AND status = $%d", argNum)
		args = append(args, filter.Status)
		countArgs = append(countArgs, filter.Status)
		argNum++
	}

	if filter.StartDate != nil {
		query += fmt.Sprintf(" AND created_at >= $%d", argNum)
		countQuery += fmt.Sprintf(" AND created_at >= $%d", argNum)
		args = append(args, *filter.StartDate)
		countArgs = append(countArgs, *filter.StartDate)
		argNum++
	}

	if filter.EndDate != nil {
		query += fmt.Sprintf(" AND created_at <= $%d", argNum)
		countQuery += fmt.Sprintf(" AND created_at <= $%d", argNum)
		args = append(args, *filter.EndDate)
		countArgs = append(countArgs, *filter.EndDate)
		argNum++
	}

	// Get total count
	var total int
	err := s.db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count audit logs: %w", err)
	}

	// Add pagination and ordering
	query += " ORDER BY created_at DESC"

	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT $%d", argNum)
		args = append(args, filter.Limit)
		argNum++
	} else {
		query += " LIMIT 100" // Default limit
	}

	if filter.Offset > 0 {
		query += fmt.Sprintf(" OFFSET $%d", argNum)
		args = append(args, filter.Offset)
	}

	// Execute query
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to query audit logs: %w", err)
	}
	defer rows.Close()

	var logs []*AuditLog
	for rows.Next() {
		var log AuditLog
		var userID sql.NullInt64
		var oldValuesJSON, newValuesJSON []byte

		err := rows.Scan(&log.ID, &log.OrgID, &userID, &log.Action, &log.Resource, &log.ResourceID,
			&log.Description, &log.IPAddress, &log.UserAgent, &log.RequestID,
			&oldValuesJSON, &newValuesJSON, &log.Status, &log.CreatedAt)
		if err != nil {
			continue
		}

		if userID.Valid {
			uid := int(userID.Int64)
			log.UserID = &uid
		}

		if oldValuesJSON != nil {
			json.Unmarshal(oldValuesJSON, &log.OldValues)
		}
		if newValuesJSON != nil {
			json.Unmarshal(newValuesJSON, &log.NewValues)
		}

		logs = append(logs, &log)
	}

	return logs, total, nil
}

// GetUserActivity returns recent activity for a specific user
func (s *AuditLogService) GetUserActivity(ctx context.Context, orgID, userID int64, limit int) ([]*AuditLog, error) {
	if limit <= 0 {
		limit = 50
	}

	filter := &AuditLogFilter{
		UserID: &userID,
		Limit:  limit,
	}

	logs, _, err := s.List(ctx, orgID, filter)
	return logs, err
}

// GetSecurityEvents returns security-related events
func (s *AuditLogService) GetSecurityEvents(ctx context.Context, orgID int64, limit int) ([]*AuditLog, error) {
	if limit <= 0 {
		limit = 100
	}

	query := `
		SELECT id, org_id, user_id, action, resource, COALESCE(resource_id, ''), COALESCE(description, ''),
		       COALESCE(ip_address, ''), COALESCE(user_agent, ''), COALESCE(request_id, ''),
		       old_values, new_values, status, created_at
		FROM audit_logs
		WHERE org_id = $1
		  AND action IN ('login', 'login_failed', 'logout', 'password_change', '2fa_enable', '2fa_disable', '2fa_verify', 'api_key_create', 'api_key_delete')
		ORDER BY created_at DESC
		LIMIT $2
	`

	rows, err := s.db.QueryContext(ctx, query, orgID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query security events: %w", err)
	}
	defer rows.Close()

	var logs []*AuditLog
	for rows.Next() {
		var log AuditLog
		var userID sql.NullInt64
		var oldValuesJSON, newValuesJSON []byte

		err := rows.Scan(&log.ID, &log.OrgID, &userID, &log.Action, &log.Resource, &log.ResourceID,
			&log.Description, &log.IPAddress, &log.UserAgent, &log.RequestID,
			&oldValuesJSON, &newValuesJSON, &log.Status, &log.CreatedAt)
		if err != nil {
			continue
		}

		if userID.Valid {
			uid := int(userID.Int64)
			log.UserID = &uid
		}

		if oldValuesJSON != nil {
			json.Unmarshal(oldValuesJSON, &log.OldValues)
		}
		if newValuesJSON != nil {
			json.Unmarshal(newValuesJSON, &log.NewValues)
		}

		logs = append(logs, &log)
	}

	return logs, nil
}
