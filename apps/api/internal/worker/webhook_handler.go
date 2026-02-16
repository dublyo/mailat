package worker

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hibiken/asynq"

	"github.com/dublyo/mailat/api/internal/config"
)

// WebhookHandler handles webhook delivery tasks
type WebhookHandler struct {
	db     *sql.DB
	cfg    *config.Config
	client *http.Client
}

// NewWebhookHandler creates a new webhook handler
func NewWebhookHandler(db *sql.DB, cfg *config.Config) *WebhookHandler {
	return &WebhookHandler{
		db:  db,
		cfg: cfg,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// HandleWebhookDeliver handles a webhook delivery task
func (h *WebhookHandler) HandleWebhookDeliver(ctx context.Context, task *asynq.Task) error {
	payload, err := UnmarshalWebhookDeliverPayload(task.Payload())
	if err != nil {
		return fmt.Errorf("failed to unmarshal payload: %w", err)
	}

	// Build webhook payload
	webhookPayload := map[string]interface{}{
		"event":     payload.EventType,
		"emailId":   payload.EmailID,
		"orgId":     payload.OrgID,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"data":      payload.Payload,
	}

	jsonPayload, err := json.Marshal(webhookPayload)
	if err != nil {
		return fmt.Errorf("failed to marshal webhook payload: %w", err)
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, "POST", payload.URL, bytes.NewReader(jsonPayload))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mailat-Webhook/1.0")

	// Add HMAC signature if secret is provided
	if payload.Secret != "" {
		timestamp := fmt.Sprintf("%d", time.Now().Unix())
		signature := h.computeSignature(timestamp, jsonPayload, payload.Secret)
		req.Header.Set("X-Webhook-Timestamp", timestamp)
		req.Header.Set("X-Webhook-Signature", signature)
	}

	// Send request
	startTime := time.Now()
	resp, err := h.client.Do(req)
	duration := time.Since(startTime)

	// Record the webhook call
	callStatus := "success"
	callStatusCode := 0
	callError := ""
	var responseBody string

	if err != nil {
		callStatus = "error"
		callError = err.Error()
	} else {
		callStatusCode = resp.StatusCode
		if resp.StatusCode >= 400 {
			callStatus = "failed"
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			responseBody = string(body)
			callError = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, responseBody)
		}
		resp.Body.Close()
	}

	// Store webhook call record
	h.recordWebhookCall(ctx, payload.WebhookID, payload.EventType, payload.EmailID,
		callStatus, callStatusCode, callError, duration, payload.RetryCount)

	// Return error to trigger retry if needed
	if callStatus != "success" {
		if payload.RetryCount >= payload.MaxRetries {
			// Max retries exceeded, don't retry
			fmt.Printf("Webhook delivery to %s failed after %d retries: %s\n",
				payload.URL, payload.RetryCount, callError)
			return nil
		}
		return fmt.Errorf("webhook delivery failed: %s", callError)
	}

	return nil
}

// computeSignature creates an HMAC-SHA256 signature for webhook verification
func (h *WebhookHandler) computeSignature(timestamp string, payload []byte, secret string) string {
	// Format: timestamp.payload
	signedPayload := fmt.Sprintf("%s.%s", timestamp, string(payload))

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedPayload))
	return hex.EncodeToString(mac.Sum(nil))
}

// recordWebhookCall stores a webhook call record
func (h *WebhookHandler) recordWebhookCall(ctx context.Context, webhookID int64, eventType string,
	emailID int64, status string, statusCode int, errorMsg string, duration time.Duration, attempt int) {

	// Build payload JSON with email ID
	payloadJSON, _ := json.Marshal(map[string]any{"emailId": emailID})

	// Insert using schema-aligned column names
	h.db.ExecContext(ctx, `
		INSERT INTO webhook_calls (webhook_id, event_type, payload, response_status,
			response_time_ms, status, attempts, error, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
	`, webhookID, eventType, payloadJSON, statusCode, duration.Milliseconds(), status, attempt+1, errorMsg)
}

// BounceHandler handles bounce processing tasks
type BounceHandler struct {
	db  *sql.DB
	cfg *config.Config
}

// NewBounceHandler creates a new bounce handler
func NewBounceHandler(db *sql.DB, cfg *config.Config) *BounceHandler {
	return &BounceHandler{db: db, cfg: cfg}
}

// HandleBounceProcess handles a bounce processing task
func (h *BounceHandler) HandleBounceProcess(ctx context.Context, task *asynq.Task) error {
	payload, err := UnmarshalBounceProcessPayload(task.Payload())
	if err != nil {
		return fmt.Errorf("failed to unmarshal payload: %w", err)
	}

	// Update email status
	_, err = h.db.ExecContext(ctx, `
		UPDATE emails SET status = 'bounced', updated_at = NOW()
		WHERE id = $1
	`, payload.EmailID)
	if err != nil {
		return fmt.Errorf("failed to update email status: %w", err)
	}

	// Record bounce event
	eventData, _ := json.Marshal(map[string]string{
		"bounceType":   payload.BounceType,
		"bounceReason": payload.BounceReason,
		"recipient":    payload.Recipient,
	})

	h.db.ExecContext(ctx, `
		INSERT INTO delivery_events (email_id, event_type, data, occurred_at)
		VALUES ($1, 'bounced', $2, NOW())
	`, payload.EmailID, eventData)

	// For hard bounces, add to suppression list
	if payload.BounceType == "hard" {
		_, err = h.db.ExecContext(ctx, `
			INSERT INTO suppressions (org_id, email, reason, source_type, source_id, created_at)
			VALUES ($1, $2, 'hard_bounce', 'email', $3, NOW())
			ON CONFLICT (org_id, email) DO NOTHING
		`, payload.OrgID, payload.Recipient, fmt.Sprintf("%d", payload.EmailID))
		if err != nil {
			fmt.Printf("Failed to add to suppression list: %v\n", err)
		}

		// Update contact status if exists
		h.db.ExecContext(ctx, `
			UPDATE contacts SET status = 'bounced', updated_at = NOW()
			WHERE org_id = $1 AND email = $2
		`, payload.OrgID, payload.Recipient)
	}

	// Trigger webhooks for bounce event
	h.triggerBounceWebhooks(ctx, payload)

	return nil
}

// triggerBounceWebhooks enqueues webhook deliveries for bounce events
func (h *BounceHandler) triggerBounceWebhooks(ctx context.Context, payload *BounceProcessPayload) {
	// Get active webhooks for this org that listen to bounce events
	rows, err := h.db.QueryContext(ctx, `
		SELECT id, url, secret FROM webhooks
		WHERE org_id = $1 AND active = true AND 'email.bounced' = ANY(events)
	`, payload.OrgID)
	if err != nil {
		return
	}
	defer rows.Close()

	// Create queue client
	queueClient, err := NewQueueClient(h.cfg)
	if err != nil {
		return
	}
	defer queueClient.Close()

	for rows.Next() {
		var webhookID int64
		var url, secret string
		if err := rows.Scan(&webhookID, &url, &secret); err != nil {
			continue
		}

		webhookPayload := &WebhookDeliverPayload{
			WebhookID:  webhookID,
			OrgID:      payload.OrgID,
			URL:        url,
			Secret:     secret,
			EventType:  "email.bounced",
			EmailID:    payload.EmailID,
			Payload: map[string]any{
				"bounceType":   payload.BounceType,
				"bounceReason": payload.BounceReason,
				"recipient":    payload.Recipient,
			},
			MaxRetries: 5,
		}

		queueClient.EnqueueWebhookDeliver(webhookPayload)
	}
}
