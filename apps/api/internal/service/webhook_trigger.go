package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
)

// Webhook trigger types
const (
	TriggerEmailReceived    = "email_received"
	TriggerEmailSent        = "email_sent"
	TriggerContactCreated   = "contact_created"
	TriggerContactUpdated   = "contact_updated"
	TriggerContactDeleted   = "contact_deleted"
	TriggerCampaignSent     = "campaign_sent"
	TriggerCampaignOpened   = "campaign_opened"
	TriggerCampaignClicked  = "campaign_clicked"
	TriggerBounceReceived   = "bounce_received"
	TriggerComplaintReceived = "complaint_received"
	TriggerSubscribed       = "subscribed"
	TriggerUnsubscribed     = "unsubscribed"
)

// WebhookTrigger represents a webhook trigger configuration
type WebhookTrigger struct {
	ID              int        `json:"id"`
	UUID            string     `json:"uuid"`
	OrgID           int        `json:"orgId"`
	UserID          int        `json:"userId"`
	Name            string     `json:"name"`
	Description     string     `json:"description,omitempty"`
	TriggerType     string     `json:"triggerType"`
	Filters         map[string]interface{} `json:"filters,omitempty"`
	WebhookURL      string     `json:"webhookUrl"`
	Active          bool       `json:"active"`
	LastTriggeredAt *time.Time `json:"lastTriggeredAt,omitempty"`
	TriggerCount    int        `json:"triggerCount"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

// CreateWebhookTriggerInput is the input for creating a webhook trigger
type CreateWebhookTriggerInput struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	TriggerType string                 `json:"triggerType"`
	Filters     map[string]interface{} `json:"filters,omitempty"`
	WebhookURL  string                 `json:"webhookUrl"`
	Active      bool                   `json:"active"`
}

// WebhookPayload is the payload sent to webhook endpoints
type WebhookPayload struct {
	Event     string                 `json:"event"`
	Timestamp string                 `json:"timestamp"`
	Data      map[string]interface{} `json:"data"`
}

// WebhookTriggerService handles webhook trigger operations
type WebhookTriggerService struct {
	db         *sql.DB
	cfg        *config.Config
	httpClient *http.Client
}

// NewWebhookTriggerService creates a new webhook trigger service
func NewWebhookTriggerService(db *sql.DB, cfg *config.Config) *WebhookTriggerService {
	return &WebhookTriggerService{
		db:         db,
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Create creates a new webhook trigger
func (s *WebhookTriggerService) Create(ctx context.Context, userID, orgID int64, input *CreateWebhookTriggerInput) (*WebhookTrigger, error) {
	if input.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if input.TriggerType == "" {
		return nil, fmt.Errorf("trigger type is required")
	}
	if input.WebhookURL == "" {
		return nil, fmt.Errorf("webhook URL is required")
	}

	// Validate trigger type
	validTypes := []string{TriggerEmailReceived, TriggerEmailSent, TriggerContactCreated, TriggerContactUpdated,
		TriggerContactDeleted, TriggerCampaignSent, TriggerCampaignOpened, TriggerCampaignClicked,
		TriggerBounceReceived, TriggerComplaintReceived, TriggerSubscribed, TriggerUnsubscribed}
	valid := false
	for _, t := range validTypes {
		if input.TriggerType == t {
			valid = true
			break
		}
	}
	if !valid {
		return nil, fmt.Errorf("invalid trigger type: %s", input.TriggerType)
	}

	// Generate secret for HMAC signing
	secret := generateRandomToken(32)

	var filtersJSON []byte
	var err error
	if input.Filters != nil {
		filtersJSON, err = json.Marshal(input.Filters)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal filters: %w", err)
		}
	}

	var trigger WebhookTrigger
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO webhook_triggers (org_id, user_id, name, description, trigger_type, filters, webhook_url, secret, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, uuid, org_id, user_id, name, COALESCE(description, ''), trigger_type, filters, webhook_url, active, last_triggered_at, trigger_count, created_at, updated_at
	`, orgID, userID, input.Name, input.Description, input.TriggerType, filtersJSON, input.WebhookURL, secret, input.Active,
	).Scan(&trigger.ID, &trigger.UUID, &trigger.OrgID, &trigger.UserID, &trigger.Name, &trigger.Description,
		&trigger.TriggerType, &filtersJSON, &trigger.WebhookURL, &trigger.Active,
		&trigger.LastTriggeredAt, &trigger.TriggerCount, &trigger.CreatedAt, &trigger.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create webhook trigger: %w", err)
	}

	if filtersJSON != nil {
		json.Unmarshal(filtersJSON, &trigger.Filters)
	}

	return &trigger, nil
}

