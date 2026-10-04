package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/eventoutbox"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"net/http"
	"strings"
	"time"
)

// WebhookService handles webhook operations
type WebhookService struct {
	httpClient *http.Client
	db         *sql.DB
	cfg        *config.Config
	userID     int64
}

func NewWebhookService(db *sql.DB, cfg *config.Config) *WebhookService {
	return &WebhookService{db: db, cfg: cfg}
}

// CreateWebhook creates a new webhook endpoint
func (s *WebhookService) CreateWebhook(ctx context.Context, orgID int64, req *model.CreateWebhookRequest) (*model.WebhookResponse, error) {
	if err := eventoutbox.ValidateDestination(req.URL); err != nil {
		return nil, err
	}
	for _, kind := range req.Events {
		if !eventoutbox.KnownType(kind) {
			return nil, fmt.Errorf("unsupported event type")
		}
	}
	webhookUUID := uuid.New().String()
	secret := generateWebhookSecret()

	var webhook model.WebhookResponse

	err := s.db.QueryRowContext(ctx, `
		INSERT INTO webhooks (uuid, org_id, name, url, secret, events, active, updated_at,user_id)
		VALUES ($1, $2, $3, $4, $5, $6, true, NOW(),NULLIF($7,0))
		RETURNING id, uuid, name, url, events, active, created_at, updated_at
	`, webhookUUID, orgID, req.Name, req.URL, secret, pq.Array(req.Events), s.userID).Scan(
		&webhook.ID, &webhook.UUID, &webhook.Name, &webhook.URL,
		pq.Array(&webhook.Events), &webhook.Active, &webhook.CreatedAt, &webhook.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create webhook: %w", err)
	}

	webhook.Secret = secret // Only returned on creation

	return &webhook, nil
}

// GetWebhook retrieves a webhook by UUID
func (s *WebhookService) GetWebhook(ctx context.Context, orgID int64, webhookUUID string) (*model.WebhookResponse, error) {
	if err := s.authorize(ctx, orgID, webhookUUID); err != nil {
		return nil, err
	}
	var webhook model.WebhookResponse

	err := s.db.QueryRowContext(ctx, `
		SELECT id, uuid, name, url, events, active, success_count, failure_count,
		       last_triggered_at, last_success_at, last_failure_at, created_at, updated_at
		FROM webhooks
		WHERE uuid = $1 AND org_id = $2
	`, webhookUUID, orgID).Scan(
		&webhook.ID, &webhook.UUID, &webhook.Name, &webhook.URL, pq.Array(&webhook.Events),
		&webhook.Active, &webhook.SuccessCount, &webhook.FailureCount,
		&webhook.LastTriggeredAt, &webhook.LastSuccessAt, &webhook.LastFailureAt,
		&webhook.CreatedAt, &webhook.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("webhook not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get webhook: %w", err)
	}

	return &webhook, nil
}

