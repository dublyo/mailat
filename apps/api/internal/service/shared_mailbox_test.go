package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
)

// sharedFixture is seedReceivedFixture with user 1 as owner, user 2 as a
// member, and users 5-8 as further members of org 1.
type sharedFixture struct {
	db    *sql.DB
	svc   *SharedMailboxService
	owner OrgActor
	user2 OrgActor
	uuids map[int64]string
}

func newSharedFixture(t *testing.T) *sharedFixture {
	t.Helper()
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	if _, err := db.Exec(`
 SELECT setval(pg_get_serial_sequence('identities','id'), 1000);
 UPDATE users SET role='owner' WHERE id=1;
 UPDATE domains SET ses_verified=true WHERE id IN (1,2);
 UPDATE identities SET can_send=true WHERE id IN (1,2);
 INSERT INTO users(id,org_id,email,password_hash,updated_at) VALUES(5,1,'user5@example.test','unused',NOW()),(6,1,'user6@example.test','unused',NOW()),(7,1,'user7@example.test','unused',NOW()),(8,1,'user8@example.test','unused',NOW());`); err != nil {
		t.Fatal(err)
	}
	f := &sharedFixture{db: db, svc: NewSharedMailboxService(db, &config.Config{EmailProvider: "ses", DisableAppLimits: true}),
		owner: OrgActor{UserID: 1, OrgID: 1, Role: "owner"}, user2: OrgActor{UserID: 2, OrgID: 1, Role: "member"}, uuids: map[int64]string{}}
	rows, err := db.Query(`SELECT id,uuid::text FROM users`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var u string
		if err = rows.Scan(&id, &u); err != nil {
			t.Fatal(err)
		}
		f.uuids[id] = u
	}
	return f
}

func (f *sharedFixture) create(t *testing.T, email string) *SharedMailbox {
	t.Helper()
	mb, err := f.svc.Create(context.Background(), f.owner, &CreateSharedMailboxInput{Name: "Team " + email, Email: email})
	if err != nil {
		t.Fatal(err)
	}
	return mb
}

func (f *sharedFixture) add(t *testing.T, mb *SharedMailbox, user int64, read, send bool) {
	t.Helper()
	if _, err := f.svc.AddMember(context.Background(), f.owner, mb.ID, &AddMemberInput{UserUUID: f.uuids[user], CanRead: read, CanSend: send}); err != nil {
		t.Fatal(err)
	}
}

func wantOrgStatus(t *testing.T, err error, status int, what string) {
	t.Helper()
	var oe *OrgError
	if !errors.As(err, &oe) || oe.Status != status {
		t.Fatalf("%s: want %d, got %v", what, status, err)
	}
}

