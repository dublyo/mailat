package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
)

// RuleCondition defines a single condition for matching emails
type RuleCondition struct {
	Field         string `json:"field"`          // from, to, subject, body, header
	Operator      string `json:"operator"`       // contains, equals, matches, starts_with, ends_with, not_contains, not_equals
	Value         string `json:"value"`
	CaseSensitive bool   `json:"caseSensitive"`
	HeaderName    string `json:"headerName,omitempty"` // Only for field="header"
}

// RuleAction defines an action to take when a rule matches
type RuleAction struct {
	Type  string `json:"type"`            // move_to_folder, add_label, mark_read, mark_starred, forward, delete, auto_reply, stop_processing
	Value string `json:"value,omitempty"` // Folder name, label name, forward address, etc.
}

// EmailRule represents an email filter rule
type EmailRule struct {
	ID             int             `json:"id"`
	UUID           string          `json:"uuid"`
	UserID         int             `json:"userId"`
	OrgID          int             `json:"orgId"`
	Name           string          `json:"name"`
	Description    string          `json:"description,omitempty"`
	Priority       int             `json:"priority"`
	Conditions     []RuleCondition `json:"conditions"`
	ConditionLogic string          `json:"conditionLogic"` // "all" or "any"
	Actions        []RuleAction    `json:"actions"`
	IdentityIDs    []int           `json:"identityIds,omitempty"`
	Active         bool            `json:"active"`
	MatchCount     int             `json:"matchCount"`
	LastMatchedAt  *time.Time      `json:"lastMatchedAt,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
}

// CreateEmailRuleInput is the input for creating an email rule
type CreateEmailRuleInput struct {
	Name           string          `json:"name"`
	Description    string          `json:"description,omitempty"`
	Priority       int             `json:"priority"`
	Conditions     []RuleCondition `json:"conditions"`
	ConditionLogic string          `json:"conditionLogic"`
	Actions        []RuleAction    `json:"actions"`
	IdentityIDs    []int           `json:"identityIds,omitempty"`
	Active         bool            `json:"active"`
}

// UpdateEmailRuleInput is the input for updating an email rule
type UpdateEmailRuleInput struct {
	Name           *string          `json:"name,omitempty"`
	Description    *string          `json:"description,omitempty"`
	Priority       *int             `json:"priority,omitempty"`
	Conditions     *[]RuleCondition `json:"conditions,omitempty"`
	ConditionLogic *string          `json:"conditionLogic,omitempty"`
	Actions        *[]RuleAction    `json:"actions,omitempty"`
	IdentityIDs    *[]int           `json:"identityIds,omitempty"`
	Active         *bool            `json:"active,omitempty"`
}

// EmailForTestInput is the input for testing a rule against an email
type EmailForTestInput struct {
	From    string            `json:"from"`
	To      []string          `json:"to"`
	Subject string            `json:"subject"`
	Body    string            `json:"body"`
	Headers map[string]string `json:"headers,omitempty"`
}

// EmailRulesService handles email rules operations
type EmailRulesService struct {
	db  *sql.DB
	cfg *config.Config
}

// NewEmailRulesService creates a new email rules service
func NewEmailRulesService(db *sql.DB, cfg *config.Config) *EmailRulesService {
	return &EmailRulesService{db: db, cfg: cfg}
}

// CreateRule creates a new email rule
func (s *EmailRulesService) CreateRule(ctx context.Context, userID, orgID int64, input *CreateEmailRuleInput) (*EmailRule, error) {
	// Validate conditions
	if len(input.Conditions) == 0 {
		return nil, fmt.Errorf("at least one condition is required")
	}

	// Validate actions
	if len(input.Actions) == 0 {
		return nil, fmt.Errorf("at least one action is required")
	}

	// Set defaults
	if input.ConditionLogic == "" {
		input.ConditionLogic = "all"
	}

	conditionsJSON, err := json.Marshal(input.Conditions)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal conditions: %w", err)
	}

	actionsJSON, err := json.Marshal(input.Actions)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal actions: %w", err)
	}

	// Convert identity IDs to PostgreSQL array format
	identityIDsArray := "{}"
	if len(input.IdentityIDs) > 0 {
		idStrs := make([]string, len(input.IdentityIDs))
		for i, id := range input.IdentityIDs {
			idStrs[i] = fmt.Sprintf("%d", id)
		}
		identityIDsArray = "{" + strings.Join(idStrs, ",") + "}"
	}

	var rule EmailRule
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO email_rules (user_id, org_id, name, description, priority, conditions, condition_logic, actions, identity_ids, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id, uuid, user_id, org_id, name, COALESCE(description, ''), priority, conditions, condition_logic, actions, identity_ids, active, match_count, last_matched_at, created_at, updated_at
	`, userID, orgID, input.Name, input.Description, input.Priority, conditionsJSON, input.ConditionLogic, actionsJSON, identityIDsArray, input.Active,
	).Scan(&rule.ID, &rule.UUID, &rule.UserID, &rule.OrgID, &rule.Name, &rule.Description, &rule.Priority,
		&conditionsJSON, &rule.ConditionLogic, &actionsJSON, scanIntArray(&rule.IdentityIDs),
		&rule.Active, &rule.MatchCount, &rule.LastMatchedAt, &rule.CreatedAt, &rule.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create email rule: %w", err)
	}

	// Unmarshal JSON fields
	json.Unmarshal(conditionsJSON, &rule.Conditions)
	json.Unmarshal(actionsJSON, &rule.Actions)

	return &rule, nil
}