// ListWebhooks returns all webhooks for an organization
func (s *WebhookService) ListWebhooks(ctx context.Context, orgID int64) ([]*model.WebhookResponse, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, uuid, name, url, events, active, success_count, failure_count,
		       last_triggered_at, last_success_at, last_failure_at, created_at, updated_at
		FROM webhooks
		WHERE org_id = $1 AND ($2=0 OR user_id=$2)
		ORDER BY created_at DESC
	`, orgID, s.userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list webhooks: %w", err)
	}
	defer rows.Close()

	webhooks := []*model.WebhookResponse{}
	for rows.Next() {
		var webhook model.WebhookResponse
		if err := rows.Scan(
			&webhook.ID, &webhook.UUID, &webhook.Name, &webhook.URL, pq.Array(&webhook.Events),
			&webhook.Active, &webhook.SuccessCount, &webhook.FailureCount,
			&webhook.LastTriggeredAt, &webhook.LastSuccessAt, &webhook.LastFailureAt,
			&webhook.CreatedAt, &webhook.UpdatedAt,
		); err != nil {
			return nil, err
		}
		webhooks = append(webhooks, &webhook)
	}

	return webhooks, rows.Err()
}

// UpdateWebhook updates a webhook
func (s *WebhookService) UpdateWebhook(ctx context.Context, orgID int64, webhookUUID string, req *model.UpdateWebhookRequest) (*model.WebhookResponse, error) {
	if err := s.authorize(ctx, orgID, webhookUUID); err != nil {
		return nil, err
	}
	for _, kind := range req.Events {
		if !eventoutbox.KnownType(kind) {
			return nil, fmt.Errorf("unsupported event type")
		}
	}
	updates := []string{}
	args := []interface{}{}
	argIndex := 1

	if req.Name != "" {
		updates = append(updates, fmt.Sprintf("name = $%d", argIndex))
		args = append(args, req.Name)
		argIndex++
	}
	if req.URL != "" {
		if err := eventoutbox.ValidateDestination(req.URL); err != nil {
			return nil, err
		}
		updates = append(updates, fmt.Sprintf("url = $%d", argIndex))
		args = append(args, req.URL)
		argIndex++
	}
	if len(req.Events) > 0 {
		updates = append(updates, fmt.Sprintf("events = $%d", argIndex))
		args = append(args, pq.Array(req.Events))
		argIndex++
	}
	if req.Active != nil {
		updates = append(updates, fmt.Sprintf("active = $%d", argIndex))
		args = append(args, *req.Active)
		argIndex++
	}

	if len(updates) == 0 {
		return s.GetWebhook(ctx, orgID, webhookUUID)
	}

	updates = append(updates, "updated_at = NOW()")

	query := fmt.Sprintf(`
		UPDATE webhooks SET %s
		WHERE uuid = $%d AND org_id = $%d
	`, joinStringsWithSep(updates, ", "), argIndex, argIndex+1)
	args = append(args, webhookUUID, orgID)

	_, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to update webhook: %w", err)
	}

	return s.GetWebhook(ctx, orgID, webhookUUID)
}

// DeleteWebhook deletes a webhook
func (s *WebhookService) DeleteWebhook(ctx context.Context, orgID int64, webhookUUID string) error {
	if err := s.authorize(ctx, orgID, webhookUUID); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM webhooks WHERE uuid = $1 AND org_id = $2
	`, webhookUUID, orgID)
	if err != nil {
		return fmt.Errorf("failed to delete webhook: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("webhook not found")
	}

	return nil
}

