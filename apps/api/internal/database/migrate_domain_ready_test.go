package database_test

import (
	"context"
	"testing"

	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/testutil"
)

// Domains that were already active (SES verified or not at that moment) or
// verified once before 018/019 must never get the one-time ready automation;
// never-verified pending ones still can. Stamps are the epoch, never recent.
func TestDomainReadyAutomationMigration(t *testing.T) {
	db := testutil.EmptyDatabase(t)
	ctx := context.Background()
	if err := database.MigrateThrough(ctx, db, "017_mailbox_users.sql"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Org','org',now());
		INSERT INTO domains(id,org_id,name,verification_token,status,ses_verified,updated_at) VALUES
			(1,1,'ready.test','t','active',true,now()),(2,1,'pending.test','t','pending',false,now()),(3,1,'smtp.test','t','active',false,now()),
			(4,1,'lapsed.test','t','active',false,now());
		UPDATE domains SET email_provider='ses',verified_at=now()-interval '30 days' WHERE id=4;
		INSERT INTO domains(id,org_id,name,verification_token,status,ses_verified,verified_at,updated_at) VALUES
			(5,1,'was-active.test','t','pending',false,now()-interval '30 days',now());`); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal("rerun:", err)
	}
	rows, err := db.Query(`SELECT id,ready_automation_at IS NOT NULL,created_by IS NULL,COALESCE(ready_automation_at=timestamptz 'epoch',true) FROM domains ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	// 4 is a long-active SES domain whose last SES check failed (ses_verified
	// false); 5 was verified once and later fell back to pending.
	want := map[int64]bool{1: true, 2: false, 3: true, 4: true, 5: true}
	for rows.Next() {
		var id int64
		var stamped, noCreator, epoch bool
		if err := rows.Scan(&id, &stamped, &noCreator, &epoch); err != nil {
			t.Fatal(err)
		}
		// A recent stamp would make the checklist report the setup as running.
		if !epoch {
			t.Fatal("domain", id, "stamped with a recent time")
		}
		if stamped != want[id] || !noCreator {
			t.Fatal("domain", id, "stamped", stamped, "creator unknown", noCreator)
		}
	}
}
