package database_test

import (
	"context"
	"testing"

	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/testutil"
)

func TestMailArrivalMigration(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "fresh"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			db := testutil.EmptyDatabase(t)
			ctx := context.Background()
			seed := `INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Org','org',now());
				INSERT INTO users(id,org_id,email,password_hash,updated_at) VALUES(1,1,'owner@arrival.test','unused',now());
				INSERT INTO domains(id,org_id,name,verification_token,updated_at) VALUES(1,1,'arrival.test','token',now());
				INSERT INTO identities(id,user_id,domain_id,email,updated_at) VALUES(1,1,1,'owner@arrival.test',now());
				INSERT INTO email_forwards(user_id,org_id,identity_id,forward_to,active,verified,verify_token,verified_at,updated_at)
					VALUES(1,1,1,'kept@elsewhere.test',true,true,'old-token',now(),now()),
					(1,1,999,'orphan@elsewhere.test',true,true,NULL,now(),now());
				INSERT INTO auto_replies(user_id,org_id,name,start_date,subject,html_content,updated_at)
					VALUES(1,1,'Away',now(),'Away','<p>Away</p>',now()),(999,1,'Orphan',now(),'Away','<p>Away</p>',now());
				INSERT INTO sieve_scripts(user_id,org_id,name,script,active,updated_at) VALUES(1,1,'Old','keep;',true,now());
				INSERT INTO push_subscriptions(user_id,endpoint,p256dh_key,auth_key,active) VALUES(1,'https://push.example.test/1','key','auth',true);`
			if legacy {
				if _, err := db.Exec(legacySchema); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(seed); err != nil {
					t.Fatal(err)
				}
			} else {
				// Stop at 014 so 015 meets the same pre-existing rows.
				if err := database.MigrateThrough(ctx, db, "014_automation_executor.sql"); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(seed); err != nil {
					t.Fatal(err)
				}
			}
			if err := database.Migrate(ctx, db); err != nil {
				t.Fatal(err)
			}
			if err := database.Migrate(ctx, db); err != nil {
				t.Fatal("rerun:", err)
			}

			var forwards, active, verified int
			var status, lastError string
			if err := db.QueryRow(`SELECT count(*), count(*) FILTER (WHERE active), count(*) FILTER (WHERE verified OR verify_token IS NOT NULL), min(status), min(last_error) FROM email_forwards`).Scan(&forwards, &active, &verified, &status, &lastError); err != nil {
				t.Fatal(err)
			}
			if forwards != 1 || active != 0 || verified != 0 || status != "pending" || lastError == "" {
				t.Fatalf("forward reset: n=%d active=%d verified=%d status=%q", forwards, active, verified, status)
			}
			var replies int
			if err := db.QueryRow(`SELECT count(*) FROM auto_replies WHERE reply_interval_days=7 AND window_count=0`).Scan(&replies); err != nil || replies != 1 {
				t.Fatalf("auto-reply orphans or defaults: %d %v", replies, err)
			}
			var sieveActive bool
			var unsupported bool
			if err := db.QueryRow(`SELECT active, unsupported_at IS NOT NULL FROM sieve_scripts`).Scan(&sieveActive, &unsupported); err != nil || sieveActive || !unsupported {
				t.Fatalf("sieve not marked unsupported: %v %v %v", sieveActive, unsupported, err)
			}
			var pushActive bool
			if err := db.QueryRow(`SELECT active FROM push_subscriptions`).Scan(&pushActive); err != nil || pushActive {
				t.Fatalf("push subscription still active: %v", err)
			}

			// New FKs cascade, and the new tables accept rows.
			if _, err := db.Exec(`INSERT INTO email_forwards(user_id,org_id,identity_id,forward_to,updated_at) VALUES(1,1,999,'x@elsewhere.test',now())`); err == nil {
				t.Fatal("forward to a missing identity accepted")
			}
			if _, err := db.Exec(`INSERT INTO transactional_emails(org_id,message_id,from_address,to_addresses,subject,system_kind,updated_at) VALUES(1,'<bad@arrival.test>','a@arrival.test','b@elsewhere.test','s','newsletter',now())`); err == nil {
				t.Fatal("unknown system_kind accepted")
			}
			var emailID int64
			if err := db.QueryRow(`INSERT INTO transactional_emails(org_id,message_id,from_address,to_addresses,subject,system_kind,system_ref,system_user_id,updated_at)
				VALUES(1,'<ok@arrival.test>','a@arrival.test','b@elsewhere.test','s','forward',gen_random_uuid(),1,now()) RETURNING id`).Scan(&emailID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO send_attachment_refs VALUES($1,'bucket','key')`, emailID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO mail_arrival_jobs(org_id,kind,identity_id,user_id,ses_message_id,dedupe_key) VALUES(1,'auto_reply',1,1,'ses-1','ar:1:ses-1')`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO mail_arrival_jobs(org_id,kind,identity_id,user_id,ses_message_id,dedupe_key) VALUES(1,'auto_reply',1,1,'ses-1','ar:1:ses-1')`); err == nil {
				t.Fatal("duplicate arrival job accepted")
			}
			if _, err := db.Exec(`DELETE FROM transactional_emails WHERE id=$1`, emailID); err != nil {
				t.Fatal(err)
			}
			var refs int
			if err := db.QueryRow(`SELECT count(*) FROM send_attachment_refs`).Scan(&refs); err != nil || refs != 0 {
				t.Fatalf("attachment refs did not cascade: %d %v", refs, err)
			}
			if _, err := db.Exec(`DELETE FROM users WHERE id=1`); err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"email_forwards", "auto_replies", "mail_arrival_jobs"} {
				var left int
				if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&left); err != nil || left != 0 {
					t.Fatalf("%s did not cascade from users: %d %v", table, left, err)
				}
			}
		})
	}
}
