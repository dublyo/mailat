package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// A join invite never replaces a pending or suspended mailbox login: create is
// refused, a stale invite cannot be accepted, and mailbox links are invisible
// to the generic invite routes.
func TestJoinInviteNeverReplacesMailboxLogin(t *testing.T) {
	f := newOrgFixture(t)
	ctx := context.Background()
	var boxID int64
	if err := f.db.QueryRow(`INSERT INTO users(org_id,email,password_hash,name,role,status,updated_at) VALUES($1,'box@acme.test','','Box','mailbox','pending',now()) RETURNING id`, f.org).Scan(&boxID); err != nil {
		t.Fatal(err)
	}
	invite := func() error {
		_, err := f.svc.CreateInvite(ctx, f.owner, &CreateInviteRequest{Email: "box@acme.test", Role: "member"})
		return err
	}
	for _, status := range []string{"pending", "suspended", "active"} {
		if _, err := f.db.Exec(`UPDATE users SET status=$2 WHERE id=$1`, boxID, status); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, invite(), http.StatusConflict)
	}

	// An invite made while the login was removed goes stale once it is live again.
	if _, err := f.db.Exec(`UPDATE users SET status='disabled' WHERE id=$1`, boxID); err != nil {
		t.Fatal(err)
	}
	if err := invite(); err != nil {
		t.Fatal(err)
	}
	token := f.lastToken(t)
	if _, err := f.db.Exec(`UPDATE users SET status='pending' WHERE id=$1`, boxID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AcceptInvite(ctx, &AcceptInviteRequest{Token: token, Name: "Intruder", Password: "long-enough-password"}, "192.0.2.1"); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("stale accept: %v", err)
	}
	var email, status string
	if err := f.db.QueryRow(`SELECT email,status FROM users WHERE id=$1`, boxID).Scan(&email, &status); err != nil || email != "box@acme.test" || status != "pending" {
		t.Fatalf("mailbox login changed: %s %s %v", email, status, err)
	}
	if n := f.count(t, `SELECT count(*) FROM users WHERE org_id=$1`, f.org); n != 4 {
		t.Fatalf("users: %d", n)
	}

	// A mailbox setup link is not a join invite anywhere in the generic flow.
	var box2 int64
	if err := f.db.QueryRow(`INSERT INTO users(org_id,email,password_hash,name,role,status,updated_at) VALUES($1,'box2@acme.test','','Box Two','mailbox','pending',now()) RETURNING id`, f.org).Scan(&box2); err != nil {
		t.Fatal(err)
	}
	const setupToken = "TWFpbGJveFNldHVwVG9rZW5fX19fMDEyMzQ1Njc4OWFiYw"
	var linkUUID string
	if err := f.db.QueryRow(`INSERT INTO org_invites(org_id,email,role,token_hash,expires_at,purpose,user_id,delivery_email)
		VALUES($1,'box2@acme.test','mailbox',$2,now()+interval '1 day','mailbox_setup',$3,'me@outside.test') RETURNING uuid::text`, f.org, hashToken(setupToken), box2).Scan(&linkUUID); err != nil {
		t.Fatal(err)
	}
	invites, err := f.svc.ListInvites(ctx, f.org)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range invites {
		if i.UUID == linkUUID {
			t.Fatal("mailbox link listed as an invite")
		}
	}
	_, err = f.svc.ResendInvite(ctx, f.owner, linkUUID)
	wantStatus(t, err, http.StatusNotFound)
	wantStatus(t, f.svc.RevokeInvite(ctx, f.owner, linkUUID), http.StatusNotFound)
	if n := f.count(t, `SELECT count(*) FROM org_invites WHERE uuid=$1 AND revoked_at IS NULL AND accepted_at IS NULL`, linkUUID); n != 1 {
		t.Fatal("mailbox link was changed by the generic routes")
	}
	// Its own flow: lookup shows the purpose and name, accept activates the
	// pending login in place (no new user, no seat).
	look, err := f.svc.LookupInvite(ctx, setupToken)
	if err != nil || look.Purpose != "mailbox_setup" || look.Name != "Box Two" || look.Email != "box2@acme.test" {
		t.Fatalf("lookup: %+v %v", look, err)
	}
	res, err := f.svc.AcceptInvite(ctx, &AcceptInviteRequest{Token: setupToken, Name: "Box Two", Password: "long-enough-password"}, "192.0.2.1")
	if err != nil || !res.SignedIn || res.User.ID != box2 {
		t.Fatalf("accept: %+v %v", res, err)
	}
	if n := f.count(t, `SELECT count(*) FROM users WHERE id=$1 AND status='active' AND role='mailbox'`, box2); n != 1 {
		t.Fatal("mailbox login not activated")
	}
}
