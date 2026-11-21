package database_test

import (
	"context"
	"testing"

	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/testutil"
)

// Sessions from before 022 never passed a PIN step: the migration ends them
// (active=false, revoked_at set) so every user chooses a PIN at the next
// sign-in. Already revoked sessions keep their original revoked_at.
func TestLoginPinMigrationRevokesSessions(t *testing.T) {
	db := testutil.EmptyDatabase(t)
	ctx := context.Background()
	if err := database.MigrateThrough(ctx, db, "021_inbox_scope_indexes.sql"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Org','org',now());
		INSERT INTO users(id,org_id,email,password_hash,name,role,updated_at) VALUES(1,1,'a@org.test','x','A','owner',now()),(2,1,'b@org.test','x','B','member',now());
		INSERT INTO user_sessions(user_id,org_id,token_hash,expires_at) VALUES(1,1,'s1',now()+interval '7 days'),(2,1,'s2',now()+interval '7 days');
		INSERT INTO user_sessions(user_id,org_id,token_hash,expires_at,active,revoked_at) VALUES(2,1,'s3',now()+interval '7 days',false,'2026-01-01');`); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var active, unstamped, oldKept int
	if err := db.QueryRow(`SELECT count(*) FILTER (WHERE active), count(*) FILTER (WHERE revoked_at IS NULL),
		count(*) FILTER (WHERE token_hash='s3' AND revoked_at='2026-01-01') FROM user_sessions`).Scan(&active, &unstamped, &oldKept); err != nil {
		t.Fatal(err)
	}
	if active != 0 || unstamped != 0 || oldKept != 1 {
		t.Fatalf("active=%d without revoked_at=%d old revoked kept=%d", active, unstamped, oldKept)
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal("rerun:", err)
	}
}
