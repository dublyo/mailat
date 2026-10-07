package service

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
)

// sendAsFixture: owner@send.test (owner), a member with aaa@ (lower id) and
// member@ with the alias support@, and another member other@ with sales@.
func sendAsFixture(t *testing.T, db *sql.DB) (org, owner, ownerIdentity, member, aaa, memberIdentity, other int64) {
	t.Helper()
	org, owner, ownerIdentity = mailboxFixture(t, db, "send.test")
	for _, u := range []struct {
		email string
		id    *int64
	}{{"member@send.test", &member}, {"other@send.test", &other}} {
		if err := db.QueryRow(`INSERT INTO users(org_id,email,password_hash,role,updated_at) VALUES($1,$2,'unused','member',now()) RETURNING id`, org, u.email).Scan(u.id); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(user int64, email string) (id int64) {
		if err := db.QueryRow(`INSERT INTO identities(user_id,domain_id,email,display_name,can_send,updated_at) SELECT $1,domain_id,$2,'',true,now() FROM identities WHERE id=$3 RETURNING id`, user, email, ownerIdentity).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	aaa = insert(member, "aaa@send.test")
	memberIdentity = insert(member, "member@send.test")
	otherIdentity := insert(other, "other@send.test")
	if _, err := db.Exec(`INSERT INTO identity_send_aliases(identity_id,address) VALUES($1,'support@send.test'),($2,'sales@send.test')`, memberIdentity, otherIdentity); err != nil {
		t.Fatal(err)
	}
	return
}

func TestSendAsRuleCompose(t *testing.T) {
	db := testutil.Database(t)
	_, owner, ownerIdentity, member, _, memberIdentity, _ := sendAsFixture(t, db)
	ctx := context.Background()
	compose := &ComposeService{db: db}
	check := func(user, identity int64, from string, want error) {
		t.Helper()
		_, err := compose.authorizeMailboxSender(ctx, user, identity, from)
		if want == nil && err != nil || want != nil && err != want {
			t.Fatalf("%d as %q: got %v want %v", user, from, err, want)
		}
	}
	checkErr := func(user, identity int64, from string) {
		t.Helper()
		if _, err := compose.authorizeMailboxSender(ctx, user, identity, from); err == nil {
			t.Fatalf("%d as %q accepted", user, from)
		}
	}
	check(member, memberIdentity, "", nil)
	check(member, memberIdentity, "member+news@send.test", nil)
	check(member, memberIdentity, "Support@send.test", nil)
	check(member, memberIdentity, "support+x@send.test", errMemberAlias)
	check(member, memberIdentity, "ceo@send.test", errMemberAlias)
	check(member, memberIdentity, "sales@send.test", errForeignSender)
	checkErr(member, memberIdentity, "member@elsewhere.test")

	if _, err := db.Exec(`UPDATE identities SET wildcard_sender=true WHERE id=$1`, memberIdentity); err != nil {
		t.Fatal(err)
	}
	check(member, memberIdentity, "ceo@send.test", nil)
	check(member, memberIdentity, "other+x@send.test", errForeignSender)
	check(member, memberIdentity, "sales+x@send.test", errMemberAlias)
	check(member, memberIdentity, "owner+x@send.test", errForeignSender)
	check(member, memberIdentity, "other@send.test", errForeignSender)
	check(member, memberIdentity, "sales@send.test", errForeignSender)
	checkErr(member, memberIdentity, "ceo@elsewhere.test")

	// Admins: any free address, never another identity's address or alias.
	check(owner, ownerIdentity, "ceo@send.test", nil)
	check(owner, ownerIdentity, "other+x@send.test", errForeignSender)
	check(owner, ownerIdentity, "member+x@send.test", errForeignSender)
	check(owner, ownerIdentity, "owner+x@send.test", nil)
	check(owner, ownerIdentity, "sales+x@send.test", nil)
	check(owner, ownerIdentity, "sales@send.test", errForeignSender)
	check(owner, ownerIdentity, "support@send.test", errForeignSender)
	check(owner, ownerIdentity, "other@send.test", errForeignSender)

	// The pure helpers.
	for in, want := range map[string]string{"a+x@d.test": "a@d.test", "A@D.test": "a@d.test", "+x@d.test": "+x@d.test", "a+x+y@d.test": "a@d.test"} {
		if got := baseAddress(in); got != want {
			t.Errorf("baseAddress(%q)=%q want %q", in, got, want)
		}
	}
}

func TestSendAsRuleTransactional(t *testing.T) {
	db := testutil.Database(t)
	org, owner, ownerIdentity, member, aaa, memberIdentity, _ := sendAsFixture(t, db)
	ctx := context.Background()
	svc := &TransactionalService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: &mailboxTestProvider{}}
	n := 0
	send := func(actor SendActor, from string) (int64, error) {
		t.Helper()
		n++
		resp, err := svc.SendEmailForUser(ctx, org, actor, &model.SendEmailRequest{From: from, To: []string{"r@external.test"}, Subject: "s", Text: "t", IdempotencyKey: "send-as-" + strings.Repeat("x", n)})
		if err != nil {
			return 0, err
		}
		waitForTransactionalSend(t, db, resp.ID)
		var identity int64
		if err = db.QueryRow(`SELECT identity_id FROM transactional_emails WHERE uuid=$1`, resp.ID).Scan(&identity); err != nil {
			t.Fatal(err)
		}
		return identity, nil
	}
	memberActor := SendActor{UserID: member}
	// A member's free address on the domain is no longer accepted.
	if _, err := send(memberActor, "ceo@send.test"); err != errMemberAlias {
		t.Fatal("member sent as a free address:", err)
	}
	// +tag and alias sends land on the base identity, not the lowest id.
	for _, from := range []string{"member@send.test", "member+news@send.test", "support@send.test", "Support <support@send.test>"} {
		identity, err := send(memberActor, from)
		if err != nil || identity != memberIdentity {
			t.Fatalf("%q: identity %d err %v", from, identity, err)
		}
	}
	if identity, err := send(memberActor, "aaa+x@send.test"); err != nil || identity != aaa {
		t.Fatal("aaa +tag", identity, err)
	}
	for _, from := range []string{"support+x@send.test", "other@send.test", "other+x@send.test", "sales@send.test"} {
		if _, err := send(memberActor, from); err == nil {
			t.Fatalf("member sent as %q", from)
		}
	}
	// The wildcard switch opens free addresses, still not others' +tags.
	if _, err := db.Exec(`UPDATE identities SET wildcard_sender=true WHERE id=$1`, memberIdentity); err != nil {
		t.Fatal(err)
	}
	if identity, err := send(memberActor, "ceo@send.test"); err != nil || identity != memberIdentity {
		t.Fatal("wildcard send", identity, err)
	}
	if _, err := send(memberActor, "other+x@send.test"); err != errForeignSender {
		t.Fatal("wildcard covered another identity's +tag:", err)
	}
	// Admins keep free addresses but never another user's alias.
	adminActor := SendActor{UserID: owner, Admin: true}
	if identity, err := send(adminActor, "news@send.test"); err != nil || identity != ownerIdentity {
		t.Fatal("admin free address", identity, err)
	}
	if _, err := send(adminActor, "sales@send.test"); err != errForeignSender {
		t.Fatal("admin sent as another user's alias:", err)
	}
	// local+tag mail routes to the base identity, so its +tags are its owner's.
	if _, err := send(adminActor, "other+x@send.test"); err != errForeignSender {
		t.Fatal("admin sent as another user's +tag:", err)
	}
	if identity, err := send(adminActor, "owner+x@send.test"); err != nil || identity != ownerIdentity {
		t.Fatal("admin own +tag", identity, err)
	}
	// A disabled alias owner stops alias sends.
	if _, err := db.Exec(`UPDATE identities SET can_send=false WHERE id=$1`, memberIdentity); err != nil {
		t.Fatal(err)
	}
	if _, err := send(memberActor, "support@send.test"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatal("alias of a send-disabled identity accepted:", err)
	}
	// The signup-form confirmation (non-admin, the form's own identity) still sends.
	if identity, err := send(memberActor, "aaa@send.test"); err != nil || identity != aaa {
		t.Fatal("signup-form style exact send", identity, err)
	}
}

func TestReplyContextFallsBackFromForeignAlias(t *testing.T) {
	db := testutil.Database(t)
	org, owner, ownerIdentity, _, _, _, _ := sendAsFixture(t, db)
	ctx := context.Background()
	var id string
	// Catch-all mail addressed to another user's send-as alias.
	if err := db.QueryRow(`INSERT INTO received_emails(org_id,domain_id,identity_id,message_id,from_email,to_emails,subject,text_body,envelope_recipients,updated_at)
	 SELECT $1,domain_id,$2,'<alias@external.test>','from@external.test',ARRAY['sales@send.test'],'Hi','Body',ARRAY['sales@send.test'],now() FROM identities WHERE id=$2 RETURNING uuid`, org, ownerIdentity).Scan(&id); err != nil {
		t.Fatal(err)
	}
	reply, err := (&ComposeService{db: db, cfg: &config.Config{EmailProvider: "ses"}}).GetReplyContext(ctx, owner, id, false)
	if err != nil {
		t.Fatal(err)
	}
	if reply.From.Email != "owner@send.test" {
		t.Fatalf("reply From %q", reply.From.Email)
	}
}

func TestIdentitySignatureAndSendAs(t *testing.T) {
	db := testutil.Database(t)
	_, _, _, member, _, _, _ := sendAsFixture(t, db)
	ctx := context.Background()
	svc := &IdentityService{db: db, cfg: &config.Config{}}
	var memberUUID string
	if err := db.QueryRow(`SELECT uuid::text FROM identities WHERE email='member@send.test'`).Scan(&memberUUID); err != nil {
		t.Fatal(err)
	}
	html, text := "<p>Thanks</p>", "Thanks"
	got, err := svc.UpdateIdentity(ctx, member, memberUUID, &model.UpdateIdentityRequest{SignatureHtml: &html, SignatureText: &text}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.SignatureHtml != html || got.SignatureText != text || len(got.SendAliases) != 1 || got.SendAliases[0] != "support@send.test" || got.WildcardSender {
		t.Fatalf("bad identity %+v", got)
	}
	name := "Member"
	if got, err = svc.UpdateIdentity(ctx, member, memberUUID, &model.UpdateIdentityRequest{DisplayName: &name}, false); err != nil || got.SignatureHtml != html {
		t.Fatal("unrelated update cleared the signature", err)
	}
	for _, bad := range []string{strings.Repeat("x", maxSignatureBytes+1), "a\x00b"} {
		if _, err = svc.UpdateIdentity(ctx, member, memberUUID, &model.UpdateIdentityRequest{SignatureHtml: &bad}, false); err == nil {
			t.Fatal("bad signature saved")
		}
	}
	list, err := svc.ListIdentities(ctx, member)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, identity := range list {
		if identity.UUID == memberUUID {
			found = identity.SignatureText == text && len(identity.SendAliases) == 1
		} else if len(identity.SendAliases) != 0 {
			t.Fatalf("aliases on the wrong identity %+v", identity)
		}
	}
	if !found {
		t.Fatal("list misses signature or aliases")
	}
}