// GetRule gets an email rule by ID
func (s *EmailRulesService) GetRule(ctx context.Context, userID int64, ruleID int) (*EmailRule, error) {
	var rule EmailRule
	var conditionsJSON, actionsJSON []byte

	err := s.db.QueryRowContext(ctx, `
		SELECT id, uuid, user_id, org_id, name, COALESCE(description, ''), priority, conditions, condition_logic, actions, identity_ids, active, match_count, last_matched_at, created_at, updated_at
		FROM email_rules
		WHERE id = $1 AND user_id = $2
	`, ruleID, userID).Scan(&rule.ID, &rule.UUID, &rule.UserID, &rule.OrgID, &rule.Name, &rule.Description, &rule.Priority,
		&conditionsJSON, &rule.ConditionLogic, &actionsJSON, scanIntArray(&rule.IdentityIDs),
		&rule.Active, &rule.MatchCount, &rule.LastMatchedAt, &rule.CreatedAt, &rule.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("email rule not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get email rule: %w", err)
	}

	json.Unmarshal(conditionsJSON, &rule.Conditions)
	json.Unmarshal(actionsJSON, &rule.Actions)

	return &rule, nil
}

// ListRules lists all email rules for a user
func (s *EmailRulesService) ListRules(ctx context.Context, userID int64, activeOnly bool) ([]*EmailRule, error) {
	query := `
		SELECT id, uuid, user_id, org_id, name, COALESCE(description, ''), priority, conditions, condition_logic, actions, identity_ids, active, match_count, last_matched_at, created_at, updated_at
		FROM email_rules
		WHERE user_id = $1
	`
	args := []interface{}{userID}

	if activeOnly {
		query += " AND active = true"
	}

	query += " ORDER BY priority ASC, created_at ASC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list email rules: %w", err)
	}
	defer rows.Close()

	var rules []*EmailRule
	for rows.Next() {
		var rule EmailRule
		var conditionsJSON, actionsJSON []byte

		if err := rows.Scan(&rule.ID, &rule.UUID, &rule.UserID, &rule.OrgID, &rule.Name, &rule.Description, &rule.Priority,
			&conditionsJSON, &rule.ConditionLogic, &actionsJSON, scanIntArray(&rule.IdentityIDs),
			&rule.Active, &rule.MatchCount, &rule.LastMatchedAt, &rule.CreatedAt, &rule.UpdatedAt); err != nil {
			continue
		}

		json.Unmarshal(conditionsJSON, &rule.Conditions)
		json.Unmarshal(actionsJSON, &rule.Actions)

		rules = append(rules, &rule)
	}

	return rules, nil
}

