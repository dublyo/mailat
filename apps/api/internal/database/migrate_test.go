package database_test

import (
	"context"
	_ "embed"
	"testing"

	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/testutil"
)

//go:embed testdata/legacy_schema.sql
var legacySchema string

func TestFreshAndLegacyMigrationPreservesMail(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "fresh"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			db := testutil.EmptyDatabase(t)
			ctx := context.Background()
			if legacy {
				if _, err := db.Exec(legacySchema); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO organizations(id,name,slug) VALUES(1,'Existing','existing'); INSERT INTO users(id,org_id,email,password_hash) VALUES(1,1,'existing@example.test','unused'); INSERT INTO domains(id,org_id,name,verification_token) VALUES(1,1,'example.test','token'); INSERT INTO identities(id,user_id,domain_id,email) VALUES(1,1,1,'mail@example.test'); INSERT INTO received_emails(org_id,identity_id,domain_id,message_id,from_email,to_emails,subject,text_body) VALUES(1,1,1,'legacy-message','sender@example.test',ARRAY['mail@example.test'],'Preserve me','Original body');`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO user_settings(user_id,org_id) VALUES(1,1)`); err != nil {
					t.Fatal(err)
				}
			}
			if err := database.Migrate(ctx, db); err != nil {
				t.Fatal(err)
			}
			if err := database.Migrate(ctx, db); err != nil {
				t.Fatal("second migration was not idempotent:", err)
			}
			if legacy {
				var body string
				if err := db.QueryRow(`SELECT text_body FROM received_emails WHERE message_id='legacy-message'`).Scan(&body); err != nil || body != "Original body" {
					t.Fatalf("legacy mail changed: %v", err)
				}
				var organize bool
				if err := db.QueryRow(`SELECT auto_organize_dmarc_reports FROM user_settings WHERE user_id=1`).Scan(&organize); err != nil || !organize {
					t.Fatalf("existing account missing DMARC default: %v", err)
				}
				if _, err := db.Exec(`UPDATE user_settings SET auto_organize_dmarc_reports=false WHERE user_id=1`); err != nil {
					t.Fatal(err)
				}
				if err := database.Migrate(ctx, db); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRow(`SELECT auto_organize_dmarc_reports FROM user_settings WHERE user_id=1`).Scan(&organize); err != nil || organize {
					t.Fatalf("migration rerun overwrote opt-out: %v", err)
				}
			}
			var n int
			if err := db.QueryRow(`SELECT count(*) FROM mailat_schema_migrations`).Scan(&n); err != nil || n < 4 {
				t.Fatalf("missing migrations: %d %v", n, err)
			}
		})
	}
}
