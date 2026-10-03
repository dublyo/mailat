package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
)

type fakeIncomingStorage struct {
	raw                 []byte
	err                 error
	gets, puts, deletes int
}

func (f *fakeIncomingStorage) GetEmailFromS3(context.Context, string, string) ([]byte, error) {
	f.gets++
	return f.raw, f.err
}
func (f *fakeIncomingStorage) PutAttachment(context.Context, string, string, string, []byte) error {
	f.puts++
	return f.err
}
func (f *fakeIncomingStorage) DeleteObject(context.Context, string, string) error {
	f.deletes++
	return f.err
}
func (f *fakeIncomingStorage) GenerateDownloadURL(context.Context, string, string, string) (string, error) {
	return "https://private.example/download", f.err
}
func seedReceivedFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
 INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Test','test',NOW()),(2,'Other','other',NOW());
 INSERT INTO users(id,org_id,email,password_hash,updated_at) VALUES(1,1,'user1@example.test','unused',NOW()),(2,1,'user2@example.test','unused',NOW()),(3,2,'user3@example.test','unused',NOW());
 INSERT INTO domains(id,org_id,name,verification_token,status,receiving_enabled,updated_at) VALUES(1,1,'one.test','test','active',true,NOW()),(2,1,'two.test','test','active',true,NOW()),(3,2,'other.test','test','active',true,NOW());
 INSERT INTO identities(id,user_id,domain_id,email,is_catch_all,updated_at) VALUES(1,1,1,'a@one.test',false,NOW()),(2,2,1,'b@one.test',false,NOW()),(3,1,2,'catch@two.test',true,NOW()),(4,3,3,'other@other.test',true,NOW());
 INSERT INTO receiving_configs(org_id,s3_bucket,s3_region,sns_topic_arn,ses_rule_set_name,webhook_secret,status,updated_at) VALUES(1,'test-bucket','us-east-1','arn:aws:sns:us-east-1:123456789012:test','test','test-secret','active',NOW());
 `)
	if err != nil {
		t.Fatal(err)
	}
}
func TestReceivedLifecycleIntegration(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	ctx := context.Background()
	raw := []byte("From: Sender <sender@example.test>\r\nTo: a@one.test, b@one.test\r\nMessage-ID: <shared@sender.test>\r\nSubject: Shared delivery\r\nContent-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\nContent-Type: text/plain\r\n\r\nfilter body\r\n--x\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=data.bin\r\nContent-Transfer-Encoding: base64\r\n\r\nAQID\r\n--x--\r\n")
	storage := &fakeIncomingStorage{raw: raw}
	receive := &ReceivingService{db: db, storage: storage}
	inbox := &InboxService{db: db}
	auth, err := receive.AuthorizeNotification(ctx, "arn:aws:sns:us-east-1:123456789012:test", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = receive.AuthorizeNotification(ctx, auth.TopicARN, "wrong"); !errors.Is(err, ErrWebhookAuthorization) {
		t.Fatal("wrong secret accepted")
	}
	_, err = db.Exec(`INSERT INTO inbox_filters(org_id,user_id,name,conditions,action_star,action_trash,updated_at) VALUES(1,1,'body','[{"field":"body","operator":"contains","value":"filter body"}]',true,false,NOW()),(1,2,'other owner','[{"field":"subject","operator":"contains","value":"Shared"}]',false,true,NOW())`)
	if err != nil {
		t.Fatal(err)
	}
	n := &model.SESNotification{NotificationType: "Received"}
	n.Mail.MessageId = "ses-1"
	n.Receipt = &model.SESReceipt{Timestamp: time.Now().UTC().Format(time.RFC3339), Recipients: []string{"a@one.test", "b@one.test", "sales@two.test", "news@two.test", "other@other.test"}, Action: model.SESAction{Type: "S3", BucketName: auth.Bucket, ObjectKey: "incoming/one.test/ses-1"}}
	storage.err = errors.New("transient S3 failure")
	if err = receive.ProcessIncomingEmail(ctx, auth, n); err == nil {
		t.Fatal("transient failure acknowledged")
	}
	storage.err = nil
	if err = receive.ProcessIncomingEmail(ctx, auth, n); err != nil {
		t.Fatal(err)
	}
	if err = receive.ProcessIncomingEmail(ctx, auth, n); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM received_emails`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("recipient copies=%d err=%v", count, err)
	}
	yes := true
	list, err := inbox.ListReceivedEmails(ctx, 1, &model.InboxListRequest{Folder: "inbox", Search: "Shared", HasAttachments: &yes, PageSize: 50})
	if err != nil || list.Total != 2 {
		t.Fatalf("user1 list: %#v %v", list, err)
	}
	for _, e := range list.Emails {
		if !e.IsStarred || e.IsTrashed {
			t.Fatal("body/owner filters not applied correctly")
		}
	}
	if _, err = inbox.ListReceivedEmails(ctx, 1, &model.InboxListRequest{IdentityID: 2}); err == nil {
		t.Fatal("cross-user identity count allowed")
	}
	if _, err = inbox.GetReceivedEmailCounts(ctx, 1, 2); err == nil {
		t.Fatal("cross-user count allowed")
	}
	own := list.Emails[0].UUID
	other := ""
	db.QueryRow(`SELECT uuid FROM received_emails WHERE identity_id=2`).Scan(&other)
	if err = inbox.MarkReceivedEmails(ctx, 1, []string{own, other}, true); err == nil {
		t.Fatal("mixed owner bulk accepted")
	}
	if err = inbox.TrashReceivedEmails(ctx, 1, []string{own}, false); err != nil {
		t.Fatal(err)
	}
	if err = inbox.MoveReceivedEmails(ctx, 1, []string{own}, "inbox"); err != nil {
		t.Fatal(err)
	}
	email, err := inbox.GetReceivedEmail(ctx, 1, own)
	if err != nil || email.IsTrashed || email.Folder != "inbox" {
		t.Fatalf("restore: %#v %v", email, err)
	}
	if len(email.Attachments) != 1 || email.Attachments[0].DownloadURL == "" {
		t.Fatal("missing real attachment metadata")
	}
	if _, _, _, err = receive.AttachmentDownload(ctx, 2, own, email.Attachments[0].UUID); err == nil {
		t.Fatal("cross-user attachment accepted")
	}
	ids := []string{list.Emails[0].UUID, list.Emails[1].UUID}
	if err = inbox.TrashReceivedEmails(ctx, 1, ids, true); err != nil {
		t.Fatal(err)
	}
	if err = receive.cleanupStorage(ctx); err != nil {
		t.Fatal(err)
	}
	if storage.deletes != 0 {
		t.Fatal("deleted another recipient's shared object")
	}
	if err = inbox.TrashReceivedEmails(ctx, 2, []string{other}, true); err != nil {
		t.Fatal(err)
	}
	if err = receive.cleanupStorage(ctx); err != nil {
		t.Fatal(err)
	}
	if storage.deletes != 2 {
		t.Fatalf("expected raw+attachment cleanup, got %d", storage.deletes)
	}
	if err = receive.ProcessIncomingEmail(ctx, auth, n); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT COUNT(*) FROM received_emails`).Scan(&count)
	if count != 0 {
		t.Fatal("retry resurrected permanently deleted mail")
	}
}

func TestReceivedEmptyMessageIntegration(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	ctx := context.Background()
	storage := &fakeIncomingStorage{raw: []byte("From: sender@example.test\r\nTo: a@one.test\r\n\r\n")}
	svc := &ReceivingService{db: db, storage: storage}
	auth, err := svc.AuthorizeNotification(ctx, "arn:aws:sns:us-east-1:123456789012:test", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	n := &model.SESNotification{NotificationType: "Received", Mail: model.SESMail{MessageId: "ses-empty"}, Receipt: &model.SESReceipt{Recipients: []string{"a@one.test"}, Action: model.SESAction{Type: "S3", BucketName: auth.Bucket, ObjectKey: "incoming/one.test/ses-empty"}}}
	if err = svc.ProcessIncomingEmail(ctx, auth, n); err != nil {
		t.Fatal(err)
	}
	inbox := &InboxService{db: db}
	list, err := inbox.ListReceivedEmails(ctx, 1, &model.InboxListRequest{})
	if err != nil || list.Total != 1 {
		t.Fatalf("empty message not listed: %#v %v", list, err)
	}
	detail, err := inbox.GetReceivedEmail(ctx, 1, list.Emails[0].UUID)
	if err != nil || detail.Subject != "" || detail.TextBody != "" || detail.HTMLBody != "" || detail.MessageID != "<ses-empty@ses.invalid>" || detail.HasAttachments {
		t.Fatalf("empty message not safely stored: %#v %v", detail, err)
	}
}

func TestSESEventIsolationAndOrderingIntegration(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	ctx := context.Background()
	svc := &ReceivingService{db: db}
	auth := &ReceivingAuthorization{OrgID: 1, TopicARN: "arn:aws:sns:us-east-1:123456789012:test"}
	var own string
	err := db.QueryRow(`INSERT INTO received_emails(org_id,domain_id,identity_id,message_id,from_email,subject,direction,folder,send_status,updated_at) VALUES(1,1,1,'out-1','a@one.test','Outgoing','outbound','outbox','sending',NOW()) RETURNING uuid`).Scan(&own)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO received_emails(org_id,domain_id,identity_id,message_id,from_email,subject,direction,folder,send_status,ses_message_id,updated_at) VALUES(2,3,4,'out-other','other@other.test','Other','outbound','sent','sent','provider-out-1',NOW())`)
	if err != nil {
		t.Fatal(err)
	}
	if err = (&InboxService{db: db}).TrashReceivedEmails(ctx, 1, []string{own}, true); err == nil {
		t.Fatal("in-flight send record was deleted")
	}
	mail := model.SESMail{MessageId: "provider-out-1", Headers: []model.SESHeader{{Name: "X-Mailat-Message-ID", Value: own}}}
	delivery := &model.SESNotification{NotificationType: "Delivery", Mail: mail, Delivery: &model.SESDelivery{Recipients: []string{"recipient@example.test"}}}
	if err = svc.ProcessDeliveryEvent(ctx, auth, "sns-delivery", delivery); err != nil {
		t.Fatal(err)
	}
	var status, providerID string
	if err = db.QueryRow(`SELECT send_status,ses_message_id FROM received_emails WHERE uuid=$1`, own).Scan(&status, &providerID); err != nil || status != "delivered" || providerID != mail.MessageId {
		t.Fatalf("early correlation: %s %s %v", status, providerID, err)
	}
	bounce := &model.SESNotification{NotificationType: "Bounce", Mail: mail, Bounce: &model.SESBounce{BounceType: "Permanent", BouncedRecipients: []model.SESBouncedRecipient{{EmailAddress: "recipient@example.test"}}}}
	for attempt := 0; attempt < 2; attempt++ {
		if err = svc.ProcessDeliveryEvent(ctx, auth, "sns-bounce", bounce); err != nil {
			t.Fatal(err)
		}
	}
	if err = svc.ProcessDeliveryEvent(ctx, auth, "sns-late-delivery", delivery); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT send_status FROM received_emails WHERE uuid=$1`, own).Scan(&status); err != nil || status != "bounced" {
		t.Fatalf("terminal bounce regressed: %s %v", status, err)
	}
	if err = db.QueryRow(`SELECT send_status FROM received_emails WHERE org_id=2`).Scan(&status); err != nil || status != "sent" {
		t.Fatalf("other organization affected: %s %v", status, err)
	}
	for _, table := range []string{"suppression_list", "suppressions"} {
		var count int
		if err = db.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE org_id=1 AND email='recipient@example.test'`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("suppression %s count=%d err=%v", table, count, err)
		}
	}
}

