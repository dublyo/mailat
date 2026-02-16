package service

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
)

// SieveScript represents a Sieve filter script
type SieveScript struct {
	ID        int       `json:"id"`
	UUID      string    `json:"uuid"`
	UserID    int       `json:"userId"`
	OrgID     int       `json:"orgId"`
	Name      string    `json:"name"`
	Script    string    `json:"script"`
	Active    bool      `json:"active"`
	IsDefault bool      `json:"isDefault"`
	IsValid   bool      `json:"isValid"`
	LastError string    `json:"lastError,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// CreateSieveScriptInput is the input for creating a Sieve script
type CreateSieveScriptInput struct {
	Name   string `json:"name"`
	Script string `json:"script"`
	Active bool   `json:"active"`
}

// SieveService handles Sieve script operations
type SieveService struct {
	db  *sql.DB
	cfg *config.Config
}

// NewSieveService creates a new Sieve service
func NewSieveService(db *sql.DB, cfg *config.Config) *SieveService {
	return &SieveService{db: db, cfg: cfg}
}

// Create creates a new Sieve script
func (s *SieveService) Create(ctx context.Context, userID, orgID int64, input *CreateSieveScriptInput) (*SieveScript, error) {
	if input.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if input.Script == "" {
		return nil, fmt.Errorf("script is required")
	}

	// Validate the script
	isValid, validationError := s.validateScript(input.Script)

	var script SieveScript
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO sieve_scripts (user_id, org_id, name, script, active, is_valid, last_error)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, uuid, user_id, org_id, name, script, active, is_default, is_valid, COALESCE(last_error, ''), created_at, updated_at
	`, userID, orgID, input.Name, input.Script, input.Active && isValid, isValid, validationError,
	).Scan(&script.ID, &script.UUID, &script.UserID, &script.OrgID, &script.Name, &script.Script,
		&script.Active, &script.IsDefault, &script.IsValid, &script.LastError, &script.CreatedAt, &script.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create sieve script: %w", err)
	}

	return &script, nil
}

// Get gets a Sieve script by ID
func (s *SieveService) Get(ctx context.Context, userID int64, scriptID int) (*SieveScript, error) {
	var script SieveScript
	err := s.db.QueryRowContext(ctx, `
		SELECT id, uuid, user_id, org_id, name, script, active, is_default, is_valid, COALESCE(last_error, ''), created_at, updated_at
		FROM sieve_scripts
		WHERE id = $1 AND user_id = $2
	`, scriptID, userID).Scan(&script.ID, &script.UUID, &script.UserID, &script.OrgID, &script.Name, &script.Script,
		&script.Active, &script.IsDefault, &script.IsValid, &script.LastError, &script.CreatedAt, &script.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("sieve script not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get sieve script: %w", err)
	}

	return &script, nil
}

// List lists all Sieve scripts for a user
func (s *SieveService) List(ctx context.Context, userID int64) ([]*SieveScript, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, uuid, user_id, org_id, name, script, active, is_default, is_valid, COALESCE(last_error, ''), created_at, updated_at
		FROM sieve_scripts
		WHERE user_id = $1
		ORDER BY is_default DESC, name ASC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list sieve scripts: %w", err)
	}
	defer rows.Close()

	var scripts []*SieveScript
	for rows.Next() {
		var sc SieveScript
		if err := rows.Scan(&sc.ID, &sc.UUID, &sc.UserID, &sc.OrgID, &sc.Name, &sc.Script,
			&sc.Active, &sc.IsDefault, &sc.IsValid, &sc.LastError, &sc.CreatedAt, &sc.UpdatedAt); err != nil {
			continue
		}
		scripts = append(scripts, &sc)
	}

	return scripts, nil
}

