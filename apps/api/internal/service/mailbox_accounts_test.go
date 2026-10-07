package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
)

type mailboxUserFixture struct {
	*orgFixture
	mb       *MailboxService
	domain   string // acme.test uuid
	catchAll int64  // the admin's catch-all (and only sending) identity
}

func newMailboxUserFixture(t *testing.T) *mailboxUserFixture {
	t.Helper()
	f := &mailboxUserFixture{orgFixture: newOrgFixture(t)}
	f.mb = NewMailboxService(f.db, f.svc)
	if err := f.db.QueryRow(`SELECT uuid::text FROM domains WHERE id=50`).Scan(&f.domain); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`INSERT INTO identities(user_id,domain_id,email,can_send,is_catch_all,updated_at) VALUES($1,50,'catchall@acme.test',true,true,now()) RETURNING id`, f.admin.UserID).Scan(&f.catchAll); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *mailboxUserFixture) create(t *testing.T, local string, access MailboxAccessRequest) *CreateMailboxResult {
	t.Helper()
	res, err := f.mb.CreateMailbox(context.Background(), f.owner, f.domain, &CreateMailboxRequest{LocalPart: local, Name: "Box " + local, Access: access})
	if err != nil {
		t.Fatalf("create %s: %v", local, err)
	}
	return res
}

func invite(email string) MailboxAccessRequest {
	return MailboxAccessRequest{Mode: "invite", InviteEmail: email}
}

func password(p string) MailboxAccessRequest {
	return MailboxAccessRequest{Mode: "password", Password: p}
}

func (f *mailboxUserFixture) userID(t *testing.T, userUUID string) int64 {
	t.Helper()
	var id int64
	if err := f.db.QueryRow(`SELECT id FROM users WHERE uuid=$1`, userUUID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *mailboxUserFixture) copies(t *testing.T, sesID string, owner int64) int {
	t.Helper()
	return f.count(t, `SELECT count(*) FROM received_emails WHERE ses_message_id=$1 AND mailbox_owner_id=$2`, sesID, owner)
}

func (f *mailboxUserFixture) login(email, pw string) (*model.LoginResponse, error) {
	return f.auth.Login(context.Background(), &model.LoginRequest{Email: email, Password: pw})
}

func (f *mailboxUserFixture) lastMail(t *testing.T) (to, text string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.mail) == 0 {
		t.Fatal("no mail queued")
	}
	m := f.mail[len(f.mail)-1]
	return strings.Join(m.To, ","), m.TextBody
}

func (f *mailboxUserFixture) mailCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.mail)
}

