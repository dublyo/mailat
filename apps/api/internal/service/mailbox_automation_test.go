package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/lib/pq"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
)

func insertAutomationMail(t *testing.T, db *sql.DB, identity int) string {
	t.Helper()
	var id string
	err := db.QueryRow(`INSERT INTO received_emails(org_id,domain_id,identity_id,message_id,from_email,to_emails,subject,received_at,updated_at) SELECT d.org_id,d.id,i.id,'<test@one.test>','sender@example.test',ARRAY[i.email],'Audit message',NOW(),NOW() FROM identities i JOIN domains d ON d.id=i.domain_id WHERE i.id=$1 RETURNING uuid`, identity).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func TestMailboxLabelsAndReadContract(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	ctx := context.Background()
	s := &InboxService{db: db}
	id := insertAutomationMail(t, db, 1)
	foreign := insertAutomationMail(t, db, 2)
	email, err := s.GetReceivedEmail(ctx, 1, id)
	if err != nil || email.IsRead {
		t.Fatalf("GET changed read state: %v %+v", err, email)
	}
	var read bool
	if err = db.QueryRow(`SELECT is_read FROM received_emails WHERE uuid=$1`, id).Scan(&read); err != nil || read {
		t.Fatal("detail persisted read state", err)
	}
	label, err := s.SaveLabel(ctx, 1, 1, "", "Invoices", "#123abc")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.LabelReceivedEmails(ctx, 1, []string{id, foreign}, []string{label.Name}, nil); !errors.Is(err, ErrMailboxNotFound) {
		t.Fatal("mixed-user label mutation allowed", err)
	}
	if err = s.LabelReceivedEmails(ctx, 1, []string{id}, []string{label.Name}, nil); err != nil {
		t.Fatal(err)
	}
	counts, err := s.GetReceivedEmailCounts(ctx, 1, 0)
	if err != nil || counts.Labels[label.Name] != 1 {
		t.Fatalf("labels not counted: %+v %v", counts, err)
	}
	label, err = s.SaveLabel(ctx, 1, 1, label.UUID, "Paid", "")
	if err != nil {
		t.Fatal(err)
	}
	email, err = s.GetReceivedEmail(ctx, 1, id)
	if err != nil || len(email.Labels) != 1 || email.Labels[0] != "Paid" {
		t.Fatalf("rename did not propagate: %+v %v", email, err)
	}
	if err = s.DeleteLabel(ctx, 2, label.UUID); !errors.Is(err, ErrMailboxNotFound) {
		t.Fatal("foreign delete", err)
	}
	if err = s.DeleteLabel(ctx, 1, label.UUID); err != nil {
		t.Fatal(err)
	}
	email, err = s.GetReceivedEmail(ctx, 1, id)
	if err != nil || len(email.Labels) != 0 {
		t.Fatal("deleted label retained", err)
	}
	labels, err := s.ListLabels(ctx, 1)
	if err != nil || labels == nil || len(labels) != 0 {
		t.Fatal("empty labels not []", err)
	}
}

func TestMailboxFilterCRUDRunsOnSESAndLegacyRules(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	ctx := context.Background()
	s := &InboxService{db: db}
	label, err := s.SaveLabel(ctx, 1, 1, "", "Sales", "")
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.SaveFilter(ctx, 1, 1, "", &model.InboxFilter{Name: "Sales mail", Active: true, ConditionLogic: "all", Conditions: []model.FilterCondition{{Field: "subject", Operator: "contains", Value: "Invoice"}}, ActionLabels: []string{label.Name}, ActionFolder: "archive", ActionStar: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetFilter(ctx, 2, f.UUID); !errors.Is(err, ErrMailboxNotFound) {
		t.Fatal("foreign filter visible", err)
	}
	receiving := &ReceivingService{db: db, storage: &fakeIncomingStorage{raw: []byte("From: sender@example.test\r\nTo: a@one.test\r\nSubject: Invoice\r\nMessage-ID: <invoice@example.test>\r\n\r\nBody")}}
	auth, err := receiving.AuthorizeNotification(ctx, "arn:aws:sns:us-east-1:123456789012:test", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	n := &model.SESNotification{NotificationType: "Received"}
	n.Mail.MessageId = "filter-api"
	n.Receipt = &model.SESReceipt{Timestamp: time.Now().UTC().Format(time.RFC3339), Recipients: []string{"a@one.test"}, Action: model.SESAction{Type: "S3", BucketName: auth.Bucket, ObjectKey: "incoming/one.test/filter-api"}}
	if err = receiving.ProcessIncomingEmail(ctx, auth, n); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListReceivedEmails(ctx, 1, &model.InboxListRequest{Folder: "archive", Labels: []string{"Sales"}})
	if err != nil || list.Total != 1 || !list.Emails[0].IsStarred {
		t.Fatalf("created filter did not execute %+v %v", list, err)
	}
	f, err = s.GetFilter(ctx, 1, f.UUID)
	if err != nil || f.MatchCount != 1 {
		t.Fatal("filter match count", err)
	}
	if err = s.DeleteFilter(ctx, 1, f.UUID); err != nil {
		t.Fatal(err)
	}
	legacy := &EmailRulesService{db: db}
	r, err := legacy.CreateRule(ctx, 1, 1, &CreateEmailRuleInput{Name: "Legacy API", Active: true, Conditions: []RuleCondition{{Field: "subject", Operator: "contains", Value: "Invoice"}}, Actions: []RuleAction{{Type: "mark_starred"}}})
	if err != nil {
		t.Fatal(err)
	}
	filters, err := s.ListFilters(ctx, 1)
	if err != nil || len(filters) != 1 || !filters[0].ActionStar {
		t.Fatalf("legacy not connected: %+v %v", filters, err)
	}
	edited := filters[0]
	edited.Name = "Updated through SES API"
	edited.ActionMarkRead = true
	if _, err = s.SaveFilter(ctx, 1, 1, edited.UUID, &edited); err != nil {
		t.Fatal(err)
	}
	updated, err := legacy.GetRule(ctx, 1, r.ID)
	if err != nil || updated.Name != edited.Name || len(updated.Actions) != 2 {
		t.Fatalf("compatibility reverse sync: %+v %v", updated, err)
	}
	if err = s.DeleteFilter(ctx, 1, edited.UUID); err != nil {
		t.Fatal(err)
	}
	if _, err = legacy.GetRule(ctx, 1, r.ID); err == nil {
		t.Fatal("legacy deletion not synchronized")
	}
}

func TestMailboxCursorCommitOrderAndDeletion(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	ctx := context.Background()
	s := &InboxService{db: db}
	id := insertAutomationMail(t, db, 1)
	second := insertAutomationMail(t, db, 3)
	foreign := insertAutomationMail(t, db, 2)
	_ = foreign
	initial, err := s.Changes(ctx, 1, "0", 100)
	if err != nil || len(initial.Changes) != 2 || initial.Changes[0].MessageUUID != id {
		t.Fatal("cursor ownership", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE received_emails SET is_starred=true WHERE uuid=$1`, id); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- s.MarkReceivedEmails(ctx, 1, []string{second}, true) }()
	select {
	case err := <-finished:
		t.Fatalf("later write passed uncommitted cursor lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	before, err := s.Changes(ctx, 1, initial.NextCursor, 100)
	if err != nil || len(before.Changes) != 0 {
		t.Fatal("uncommitted cursor visible", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
	after, err := s.Changes(ctx, 1, initial.NextCursor, 1)
	if err != nil || len(after.Changes) != 1 || !after.HasMore {
		t.Fatal("pagination missing change", err)
	}
	next, err := s.Changes(ctx, 1, after.NextCursor, 100)
	if err != nil || len(next.Changes) != 1 {
		t.Fatal("second committed change missing", err)
	}
	if err = s.TrashReceivedEmails(ctx, 1, []string{id}, true); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.Changes(ctx, 1, next.NextCursor, 100)
	if err != nil || len(deleted.Changes) != 1 || deleted.Changes[0].Operation != "deleted" {
		t.Fatal("missing deletion tombstone", err)
	}
	if err = s.PruneMailboxChanges(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Changes(ctx, 1, "0", 100); !errors.Is(err, ErrMailboxCursorExpired) {
		t.Fatal("expired cursor silently lost changes", err)
	}
	now, err := s.Changes(ctx, 1, "now", 100)
	if err != nil || now.NextCursor != deleted.NextCursor {
		t.Fatal("bootstrap cursor", err)
	}
}

func TestLegacyRuleMigrationPreservesInvalidRowsAsInactive(t *testing.T) {
	db := testutil.EmptyDatabase(t)
	files, err := os.ReadDir("../database/migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.Name() >= "008_mailbox_automation.sql" {
			continue
		}
		body, err := os.ReadFile(filepath.Join("../database/migrations", file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(body)); err != nil {
			t.Fatal(file.Name(), err)
		}
	}
	seedReceivedFixture(t, db)
	_, err = db.Exec(`INSERT INTO email_rules(org_id,user_id,name,conditions,actions,active,updated_at) VALUES(1,1,'Missing label','[{"field":"subject","operator":"contains","value":"Invoice"}]','[{"type":"add_label"}]',true,NOW()),(1,1,'Null condition','[{"field":"subject","operator":"contains","value":null}]','[{"type":"mark_read"}]',true,NOW())`)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("../database/migrations/008_mailbox_automation.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(body)); err != nil {
		t.Fatal("migration failed on existing malformed rule", err)
	}
	var preserved int
	if err = db.QueryRow(`SELECT count(*) FROM email_rules WHERE active=false AND migration_warning IS NOT NULL`).Scan(&preserved); err != nil || preserved != 2 {
		t.Fatalf("invalid rules not safely retained %d %v", preserved, err)
	}
	var active int
	if err = db.QueryRow(`SELECT count(*) FROM inbox_filters`).Scan(&active); err != nil || active != 0 {
		t.Fatal("invalid rule silently activated", active, err)
	}
}

func TestSESRegexPreservesEscapesAndIgnoresCase(t *testing.T) {
	receive := &ReceivingService{}
	for _, condition := range []model.FilterCondition{{Field: "subject", Operator: "regex", Value: "^Invoice"}, {Field: "subject", Operator: "regex", Value: `^\D+$`}} {
		if !receive.matchesCondition(model.ReceivedEmail{Subject: "INVOICE"}, condition) {
			t.Fatalf("regex lost case/escape semantics %+v", condition)
		}
	}
	if receive.matchesCondition(model.ReceivedEmail{Subject: "123"}, model.FilterCondition{Field: "subject", Operator: "regex", Value: `^\D+$`}) {
		t.Fatal("regex escape was lowercased")
	}
}

func TestLabelRenameSerializesWithPendingAssignment(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := &InboxService{db: db}
	id := insertAutomationMail(t, db, 1)
	label, err := s.SaveLabel(ctx, 1, 1, "", "Old", "#123abc")
	if err != nil {
		t.Fatal(err)
	}
	// Pause the real assignment after it validated Old, before it writes labels.
	barrier := time.Now().UnixNano()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, barrier); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, barrier)
	_, err = db.ExecContext(ctx, fmt.Sprintf(`CREATE FUNCTION pause_assignment() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(%d);RETURN NEW;END $$; CREATE TRIGGER pause_assignment BEFORE UPDATE OF labels ON received_emails FOR EACH ROW EXECUTE FUNCTION pause_assignment()`, barrier))
	if err != nil {
		t.Fatal(err)
	}
	assigned := make(chan error, 1)
	go func() { assigned <- s.LabelReceivedEmails(ctx, 1, []string{id}, []string{"Old"}, nil) }()
	waiting := false
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		var blocked bool
		err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted AND classid=$1::oid AND objid=$2::oid)`, uint32(uint64(barrier)>>32), uint32(barrier)).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			waiting = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !waiting {
		t.Fatal("assignment did not reach barrier")
	}
	renamed := make(chan error, 1)
	go func() { _, err := s.SaveLabel(ctx, 1, 1, label.UUID, "New", ""); renamed <- err }()
	select {
	case err := <-renamed:
		t.Fatalf("rename passed an uncommitted label assignment: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if _, err = conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, barrier); err != nil {
		t.Fatal(err)
	}
	if err = <-assigned; err != nil {
		t.Fatal(err)
	}
	if err = <-renamed; err != nil {
		t.Fatal(err)
	}
	var labels []string
	if err = db.QueryRowContext(ctx, `SELECT labels FROM received_emails WHERE uuid=$1`, id).Scan(pq.Array(&labels)); err != nil || len(labels) != 1 || labels[0] != "New" {
		t.Fatalf("orphan label after rename: %v %v", labels, err)
	}
}