// Update updates a Sieve script
func (s *SieveService) Update(ctx context.Context, userID int64, scriptID int, name, script *string, active *bool) (*SieveScript, error) {
	updates := []string{}
	args := []interface{}{}
	argNum := 1

	if name != nil {
		updates = append(updates, fmt.Sprintf("name = $%d", argNum))
		args = append(args, *name)
		argNum++
	}

	if script != nil {
		// Validate the script
		isValid, validationError := s.validateScript(*script)

		updates = append(updates, fmt.Sprintf("script = $%d", argNum))
		args = append(args, *script)
		argNum++

		updates = append(updates, fmt.Sprintf("is_valid = $%d", argNum))
		args = append(args, isValid)
		argNum++

		updates = append(updates, fmt.Sprintf("last_error = $%d", argNum))
		args = append(args, validationError)
		argNum++
	}

	if active != nil {
		updates = append(updates, fmt.Sprintf("active = $%d", argNum))
		args = append(args, *active)
		argNum++
	}

	if len(updates) == 0 {
		return s.Get(ctx, userID, scriptID)
	}

	updates = append(updates, "updated_at = NOW()")

	query := fmt.Sprintf(`
		UPDATE sieve_scripts
		SET %s
		WHERE id = $%d AND user_id = $%d
	`, strings.Join(updates, ", "), argNum, argNum+1)

	args = append(args, scriptID, userID)

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to update sieve script: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return nil, fmt.Errorf("sieve script not found")
	}

	return s.Get(ctx, userID, scriptID)
}

