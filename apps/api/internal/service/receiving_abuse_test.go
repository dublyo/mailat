package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/model"
)

// ingestRaw delivers one SES notification carrying raw for the given envelope
// recipients of arrival.test.
func (f *arrivalFixture) ingestRaw(t *testing.T, sesID, raw string, recipients []string, receipt func(*model.SESNotification)) {
	t.Helper()
	f.storage.raw = []byte(raw)
	n := &model.SESNotification{NotificationType: "Received"}
	n.Mail.MessageId, n.Mail.Source = sesID, "bounce@mail.sender.test"
	pass := model.SESVerdict{Status: "PASS"}
	n.Receipt = &model.SESReceipt{Timestamp: time.Now().UTC().Format(time.RFC3339), Recipients: recipients,
		SpamVerdict: pass, VirusVerdict: pass, SPFVerdict: pass, DKIMVerdict: pass, DMARCVerdict: pass,
		Action: model.SESAction{Type: "S3", BucketName: f.auth.Bucket, ObjectKey: "incoming/arrival.test/" + sesID}}
	if receipt != nil {
		receipt(n)
	}
	if err := f.receive.ProcessIncomingEmail(context.Background(), f.auth, n); err != nil {
		t.Fatal(err)
	}
}

// runDueCleanup makes every queued cleanup job due and runs the worker.
func (f *arrivalFixture) runDueCleanup(t *testing.T) {
	t.Helper()
	mustExec(t, f.db, `UPDATE storage_cleanup_jobs SET next_attempt_at=NOW()`)
	if err := f.receive.cleanupStorage(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func spoofRaw(sesID, from string) string {
	return "From: " + from + "\r\nTo: owner@arrival.test\r\nMessage-ID: <" + sesID + "@sender.test>\r\nSubject: Hi " + sesID + "\r\n\r\nHello\r\n"
}

func TestInboundFromSpoofIntegration(t *testing.T) {
	f := newArrivalFixture(t)
	f.createRule(t, CreateAutoReplyInput{Subject: "Away"})
	owner := []string{"owner@arrival.test"}
	stored := func(sesID string) (from, name string) {
		t.Helper()
		if err := f.db.QueryRow(`SELECT COALESCE(from_email,''),COALESCE(from_name,'') FROM received_emails WHERE ses_message_id=$1`, sesID).Scan(&from, &name); err != nil {
			t.Fatal(err)
		}
		return
	}
	replies := func(sesID string) (n int, payload string) {
		t.Helper()
		n = f.count(t, `SELECT COUNT(*) FROM mail_arrival_jobs WHERE kind='auto_reply' AND ses_message_id=$1`, sesID)
		if n > 0 {
			if err := f.db.QueryRow(`SELECT payload::text FROM mail_arrival_jobs WHERE kind='auto_reply' AND ses_message_id=$1`, sesID).Scan(&payload); err != nil {
				t.Fatal(err)
			}
		}
		return
	}

	// An address in the quoted display name is only a name: the mailbox, the
	// filters and the auto-reply all use the real (DMARC-checked) address.
	f.ingestRaw(t, "quoted", spoofRaw("quoted", `"<victim@third.example>" <attacker@evil.example>`), owner, nil)
	if from, name := stored("quoted"); from != "attacker@evil.example" || name != "<victim@third.example>" {
		t.Fatalf("stored from=%q name=%q", from, name)
	}
	if n, payload := replies("quoted"); n != 1 || strings.Contains(payload, "victim@") || !strings.Contains(payload, `"sender": "attacker@evil.example"`) {
		t.Fatalf("auto-reply jobs=%d payload=%s", n, payload)
	}

	// Ambiguous or unparseable From headers store no address and get no reply.
	for sesID, from := range map[string]string{
		"unquoted": "<victim@third.example> <attacker@evil.example>",
		"unclosed": "Victim <victim@third.example",
	} {
		f.ingestRaw(t, sesID, spoofRaw(sesID, from), owner, nil)
		if got, _ := stored(sesID); got != "" {
			t.Fatalf("%s stored from=%q", sesID, got)
		}
		if n, _ := replies(sesID); n != 0 {
			t.Fatalf("%s auto-replied", sesID)
		}
	}
	// Several From mailboxes: the first is shown, but nobody is answered.
	f.ingestRaw(t, "multi", spoofRaw("multi", "victim@third.example, attacker@evil.example"), owner, nil)
	if got, _ := stored("multi"); got != "victim@third.example" {
		t.Fatalf("multi stored from=%q", got)
	}
	if n, _ := replies("multi"); n != 0 {
		t.Fatal("auto-replied to a multi-address From")
	}
	// The From that SES evaluated DMARC on must be the one answered.
	f.ingestRaw(t, "provider", spoofRaw("provider", "Alice <alice@sender.test>"), owner, func(n *model.SESNotification) {
		n.Mail.CommonHeaders.From = []string{"Mallory <mallory@evil.example>"}
	})
	if n, _ := replies("provider"); n != 0 {
		t.Fatal("auto-replied although SES saw a different From")
	}
	f.ingestRaw(t, "agrees", spoofRaw("agrees", "=?utf-8?q?Al=C3=AFce?= <alice@sender.test>"), owner, func(n *model.SESNotification) {
		n.Mail.CommonHeaders.From = []string{"Alïce <alice@sender.test>"}
	})
	if from, name := stored("agrees"); from != "alice@sender.test" || name != "Alïce" {
		t.Fatalf("encoded name stored from=%q name=%q", from, name)
	}
	if n, _ := replies("agrees"); n != 1 {
		t.Fatal("no auto-reply for a genuine sender")
	}
	// An encoded word inside a quoted display name is decoded too.
	f.ingestRaw(t, "quoted-encoded", spoofRaw("quoted-encoded", `"=?UTF-8?B?2YXYrdmF2K8g2LnZhNmK?=" <alice@sender.test>`), owner, nil)
	if from, name := stored("quoted-encoded"); from != "alice@sender.test" || name != "محمد علي" {
		t.Fatalf("quoted encoded name stored from=%q name=%q", from, name)
	}
	// Carrier addresses net/mail rejects keep their address for reply, filters
	// and block rules, but are never answered automatically.
	for sesID, want := range map[string]string{
		"docomo-bare":  "foo..bar@docomo.ne.jp",
		"docomo-angle": "<foo..bar@docomo.ne.jp>",
		"ezweb-named":  "Taro <foo.@ezweb.ne.jp>",
	} {
		f.ingestRaw(t, sesID, spoofRaw(sesID, want), owner, nil)
		got, _ := stored(sesID)
		if !strings.Contains(want, got) || got == "" || strings.ContainsAny(got, "<> ") {
			t.Fatalf("%s stored from=%q", sesID, got)
		}
		if n, _ := replies(sesID); n != 0 {
			t.Fatalf("%s auto-replied to a lenient From", sesID)
		}
	}
	// Reply-To is parsed the same way.
	f.ingestRaw(t, "replyto", "From: alice@sender.test\r\nReply-To: \"<victim@third.example>\" <alice@sender.test>\r\nTo: owner@arrival.test\r\n\r\nHi\r\n", owner, nil)
	var replyTo string
	if err := f.db.QueryRow(`SELECT reply_to FROM received_emails WHERE ses_message_id='replyto'`).Scan(&replyTo); err != nil || replyTo != "alice@sender.test" {
		t.Fatalf("reply_to=%q err=%v", replyTo, err)
	}
}

func TestForwardSkipsUnauthenticatedMail(t *testing.T) {
	f := newForwardFixture(t)
	uuid, token := f.create(t, "dest@outside.test", true)
	if err := f.replies.VerifyEmailForward(context.Background(), uuid, token); err != nil {
		t.Fatal(err)
	}
	f.deliverWith(t, "dmarc-fail", "alice@sender.test", func(r *model.SESReceipt) { r.DMARCVerdict.Status = "FAIL" })
	f.deliverWith(t, "spf-dkim-fail", "alice@sender.test", func(r *model.SESReceipt) {
		r.DMARCVerdict.Status, r.SPFVerdict.Status, r.DKIMVerdict.Status = "GRAY", "FAIL", "FAIL"
	})
	f.deliverWith(t, "spf-fail-only", "alice@sender.test", func(r *model.SESReceipt) { r.SPFVerdict.Status = "FAIL" })
	f.runOnce(t)
	for ses, want := range map[string]string{"dmarc-fail": "skipped:unauthenticated", "spf-dkim-fail": "skipped:unauthenticated", "spf-fail-only": "done"} {
		if _, result := f.job(t, ses, "forward"); result != want {
			t.Errorf("%s: %s, want %s", ses, result, want)
		}
		// The local copy is always kept.
		if n := f.count(t, `SELECT COUNT(*) FROM received_emails WHERE ses_message_id=$1 AND folder='inbox'`, ses); n != 1 {
			t.Errorf("%s: %d local copies", ses, n)
		}
	}
	if len(f.dispatched) != 1 {
		t.Fatalf("forwarded %d messages, want only the authenticated one", len(f.dispatched))
	}
	// A skipped forward does not suspend the rule.
	if status, _, _ := f.forward(t, uuid); status != "active" {
		t.Fatal("forward state changed:", status)
	}
}

func TestUnknownRecipientObjectCleanup(t *testing.T) {
	f := newArrivalFixture(t)
	raw := "From: alice@sender.test\r\nTo: x@arrival.test\r\nSubject: hi\r\n\r\nHello\r\n"
	jobs := func(key string) int {
		return f.count(t, `SELECT COUNT(*) FROM storage_cleanup_jobs WHERE bucket=$1 AND object_key=$2`, f.auth.Bucket, key)
	}

	// No recipient has an identity: acknowledged, nothing stored, raw object
	// queued with a delay and deleted by the worker.
	f.ingestRaw(t, "unknown-1", raw, []string{"random@arrival.test", "Other <nobody@arrival.test>"}, nil)
	if n := f.count(t, `SELECT COUNT(*) FROM received_emails`); n != 0 {
		t.Fatalf("stored %d copies", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM storage_cleanup_jobs WHERE object_key='incoming/arrival.test/unknown-1' AND next_attempt_at>NOW()+INTERVAL '50 minutes'`); n != 1 {
		t.Fatal("unknown recipient object not queued with a delay")
	}
	if err := f.receive.cleanupStorage(context.Background()); err != nil || f.storage.deletes != 0 {
		t.Fatalf("deleted before the delay: %d %v", f.storage.deletes, err)
	}
	f.runDueCleanup(t)
	if len(f.storage.deleted) != 1 || f.storage.deleted[0] != "incoming/arrival.test/unknown-1" {
		t.Fatalf("deleted %v", f.storage.deleted)
	}

	// One known recipient keeps the object for everyone.
	f.ingestRaw(t, "mixed-1", raw, []string{"random@arrival.test", "owner@arrival.test"}, nil)
	if jobs("incoming/arrival.test/mixed-1") != 0 || f.count(t, `SELECT COUNT(*) FROM received_emails WHERE ses_message_id='mixed-1'`) != 1 {
		t.Fatal("mixed delivery queued its object or lost its copy")
	}
	// An SNS retry of a delivered message (every identity already has its
	// copy) must not queue the object its copies reference.
	f.ingestRaw(t, "mixed-1", raw, []string{"random@arrival.test", "owner@arrival.test"}, nil)
	if jobs("incoming/arrival.test/mixed-1") != 0 {
		t.Fatal("redelivery queued a referenced object")
	}

	// A second notification for the same object that does deliver wins: the
	// worker rechecks references and never deletes a referenced object.
	f.storage.deleted = nil
	f.ingestRaw(t, "shared-1", raw, []string{"random@arrival.test"}, nil)
	f.ingestRaw(t, "shared-1", raw, []string{"owner@arrival.test"}, nil)
	if jobs("incoming/arrival.test/shared-1") != 1 {
		t.Fatal("unknown recipient notification not queued")
	}
	f.runDueCleanup(t)
	if len(f.storage.deleted) != 0 {
		t.Fatalf("deleted a referenced object: %v", f.storage.deleted)
	}

	// Objects outside the org's active receiving domains or bucket stay.
	mustExec(t, f.db, `INSERT INTO domains(org_id,name,verification_token,status,receiving_enabled,updated_at) VALUES($1,'paused.test','t','active',false,now())`, f.org)
	f.ingestRaw(t, "paused-1", raw, []string{"x@paused.test"}, func(n *model.SESNotification) { n.Receipt.Action.ObjectKey = "incoming/paused.test/paused-1" })
	f.ingestRaw(t, "foreign-1", raw, []string{"x@foreign.test"}, func(n *model.SESNotification) { n.Receipt.Action.ObjectKey = "incoming/foreign.test/foreign-1" })
	f.ingestRaw(t, "bucket-1", raw, []string{"x@arrival.test"}, func(n *model.SESNotification) { n.Receipt.Action.BucketName = "someone-else" })
	f.ingestRaw(t, "key-1", raw, []string{"x@arrival.test"}, func(n *model.SESNotification) { n.Receipt.Action.ObjectKey = "attachments/1/key-1/0-abc" })
	for _, key := range []string{"incoming/paused.test/paused-1", "incoming/foreign.test/foreign-1", "incoming/arrival.test/bucket-1", "incoming/arrival.test/key-1", "attachments/1/key-1/0-abc"} {
		if jobs(key) != 0 {
			t.Errorf("%s queued", key)
		}
	}
}

func TestVirusVerdictNotDelivered(t *testing.T) {
	f := newArrivalFixture(t)
	f.createRule(t, CreateAutoReplyInput{Subject: "Away"})
	raw := "From: alice@sender.test\r\nTo: owner@arrival.test\r\nSubject: invoice\r\nContent-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n--x\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=a.exe\r\nContent-Transfer-Encoding: base64\r\n\r\nAQID\r\n--x--\r\n"
	f.ingestRaw(t, "virus-1", raw, []string{"owner@arrival.test"}, func(n *model.SESNotification) { n.Receipt.VirusVerdict.Status = "FAIL" })
	if n := f.count(t, `SELECT COUNT(*) FROM received_emails`) + f.count(t, `SELECT COUNT(*) FROM mail_arrival_jobs`) + f.count(t, `SELECT COUNT(*) FROM webhook_events`); n != 0 {
		t.Fatalf("virus mail left %d rows", n)
	}
	if f.storage.puts != 0 {
		t.Fatal("virus attachment stored")
	}
	f.runDueCleanup(t)
	if len(f.storage.deleted) != 1 || f.storage.deleted[0] != "incoming/arrival.test/virus-1" {
		t.Fatalf("deleted %v", f.storage.deleted)
	}

	// A spam verdict is unchanged: delivered to Spam, object kept.
	f.ingestRaw(t, "spam-1", raw, []string{"owner@arrival.test"}, func(n *model.SESNotification) { n.Receipt.SpamVerdict.Status = "FAIL" })
	if n := f.count(t, `SELECT COUNT(*) FROM received_emails WHERE ses_message_id='spam-1' AND folder='spam' AND is_spam`); n != 1 {
		t.Fatal("spam not delivered to Spam")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM storage_cleanup_jobs`); n != 0 {
		t.Fatal("spam object queued for deletion")
	}
}