// UpdateRule updates an email rule
func (s *EmailRulesService) UpdateRule(ctx context.Context, userID int64, ruleID int, input *UpdateEmailRuleInput) (*EmailRule, error) {
	// Build update query dynamically
	updates := []string{}
	args := []interface{}{}
	argNum := 1

	if input.Name != nil {
		updates = append(updates, fmt.Sprintf("name = $%d", argNum))
		args = append(args, *input.Name)
		argNum++
	}

	if input.Description != nil {
		updates = append(updates, fmt.Sprintf("description = $%d", argNum))
		args = append(args, *input.Description)
		argNum++
	}

	if input.Priority != nil {
		updates = append(updates, fmt.Sprintf("priority = $%d", argNum))
		args = append(args, *input.Priority)
		argNum++
	}

	if input.Conditions != nil {
		conditionsJSON, _ := json.Marshal(*input.Conditions)
		updates = append(updates, fmt.Sprintf("conditions = $%d", argNum))
		args = append(args, conditionsJSON)
		argNum++
	}

	if input.ConditionLogic != nil {
		updates = append(updates, fmt.Sprintf("condition_logic = $%d", argNum))
		args = append(args, *input.ConditionLogic)
		argNum++
	}

	if input.Actions != nil {
		actionsJSON, _ := json.Marshal(*input.Actions)
		updates = append(updates, fmt.Sprintf("actions = $%d", argNum))
		args = append(args, actionsJSON)
		argNum++
	}

	if input.IdentityIDs != nil {
		identityIDsArray := "{}"
		if len(*input.IdentityIDs) > 0 {
			idStrs := make([]string, len(*input.IdentityIDs))
			for i, id := range *input.IdentityIDs {
				idStrs[i] = fmt.Sprintf("%d", id)
			}
			identityIDsArray = "{" + strings.Join(idStrs, ",") + "}"
		}
		updates = append(updates, fmt.Sprintf("identity_ids = $%d", argNum))
		args = append(args, identityIDsArray)
		argNum++
	}

	if input.Active != nil {
		updates = append(updates, fmt.Sprintf("active = $%d", argNum))
		args = append(args, *input.Active)
		argNum++
	}

	if len(updates) == 0 {
		return s.GetRule(ctx, userID, ruleID)
	}

	updates = append(updates, "updated_at = NOW()")

	query := fmt.Sprintf(`
		UPDATE email_rules
		SET %s
		WHERE id = $%d AND user_id = $%d
	`, strings.Join(updates, ", "), argNum, argNum+1)

	args = append(args, ruleID, userID)

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to update email rule: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return nil, fmt.Errorf("email rule not found")
	}

	return s.GetRule(ctx, userID, ruleID)
}