// Delete deletes a Sieve script
func (s *SieveService) Delete(ctx context.Context, userID int64, scriptID int) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM sieve_scripts WHERE id = $1 AND user_id = $2
	`, scriptID, userID)
	if err != nil {
		return fmt.Errorf("failed to delete sieve script: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("sieve script not found")
	}

	return nil
}

// SetDefault sets a script as the default (deactivates others)
func (s *SieveService) SetDefault(ctx context.Context, userID int64, scriptID int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Unset current default
	_, err = tx.ExecContext(ctx, `
		UPDATE sieve_scripts SET is_default = false WHERE user_id = $1
	`, userID)
	if err != nil {
		return fmt.Errorf("failed to unset default: %w", err)
	}

	// Set new default
	result, err := tx.ExecContext(ctx, `
		UPDATE sieve_scripts SET is_default = true, active = true WHERE id = $1 AND user_id = $2 AND is_valid = true
	`, scriptID, userID)
	if err != nil {
		return fmt.Errorf("failed to set default: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("sieve script not found or invalid")
	}

	return tx.Commit()
}

// Validate validates a Sieve script without saving
func (s *SieveService) Validate(script string) (bool, string) {
	return s.validateScript(script)
}

// GetActiveScript returns the active default script for a user
func (s *SieveService) GetActiveScript(ctx context.Context, userID int64) (*SieveScript, error) {
	var script SieveScript
	err := s.db.QueryRowContext(ctx, `
		SELECT id, uuid, user_id, org_id, name, script, active, is_default, is_valid, COALESCE(last_error, ''), created_at, updated_at
		FROM sieve_scripts
		WHERE user_id = $1 AND active = true AND is_default = true AND is_valid = true
	`, userID).Scan(&script.ID, &script.UUID, &script.UserID, &script.OrgID, &script.Name, &script.Script,
		&script.Active, &script.IsDefault, &script.IsValid, &script.LastError, &script.CreatedAt, &script.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil // No active script
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get active script: %w", err)
	}

	return &script, nil
}

// validateScript performs basic Sieve script validation
func (s *SieveService) validateScript(script string) (bool, string) {
	// Basic syntax validation (simplified - real implementation would use a proper parser)

	// Check for required capability declarations
	if strings.Contains(script, "require") {
		// Validate require statement
		requirePattern := regexp.MustCompile(`require\s+\[([^\]]+)\]`)
		matches := requirePattern.FindStringSubmatch(script)
		if len(matches) > 0 {
			// Check for valid capabilities
			validCaps := []string{"fileinto", "reject", "vacation", "envelope", "body", "regex", "copy", "imap4flags", "variables", "include", "relational", "comparator-i;ascii-numeric"}
			caps := strings.Split(matches[1], ",")
			for _, cap := range caps {
				cap = strings.Trim(strings.TrimSpace(cap), "\"")
				found := false
				for _, valid := range validCaps {
					if cap == valid {
						found = true
						break
					}
				}
				if !found {
					return false, fmt.Sprintf("unknown capability: %s", cap)
				}
			}
		}
	}

	// Check for balanced braces
	braceCount := 0
	for _, c := range script {
		if c == '{' {
			braceCount++
		} else if c == '}' {
			braceCount--
		}
		if braceCount < 0 {
			return false, "unmatched closing brace"
		}
	}
	if braceCount != 0 {
		return false, "unmatched opening brace"
	}

	// Check for valid commands
	validCommands := []string{"if", "elsif", "else", "require", "stop", "keep", "discard", "redirect", "reject", "fileinto", "vacation", "set", "addheader", "deleteheader"}
	lines := strings.Split(script, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Check if line starts with a valid command
		foundCommand := false
		for _, cmd := range validCommands {
			if strings.HasPrefix(line, cmd) {
				foundCommand = true
				break
			}
		}
		if !foundCommand && !strings.HasPrefix(line, "}") && !strings.HasPrefix(line, "{") {
			// Could be a continuation or test condition
			if !strings.Contains(line, "\"") && !strings.Contains(line, ":") && !strings.Contains(line, "[") {
				return false, fmt.Sprintf("unknown command on line: %s", line)
			}
		}
	}

	return true, ""
}

// GenerateFromRules generates a Sieve script from email rules
func (s *SieveService) GenerateFromRules(rules []*EmailRule) string {
	var sb strings.Builder

	sb.WriteString("# Auto-generated Sieve script from email rules\n")
	sb.WriteString("require [\"fileinto\", \"imap4flags\", \"reject\", \"vacation\", \"copy\"];\n\n")

	for _, rule := range rules {
		if !rule.Active {
			continue
		}

		sb.WriteString(fmt.Sprintf("# Rule: %s\n", rule.Name))

		// Build condition
		conditions := make([]string, 0)
		for _, cond := range rule.Conditions {
			condition := s.buildSieveCondition(cond)
			if condition != "" {
				conditions = append(conditions, condition)
			}
		}

		if len(conditions) == 0 {
			continue
		}

		logic := "allof"
		if rule.ConditionLogic == "any" {
			logic = "anyof"
		}

		if len(conditions) == 1 {
			sb.WriteString(fmt.Sprintf("if %s {\n", conditions[0]))
		} else {
			sb.WriteString(fmt.Sprintf("if %s (%s) {\n", logic, strings.Join(conditions, ", ")))
		}

		// Build actions
		for _, action := range rule.Actions {
			actionStr := s.buildSieveAction(action)
			if actionStr != "" {
				sb.WriteString(fmt.Sprintf("    %s\n", actionStr))
			}
		}

		sb.WriteString("}\n\n")
	}

	return sb.String()
}

func (s *SieveService) buildSieveCondition(cond RuleCondition) string {
	field := ""
	switch cond.Field {
	case "from":
		field = "From"
	case "to":
		field = "To"
	case "subject":
		field = "Subject"
	case "body":
		return fmt.Sprintf("body :contains \"%s\"", escapeSieveString(cond.Value))
	default:
		return ""
	}

	switch cond.Operator {
	case "contains":
		return fmt.Sprintf("header :contains \"%s\" \"%s\"", field, escapeSieveString(cond.Value))
	case "equals":
		return fmt.Sprintf("header :is \"%s\" \"%s\"", field, escapeSieveString(cond.Value))
	case "matches":
		return fmt.Sprintf("header :matches \"%s\" \"%s\"", field, escapeSieveString(cond.Value))
	case "starts_with":
		return fmt.Sprintf("header :matches \"%s\" \"%s*\"", field, escapeSieveString(cond.Value))
	case "ends_with":
		return fmt.Sprintf("header :matches \"%s\" \"*%s\"", field, escapeSieveString(cond.Value))
	default:
		return ""
	}
}

func (s *SieveService) buildSieveAction(action RuleAction) string {
	switch action.Type {
	case "move_to_folder":
		return fmt.Sprintf("fileinto \"%s\";", escapeSieveString(action.Value))
	case "delete":
		return "discard;"
	case "mark_read":
		return "addflag \"\\\\Seen\";"
	case "mark_starred":
		return "addflag \"\\\\Flagged\";"
	case "forward":
		return fmt.Sprintf("redirect :copy \"%s\";", escapeSieveString(action.Value))
	case "reject":
		return fmt.Sprintf("reject \"%s\";", escapeSieveString(action.Value))
	case "stop_processing":
		return "stop;"
	default:
		return ""
	}
}

func escapeSieveString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return s
}