// Get gets a webhook trigger by ID
func (s *WebhookTriggerService) Get(ctx context.Context, orgID int64, triggerID int) (*WebhookTrigger, error) {
	var trigger WebhookTrigger
	var filtersJSON []byte

	err := s.db.QueryRowContext(ctx, `
		SELECT id, uuid, org_id, user_id, name, COALESCE(description, ''), trigger_type, filters, webhook_url, active, last_triggered_at, trigger_count, created_at, updated_at
		FROM webhook_triggers
		WHERE id = $1 AND org_id = $2
	`, triggerID, orgID).Scan(&trigger.ID, &trigger.UUID, &trigger.OrgID, &trigger.UserID, &trigger.Name, &trigger.Description,
		&trigger.TriggerType, &filtersJSON, &trigger.WebhookURL, &trigger.Active,
		&trigger.LastTriggeredAt, &trigger.TriggerCount, &trigger.CreatedAt, &trigger.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("webhook trigger not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get webhook trigger: %w", err)
	}

	if filtersJSON != nil {
		json.Unmarshal(filtersJSON, &trigger.Filters)
	}

	return &trigger, nil
}

// List lists all webhook triggers for an organization
func (s *WebhookTriggerService) List(ctx context.Context, orgID int64) ([]*WebhookTrigger, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, uuid, org_id, user_id, name, COALESCE(description, ''), trigger_type, filters, webhook_url, active, last_triggered_at, trigger_count, created_at, updated_at
		FROM webhook_triggers
		WHERE org_id = $1
		ORDER BY created_at DESC
	`, orgID)
	if err != nil {
		return nil, fmt.Errorf("failed to list webhook triggers: %w", err)
	}
	defer rows.Close()

	var triggers []*WebhookTrigger
	for rows.Next() {
		var t WebhookTrigger
		var filtersJSON []byte

		if err := rows.Scan(&t.ID, &t.UUID, &t.OrgID, &t.UserID, &t.Name, &t.Description,
			&t.TriggerType, &filtersJSON, &t.WebhookURL, &t.Active,
			&t.LastTriggeredAt, &t.TriggerCount, &t.CreatedAt, &t.UpdatedAt); err != nil {
			continue
		}

		if filtersJSON != nil {
			json.Unmarshal(filtersJSON, &t.Filters)
		}

		triggers = append(triggers, &t)
	}

	return triggers, nil
}

// Update updates a webhook trigger
func (s *WebhookTriggerService) Update(ctx context.Context, orgID int64, triggerID int, name, description, webhookURL *string, filters *map[string]interface{}, active *bool) (*WebhookTrigger, error) {
	updates := []string{}
	args := []interface{}{}
	argNum := 1

	if name != nil {
		updates = append(updates, fmt.Sprintf("name = $%d", argNum))
		args = append(args, *name)
		argNum++
	}

	if description != nil {
		updates = append(updates, fmt.Sprintf("description = $%d", argNum))
		args = append(args, *description)
		argNum++
	}

	if webhookURL != nil {
		updates = append(updates, fmt.Sprintf("webhook_url = $%d", argNum))
		args = append(args, *webhookURL)
		argNum++
	}

	if filters != nil {
		filtersJSON, _ := json.Marshal(*filters)
		updates = append(updates, fmt.Sprintf("filters = $%d", argNum))
		args = append(args, filtersJSON)
		argNum++
	}

	if active != nil {
		updates = append(updates, fmt.Sprintf("active = $%d", argNum))
		args = append(args, *active)
		argNum++
	}

	if len(updates) == 0 {
		return s.Get(ctx, orgID, triggerID)
	}

	updates = append(updates, "updated_at = NOW()")

	query := fmt.Sprintf(`
		UPDATE webhook_triggers
		SET %s
		WHERE id = $%d AND org_id = $%d
	`, strings.Join(updates, ", "), argNum, argNum+1)

	args = append(args, triggerID, orgID)

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to update webhook trigger: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return nil, fmt.Errorf("webhook trigger not found")
	}

	return s.Get(ctx, orgID, triggerID)
}

// Delete deletes a webhook trigger
func (s *WebhookTriggerService) Delete(ctx context.Context, orgID int64, triggerID int) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM webhook_triggers WHERE id = $1 AND org_id = $2
	`, triggerID, orgID)
	if err != nil {
		return fmt.Errorf("failed to delete webhook trigger: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("webhook trigger not found")
	}

	return nil
}

