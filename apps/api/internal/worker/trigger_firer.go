package worker

import (
	"context"
	"database/sql"
	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/eventoutbox"
)

type WebhookTriggerFirer struct{ db *sql.DB }

func NewWebhookTriggerFirer(db *sql.DB, cfg *config.Config) *WebhookTriggerFirer {
	return &WebhookTriggerFirer{db: db}
}
func (f *WebhookTriggerFirer) Fire(ctx context.Context, orgID int64, kind string, data map[string]interface{}) error {
	return eventoutbox.EmitLegacy(ctx, f.db, orgID, kind, data)
}