func TestReceivedSeparateRuleNotificationsIntegration(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	ctx := context.Background()
	storage := &fakeIncomingStorage{raw: []byte("From: sender@example.test\r\nTo: a@one.test, sales@two.test\r\nSubject: Two rules\r\n\r\nbody")}
	svc := &ReceivingService{db: db, storage: storage}
	auth := &ReceivingAuthorization{OrgID: 1, TopicARN: "arn:aws:sns:us-east-1:123456789012:test", Bucket: "test-bucket", Region: "us-east-1"}
	// Real SES notifications contain only recipients matched by that rule.
	for _, domain := range []struct{ recipient, key string }{{"a@one.test", "incoming/one.test/ses-two-rules"}, {"sales@two.test", "incoming/two.test/ses-two-rules"}} {
		n := &model.SESNotification{NotificationType: "Received", Mail: model.SESMail{MessageId: "ses-two-rules"}, Receipt: &model.SESReceipt{Recipients: []string{domain.recipient}, Action: model.SESAction{Type: "S3", BucketName: auth.Bucket, ObjectKey: domain.key}}}
		if err := svc.ProcessIncomingEmail(ctx, auth, n); err != nil {
			t.Fatal(err)
		}
		if err := svc.ProcessIncomingEmail(ctx, auth, n); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM received_emails WHERE ses_message_id='ses-two-rules'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("separate domain copies=%d err=%v", count, err)
	}
	if storage.gets != 2 {
		t.Fatalf("duplicate notification fetched storage: gets=%d", storage.gets)
	}
	// Legacy separate-SNS action has no S3 key; scoped fallback must still work.
	legacy := &model.SESNotification{NotificationType: "Received", Mail: model.SESMail{MessageId: "ses-legacy-action"}, Receipt: &model.SESReceipt{Recipients: []string{"a@one.test"}, Action: model.SESAction{Type: "SNS", TopicArn: auth.TopicARN}}}
	if err := svc.ProcessIncomingEmail(ctx, auth, legacy); err != nil {
		t.Fatal(err)
	}
}

