package database_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/testutil"
)

func TestCampaignSendingMigration(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "fresh"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			db := testutil.EmptyDatabase(t)
			ctx := context.Background()
			seed := `INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Org','org',now());
				INSERT INTO users(id,org_id,email,password_hash,updated_at) VALUES(1,1,'owner@example.test','unused',now());
				INSERT INTO lists(id,org_id,name,updated_at) VALUES(1,1,'Readers',now());
				INSERT INTO contacts(id,org_id,email,updated_at) VALUES(1,1,'a@example.test',now()),(2,1,'b@example.test',now())`
			if legacy {
				if _, err := db.Exec(legacySchema); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(seed); err != nil {
					t.Fatal(err)
				}
				// Pre-M3 rows: nothing ever sent, but statuses claimed otherwise.
				if _, err := db.Exec(`INSERT INTO campaigns(org_id,name,subject,from_name,from_email,list_id,status,scheduled_at,started_at,total_recipients) VALUES
					(1,'sending','s','n','f@example.test',1,'sending',NULL,now(),40),
					(1,'scheduled','s','n','f@example.test',1,'scheduled',now()+interval '1 day',NULL,0),
					(1,'paused','s','n','f@example.test',1,'paused',NULL,now(),7),
					(1,'null','s','n','f@example.test',1,NULL,NULL,NULL,0),
					(1,'weird','s','n','f@example.test',1,'completed',NULL,NULL,0),
					(1,'draft','s','n','f@example.test',1,'draft',NULL,NULL,0),
					(1,'sent','s','n','f@example.test',1,'sent',NULL,now(),12),
					(1,'cancelled','s','n','f@example.test',1,'cancelled',NULL,NULL,0)`); err != nil {
					t.Fatal(err)
				}
			}
			if err := database.Migrate(ctx, db); err != nil {
				t.Fatal(err)
			}
			if err := database.Migrate(ctx, db); err != nil {
				t.Fatal("rerun:", err)
			}
			var applied bool
			if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM mailat_schema_migrations WHERE name='013_campaign_sending.sql')`).Scan(&applied); err != nil || !applied {
				t.Fatalf("013 not recorded: %v", err)
			}
			if !legacy {
				if _, err := db.Exec(seed); err != nil {
					t.Fatal(err)
				}
			}

			if legacy {
				for _, tc := range []struct{ name, status, reason string }{
					{"sending", "draft", "legacy_requires_review"},
					{"scheduled", "draft", "legacy_requires_review"},
					{"paused", "draft", "legacy_requires_review"},
					{"null", "draft", "legacy_requires_review"},
					{"weird", "draft", "legacy_requires_review"},
					{"draft", "draft", ""},
					{"sent", "sent", ""},
					{"cancelled", "cancelled", ""},
				} {
					var status string
					var reason sql.NullString
					var scheduled, started sql.NullTime
					var total int
					if err := db.QueryRow(`SELECT status,status_reason,scheduled_at,started_at,total_recipients FROM campaigns WHERE name=$1`, tc.name).
						Scan(&status, &reason, &scheduled, &started, &total); err != nil {
						t.Fatal(tc.name, err)
					}
					if status != tc.status || reason.String != tc.reason {
						t.Errorf("%s: status=%q reason=%q", tc.name, status, reason.String)
					}
					if tc.reason != "" && (scheduled.Valid || started.Valid || total != 0) {
						t.Errorf("%s: legacy reset kept schedule/start/total", tc.name)
					}
				}
				var sentTotal int
				if err := db.QueryRow(`SELECT total_recipients FROM campaigns WHERE name='sent'`).Scan(&sentTotal); err != nil || sentTotal != 12 {
					t.Errorf("sent row changed: %d %v", sentTotal, err)
				}
			}

			// New columns, defaults and the status constraint.
			var id int64
			var status string
			var opens, clicks bool
			if err := db.QueryRow(`INSERT INTO campaigns(org_id,name,subject,from_name,from_email,list_id,created_by_user_id,updated_at)
				VALUES(1,'new','s','n','f@example.test',1,1,now()) RETURNING id,status,track_opens,track_clicks`).Scan(&id, &status, &opens, &clicks); err != nil {
				t.Fatal(err)
			}
			if status != "draft" || !opens || !clicks {
				t.Fatalf("defaults: status=%q opens=%v clicks=%v", status, opens, clicks)
			}
			if _, err := db.Exec(`UPDATE campaigns SET status='completed' WHERE id=$1`, id); err == nil {
				t.Fatal("unknown status accepted")
			}
			if _, err := db.Exec(`UPDATE campaigns SET status=NULL WHERE id=$1`, id); err == nil {
				t.Fatal("NULL status accepted")
			}
			if _, err := db.Exec(`UPDATE organizations SET postal_address='1 Main St' WHERE id=1`); err != nil {
				t.Fatal(err)
			}

			// Recipients: one row per contact and per case-insensitive address.
			if _, err := db.Exec(`INSERT INTO campaign_recipients(campaign_id,org_id,contact_id,email) VALUES($1,1,1,'a@example.test'),($1,1,2,'b@example.test')`, id); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO campaign_recipients(campaign_id,org_id,contact_id,email) VALUES($1,1,NULL,'A@Example.TEST')`, id); err == nil {
				t.Fatal("case-variant duplicate accepted")
			}
			var rcpt int64
			var rstatus string
			var reserved bool
			if err := db.QueryRow(`SELECT id,status,quota_reserved FROM campaign_recipients WHERE campaign_id=$1 AND contact_id=1`, id).Scan(&rcpt, &rstatus, &reserved); err != nil || rstatus != "pending" || reserved {
				t.Fatalf("recipient defaults: %q %v %v", rstatus, reserved, err)
			}
			if _, err := db.Exec(`UPDATE campaign_recipients SET status='queued' WHERE id=$1`, rcpt); err == nil {
				t.Fatal("unknown recipient status accepted")
			}
			if _, err := db.Exec(`INSERT INTO campaign_events(campaign_id,recipient_id,event_type) VALUES($1,$2,'open')`, id, rcpt); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO campaign_test_sends(campaign_id,user_id,idempotency_key,request_hash,recipients) VALUES($1,1,'key-12345','h',ARRAY['t@example.test'])`, id); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO campaign_test_sends(campaign_id,user_id,idempotency_key,request_hash,recipients) VALUES($1,1,'key-12345','h2',ARRAY['t@example.test'])`, id); err == nil {
				t.Fatal("duplicate idempotency key accepted")
			}
			// Deleting the contact keeps the snapshot row; deleting the campaign cascades.
			if _, err := db.Exec(`DELETE FROM contacts WHERE id=1`); err != nil {
				t.Fatal(err)
			}
			var contact sql.NullInt64
			if err := db.QueryRow(`SELECT contact_id FROM campaign_recipients WHERE id=$1`, rcpt).Scan(&contact); err != nil || contact.Valid {
				t.Fatalf("contact delete: %v %v", contact, err)
			}
			if _, err := db.Exec(`DELETE FROM campaigns WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
			var left int
			if err := db.QueryRow(`SELECT (SELECT count(*) FROM campaign_recipients)+(SELECT count(*) FROM campaign_events)+(SELECT count(*) FROM campaign_test_sends)`).Scan(&left); err != nil || left != 0 {
				t.Fatalf("campaign delete left %d rows: %v", left, err)
			}
		})
	}
}
