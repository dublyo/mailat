package service

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dublyo/mailat/api/internal/config"
)

// reserveMonthlySend enforces only an explicit operator quota. A single SQL
// statement reserves an attempt atomically, with a calendar-month key rather
// than a sliding Redis expiration or an invented daily fraction.
func reserveMonthlySend(ctx context.Context, db *sql.DB, cfg *config.Config, orgID int64) error {
	if cfg.DisableAppLimits {
		return nil
	}
	var limit int64
	if err := db.QueryRowContext(ctx, `SELECT monthly_email_limit FROM organizations WHERE id=$1`, orgID).Scan(&limit); err != nil {
		return fmt.Errorf("load send quota: %w", err)
	}
	if limit <= 0 {
		return nil
	}
	var attempts int64
	err := db.QueryRowContext(ctx, `INSERT INTO organization_send_usage (org_id,month,attempts)
		VALUES ($1,date_trunc('month',now() AT TIME ZONE 'UTC')::date,1)
		ON CONFLICT (org_id,month) DO UPDATE SET attempts=organization_send_usage.attempts+1
		WHERE organization_send_usage.attempts < $2 RETURNING attempts`, orgID, limit).Scan(&attempts)
	if err == sql.ErrNoRows {
		return fmt.Errorf("monthly application send quota exceeded")
	}
	if err != nil {
		return fmt.Errorf("reserve send quota: %w", err)
	}
	return nil
}