// F1/F3: an invited mailbox is pending, receives mail at once (exact, +tag
// and send-as alias, ahead of the catch-all), and the setup link activates it
// with that mail already there.
func TestMailboxInviteLifecycle(t *testing.T) {
	f := newMailboxUserFixture(t)
	ctx := context.Background()
	res := f.create(t, "Ibrahim", invite("Ibrahim.Personal@Elsewhere.test"))
	box := res.Mailbox
	if box.Address != "ibrahim@acme.test" || box.Status != "invited" || !box.MaySend || !box.MayReceive || box.WildcardSender {
		t.Fatalf("mailbox %+v", box)
	}
	if len(res.Warnings) != 1 || res.Warnings[0] != "receiving_disabled" {
		t.Fatalf("warnings %v", res.Warnings)
	}
	user := f.userID(t, box.UserUUID)
	if n := f.count(t, `SELECT count(*) FROM users WHERE id=$1 AND role='mailbox' AND status='pending' AND password_hash='' AND NOT email_verified`, user); n != 1 {
		t.Fatal("user is not a pending mailbox login")
	}
	if n := f.count(t, `SELECT count(*) FROM identities i JOIN mailbox_accounts ma ON ma.identity_id=i.id AND ma.user_id=i.user_id
		WHERE i.user_id=$1 AND i.is_default AND i.kind='personal' AND ma.recovery_email='ibrahim.personal@elsewhere.test' AND ma.created_by=$2`, user, f.owner.UserID); n != 1 {
		t.Fatal("identity or mailbox row missing")
	}
	if n := f.count(t, `SELECT count(*) FROM org_invites WHERE user_id=$1 AND purpose='mailbox_setup' AND role='mailbox' AND delivery_email='ibrahim.personal@elsewhere.test'
		AND expires_at BETWEEN now()+interval '71 hours' AND now()+interval '73 hours'`, user); n != 1 {
		t.Fatal("setup link missing or wrong TTL")
	}
	to, text := f.lastMail(t)
	if !strings.Contains(to, "<ibrahim.personal@elsewhere.test>") || !strings.Contains(text, "set up your mailbox") || !strings.Contains(text, "within 3 days") {
		t.Fatalf("setup mail to %s: %q", to, text)
	}
	if n := f.count(t, `SELECT count(*) FROM audit_logs WHERE action='mailbox_create' AND resource_id=$1`, box.UserUUID); n != 1 {
		t.Fatal("no mailbox_create audit")
	}
	// A pending login cannot sign in.
	if _, err := f.login("ibrahim@acme.test", ""); err == nil {
		t.Fatal("pending user signed in")
	}

	// Mail accumulates for the pending user; the catch-all gets none of it.
	if _, err := f.db.Exec(`INSERT INTO identity_send_aliases(identity_id,address) SELECT id,'sales@acme.test' FROM identities WHERE user_id=$1`, user); err != nil {
		t.Fatal(err)
	}
	for i, rcpt := range []string{"ibrahim@acme.test", "ibrahim+news@acme.test", "sales@acme.test"} {
		sesID := "pending-" + itoa(int64(i))
		f.ingestShared(t, sesID, rcpt)
		if f.copies(t, sesID, user) != 1 || f.copies(t, sesID, f.admin.UserID) != 0 {
			t.Fatalf("%s was not routed to the mailbox", rcpt)
		}
	}
	f.ingestShared(t, "nobody", "nobody@acme.test")
	if f.copies(t, "nobody", f.admin.UserID) != 1 {
		t.Fatal("unknown address did not reach the catch-all")
	}

	// Resend: cooldown, then a different admin's own sender, a fresh token.
	first := f.lastToken(t)
	_, err := f.mb.ResendSetup(ctx, f.admin, box.UserUUID)
	wantStatus(t, err, http.StatusTooManyRequests)
	if _, err = f.db.Exec(`UPDATE org_invites SET last_sent_at=now()-interval '2 minutes' WHERE user_id=$1`, user); err != nil {
		t.Fatal(err)
	}
	link, err := f.mb.ResendSetup(ctx, f.admin, box.UserUUID)
	if err != nil || link.Purpose != "mailbox_setup" || link.SendCount != 2 || link.Status != "pending" {
		t.Fatalf("resend %+v %v", link, err)
	}
	if n := f.count(t, `SELECT count(*) FROM org_invites WHERE user_id=$1 AND sender_identity_id=$2`, user, f.catchAll); n != 1 {
		t.Fatal("resend did not use the acting admin's sender")
	}
	fresh := f.lastToken(t)
	if _, err = f.svc.LookupInvite(ctx, first); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("old token still works: %v", err)
	}
	look, err := f.svc.LookupInvite(ctx, fresh)
	if err != nil || look.Purpose != "mailbox_setup" || look.Name != "Box Ibrahim" || look.Email != "ibrahim@acme.test" {
		t.Fatalf("lookup %+v %v", look, err)
	}

	// Accept: the name is required here; the user becomes active and signed in.
	_, err = f.svc.AcceptInvite(ctx, &AcceptInviteRequest{Token: fresh, Password: "a-long-password"}, "")
	wantStatus(t, err, http.StatusBadRequest)
	acc, err := f.svc.AcceptInvite(ctx, &AcceptInviteRequest{Token: fresh, Name: "Ibrahim", Password: "a-long-password"}, "198.51.100.7")
	if err != nil || !acc.SignedIn || acc.Token == "" || acc.User.ID != user || acc.User.Role != "mailbox" {
		t.Fatalf("accept %+v %v", acc, err)
	}
	if n := f.count(t, `SELECT count(*) FROM users WHERE id=$1 AND status='active' AND email_verified AND name='Ibrahim'`, user); n != 1 {
		t.Fatal("not activated")
	}
	if n := f.count(t, `SELECT count(*) FROM received_emails WHERE mailbox_owner_id=$1`, user); n != 3 {
		t.Fatalf("mail received while pending: %d, want 3", n)
	}
	if _, err = f.login("ibrahim@acme.test", "a-long-password"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.AcceptInvite(ctx, &AcceptInviteRequest{Token: fresh, Name: "Again", Password: "a-long-password"}, ""); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("second accept: %v", err)
	}
	_, err = f.mb.ResendSetup(ctx, f.owner, box.UserUUID)
	wantStatus(t, err, http.StatusConflict)
	got, err := loadMailbox(ctx, f.db, f.org, user)
	if err != nil || got.Status != "active" || got.AliasCount != 1 {
		t.Fatalf("mailbox %+v %v", got, err)
	}
}

