package service

import (
	"context"
	"fmt"
	"net/mail"
	"os"
	"testing"

	"github.com/dublyo/mailat/api/internal/testutil"
)

func reportMail(from, subject, contentType string, attachments ...AttachmentInfo) *parsedIncoming {
	h := mail.Header{"From": {from}, "Subject": {subject}}
	if contentType != "" {
		h["Content-Type"] = []string{contentType}
	}
	return &parsedIncoming{Header: h, Attachments: attachments}
}

func TestDeliveryStatusReport(t *testing.T) {
	cases := []struct {
		name string
		mail *parsedIncoming
		want bool
	}{
		{"SES bounce", reportMail("MAILER-DAEMON@us-east-2.amazonses.com", "Delivery Status Notification (Failure)", `multipart/report; report-type=delivery-status; boundary="b"`), true},
		{"RFC 3464 from any sender", reportMail("bounces@mx.example.net", "Your message", `multipart/report; report-type="delivery-status"; boundary=b`), true},
		{"Postfix bounce without report type", reportMail("MAILER-DAEMON (Mail Delivery System) <MAILER-DAEMON@mx.example.net>", "Undelivered Mail Returned to Sender", "text/plain"), true},
		{"Exchange postmaster", reportMail("postmaster@outlook.com", "Undeliverable: Hello", "text/plain"), true},
		{"Encoded subject", reportMail("mailer-daemon@googlemail.com", "=?UTF-8?Q?Delivery_Status_Notification_(Delay)?=", ""), true},
		{"Postmaster writing a person", reportMail("postmaster@example.net", "Welcome to the team", "text/plain"), false},
		{"Person quoting a bounce subject", reportMail("alice@example.net", "Delivery Status Notification (Failure)", "text/plain"), false},
		{"Forwarded bounce stays in Inbox", reportMail("alice@example.net", "Fwd: Delivery Status Notification (Failure)", `multipart/report; report-type=delivery-status; boundary=b`), false},
		{"Feedback report is not a bounce", reportMail("abuse@example.net", "Abuse report", `multipart/report; report-type=feedback-report; boundary=b`), false},
		{"Nil", nil, false},
	}
	for _, c := range cases {
		if got := deliveryStatusReport(c.mail); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// Migration 020 files existing bounce notices under Reports, but leaves real
// mail, unverified report lookalikes, replies, spam, archived mail and users
// who turned automatic filing off untouched.
func TestMigration020FilesExistingReports(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	if _, err := db.Exec(`INSERT INTO user_settings(org_id,user_id,auto_organize_dmarc_reports,updated_at) VALUES(1,2,false,NOW())`); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		id, identity             int
		from, subject, inReplyTo string
		attach, archived, spam   bool
		want                     string
	}{
		{1, 1, "MAILER-DAEMON@us-east-2.amazonses.com", "Delivery Status Notification (Failure)", "", true, false, false, "dmarc-reports"},
		{2, 1, "postmaster@outlook.com", "Undeliverable: hello", "", false, false, false, "dmarc-reports"},
		{3, 1, "dmarcreport@microsoft.com", "Report Domain: one.test Submitter: protection.outlook.com", "", true, false, false, "inbox"},
		{4, 1, "dmarcreport@microsoft.com", "Report Domain: one.test Submitter: protection.outlook.com", "", false, false, false, "inbox"},
		{5, 1, "alice@example.test", "Delivery Status Notification (Failure)", "", false, false, false, "inbox"},
		{6, 1, "MAILER-DAEMON@mx.example.test", "Fwd: Delivery Status Notification", "", false, false, false, "inbox"},
		{7, 1, "alice@example.test", "Report domain: one.test", "<a@b>", true, false, false, "inbox"},
		{8, 1, "MAILER-DAEMON@mx.example.test", "Returned mail: see transcript", "", false, true, false, "inbox"},
		{9, 1, "MAILER-DAEMON@mx.example.test", "Returned mail: see transcript", "", false, false, true, "inbox"},
		{10, 2, "MAILER-DAEMON@us-east-2.amazonses.com", "Delivery Status Notification (Failure)", "", true, false, false, "inbox"},
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO received_emails(id,org_id,domain_id,identity_id,message_id,from_email,to_emails,subject,in_reply_to,has_attachments,is_archived,is_spam,folder,direction,received_at,updated_at)
		 VALUES($1,1,1,$2,$3,$4,ARRAY['a@one.test'],$5,$6,$7,$8,$9,'inbox','inbound',NOW(),NOW())`, r.id, r.identity, fmt.Sprintf("m020-%d", r.id), r.from, r.subject, r.inReplyTo, r.attach, r.archived, r.spam); err != nil {
			t.Fatal(err)
		}
	}
	migration, err := os.ReadFile("../database/migrations/020_file_existing_reports.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		var folder string
		if err := db.QueryRow(`SELECT folder FROM received_emails WHERE id=$1`, r.id).Scan(&folder); err != nil {
			t.Fatal(err)
		}
		if folder != r.want {
			t.Errorf("row %d (%s / %q): folder %s, want %s", r.id, r.from, r.subject, folder, r.want)
		}
	}
}

// An SES bounce notice arriving at the sender's address goes to Reports, not
// the Inbox; a person's mail with the same subject still lands in the Inbox.
func TestBounceNoticeArrivalFiledUnderReports(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	dsn := []byte("From: MAILER-DAEMON@us-east-2.amazonses.com\r\nTo: a@one.test\r\nSubject: Delivery Status Notification (Failure)\r\nAuto-Submitted: auto-replied\r\nContent-Type: multipart/report; report-type=delivery-status; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nAn error occurred while trying to deliver the mail to the following recipients: x@demo.example.test\r\n--b\r\nContent-Type: message/delivery-status\r\n\r\nReporting-MTA: dns; amazonses.com\r\n\r\nFinal-Recipient: rfc822; x@demo.example.test\r\nAction: failed\r\nStatus: 5.1.2\r\n--b--\r\n")
	person := []byte("From: Alice <alice@example.test>\r\nTo: a@one.test\r\nSubject: Delivery Status Notification (Failure)\r\nContent-Type: text/plain\r\n\r\nWhy did my mail bounce?\r\n")
	for i, c := range []struct {
		raw  []byte
		want string
	}{{dsn, "dmarc-reports"}, {person, "inbox"}} {
		s, a := dmarcReceiving(t, db, &fakeIncomingStorage{raw: c.raw})
		id := fmt.Sprintf("bounce-arrival-%d", i)
		if err := s.ProcessIncomingEmail(context.Background(), a, dmarcNotification(id, "a@one.test")); err != nil {
			t.Fatal(err)
		}
		var folder string
		if err := db.QueryRow(`SELECT folder FROM received_emails WHERE ses_message_id=$1`, id).Scan(&folder); err != nil || folder != c.want {
			t.Fatalf("message %d folder=%s want %s err=%v", i, folder, c.want, err)
		}
	}
}
