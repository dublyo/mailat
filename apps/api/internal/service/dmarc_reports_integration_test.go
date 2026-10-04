package service

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
)

func dmarcTestRaw(domain string) []byte {
	return []byte("From: Reports <reports@receiver.test>\r\nTo: a@one.test\r\nSubject: Report domain: " + domain + "\r\nContent-Type: multipart/mixed; boundary=report\r\n\r\n--report\r\nContent-Type: text/plain\r\n\r\nDaily aggregate report\r\n--report\r\nContent-Type: application/gzip\r\nContent-Disposition: attachment; filename=report.xml\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString(dmarcTestXML(domain, 1)) + "\r\n--report--\r\n")
}
func dmarcNotification(id string, recipients ...string) *model.SESNotification {
	return &model.SESNotification{NotificationType: "Received", Mail: model.SESMail{MessageId: id}, Receipt: &model.SESReceipt{Timestamp: time.Now().UTC().Format(time.RFC3339), Recipients: recipients, Action: model.SESAction{Type: "S3", BucketName: "test-bucket", ObjectKey: "incoming/one.test/" + id}, DMARCVerdict: model.SESVerdict{Status: "PASS"}, SPFVerdict: model.SESVerdict{Status: "PASS"}, DKIMVerdict: model.SESVerdict{Status: "PASS"}, SpamVerdict: model.SESVerdict{Status: "PASS"}, VirusVerdict: model.SESVerdict{Status: "PASS"}}}
}
func dmarcReceiving(t *testing.T, db *sql.DB, storage incomingStorage) (*ReceivingService, *ReceivingAuthorization) {
	t.Helper()
	s := &ReceivingService{db: db, storage: storage}
	a, err := s.AuthorizeNotification(context.Background(), "arn:aws:sns:us-east-1:123456789012:test", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	return s, a
}
func TestDMARCReceivingRoutesPerUserBeforeEvents(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	ctx := context.Background()
	_, err := db.Exec(`INSERT INTO user_settings(org_id,user_id,auto_organize_dmarc_reports,updated_at) VALUES(1,2,false,NOW()); INSERT INTO inbox_filters(org_id,user_id,identity_id,name,conditions,action_folder,action_star,action_mark_read,updated_at) VALUES(1,1,3,'explicit archive','[{"field":"subject","operator":"contains","value":"Report"}]','archive',true,true,NOW())`)
	if err != nil {
		t.Fatal(err)
	}
	storage := &fakeIncomingStorage{raw: dmarcTestRaw("one.test")}
	s, a := dmarcReceiving(t, db, storage)
	notices := map[int64]string{}
	s.SetNotifier(func(_ int64, e *model.ReceivedEmail) { notices[e.IdentityID] = e.Folder })
	n := dmarcNotification("route-report", "a@one.test", "b@one.test", "reports@two.test")
	if err = s.ProcessIncomingEmail(ctx, a, n); err != nil {
		t.Fatal(err)
	}
	if err = s.ProcessIncomingEmail(ctx, a, n); err != nil {
		t.Fatal(err)
	}
	for identity, want := range map[int64]string{1: DMARCReportsFolder, 2: "inbox", 3: "archive"} {
		var folder string
		var read, star bool
		if err = db.QueryRow(`SELECT folder,is_read,is_starred FROM received_emails WHERE identity_id=$1`, identity).Scan(&folder, &read, &star); err != nil || folder != want {
			t.Fatalf("identity %d folder %s: %v", identity, folder, err)
		}
		if identity == 3 && (!read || !star) {
			t.Fatal("filter actions lost")
		}
		if notices[identity] != want {
			t.Fatalf("notification used stale folder: %v", notices)
		}
		var eventFolder string
		if err = db.QueryRow(`SELECT payload->'data'->>'folder' FROM webhook_events WHERE payload->'data'->>'messageUuid'=(SELECT uuid::text FROM received_emails WHERE identity_id=$1)`, identity).Scan(&eventFolder); err != nil || eventFolder != want {
			t.Fatalf("event %s want %s: %v", eventFolder, want, err)
		}
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM webhook_events WHERE event_type='email.received'`).Scan(&count)
	if count != 3 {
		t.Fatalf("duplicate notification emitted %d events", count)
	}
	var attachments int
	db.QueryRow(`SELECT COUNT(*) FROM email_attachments`).Scan(&attachments)
	if attachments != 3 {
		t.Fatal("report attachments lost")
	}
}
func TestDMARCReceivingConservativeAndSpamPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, domain, verdict string
		invalid               bool
		folder                string
	}{
		{name: "unowned domain", domain: "other.test", folder: "inbox"},
		{name: "missing authentication", domain: "one.test", verdict: "missing", folder: "inbox"},
		{name: "malformed report", domain: "one.test", invalid: true, folder: "inbox"},
		{name: "spam cannot promote", domain: "one.test", verdict: "spam", folder: "spam"},
		{name: "virus cannot promote", domain: "one.test", verdict: "virus", folder: "spam"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testutil.Database(t)
			seedReceivedFixture(t, db)
			storage := &fakeIncomingStorage{raw: dmarcTestRaw(tc.domain)}
			if tc.invalid {
				storage.raw = []byte(strings.Replace(string(storage.raw), base64.StdEncoding.EncodeToString(dmarcTestXML(tc.domain, 1)), base64.StdEncoding.EncodeToString([]byte("<feedback>broken")), 1))
			}
			s, a := dmarcReceiving(t, db, storage)
			n := dmarcNotification(strings.ReplaceAll(tc.name, " ", "-"), "a@one.test")
			switch tc.verdict {
			case "missing":
				n.Receipt.DMARCVerdict.Status = ""
			case "spam":
				n.Receipt.SpamVerdict.Status = "FAIL"
			case "virus":
				n.Receipt.VirusVerdict.Status = "FAIL"
			}
			if tc.verdict == "spam" || tc.verdict == "virus" {
				if _, err := db.Exec(`INSERT INTO inbox_filters(org_id,user_id,name,conditions,action_folder,action_star,updated_at) VALUES(1,1,'explicit folder','[{"field":"subject","operator":"contains","value":"Report"}]','dmarc-reports',true,NOW())`); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.ProcessIncomingEmail(context.Background(), a, n); err != nil {
				t.Fatal(err)
			}
			var folder string
			var spam bool
			if err := db.QueryRow(`SELECT folder,is_spam FROM received_emails`).Scan(&folder, &spam); err != nil || folder != tc.folder || spam != (tc.folder == "spam") {
				t.Fatalf("folder=%s spam=%v err=%v", folder, spam, err)
			}
		})
	}
	t.Run("spam trash permitted", func(t *testing.T) {
		db := testutil.Database(t)
		seedReceivedFixture(t, db)
		_, err := db.Exec(`INSERT INTO inbox_filters(org_id,user_id,name,conditions,action_trash,updated_at) VALUES(1,1,'discard','[{"field":"subject","operator":"contains","value":"Report"}]',true,NOW())`)
		if err != nil {
			t.Fatal(err)
		}
		s, a := dmarcReceiving(t, db, &fakeIncomingStorage{raw: dmarcTestRaw("one.test")})
		n := dmarcNotification("trash-report", "a@one.test")
		n.Receipt.SpamVerdict.Status = "FAIL"
		if err = s.ProcessIncomingEmail(context.Background(), a, n); err != nil {
			t.Fatal(err)
		}
		var folder string
		db.QueryRow(`SELECT folder FROM received_emails`).Scan(&folder)
		if folder != "trash" {
			t.Fatal(folder)
		}
	})
}
func seedDMARCHistory(t *testing.T, db *sql.DB, identity int, folder string) string {
	t.Helper()
	var id string
	err := db.QueryRow(`INSERT INTO received_emails(org_id,domain_id,identity_id,message_id,from_email,subject,raw_s3_bucket,raw_s3_key,has_attachments,folder,is_archived,is_trashed,is_spam,is_read,is_starred,labels,spf_verdict,dkim_verdict,dmarc_verdict,spam_verdict,virus_verdict,updated_at) VALUES(1,1,$1,gen_random_uuid()::text,'report@receiver.test','Report domain: one.test','test-bucket','history/report',true,$2::text,$2::text='archive',$2::text='trash',$2::text='spam',true,true,ARRAY['kept'],'PASS','PASS','PASS','PASS','PASS',NOW()-interval '1 hour') RETURNING uuid`, identity, folder).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func snapshotDMARCHistory(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`UPDATE dmarc_report_backfills SET cutoff_id=(SELECT COALESCE(MAX(id),0) FROM received_emails),last_id=0,started_at=clock_timestamp(),completed_at=NULL`); err != nil {
		t.Fatal(err)
	}
}

type dmarcHookStorage struct {
	fakeIncomingStorage
	hook func()
}

func (f *dmarcHookStorage) GetEmailFromS3(ctx context.Context, bucket, key string) ([]byte, error) {
	if f.hook != nil {
		hook := f.hook
		f.hook = nil
		hook()
	}
	return f.fakeIncomingStorage.GetEmailFromS3(ctx, bucket, key)
}
func TestDMARCHistorySnapshotAndPreservation(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	ctx := context.Background()
	report := seedDMARCHistory(t, db, 1, "inbox")
	seedDMARCHistory(t, db, 1, "archive")
	seedDMARCHistory(t, db, 1, "trash")
	seedDMARCHistory(t, db, 1, "spam")
	seedDMARCHistory(t, db, 2, "inbox")
	restored := seedDMARCHistory(t, db, 1, "dmarc-reports")
	_, err := db.Exec(`INSERT INTO user_settings(org_id,user_id,auto_organize_dmarc_reports,updated_at) VALUES(1,2,false,NOW())`)
	if err != nil {
		t.Fatal(err)
	}
	snapshotDMARCHistory(t, db)
	newer := seedDMARCHistory(t, db, 1, "inbox")
	if err = (&InboxService{db: db}).MoveReceivedEmails(ctx, 1, []string{restored}, "inbox"); err != nil {
		t.Fatal(err)
	}
	storage := &fakeIncomingStorage{raw: dmarcTestRaw("one.test")}
	s := &ReceivingService{db: db, storage: storage}
	db.SetMaxOpenConns(1)
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	done, err := s.backfillDMARCReportsBatch(bounded)
	if err != nil || !done {
		t.Fatalf("done=%v error=%v", done, err)
	}
	if storage.gets != 1 {
		t.Fatalf("unexpected historical candidates %d", storage.gets)
	}
	var folder string
	var read, star bool
	var labels string
	if err = db.QueryRow(`SELECT folder,is_read,is_starred,array_to_string(labels,',') FROM received_emails WHERE uuid=$1`, report).Scan(&folder, &read, &star, &labels); err != nil || folder != DMARCReportsFolder || !read || !star || labels != "kept" {
		t.Fatalf("preservation %s %v %v %s %v", folder, read, star, labels, err)
	}
	for _, id := range []string{newer, restored} {
		db.QueryRow(`SELECT folder FROM received_emails WHERE uuid=$1`, id).Scan(&folder)
		if folder != "inbox" {
			t.Fatal("new or manually restored message reprocessed")
		}
	}
	var events, changes int
	db.QueryRow(`SELECT COUNT(*) FROM webhook_events`).Scan(&events)
	db.QueryRow(`SELECT COUNT(*) FROM mailbox_changes WHERE message_uuid=$1 AND operation='updated'`, report).Scan(&changes)
	if events != 0 || changes != 1 {
		t.Fatalf("events=%d changes=%d", events, changes)
	}
	if done, err = s.backfillDMARCReportsBatch(ctx); err != nil || !done || storage.gets != 1 {
		t.Fatalf("completed scan not durable: %v %v %d", done, err, storage.gets)
	}
}
func TestDMARCHistoryConcurrentChanges(t *testing.T) {
	for _, kind := range []string{"move", "read", "optout"} {
		t.Run(kind, func(t *testing.T) {
			db := testutil.Database(t)
			seedReceivedFixture(t, db)
			id := seedDMARCHistory(t, db, 1, "inbox")
			snapshotDMARCHistory(t, db)
			storage := &dmarcHookStorage{fakeIncomingStorage: fakeIncomingStorage{raw: dmarcTestRaw("one.test")}}
			storage.hook = func() {
				var err error
				switch kind {
				case "move":
					err = (&InboxService{db: db}).MoveReceivedEmails(context.Background(), 1, []string{id}, "archive")
				case "read":
					err = (&InboxService{db: db}).MarkReceivedEmails(context.Background(), 1, []string{id}, false)
				case "optout":
					_, err = db.Exec(`INSERT INTO user_settings(org_id,user_id,auto_organize_dmarc_reports,updated_at) VALUES(1,1,false,NOW())`)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			s := &ReceivingService{db: db, storage: storage}
			if done, err := s.backfillDMARCReportsBatch(context.Background()); err != nil || !done {
				t.Fatalf("%v %v", done, err)
			}
			var folder string
			db.QueryRow(`SELECT folder FROM received_emails WHERE uuid=$1`, id).Scan(&folder)
			want := "inbox"
			if kind == "move" {
				want = "archive"
			}
			if folder != want {
				t.Fatalf("concurrent %s overwritten: %s", kind, folder)
			}
		})
	}
}
func TestDMARCHistoryFailuresRetryAndResume(t *testing.T) {
	t.Run("storage bounded retry", func(t *testing.T) {
		db := testutil.Database(t)
		seedReceivedFixture(t, db)
		id := seedDMARCHistory(t, db, 1, "inbox")
		snapshotDMARCHistory(t, db)
		storage := &fakeIncomingStorage{err: fmt.Errorf("private unavailable")}
		s := &ReceivingService{db: db, storage: storage}
		for attempt := 1; attempt <= 3; attempt++ {
			done, err := s.backfillDMARCReportsBatch(context.Background())
			if err != nil || done != (attempt == 3) {
				t.Fatalf("attempt %d: %v %v", attempt, done, err)
			}
			db.Exec(`UPDATE dmarc_report_backfills SET next_attempt_at=NULL`)
		}
		var folder string
		var failed int
		db.QueryRow(`SELECT folder FROM received_emails WHERE uuid=$1`, id).Scan(&folder)
		db.QueryRow(`SELECT failed FROM dmarc_report_backfills`).Scan(&failed)
		if folder != "inbox" || failed != 1 || storage.gets != 3 {
			t.Fatalf("folder=%s failed=%d gets=%d", folder, failed, storage.gets)
		}
	})
	t.Run("ownership SQL error does not advance", func(t *testing.T) {
		db := testutil.Database(t)
		seedReceivedFixture(t, db)
		seedDMARCHistory(t, db, 1, "inbox")
		snapshotDMARCHistory(t, db)
		storage := &dmarcHookStorage{fakeIncomingStorage: fakeIncomingStorage{raw: dmarcTestRaw("one.test")}, hook: func() {
			if _, err := db.Exec(`ALTER TABLE domains RENAME TO domains_unavailable`); err != nil {
				t.Fatal(err)
			}
		}}
		s := &ReceivingService{db: db, storage: storage}
		if _, err := s.backfillDMARCReportsBatch(context.Background()); err == nil {
			t.Fatal("lookup failure swallowed")
		}
		var last int
		db.QueryRow(`SELECT last_id FROM dmarc_report_backfills`).Scan(&last)
		if last != 0 {
			t.Fatal("failed lookup advanced scan")
		}
		if _, err := db.Exec(`ALTER TABLE domains_unavailable RENAME TO domains`); err != nil {
			t.Fatal(err)
		}
		if done, err := s.backfillDMARCReportsBatch(context.Background()); err != nil || !done {
			t.Fatalf("retry failed %v %v", done, err)
		}
	})
	t.Run("bounded batch resumes new worker", func(t *testing.T) {
		db := testutil.Database(t)
		seedReceivedFixture(t, db)
		for i := 0; i < dmarcBackfillBatchSize+2; i++ {
			seedDMARCHistory(t, db, 1, "inbox")
		}
		snapshotDMARCHistory(t, db)
		storage := &fakeIncomingStorage{raw: dmarcTestRaw("one.test")}
		s := &ReceivingService{db: db, storage: storage}
		if done, err := s.backfillDMARCReportsBatch(context.Background()); err != nil || done || storage.gets != dmarcBackfillBatchSize {
			t.Fatalf("first batch %v %v %d", done, err, storage.gets)
		}
		s = &ReceivingService{db: db, storage: storage}
		if done, err := s.backfillDMARCReportsBatch(context.Background()); err != nil || !done || storage.gets != dmarcBackfillBatchSize+2 {
			t.Fatalf("resume %v %v %d", done, err, storage.gets)
		}
	})
}

func TestDMARCHistoryCancellationAndReplicaLock(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	seedDMARCHistory(t, db, 1, "inbox")
	snapshotDMARCHistory(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	storage := &dmarcHookStorage{fakeIncomingStorage: fakeIncomingStorage{raw: dmarcTestRaw("one.test")}, hook: cancel}
	s := &ReceivingService{db: db, storage: storage}
	if _, err := s.backfillDMARCReportsBatch(ctx); err != context.Canceled {
		t.Fatalf("cancelled scan: %v", err)
	}
	var last int
	if err := db.QueryRow(`SELECT last_id FROM dmarc_report_backfills`).Scan(&last); err != nil || last != 0 {
		t.Fatalf("cancel advanced %d: %v", last, err)
	}
	held, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = held.ExecContext(context.Background(), `SELECT pg_advisory_lock(hashtextextended(current_schema()||':mailat:dmarc-history:v1',0))`); err != nil {
		t.Fatal(err)
	}
	if done, err := s.backfillDMARCReportsBatch(context.Background()); err != nil || done || storage.gets != 1 {
		t.Fatalf("replica bypassed lock: %v %v %d", done, err, storage.gets)
	}
	if _, err = held.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtextextended(current_schema()||':mailat:dmarc-history:v1',0))`); err != nil {
		t.Fatal(err)
	}
	held.Close()
	if done, err := s.backfillDMARCReportsBatch(context.Background()); err != nil || !done || storage.gets != 2 {
		t.Fatalf("resume: %v %v %d", done, err, storage.gets)
	}
}