// Fire fires all matching triggers for an event
func (s *WebhookTriggerService) Fire(ctx context.Context, orgID int64, triggerType string, data map[string]interface{}) error {
	// Get all active triggers of this type
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, webhook_url, secret, filters
		FROM webhook_triggers
		WHERE org_id = $1 AND trigger_type = $2 AND active = true
	`, orgID, triggerType)
	if err != nil {
		return fmt.Errorf("failed to get triggers: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int
		var webhookURL, secret string
		var filtersJSON []byte

		if err := rows.Scan(&id, &webhookURL, &secret, &filtersJSON); err != nil {
			continue
		}

		// Check filters
		if filtersJSON != nil {
			var filters map[string]interface{}
			json.Unmarshal(filtersJSON, &filters)
			if !s.matchFilters(filters, data) {
				continue
			}
		}

		// Fire the webhook asynchronously
		go s.sendWebhook(id, webhookURL, secret, triggerType, data)
	}

	return nil
}

// matchFilters checks if data matches the filters
func (s *WebhookTriggerService) matchFilters(filters, data map[string]interface{}) bool {
	for key, filterValue := range filters {
		dataValue, ok := data[key]
		if !ok {
			return false
		}

		// Simple equality check (can be extended for more complex filters)
		if fmt.Sprintf("%v", filterValue) != fmt.Sprintf("%v", dataValue) {
			return false
		}
	}
	return true
}

// sendWebhook sends a webhook request
func (s *WebhookTriggerService) sendWebhook(triggerID int, url, secret, event string, data map[string]interface{}) {
	payload := WebhookPayload{
		Event:     event,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Data:      data,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("%s/1.0", s.cfg.AppName))
	req.Header.Set("X-Webhook-Event", event)

	// Add HMAC signature if secret is set
	if secret != "" {
		signature := s.computeSignature(body, secret)
		req.Header.Set("X-Webhook-Signature", signature)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	// Update trigger stats
	s.db.ExecContext(context.Background(), `
		UPDATE webhook_triggers
		SET trigger_count = trigger_count + 1, last_triggered_at = NOW()
		WHERE id = $1
	`, triggerID)
}

// computeSignature computes HMAC-SHA256 signature
func (s *WebhookTriggerService) computeSignature(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Test tests a webhook trigger by sending a test payload
func (s *WebhookTriggerService) Test(ctx context.Context, orgID int64, triggerID int) error {
	trigger, err := s.Get(ctx, orgID, triggerID)
	if err != nil {
		return err
	}

	// Get secret
	var secret string
	s.db.QueryRowContext(ctx, `SELECT secret FROM webhook_triggers WHERE id = $1`, triggerID).Scan(&secret)

	testData := map[string]interface{}{
		"test":      true,
		"message":   fmt.Sprintf("This is a test webhook from %s", s.cfg.AppName),
		"triggerId": trigger.UUID,
	}

	s.sendWebhook(triggerID, trigger.WebhookURL, secret, trigger.TriggerType+".test", testData)

	return nil
}

// GetAvailableTriggerTypes returns all available trigger types
func (s *WebhookTriggerService) GetAvailableTriggerTypes() []map[string]string {
	return []map[string]string{
		{"type": TriggerEmailReceived, "name": "Email Received", "description": "Triggered when a new email is received"},
		{"type": TriggerEmailSent, "name": "Email Sent", "description": "Triggered when an email is sent"},
		{"type": TriggerContactCreated, "name": "Contact Created", "description": "Triggered when a new contact is created"},
		{"type": TriggerContactUpdated, "name": "Contact Updated", "description": "Triggered when a contact is updated"},
		{"type": TriggerContactDeleted, "name": "Contact Deleted", "description": "Triggered when a contact is deleted"},
		{"type": TriggerCampaignSent, "name": "Campaign Sent", "description": "Triggered when a campaign is sent"},
		{"type": TriggerCampaignOpened, "name": "Campaign Opened", "description": "Triggered when a campaign email is opened"},
		{"type": TriggerCampaignClicked, "name": "Campaign Clicked", "description": "Triggered when a link in a campaign is clicked"},
		{"type": TriggerBounceReceived, "name": "Bounce Received", "description": "Triggered when an email bounces"},
		{"type": TriggerComplaintReceived, "name": "Complaint Received", "description": "Triggered when a spam complaint is received"},
		{"type": TriggerSubscribed, "name": "Subscribed", "description": "Triggered when someone subscribes to a list"},
		{"type": TriggerUnsubscribed, "name": "Unsubscribed", "description": "Triggered when someone unsubscribes from a list"},
	}
}
