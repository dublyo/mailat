package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/eventoutbox"
)

const monthlyUsageMonth = `date_trunc('month',now() AT TIME ZONE 'UTC')::date`

// reserveMonthlySend enforces only an explicit operator quota, keyed by
// calendar month rather than a sliding Redis expiration or a daily fraction.
// ErrMonthlySendQuota means the organization's monthly send limit is used up.
var ErrMonthlySendQuota = errors.New("monthly application send quota exceeded")

func reserveMonthlySend(ctx context.Context, db *sql.DB, cfg *config.Config, orgID int64) error {
	granted, err := reserveMonthlySends(ctx, db, cfg, orgID, 1)
	if err != nil {
		return err
	}
	if granted == 0 {
		return ErrMonthlySendQuota
	}
	return nil
}

// reserveMonthlySends reserves up to n sends in its own transaction.
func reserveMonthlySends(ctx context.Context, db *sql.DB, cfg *config.Config, orgID, n int64) (int64, error) {
	if cfg.DisableAppLimits || n <= 0 {
		return max(n, 0), nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("reserve send quota: %w", err)
	}
	defer tx.Rollback()
	granted, err := reserveMonthlySendsTx(ctx, tx, cfg, orgID, n)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, fmt.Errorf("reserve send quota: %w", err)
	}
	return granted, nil
}

// reserveMonthlySendsTx grants min(n, remaining) sends. The usage row is locked
// until q commits, so concurrent reservations never exceed the limit; callers
// must be inside a transaction. Without a limit every send is granted.
func reserveMonthlySendsTx(ctx context.Context, q eventoutbox.DBTX, cfg *config.Config, orgID, n int64) (int64, error) {
	if cfg.DisableAppLimits || n <= 0 {
		return max(n, 0), nil
	}
	var limit int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(monthly_email_limit,0) FROM organizations WHERE id=$1`, orgID).Scan(&limit); err != nil {
		return 0, fmt.Errorf("load send quota: %w", err)
	}
	if limit <= 0 {
		return n, nil
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO organization_send_usage (org_id,month,attempts) VALUES ($1,`+monthlyUsageMonth+`,0)
		ON CONFLICT (org_id,month) DO NOTHING`, orgID); err != nil {
		return 0, fmt.Errorf("reserve send quota: %w", err)
	}
	var attempts int64
	if err := q.QueryRowContext(ctx, `SELECT attempts FROM organization_send_usage WHERE org_id=$1 AND month=`+monthlyUsageMonth+` FOR UPDATE`, orgID).Scan(&attempts); err != nil {
		return 0, fmt.Errorf("reserve send quota: %w", err)
	}
	granted := min(n, max(limit-attempts, 0))
	if granted == 0 {
		return 0, nil
	}
	if _, err := q.ExecContext(ctx, `UPDATE organization_send_usage SET attempts=attempts+$2 WHERE org_id=$1 AND month=`+monthlyUsageMonth, orgID, granted); err != nil {
		return 0, fmt.Errorf("reserve send quota: %w", err)
	}
	return granted, nil
}

// refundMonthlySendsTx returns n reserved but never attempted sends to the
// current month, never below zero.
func refundMonthlySendsTx(ctx context.Context, q eventoutbox.DBTX, orgID, n int64) error {
	if n <= 0 {
		return nil
	}
	if _, err := q.ExecContext(ctx, `UPDATE organization_send_usage SET attempts=GREATEST(attempts-$2,0) WHERE org_id=$1 AND month=`+monthlyUsageMonth, orgID, n); err != nil {
		return fmt.Errorf("refund send quota: %w", err)
	}
	return nil
}
