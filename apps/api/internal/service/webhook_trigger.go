package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/eventoutbox"
)

// Webhook trigger types
const (
	TriggerEmailReceived     = "email_received"
	TriggerEmailSent         = "email_sent"
	TriggerContactCreated    = "contact_created"
	TriggerContactUpdated    = "contact_updated"
	TriggerContactDeleted    = "contact_deleted"
	TriggerCampaignSent      = "campaign_sent"
	TriggerCampaignOpened    = "campaign_opened"
	TriggerCampaignClicked   = "campaign_clicked"
	TriggerBounceReceived    = "bounce_received"
	TriggerComplaintReceived = "complaint_received"
	TriggerSubscribed        = "subscribed"
	TriggerUnsubscribed      = "unsubscribed"
)

// WebhookTrigger represents a webhook trigger configuration
type WebhookTrigger struct {
	Secret          string                 `json:"secret,omitempty"`
	ID              int                    `json:"id"`
	UUID            string                 `json:"uuid"`
	OrgID           int                    `json:"orgId"`
	UserID          int                    `json:"userId"`
	Name            string                 `json:"name"`
	Description     string                 `json:"description,omitempty"`
	TriggerType     string                 `json:"triggerType"`
	Filters         map[string]interface{} `json:"filters,omitempty"`
	WebhookURL      string                 `json:"webhookUrl"`
	Active          bool                   `json:"active"`
	LastTriggeredAt *time.Time             `json:"lastTriggeredAt,omitempty"`
	TriggerCount    int                    `json:"triggerCount"`
	CreatedAt       time.Time              `json:"createdAt"`
	UpdatedAt       time.Time              `json:"updatedAt"`
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
	httpClient *http.Client
	db         *sql.DB
	cfg        *config.Config
	userID     int64
}

