package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/worker"
)

// WebhookService handles webhook operations
type WebhookService struct {
	db          *sql.DB
	cfg         *config.Config
	queueClient *worker.QueueClient
	httpClient  *http.Client
}

// NewWebhookService creates a new webhook service
func NewWebhookService(db *sql.DB, cfg *config.Config) *WebhookService {
	queueClient, _ := worker.NewQueueClient(cfg)

	return &WebhookService{
		db:          db,
		cfg:         cfg,
		queueClient: queueClient,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// CreateWebhook creates a new webhook endpoint
func (s *WebhookService) CreateWebhook(ctx context.Context, orgID int64, req *model.CreateWebhookRequest) (*model.WebhookResponse, error) {
	webhookUUID := uuid.New().String()
	secret := generateWebhookSecret()

	var webhook model.WebhookResponse

	err := s.db.QueryRowContext(ctx, `
		INSERT INTO webhooks (uuid, org_id, name, url, secret, events, active, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, true, NOW())
		RETURNING id, uuid, name, url, events, active, created_at, updated_at
	`, webhookUUID, orgID, req.Name, req.URL, secret, pq.Array(req.Events)).Scan(
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
		WHERE org_id = $1
		ORDER BY created_at DESC
	`, orgID)
	if err != nil {
		return nil, fmt.Errorf("failed to list webhooks: %w", err)
	}
	defer rows.Close()

	var webhooks []*model.WebhookResponse
	for rows.Next() {
		var webhook model.WebhookResponse
		if err := rows.Scan(
			&webhook.ID, &webhook.UUID, &webhook.Name, &webhook.URL, pq.Array(&webhook.Events),
			&webhook.Active, &webhook.SuccessCount, &webhook.FailureCount,
			&webhook.LastTriggeredAt, &webhook.LastSuccessAt, &webhook.LastFailureAt,
			&webhook.CreatedAt, &webhook.UpdatedAt,
		); err != nil {
			continue
		}
		webhooks = append(webhooks, &webhook)
	}

	return webhooks, nil
}

// UpdateWebhook updates a webhook
func (s *WebhookService) UpdateWebhook(ctx context.Context, orgID int64, webhookUUID string, req *model.UpdateWebhookRequest) (*model.WebhookResponse, error) {
	updates := []string{}
	args := []interface{}{}
	argIndex := 1

	if req.Name != "" {
		updates = append(updates, fmt.Sprintf("name = $%d", argIndex))
		args = append(args, req.Name)
		argIndex++
	}
	if req.URL != "" {
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
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT wc.id, wc.event_type, wc.payload, wc.response_status, wc.response_body,
		       wc.response_time_ms, wc.status, wc.attempts, wc.error, wc.created_at, wc.completed_at
		FROM webhook_calls wc
		JOIN webhooks w ON wc.webhook_id = w.id
		WHERE w.uuid = $1 AND w.org_id = $2
		ORDER BY wc.created_at DESC
		LIMIT $3
	`, webhookUUID, orgID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get webhook calls: %w", err)
	}
	defer rows.Close()

	var calls []*model.WebhookCallResponse
	for rows.Next() {
		var call model.WebhookCallResponse
		var payload, responseBody, errorMsg sql.NullString
		var responseStatus, responseTimeMs sql.NullInt32
		var completedAt sql.NullTime

		if err := rows.Scan(
			&call.ID, &call.EventType, &payload, &responseStatus, &responseBody,
			&responseTimeMs, &call.Status, &call.Attempts, &errorMsg, &call.CreatedAt, &completedAt,
		); err != nil {
			continue
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

	return calls, nil
}

// DeliverWebhook delivers a webhook event
func (s *WebhookService) DeliverWebhook(ctx context.Context, webhookID int64, eventType string, payload map[string]interface{}) error {
	// Get webhook details
	var url, secret string
	var active bool
	err := s.db.QueryRowContext(ctx, `
		SELECT url, secret, active FROM webhooks WHERE id = $1
	`, webhookID).Scan(&url, &secret, &active)
	if err != nil {
		return fmt.Errorf("failed to get webhook: %w", err)
	}

	if !active {
		return nil // Skip inactive webhooks
	}

	// Create webhook call record
	payloadJSON, _ := json.Marshal(payload)
	var callID int64
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO webhook_calls (webhook_id, event_type, payload, status, attempts)
		VALUES ($1, $2, $3, 'pending', 0)
		RETURNING id
	`, webhookID, eventType, string(payloadJSON)).Scan(&callID)
	if err != nil {
		return fmt.Errorf("failed to create webhook call record: %w", err)
	}

	// Deliver the webhook
	go s.executeWebhookDelivery(context.Background(), callID, webhookID, url, secret, eventType, payload)

	return nil
}

// executeWebhookDelivery performs the actual webhook delivery with retries
func (s *WebhookService) executeWebhookDelivery(ctx context.Context, callID, webhookID int64, url, secret, eventType string, payload map[string]interface{}) {
	maxRetries := 5
	var lastError error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 5s, 30s, 2m, 10m, 30m
			delays := []time.Duration{
				5 * time.Second,
				30 * time.Second,
				2 * time.Minute,
				10 * time.Minute,
				30 * time.Minute,
			}
			delay := delays[attempt-1]
			time.Sleep(delay)
		}

		// Update attempt count
		s.db.ExecContext(ctx, `
			UPDATE webhook_calls SET attempts = $1 WHERE id = $2
		`, attempt+1, callID)

		// Build and sign payload
		webhookPayload := map[string]interface{}{
			"type":       eventType,
			"created_at": time.Now().Unix(),
			"data":       payload,
		}

		payloadBytes, _ := json.Marshal(webhookPayload)
		timestamp := time.Now().Unix()
		signature := s.signPayload(payloadBytes, timestamp, secret)

		// Create request
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payloadBytes))
		if err != nil {
			lastError = err
			continue
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Webhook-Signature", signature)
		req.Header.Set("X-Webhook-Timestamp", fmt.Sprintf("%d", timestamp))
		req.Header.Set("User-Agent", "Mailat-Webhook/1.0")

		// Send request
		startTime := time.Now()
		resp, err := s.httpClient.Do(req)
		duration := time.Since(startTime)

		if err != nil {
			lastError = err
			s.recordWebhookAttempt(ctx, callID, 0, "", int(duration.Milliseconds()), err.Error())
			continue
		}

		// Read response
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		s.recordWebhookAttempt(ctx, callID, resp.StatusCode, string(body), int(duration.Milliseconds()), "")

		// Check if successful (2xx status)
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			// Success
			s.db.ExecContext(ctx, `
				UPDATE webhook_calls
				SET status = 'success', completed_at = NOW()
				WHERE id = $1
			`, callID)

			s.db.ExecContext(ctx, `
				UPDATE webhooks
				SET success_count = success_count + 1,
				    last_triggered_at = NOW(),
				    last_success_at = NOW()
				WHERE id = $1
			`, webhookID)

			return
		}

		lastError = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	// All retries exhausted
	s.db.ExecContext(ctx, `
		UPDATE webhook_calls
		SET status = 'failed', error = $2, completed_at = NOW()
		WHERE id = $1
	`, callID, lastError.Error())

	s.db.ExecContext(ctx, `
		UPDATE webhooks
		SET failure_count = failure_count + 1,
		    last_triggered_at = NOW(),
		    last_failure_at = NOW()
		WHERE id = $1
	`, webhookID)
}

// recordWebhookAttempt records a webhook delivery attempt
func (s *WebhookService) recordWebhookAttempt(ctx context.Context, callID int64, status int, body string, durationMs int, errMsg string) {
	s.db.ExecContext(ctx, `
		UPDATE webhook_calls
		SET response_status = $2, response_body = $3, response_time_ms = $4, error = $5
		WHERE id = $1
	`, callID, status, truncateString(body, 5000), durationMs, errMsg)
}

// signPayload creates a HMAC-SHA256 signature for the webhook payload
func (s *WebhookService) signPayload(payload []byte, timestamp int64, secret string) string {
	// Stripe-style signature: t=timestamp,v1=signature
	signedPayload := fmt.Sprintf("%d.%s", timestamp, string(payload))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedPayload))
	signature := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("t=%d,v1=%s", timestamp, signature)
}

// VerifySignature verifies a webhook signature (for external use)
func VerifySignature(payload []byte, signature string, secret string, tolerance time.Duration) bool {
	// Parse signature
	var timestamp int64
	var v1Sig string

	parts := splitStringByChar(signature, ',')
	for _, part := range parts {
		kv := splitStringByChar(part, '=')
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			fmt.Sscanf(kv[1], "%d", &timestamp)
		case "v1":
			v1Sig = kv[1]
		}
	}

	if timestamp == 0 || v1Sig == "" {
		return false
	}

	// Check timestamp tolerance (default 5 minutes)
	if tolerance == 0 {
		tolerance = 5 * time.Minute
	}
	if time.Since(time.Unix(timestamp, 0)) > tolerance {
		return false
	}

	// Compute expected signature
	signedPayload := fmt.Sprintf("%d.%s", timestamp, string(payload))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedPayload))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(v1Sig), []byte(expectedSig))
}

// TriggerEmailEvent triggers webhooks for an email event
func (s *WebhookService) TriggerEmailEvent(ctx context.Context, orgID, emailID int64, eventType string) error {
	// Get email details
	var emailUUID, from, subject, status string
	var toAddresses string
	var sentAt, deliveredAt sql.NullTime

	err := s.db.QueryRowContext(ctx, `
		SELECT uuid, from_address, to_addresses, subject, status, sent_at, delivered_at
		FROM transactional_emails
		WHERE id = $1 AND org_id = $2
	`, emailID, orgID).Scan(&emailUUID, &from, &toAddresses, &subject, &status, &sentAt, &deliveredAt)
	if err != nil {
		return fmt.Errorf("failed to get email: %w", err)
	}

	// Build event payload
	payload := map[string]interface{}{
		"email_id":   emailUUID,
		"from":       from,
		"to":         splitAddresses(toAddresses),
		"subject":    subject,
		"status":     status,
		"event_type": eventType,
		"timestamp":  time.Now().Unix(),
	}

	if sentAt.Valid {
		payload["sent_at"] = sentAt.Time.Unix()
	}
	if deliveredAt.Valid {
		payload["delivered_at"] = deliveredAt.Time.Unix()
	}

	// Find webhooks that are subscribed to this event
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM webhooks
		WHERE org_id = $1 AND active = true AND $2 = ANY(events)
	`, orgID, "email."+eventType)
	if err != nil {
		return fmt.Errorf("failed to query webhooks: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var webhookID int64
		if err := rows.Scan(&webhookID); err != nil {
			continue
		}

		// Deliver webhook
		s.DeliverWebhook(ctx, webhookID, "email."+eventType, payload)
	}

	return nil
}

// Helper functions

func generateWebhookSecret() string {
	bytes := make([]byte, 32)
	rand.Read(bytes)
	return "whsec_" + hex.EncodeToString(bytes)
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

func joinStringsWithSep(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}

func splitStringByChar(s string, sep byte) []string {
	var result []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			result = append(result, s[start:i])
			start = i + 1
		}
	}
	result = append(result, s[start:])
	return result
}
