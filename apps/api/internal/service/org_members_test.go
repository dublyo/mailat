package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
	"github.com/dublyo/mailat/api/internal/worker"
)

type orgFixture struct {
	db                    *sql.DB
	svc                   *OrgMemberService
	auth                  *AuthService
	org                   int64
	owner, admin, member  OrgActor
	memberUUID, adminUUID string
	mu                    sync.Mutex
	mail                  []*worker.EmailSendPayload
}

func newOrgFixture(t *testing.T) *orgFixture {
	t.Helper()
	db := testutil.Database(t)
	f := &orgFixture{db: db}
	if err := db.QueryRow(`INSERT INTO organizations(name,slug,max_users,updated_at) VALUES('Acme','acme',5,now()) RETURNING id`).Scan(&f.org); err != nil {
		t.Fatal(err)
	}
	user := func(email, role string) (OrgActor, string) {
		var id int64
		var u string
		if err := db.QueryRow(`INSERT INTO users(org_id,email,password_hash,name,role,updated_at) VALUES($1,$2,'unused',$3,$4,now()) RETURNING id,uuid::text`, f.org, email, role+" person", role).Scan(&id, &u); err != nil {
			t.Fatal(err)
		}
		return OrgActor{UserID: id, OrgID: f.org, Role: role, IP: "192.0.2.10"}, u
	}
	f.owner, _ = user("owner@acme.test", "owner")
	f.admin, f.adminUUID = user("admin@acme.test", "admin")
	f.member, f.memberUUID = user("member@acme.test", "member")
	if _, err := db.Exec(`INSERT INTO domains(id,org_id,name,verification_token,status,ses_verified,updated_at) VALUES(50,$1,'acme.test','t','active',true,now())`, f.org); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO identities(user_id,domain_id,email,display_name,can_send,is_default,updated_at) VALUES($1,50,'owner@acme.test','Owner',true,true,now())`, f.owner.UserID); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{EmailProvider: "ses", JWTSecret: "org-members-fixture-not-a-secret", WebUrl: "https://mail.acme.test/"}
	f.auth = NewAuthService(db, cfg)
	f.svc = NewOrgMemberService(db, cfg, f.auth)
	f.svc.SetSender(&TransactionalService{db: db, cfg: cfg, emailProvider: &mailboxTestProvider{}})
	f.svc.dispatch = func(p *worker.EmailSendPayload) {
		f.mu.Lock()
		f.mail = append(f.mail, p)
		f.mu.Unlock()
	}
	return f
}

var inviteLink = regexp.MustCompile(`https://mail\.acme\.test/invite#token=([A-Za-z0-9_-]{43})`)

func (f *orgFixture) lastToken(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.mail) == 0 {
		t.Fatal("no invite mail")
	}
	m := inviteLink.FindStringSubmatch(f.mail[len(f.mail)-1].TextBody)
	if m == nil {
		t.Fatalf("invite link missing: %q", f.mail[len(f.mail)-1].TextBody)
	}
	return m[1]
}

func wantStatus(t *testing.T, err error, status int) {
	t.Helper()
	var orgErr *OrgError
	if !errors.As(err, &orgErr) || orgErr.Status != status {
		t.Fatalf("want %d, got %v", status, err)
	}
}

