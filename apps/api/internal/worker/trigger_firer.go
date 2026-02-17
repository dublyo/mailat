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
	"net/http"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
)

// WebhookTriggerFirer fires webhook triggers from the worker package
// without importing the service package (avoids circular imports).
type WebhookTriggerFirer struct {
	db         *sql.DB
	cfg        *config.Config
	httpClient *http.Client
}

// NewWebhookTriggerFirer creates a new trigger firer
func NewWebhookTriggerFirer(db *sql.DB, cfg *config.Config) *WebhookTriggerFirer {
	return &WebhookTriggerFirer{
		db:         db,
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Fire sends webhook trigger events to all matching active triggers
func (f *WebhookTriggerFirer) Fire(ctx context.Context, orgID int64, triggerType string, data map[string]interface{}) error {
	rows, err := f.db.QueryContext(ctx, `
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

		go f.sendWebhook(id, webhookURL, secret, triggerType, data)
	}

	return nil
}

func (f *WebhookTriggerFirer) sendWebhook(triggerID int, url, secret, event string, data map[string]interface{}) {
	payload := map[string]interface{}{
		"event":     event,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"data":      data,
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
	req.Header.Set("User-Agent", fmt.Sprintf("%s/1.0", f.cfg.AppName))
	req.Header.Set("X-Webhook-Event", event)

	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req.Header.Set("X-Webhook-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	f.db.ExecContext(context.Background(), `
		UPDATE webhook_triggers
		SET trigger_count = trigger_count + 1, last_triggered_at = NOW()
		WHERE id = $1
	`, triggerID)
}
