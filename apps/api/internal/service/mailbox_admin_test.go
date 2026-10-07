package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func (f *mailboxUserFixture) identityOf(t *testing.T, user int64) int64 {
	t.Helper()
	var id int64
	if err := f.db.QueryRow(`SELECT identity_id FROM mailbox_accounts WHERE user_id=$1 AND removed_at IS NULL`, user).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func boolp(v bool) *bool    { return &v }
func strp(v string) *string { return &v }

// The domain list, the detail page and the switches.
func TestMailboxAdminListDetailUpdate(t *testing.T) {
	f := newMailboxUserFixture(t)
	ctx := context.Background()
	ibrahim := f.create(t, "ibrahim", invite("ibrahim@home.test")).Mailbox
	anna := f.create(t, "anna", password("anna-password")).Mailbox
	annaID, ibrahimID := f.userID(t, anna.UserUUID), f.userID(t, ibrahim.UserUUID)

	list, err := f.mb.ListDomainMailboxes(ctx, f.org, f.domain, false)
	if err != nil {
		t.Fatal(err)
	}
	if list.Domain.Name != "acme.test" || list.Domain.UUID != f.domain || !list.Domain.SESVerified || list.Domain.ReceivingEnabled {
		t.Fatalf("domain %+v", list.Domain)
	}
	if list.CatchAll == nil || list.CatchAll.Email != "catchall@acme.test" || list.CatchAll.OwnerEmail != "admin@acme.test" || list.CatchAll.IsMailbox {
		t.Fatalf("catch-all %+v", list.CatchAll)
	}
	if len(list.Mailboxes) != 2 || list.Mailboxes[0].Address != "anna@acme.test" || list.Mailboxes[0].Status != "active" ||
		list.Mailboxes[1].Address != "ibrahim@acme.test" || list.Mailboxes[1].Status != "invited" {
		t.Fatalf("mailboxes %+v", list.Mailboxes)
	}
	for _, uuid := range []string{"not-a-uuid", "00000000-0000-0000-0000-000000000000"} {
		_, err = f.mb.ListDomainMailboxes(ctx, f.org, uuid, false)
		wantStatus(t, err, http.StatusNotFound)
	}
	_, err = f.mb.ListDomainMailboxes(ctx, f.org+1000, f.domain, false)
	wantStatus(t, err, http.StatusNotFound)

	// Detail: overview, open setup link, no aliases yet.
	d, err := f.mb.GetMailbox(ctx, f.org, ibrahim.UserUUID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Mailbox.Address != "ibrahim@acme.test" || d.Domain.Name != "acme.test" || d.Overview.RecoveryEmail != "ibrahim@home.test" || !d.Overview.MaySend ||
		d.Overview.TwoFactor || d.Overview.ForwardsActive || d.Overview.AutoReplyActive || len(d.Aliases) != 0 ||
		d.Invite == nil || d.Invite.Purpose != "mailbox_setup" || d.Invite.Status != "pending" || d.Invite.SendCount != 1 {
		t.Fatalf("detail %+v %+v", d, d.Invite)
	}
	annaIdentity := f.identityOf(t, annaID)
	for _, q := range []string{
		`INSERT INTO email_forwards(user_id,org_id,identity_id,forward_to,status,active,verified,updated_at) VALUES($1,$2,$3,'out@elsewhere.test','active',true,true,now())`,
		`INSERT INTO auto_replies(user_id,org_id,name,start_date,subject,html_content,identity_ids,active,updated_at) VALUES($1,$2,'Away',now()-interval '1 day','Away','x',ARRAY[$3::int],true,now())`,
		`UPDATE users SET totp_enabled=true WHERE id=$1 AND org_id=$2 AND $3::int>0`,
	} {
		if _, err = f.db.Exec(q, annaID, f.org, annaIdentity); err != nil {
			t.Fatal(err)
		}
	}
	d, err = f.mb.GetMailbox(ctx, f.org, anna.UserUUID)
	if err != nil || !d.Overview.ForwardsActive || !d.Overview.AutoReplyActive || !d.Overview.TwoFactor || d.Overview.RecoveryEmail != "" || d.Invite != nil {
		t.Fatalf("anna detail %+v %v", d, err)
	}
	for _, uuid := range []string{"bad", "00000000-0000-0000-0000-000000000000", f.memberUUID} {
		_, err = f.mb.GetMailbox(ctx, f.org, uuid)
		wantStatus(t, err, http.StatusNotFound)
	}

	// Update: validation first, then every field.
	_, err = f.mb.UpdateMailbox(ctx, f.admin, ibrahim.UserUUID, &UpdateMailboxRequest{Name: strp("x")})
	wantStatus(t, err, http.StatusBadRequest)
	_, err = f.mb.UpdateMailbox(ctx, f.admin, ibrahim.UserUUID, &UpdateMailboxRequest{RecoveryEmail: strp("not an email")})
	wantStatus(t, err, http.StatusBadRequest)
	_, err = f.mb.UpdateMailbox(ctx, f.admin, ibrahim.UserUUID, &UpdateMailboxRequest{RecoveryEmail: strp("Ibrahim@Acme.test")})
	wantStatus(t, err, http.StatusBadRequest)
	_, err = f.mb.UpdateMailbox(ctx, f.admin, f.memberUUID, &UpdateMailboxRequest{MaySend: boolp(false)})
	wantStatus(t, err, http.StatusNotFound)
	mails := f.mailCount()
	got, err := f.mb.UpdateMailbox(ctx, f.admin, ibrahim.UserUUID, &UpdateMailboxRequest{Name: strp("Ibrahim M"), MaySend: boolp(false), WildcardSender: boolp(true), RecoveryEmail: strp("New@Home.test")})
	if err != nil || got.Name != "Ibrahim M" || got.MaySend || !got.MayReceive || !got.WildcardSender {
		t.Fatalf("update %+v %v", got, err)
	}
	if f.count(t, `SELECT count(*) FROM identities WHERE user_id=$1 AND display_name='Ibrahim M' AND NOT can_send AND can_receive AND wildcard_sender`, ibrahimID) != 1 ||
		f.count(t, `SELECT count(*) FROM mailbox_accounts WHERE user_id=$1 AND recovery_email='new@home.test'`, ibrahimID) != 1 {
		t.Fatal("update not stored")
	}
	if to, text := f.lastMail(t); f.mailCount() != mails+1 || !strings.Contains(to, "<ibrahim@home.test>") || !strings.Contains(text, "changed the recovery email") {
		t.Fatalf("previous recovery email not told: %s %q", to, text)
	}
	var values string
	if err = f.db.QueryRow(`SELECT new_values::text FROM audit_logs WHERE action='mailbox_update' AND resource_id=$1`, ibrahim.UserUUID).Scan(&values); err != nil ||
		!strings.Contains(values, `"wildcardSender": true`) || strings.Contains(values, "new@home.test") {
		t.Fatalf("audit %s %v", values, err)
	}
	// Nothing changed: no audit row; clearing the recovery email tells the old one.
	if _, err = f.mb.UpdateMailbox(ctx, f.admin, ibrahim.UserUUID, &UpdateMailboxRequest{MaySend: boolp(false)}); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT count(*) FROM audit_logs WHERE action='mailbox_update' AND resource_id=$1`, ibrahim.UserUUID) != 1 {
		t.Fatal("no-op update was audited")
	}
	if _, err = f.mb.UpdateMailbox(ctx, f.admin, ibrahim.UserUUID, &UpdateMailboxRequest{RecoveryEmail: strp("")}); err != nil {
		t.Fatal(err)
	}
	if to, _ := f.lastMail(t); !strings.Contains(to, "<new@home.test>") || f.count(t, `SELECT count(*) FROM mailbox_accounts WHERE user_id=$1 AND recovery_email IS NULL`, ibrahimID) != 1 {
		t.Fatalf("clear recovery: %s", to)
	}

	// May receive off: new mail goes to the catch-all.
	if _, err = f.mb.UpdateMailbox(ctx, f.owner, anna.UserUUID, &UpdateMailboxRequest{MayReceive: boolp(false)}); err != nil {
		t.Fatal(err)
	}
	f.ingestShared(t, "anna-off", "anna@acme.test")
	if f.copies(t, "anna-off", annaID) != 0 || f.copies(t, "anna-off", f.admin.UserID) != 1 {
		t.Fatal("may receive off did not fall back to the catch-all")
	}

	// Removed mailboxes only show with removed=true, without their switches.
	if _, err = f.mb.Remove(ctx, f.owner, anna.UserUUID, ""); err != nil {
		t.Fatal(err)
	}
	if list, err = f.mb.ListDomainMailboxes(ctx, f.org, f.domain, false); err != nil || len(list.Mailboxes) != 1 {
		t.Fatalf("live list %+v %v", list, err)
	}
	if list, err = f.mb.ListDomainMailboxes(ctx, f.org, f.domain, true); err != nil || len(list.Mailboxes) != 2 {
		t.Fatalf("removed list %+v %v", list, err)
	}
	if r := list.Mailboxes[1]; r.Address != "anna@acme.test" || r.Status != "removed" || r.MaySend || r.MayReceive {
		t.Fatalf("removed row %+v", r)
	}
	_, err = f.mb.GetMailbox(ctx, f.org, anna.UserUUID)
	wantStatus(t, err, http.StatusNotFound)
	_, err = f.mb.UpdateMailbox(ctx, f.owner, anna.UserUUID, &UpdateMailboxRequest{MaySend: boolp(true)})
	wantStatus(t, err, http.StatusNotFound)

	// A live mailbox whose identity left its user is reported, never papered over.
	if _, err = f.db.Exec(`UPDATE identities SET user_id=$2 WHERE user_id=$1`, ibrahimID, f.member.UserID); err != nil {
		t.Fatal(err)
	}
	_, err = f.mb.ListDomainMailboxes(ctx, f.org, f.domain, false)
	wantStatus(t, err, http.StatusConflict)
	_, err = f.mb.GetMailbox(ctx, f.org, ibrahim.UserUUID)
	wantStatus(t, err, http.StatusConflict)
	_, err = f.mb.UpdateMailbox(ctx, f.owner, ibrahim.UserUUID, &UpdateMailboxRequest{MaySend: boolp(true)})
	wantStatus(t, err, http.StatusConflict)
}

// Send-as aliases: same domain, never an identity or another alias, routed to
// the mailbox, and gone once deleted.
func TestMailboxSendAliases(t *testing.T) {
	f := newMailboxUserFixture(t)
	ctx := context.Background()
	if _, err := f.db.Exec(`UPDATE domains SET receiving_enabled=true WHERE id=50`); err != nil {
		t.Fatal(err)
	}
	box := f.create(t, "ibrahim", password("ibrahim-password")).Mailbox
	kim := f.create(t, "kim", password("kim-password")).Mailbox
	boxID := f.userID(t, box.UserUUID)

	alias, err := f.mb.AddSendAlias(ctx, f.admin, box.UserUUID, " Sales ")
	if err != nil || alias.Address != "sales@acme.test" || !validUUID(alias.UUID) {
		t.Fatalf("alias %+v %v", alias, err)
	}
	for local, status := range map[string]int{"sales": http.StatusConflict, "owner": http.StatusConflict, "kim": http.StatusConflict, "a+b": http.StatusBadRequest, "": http.StatusBadRequest} {
		_, err = f.mb.AddSendAlias(ctx, f.admin, box.UserUUID, local)
		wantStatus(t, err, status)
	}
	_, err = f.mb.AddSendAlias(ctx, f.admin, kim.UserUUID, "sales")
	wantStatus(t, err, http.StatusConflict)
	_, err = f.mb.AddSendAlias(ctx, f.admin, f.memberUUID, "member-alias")
	wantStatus(t, err, http.StatusNotFound)
	// An address that is an alias cannot become a mailbox.
	_, err = f.mb.CreateMailbox(ctx, f.owner, f.domain, &CreateMailboxRequest{LocalPart: "sales", Name: "Sales", Access: password("sales-password")})
	wantStatus(t, err, http.StatusConflict)

	d, err := f.mb.GetMailbox(ctx, f.org, box.UserUUID)
	if err != nil || len(d.Aliases) != 1 || d.Aliases[0] != *alias || d.Mailbox.AliasCount != 1 {
		t.Fatalf("detail %+v %v", d, err)
	}
	f.ingestShared(t, "to-alias", "sales@acme.test")
	if f.copies(t, "to-alias", boxID) != 1 {
		t.Fatal("alias mail not delivered to the mailbox")
	}

	err = f.mb.DeleteSendAlias(ctx, f.admin, kim.UserUUID, alias.UUID)
	wantStatus(t, err, http.StatusNotFound)
	err = f.mb.DeleteSendAlias(ctx, f.admin, box.UserUUID, "not-a-uuid")
	wantStatus(t, err, http.StatusNotFound)
	if err = f.mb.DeleteSendAlias(ctx, f.admin, box.UserUUID, alias.UUID); err != nil {
		t.Fatal(err)
	}
	err = f.mb.DeleteSendAlias(ctx, f.admin, box.UserUUID, alias.UUID)
	wantStatus(t, err, http.StatusNotFound)
	f.ingestShared(t, "after-alias", "sales@acme.test")
	if f.copies(t, "after-alias", boxID) != 0 || f.copies(t, "after-alias", f.admin.UserID) != 1 {
		t.Fatal("deleted alias still routed to the mailbox")
	}
	if f.count(t, `SELECT count(*) FROM audit_logs WHERE resource_id=$1 AND action IN ('mailbox_alias_add','mailbox_alias_remove')`, box.UserUUID) != 2 {
		t.Fatal("alias changes not audited")
	}

	// Two mailboxes racing for one alias: exactly one wins.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, user := range []string{box.UserUUID, kim.UserUUID} {
		wg.Add(1)
		go func(i int, user string) {
			defer wg.Done()
			_, errs[i] = f.mb.AddSendAlias(ctx, f.owner, user, "race")
		}(i, user)
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("race: %v / %v", errs[0], errs[1])
	}
	for _, err := range errs {
		if err != nil {
			wantStatus(t, err, http.StatusConflict)
		}
	}
}

func importRows(t *testing.T, res *MailboxImportResult, err error) map[string]MailboxImportRow {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]MailboxImportRow{}
	for _, r := range res.Rows {
		out[fmt.Sprintf("%d", r.Line)] = r
	}
	return out
}

func TestMailboxImportCSV(t *testing.T) {
	f := newMailboxUserFixture(t)
	ctx := context.Background()
	imp := func(body string, dryRun bool) (*MailboxImportResult, error) {
		return f.mb.ImportCSV(ctx, f.admin, f.domain, []byte(body), dryRun)
	}
	dry := func(body string) map[string]MailboxImportRow {
		t.Helper()
		res, err := imp(body, true)
		return importRows(t, res, err)
	}

	// Whole-file errors.
	for body, want := range map[string]string{
		"":                                       "empty",
		"local_part,name,role\nx,Xavier,admin\n": `Unknown column "role"`,
		"local_part,password\nx,long-password\n": "needs local_part",
		"name,password\nX Y,long-password\n":     "needs local_part",
		"local_part,name,name\nx,X,Y\n":          "appears twice",
		"local_part,name,password\n":             "no mailbox rows",
		"local_part,name\n\xff,X\n":              "UTF-8",
		"local_part,name\n\"x,X\n":               "not valid CSV",
	} {
		_, err := imp(body, true)
		var orgErr *OrgError
		if !errors.As(err, &orgErr) || orgErr.Status != http.StatusBadRequest || !strings.Contains(orgErr.Message, want) {
			t.Errorf("%q: %v", body, err)
		}
	}
	_, err := f.mb.ImportCSV(ctx, f.admin, "00000000-0000-0000-0000-000000000000", []byte("local_part,name\n"), true)
	wantStatus(t, err, http.StatusNotFound)

	file := "\xef\xbb\xbfLocal_Part,Name,Invite_Email,PASSWORD,May_Send,May_Receive\n" +
		"alice,Alice A,alice@home.test,,true,true\n" + // 2 ok (invite)
		"bob,Bob B,,bob-password-1,false,\n" + // 3 ok (password, may not send)
		"Alice,Alice Again,,another-pass,,\n" + // 4 duplicate of 2
		"carol,Carol C,carol@home.test,carol-pass-1,,\n" + // 5 both access modes
		"dave,D,,dave-password,,\n" + // 6 name too short
		"eve+x,Eve,,eve-password,,\n" + // 7 '+'
		"owner,Owner Clash,,owner-password,,\n" + // 8 existing identity
		"frank,Frank,,short,,\n" + // 9 password length
		"gina,Gina,,gina-password,maybe,\n" + // 10 bad boolean
		"hank,Hank,hank@acme.test,,,\n" // 11 invite to the mailbox itself
	want := map[string]string{"2": "ok", "3": "ok", "4": "Duplicate of line 2", "5": "exactly one", "6": "name must be", "7": "'+'",
		"8": "already in use", "9": "8 to 72 bytes", "10": "may_send must be", "11": "outside the new mailbox"}
	check := func(rows map[string]MailboxImportRow, okResult string) {
		t.Helper()
		if len(rows) != len(want) {
			t.Fatalf("rows %+v", rows)
		}
		for line, w := range want {
			r := rows[line]
			if w == "ok" {
				if r.Result != okResult || r.Message != "" {
					t.Errorf("line %s: %+v", line, r)
				}
			} else if r.Result != "error" || !strings.Contains(r.Message, w) {
				t.Errorf("line %s: want %q, got %+v", line, w, r)
			}
		}
	}
	res, err := imp(file, true)
	rows := importRows(t, res, err)
	check(rows, "ok")
	if !res.DryRun || rows["2"].Address != "alice@acme.test" || rows["4"].Address != "alice@acme.test" || rows["7"].Address != "eve+x" {
		t.Fatalf("addresses %+v", rows)
	}
	if b, _ := json.Marshal(res); strings.Contains(string(b), "bob-password-1") || strings.Contains(string(b), "short") {
		t.Fatal("a password was echoed")
	}
	if f.count(t, `SELECT count(*) FROM users WHERE role='mailbox'`) != 0 || f.mailCount() != 0 || f.count(t, `SELECT count(*) FROM audit_logs WHERE action LIKE 'mailbox_%'`) != 0 {
		t.Fatal("the dry run wrote something")
	}

	res, err = imp(file, false)
	rows = importRows(t, res, err)
	check(rows, "created")
	if f.count(t, `SELECT count(*) FROM users WHERE email='alice@acme.test' AND role='mailbox' AND status='pending'`) != 1 ||
		f.count(t, `SELECT count(*) FROM users u JOIN identities i ON i.user_id=u.id WHERE u.email='bob@acme.test' AND u.status='active' AND NOT i.can_send AND i.can_receive`) != 1 {
		t.Fatal("rows not created as asked")
	}
	if to, _ := f.lastMail(t); f.mailCount() != 1 || !strings.Contains(to, "<alice@home.test>") {
		t.Fatalf("setup mail: %s", to)
	}
	if _, err = f.login("bob@acme.test", "bob-password-1"); err != nil {
		t.Fatal(err)
	}
	var summary string
	if err = f.db.QueryRow(`SELECT new_values::text FROM audit_logs WHERE action='mailbox_import' AND resource_id=$1`, f.domain).Scan(&summary); err != nil ||
		!strings.Contains(summary, `"created": 2`) || !strings.Contains(summary, `"rows": 10`) {
		t.Fatalf("import audit %s %v", summary, err)
	}
	// Re-running turns existing addresses into errors.
	rows = dry("address,name,password\nbob@acme.test,Bob B,bob-password-1\nivy@other.test,Ivy,ivy-password\n")
	if rows["2"].Result != "error" || !strings.Contains(rows["2"].Message, "already in use") || !strings.Contains(rows["3"].Message, "must be on acme.test") {
		t.Fatalf("rerun %+v", rows)
	}

	// The identity limit counts new rows across the file.
	n := f.count(t, `SELECT count(*) FROM identities i JOIN users u ON u.id=i.user_id WHERE u.org_id=$1`, f.org)
	if _, err = f.db.Exec(`UPDATE organizations SET max_identities=$2 WHERE id=$1`, f.org, n+1); err != nil {
		t.Fatal(err)
	}
	rows = dry("local_part,name,password\njo,Jo Jo,jo-password\nli,Li Li,li-password\n")
	if rows["2"].Result != "ok" || !strings.Contains(rows["3"].Message, "identity limit") {
		t.Fatalf("limit %+v", rows)
	}
	if _, err = f.db.Exec(`UPDATE organizations SET max_identities=0 WHERE id=$1`, f.org); err != nil {
		t.Fatal(err)
	}

	// Invite rows need a sending identity; password rows do not.
	if _, err = f.db.Exec(`UPDATE identities SET can_send=false WHERE user_id=$1`, f.admin.UserID); err != nil {
		t.Fatal(err)
	}
	rows = dry("local_part,name,invite_email,password\nmo,Mo Mo,mo@home.test,\nno,No No,,no-password\n")
	if !strings.Contains(rows["2"].Message, "sending identity") || rows["3"].Result != "ok" {
		t.Fatalf("sender %+v", rows)
	}

	// 200 rows is the limit.
	var b strings.Builder
	b.WriteString("local_part,name,password\n")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "user%d,User %d,password-%d\n", i, i, i)
	}
	if res, err = imp(b.String(), true); err != nil || len(res.Rows) != 200 || res.Rows[199].Result != "ok" {
		t.Fatalf("200 rows: %v", err)
	}
	b.WriteString("user200,User 200,password-200\n")
	_, err = imp(b.String(), true)
	wantStatus(t, err, http.StatusBadRequest)

	// The domain must be active and SES-verified.
	if _, err = f.db.Exec(`UPDATE domains SET ses_verified=false WHERE id=50`); err != nil {
		t.Fatal(err)
	}
	_, err = imp("local_part,name,password\nzed,Zed Z,zed-password\n", true)
	wantStatus(t, err, http.StatusBadRequest)
}