func (f *orgFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestInviteLifecycle(t *testing.T) {
	f := newOrgFixture(t)
	ctx := context.Background()

	wantStatus(t, func() error {
		_, err := f.svc.CreateInvite(ctx, f.admin, &CreateInviteRequest{Email: "x@elsewhere.test", Role: "admin"})
		return err
	}(), http.StatusForbidden)
	// The admin has no sending identity of their own.
	_, err := f.svc.CreateInvite(ctx, f.admin, &CreateInviteRequest{Email: "x@elsewhere.test", Role: "member"})
	wantStatus(t, err, http.StatusConflict)

	invite, err := f.svc.CreateInvite(ctx, f.owner, &CreateInviteRequest{Email: " New@Elsewhere.test ", Role: "member"})
	if err != nil {
		t.Fatal(err)
	}
	if invite.Email != "new@elsewhere.test" || invite.Status != "pending" || invite.SendCount != 1 || invite.InvitedBy != "owner@acme.test" {
		t.Fatalf("%+v", invite)
	}
	token := f.lastToken(t)
	if !strings.Contains(f.mail[0].To[0], "new@elsewhere.test") {
		t.Fatal(f.mail[0].To)
	}
	if n := f.count(t, `SELECT count(*) FROM org_invites WHERE token_hash=$1 AND token_hash<>$2`, hashToken(token), token); n != 1 {
		t.Fatal("only the token hash must be stored")
	}
	if n := f.count(t, `SELECT count(*) FROM transactional_emails WHERE system_kind='invite' AND system_ref=$1 AND system_user_id=$2`, invite.UUID, f.owner.UserID); n != 1 {
		t.Fatal("invite mail is not a recorded system send")
	}
	for _, email := range []string{"new@elsewhere.test", "member@acme.test"} {
		_, err = f.svc.CreateInvite(ctx, f.owner, &CreateInviteRequest{Email: email, Role: "member"})
		wantStatus(t, err, http.StatusConflict)
	}
	var other int64
	if err = f.db.QueryRow(`INSERT INTO organizations(name,slug,updated_at) VALUES('Other','other',now()) RETURNING id`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`INSERT INTO users(org_id,email,password_hash,status,updated_at) VALUES($1,'taken@other.test','x','disabled',now())`, other); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.CreateInvite(ctx, f.owner, &CreateInviteRequest{Email: "taken@other.test", Role: "member"})
	wantStatus(t, err, http.StatusConflict)

	lookup, err := f.svc.LookupInvite(ctx, token)
	if err != nil || lookup.OrgName != "Acme" || lookup.Email != "new@elsewhere.test" || lookup.Role != "member" || lookup.InviterName != "owner person" {
		t.Fatalf("%+v %v", lookup, err)
	}

	// Resend: cooldown, then a fresh token that replaces the old one.
	_, err = f.svc.ResendInvite(ctx, f.owner, invite.UUID)
	wantStatus(t, err, http.StatusTooManyRequests)
	if _, err = f.db.Exec(`UPDATE org_invites SET last_sent_at=now()-interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	resent, err := f.svc.ResendInvite(ctx, f.owner, invite.UUID)
	if err != nil || resent.SendCount != 2 {
		t.Fatal(resent, err)
	}
	fresh := f.lastToken(t)
	if _, err = f.svc.LookupInvite(ctx, token); !errors.Is(err, ErrInviteInvalid) {
		t.Fatal("the replaced token still works", err)
	}

	_, err = f.svc.AcceptInvite(ctx, &AcceptInviteRequest{Token: fresh, Name: "New Person", Password: "short"}, "")
	wantStatus(t, err, http.StatusBadRequest)
	res, err := f.svc.AcceptInvite(ctx, &AcceptInviteRequest{Token: fresh, Name: "New Person", Password: "a-long-password"}, "198.51.100.7")
	if err != nil {
		t.Fatal(err)
	}
	if res.Token == "" || res.User.Email != "new@elsewhere.test" || res.User.Role != "member" || res.User.OrgID != f.org || !res.User.EmailVerified {
		t.Fatalf("%+v", res.User)
	}
	if n := f.count(t, `SELECT count(*) FROM user_sessions WHERE user_id=$1 AND active`, res.User.ID); n != 1 {
		t.Fatal("accept did not sign in")
	}
	if login, err := f.auth.Login(ctx, &model.LoginRequest{Email: "new@elsewhere.test", Password: "a-long-password"}); err != nil || login.Token == "" {
		t.Fatal("password from the invite does not sign in", err)
	}
	if _, err = f.svc.AcceptInvite(ctx, &AcceptInviteRequest{Token: fresh, Name: "Again", Password: "a-long-password"}, ""); !errors.Is(err, ErrInviteInvalid) {
		t.Fatal("accepted twice", err)
	}
	if _, err = f.svc.LookupInvite(ctx, fresh); !errors.Is(err, ErrInviteInvalid) {
		t.Fatal(err)
	}
	invites, err := f.svc.ListInvites(ctx, f.org)
	if err != nil || len(invites) != 1 || invites[0].Status != "accepted" {
		t.Fatalf("%+v %v", invites, err)
	}

	// Expired and revoked invites are unusable; revoking twice is 404.
	if _, err = f.svc.CreateInvite(ctx, f.owner, &CreateInviteRequest{Email: "late@elsewhere.test", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	late := f.lastToken(t)
	if _, err = f.db.Exec(`UPDATE org_invites SET expires_at=now()-interval '1 second' WHERE email='late@elsewhere.test'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.LookupInvite(ctx, late); !errors.Is(err, ErrInviteInvalid) {
		t.Fatal("expired invite usable", err)
	}
	if _, err = f.svc.AcceptInvite(ctx, &AcceptInviteRequest{Token: late, Name: "Late", Password: "a-long-password"}, ""); !errors.Is(err, ErrInviteInvalid) {
		t.Fatal("expired invite accepted", err)
	}
	// An expired invite does not block a new one for the same address.
	again, err := f.svc.CreateInvite(ctx, f.owner, &CreateInviteRequest{Email: "late@elsewhere.test", Role: "member"})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.RevokeInvite(ctx, f.admin, again.UUID); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, f.svc.RevokeInvite(ctx, f.admin, again.UUID), http.StatusNotFound)
	if _, err = f.svc.LookupInvite(ctx, f.lastToken(t)); !errors.Is(err, ErrInviteInvalid) {
		t.Fatal("revoked invite usable", err)
	}

	for _, action := range []string{"invite_create", "invite_resend", "invite_accept", "invite_revoke"} {
		if n := f.count(t, `SELECT count(*) FROM audit_logs WHERE org_id=$1 AND action=$2`, f.org, action); n == 0 {
			t.Errorf("audit entry %s missing", action)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM audit_logs WHERE action='invite_accept' AND ip_address='198.51.100.7'`); n != 1 {
		t.Error("accept audit lacks the client IP")
	}
}

func TestInviteSeatsAndConcurrentAccept(t *testing.T) {
	f := newOrgFixture(t)
	ctx := context.Background()
	// 3 active users + 1 open invite = 4 of 5 seats.
	if _, err := f.svc.CreateInvite(ctx, f.owner, &CreateInviteRequest{Email: "one@elsewhere.test", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	token := f.lastToken(t)
	if _, err := f.svc.CreateInvite(ctx, f.owner, &CreateInviteRequest{Email: "two@elsewhere.test", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	_, err := f.svc.CreateInvite(ctx, f.owner, &CreateInviteRequest{Email: "three@elsewhere.test", Role: "member"})
	wantStatus(t, err, http.StatusConflict)

	var wg sync.WaitGroup
	results := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.svc.AcceptInvite(ctx, &AcceptInviteRequest{Token: token, Name: "Racer", Password: "a-long-password"}, "")
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	ok := 0
	for err := range results {
		if err == nil {
			ok++
		} else if !errors.Is(err, ErrInviteInvalid) {
			t.Fatal(err)
		}
	}
	if ok != 1 || f.count(t, `SELECT count(*) FROM users WHERE email='one@elsewhere.test'`) != 1 {
		t.Fatalf("concurrent accept: %d successes", ok)
	}

	// Seats are rechecked at accept time.
	if _, err = f.db.Exec(`UPDATE organizations SET max_users=4 WHERE id=$1`, f.org); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`UPDATE org_invites SET last_sent_at=now()-interval '2 minutes' WHERE email='two@elsewhere.test'`); err != nil {
		t.Fatal(err)
	}
	var twoUUID string
	if err = f.db.QueryRow(`SELECT uuid::text FROM org_invites WHERE email='two@elsewhere.test'`).Scan(&twoUUID); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.ResendInvite(ctx, f.owner, twoUUID)
	wantStatus(t, err, http.StatusConflict)
}

func TestRemoveMemberHandsOverEverything(t *testing.T) {
	f := newOrgFixture(t)
	ctx := context.Background()
	m := f.member.UserID
	var personal, other, shared int64
	if err := f.db.QueryRow(`INSERT INTO identities(user_id,domain_id,email,can_send,is_default,is_catch_all,updated_at) VALUES($1,50,'m1@acme.test',true,true,true,now()) RETURNING id`, m).Scan(&personal); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`INSERT INTO identities(user_id,domain_id,email,can_send,updated_at) VALUES($1,50,'m2@acme.test',true,now()) RETURNING id`, m).Scan(&other); err != nil {
		t.Fatal(err)
	}
	// A shared mailbox stewarded by the member, with copies for the member and the owner.
	if err := f.db.QueryRow(`INSERT INTO identities(user_id,domain_id,email,kind,can_send,updated_at) VALUES($1,50,'team@acme.test','shared',true,now()) RETURNING id`, m).Scan(&shared); err != nil {
		t.Fatal(err)
	}
	seed := []string{
		`INSERT INTO shared_mailboxes(id,org_id,name,email,identity_id,updated_at) VALUES(70,$1,'Team','team@acme.test',` + itoa(shared) + `,now())`,
		`INSERT INTO shared_mailbox_members(shared_mailbox_id,user_id,can_read,can_send) VALUES(70,` + itoa(m) + `,true,true),(70,` + itoa(f.owner.UserID) + `,true,false)`,
		`INSERT INTO received_emails(org_id,domain_id,identity_id,message_id,from_email,subject,ses_message_id,raw_s3_bucket,raw_s3_key,mailbox_owner_id,updated_at)
			SELECT $1,50,` + itoa(shared) + `,'s','a@sender.test','S','ses-s','bucket','raw/s',u,now() FROM unnest(ARRAY[` + itoa(m) + `,` + itoa(f.owner.UserID) + `]) u`,
		`INSERT INTO received_emails(org_id,domain_id,identity_id,message_id,from_email,subject,ses_message_id,updated_at) SELECT $1,50,` + itoa(personal) + `,'p','a@sender.test','P','ses-p',now()`,
		`INSERT INTO email_forwards(user_id,org_id,identity_id,forward_to,status,active,verified,updated_at) SELECT ` + itoa(m) + `,$1,` + itoa(personal) + `,'out@elsewhere.test','active',true,true,now()`,
		`INSERT INTO auto_replies(user_id,org_id,name,start_date,subject,html_content,identity_ids,active,updated_at) SELECT ` + itoa(m) + `,$1,'Away',now(),'Away','x',ARRAY[` + itoa(personal) + `],true,now()`,
		`INSERT INTO auto_replies(user_id,org_id,name,start_date,subject,html_content,identity_ids,active,updated_at) SELECT ` + itoa(m) + `,$1,'All',now(),'All','x','{}',true,now()`,
		`INSERT INTO api_keys(org_id,user_id,name,key_prefix,key_hash) SELECT $1,` + itoa(m) + `,'k','ue_x','hash-m'`,
		`INSERT INTO push_subscriptions(user_id,endpoint,p256dh_key,auth_key) SELECT ` + itoa(m) + `,'https://push.example.test/m','k','a' WHERE $1>0`,
		`INSERT INTO oauth_connections(user_id,provider,provider_user_id,updated_at) SELECT ` + itoa(m) + `,'google','g-member',now() WHERE $1>0`,
		`INSERT INTO user_sessions(user_id,org_id,token_hash,expires_at) SELECT ` + itoa(m) + `,$1,'session-m',now()+interval '1 day'`,
	}
	for _, q := range seed {
		args := []any{f.org}
		if !strings.Contains(q, "$1") {
			args = nil
		}
		if _, err := f.db.Exec(q, args...); err != nil {
			t.Fatal(q, err)
		}
	}

	_, err := f.svc.RemoveMember(ctx, f.admin, f.adminUUID, "")
	wantStatus(t, err, http.StatusConflict)
	var ownerUUID string
	if err = f.db.QueryRow(`SELECT uuid::text FROM users WHERE id=$1`, f.owner.UserID).Scan(&ownerUUID); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.RemoveMember(ctx, f.admin, ownerUUID, "")
	wantStatus(t, err, http.StatusConflict)
	_, err = f.svc.RemoveMember(ctx, f.member, f.adminUUID, "")
	wantStatus(t, err, http.StatusForbidden)
	_, err = f.svc.RemoveMember(ctx, f.admin, f.memberUUID, f.memberUUID)
	wantStatus(t, err, http.StatusBadRequest)

	res, err := f.svc.RemoveMember(ctx, f.admin, f.memberUUID, f.adminUUID)
	if err != nil || !res.Removed || res.IdentitiesTransferred != 2 || res.IdentitiesDisabled != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	var status string
	var removed bool
	var version int64
	if err = f.db.QueryRow(`SELECT status,removed_at IS NOT NULL,auth_version FROM users WHERE id=$1`, m).Scan(&status, &removed, &version); err != nil || status != "disabled" || !removed || version != 1 {
		t.Fatal(status, removed, version, err)
	}
	checks := map[string]int{
		`SELECT count(*) FROM user_sessions WHERE user_id=$1 AND active`:                            0,
		`SELECT count(*) FROM api_keys WHERE user_id=$1`:                                            0,
		`SELECT count(*) FROM push_subscriptions WHERE user_id=$1`:                                  0,
		`SELECT count(*) FROM oauth_connections WHERE user_id=$1`:                                   0,
		`SELECT count(*) FROM shared_mailbox_members WHERE user_id=$1`:                              0,
		`SELECT count(*) FROM received_emails WHERE mailbox_owner_id=$1 AND ses_message_id='ses-s'`: 0,
		`SELECT count(*) FROM email_forwards WHERE user_id=$1 AND status='paused' AND NOT active`:   1,
		`SELECT count(*) FROM auto_replies WHERE user_id=$1 AND active`:                             0,
		`SELECT count(*) FROM auto_replies WHERE user_id=$1 AND cardinality(identity_ids)>0`:        0,
		// Old personal mail keeps its owner and is visible to no one else.
		`SELECT count(*) FROM received_emails WHERE mailbox_owner_id=$1 AND ses_message_id='ses-p'`: 1,
	}
	for q, want := range checks {
		if got := f.count(t, q, m); got != want {
			t.Errorf("%s: got %d want %d", q, got, want)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM received_emails WHERE mailbox_owner_id=$1 AND ses_message_id='ses-s'`, f.owner.UserID); n != 1 {
		t.Error("another member's shared copy was deleted")
	}
	if n := f.count(t, `SELECT count(*) FROM identities WHERE id=$1 AND user_id=$2`, shared, f.owner.UserID); n != 1 {
		t.Error("shared mailbox steward not handed to the owner")
	}
	if n := f.count(t, `SELECT count(*) FROM identities WHERE id IN ($1,$2) AND user_id=$3 AND NOT is_default AND can_send`, personal, other, f.admin.UserID); n != 2 {
		t.Error("personal identities not transferred")
	}
	if n := f.count(t, `SELECT count(*) FROM storage_cleanup_jobs WHERE object_key='raw/s'`); n != 1 {
		t.Error("shared copy storage cleanup not queued")
	}
	if n := f.count(t, `SELECT count(*) FROM audit_logs WHERE action='member_remove' AND resource_id=$1`, f.memberUUID); n != 1 {
		t.Error("removal not audited")
	}
	_, err = f.svc.RemoveMember(ctx, f.admin, f.memberUUID, "")
	wantStatus(t, err, http.StatusNotFound)
	members, err := f.svc.ListMembers(ctx, f.org)
	if err != nil || len(members) != 3 || members[0].Role != "owner" || members[2].Status != "disabled" {
		t.Fatalf("%+v %v", members, err)
	}
	// A removed user cannot sign in again through a leftover OAuth connection.
	if _, err = f.db.Exec(`INSERT INTO oauth_connections(user_id,provider,provider_user_id,updated_at) VALUES($1,'google','g-left',now())`, m); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = NewOAuthService(f.db, f.svc.cfg).FindLoginUser(ctx, ProviderGoogle, &OAuthUserInfo{ID: "g-left", Email: "member@acme.test", EmailVerified: true}); !errors.Is(err, ErrOAuthNotLinked) {
		t.Fatal("disabled user signed in through OAuth:", err)
	}
}

func TestRemoveWithoutTransferDisablesIdentitiesAndReinviteResets(t *testing.T) {
	f := newOrgFixture(t)
	ctx := context.Background()
	m := f.member.UserID
	if _, err := f.db.Exec(`INSERT INTO identities(user_id,domain_id,email,can_send,is_catch_all,updated_at) VALUES($1,50,'m1@acme.test',true,true,now())`, m); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE users SET totp_enabled=true,totp_secret='secret',backup_codes=ARRAY['code'] WHERE id=$1`, m); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO webauthn_credentials(user_id,credential_id,public_key,name) VALUES($1,'\x01','\x02','key')`, m); err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.RemoveMember(ctx, f.owner, f.memberUUID, "")
	if err != nil || res.IdentitiesDisabled != 1 {
		t.Fatal(res, err)
	}
	if n := f.count(t, `SELECT count(*) FROM identities WHERE user_id=$1 AND NOT can_send AND NOT can_receive AND NOT is_catch_all`, m); n != 1 {
		t.Fatal("identity not disabled")
	}

	if _, err = f.svc.CreateInvite(ctx, f.owner, &CreateInviteRequest{Email: "member@acme.test", Role: "admin"}); err != nil {
		t.Fatal("removed user cannot be re-invited:", err)
	}
	// The removed account keeps mail no one may read.
	if _, err = f.db.Exec(`INSERT INTO received_emails(org_id,domain_id,identity_id,message_id,from_email,subject,ses_message_id,updated_at)
		SELECT $1,50,id,'old','a@sender.test','Old mail','ses-old',now() FROM identities WHERE email='m1@acme.test'`, f.org); err != nil {
		t.Fatal(err)
	}
	back, err := f.svc.AcceptInvite(ctx, &AcceptInviteRequest{Token: f.lastToken(t), Name: "Returning", Password: "brand-new-password"}, "")
	if err != nil {
		t.Fatal(err)
	}
	// Whoever accepts gets a fresh account, never the removed one and its mailbox.
	if back.User.ID == m || back.User.Email != "member@acme.test" || back.User.Role != "admin" || back.User.Status != "active" {
		t.Fatalf("%+v", back.User)
	}
	var oldEmail, oldStatus string
	if err = f.db.QueryRow(`SELECT email,status FROM users WHERE id=$1`, m).Scan(&oldEmail, &oldStatus); err != nil || oldStatus != "disabled" || !strings.HasPrefix(oldEmail, "removed+") || !strings.HasSuffix(oldEmail, "@invalid") {
		t.Fatal("removed account changed:", oldEmail, oldStatus, err)
	}
	if got := listSubjects(t, &InboxService{db: f.db}, back.User.ID); len(got) != 0 {
		t.Fatalf("new account sees the removed account's mail: %v", got)
	}
	if n := f.count(t, `SELECT count(*) FROM received_emails WHERE mailbox_owner_id=$1`, m); n != 1 {
		t.Fatal("retained mail lost", n)
	}
	if n := f.count(t, `SELECT count(*) FROM identities WHERE user_id=$1`, back.User.ID); n != 0 {
		t.Fatal("new account inherited identities", n)
	}
	var totp bool
	var codes int
	if err = f.db.QueryRow(`SELECT totp_enabled,cardinality(COALESCE(backup_codes,'{}')) FROM users WHERE id=$1`, back.User.ID).Scan(&totp, &codes); err != nil || totp || codes != 0 {
		t.Fatal("new account has second factors:", totp, codes, err)
	}
	if n := f.count(t, `SELECT count(*) FROM webauthn_credentials WHERE user_id=$1`, back.User.ID); n != 0 {
		t.Fatal("security keys carried over")
	}
	// The address can be removed and invited again.
	if _, err = f.svc.RemoveMember(ctx, f.owner, back.User.UUID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.CreateInvite(ctx, f.owner, &CreateInviteRequest{Email: "member@acme.test", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	if again, err := f.svc.AcceptInvite(ctx, &AcceptInviteRequest{Token: f.lastToken(t), Name: "Third", Password: "another-new-password"}, ""); err != nil || again.User.ID == back.User.ID {
		t.Fatal(again, err)
	}
}

func TestChangeRoleAndTransferIdentity(t *testing.T) {
	f := newOrgFixture(t)
	ctx := context.Background()
	_, err := f.svc.ChangeRole(ctx, f.admin, f.memberUUID, "admin")
	wantStatus(t, err, http.StatusForbidden)
	var ownerUUID string
	if err = f.db.QueryRow(`SELECT uuid::text FROM users WHERE id=$1`, f.owner.UserID).Scan(&ownerUUID); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.ChangeRole(ctx, f.owner, ownerUUID, "member")
	wantStatus(t, err, http.StatusConflict)
	_, err = f.svc.ChangeRole(ctx, f.owner, f.memberUUID, "owner")
	wantStatus(t, err, http.StatusBadRequest)
	promoted, err := f.svc.ChangeRole(ctx, f.owner, f.memberUUID, "admin")
	if err != nil || promoted.Role != "admin" {
		t.Fatal(promoted, err)
	}
	if n := f.count(t, `SELECT count(*) FROM audit_logs WHERE action='member_role_change'`); n != 1 {
		t.Fatal("role change not audited")
	}

	var personal, shared int64
	var personalUUID, sharedUUID string
	if err = f.db.QueryRow(`INSERT INTO identities(user_id,domain_id,email,can_send,is_default,updated_at) VALUES($1,50,'sales@acme.test',true,false,now()) RETURNING id,uuid::text`, f.owner.UserID).Scan(&personal, &personalUUID); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow(`INSERT INTO identities(user_id,domain_id,email,kind,updated_at) VALUES($1,50,'team@acme.test','shared',now()) RETURNING id,uuid::text`, f.owner.UserID).Scan(&shared, &sharedUUID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`INSERT INTO email_forwards(user_id,org_id,identity_id,forward_to,status,active,verified,updated_at) VALUES($1,$2,$3,'out@elsewhere.test','active',true,true,now())`, f.owner.UserID, f.org, personal); err != nil {
		t.Fatal(err)
	}
	moved, err := f.svc.TransferIdentity(ctx, f.admin, personalUUID, f.memberUUID)
	if err != nil || moved.OwnerUUID != f.memberUUID || moved.Kind != "personal" {
		t.Fatal(moved, err)
	}
	if n := f.count(t, `SELECT count(*) FROM email_forwards WHERE identity_id=$1 AND status='paused' AND NOT active`, personal); n != 1 {
		t.Fatal("previous owner's forward still active")
	}
	_, err = f.svc.TransferIdentity(ctx, f.admin, sharedUUID, f.memberUUID)
	wantStatus(t, err, http.StatusBadRequest)
	_, err = f.svc.TransferIdentity(ctx, f.admin, personalUUID, "00000000-0000-0000-0000-000000000000")
	wantStatus(t, err, http.StatusBadRequest)
	list, err := f.svc.ListOrgIdentities(ctx, f.org)
	if err != nil || len(list) != 3 {
		t.Fatalf("%+v %v", list, err)
	}
}

func itoa(v int64) string { return fmt.Sprint(v) }

// sharedMailbox creates a shared identity and mailbox on acme.test with the
// given members (user id -> can_read), inserted in that order.
func (f *orgFixture) sharedMailbox(t *testing.T, email string, members [][2]any) (mailboxID, identityID int64) {
	t.Helper()
	if err := f.db.QueryRow(`INSERT INTO identities(user_id,domain_id,email,kind,can_send,updated_at) VALUES($1,50,$2,'shared',true,now()) RETURNING id`, f.owner.UserID, email).Scan(&identityID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`INSERT INTO shared_mailboxes(org_id,name,email,identity_id,updated_at) VALUES($1,$2,$2,$3,now()) RETURNING id`, f.org, email, identityID).Scan(&mailboxID); err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		if _, err := f.db.Exec(`INSERT INTO shared_mailbox_members(shared_mailbox_id,user_id,can_read,can_send) VALUES($1,$2,$3,true)`, mailboxID, m[0], m[1]); err != nil {
			t.Fatal(err)
		}
	}
	return mailboxID, identityID
}

func (f *orgFixture) ingestShared(t *testing.T, sesID, rcpt string) {
	t.Helper()
	if _, err := f.db.Exec(`UPDATE domains SET receiving_enabled=true WHERE id=50`); err != nil {
		t.Fatal(err)
	}
	storage := &fakeIncomingStorage{raw: []byte("From: Sender <sender@example.test>\r\nTo: " + rcpt + "\r\nMessage-ID: <" + sesID + "@example.test>\r\nSubject: Hello\r\n\r\nbody")}
	svc := &ReceivingService{db: f.db, storage: storage}
	auth := &ReceivingAuthorization{OrgID: f.org, TopicARN: "arn:aws:sns:us-east-1:123456789012:acme", Bucket: "acme-bucket", Region: "us-east-1"}
	n := &model.SESNotification{NotificationType: "Received", Mail: model.SESMail{MessageId: sesID}, Receipt: &model.SESReceipt{Timestamp: "2026-10-07T00:00:00Z", Recipients: []string{rcpt},
		Action: model.SESAction{Type: "S3", BucketName: auth.Bucket, ObjectKey: "incoming/acme.test/" + sesID}}}
	if err := svc.ProcessIncomingEmail(context.Background(), auth, n); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveSoleSharedReaderKeepsMailboxReceiving(t *testing.T) {
	f := newOrgFixture(t)
	ctx := context.Background()
	m := f.member.UserID
	var disabled int64
	if err := f.db.QueryRow(`INSERT INTO users(org_id,email,password_hash,role,status,updated_at) VALUES($1,'gone@acme.test','unused','member','disabled',now()) RETURNING id`, f.org).Scan(&disabled); err != nil {
		t.Fatal(err)
	}
	// The member is the only active reader of support@ (a disabled reader does
	// not count); sales@ has another reader; send-only membership on ops@.
	support, _ := f.sharedMailbox(t, "support@acme.test", [][2]any{{m, true}, {disabled, true}})
	sales, _ := f.sharedMailbox(t, "sales@acme.test", [][2]any{{m, true}, {f.admin.UserID, true}})
	ops, _ := f.sharedMailbox(t, "ops@acme.test", [][2]any{{m, false}, {f.admin.UserID, true}})

	if _, err := f.svc.RemoveMember(ctx, f.admin, f.memberUUID, ""); err != nil {
		t.Fatal(err)
	}
	var read, send, manage bool
	if err := f.db.QueryRow(`SELECT can_read,can_send,can_manage FROM shared_mailbox_members WHERE shared_mailbox_id=$1 AND user_id=$2`, support, f.owner.UserID).Scan(&read, &send, &manage); err != nil {
		t.Fatal("support@ was left without a reader:", err)
	}
	if !read || send || !manage {
		t.Fatal("owner membership", read, send, manage)
	}
	for _, mb := range []int64{sales, ops} {
		if n := f.count(t, `SELECT count(*) FROM shared_mailbox_members WHERE shared_mailbox_id=$1 AND user_id=$2`, mb, f.owner.UserID); n != 0 {
			t.Fatalf("owner added to mailbox %d that still had a reader", mb)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM audit_logs WHERE action='member_remove' AND resource_id=$1 AND new_values::text LIKE '%sharedMailboxesHandedOver%'`, f.memberUUID); n != 1 {
		t.Fatal("handover not audited")
	}
	// The owner also sees an unacknowledged alert naming the mailbox.
	if n := f.count(t, `SELECT count(*) FROM alerts WHERE org_id=$1 AND type='shared_mailbox_handover' AND NOT acknowledged AND message LIKE '%support@acme.test%'`, f.org); n != 1 {
		t.Fatalf("handover alerts for support@: %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM alerts WHERE org_id=$1 AND type='shared_mailbox_handover'`, f.org); n != 1 {
		t.Fatalf("handover alerts: %d, want only support@", n)
	}

	// Mail to support@ is still stored for a reader instead of being dropped.
	f.ingestShared(t, "after-removal", "support@acme.test")
	if n := f.count(t, `SELECT count(*) FROM received_emails WHERE ses_message_id='after-removal' AND mailbox_owner_id=$1`, f.owner.UserID); n != 1 {
		t.Fatalf("mail to support@ after removal stored %d copies for the owner", n)
	}
}

// Ingest locks membership rows in ascending identity order; removal must take
// them in the same order, or the two deadlock.
func TestRemoveMemberLocksSharedMembershipsInIngestOrder(t *testing.T) {
	f := newOrgFixture(t)
	ctx := context.Background()
	m := f.member.UserID
	// Mailbox A has the lower identity id, so ingest locks it first, but B's
	// membership row is written first, so heap order puts it before A's.
	mbA, identityA := f.sharedMailbox(t, "a@acme.test", nil)
	mbB, identityB := f.sharedMailbox(t, "b@acme.test", nil)
	if identityA >= identityB {
		t.Fatal("identity ids out of order")
	}
	for _, mb := range []int64{mbB, mbA} {
		if _, err := f.db.Exec(`INSERT INTO shared_mailbox_members(shared_mailbox_id,user_id,can_read,can_send) VALUES($1,$2,true,true),($1,$3,true,false)`, mb, m, f.admin.UserID); err != nil {
			t.Fatal(err)
		}
	}
	lockMembers := func(tx *sql.Tx, identity int64) error {
		_, err := tx.Exec(`SELECT m.user_id FROM shared_mailbox_members m JOIN shared_mailboxes sm ON sm.id=m.shared_mailbox_id
			WHERE sm.identity_id=$1 AND m.can_read ORDER BY m.user_id FOR SHARE OF m`, identity)
		return err
	}
	ingest, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer ingest.Rollback()
	if _, err = ingest.Exec(`SET LOCAL lock_timeout='5s'`); err != nil {
		t.Fatal(err)
	}
	if err = lockMembers(ingest, identityA); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.RemoveMember(ctx, f.admin, f.memberUUID, "")
		done <- err
	}()
	// Wait until the removal blocks on the first mailbox.
	for i := 0; ; i++ {
		if f.count(t, `SELECT count(*) FROM pg_locks WHERE NOT granted`) > 0 {
			break
		}
		if i > 500 {
			t.Fatal("removal never waited for the ingest lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = lockMembers(ingest, identityB); err != nil {
		t.Fatal("ingest could not lock the second mailbox:", err)
	}
	if err = ingest.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal("removal:", err)
	}
}

// Ingest re-reads a personal identity's owner in its transaction, so mail
// routed before a transfer committed follows the new owner.
func TestIngestResolvesPersonalOwnerInsideTransaction(t *testing.T) {
	f := newOrgFixture(t)
	ctx := context.Background()
	var personal int64
	if err := f.db.QueryRow(`INSERT INTO identities(user_id,domain_id,email,can_send,updated_at) VALUES($1,50,'m1@acme.test',true,now()) RETURNING id`, f.member.UserID).Scan(&personal); err != nil {
		t.Fatal(err)
	}
	// Routing read the member as owner; a transfer then commits.
	stale := &recipientIdentity{ID: personal, Kind: "personal", UserID: f.member.UserID}
	var adminUUID, identityUUID string
	if err := f.db.QueryRow(`SELECT (SELECT uuid::text FROM users WHERE id=$1),(SELECT uuid::text FROM identities WHERE id=$2)`, f.admin.UserID, personal).Scan(&adminUUID, &identityUUID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.TransferIdentity(ctx, f.owner, identityUUID, adminUUID); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	owners, err := mailboxOwners(ctx, tx, stale)
	if err != nil || len(owners) != 1 || owners[0] != f.admin.UserID || stale.UserID != f.admin.UserID {
		t.Fatalf("owners %v (identity owner %d), err %v; want the new owner %d", owners, stale.UserID, err, f.admin.UserID)
	}
	_ = tx.Rollback()

	// The ingest's share lock makes a concurrent transfer wait for its commit.
	tx, err = f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mailboxOwners(ctx, tx, stale); err != nil {
		t.Fatal(err)
	}
	blocked, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.Rollback()
	if _, err = blocked.Exec(`SET LOCAL lock_timeout='200ms'`); err != nil {
		t.Fatal(err)
	}
	if _, err = blocked.Exec(`SELECT id FROM identities WHERE id=$1 FOR UPDATE`, personal); err == nil {
		t.Fatal("a transfer could lock the identity while ingest delivered to it")
	}
	_ = blocked.Rollback()
	_ = tx.Rollback()

	// An identity that stopped receiving, or whose owner left, gets no copy.
	if _, err = f.db.Exec(`UPDATE identities SET can_receive=false WHERE id=$1`, personal); err != nil {
		t.Fatal(err)
	}
	tx, err = f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if owners, err = mailboxOwners(ctx, tx, stale); !errors.Is(err, errIngestRouteChanged) || len(owners) != 0 {
		t.Fatalf("disabled identity owners %v %v; want a re-route", owners, err)
	}
}

// A personal identity that stops receiving between routing and delivery must
// not swallow the message: it is routed again, here to the catch-all.
func TestIngestReroutesWhenPersonalIdentityStopsReceiving(t *testing.T) {
	f := newOrgFixture(t)
	var personal int64
	if err := f.db.QueryRow(`INSERT INTO identities(user_id,domain_id,email,can_send,updated_at) VALUES($1,50,'m1@acme.test',true,now()) RETURNING id`, f.member.UserID).Scan(&personal); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO identities(user_id,domain_id,email,can_send,is_catch_all,updated_at) VALUES($1,50,'catchall@acme.test',true,true,now())`, f.admin.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE domains SET receiving_enabled=true WHERE id=50`); err != nil {
		t.Fatal(err)
	}
	const sesID = "raced-disable"
	storage := &fakeIncomingStorage{raw: []byte("From: Sender <sender@example.test>\r\nTo: m1@acme.test\r\nMessage-ID: <" + sesID + "@example.test>\r\nSubject: Hello\r\n\r\nbody")}
	disabled := false
	svc := &ReceivingService{db: f.db, storage: storage, beforeIngestTx: func() {
		if !disabled {
			disabled = true
			if _, err := f.db.Exec(`UPDATE identities SET can_receive=false WHERE id=$1`, personal); err != nil {
				t.Error(err)
			}
		}
	}}
	auth := &ReceivingAuthorization{OrgID: f.org, TopicARN: "arn:aws:sns:us-east-1:123456789012:acme", Bucket: "acme-bucket", Region: "us-east-1"}
	n := &model.SESNotification{NotificationType: "Received", Mail: model.SESMail{MessageId: sesID}, Receipt: &model.SESReceipt{Timestamp: "2026-10-07T00:00:00Z", Recipients: []string{"m1@acme.test"},
		Action: model.SESAction{Type: "S3", BucketName: auth.Bucket, ObjectKey: "incoming/acme.test/" + sesID}}}
	if err := svc.ProcessIncomingEmail(context.Background(), auth, n); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM received_emails WHERE ses_message_id=$1 AND mailbox_owner_id=$2`, sesID, f.admin.UserID); n != 1 {
		t.Fatalf("catch-all copies %d, want 1", n)
	}
	if n := f.count(t, `SELECT count(*) FROM received_ingestions WHERE ses_message_id=$1 AND identity_id=$2`, sesID, personal); n != 0 {
		t.Fatal("the disabled identity was tombstoned")
	}
}

// A personal identity with a lower id than a shared mailbox: ingest locks the
// identity, then the memberships; removal must not take them the other way.
func TestRemoveMemberLocksPersonalAndSharedInIngestOrder(t *testing.T) {
	f := newOrgFixture(t)
	ctx := context.Background()
	m := f.member.UserID
	var personal int64
	if err := f.db.QueryRow(`INSERT INTO identities(user_id,domain_id,email,can_send,updated_at) VALUES($1,50,'m1@acme.test',true,now()) RETURNING id`, m).Scan(&personal); err != nil {
		t.Fatal(err)
	}
	_, shared := f.sharedMailbox(t, "team@acme.test", [][2]any{{m, true}, {f.admin.UserID, true}})
	if personal >= shared {
		t.Fatal("identity ids out of order")
	}
	ingest, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer ingest.Rollback()
	if _, err = ingest.Exec(`SET LOCAL lock_timeout='5s'`); err != nil {
		t.Fatal(err)
	}
	if _, err = mailboxOwners(ctx, ingest, &recipientIdentity{ID: personal, Kind: "personal"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.RemoveMember(ctx, f.admin, f.memberUUID, "")
		done <- err
	}()
	for i := 0; f.count(t, `SELECT count(*) FROM pg_locks WHERE NOT granted`) == 0; i++ {
		if i > 500 {
			t.Fatal("removal never waited for the ingest lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = mailboxOwners(ctx, ingest, &recipientIdentity{ID: shared, Kind: "shared"}); err != nil {
		t.Fatal("ingest could not lock the shared mailbox:", err)
	}
	if err = ingest.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal("removal:", err)
	}
}

// Ingest holds the personal identity lock and then inserts mail and change
// rows whose foreign keys take FOR KEY SHARE on the owner's users row. Removal
// must not hold a lock on that row that conflicts with FOR KEY SHARE while it
// waits for the identity, or the two deadlock and removal fails.
func TestRemoveMemberDoesNotDeadlockIngestForeignKeys(t *testing.T) {
	f := newOrgFixture(t)
	ctx := context.Background()
	m := f.member.UserID
	var personal int64
	if err := f.db.QueryRow(`INSERT INTO identities(user_id,domain_id,email,can_send,updated_at) VALUES($1,50,'m1@acme.test',true,now()) RETURNING id`, m).Scan(&personal); err != nil {
		t.Fatal(err)
	}
	ingest, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer ingest.Rollback()
	if _, err = ingest.Exec(`SET LOCAL lock_timeout='5s'`); err != nil {
		t.Fatal(err)
	}
	if _, err = mailboxOwners(ctx, ingest, &recipientIdentity{ID: personal, Kind: "personal"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.RemoveMember(ctx, f.admin, f.memberUUID, "")
		done <- err
	}()
	for i := 0; f.count(t, `SELECT count(*) FROM pg_locks WHERE NOT granted`) == 0; i++ {
		if i > 500 {
			t.Fatal("removal never waited for the ingest lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The lock a foreign key check on mailbox_changes/received_emails takes.
	if _, err = ingest.Exec(`SELECT 1 FROM users WHERE id=$1 FOR KEY SHARE`, m); err != nil {
		t.Fatal("ingest foreign key check blocked by removal:", err)
	}
	if err = ingest.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal("removal:", err)
	}
	if f.count(t, `SELECT count(*) FROM users WHERE id=$1 AND status='disabled'`, m) != 1 {
		t.Fatal("member not removed")
	}
}
