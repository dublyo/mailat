package database_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/testutil"
)

// bootTriggerSQL is the DDL that service.SetupTriggers used to run at every
// boot before 012 took it over. Installs upgraded from that code already have it.
const bootTriggerSQL = `
CREATE OR REPLACE FUNCTION notify_email_status_change() RETURNS TRIGGER AS $$
DECLARE payload JSON;
BEGIN
	payload := json_build_object('table', TG_TABLE_NAME, 'action', TG_OP, 'email_id', NEW.id, 'org_id', NEW.org_id,
		'old_status', COALESCE(OLD.status, ''), 'new_status', NEW.status);
	PERFORM pg_notify('email_status_changed', payload::text);
	RETURN NEW;
END; $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS email_status_changed_trigger ON transactional_emails;
CREATE TRIGGER email_status_changed_trigger AFTER UPDATE OF status ON transactional_emails
	FOR EACH ROW WHEN (OLD.status IS DISTINCT FROM NEW.status) EXECUTE FUNCTION notify_email_status_change();
CREATE OR REPLACE FUNCTION notify_delivery_event() RETURNS TRIGGER AS $$
DECLARE payload JSON; org_id INT;
BEGIN
	SELECT te.org_id INTO org_id FROM transactional_emails te WHERE te.id = NEW.email_id;
	payload := json_build_object('table', TG_TABLE_NAME, 'action', TG_OP, 'email_id', NEW.email_id, 'org_id', org_id,
		'event_type', NEW.event_type, 'data', json_build_object('details', NEW.details, 'ip_address', NEW.ip_address, 'user_agent', NEW.user_agent));
	PERFORM pg_notify('delivery_event_created', payload::text);
	RETURN NEW;
END; $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS delivery_event_created_trigger ON transactional_delivery_events;
CREATE TRIGGER delivery_event_created_trigger AFTER INSERT ON transactional_delivery_events
	FOR EACH ROW EXECUTE FUNCTION notify_delivery_event();`

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(s))))
	return hex.EncodeToString(sum[:])
}

func TestHardeningMigration(t *testing.T) {
	for _, tc := range []struct {
		name            string
		legacy, bootDDL bool
	}{{"fresh", false, false}, {"legacy", true, false}, {"legacy_with_boot_triggers", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			db := testutil.EmptyDatabase(t)
			ctx := context.Background()
			if tc.legacy {
				if _, err := db.Exec(legacySchema); err != nil {
					t.Fatal(err)
				}
				if tc.bootDDL {
					if _, err := db.Exec(bootTriggerSQL); err != nil {
						t.Fatal(err)
					}
				}
				// Pre-012 data: one unique mixed-case contact, one case-colliding pair and
				// a plaintext suppression with surrounding whitespace and capitals.
				if _, err := db.Exec(`INSERT INTO organizations(id,name,slug) VALUES(1,'Org','org');
					INSERT INTO contacts(org_id,email) VALUES(1,'Mixed@Example.TEST'),(1,'Dup@Example.test'),(1,'dup@example.test');
					INSERT INTO suppressions(org_id,email,reason,source_type) VALUES(1,' Gone@Example.TEST','unsubscribe','test')`); err != nil {
					t.Fatal(err)
				}
			}
			if err := database.Migrate(ctx, db); err != nil {
				t.Fatal(err)
			}
			if err := database.Migrate(ctx, db); err != nil {
				t.Fatal("rerun:", err)
			}
			if !tc.legacy {
				if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Org','org',now())`); err != nil {
					t.Fatal(err)
				}
			}

			var triggers int
			if err := db.QueryRow(`SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid
				WHERE c.relnamespace=current_schema()::regnamespace AND NOT t.tgisinternal
				AND t.tgname IN ('email_status_changed_trigger','delivery_event_created_trigger')`).Scan(&triggers); err != nil || triggers != 2 {
				t.Fatalf("delivery triggers = %d, %v", triggers, err)
			}
			for _, col := range []string{"send_attempts", "next_attempt_at", "last_deferral_reason"} {
				var ok bool
				if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='transactional_emails' AND column_name=$1)`, col).Scan(&ok); err != nil || !ok {
					t.Fatalf("missing transactional_emails.%s: %v", col, err)
				}
			}

			if tc.legacy {
				var hash string
				if err := db.QueryRow(`SELECT email_sha256 FROM suppressions WHERE org_id=1`).Scan(&hash); err != nil || hash != sha256Hex("gone@example.test") {
					t.Fatalf("backfilled hash %q %v", hash, err)
				}
				var unique string
				if err := db.QueryRow(`SELECT email FROM contacts WHERE lower(email)='mixed@example.test'`).Scan(&unique); err != nil || unique != "mixed@example.test" {
					t.Fatalf("unique contact not lowercased: %q %v", unique, err)
				}
				var kept int
				if err := db.QueryRow(`SELECT count(*) FROM contacts WHERE email IN ('Dup@Example.test','dup@example.test')`).Scan(&kept); err != nil || kept != 2 {
					t.Fatalf("colliding pair changed: %d %v", kept, err)
				}
			}

			// The trigger derives the hash for plaintext rows, matching Go's normalization.
			var got string
			if err := db.QueryRow(`INSERT INTO suppressions(org_id,email,reason,source_type) VALUES(1,'New@Example.test ','bounce','test') RETURNING email_sha256`).Scan(&got); err != nil || got != sha256Hex("New@Example.test ") {
				t.Fatalf("trigger hash %q %v", got, err)
			}
			// Erased rows keep only the hash, which the caller must supply.
			if _, err := db.Exec(`INSERT INTO suppressions(org_id,email,reason,source_type) VALUES(1,'erased:abc','gdpr_erasure','gdpr')`); err == nil {
				t.Fatal("erased row without hash was accepted")
			}
			h := sha256Hex("erased@example.test")
			if _, err := db.Exec(`INSERT INTO suppressions(org_id,email,email_sha256,reason,source_type) VALUES(1,'erased:'||$1,$1,'gdpr_erasure','gdpr')`, h); err != nil {
				t.Fatal("erased row with hash:", err)
			}
		})
	}
}