// DeleteRule deletes an email rule
func (s *EmailRulesService) DeleteRule(ctx context.Context, userID int64, ruleID int) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM email_rules WHERE id = $1 AND user_id = $2
	`, ruleID, userID)
	if err != nil {
		return fmt.Errorf("failed to delete email rule: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("email rule not found")
	}

	return nil
}

// ReorderRules updates the priority order of rules
func (s *EmailRulesService) ReorderRules(ctx context.Context, userID int64, ruleIDs []int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	for i, ruleID := range ruleIDs {
		_, err := tx.ExecContext(ctx, `
			UPDATE email_rules SET priority = $1, updated_at = NOW()
			WHERE id = $2 AND user_id = $3
		`, i, ruleID, userID)
		if err != nil {
			return fmt.Errorf("failed to update rule priority: %w", err)
		}
	}

	return tx.Commit()
}

// TestRule tests a rule against a sample email
func (s *EmailRulesService) TestRule(ctx context.Context, userID int64, ruleID int, email *EmailForTestInput) (bool, []RuleAction, error) {
	rule, err := s.GetRule(ctx, userID, ruleID)
	if err != nil {
		return false, nil, err
	}

	matches := s.evaluateRule(rule, email)
	if matches {
		return true, rule.Actions, nil
	}

	return false, nil, nil
}

// ApplyRulesToEmail applies all active rules to an email and returns the actions to take
func (s *EmailRulesService) ApplyRulesToEmail(ctx context.Context, userID int64, identityID int, email *EmailForTestInput) ([]RuleAction, error) {
	rules, err := s.ListRules(ctx, userID, true)
	if err != nil {
		return nil, err
	}

	var allActions []RuleAction
	stopProcessing := false

	for _, rule := range rules {
		if stopProcessing {
			break
		}

		// Check if rule applies to this identity
		if len(rule.IdentityIDs) > 0 {
			found := false
			for _, id := range rule.IdentityIDs {
				if id == identityID {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		if s.evaluateRule(rule, email) {
			// Update match stats
			s.db.ExecContext(ctx, `
				UPDATE email_rules
				SET match_count = match_count + 1, last_matched_at = NOW()
				WHERE id = $1
			`, rule.ID)

			for _, action := range rule.Actions {
				if action.Type == "stop_processing" {
					stopProcessing = true
					break
				}
				allActions = append(allActions, action)
			}
		}
	}

	return allActions, nil
}

// evaluateRule evaluates a single rule against an email
func (s *EmailRulesService) evaluateRule(rule *EmailRule, email *EmailForTestInput) bool {
	if len(rule.Conditions) == 0 {
		return false
	}

	matchAll := rule.ConditionLogic == "all"
	matchedCount := 0

	for _, condition := range rule.Conditions {
		matched := s.evaluateCondition(&condition, email)
		if matched {
			matchedCount++
			if !matchAll {
				// "any" logic - one match is enough
				return true
			}
		} else if matchAll {
			// "all" logic - one failure means no match
			return false
		}
	}

	return matchedCount == len(rule.Conditions)
}

// evaluateCondition evaluates a single condition against an email
func (s *EmailRulesService) evaluateCondition(condition *RuleCondition, email *EmailForTestInput) bool {
	var fieldValue string

	switch condition.Field {
	case "from":
		fieldValue = email.From
	case "to":
		fieldValue = strings.Join(email.To, ", ")
	case "subject":
		fieldValue = email.Subject
	case "body":
		fieldValue = email.Body
	case "header":
		if email.Headers != nil {
			fieldValue = email.Headers[condition.HeaderName]
		}
	default:
		return false
	}

	return s.matchValue(fieldValue, condition.Operator, condition.Value, condition.CaseSensitive)
}

// matchValue performs the actual comparison
func (s *EmailRulesService) matchValue(fieldValue, operator, testValue string, caseSensitive bool) bool {
	if !caseSensitive {
		fieldValue = strings.ToLower(fieldValue)
		testValue = strings.ToLower(testValue)
	}

	switch operator {
	case "contains":
		return strings.Contains(fieldValue, testValue)
	case "not_contains":
		return !strings.Contains(fieldValue, testValue)
	case "equals":
		return fieldValue == testValue
	case "not_equals":
		return fieldValue != testValue
	case "starts_with":
		return strings.HasPrefix(fieldValue, testValue)
	case "ends_with":
		return strings.HasSuffix(fieldValue, testValue)
	case "matches":
		// Regex match
		re, err := regexp.Compile(testValue)
		if err != nil {
			return false
		}
		return re.MatchString(fieldValue)
	default:
		return false
	}
}

// intArrayScanner implements sql.Scanner for PostgreSQL integer arrays
type intArrayScanner struct {
	dest *[]int
}

func (a *intArrayScanner) Scan(src interface{}) error {
	if src == nil {
		*a.dest = []int{}
		return nil
	}

	var s string
	switch v := src.(type) {
	case []byte:
		s = string(v)
	case string:
		s = v
	default:
		return fmt.Errorf("unsupported type for intArrayScanner: %T", src)
	}

	if s == "{}" || s == "" {
		*a.dest = []int{}
		return nil
	}

	// Remove braces
	s = strings.Trim(s, "{}")

	// Split and parse
	parts := strings.Split(s, ",")
	result := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		var i int
		if _, err := fmt.Sscanf(p, "%d", &i); err == nil {
			result = append(result, i)
		}
	}

	*a.dest = result
	return nil
}

// scanIntArray returns a scanner for PostgreSQL integer arrays
func scanIntArray(dest *[]int) *intArrayScanner {
	return &intArrayScanner{dest: dest}
}
