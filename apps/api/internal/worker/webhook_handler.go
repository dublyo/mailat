package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/eventoutbox"
	"github.com/hibiken/asynq"
)

type WebhookHandler struct {
	db  *sql.DB
	cfg *config.Config
}

func NewWebhookHandler(db *sql.DB, cfg *config.Config) *WebhookHandler {
	return &WebhookHandler{db: db, cfg: cfg}
}

// Old Redis tasks are durably adopted; URL and secret are read from the current endpoint.
func (h *WebhookHandler) HandleWebhookDeliver(ctx context.Context, task *asynq.Task) error {
	p, err := UnmarshalWebhookDeliverPayload(task.Payload())
	if err != nil {
		return err
	}
	var user int64
	if err = h.db.QueryRowContext(ctx, `SELECT COALESCE(user_id,0) FROM webhooks WHERE id=$1 AND org_id=$2`, p.WebhookID, p.OrgID).Scan(&user); err == sql.ErrNoRows {
		return nil
	} else if err != nil {
		return err
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, _, err = eventoutbox.QueueTarget(ctx, tx, eventoutbox.Event{Type: p.EventType, OrgID: p.OrgID, UserID: user, DedupeKey: fmt.Sprintf("legacy:%d:%d:%s", p.WebhookID, p.EmailID, p.EventType), Data: p.Payload}, p.WebhookID, 0)
	if err != nil {
		return err
	}
	return tx.Commit()
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
			WebhookID: webhookID,
			OrgID:     payload.OrgID,
			URL:       url,
			Secret:    secret,
			EventType: "email.bounced",
			EmailID:   payload.EmailID,
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
