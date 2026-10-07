package database_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lib/pq"

	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/testutil"
)

func pqCode(err error) string {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return string(pqErr.Code)
	}
	return ""
}

func TestMailboxUsersMigration(t *testing.T) {
	db := testutil.EmptyDatabase(t)
	ctx := context.Background()
	if err := database.MigrateThrough(ctx, db, "016_multi_user_live.sql"); err != nil {
		t.Fatal(err)
	}
	// A legacy row with a status the new check does not know.
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Org','org',now());
		INSERT INTO users(id,org_id,email,password_hash,role,status,updated_at) VALUES
			(1,1,'owner@mbx.test','unused','owner','active',now()),(2,1,'legacy@mbx.test','unused','member','inactive',now());
		INSERT INTO domains(id,org_id,name,verification_token,updated_at) VALUES(1,1,'mbx.test','token',now());
		INSERT INTO identities(id,user_id,domain_id,email,updated_at) VALUES(1,1,1,'owner@mbx.test',now());
		INSERT INTO org_invites(org_id,email,role,token_hash,expires_at) VALUES(1,'old@mbx.test','member',repeat('a',64),now()+interval '1 day');`); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal("rerun:", err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustFail := func(code, q string, args ...any) {
		t.Helper()
		_, err := db.Exec(q, args...)
		if got := pqCode(err); got != code {
			t.Fatalf("%s: got %q (%v) want %s", q, got, err, code)
		}
	}

	// Legacy rows survive; any later UPDATE of them is checked (NOT VALID).
	var purpose string
	if err := db.QueryRow(`SELECT purpose FROM org_invites WHERE email='old@mbx.test'`).Scan(&purpose); err != nil || purpose != "join" {
		t.Fatal(purpose, err)
	}
	mustFail("23514", `UPDATE users SET name='x' WHERE id=2`)

	// Roles, statuses and pending-only-for-mailbox.
	mustExec(`INSERT INTO users(id,org_id,email,password_hash,role,status,updated_at) VALUES(3,1,'box@mbx.test','','mailbox','pending',now())`)
	mustFail("23514", `INSERT INTO users(org_id,email,password_hash,role,status,updated_at) VALUES(1,'p@mbx.test','','member','pending',now())`)
	mustFail("23514", `INSERT INTO users(org_id,email,password_hash,role,updated_at) VALUES(1,'r@mbx.test','','superuser',now())`)
	mustFail("23514", `INSERT INTO users(org_id,email,password_hash,role,status,updated_at) VALUES(1,'s@mbx.test','','member','frozen',now())`)
	mustExec(`INSERT INTO users(id,org_id,email,password_hash,role,updated_at) VALUES(4,1,'member@mbx.test','x','member',now())`)
	mustExec(`UPDATE users SET status='suspended' WHERE id=4`)
	mustExec(`UPDATE users SET status='suspended' WHERE id=3`)

	// Invite shape: join invites have no user; mailbox links need one plus a delivery address.
	mustFail("23514", `INSERT INTO org_invites(org_id,email,role,token_hash,expires_at,purpose) VALUES(1,'a@mbx.test','mailbox',repeat('b',64),now(),'join')`)
	mustFail("23514", `INSERT INTO org_invites(org_id,email,role,token_hash,expires_at,purpose,user_id) VALUES(1,'a@mbx.test','member',repeat('b',64),now(),'join',3)`)
	mustFail("23514", `INSERT INTO org_invites(org_id,email,role,token_hash,expires_at,purpose,user_id) VALUES(1,'box@mbx.test','mailbox',repeat('b',64),now(),'mailbox_setup',3)`)
	mustFail("23514", `INSERT INTO org_invites(org_id,email,role,token_hash,expires_at,purpose,user_id,delivery_email) VALUES(1,'box@mbx.test','member',repeat('b',64),now(),'password_reset',3,'me@outside.test')`)
	mustExec(`INSERT INTO org_invites(org_id,email,role,token_hash,expires_at,purpose,user_id,delivery_email) VALUES(1,'box@mbx.test','mailbox',repeat('b',64),now(),'mailbox_setup',3,'me@outside.test')`)
	mustFail("23505", `INSERT INTO org_invites(org_id,email,role,token_hash,expires_at) VALUES(1,'box@mbx.test','member',repeat('c',64),now())`)

	// Alias/identity exclusion in both directions, case-insensitively.
	mustExec(`INSERT INTO identities(id,user_id,domain_id,email,updated_at) VALUES(2,3,1,'box@mbx.test',now())`)
	mustExec(`INSERT INTO identity_send_aliases(identity_id,address) VALUES(2,'sales@mbx.test')`)
	mustFail("23505", `INSERT INTO identity_send_aliases(identity_id,address) VALUES(2,'owner@mbx.test')`)
	mustFail("23505", `INSERT INTO identities(user_id,domain_id,email,updated_at) VALUES(1,1,'Sales@MBX.test',now())`)
	mustFail("23505", `UPDATE identities SET email='sales@mbx.test' WHERE id=1`)
	mustFail("23505", `INSERT INTO identity_send_aliases(identity_id,address) VALUES(1,'sales@mbx.test')`)
	mustFail("23514", `INSERT INTO identity_send_aliases(identity_id,address) VALUES(2,'Upper@mbx.test')`)

	// Shared identities take neither aliases nor the wildcard switch.
	mustExec(`INSERT INTO identities(id,user_id,domain_id,email,kind,updated_at) VALUES(3,1,1,'team@mbx.test','shared',now())`)
	mustFail("23514", `INSERT INTO identity_send_aliases(identity_id,address) VALUES(3,'desk@mbx.test')`)
	mustFail("23514", `UPDATE identities SET wildcard_sender=true WHERE id=3`)
	mustExec(`UPDATE identities SET wildcard_sender=true WHERE id=2`)

	// One live mailbox per identity; a removed row allows re-creation.
	mustExec(`INSERT INTO mailbox_accounts(user_id,org_id,identity_id,domain_id) VALUES(3,1,2,1)`)
	mustFail("23505", `INSERT INTO mailbox_accounts(user_id,org_id,identity_id,domain_id) VALUES(4,1,2,1)`)
	mustExec(`UPDATE mailbox_accounts SET removed_at=now() WHERE user_id=3`)
	mustExec(`INSERT INTO mailbox_accounts(user_id,org_id,identity_id,domain_id) VALUES(4,1,2,1)`)
	mustFail("23514", `UPDATE mailbox_accounts SET recovery_email='Me@Outside.test' WHERE user_id=4`)
}

// Two transactions claiming one address as an identity and as an alias: the
// advisory lock serializes them, so the second always sees the first.
func TestMailboxAliasIdentityRace(t *testing.T) {
	db := testutil.Database(t)
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Org','org',now());
		INSERT INTO users(id,org_id,email,password_hash,role,updated_at) VALUES(1,1,'owner@race.test','unused','owner',now());
		INSERT INTO domains(id,org_id,name,verification_token,updated_at) VALUES(1,1,'race.test','token',now());
		INSERT INTO identities(id,user_id,domain_id,email,updated_at) VALUES(1,1,1,'owner@race.test',now());
		SELECT setval(pg_get_serial_sequence('identities','id'),100);`); err != nil {
		t.Fatal(err)
	}
	for _, aliasFirst := range []bool{false, true} {
		addr := "first@race.test"
		if aliasFirst {
			addr = "second@race.test"
		}
		insertIdentity := `INSERT INTO identities(user_id,domain_id,email,updated_at) VALUES(1,1,$1,now())`
		insertAlias := `INSERT INTO identity_send_aliases(identity_id,address) VALUES(1,$1)`
		first, second := insertIdentity, insertAlias
		if aliasFirst {
			first, second = insertAlias, insertIdentity
		}
		tx1, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx1.Exec(first, addr); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := db.Exec(second, addr)
			done <- err
		}()
		select {
		case err := <-done:
			t.Fatalf("second insert did not wait for the first: %v", err)
		case <-time.After(200 * time.Millisecond):
		}
		if err = tx1.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; pqCode(err) != "23505" {
			t.Fatalf("aliasFirst=%v: second insert got %v", aliasFirst, err)
		}
	}
}