func (f *sharedFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSharedMailboxLifecycle(t *testing.T) {
	f := newSharedFixture(t)
	ctx := context.Background()

	_, err := f.svc.Create(ctx, f.user2, &CreateSharedMailboxInput{Name: "x", Email: "x@one.test"})
	wantOrgStatus(t, err, http.StatusForbidden, "member create")
	_, err = f.svc.Create(ctx, f.owner, &CreateSharedMailboxInput{Name: "x", Email: "x@other.test"})
	wantOrgStatus(t, err, http.StatusBadRequest, "foreign domain")
	_, err = f.svc.Create(ctx, f.owner, &CreateSharedMailboxInput{Name: "x", Email: "a@one.test"})
	wantOrgStatus(t, err, http.StatusConflict, "existing identity address")

	// An unlinked legacy row with the same address is replaced.
	if _, err = f.db.Exec(`INSERT INTO shared_mailboxes(org_id,name,email,updated_at) VALUES(1,'Old','team@one.test',NOW())`); err != nil {
		t.Fatal(err)
	}
	mb := f.create(t, "Team@one.test")
	if !mb.Active || mb.Email != "team@one.test" || mb.IdentityUUID == "" || mb.MemberCount != 1 || !mb.CanRead || !mb.CanSend || !mb.CanManage {
		t.Fatalf("created mailbox %+v", mb)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM shared_mailboxes WHERE email='team@one.test'`); n != 1 {
		t.Fatalf("legacy row kept: %d rows", n)
	}
	_, err = f.svc.Create(ctx, f.owner, &CreateSharedMailboxInput{Name: "x", Email: "team@one.test"})
	wantOrgStatus(t, err, http.StatusConflict, "duplicate")

	// Shared identities are never default or catch-all.
	var kind string
	var isDefault, catchAll bool
	if err = f.db.QueryRow(`SELECT kind,is_default,is_catch_all FROM identities WHERE uuid=$1`, mb.IdentityUUID).Scan(&kind, &isDefault, &catchAll); err != nil || kind != "shared" || isDefault || catchAll {
		t.Fatalf("identity kind=%s default=%t catchAll=%t err=%v", kind, isDefault, catchAll, err)
	}
	if _, err = f.db.Exec(`UPDATE identities SET is_catch_all=true WHERE uuid=$1`, mb.IdentityUUID); err == nil {
		t.Fatal("shared identity became a catch-all")
	}
	if _, err = f.db.Exec(`UPDATE identities SET is_default=true WHERE uuid=$1`, mb.IdentityUUID); err == nil {
		t.Fatal("shared identity became a default")
	}

	// Non-members see nothing and cannot manage.
	if list, err := f.svc.List(ctx, f.user2); err != nil || len(list) != 0 {
		t.Fatalf("non-member list %v %v", list, err)
	}
	_, err = f.svc.Get(ctx, f.user2, mb.ID)
	wantOrgStatus(t, err, http.StatusNotFound, "non-member get")
	_, err = f.svc.ListMembers(ctx, f.user2, mb.ID)
	wantOrgStatus(t, err, http.StatusNotFound, "non-member members")
	_, err = f.svc.AddMember(ctx, f.user2, mb.ID, &AddMemberInput{UserUUID: f.uuids[2], CanRead: true})
	wantOrgStatus(t, err, http.StatusForbidden, "non-member add")
	_, err = f.svc.Get(ctx, OrgActor{UserID: 3, OrgID: 2, Role: "owner"}, mb.ID)
	wantOrgStatus(t, err, http.StatusNotFound, "other org admin get")

	_, err = f.svc.AddMember(ctx, f.owner, mb.ID, &AddMemberInput{UserUUID: f.uuids[3], CanRead: true})
	wantOrgStatus(t, err, http.StatusBadRequest, "other-org user")
	_, err = f.svc.AddMember(ctx, f.owner, mb.ID, &AddMemberInput{UserUUID: f.uuids[2], CanManage: true})
	wantOrgStatus(t, err, http.StatusBadRequest, "no read or send")
	f.add(t, mb, 2, true, false)
	_, err = f.svc.AddMember(ctx, f.owner, mb.ID, &AddMemberInput{UserUUID: f.uuids[2], CanRead: true})
	wantOrgStatus(t, err, http.StatusConflict, "duplicate member")

	// A member sees only the mailboxes they belong to.
	other := f.create(t, "sales@one.test")
	list, err := f.svc.List(ctx, f.user2)
	if err != nil || len(list) != 1 || list[0].ID != mb.ID || !list[0].CanRead || list[0].CanManage {
		t.Fatalf("member list %+v %v", list, err)
	}
	if all, err := f.svc.List(ctx, f.owner); err != nil || len(all) != 2 {
		t.Fatalf("admin list %+v %v", all, err)
	}
	_, err = f.svc.Get(ctx, f.user2, other.ID)
	wantOrgStatus(t, err, http.StatusNotFound, "member get of another mailbox")
	members, err := f.svc.ListMembers(ctx, f.user2, mb.ID)
	if err != nil || len(members) != 2 {
		t.Fatalf("members %+v %v", members, err)
	}
	identities, err := (&IdentityService{db: f.db}).ListIdentities(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	var shared []*model.Identity
	for _, i := range identities {
		if i.Shared {
			shared = append(shared, i)
		}
	}
	if len(shared) != 1 || shared[0].Email != "team@one.test" || !shared[0].CanRead || shared[0].CanSend || shared[0].CanManage || shared[0].SharedMailboxName != mb.Name || shared[0].UserID != 0 || identities[0].Shared {
		t.Fatalf("member identities %+v", identities)
	}

	// can_manage is required to change membership.
	_, err = f.svc.UpdateMember(ctx, f.user2, mb.ID, f.uuids[2], &UpdateMemberInput{CanRead: true, CanSend: true})
	wantOrgStatus(t, err, http.StatusForbidden, "member update")
	err = f.svc.RemoveMember(ctx, f.user2, mb.ID, f.uuids[1])
	wantOrgStatus(t, err, http.StatusForbidden, "member remove")

	// The last reader stays.
	if _, err = f.svc.UpdateMember(ctx, f.owner, mb.ID, f.uuids[1], &UpdateMemberInput{CanSend: true, CanManage: true}); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.UpdateMember(ctx, f.owner, mb.ID, f.uuids[2], &UpdateMemberInput{CanSend: true})
	wantOrgStatus(t, err, http.StatusConflict, "last reader update")
	err = f.svc.RemoveMember(ctx, f.owner, mb.ID, f.uuids[2])
	wantOrgStatus(t, err, http.StatusConflict, "last reader remove")
	err = f.svc.RemoveMember(ctx, f.owner, mb.ID, f.uuids[5])
	wantOrgStatus(t, err, http.StatusNotFound, "remove non-member")
	// A can_manage member may manage without being an admin.
	if _, err = f.svc.UpdateMember(ctx, f.owner, mb.ID, f.uuids[2], &UpdateMemberInput{CanRead: true, CanManage: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.UpdateMember(ctx, f.user2, mb.ID, f.uuids[1], &UpdateMemberInput{CanRead: true}); err != nil {
		t.Fatal("can_manage member update:", err)
	}
	if err = f.svc.RemoveMember(ctx, f.user2, mb.ID, f.uuids[1]); err != nil {
		t.Fatal("can_manage member remove:", err)
	}

	// Member cap.
	if _, err = f.db.Exec(`INSERT INTO users(id,org_id,email,password_hash,updated_at) SELECT g,1,'bulk'||g||'@example.test','unused',NOW() FROM generate_series(100,148) g`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`INSERT INTO shared_mailbox_members(shared_mailbox_id,user_id) SELECT $1,g FROM generate_series(100,148) g`, mb.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.AddMember(ctx, f.owner, mb.ID, &AddMemberInput{UserUUID: f.uuids[5], CanRead: true})
	wantOrgStatus(t, err, http.StatusConflict, "member cap")

	// Delete: admin only; removes the identity, members and copies.
	insertOwnedMail(t, f.db, identityIDByUUID(t, f.db, mb.IdentityUUID), 2, "shared")
	err = f.svc.Delete(ctx, f.user2, mb.ID)
	wantOrgStatus(t, err, http.StatusForbidden, "member delete")
	if err = f.svc.Delete(ctx, f.owner, mb.ID); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT (SELECT COUNT(*) FROM identities WHERE email='team@one.test')+(SELECT COUNT(*) FROM shared_mailboxes WHERE id=$1)+(SELECT COUNT(*) FROM received_emails WHERE subject='shared')+(SELECT COUNT(*) FROM shared_mailbox_members WHERE shared_mailbox_id=$1)`, mb.ID); n != 0 {
		t.Fatalf("delete left %d rows", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM audit_logs WHERE action LIKE 'shared_%'`); n < 6 {
		t.Fatalf("audit rows %d", n)
	}
}

func identityIDByUUID(t *testing.T, db *sql.DB, u string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(`SELECT id FROM identities WHERE uuid=$1`, u).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *sharedFixture) ingest(t *testing.T, sesID string, rcpts ...string) error {
	t.Helper()
	storage := &fakeIncomingStorage{raw: []byte("From: Sender <sender@example.test>\r\nTo: team@one.test, a@one.test\r\nMessage-ID: <" + sesID + "@example.test>\r\nSubject: Hello " + sesID + "\r\n\r\nbody")}
	svc := &ReceivingService{db: f.db, storage: storage}
	auth := &ReceivingAuthorization{OrgID: 1, TopicARN: "arn:aws:sns:us-east-1:123456789012:test", Bucket: "test-bucket", Region: "us-east-1"}
	n := &model.SESNotification{NotificationType: "Received", Mail: model.SESMail{MessageId: sesID}, Receipt: &model.SESReceipt{Timestamp: time.Now().UTC().Format(time.RFC3339), Recipients: rcpts, Action: model.SESAction{Type: "S3", BucketName: auth.Bucket, ObjectKey: "incoming/one.test/" + sesID}}}
	return svc.ProcessIncomingEmail(context.Background(), auth, n)
}

func (f *sharedFixture) owners(t *testing.T, identity int64, sesID string) []int64 {
	t.Helper()
	rows, err := f.db.Query(`SELECT mailbox_owner_id FROM received_emails WHERE identity_id=$1 AND ses_message_id=$2 ORDER BY 1`, identity, sesID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func TestSharedMailboxIngestFanOut(t *testing.T) {
	f := newSharedFixture(t)
	ctx := context.Background()
	mb := f.create(t, "team@one.test")
	team := identityIDByUUID(t, f.db, mb.IdentityUUID)
	f.add(t, mb, 2, true, false)
	f.add(t, mb, 5, true, true)
	f.add(t, mb, 6, true, false)
	f.add(t, mb, 7, false, true) // sender only: no copy
	f.add(t, mb, 8, true, false) // disabled below: no copy
	if _, err := f.db.Exec(`UPDATE users SET status='disabled' WHERE id=8`); err != nil {
		t.Fatal(err)
	}
	// The creator steps out, leaving user 1 as a non-member steward.
	if err := f.svc.RemoveMember(ctx, f.owner, mb.ID, f.uuids[1]); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ { // the SNS retry adds nothing
		if err := f.ingest(t, "ses-team-1", "team@one.test", "a@one.test"); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.owners(t, team, "ses-team-1"); fmt.Sprint(got) != "[2 5 6]" {
		t.Fatalf("shared copies owned by %v", got)
	}
	if got := f.owners(t, 1, "ses-team-1"); fmt.Sprint(got) != "[1]" {
		t.Fatalf("personal copy owned by %v", got)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM mailbox_changes WHERE identity_id=$1 AND operation='created'`, team); n != 3 {
		t.Fatalf("mailbox changes %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM webhook_events WHERE dedupe_key LIKE $1`, fmt.Sprintf("received:ses-team-1:%d:%%", team)); n != 3 {
		t.Fatalf("shared outbox events %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM webhook_events WHERE dedupe_key='received:ses-team-1:1'`); n != 1 {
		t.Fatalf("personal outbox events %d", n)
	}

	// Steward isolation: user 1 sees only the personal copy.
	inbox := &InboxService{db: f.db}
	if got := listSubjects(t, inbox, 1); len(got) != 1 {
		t.Fatalf("steward list %v", got)
	}
	var sharedCopy string
	if err := f.db.QueryRow(`SELECT uuid::text FROM received_emails WHERE identity_id=$1 AND mailbox_owner_id=2`, team).Scan(&sharedCopy); err != nil {
		t.Fatal(err)
	}
	if _, err := inbox.GetReceivedEmail(ctx, 1, sharedCopy); err == nil {
		t.Fatal("steward read a shared copy")
	}
	compose := &ComposeService{db: f.db, cfg: &config.Config{EmailProvider: "ses"}}
	if _, err := compose.GetReplyContext(ctx, 1, sharedCopy, false); err == nil {
		t.Fatal("steward got a reply context for a shared copy")
	}
	if got := listSubjects(t, inbox, 2); len(got) != 1 {
		t.Fatalf("member list %v", got)
	}
	if _, err := inbox.GetReceivedEmail(ctx, 5, sharedCopy); err == nil {
		t.Fatal("member read another member's copy")
	}

	// A disabled steward never stops shared delivery, but its personal identities stop receiving.
	if _, err := f.db.Exec(`UPDATE users SET status='disabled' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := f.ingest(t, "ses-team-2", "team@one.test", "a@one.test"); err != nil {
		t.Fatal(err)
	}
	if got := f.owners(t, team, "ses-team-2"); fmt.Sprint(got) != "[2 5 6]" {
		t.Fatalf("shared copies with disabled steward: %v", got)
	}
	if got := f.owners(t, 1, "ses-team-2"); len(got) != 0 {
		t.Fatalf("disabled user's identity received mail: %v", got)
	}
}

// A suspended mailbox user keeps receiving shared copies like personal mail,
// so it counts as a reader; a suspended staff member gets none and does not.
func TestSharedMailboxSuspendedReaders(t *testing.T) {
	f := newSharedFixture(t)
	ctx := context.Background()
	mb := f.create(t, "team@one.test")
	team := identityIDByUUID(t, f.db, mb.IdentityUUID)
	f.add(t, mb, 5, true, false)
	f.add(t, mb, 6, true, false)
	if _, err := f.db.Exec(`UPDATE users SET role='mailbox',status='suspended' WHERE id=5; UPDATE users SET status='suspended' WHERE id=6`); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RemoveMember(ctx, f.owner, mb.ID, f.uuids[1]); err != nil {
		t.Fatal(err)
	}
	if err := f.ingest(t, "ses-suspended-1", "team@one.test", "a@one.test"); err != nil {
		t.Fatal(err)
	}
	if got := f.owners(t, team, "ses-suspended-1"); fmt.Sprint(got) != "[5]" {
		t.Fatalf("shared copies owned by %v", got)
	}
	// User 6 gets no copies, so user 5 is the last reader.
	err := f.svc.RemoveMember(ctx, f.owner, mb.ID, f.uuids[5])
	var oe *OrgError
	if !errors.As(err, &oe) || oe.Status != http.StatusConflict {
		t.Fatal("removed the last reader that gets copies:", err)
	}
}

func TestSharedMailboxRevokeReadDropsDeliveredCopies(t *testing.T) {
	f := newSharedFixture(t)
	ctx := context.Background()
	mb := f.create(t, "team@one.test")
	team := identityIDByUUID(t, f.db, mb.IdentityUUID)
	f.add(t, mb, 2, true, false)
	f.add(t, mb, 5, true, true)
	if err := f.ingest(t, "ses-revoke-1", "team@one.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE received_emails SET raw_s3_bucket='test-bucket',raw_s3_key='incoming/one.test/ses-revoke-1' WHERE identity_id=$1`, team); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO received_emails(org_id,domain_id,identity_id,mailbox_owner_id,message_id,from_email,subject,direction,folder,send_status,updated_at)
		SELECT 1,domain_id,id,5,'out-team-5','team@one.test','Sent by 5','outbound','sent','sent',NOW() FROM identities WHERE id=$1`, team); err != nil {
		t.Fatal(err)
	}

	// Keeping send-only access removes the delivered copies, not sent mail.
	if _, err := f.svc.UpdateMember(ctx, f.owner, mb.ID, f.uuids[5], &UpdateMemberInput{CanSend: true}); err != nil {
		t.Fatal(err)
	}
	if got := f.owners(t, team, "ses-revoke-1"); fmt.Sprint(got) != "[1 2]" {
		t.Fatalf("copies after revoking read: %v", got)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM received_emails WHERE identity_id=$1 AND mailbox_owner_id=5 AND direction='outbound'`, team); n != 1 {
		t.Fatalf("sent copies kept %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM storage_cleanup_jobs WHERE object_key='incoming/one.test/ses-revoke-1'`); n != 1 {
		t.Fatalf("storage cleanup jobs %d", n)
	}
	inbox := &InboxService{db: f.db}
	if got := listSubjects(t, inbox, 5); len(got) != 1 || got[0] != "Sent by 5" {
		t.Fatalf("member without read access lists %v", got)
	}

	// Changing other permissions of a reader keeps its copies.
	if _, err := f.svc.UpdateMember(ctx, f.owner, mb.ID, f.uuids[2], &UpdateMemberInput{CanRead: true, CanSend: true}); err != nil {
		t.Fatal(err)
	}
	if got := f.owners(t, team, "ses-revoke-1"); fmt.Sprint(got) != "[1 2]" {
		t.Fatalf("copies after a reader update: %v", got)
	}
}

func TestSharedMailboxRemovalWaitsForIngest(t *testing.T) {
	f := newSharedFixture(t)
	ctx := context.Background()
	mb := f.create(t, "team@one.test")
	team := identityIDByUUID(t, f.db, mb.IdentityUUID)
	f.add(t, mb, 2, true, false)

	// Hold the ingest-side membership lock, as ProcessIncomingEmail does.
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	owners, err := mailboxOwners(ctx, tx, &recipientIdentity{ID: team, Kind: "shared"})
	if err != nil || fmt.Sprint(owners) != "[1 2]" {
		t.Fatalf("owners %v %v", owners, err)
	}
	remove := concurrently(func() error { return f.svc.RemoveMember(ctx, f.owner, mb.ID, f.uuids[2]) })
	defer remove()
	waitForLockWait(t, f.db, "DELETE FROM shared_mailbox_members")
	if _, err = tx.Exec(`INSERT INTO received_emails(org_id,domain_id,identity_id,mailbox_owner_id,message_id,from_email,subject,ses_message_id,updated_at)
		VALUES(1,1,$1,2,'<late@x>','s@example.test','late','late',NOW())`, team); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = remove(); err != nil {
		t.Fatal(err)
	}
	if got := f.owners(t, team, "late"); len(got) != 0 {
		t.Fatalf("orphan copy left for removed member: %v", got)
	}

	// Real ingest racing removal never leaves a copy for a non-member.
	for i := 0; i < 5; i++ {
		f.add(t, mb, 5, true, false)
		sesID := fmt.Sprintf("race-%d", i)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() { defer wg.Done(); errs[0] = f.ingest(t, sesID, "team@one.test") }()
		go func() { defer wg.Done(); errs[1] = f.svc.RemoveMember(ctx, f.owner, mb.ID, f.uuids[5]) }()
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatal(errs)
		}
		// Either order ends with the remaining reader's copy only.
		if got := f.owners(t, team, sesID); fmt.Sprint(got) != "[1]" {
			t.Fatalf("race %d copies %v", i, got)
		}
	}
}

func TestSharedMailboxSendingAndRules(t *testing.T) {
	f := newSharedFixture(t)
	ctx := context.Background()
	mb := f.create(t, "team@one.test")
	team := identityIDByUUID(t, f.db, mb.IdentityUUID)
	f.add(t, mb, 2, true, true)
	f.add(t, mb, 5, true, false)
	compose := &ComposeService{db: f.db, cfg: &config.Config{EmailProvider: "ses"}}

	if s, err := compose.authorizeMailboxSender(ctx, 2, team, ""); err != nil || s.email != "team@one.test" {
		t.Fatalf("can_send member: %+v %v", s, err)
	}
	if _, err := compose.authorizeMailboxSender(ctx, 2, team, "team+billing@one.test"); err != nil {
		t.Fatal("member +tag alias:", err)
	}
	if _, err := compose.authorizeMailboxSender(ctx, 2, team, "anything@one.test"); err != errMemberAlias {
		t.Fatal("member free alias:", err)
	}
	if _, err := compose.authorizeMailboxSender(ctx, 2, 2, "b+x@one.test"); err != nil {
		t.Fatal("member personal +tag:", err)
	}
	if _, err := compose.authorizeMailboxSender(ctx, 2, 2, "free@one.test"); err != errMemberAlias {
		t.Fatal("member personal free alias:", err)
	}
	if _, err := compose.authorizeMailboxSender(ctx, 1, 1, "free@one.test"); err != nil {
		t.Fatal("owner alias:", err)
	}
	if _, err := compose.authorizeMailboxSender(ctx, 1, 1, "team+x@one.test"); err != errForeignSender {
		t.Fatal("owner sent as a shared identity's +tag:", err)
	}
	if _, err := compose.authorizeMailboxSender(ctx, 5, team, ""); err == nil {
		t.Fatal("reader without can_send composed as the shared identity")
	}
	if _, err := compose.authorizeMailboxSender(ctx, 1, 1, "team@one.test"); err == nil {
		t.Fatal("shared address used as an alias of a personal identity")
	}
	for alias, ok := range map[string]bool{"b@one.test": true, "B+x@one.test": true, "b+@one.test": false, "bb@one.test": false, "b+x@two.test": false, "b+x@y@one.test": false} {
		if memberAliasAllowed("b@one.test", alias) != ok {
			t.Errorf("memberAliasAllowed(%q) != %t", alias, ok)
		}
	}

	// A member replying to mail sent to another address answers from the identity address.
	var reply string
	if err := f.db.QueryRow(`INSERT INTO received_emails(org_id,domain_id,identity_id,message_id,from_email,subject,envelope_recipients,updated_at)
		VALUES(1,1,2,'<m@x>','s@example.test','Hi',ARRAY['sales@one.test'],NOW()) RETURNING uuid::text`).Scan(&reply); err != nil {
		t.Fatal(err)
	}
	rc, err := compose.GetReplyContext(ctx, 2, reply, false)
	if err != nil || rc.From.Email != "b@one.test" {
		t.Fatalf("member reply context %+v %v", rc, err)
	}

	// Shared forwards need can_manage and keepCopy.
	forwards := NewAutoReplyService(f.db, &config.Config{EmailProvider: "ses"})
	_, err = forwards.CreateEmailForward(ctx, 2, 1, &CreateEmailForwardInput{IdentityUUID: mb.IdentityUUID, ForwardTo: "out@elsewhere.test", KeepCopy: true})
	var fv *ForwardValidationError
	if !errors.As(err, &fv) || fv.Message != "identity not found" {
		t.Fatal("non-manager forward:", err)
	}
	if _, err = forwards.CreateEmailForward(ctx, 1, 1, &CreateEmailForwardInput{IdentityUUID: mb.IdentityUUID, ForwardTo: "out@elsewhere.test"}); err != errSharedForwardKeepCopy {
		t.Fatal("shared forward without keepCopy:", err)
	}
	var fwd string
	if err = f.db.QueryRow(`INSERT INTO email_forwards(user_id,org_id,identity_id,forward_to,keep_copy,status,active,verified,updated_at) VALUES(1,1,$1,'out@elsewhere.test',true,'active',true,true,now()) RETURNING uuid::text`, team).Scan(&fwd); err != nil {
		t.Fatal(err)
	}
	no := false
	if _, err = forwards.UpdateEmailForward(ctx, 1, fwd, &UpdateEmailForwardInput{KeepCopy: &no}); err != errSharedForwardKeepCopy {
		t.Fatal("shared forward keepCopy=false update:", err)
	}

	// Auto-replies on a shared identity: listed explicitly by a can_manage member.
	if _, err = f.db.Exec(`INSERT INTO auto_replies(user_id,org_id,name,start_date,subject,html_content,identity_ids,active,created_at,updated_at) VALUES
		(1,1,'All personal',now()-interval '1 hour','Away','x','{}',true,now()-interval '2 hours',now()),
		(5,1,'Reader',now()-interval '1 hour','Away','x',ARRAY[$1::int],true,now()-interval '3 hours',now()),
		(1,1,'Team',now()-interval '1 hour','Away','x',ARRAY[$1::int],true,now()-interval '1 hour',now())`, team); err != nil {
		t.Fatal(err)
	}
	id, user, err := activeRuleForIdentity(ctx, f.db, team)
	if err != nil || user != 1 || id == 0 {
		t.Fatalf("shared rule id=%d user=%d err=%v", id, user, err)
	}
	if _, user, err = activeRuleForIdentity(ctx, f.db, 1); err != nil || user != 1 {
		t.Fatalf("personal rule user=%d err=%v", user, err)
	}
}

// '+' stays reserved for local+tag routing: neither a shared mailbox nor a new
// identity may take a +tag of b@one.test, which routes to b's mailbox.
func TestPlusAddressesStayReserved(t *testing.T) {
	f := newSharedFixture(t)
	ctx := context.Background()
	_, err := f.svc.Create(ctx, f.owner, &CreateSharedMailboxInput{Name: "Bills", Email: "b+bills@one.test"})
	var oe *OrgError
	if !errors.As(err, &oe) || oe.Status != http.StatusBadRequest {
		t.Fatal("shared +tag mailbox:", err)
	}
	var domain string
	if err = f.db.QueryRow(`SELECT uuid FROM domains WHERE id=1`).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	ids := NewIdentityService(f.db, &config.Config{EmailProvider: "ses"})
	if _, err = ids.CreateIdentity(ctx, 1, &model.CreateIdentityRequest{DomainId: domain, Email: "b+bills@one.test", DisplayName: "Bills"}); err == nil || !strings.Contains(err.Error(), "'+'") {
		t.Fatal("+tag identity:", err)
	}
	if _, err = ids.CreateIdentity(ctx, 1, &model.CreateIdentityRequest{DomainId: domain, Email: "bills@one.test", DisplayName: "Bills"}); err != nil {
		t.Fatal("plain identity:", err)
	}
}

// An identity created with '+' before plus-addresses were reserved still owns
// its exact address: routing ranks the exact match first, so its owner may send
// as it, while others are refused for both the exact and the base address.
func TestForeignSenderPrefersExactIdentity(t *testing.T) {
	f := newSharedFixture(t)
	ctx := context.Background()
	var base, tagged int64
	if err := f.db.QueryRow(`INSERT INTO identities(user_id,domain_id,email,can_send,updated_at) VALUES(5,1,'sales@one.test',true,now()) RETURNING id`).Scan(&base); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`INSERT INTO identities(user_id,domain_id,email,can_send,updated_at) VALUES(6,1,'sales+offer@one.test',true,now()) RETURNING id`).Scan(&tagged); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		user, identity int64
		addr           string
		foreign        bool
	}{
		{6, tagged, "sales+offer@one.test", false}, // legacy exact identity owner
		{5, base, "sales+offer@one.test", true},    // exact identity belongs to user 6
		{5, base, "sales+news@one.test", false},    // base owner's own +tag
		{6, tagged, "sales+news@one.test", true},   // base address belongs to user 5
	}
	for _, c := range cases {
		got, err := foreignSender(ctx, f.db, c.user, c.identity, c.addr)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.foreign {
			t.Errorf("user %d sending as %s: foreign=%v, want %v", c.user, c.addr, got, c.foreign)
		}
	}
}
