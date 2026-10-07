package service

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/eventoutbox"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
)

// sharedIdentityFixture adds shared identity 10 (team@one.test, steward user 1)
// to seedReceivedFixture, with user 2 as its only member.
func sharedIdentityFixture(t *testing.T, db *sql.DB, canSend bool) {
	t.Helper()
	_, err := db.Exec(fmt.Sprintf(`
 UPDATE domains SET ses_verified=true WHERE id=1;
 UPDATE identities SET can_send=true WHERE id IN (1,2);
 INSERT INTO identities(id,user_id,domain_id,email,kind,can_send,updated_at) VALUES(10,1,1,'team@one.test','shared',true,NOW());
 INSERT INTO shared_mailboxes(id,org_id,name,email,identity_id,updated_at) VALUES(10,1,'Team','team@one.test',10,NOW());
 INSERT INTO shared_mailbox_members(shared_mailbox_id,user_id,can_read,can_send) VALUES(10,2,true,%t);`, canSend))
	if err != nil {
		t.Fatal(err)
	}
}

func insertOwnedMail(t *testing.T, db *sql.DB, identity, owner int64, subject string) string {
	t.Helper()
	var id string
	err := db.QueryRow(`INSERT INTO received_emails(org_id,domain_id,identity_id,mailbox_owner_id,message_id,from_email,subject,updated_at)
		VALUES(1,1,$1,$2,gen_random_uuid()::text,'sender@example.test',$3,NOW()) RETURNING uuid::text`, identity, owner, subject).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func listSubjects(t *testing.T, s *InboxService, user int64) []string {
	t.Helper()
	res, err := s.ListReceivedEmails(context.Background(), user, &model.InboxListRequest{Folder: "all"})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range res.Emails {
		out = append(out, e.Subject)
	}
	return out
}

func TestMailboxOwnershipFollowsMailboxOwner(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	sharedIdentityFixture(t, db, false)
	ctx := context.Background()
	inbox := &InboxService{db: db}

	personal := insertOwnedMail(t, db, 1, 1, "personal")
	shared := insertOwnedMail(t, db, 10, 2, "shared copy")
	if _, err := db.Exec(`INSERT INTO email_attachments(received_email_id,filename,content_type,size_bytes,s3_bucket,s3_key)
		SELECT id,'a.txt','text/plain',1,'b','k' FROM received_emails WHERE uuid=$1`, shared); err != nil {
		t.Fatal(err)
	}

	// The steward of a shared identity is not a member and sees none of its mail.
	if got := listSubjects(t, inbox, 1); len(got) != 1 || got[0] != "personal" {
		t.Fatalf("steward list = %v", got)
	}
	if _, err := inbox.GetReceivedEmail(ctx, 1, shared); err == nil {
		t.Fatal("steward read a member's shared copy")
	}
	if got := listSubjects(t, inbox, 2); len(got) != 1 || got[0] != "shared copy" {
		t.Fatalf("member list = %v", got)
	}
	counts, err := inbox.GetReceivedEmailCounts(ctx, 2, 10)
	if err != nil || counts.Inbox != 1 {
		t.Fatalf("member counts for shared identity: %+v %v", counts, err)
	}
	if _, err = inbox.GetReceivedEmailCounts(ctx, 1, 10); err == nil {
		t.Fatal("steward scoped counts to a shared identity it is not a member of")
	}
	var attachment string
	if err = db.QueryRow(`SELECT uuid::text FROM email_attachments`).Scan(&attachment); err != nil {
		t.Fatal(err)
	}
	receiving := &ReceivingService{db: db}
	if _, _, _, err = receiving.AttachmentDownload(ctx, 1, shared, attachment); err == nil {
		t.Fatal("steward downloaded a member's attachment")
	}

	// Filters may target a shared identity only for readers.
	filter := func() *model.InboxFilter {
		id := int64(10)
		return &model.InboxFilter{Name: "team", Active: true, ConditionLogic: "all", IdentityID: &id,
			Conditions: []model.FilterCondition{{Field: "subject", Operator: "contains", Value: "x"}}, ActionStar: true}
	}
	if _, err = inbox.SaveFilter(ctx, 1, 2, "", filter()); err != nil {
		t.Fatal("member filter on shared identity:", err)
	}
	if _, err = inbox.SaveFilter(ctx, 1, 1, "", filter()); err == nil {
		t.Fatal("steward filter on shared identity accepted")
	}

	// Transferring a personal identity leaves earlier mail with its old owner.
	if _, err = db.Exec(`UPDATE identities SET user_id=2 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err = inbox.GetReceivedEmail(ctx, 1, personal); err != nil {
		t.Fatal("previous owner lost access to their mail:", err)
	}
	if _, err = inbox.GetReceivedEmail(ctx, 2, personal); err == nil {
		t.Fatal("new identity owner read mail delivered before the transfer")
	}
	if err = inbox.StarReceivedEmails(ctx, 2, []string{personal}, true); err == nil {
		t.Fatal("new identity owner mutated mail delivered before the transfer")
	}
}

func TestSharedIdentityIsForeignToSendPaths(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	sharedIdentityFixture(t, db, true)
	ctx := context.Background()

	compose := &ComposeService{db: db}
	if _, err := compose.authorizeMailboxSender(ctx, 1, 10, ""); err == nil {
		t.Fatal("steward composed as a shared identity")
	}
	if _, err := compose.authorizeMailboxSender(ctx, 1, 1, "team@one.test"); err == nil {
		t.Fatal("shared address accepted as an alias of a personal identity")
	}
	if _, err := compose.authorizeMailboxSender(ctx, 1, 1, ""); err != nil {
		t.Fatal("personal identity rejected:", err)
	}

	svc := &TransactionalService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: &mailboxTestProvider{}}
	req := &model.SendEmailRequest{From: "team@one.test", To: []string{"r@example.net"}, Subject: "s", Text: "t"}
	if _, err := svc.SendEmailForUser(ctx, 1, SendActor{UserID: 1}, req); err == nil {
		t.Fatal("transactional API sent as a shared identity")
	}

	// Events name a shared identity only for its members.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = eventoutbox.Emit(ctx, tx, eventoutbox.Event{Type: "email.sent", OrgID: 1, UserID: 1, IdentityID: 10, DedupeKey: "steward"}); err == nil {
		t.Fatal("event attributed a shared identity to its steward")
	}
	if err = eventoutbox.Emit(ctx, tx, eventoutbox.Event{Type: "email.sent", OrgID: 1, UserID: 2, IdentityID: 10, DedupeKey: "member"}); err != nil {
		t.Fatal("member event rejected:", err)
	}
}
