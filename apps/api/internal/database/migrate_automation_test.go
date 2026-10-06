package database_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/testutil"
)

func TestAutomationExecutorMigration(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "fresh"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			db := testutil.EmptyDatabase(t)
			ctx := context.Background()
			seed := `INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Org','org',now()),(2,'Other','other',now());
				INSERT INTO users(id,org_id,email,password_hash,updated_at) VALUES(1,1,'owner@example.test','unused',now());
				INSERT INTO lists(id,org_id,name,updated_at) VALUES(1,1,'Readers',now()),(2,2,'Foreign',now());
				INSERT INTO contacts(id,org_id,email,updated_at) VALUES(1,1,'a@example.test',now()),(2,1,'b@example.test',now())`
			if legacy {
				if _, err := db.Exec(legacySchema); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(seed); err != nil {
					t.Fatal(err)
				}
				// Pre-M4 rows: nothing ever ran, but statuses claimed otherwise.
				if _, err := db.Exec(`INSERT INTO automations(id,org_id,name,trigger_type,status) VALUES
					(1,1,'active','contact_added','active'),
					(2,1,'null','contact.subscribed',NULL),
					(3,1,'weird','subscribed','running'),
					(4,1,'archived','contact.created','archived'),
					(5,1,'draft','manual','draft'),
					(6,99,'orphan','manual','draft');
					INSERT INTO automation_enrollments(automation_id,contact_id,org_id,status) VALUES
					(1,1,1,'active'),(2,1,1,'completed'),(3,1,1,NULL),(4,999,1,'completed')`); err != nil {
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
			if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM mailat_schema_migrations WHERE name='014_automation_executor.sql')`).Scan(&applied); err != nil || !applied {
				t.Fatalf("014 not recorded: %v", err)
			}
			if !legacy {
				if _, err := db.Exec(seed); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO automations(id,org_id,name,trigger_type) VALUES(1,1,'a','contact.created')`); err != nil {
					t.Fatal("insert without updated_at/status:", err)
				}
			}

			if legacy {
				for _, tc := range []struct{ name, status, trigger string }{
					{"active", "paused", "contact.created"},
					{"null", "draft", "contact.subscribed"},
					{"weird", "draft", "contact.subscribed"},
					{"archived", "archived", "contact.created"},
					{"draft", "draft", "manual"},
				} {
					var status, trigger, reentry string
					if err := db.QueryRow(`SELECT status,trigger_type,reentry_policy FROM automations WHERE name=$1`, tc.name).Scan(&status, &trigger, &reentry); err != nil {
						t.Fatal(tc.name, err)
					}
					if status != tc.status || trigger != tc.trigger || reentry != "never" {
						t.Errorf("%s: status=%q trigger=%q reentry=%q", tc.name, status, trigger, reentry)
					}
				}
				count(t, db, 0, `SELECT count(*) FROM automations WHERE name='orphan'`)
				count(t, db, 0, `SELECT count(*) FROM automation_enrollments WHERE contact_id=999`)
				count(t, db, 2, `SELECT count(*) FROM automation_enrollments WHERE status='cancelled' AND error_message IS NOT NULL`)
				count(t, db, 1, `SELECT count(*) FROM automation_enrollments WHERE status='completed'`)
			}

			// Constraints.
			if _, err := db.Exec(`UPDATE automations SET status='running' WHERE id=1`); err == nil {
				t.Fatal("unknown automation status accepted")
			}
			if _, err := db.Exec(`UPDATE automations SET reentry_policy='always' WHERE id=1`); err == nil {
				t.Fatal("unknown reentry policy accepted")
			}
			if _, err := db.Exec(`INSERT INTO automation_enrollments(automation_id,contact_id,org_id,status) VALUES(1,2,1,'active')`); err == nil {
				t.Fatal("unversioned active enrollment accepted")
			}
			// The old one-row-per-contact rule is gone: history may repeat.
			if _, err := db.Exec(`INSERT INTO automation_enrollments(automation_id,contact_id,org_id,status) VALUES(1,2,1,'completed'),(1,2,1,'exited')`); err != nil {
				t.Fatal("repeat non-active enrollments:", err)
			}
			if _, err := db.Exec(`INSERT INTO automation_enrollments(automation_id,contact_id,org_id,status) VALUES(1,999,1,'completed')`); err == nil {
				t.Fatal("enrollment for missing contact accepted")
			}

			// No trigger events without an active published automation.
			if _, err := db.Exec(`INSERT INTO list_contacts(list_id,contact_id,source) VALUES(1,1,'signup_form')`); err != nil {
				t.Fatal(err)
			}
			count(t, db, 0, `SELECT count(*) FROM automation_trigger_events`)

			// Publish v1 of a contact.subscribed automation and activate it.
			var versionID int64
			if err := db.QueryRow(`INSERT INTO automation_versions(automation_id,version,trigger_type,workflow,graph_hash,published_by)
				VALUES(1,1,'contact.subscribed','{"nodes":[],"edges":[]}','h',1) RETURNING id`).Scan(&versionID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO automation_versions(automation_id,version,trigger_type,workflow,graph_hash) VALUES(1,1,'manual','{}','h')`); err == nil {
				t.Fatal("duplicate version number accepted")
			}
			if _, err := db.Exec(`INSERT INTO automation_versions(automation_id,version,trigger_type,workflow,graph_hash,reentry_policy) VALUES(1,2,'manual','{}','h','always')`); err == nil {
				t.Fatal("unknown version reentry policy accepted")
			}
			if _, err := db.Exec(`UPDATE automations SET status='active',published_version_id=$1,activated_at=now(),trigger_type='contact.subscribed' WHERE id=1`, versionID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO list_contacts(list_id,contact_id,source) VALUES(1,2,'import')`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO list_contacts(list_id,contact_id,source) VALUES(1,2,'import') ON CONFLICT DO NOTHING`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`DELETE FROM list_contacts WHERE list_id=1 AND contact_id=1`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO list_contacts(list_id,contact_id,source) VALUES(1,1,'a value no trigger knows')`); err != nil {
				t.Fatal(err)
			}
			// A list from another org never records an event for this contact.
			if _, err := db.Exec(`INSERT INTO list_contacts(list_id,contact_id) VALUES(2,1)`); err != nil {
				t.Fatal(err)
			}
			rows, err := db.Query(`SELECT contact_id,list_id,source FROM automation_trigger_events WHERE event_type='contact.subscribed' ORDER BY id`)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for rows.Next() {
				var c, l int64
				var s string
				if err := rows.Scan(&c, &l, &s); err != nil {
					t.Fatal(err)
				}
				got = append(got, s)
				if l != 1 {
					t.Errorf("event for list %d", l)
				}
			}
			rows.Close()
			if len(got) != 2 || got[0] != "import" || got[1] != "unknown" {
				t.Fatalf("subscribed events: %v", got)
			}
			count(t, db, 0, `SELECT count(*) FROM automation_trigger_events WHERE event_type='contact.created'`)

			// contact.created reads created_source, not free-text consent_source.
			if _, err := db.Exec(`UPDATE automation_versions SET trigger_type='contact.created' WHERE id=$1`, versionID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO contacts(id,org_id,email,consent_source,created_source,updated_at) VALUES
				(3,1,'c@example.test','import','api',now()),(4,1,'d@example.test',repeat('x',100),NULL,now()),(5,2,'e@example.test',NULL,'api',now())`); err != nil {
				t.Fatal(err)
			}
			count(t, db, 1, `SELECT count(*) FROM automation_trigger_events WHERE event_type='contact.created' AND contact_id=3 AND source='api' AND org_id=1`)
			count(t, db, 1, `SELECT count(*) FROM automation_trigger_events WHERE event_type='contact.created' AND contact_id=4 AND source='unknown'`)
			count(t, db, 0, `SELECT count(*) FROM automation_trigger_events WHERE contact_id=5`)

			// Paused automations record nothing.
			if _, err := db.Exec(`UPDATE automations SET status='paused' WHERE id=1`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO contacts(id,org_id,email,created_source,updated_at) VALUES(6,1,'f@example.test','api',now())`); err != nil {
				t.Fatal(err)
			}
			count(t, db, 0, `SELECT count(*) FROM automation_trigger_events WHERE contact_id=6`)

			// One active enrollment per contact; step runs and messages are unique per node.
			var enr int64
			if err := db.QueryRow(`INSERT INTO automation_enrollments(automation_id,contact_id,org_id,status,version_id,current_node_id,next_run_at)
				VALUES(1,3,1,'active',$1,'email-1',now()) RETURNING id`, versionID).Scan(&enr); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO automation_enrollments(automation_id,contact_id,org_id,status,version_id,current_node_id) VALUES(1,3,1,'active',$1,'x')`, versionID); err == nil {
				t.Fatal("second active enrollment accepted")
			}
			var msg int64
			if err := db.QueryRow(`INSERT INTO automation_messages(org_id,automation_id,version_id,enrollment_id,node_id,contact_id,email)
				VALUES(1,1,$1,$2,'email-1',3,'c@example.test') RETURNING id`, versionID, enr).Scan(&msg); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO automation_messages(org_id,automation_id,version_id,enrollment_id,node_id,contact_id,email)
				VALUES(1,1,$1,$2,'email-1',3,'c@example.test')`, versionID, enr); err == nil {
				t.Fatal("duplicate message per enrollment node accepted")
			}
			if _, err := db.Exec(`UPDATE automation_messages SET status='queued' WHERE id=$1`, msg); err == nil {
				t.Fatal("unknown message status accepted")
			}
			if _, err := db.Exec(`INSERT INTO automation_message_events(message_id,automation_id,event_type) VALUES($1,1,'open')`, msg); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO automation_step_runs(enrollment_id,automation_id,version_id,node_id,node_type,status,message_id)
				VALUES($1,1,$2,'email-1','email','succeeded',$3)`, enr, versionID, msg); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO automation_step_runs(enrollment_id,automation_id,version_id,node_id,node_type,status)
				VALUES($1,1,$2,'email-1','email','failed')`, enr, versionID); err == nil {
				t.Fatal("duplicate step run accepted")
			}

			// Erasure: deleting the contact removes every automation trace.
			if _, err := db.Exec(`DELETE FROM contacts WHERE id=3`); err != nil {
				t.Fatal(err)
			}
			count(t, db, 0, `SELECT (SELECT count(*) FROM automation_enrollments WHERE contact_id=3)+(SELECT count(*) FROM automation_step_runs)
				+(SELECT count(*) FROM automation_messages)+(SELECT count(*) FROM automation_message_events)
				+(SELECT count(*) FROM automation_trigger_events WHERE contact_id=3)`)

			// Deleting the automation cascades to versions and history.
			if _, err := db.Exec(`DELETE FROM automations WHERE id=1`); err != nil {
				t.Fatal(err)
			}
			count(t, db, 0, `SELECT (SELECT count(*) FROM automation_versions)+(SELECT count(*) FROM automation_enrollments WHERE automation_id=1)`)
		})
	}
}

func count(t *testing.T, db *sql.DB, want int, query string, args ...any) {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(query, err)
	}
	if n != want {
		t.Errorf("%s = %d, want %d", query, n, want)
	}
}
