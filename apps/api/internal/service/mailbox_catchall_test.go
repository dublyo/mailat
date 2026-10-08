package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// The domain catch-all can be moved to a mailbox user's inbox (Migadu
// "catchall recipient"), back to a staff identity, or removed; only one
// identity holds it at a time and every change is audited.
func TestSetDomainCatchAll(t *testing.T) {
	f := newMailboxUserFixture(t)
	ctx := context.Background()
	box := f.create(t, "boss", password("correct horse battery"))
	user := f.userID(t, box.Mailbox.UserUUID)

	list, err := f.mb.ListDomainMailboxes(ctx, f.owner.OrgID, f.domain, false)
	if err != nil {
		t.Fatal(err)
	}
	if list.CatchAll == nil || list.CatchAll.Email != "catchall@acme.test" || len(list.CatchAllOptions) < 2 {
		t.Fatalf("before: catch-all %+v, options %d", list.CatchAll, len(list.CatchAllOptions))
	}

	// Move it to the mailbox: unknown addresses now reach the mailbox user.
	got, err := f.mb.SetDomainCatchAll(ctx, f.owner, f.domain, box.Mailbox.IdentityUUID)
	if err != nil || got == nil || got.Email != "boss@acme.test" || !got.IsMailbox {
		t.Fatalf("set to mailbox: %+v %v", got, err)
	}
	if n := f.count(t, `SELECT count(*) FROM identities WHERE domain_id=50 AND is_catch_all`); n != 1 {
		t.Fatalf("%d catch-all identities", n)
	}
	f.ingestShared(t, "to-boss", "nobody@acme.test")
	if f.copies(t, "to-boss", user) != 1 || f.copies(t, "to-boss", f.admin.UserID) != 0 {
		t.Fatal("unknown address did not reach the new catch-all mailbox")
	}
	// Setting the same identity again is a no-op.
	if _, err = f.mb.SetDomainCatchAll(ctx, f.owner, f.domain, box.Mailbox.IdentityUUID); err != nil {
		t.Fatal(err)
	}

	// A mailbox that cannot receive cannot be the catch-all.
	other := f.create(t, "quiet", password("correct horse battery"))
	if _, err = f.db.Exec(`UPDATE identities SET can_receive=false WHERE uuid=$1`, other.Mailbox.IdentityUUID); err != nil {
		t.Fatal(err)
	}
	var oe *OrgError
	if _, err = f.mb.SetDomainCatchAll(ctx, f.owner, f.domain, other.Mailbox.IdentityUUID); !errors.As(err, &oe) || oe.Status != http.StatusConflict {
		t.Fatalf("non-receiving target: %v", err)
	}
	// An identity on another domain (or a random UUID) is not found here.
	if _, err = f.mb.SetDomainCatchAll(ctx, f.owner, f.domain, "8a1c2b3d-0000-4000-8000-000000000000"); !errors.As(err, &oe) || oe.Status != http.StatusNotFound {
		t.Fatalf("unknown identity: %v", err)
	}

	// Remove it: unknown addresses are no longer delivered to anyone.
	if got, err = f.mb.SetDomainCatchAll(ctx, f.owner, f.domain, ""); err != nil || got != nil {
		t.Fatalf("remove: %+v %v", got, err)
	}
	if n := f.count(t, `SELECT count(*) FROM identities WHERE domain_id=50 AND is_catch_all`); n != 0 {
		t.Fatalf("%d catch-all identities after remove", n)
	}
	if n := f.count(t, `SELECT count(*) FROM audit_logs WHERE action='domain_catch_all' AND resource_id=$1`, f.domain); n != 3 {
		t.Fatalf("%d domain_catch_all audit rows", n)
	}
}