// NewWebhookTriggerService creates a new webhook trigger service
func NewWebhookTriggerService(db *sql.DB, cfg *config.Config) *WebhookTriggerService {
	return &WebhookTriggerService{
		db:  db,
		cfg: cfg,
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

	if !eventoutbox.KnownType(input.TriggerType) {
		return nil, fmt.Errorf("unsupported trigger type")
	}
	if err := eventoutbox.ValidateDestination(input.WebhookURL); err != nil {
		return nil, err
	}
	secret := generateWebhookSecret()
	if input.Filters == nil {
		input.Filters = map[string]interface{}{}
	}
	filtersJSON, err := json.Marshal(input.Filters)
	if err != nil {
		return nil, err
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

	trigger.Secret = secret
	return &trigger, nil
}

// Get gets a webhook trigger by ID
func (s *WebhookTriggerService) Get(ctx context.Context, orgID int64, triggerID int) (*WebhookTrigger, error) {
	var trigger WebhookTrigger
	var filtersJSON []byte

	err := s.db.QueryRowContext(ctx, `
		SELECT id, uuid, org_id, user_id, name, COALESCE(description, ''), trigger_type, filters, webhook_url, active, last_triggered_at, trigger_count, created_at, updated_at
		FROM webhook_triggers
		WHERE id = $1 AND org_id = $2 AND ($3=0 OR user_id=$3)
	`, triggerID, orgID, s.userID).Scan(&trigger.ID, &trigger.UUID, &trigger.OrgID, &trigger.UserID, &trigger.Name, &trigger.Description,
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
		WHERE org_id = $1 AND ($2=0 OR user_id=$2)
		ORDER BY created_at DESC
	`, orgID, s.userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list webhook triggers: %w", err)
	}
	defer rows.Close()

	triggers := []*WebhookTrigger{}
	for rows.Next() {
		var t WebhookTrigger
		var filtersJSON []byte

		if err := rows.Scan(&t.ID, &t.UUID, &t.OrgID, &t.UserID, &t.Name, &t.Description,
			&t.TriggerType, &filtersJSON, &t.WebhookURL, &t.Active,
			&t.LastTriggeredAt, &t.TriggerCount, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}

		if filtersJSON != nil {
			json.Unmarshal(filtersJSON, &t.Filters)
		}

		triggers = append(triggers, &t)
	}

	return triggers, rows.Err()
}

// Update updates a webhook trigger
func (s *WebhookTriggerService) Update(ctx context.Context, orgID int64, triggerID int, name, description, webhookURL *string, filters *map[string]interface{}, active *bool) (*WebhookTrigger, error) {
	if _, err := s.Get(ctx, orgID, triggerID); err != nil {
		return nil, err
	}
	if webhookURL != nil {
		if err := eventoutbox.ValidateDestination(*webhookURL); err != nil {
			return nil, err
		}
	}
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
		if *filters == nil {
			*filters = map[string]interface{}{}
		}
		filtersJSON, err := json.Marshal(*filters)
		if err != nil {
			return nil, err
		}
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
	if _, err := s.Get(ctx, orgID, triggerID); err != nil {
		return err
	}
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

// ForUser binds HTTP operations to the authenticated mailbox owner.
func (s *WebhookTriggerService) ForUser(userID int64) *WebhookTriggerService {
	copy := *s
	copy.userID = userID
	return &copy
}
func (s *WebhookTriggerService) ResolveID(ctx context.Context, orgID int64, ref string) (int, error) {
	var id int
	err := s.db.QueryRowContext(ctx, `SELECT id FROM webhook_triggers WHERE (uuid::text=$1 OR id::text=$1) AND org_id=$2 AND ($3=0 OR user_id=$3)`, ref, orgID, s.userID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("webhook trigger not found")
	}
	return id, nil
}
func (s *WebhookTriggerService) RotateSecret(ctx context.Context, orgID int64, id int) (string, error) {
	if _, err := s.Get(ctx, orgID, id); err != nil {
		return "", err
	}
	secret := generateWebhookSecret()
	_, err := s.db.ExecContext(ctx, `UPDATE webhook_triggers SET secret=$1,updated_at=NOW() WHERE id=$2`, secret, id)
	return secret, err
}

// Fire supports older internal callers. Mail events use Emit inside their source transaction.
func (s *WebhookTriggerService) Fire(ctx context.Context, orgID int64, kind string, data map[string]interface{}) error {
	return eventoutbox.EmitLegacy(ctx, s.db, orgID, kind, data)
}
func (s *WebhookTriggerService) TestDelivery(ctx context.Context, orgID int64, id int) (*eventoutbox.Result, error) {
	t, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if !t.Active {
		return nil, fmt.Errorf("activate this trigger before testing")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	eventID, deliveryID, err := eventoutbox.QueueTarget(ctx, tx, eventoutbox.Event{Type: "webhook.test", OrgID: orgID, UserID: int64(t.UserID), Data: map[string]any{"test": true, "triggerUuid": t.UUID}}, 0, int64(id))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	dispatcher := eventoutbox.NewDispatcher(s.db)
	if s.httpClient != nil {
		dispatcher.Client = s.httpClient
	}
	result, err := dispatcher.DispatchOne(ctx, deliveryID)
	if result == nil && err == nil {
		return &eventoutbox.Result{EventID: eventID, DeliveryID: deliveryID, Status: "pending"}, nil
	}
	return result, err
}
func (s *WebhookTriggerService) Test(ctx context.Context, orgID int64, id int) error {
	r, err := s.TestDelivery(ctx, orgID, id)
	if err != nil {
		return err
	}
	if r.Status != "delivered" {
		return fmt.Errorf("webhook delivery %s: %s", r.Status, r.Error)
	}
	return nil
}
func (s *WebhookTriggerService) GetAvailableTriggerTypes() []map[string]string {
	out := []map[string]string{}
	for _, kind := range eventoutbox.Types {
		out = append(out, map[string]string{"type": kind, "name": kind, "description": "Durable " + kind + " event"})
	}
	return out
}
