package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/dublyo/mailat/api/internal/eventoutbox"
)

// automationRefInUse refuses deleting a list or template that a live (active
// or paused) automation references, in its published version or in a version
// that still has active enrollments.
func automationRefInUse(ctx context.Context, q eventoutbox.DBTX, orgID int64, ref string) error {
	u, err := uuid.Parse(ref)
	if err != nil {
		return nil
	}
	var name string
	err = q.QueryRowContext(ctx, `SELECT a.name FROM automations a
		WHERE a.org_id = $1 AND a.status IN ('active','paused') AND EXISTS (
			SELECT 1 FROM automation_versions v WHERE v.automation_id = a.id AND $2 = ANY(v.resource_refs)
			  AND (v.id = a.published_version_id
			       OR EXISTS (SELECT 1 FROM automation_enrollments e WHERE e.version_id = v.id AND e.status = 'active')))
		ORDER BY a.id LIMIT 1`, orgID, u.String()).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to check automation usage: %w", err)
	}
	return automationErr("Used by automation “%s”; archive it first", name)
}
