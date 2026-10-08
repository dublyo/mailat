package database_test

import (
	"context"
	"testing"

	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/testutil"
)

// Domains that were already active and SES verified before 018 must never get
// the one-time ready automation; pending ones still can.
func TestDomainReadyAutomationMigration(t *testing.T) {
	db := testutil.EmptyDatabase(t)
	ctx := context.Background()
	if err := database.MigrateThrough(ctx, db, "017_mailbox_users.sql"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Org','org',now());
		INSERT INTO domains(id,org_id,name,verification_token,status,ses_verified,updated_at) VALUES
			(1,1,'ready.test','t','active',true,now()),(2,1,'pending.test','t','pending',false,now()),(3,1,'smtp.test','t','active',false,now());`); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal("rerun:", err)
	}
	rows, err := db.Query(`SELECT id,ready_automation_at IS NOT NULL,created_by IS NULL FROM domains ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := map[int64]bool{1: true, 2: false, 3: false}
	for rows.Next() {
		var id int64
		var stamped, noCreator bool
		if err := rows.Scan(&id, &stamped, &noCreator); err != nil {
			t.Fatal(err)
		}
		if stamped != want[id] || !noCreator {
			t.Fatal("domain", id, "stamped", stamped, "creator unknown", noCreator)
		}
	}
}