// F2/F4: password mode, admin set-password and reset links (72 h, cooldown,
// one open link, TOTP users not signed in), self-service change, 2FA reset.
func TestMailboxPasswordModeAndAdminReset(t *testing.T) {
	f := newMailboxUserFixture(t)
	ctx := context.Background()
	res := f.create(t, "anna", password("initial-password"))
	user := f.userID(t, res.Mailbox.UserUUID)
	if res.Mailbox.Status != "active" || f.mailCount() != 0 || f.count(t, `SELECT count(*) FROM org_invites WHERE user_id=$1`, user) != 0 {
		t.Fatalf("password mode: %+v", res.Mailbox)
	}
	if _, err := f.login("anna@acme.test", "initial-password"); err != nil {
		t.Fatal(err)
	}
	version := func() int {
		return f.count(t, `SELECT auth_version FROM users WHERE id=$1`, user)
	}

	_, err := f.mb.SetPassword(ctx, f.admin, res.Mailbox.UserUUID, "short")
	wantStatus(t, err, http.StatusBadRequest)
	before := version()
	set, err := f.mb.SetPassword(ctx, f.admin, res.Mailbox.UserUUID, "second-password")
	if err != nil || set.SessionsRevoked != 1 || version() != before+1 {
		t.Fatalf("set %+v %v", set, err)
	}
	if _, err = f.login("anna@acme.test", "initial-password"); err == nil {
		t.Fatal("old password still works")
	}
	if f.mailCount() != 0 {
		t.Fatal("notice sent without a recovery email")
	}

	// Reset links: no recovery email and no address; then an override.
	_, err = f.mb.SendPasswordLink(ctx, f.owner, res.Mailbox.UserUUID, "")
	wantStatus(t, err, http.StatusBadRequest)
	_, err = f.mb.SendPasswordLink(ctx, f.owner, res.Mailbox.UserUUID, "anna@acme.test")
	wantStatus(t, err, http.StatusBadRequest)
	link, err := f.mb.SendPasswordLink(ctx, f.owner, res.Mailbox.UserUUID, "Anna@Home.test")
	if err != nil || link.Purpose != "password_reset" || link.Status != "pending" || link.ExpiresAt.Before(time.Now().Add(71*time.Hour)) {
		t.Fatalf("link %+v %v", link, err)
	}
	to, text := f.lastMail(t)
	if !strings.Contains(to, "<anna@home.test>") || !strings.Contains(text, "reset the password for anna@acme.test") {
		t.Fatalf("reset mail to %s: %q", to, text)
	}
	firstToken := f.lastToken(t)
	look, err := f.svc.LookupInvite(ctx, firstToken)
	if err != nil || look.Purpose != "password_reset" || look.InviterName != "" || look.Name != "" {
		t.Fatalf("lookup %+v %v", look, err)
	}
	_, err = f.mb.SendPasswordLink(ctx, f.owner, res.Mailbox.UserUUID, "anna@home.test")
	wantStatus(t, err, http.StatusTooManyRequests)
	if _, err = f.db.Exec(`UPDATE org_invites SET created_at=created_at-interval '2 minutes' WHERE user_id=$1`, user); err != nil {
		t.Fatal(err)
	}
	// A recovery email is told when the link goes elsewhere.
	if _, err = f.db.Exec(`UPDATE mailbox_accounts SET recovery_email='anna@recovery.test' WHERE user_id=$1`, user); err != nil {
		t.Fatal(err)
	}
	sent := f.mailCount()
	if _, err = f.mb.SendPasswordLink(ctx, f.owner, res.Mailbox.UserUUID, "anna@home.test"); err != nil {
		t.Fatal(err)
	}
	if f.mailCount() != sent+2 {
		t.Fatalf("mails %d, want the link and a recovery notice", f.mailCount()-sent)
	}
	f.mu.Lock()
	linkMail, notice := f.mail[len(f.mail)-2], f.mail[len(f.mail)-1]
	f.mu.Unlock()
	if !strings.Contains(notice.To[0], "anna@recovery.test") || strings.Contains(notice.TextBody, "#token=") || !strings.Contains(linkMail.To[0], "anna@home.test") {
		t.Fatalf("link to %v, notice to %v: %q", linkMail.To, notice.To, notice.TextBody)
	}
	secondToken := inviteLink.FindStringSubmatch(linkMail.TextBody)[1]
	if _, err = f.svc.LookupInvite(ctx, firstToken); !errors.Is(err, ErrInviteInvalid) {
		t.Fatal("a new link did not revoke the previous one")
	}

	// A TOTP user gets the new password but is not signed in.
	if _, err = f.db.Exec(`UPDATE users SET totp_enabled=true,totp_secret='JBSWY3DPEHPK3PXP' WHERE id=$1`, user); err != nil {
		t.Fatal(err)
	}
	before = version()
	acc, err := f.svc.AcceptInvite(ctx, &AcceptInviteRequest{Token: secondToken, Password: "third-password"}, "")
	if err != nil || acc.SignedIn || acc.Token != "" || version() != before+1 {
		t.Fatalf("accept %+v %v", acc, err)
	}
	if lr, err := f.login("anna@acme.test", "third-password"); err != nil || !lr.RequiresTwoFactor {
		t.Fatalf("login after reset: %+v %v", lr, err)
	}

	// Set-password and the user's own change both revoke an open link.
	openLink := func() {
		t.Helper()
		if _, err := f.db.Exec(`UPDATE org_invites SET created_at=created_at-interval '2 minutes' WHERE user_id=$1`, user); err != nil {
			t.Fatal(err)
		}
		if _, err := f.mb.SendPasswordLink(ctx, f.owner, res.Mailbox.UserUUID, ""); err != nil {
			t.Fatal(err)
		}
	}
	openCount := func() int {
		return f.count(t, `SELECT count(*) FROM org_invites WHERE user_id=$1 AND accepted_at IS NULL AND revoked_at IS NULL`, user)
	}
	openLink()
	if _, err = f.mb.SetPassword(ctx, f.owner, res.Mailbox.UserUUID, "fourth-password"); err != nil || openCount() != 0 {
		t.Fatalf("set-password left a link open: %v", err)
	}
	if f.count(t, `SELECT count(*) FROM users WHERE id=$1 AND totp_enabled`, user) != 1 {
		t.Fatal("set-password turned TOTP off")
	}
	openLink()
	if err = NewSessionService(f.db, &config.Config{}).ChangePassword(ctx, user, "fourth-password", "fifth-password"); err != nil || openCount() != 0 {
		t.Fatalf("own change left a link open: %v", err)
	}
	// Five links in 24 h at most.
	for openCount() == 0 && f.count(t, `SELECT count(*) FROM org_invites WHERE user_id=$1 AND purpose='password_reset'`, user) < 5 {
		openLink()
		if _, err = f.db.Exec(`UPDATE org_invites SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, user); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.db.Exec(`UPDATE org_invites SET created_at=created_at-interval '2 minutes' WHERE user_id=$1`, user); err != nil {
		t.Fatal(err)
	}
	_, err = f.mb.SendPasswordLink(ctx, f.owner, res.Mailbox.UserUUID, "")
	wantStatus(t, err, http.StatusTooManyRequests)

	// Admin 2FA reset: TOTP off, signed out everywhere, audited.
	before = version()
	if _, err = f.mb.ResetTwoFactor(ctx, f.owner, res.Mailbox.UserUUID); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT count(*) FROM users WHERE id=$1 AND NOT totp_enabled AND totp_secret IS NULL AND cardinality(backup_codes)=0`, user) != 1 || version() != before+1 {
		t.Fatal("TOTP not cleared")
	}
	_, err = f.mb.ResetTwoFactor(ctx, f.owner, res.Mailbox.UserUUID)
	wantStatus(t, err, http.StatusConflict)
	for _, action := range []string{"mailbox_password_set", "mailbox_password_link", "mailbox_2fa_reset", "invite_accept"} {
		if f.count(t, `SELECT count(*) FROM audit_logs WHERE action=$1`, action) == 0 {
			t.Fatalf("no %s audit", action)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM audit_logs WHERE new_values::text ~ '(second|third|fourth|fifth)-password'`); n != 0 {
		t.Fatal("a password reached the audit log")
	}
	// Members, admins and owners have no admin reset.
	_, err = f.mb.SetPassword(ctx, f.owner, f.memberUUID, "member-password")
	wantStatus(t, err, http.StatusNotFound)
}

// An admin without a sending identity can still set a password, reset 2FA and
// change the recovery email of a mailbox with a recovery email; the notice is
// skipped and the audit row says so (spec: password mode works without one).
func TestMailboxAdminChangesWithoutSendingIdentity(t *testing.T) {
	f := newMailboxUserFixture(t)
	ctx := context.Background()
	res := f.create(t, "rae", password("initial-password"))
	user := f.userID(t, res.Mailbox.UserUUID)
	if _, err := f.mb.UpdateMailbox(ctx, f.admin, res.Mailbox.UserUUID, &UpdateMailboxRequest{RecoveryEmail: strp("rae@home.test")}); err != nil {
		t.Fatal(err)
	}
	// The admin's only sending identity stops sending.
	if _, err := f.db.Exec(`UPDATE identities SET can_send=false WHERE id=$1`, f.catchAll); err != nil {
		t.Fatal(err)
	}
	sent := f.mailCount()
	if _, err := f.mb.SetPassword(ctx, f.admin, res.Mailbox.UserUUID, "second-password"); err != nil {
		t.Fatal("set password without a sending identity:", err)
	}
	if _, err := f.login("rae@acme.test", "second-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE users SET totp_enabled=true WHERE id=$1`, user); err != nil {
		t.Fatal(err)
	}
	if _, err := f.mb.ResetTwoFactor(ctx, f.admin, res.Mailbox.UserUUID); err != nil {
		t.Fatal("2FA reset without a sending identity:", err)
	}
	if _, err := f.mb.UpdateMailbox(ctx, f.admin, res.Mailbox.UserUUID, &UpdateMailboxRequest{RecoveryEmail: strp("rae@new.test")}); err != nil {
		t.Fatal("recovery change without a sending identity:", err)
	}
	if f.mailCount() != sent {
		t.Fatal("a notice was sent without a sending identity")
	}
	if n := f.count(t, `SELECT count(DISTINCT action) FROM audit_logs WHERE action IN ('mailbox_password_set','mailbox_2fa_reset','mailbox_update') AND new_values->>'notified'='false'`); n != 3 ||
		f.count(t, `SELECT count(*) FROM audit_logs WHERE new_values->>'notified'='true'`) != 0 {
		t.Fatalf("%d actions record the skipped notice", n)
	}
}

// F6: suspend blocks sign-in but keeps mail arriving; forwards pause and stay
// paused after reactivation; auto-replies stop. "May receive" off sends mail
// to the catch-all.
func TestMailboxSuspendReactivate(t *testing.T) {
	f := newMailboxUserFixture(t)
	ctx := context.Background()
	res := f.create(t, "sam", password("sam-password"))
	user := f.userID(t, res.Mailbox.UserUUID)
	var identity int64
	if err := f.db.QueryRow(`SELECT id FROM identities WHERE user_id=$1`, user).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO email_forwards(user_id,org_id,identity_id,forward_to,status,active,verified,updated_at) VALUES($1,$2,$3,'out@elsewhere.test','active',true,true,now())`, user, f.org, identity); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO auto_replies(user_id,org_id,name,start_date,subject,html_content,identity_ids,active,updated_at) VALUES($1,$2,'Away',now()-interval '1 day','Away','x',ARRAY[$3::int],true,now())`, user, f.org, identity); err != nil {
		t.Fatal(err)
	}
	if id, _, err := activeRuleForIdentity(ctx, f.db, identity); err != nil || id == 0 {
		t.Fatalf("auto-reply not active before suspend: %d %v", id, err)
	}
	if _, err := f.login("sam@acme.test", "sam-password"); err != nil {
		t.Fatal(err)
	}
	_, err := f.mb.Reactivate(ctx, f.admin, res.Mailbox.UserUUID)
	wantStatus(t, err, http.StatusConflict)

	box, err := f.mb.Suspend(ctx, f.admin, res.Mailbox.UserUUID)
	if err != nil || box.Status != "suspended" {
		t.Fatalf("suspend %+v %v", box, err)
	}
	if _, err = f.login("sam@acme.test", "sam-password"); err == nil {
		t.Fatal("suspended user signed in")
	}
	if f.count(t, `SELECT count(*) FROM user_sessions WHERE user_id=$1 AND active`, user) != 0 {
		t.Fatal("session survived suspend")
	}
	if f.count(t, `SELECT count(*) FROM email_forwards WHERE user_id=$1 AND status='paused' AND NOT active`, user) != 1 {
		t.Fatal("forward not paused")
	}
	if id, _, err := activeRuleForIdentity(ctx, f.db, identity); err != nil || id != 0 {
		t.Fatalf("auto-reply still active for a suspended owner: %d %v", id, err)
	}
	f.ingestShared(t, "while-suspended", "sam@acme.test")
	if f.copies(t, "while-suspended", user) != 1 || f.copies(t, "while-suspended", f.admin.UserID) != 0 {
		t.Fatal("suspended mailbox did not keep receiving")
	}
	_, err = f.mb.Suspend(ctx, f.admin, res.Mailbox.UserUUID)
	wantStatus(t, err, http.StatusConflict)

	box, err = f.mb.Reactivate(ctx, f.admin, res.Mailbox.UserUUID)
	if err != nil || box.Status != "active" {
		t.Fatalf("reactivate %+v %v", box, err)
	}
	if _, err = f.login("sam@acme.test", "sam-password"); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT count(*) FROM email_forwards WHERE user_id=$1 AND status='paused' AND NOT active`, user) != 1 {
		t.Fatal("forward resumed by itself")
	}
	for _, action := range []string{"mailbox_suspend", "mailbox_reactivate"} {
		if f.count(t, `SELECT count(*) FROM audit_logs WHERE action=$1 AND resource_id=$2`, action, res.Mailbox.UserUUID) != 1 {
			t.Fatalf("no %s audit", action)
		}
	}

	if _, err = f.db.Exec(`UPDATE identities SET can_receive=false WHERE id=$1`, identity); err != nil {
		t.Fatal(err)
	}
	f.ingestShared(t, "receive-off", "sam@acme.test")
	if f.copies(t, "receive-off", user) != 0 || f.copies(t, "receive-off", f.admin.UserID) != 1 {
		t.Fatal("may-receive off did not fall back to the catch-all")
	}
}

// F7 and re-creation: removal (pending or active) falls back to the
// catch-all and revokes links and aliases; re-creating the address reuses the
// identity while the old mail stays with the removed login.
func TestMailboxRemoveAndRecreate(t *testing.T) {
	f := newMailboxUserFixture(t)
	ctx := context.Background()
	pending := f.create(t, "pat", invite("pat@elsewhere.test")).Mailbox
	pendingID := f.userID(t, pending.UserUUID)
	if _, err := f.db.Exec(`INSERT INTO identity_send_aliases(identity_id,address) SELECT id,'pat.alias@acme.test' FROM identities WHERE user_id=$1`, pendingID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE identities SET wildcard_sender=true WHERE user_id=$1`, pendingID); err != nil {
		t.Fatal(err)
	}
	// A member cannot be removed through the mailbox route.
	_, err := f.mb.Remove(ctx, f.admin, f.memberUUID, "")
	wantStatus(t, err, http.StatusNotFound)
	// Admins may remove mailboxes.
	if _, err = f.mb.Remove(ctx, f.admin, pending.UserUUID, ""); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT count(*) FROM users WHERE id=$1 AND status='disabled' AND removed_at IS NOT NULL`, pendingID) != 1 ||
		f.count(t, `SELECT count(*) FROM org_invites WHERE user_id=$1 AND revoked_at IS NULL`, pendingID) != 0 ||
		f.count(t, `SELECT count(*) FROM identity_send_aliases`) != 0 ||
		f.count(t, `SELECT count(*) FROM identities WHERE user_id=$1 AND (wildcard_sender OR can_receive OR can_send)`, pendingID) != 0 ||
		f.count(t, `SELECT count(*) FROM mailbox_accounts WHERE user_id=$1 AND removed_at IS NOT NULL`, pendingID) != 1 {
		t.Fatal("pending removal left something behind")
	}
	f.ingestShared(t, "after-pending-removal", "pat@acme.test")
	f.ingestShared(t, "alias-after-removal", "pat.alias@acme.test")
	if f.copies(t, "after-pending-removal", f.admin.UserID) != 1 || f.copies(t, "alias-after-removal", f.admin.UserID) != 1 {
		t.Fatal("removed mailbox did not fall back to the catch-all")
	}
	_, err = f.mb.Remove(ctx, f.admin, pending.UserUUID, "")
	wantStatus(t, err, http.StatusNotFound)

	// An active mailbox with mail, removed and then re-created.
	old := f.create(t, "kim", password("kim-password")).Mailbox
	oldID := f.userID(t, old.UserUUID)
	f.ingestShared(t, "old-mail", "kim@acme.test")
	var identity int64
	if err = f.db.QueryRow(`SELECT id FROM identities WHERE user_id=$1`, oldID).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	// Personal data on the identity must not pass to the next person.
	if _, err = f.db.Exec(`UPDATE identities SET signature_html='<p>Kim, CFO, +1 555</p>',signature_text='Kim, CFO, +1 555',color='#FF0000' WHERE id=$1`, identity); err != nil {
		t.Fatal(err)
	}
	if _, err = f.mb.Remove(ctx, f.owner, old.UserUUID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = f.login("kim@acme.test", "kim-password"); err == nil {
		t.Fatal("removed mailbox signed in")
	}
	again := f.create(t, "kim", invite("kim@elsewhere.test")).Mailbox
	newID := f.userID(t, again.UserUUID)
	if again.IdentityUUID != old.IdentityUUID || newID == oldID || again.Status != "invited" || !again.MayReceive {
		t.Fatalf("re-created %+v (old %+v)", again, old)
	}
	if f.count(t, `SELECT count(*) FROM identities WHERE id=$1 AND signature_html IS NULL AND signature_text IS NULL AND color='#3B82F6'`, identity) != 1 {
		t.Fatal("re-created mailbox kept the previous signature")
	}
	if f.count(t, `SELECT count(*) FROM users WHERE id=$1 AND email LIKE 'removed+%@invalid'`, oldID) != 1 {
		t.Fatal("old login not renamed")
	}
	if f.count(t, `SELECT count(*) FROM mailbox_accounts WHERE identity_id=$1`, identity) != 2 {
		t.Fatal("old mailbox row not kept")
	}
	if f.copies(t, "old-mail", oldID) != 1 || f.count(t, `SELECT count(*) FROM received_emails WHERE mailbox_owner_id=$1`, newID) != 0 {
		t.Fatal("old mail moved to the new mailbox")
	}
	f.ingestShared(t, "new-mail", "kim@acme.test")
	if f.copies(t, "new-mail", newID) != 1 {
		t.Fatal("re-created mailbox does not receive")
	}
}

// Mailbox users and their setup links use no seat; max_identities applies.
func TestMailboxUsesNoSeatButCountsIdentities(t *testing.T) {
	f := newMailboxUserFixture(t)
	ctx := context.Background()
	if _, err := f.db.Exec(`UPDATE organizations SET max_users=4,max_identities=4 WHERE id=$1`, f.org); err != nil {
		t.Fatal(err)
	}
	f.create(t, "one", invite("one@elsewhere.test"))
	f.create(t, "two", password("two-password"))
	// Three active staff, a pending and an active mailbox: one seat is still free.
	if _, err := f.svc.CreateInvite(ctx, f.owner, &CreateInviteRequest{Email: "new@elsewhere.test", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	_, err := f.svc.CreateInvite(ctx, f.owner, &CreateInviteRequest{Email: "full@elsewhere.test", Role: "member"})
	wantStatus(t, err, http.StatusConflict)
	_, err = f.mb.CreateMailbox(ctx, f.owner, f.domain, &CreateMailboxRequest{LocalPart: "three", Name: "Three", Access: password("three-password")})
	wantStatus(t, err, http.StatusConflict) // owner@, catchall@, one@, two@
}

func TestMailboxCreateValidation(t *testing.T) {
	f := newMailboxUserFixture(t)
	ctx := context.Background()
	create := func(a OrgActor, domain, local string, access MailboxAccessRequest) error {
		_, err := f.mb.CreateMailbox(ctx, a, domain, &CreateMailboxRequest{LocalPart: local, Name: "Someone", Access: access})
		return err
	}
	pw := password("long-enough")
	wantStatus(t, create(f.owner, f.domain, "a+b", pw), http.StatusBadRequest)
	wantStatus(t, create(f.owner, f.domain, "a..b", pw), http.StatusBadRequest)
	wantStatus(t, create(f.owner, f.domain, strings.Repeat("a", 65), pw), http.StatusBadRequest)
	wantStatus(t, create(f.owner, f.domain, "ok", password("short")), http.StatusBadRequest)
	wantStatus(t, create(f.owner, f.domain, "ok", MailboxAccessRequest{Mode: "magic"}), http.StatusBadRequest)
	wantStatus(t, create(f.owner, f.domain, "ok", invite("ok@acme.test")), http.StatusBadRequest)
	wantStatus(t, create(f.owner, "00000000-0000-0000-0000-000000000000", "ok", pw), http.StatusNotFound)
	if _, err := f.db.Exec(`INSERT INTO domains(id,org_id,name,verification_token,status,ses_verified,updated_at) VALUES(51,$1,'unverified.test','t','active',false,now())`, f.org); err != nil {
		t.Fatal(err)
	}
	var unverified string
	if err := f.db.QueryRow(`SELECT uuid::text FROM domains WHERE id=51`).Scan(&unverified); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, create(f.owner, unverified, "ok", pw), http.StatusBadRequest)
	// Addresses in use: an identity, the catch-all, a shared mailbox, an alias,
	// another org's login, an open invite.
	f.sharedMailbox(t, "team@acme.test", nil)
	if _, err := f.db.Exec(`INSERT INTO identity_send_aliases(identity_id,address) SELECT id,'alias@acme.test' FROM identities WHERE email='owner@acme.test'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(900,'Other','other',now());
		INSERT INTO users(org_id,email,password_hash,role,updated_at) VALUES(900,'elsewhere@acme.test','x','owner',now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateInvite(ctx, f.owner, &CreateInviteRequest{Email: "invited@acme.test", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	for _, local := range []string{"owner", "catchall", "team", "alias", "elsewhere", "invited", "member"} {
		if err := create(f.owner, f.domain, local, pw); err == nil {
			t.Fatalf("%s@ was free", local)
		} else {
			wantStatus(t, err, http.StatusConflict)
		}
	}
	// Invite mode needs a sending identity; password mode does not.
	if _, err := f.db.Exec(`UPDATE identities SET can_send=false WHERE id=$1`, f.catchAll); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, create(f.admin, f.domain, "ok", invite("ok@elsewhere.test")), http.StatusConflict)
	if err := create(f.admin, f.domain, "ok", pw); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT count(*) FROM users WHERE role='mailbox'`) != 1 {
		t.Fatal("a rejected create left a user behind")
	}
}

// Guards elsewhere keep a mailbox login from losing its mailbox or growing
// beyond its domain.
func TestMailboxGuards(t *testing.T) {
	f := newMailboxUserFixture(t)
	ctx := context.Background()
	box := f.create(t, "guard", password("guard-password")).Mailbox
	boxID := f.userID(t, box.UserUUID)
	cfg := &config.Config{EmailProvider: "ses"}

	_, err := f.svc.ChangeRole(ctx, f.owner, box.UserUUID, "member")
	wantStatus(t, err, http.StatusConflict)

	wantStatus(t, NewDomainService(f.db, cfg).DeleteDomain(ctx, f.org, f.domain), http.StatusConflict)
	wantStatus(t, NewIdentityService(f.db, cfg).DeleteIdentity(ctx, f.owner.UserID, box.IdentityUUID), http.StatusConflict)
	_, err = f.svc.TransferIdentity(ctx, f.owner, box.IdentityUUID, f.memberUUID)
	wantStatus(t, err, http.StatusConflict)

	ids, err := f.svc.ListOrgIdentities(ctx, f.org)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range ids {
		if i.MailboxPrimary != (i.UUID == box.IdentityUUID) {
			t.Fatalf("mailboxPrimary wrong for %s", i.Email)
		}
	}

	// A mailbox user only gets identities on their own domain.
	if _, err = f.db.Exec(`INSERT INTO domains(id,org_id,name,verification_token,status,ses_verified,updated_at) VALUES(52,$1,'other.test','t','active',true,now())`, f.org); err != nil {
		t.Fatal(err)
	}
	var otherDomain, otherIdentity, sameIdentity string
	if err = f.db.QueryRow(`SELECT uuid::text FROM domains WHERE id=52`).Scan(&otherDomain); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow(`INSERT INTO identities(user_id,domain_id,email,updated_at) VALUES($1,52,'m@other.test',now()) RETURNING uuid::text`, f.member.UserID).Scan(&otherIdentity); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow(`INSERT INTO identities(user_id,domain_id,email,updated_at) VALUES($1,50,'shift@acme.test',now()) RETURNING uuid::text`, f.owner.UserID).Scan(&sameIdentity); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.TransferIdentity(ctx, f.owner, otherIdentity, box.UserUUID)
	wantStatus(t, err, http.StatusBadRequest)
	if _, err = f.svc.TransferIdentity(ctx, f.owner, sameIdentity, box.UserUUID); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.RemoveMember(ctx, f.owner, f.memberUUID, box.UserUUID)
	wantStatus(t, err, http.StatusBadRequest)
	idSvc := NewIdentityService(f.db, cfg)
	if _, err = idSvc.CreateIdentity(ctx, f.owner.UserID, &model.CreateIdentityRequest{DomainId: otherDomain, Email: "x@other.test", DisplayName: "X", OwnerUserUuid: box.UserUUID}); err == nil {
		t.Fatal("mailbox user got an identity on another domain")
	}
	if _, err = idSvc.CreateIdentity(ctx, f.owner.UserID, &model.CreateIdentityRequest{DomainId: f.domain, Email: "guard2@acme.test", DisplayName: "G", OwnerUserUuid: box.UserUUID}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`INSERT INTO identity_send_aliases(identity_id,address) SELECT id,'held@acme.test' FROM identities WHERE user_id=$1 AND email='guard@acme.test'`, boxID); err != nil {
		t.Fatal(err)
	}
	if _, err = idSvc.CreateIdentity(ctx, f.owner.UserID, &model.CreateIdentityRequest{DomainId: f.domain, Email: "held@acme.test", DisplayName: "H"}); err == nil {
		t.Fatal("an identity was created over a send-as alias")
	}

	// Shared mailboxes: read/send yes, manage no.
	shared := &SharedMailboxService{db: f.db, cfg: cfg}
	team, _ := f.sharedMailbox(t, "team@acme.test", [][2]any{{f.owner.UserID, true}})
	_, err = shared.AddMember(ctx, f.owner, int(team), &AddMemberInput{UserUUID: box.UserUUID, CanRead: true, CanManage: true})
	wantStatus(t, err, http.StatusBadRequest)
	if _, err = shared.AddMember(ctx, f.owner, int(team), &AddMemberInput{UserUUID: box.UserUUID, CanRead: true, CanSend: true}); err != nil {
		t.Fatal(err)
	}
	_, err = shared.UpdateMember(ctx, f.owner, int(team), box.UserUUID, &UpdateMemberInput{CanRead: true, CanManage: true})
	wantStatus(t, err, http.StatusBadRequest)

	// The member list hides mailbox users unless asked.
	for _, include := range []bool{false, true} {
		members, err := f.svc.ListMembers(ctx, f.org, include)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, m := range members {
			found = found || m.UUID == box.UserUUID
		}
		if found != include {
			t.Fatalf("includeMailboxes=%v listed the mailbox: %v", include, found)
		}
	}
}

// Two creates of one address, and a create racing CreateIdentity, end with
// exactly one owner and no deadlock.
func TestMailboxCreateRaces(t *testing.T) {
	f := newMailboxUserFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	idSvc := NewIdentityService(f.db, &config.Config{EmailProvider: "ses"})
	for round, rival := range []string{"mailbox", "identity"} {
		local := []string{"race", "clash"}[round]
		errs := make([]error, 2)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range errs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				if i == 1 && rival == "identity" {
					_, errs[i] = idSvc.CreateIdentity(ctx, f.owner.UserID, &model.CreateIdentityRequest{DomainId: f.domain, Email: local + "@acme.test", DisplayName: "Rival"})
					return
				}
				_, errs[i] = f.mb.CreateMailbox(ctx, f.owner, f.domain, &CreateMailboxRequest{LocalPart: local, Name: "Racer", Access: password("race-password")})
			}(i)
		}
		close(start)
		wg.Wait()
		if ctx.Err() != nil {
			t.Fatal("create race deadlocked")
		}
		if (errs[0] == nil) == (errs[1] == nil) {
			t.Fatalf("%s race: want exactly one success, got %v / %v", rival, errs[0], errs[1])
		}
		if f.count(t, `SELECT count(*) FROM identities WHERE email=$1`, local+"@acme.test") != 1 {
			t.Fatalf("%s race: identities for %s", rival, local)
		}
		if f.count(t, `SELECT count(*) FROM mailbox_accounts ma JOIN identities i ON i.id=ma.identity_id WHERE i.email=$1`, local+"@acme.test") != f.count(t, `SELECT count(*) FROM users WHERE email=$1`, local+"@acme.test") {
			t.Fatalf("%s race: mailbox row without its login, or the reverse", rival)
		}
	}
}

// Accepting a setup link while the mailbox is removed: either order is fine,
// but the result is always a removed login and no deadlock.
func TestMailboxAcceptRacesRemove(t *testing.T) {
	f := newMailboxUserFixture(t)
	if _, err := f.db.Exec(`INSERT INTO identities(user_id,domain_id,email,can_send,is_default,updated_at) VALUES($1,50,'admin@acme.test',true,true,now())`, f.admin.UserID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for round := 0; round < 3; round++ {
		box := f.create(t, "racer"+itoa(int64(round)), invite("racer@elsewhere.test")).Mailbox
		token := f.lastToken(t)
		var acceptErr, removeErr error
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, acceptErr = f.svc.AcceptInvite(ctx, &AcceptInviteRequest{Token: token, Name: "Racer", Password: "race-password"}, "")
		}()
		go func() {
			defer wg.Done()
			<-start
			_, removeErr = f.mb.Remove(ctx, f.admin, box.UserUUID, "")
		}()
		close(start)
		wg.Wait()
		if ctx.Err() != nil {
			t.Fatal("accept/remove deadlocked")
		}
		if removeErr != nil || (acceptErr != nil && !errors.Is(acceptErr, ErrInviteInvalid)) {
			t.Fatalf("round %d: accept %v, remove %v", round, acceptErr, removeErr)
		}
		user := f.userID(t, box.UserUUID)
		if f.count(t, `SELECT count(*) FROM users WHERE id=$1 AND status='disabled'`, user) != 1 ||
			f.count(t, `SELECT count(*) FROM user_sessions WHERE user_id=$1 AND active`, user) != 0 ||
			f.count(t, `SELECT count(*) FROM org_invites WHERE user_id=$1 AND accepted_at IS NULL AND revoked_at IS NULL`, user) != 0 {
			t.Fatalf("round %d: inconsistent end state (accept err %v)", round, acceptErr)
		}
	}
}

// A mailbox removed between routing and delivery: the message is routed
// again and lands with the catch-all, never with the removed login.
func TestMailboxIngestRacesRemove(t *testing.T) {
	f := newMailboxUserFixture(t)
	box := f.create(t, "gone", password("gone-password")).Mailbox
	user := f.userID(t, box.UserUUID)
	if _, err := f.db.Exec(`UPDATE domains SET receiving_enabled=true WHERE id=50`); err != nil {
		t.Fatal(err)
	}
	const sesID = "raced-removal"
	storage := &fakeIncomingStorage{raw: []byte("From: Sender <sender@example.test>\r\nTo: gone@acme.test\r\nMessage-ID: <" + sesID + "@example.test>\r\nSubject: Hello\r\n\r\nbody")}
	removed := false
	svc := &ReceivingService{db: f.db, storage: storage, beforeIngestTx: func() {
		if !removed {
			removed = true
			if _, err := f.mb.Remove(context.Background(), f.owner, box.UserUUID, ""); err != nil {
				t.Error(err)
			}
		}
	}}
	auth := &ReceivingAuthorization{OrgID: f.org, TopicARN: "arn:aws:sns:us-east-1:123456789012:acme", Bucket: "acme-bucket", Region: "us-east-1"}
	n := &model.SESNotification{NotificationType: "Received", Mail: model.SESMail{MessageId: sesID}, Receipt: &model.SESReceipt{Timestamp: "2026-10-07T00:00:00Z", Recipients: []string{"gone@acme.test"},
		Action: model.SESAction{Type: "S3", BucketName: auth.Bucket, ObjectKey: "incoming/acme.test/" + sesID}}}
	if err := svc.ProcessIncomingEmail(context.Background(), auth, n); err != nil {
		t.Fatal(err)
	}
	if f.copies(t, sesID, user) != 0 || f.copies(t, sesID, f.admin.UserID) != 1 {
		t.Fatal("mail raced with removal did not re-route to the catch-all")
	}
}