// RotateSecret generates a new secret for a webhook
func (s *WebhookService) RotateSecret(ctx context.Context, orgID int64, webhookUUID string) (string, error) {
	if err := s.authorize(ctx, orgID, webhookUUID); err != nil {
		return "", err
	}
	newSecret := generateWebhookSecret()

	result, err := s.db.ExecContext(ctx, `
		UPDATE webhooks SET secret = $1, updated_at = NOW()
		WHERE uuid = $2 AND org_id = $3
	`, newSecret, webhookUUID, orgID)
	if err != nil {
		return "", fmt.Errorf("failed to rotate secret: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return "", fmt.Errorf("webhook not found")
	}

	return newSecret, nil
}

// GetWebhookCalls returns recent webhook calls for a webhook
func (s *WebhookService) GetWebhookCalls(ctx context.Context, orgID int64, webhookUUID string, limit int) ([]*model.WebhookCallResponse, error) {
	if err := s.authorize(ctx, orgID, webhookUUID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	rows, err := s.db.QueryContext(ctx, `
 SELECT a.id,e.event_type,e.payload,a.http_status,a.response_body,a.duration_ms,
 CASE WHEN a.http_status BETWEEN 200 AND 299 AND a.error='' THEN 'success' ELSE 'failed' END,a.attempt,a.error,a.created_at,a.created_at
 FROM webhook_delivery_attempts a JOIN webhook_deliveries d ON d.id=a.delivery_id JOIN webhook_events e ON e.id=d.event_id JOIN webhooks w ON w.id=d.webhook_id
 WHERE w.uuid=$1 AND w.org_id=$2 ORDER BY a.created_at DESC,a.id DESC LIMIT $3`, webhookUUID, orgID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get webhook calls: %w", err)
	}
	defer rows.Close()

	calls := []*model.WebhookCallResponse{}
	for rows.Next() {
		var call model.WebhookCallResponse
		var payload, responseBody, errorMsg sql.NullString
		var responseStatus, responseTimeMs sql.NullInt32
		var completedAt sql.NullTime

		if err := rows.Scan(
			&call.ID, &call.EventType, &payload, &responseStatus, &responseBody,
			&responseTimeMs, &call.Status, &call.Attempts, &errorMsg, &call.CreatedAt, &completedAt,
		); err != nil {
			return nil, err
		}

		if payload.Valid {
			json.Unmarshal([]byte(payload.String), &call.Payload)
		}
		if responseStatus.Valid {
			call.ResponseStatus = int(responseStatus.Int32)
		}
		if responseBody.Valid {
			call.ResponseBody = responseBody.String
		}
		if responseTimeMs.Valid {
			call.ResponseTimeMs = int(responseTimeMs.Int32)
		}
		if errorMsg.Valid {
			call.Error = errorMsg.String
		}
		if completedAt.Valid {
			call.CompletedAt = &completedAt.Time
		}

		calls = append(calls, &call)
	}

	return calls, rows.Err()
}

// DeliverWebhook queues legacy callers through the same durable delivery engine.
func (s *WebhookService) DeliverWebhook(ctx context.Context, webhookID int64, eventType string, payload map[string]interface{}) error {
	var orgID, userID int64
	if err := s.db.QueryRowContext(ctx, `SELECT org_id,COALESCE(user_id,0) FROM webhooks WHERE id=$1`, webhookID).Scan(&orgID, &userID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, _, err = eventoutbox.QueueTarget(ctx, tx, eventoutbox.Event{Type: eventType, OrgID: orgID, UserID: userID, Data: payload}, webhookID, 0)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *WebhookService) TestDelivery(ctx context.Context, orgID int64, webhookUUID string) (*eventoutbox.Result, error) {
	webhook, err := s.GetWebhook(ctx, orgID, webhookUUID)
	if err != nil {
		return nil, err
	}
	if !webhook.Active {
		return nil, fmt.Errorf("activate this endpoint before testing")
	}
	var userID int64
	if err = s.db.QueryRowContext(ctx, `SELECT COALESCE(user_id,0) FROM webhooks WHERE id=$1`, webhook.ID).Scan(&userID); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	eventID, delivery, err := eventoutbox.QueueTarget(ctx, tx, eventoutbox.Event{Type: "webhook.test", OrgID: orgID, UserID: userID, Data: map[string]any{"test": true}}, webhook.ID, 0)
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
	result, err := dispatcher.DispatchOne(ctx, delivery)
	if result == nil && err == nil {
		return &eventoutbox.Result{EventID: eventID, DeliveryID: delivery, Status: "pending"}, nil
	}
	return result, err
}

func (s *WebhookService) ForUser(userID int64) *WebhookService {
	copy := *s
	copy.userID = userID
	return &copy
}
func (s *WebhookService) authorize(ctx context.Context, orgID int64, id string) error {
	if s.userID == 0 {
		return nil
	}
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM webhooks WHERE uuid::text=$1 AND org_id=$2 AND user_id=$3)`, id, orgID, s.userID).Scan(&exists)
	if err != nil {
		return fmt.Errorf("could not check webhook ownership")
	}
	if !exists {
		return fmt.Errorf("webhook not found")
	}
	return nil
}

// VerifySignature is kept for compatibility; all producers use this same format.
func VerifySignature(payload []byte, signature, secret string, tolerance time.Duration) bool {
	return eventoutbox.Verify(payload, signature, secret, tolerance)
}
func generateWebhookSecret() string {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		panic("secure random source unavailable")
	}
	return "whsec_" + hex.EncodeToString(data)
}
func joinStringsWithSep(strs []string, sep string) string { return strings.Join(strs, sep) }

func (s *WebhookService) Deliveries(ctx context.Context, org, user int64, status string, page, size int) (*eventoutbox.DeliveryPage, error) {
	return eventoutbox.List(ctx, s.db, org, user, status, page, size)
}
func (s *WebhookService) Delivery(ctx context.Context, org, user int64, id string) (*eventoutbox.Delivery, []eventoutbox.Attempt, error) {
	return eventoutbox.Get(ctx, s.db, org, user, id)
}
func (s *WebhookService) Replay(ctx context.Context, org, user int64, id string) error {
	return eventoutbox.Replay(ctx, s.db, org, user, id)
}