func TestReceivedLegacyDeleteTombstoneIntegration(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	ctx := context.Background()
	var id string
	err := db.QueryRow(`INSERT INTO received_emails(org_id,domain_id,identity_id,message_id,from_email,subject,ses_message_id,raw_s3_bucket,raw_s3_key,updated_at) VALUES(1,1,1,'legacy-rfc','sender@example.test','Legacy','legacy-ses','test-bucket','incoming/one.test/legacy-ses',NOW()) RETURNING uuid`).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	if err = (&InboxService{db: db}).TrashReceivedEmails(ctx, 1, []string{id}, true); err != nil {
		t.Fatal(err)
	}
	storage := &fakeIncomingStorage{raw: []byte("From: sender@example.test\r\n\r\nbody")}
	svc := &ReceivingService{db: db, storage: storage}
	auth := &ReceivingAuthorization{OrgID: 1, TopicARN: "arn:aws:sns:us-east-1:123456789012:test", Bucket: "test-bucket"}
	n := &model.SESNotification{NotificationType: "Received", Mail: model.SESMail{MessageId: "legacy-ses"}, Receipt: &model.SESReceipt{Recipients: []string{"a@one.test"}, Action: model.SESAction{Type: "SNS", TopicArn: auth.TopicARN}}}
	if err = svc.ProcessIncomingEmail(ctx, auth, n); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM received_emails`).Scan(&count); err != nil || count != 0 || storage.gets != 0 {
		t.Fatalf("legacy replay resurrected/fetched mail count=%d gets=%d err=%v", count, storage.gets, err)
	}
}

func TestReceivedMalformedMIMEFallbackIntegration(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	ctx := context.Background()
	storage := &fakeIncomingStorage{raw: []byte("From: sender@example.test\r\nContent-Type: multipart/mixed\r\n\r\nbroken boundary")}
	svc := &ReceivingService{db: db, storage: storage}
	auth := &ReceivingAuthorization{OrgID: 1, TopicARN: "arn:aws:sns:us-east-1:123456789012:test", Bucket: "test-bucket"}
	n := &model.SESNotification{NotificationType: "Received", Mail: model.SESMail{MessageId: "malformed-ses", Source: "sender@example.test"}, Receipt: &model.SESReceipt{Recipients: []string{"a@one.test"}, Action: model.SESAction{Type: "SNS", TopicArn: auth.TopicARN}}}
	if err := svc.ProcessIncomingEmail(ctx, auth, n); err != nil {
		t.Fatal(err)
	}
	if err := svc.ProcessIncomingEmail(ctx, auth, n); err != nil {
		t.Fatal(err)
	}
	var body, key, name string
	err := db.QueryRow(`SELECT e.text_body,e.raw_s3_key,a.filename FROM received_emails e JOIN email_attachments a ON a.received_email_id=e.id WHERE e.ses_message_id='malformed-ses'`).Scan(&body, &key, &name)
	if err != nil || !strings.Contains(body, "Parse warning:") || key != "incoming/one.test/malformed-ses" || name != "original-message.eml" || storage.gets != 1 {
		t.Fatalf("malformed fallback missing body=%q key=%q name=%q gets=%d err=%v", body, key, name, storage.gets, err)
	}
}

func TestSESLateBounceAfterSentDeleteIntegration(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	ctx := context.Background()
	var id string
	err := db.QueryRow(`INSERT INTO received_emails(org_id,domain_id,identity_id,message_id,from_email,subject,direction,folder,send_status,ses_message_id,submitter_user_id,submission_key,updated_at) VALUES(1,1,1,'late-rfc','a@one.test','Sent','outbound','sent','sent','late-provider',1,'late-key',NOW()) RETURNING uuid`).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO compose_submission_keys(user_id,submission_key,request_hash,email_uuid,message_id,status,ses_message_id) VALUES(1,'late-key','test',$1,'late-rfc','sent','late-provider')`, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = (&InboxService{db: db}).TrashReceivedEmails(ctx, 1, []string{id}, true); err != nil {
		t.Fatal(err)
	}
	svc := &ReceivingService{db: db}
	auth := &ReceivingAuthorization{OrgID: 1, TopicARN: "arn:aws:sns:us-east-1:123456789012:test"}
	n := &model.SESNotification{NotificationType: "Bounce", Mail: model.SESMail{MessageId: "late-provider"}, Bounce: &model.SESBounce{BounceType: "Permanent", BouncedRecipients: []model.SESBouncedRecipient{{EmailAddress: "bounce@example.test"}}}}
	if err = svc.ProcessDeliveryEvent(ctx, auth, "late-bounce", n); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = db.QueryRow(`SELECT status FROM compose_submission_keys WHERE email_uuid=$1`, id).Scan(&status); err != nil || status != "bounced" {
		t.Fatalf("deleted send receipt status=%s err=%v", status, err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM suppression_list WHERE org_id=1 AND email='bounce@example.test'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("late bounce suppression=%d err=%v", count, err)
	}
	wrong := &ReceivingAuthorization{OrgID: 2, TopicARN: "arn:aws:sns:us-east-1:123456789012:other"}
	if err = svc.ProcessDeliveryEvent(ctx, wrong, "wrong-org", n); err == nil {
		t.Fatal("receipt accepted under another organization")
	}
}
