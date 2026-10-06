package database_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lib/pq"

	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/testutil"
)

func TestMultiUserMigration(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "fresh"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			db := testutil.EmptyDatabase(t)
			ctx := context.Background()
			seed := `INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Org','org',now());
				INSERT INTO users(id,org_id,email,password_hash,role,updated_at) VALUES(1,1,'owner@multi.test','unused','owner',now()),(2,1,'member@multi.test','unused','member',now()),(3,1,'steward@multi.test','unused','member',now());
				INSERT INTO domains(id,org_id,name,verification_token,updated_at) VALUES(1,1,'multi.test','token',now());
				INSERT INTO identities(id,user_id,domain_id,email,updated_at) VALUES(1,1,1,'owner@multi.test',now());
				INSERT INTO received_emails(uuid,org_id,domain_id,identity_id,message_id,from_email,subject,ses_message_id,to_emails,updated_at)
					VALUES('00000000-0000-0000-0000-000000000001',1,1,1,'old','a@sender.test','Old','ses-old','{}',now());
				INSERT INTO shared_mailboxes(org_id,name,email,updated_at) VALUES(1,'Legacy','legacy@multi.test',now());`
			if legacy {
				if _, err := db.Exec(legacySchema); err != nil {
					t.Fatal(err)
				}
			} else if err := database.MigrateThrough(ctx, db, "015_mail_arrival.sql"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(seed); err != nil {
				t.Fatal(err)
			}
			if err := database.Migrate(ctx, db); err != nil {
				t.Fatal(err)
			}
			if err := database.Migrate(ctx, db); err != nil {
				t.Fatal("rerun:", err)
			}

			var oldIndex, newIndex bool
			if err := db.QueryRow(`SELECT to_regclass('received_emails_identity_ses_key') IS NOT NULL, to_regclass('received_emails_identity_owner_ses_key') IS NOT NULL`).Scan(&oldIndex, &newIndex); err != nil || oldIndex || !newIndex {
				t.Fatalf("index swap: old=%v new=%v %v", oldIndex, newIndex, err)
			}
			var kind string
			var linked bool
			if err := db.QueryRow(`SELECT kind FROM identities WHERE id=1`).Scan(&kind); err != nil || kind != "personal" {
				t.Fatal(kind, err)
			}
			if err := db.QueryRow(`SELECT identity_id IS NOT NULL FROM shared_mailboxes`).Scan(&linked); err != nil || linked {
				t.Fatal("legacy shared mailbox must stay unlinked", err)
			}

			// New writes are checked against the role list.
			if _, err := db.Exec(`INSERT INTO users(org_id,email,password_hash,role,updated_at) VALUES(1,'bad@multi.test','x','superuser',now())`); err == nil {
				t.Fatal("unknown role accepted")
			}
			if _, err := db.Exec(`INSERT INTO org_invites(org_id,email,role,token_hash,expires_at) VALUES(1,'Upper@multi.test','member',repeat('a',64),now()+interval '1 day')`); err == nil {
				t.Fatal("mixed-case invite email accepted")
			}
			if _, err := db.Exec(`INSERT INTO org_invites(org_id,email,role,token_hash,expires_at) VALUES(1,'new@multi.test','member',repeat('a',64),now()+interval '1 day')`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO org_invites(org_id,email,role,token_hash,expires_at) VALUES(1,'new@multi.test','admin',repeat('b',64),now()+interval '1 day')`); err == nil {
				t.Fatal("second open invite for one email accepted")
			}

			// Personal mail is always owned by the identity owner.
			var owner int64
			if err := db.QueryRow(`INSERT INTO received_emails(org_id,domain_id,identity_id,message_id,from_email,subject,ses_message_id,mailbox_owner_id,to_emails,updated_at)
				VALUES(1,1,1,'p','a@sender.test','P','ses-p',2,'{}',now()) RETURNING mailbox_owner_id`).Scan(&owner); err != nil || owner != 1 {
				t.Fatalf("personal owner not forced: %d %v", owner, err)
			}

			// Shared identities: no catch-all or default, copies only for members.
			if _, err := db.Exec(`INSERT INTO identities(id,user_id,domain_id,email,kind,is_catch_all,updated_at) VALUES(9,3,1,'bad@multi.test','shared',true,now())`); err == nil {
				t.Fatal("shared catch-all accepted")
			}
			if _, err := db.Exec(`INSERT INTO identities(id,user_id,domain_id,email,kind,updated_at) VALUES(2,3,1,'team@multi.test','shared',now());
				INSERT INTO shared_mailboxes(org_id,name,email,identity_id,updated_at) VALUES(1,'Team','team@multi.test',2,now());`); err != nil {
				t.Fatal(err)
			}
			_, err := db.Exec(`INSERT INTO received_emails(org_id,domain_id,identity_id,message_id,from_email,subject,ses_message_id,mailbox_owner_id,to_emails,updated_at)
				VALUES(1,1,2,'s','a@sender.test','S','ses-s',3,'{}',now())`)
			var pqErr *pq.Error
			if !errors.As(err, &pqErr) || pqErr.Code != "42501" {
				t.Fatalf("shared copy for the non-member steward: %v", err)
			}
			if _, err := db.Exec(`INSERT INTO shared_mailbox_members(shared_mailbox_id,user_id,can_read) SELECT id,2,true FROM shared_mailboxes WHERE identity_id=2`); err != nil {
				t.Fatal(err)
			}
			for _, member := range []int{2, 2} {
				_, err = db.Exec(`INSERT INTO received_emails(org_id,domain_id,identity_id,message_id,from_email,subject,ses_message_id,mailbox_owner_id,to_emails,updated_at)
					VALUES(1,1,2,'s','a@sender.test','S','ses-s',$1,'{}',now()) ON CONFLICT(identity_id,mailbox_owner_id,ses_message_id) DO NOTHING`, member)
				if err != nil {
					t.Fatal(err)
				}
			}
			var copies, changes int
			if err := db.QueryRow(`SELECT count(*) FROM received_emails WHERE identity_id=2 AND mailbox_owner_id=2`).Scan(&copies); err != nil || copies != 1 {
				t.Fatalf("member copies: %d %v", copies, err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM mailbox_changes WHERE user_id=2`).Scan(&changes); err != nil || changes != 1 {
				t.Fatalf("member change feed: %d %v", changes, err)
			}
			if _, err := db.Exec(`INSERT INTO stream_ticket_redemptions(jti,user_id,expires_at) VALUES('00000000-0000-0000-0000-0000000000aa',1,now())`); err != nil {
				t.Fatal(err)
			}
		})
	}
}
