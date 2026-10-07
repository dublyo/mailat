package service

import (
	"context"
	"errors"
	"mime"
	"net/mail"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/worker"
)

func TestBuildForwardMessage(t *testing.T) {
	msg := BuildForwardMessage(ForwardSource{FromName: "Ana \"Q\" Pérez\r\n", FromEmail: "ana@sender.test", ReplyTo: "help@sender.test",
		IdentityEmail: "me@own.test", LoopTokens: []string{"aaaa1111", "bad token!", "BBBB2222"}, Tag: "cccc3333"})
	if msg.FromName != "Ana Q Pérez via Mailat" || msg.ReplyTo != "help@sender.test" {
		t.Fatalf("%+v", msg)
	}
	if msg.Headers["X-Mailat-Loop"] != "aaaa1111, bbbb2222, cccc3333" || msg.Headers["X-Mailat-Forwarded-For"] != "me@own.test" {
		t.Fatalf("%v", msg.Headers)
	}
	// The display name survives Q-encoding in a From header.
	from := (&mail.Address{Name: msg.FromName, Address: "me@own.test"}).String()
	if decoded, err := new(mime.WordDecoder).DecodeHeader(from); err != nil || !strings.Contains(decoded, "Pérez via Mailat") {
		t.Fatal(from, decoded, err)
	}

	long := BuildForwardMessage(ForwardSource{FromName: strings.Repeat("é", 100), FromEmail: "x@sender.test", ReplyTo: "not an address", Tag: "t"})
	if n := len([]rune(long.FromName)); n != forwardDisplayNameMax || !strings.HasSuffix(long.FromName, "… via Mailat") {
		t.Fatalf("%d %q", n, long.FromName)
	}
	if long.ReplyTo != "x@sender.test" {
		t.Fatal("invalid Reply-To must fall back to From", long.ReplyTo)
	}
	if bare := BuildForwardMessage(ForwardSource{FromEmail: "x@sender.test", Tag: "t"}); bare.FromName != "x@sender.test via Mailat" {
		t.Fatal(bare.FromName)
	}
	if got := forwardLoopTag("org", "fw"); len(got) != 16 || got != forwardLoopTag("org", "fw") || got == forwardLoopTag("org", "fw2") {
		t.Fatal(got)
	}
}

type forwardFixture struct {
	*arrivalFixture
	identityUUID string
	verifyMail   []*worker.EmailSendPayload
}

func newForwardFixture(t *testing.T) *forwardFixture {
	f := &forwardFixture{arrivalFixture: newArrivalFixture(t)}
	f.replies.SetSender(f.runner.tx)
	f.replies.dispatch = func(p *worker.EmailSendPayload) { f.verifyMail = append(f.verifyMail, p) }
	f.replies.cfg.WebUrl = "https://mail.example.test/"
	if err := f.db.QueryRow(`SELECT uuid::text FROM identities WHERE id=$1`, f.identity).Scan(&f.identityUUID); err != nil {
		t.Fatal(err)
	}
	return f
}

var forwardLink = regexp.MustCompile(`/forwards/verify#id=([0-9a-f-]+)&token=([A-Za-z0-9_-]+)`)

// create makes a forward and returns its uuid and the token from the mailed link.
func (f *forwardFixture) create(t *testing.T, dest string, keepCopy bool) (string, string) {
	t.Helper()
	fw, err := f.replies.CreateEmailForward(context.Background(), f.user, f.org, &CreateEmailForwardInput{IdentityUUID: f.identityUUID, ForwardTo: dest, KeepCopy: keepCopy})
	if err != nil {
		t.Fatal(err)
	}
	m := forwardLink.FindStringSubmatch(f.verifyMail[len(f.verifyMail)-1].TextBody)
	if m == nil || m[1] != fw.UUID {
		t.Fatalf("verification link missing: %q", f.verifyMail[len(f.verifyMail)-1].TextBody)
	}
	return fw.UUID, m[2]
}

func (f *forwardFixture) forward(t *testing.T, uuid string) (status string, lastError string, count int) {
	t.Helper()
	var le *string
	if err := f.db.QueryRow(`SELECT status,last_error,forward_count FROM email_forwards WHERE uuid=$1`, uuid).Scan(&status, &le, &count); err != nil {
		t.Fatal(err)
	}
	if le != nil {
		lastError = *le
	}
	return
}

func (f *forwardFixture) deliverRaw(t *testing.T, sesID, raw string) {
	t.Helper()
	f.storage.raw = []byte(raw)
	n := &model.SESNotification{NotificationType: "Received"}
	n.Mail.MessageId, n.Mail.Source = sesID, "bounce@mail.sender.test"
	pass := model.SESVerdict{Status: "PASS"}
	n.Receipt = &model.SESReceipt{Timestamp: time.Now().UTC().Format(time.RFC3339), Recipients: []string{"owner@arrival.test"},
		SpamVerdict: pass, VirusVerdict: pass, SPFVerdict: pass, DKIMVerdict: pass, DMARCVerdict: pass,
		Action: model.SESAction{Type: "S3", BucketName: f.auth.Bucket, ObjectKey: "incoming/arrival.test/" + sesID}}
	if err := f.receive.ProcessIncomingEmail(context.Background(), f.auth, n); err != nil {
		t.Fatal(err)
	}
}

func TestForwardCreateAndVerify(t *testing.T) {
	f := newForwardFixture(t)
	ctx := context.Background()

	uuid, token := f.create(t, " Dest@Outside.TEST ", true)
	var dest, hash, kind, ref, to string
	var legacyToken *string
	if err := f.db.QueryRow(`SELECT forward_to,verify_token_hash,verify_token FROM email_forwards WHERE uuid=$1`, uuid).Scan(&dest, &hash, &legacyToken); err != nil {
		t.Fatal(err)
	}
	if dest != "dest@outside.test" || hash != forwardTokenHash(token) || strings.Contains(hash, token) || legacyToken != nil {
		t.Fatalf("stored %q %q %v", dest, hash, legacyToken)
	}
	if err := f.db.QueryRow(`SELECT system_kind,system_ref::text,to_addresses FROM transactional_emails ORDER BY id DESC LIMIT 1`).Scan(&kind, &ref, &to); err != nil {
		t.Fatal(err)
	}
	if kind != "forward_verify" || ref != uuid || to != "<dest@outside.test>" || !strings.HasPrefix(f.verifyMail[0].TextBody, "owner@arrival.test asked") {
		t.Fatal(kind, ref, to)
	}

	var invalid *ForwardValidationError
	for name, in := range map[string]CreateEmailForwardInput{
		"internal domain":  {IdentityUUID: f.identityUUID, ForwardTo: "someone@arrival.test"},
		"identity address": {IdentityUUID: f.identityUUID, ForwardTo: "owner@arrival.test"},
		"not an address":   {IdentityUUID: f.identityUUID, ForwardTo: "Name <x@outside.test>"},
		"unknown identity": {IdentityUUID: "00000000-0000-0000-0000-000000000000", ForwardTo: "x@outside.test"},
	} {
		if _, err := f.replies.CreateEmailForward(ctx, f.user, f.org, &in); !errors.As(err, &invalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	mustExec(t, f.db, `INSERT INTO suppression_list(org_id,email,reason,source) VALUES($1,'blocked@outside.test','bounce','bounce')`, f.org)
	if _, err := f.replies.CreateEmailForward(ctx, f.user, f.org, &CreateEmailForwardInput{IdentityUUID: f.identityUUID, ForwardTo: "blocked@outside.test"}); !errors.As(err, &invalid) {
		t.Fatal("suppressed destination accepted", err)
	}
	var stranger int64
	if err := f.db.QueryRow(`INSERT INTO users(org_id,email,password_hash,updated_at) VALUES($1,'stranger@arrival.test','x',now()) RETURNING id`, f.org).Scan(&stranger); err != nil {
		t.Fatal(err)
	}
	if _, err := f.replies.CreateEmailForward(ctx, stranger, f.org, &CreateEmailForwardInput{IdentityUUID: f.identityUUID, ForwardTo: "x@outside.test"}); !errors.As(err, &invalid) {
		t.Fatal("another user's identity accepted", err)
	}
	if _, err := f.replies.CreateEmailForward(ctx, f.user, f.org, &CreateEmailForwardInput{IdentityUUID: f.identityUUID, ForwardTo: "dest@outside.test"}); !errors.Is(err, ErrForwardConflict) {
		t.Fatal("duplicate accepted", err)
	}
	for i := 2; i <= forwardMaxPerIdentity; i++ {
		f.create(t, "extra"+string(rune('0'+i))+"@outside.test", true)
	}
	if _, err := f.replies.CreateEmailForward(ctx, f.user, f.org, &CreateEmailForwardInput{IdentityUUID: f.identityUUID, ForwardTo: "sixth@outside.test"}); !errors.Is(err, ErrForwardConflict) {
		t.Fatal("sixth forward accepted", err)
	}

	// Wrong tokens count; the right one activates once.
	if err := f.replies.VerifyEmailForward(ctx, uuid, token+"x"); !errors.Is(err, ErrForwardVerifyFailed) {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT verify_failures FROM email_forwards WHERE uuid=$1`, uuid); n != 1 {
		t.Fatal("failure not counted", n)
	}
	if err := f.replies.VerifyEmailForward(ctx, uuid, token); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := f.forward(t, uuid); status != "active" {
		t.Fatal(status)
	}
	if err := f.replies.VerifyEmailForward(ctx, uuid, token); !errors.Is(err, ErrForwardVerifyFailed) {
		t.Fatal("token reused", err)
	}

	// Expired and locked links fail even with the right token.
	mustExec(t, f.db, `DELETE FROM email_forwards WHERE forward_to LIKE 'extra%'`)
	other, otherToken := f.create(t, "later@outside.test", true)
	mustExec(t, f.db, `UPDATE email_forwards SET verify_expires_at=now()-interval '1 second' WHERE uuid=$1`, other)
	if err := f.replies.VerifyEmailForward(ctx, other, otherToken); !errors.Is(err, ErrForwardVerifyFailed) {
		t.Fatal("expired token accepted", err)
	}
	mustExec(t, f.db, `UPDATE email_forwards SET verify_expires_at=now()+interval '1 hour', verify_failures=10 WHERE uuid=$1`, other)
	if err := f.replies.VerifyEmailForward(ctx, other, otherToken); !errors.Is(err, ErrForwardVerifyFailed) {
		t.Fatal("locked forward verified", err)
	}

	// Resend: cooldown, then a rotated token that unlocks; three per day.
	if _, err := f.replies.ResendForwardVerification(ctx, f.user, f.org, other); !errors.Is(err, ErrForwardResendLimited) {
		t.Fatal("cooldown ignored", err)
	}
	mustExec(t, f.db, `UPDATE email_forwards SET verify_last_sent_at=now()-interval '2 minutes' WHERE uuid=$1`, other)
	if _, err := f.replies.ResendForwardVerification(ctx, f.user, f.org, other); err != nil {
		t.Fatal(err)
	}
	fresh := forwardLink.FindStringSubmatch(f.verifyMail[len(f.verifyMail)-1].TextBody)[2]
	if err := f.replies.VerifyEmailForward(ctx, other, otherToken); !errors.Is(err, ErrForwardVerifyFailed) {
		t.Fatal("old token still valid", err)
	}
	if err := f.replies.VerifyEmailForward(ctx, other, fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := f.replies.ResendForwardVerification(ctx, f.user, f.org, other); !errors.As(err, &invalid) {
		t.Fatal("resend for an active forward", err)
	}
	mustExec(t, f.db, `UPDATE email_forwards SET status='pending',verify_send_count=3,verify_last_sent_at=now()-interval '2 minutes' WHERE uuid=$1`, other)
	if _, err := f.replies.ResendForwardVerification(ctx, f.user, f.org, other); !errors.Is(err, ErrForwardResendLimited) {
		t.Fatal("daily resend limit ignored", err)
	}

	// Pause, resume, and the list/delete ownership checks.
	paused, err := f.replies.UpdateEmailForward(ctx, f.user, uuid, &UpdateEmailForwardInput{Active: ptr(false), KeepCopy: ptr(false)})
	if err != nil || paused.Status != "paused" || paused.Active || paused.KeepCopy {
		t.Fatal(paused, err)
	}
	if resumed, err := f.replies.UpdateEmailForward(ctx, f.user, uuid, &UpdateEmailForwardInput{Active: ptr(true)}); err != nil || resumed.Status != "active" {
		t.Fatal(resumed, err)
	}
	if _, err = f.replies.UpdateEmailForward(ctx, f.user, other, &UpdateEmailForwardInput{Active: ptr(true)}); !errors.As(err, &invalid) {
		t.Fatal("unverified forward resumed", err)
	}
	if _, err = f.replies.UpdateEmailForward(ctx, stranger, uuid, &UpdateEmailForwardInput{Active: ptr(false)}); !errors.Is(err, ErrForwardNotFound) {
		t.Fatal(err)
	}
	list, err := f.replies.ListEmailForwards(ctx, f.user)
	if err != nil || len(list) != 2 || list[0].IdentityUUID != f.identityUUID || list[0].IdentityEmail != "owner@arrival.test" {
		t.Fatal(list, err)
	}
	if err = f.replies.DeleteEmailForward(ctx, stranger, uuid); !errors.Is(err, ErrForwardNotFound) {
		t.Fatal(err)
	}
	if err = f.replies.DeleteEmailForward(ctx, f.user, uuid); err != nil {
		t.Fatal(err)
	}

	// Without a sender nothing is stored.
	bare := NewAutoReplyService(f.db, f.replies.cfg)
	if _, err = bare.CreateEmailForward(ctx, f.user, f.org, &CreateEmailForwardInput{IdentityUUID: f.identityUUID, ForwardTo: "new@outside.test"}); !errors.Is(err, ErrProviderNotConfigured) {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM email_forwards WHERE forward_to='new@outside.test'`); n != 0 {
		t.Fatal("forward stored without verification mail")
	}
}

func TestForwardVerificationSendLimits(t *testing.T) {
	f := newForwardFixture(t)
	ctx := context.Background()

	// Deleting and recreating a forward does not reset the destination limit.
	for i := 0; i < forwardVerifyDestHour; i++ {
		uuid, _ := f.create(t, "victim@outside.test", true)
		if err := f.replies.DeleteEmailForward(ctx, f.user, uuid); err != nil {
			t.Fatal(err)
		}
	}
	sent := len(f.verifyMail)
	if _, err := f.replies.CreateEmailForward(ctx, f.user, f.org, &CreateEmailForwardInput{IdentityUUID: f.identityUUID, ForwardTo: "VICTIM@outside.test", KeepCopy: true}); !errors.Is(err, ErrForwardVerifySendLimited) {
		t.Fatal("destination hourly limit ignored", err)
	}
	if len(f.verifyMail) != sent || f.count(t, `SELECT COUNT(*) FROM email_forwards`) != 0 {
		t.Fatal("a limited request sent mail or kept a forward")
	}
	// Older sends count toward the daily limit only.
	mustExec(t, f.db, `UPDATE transactional_emails SET created_at=now()-interval '2 hours'`)
	for i := forwardVerifyDestHour; i < forwardVerifyDestDay; i++ {
		uuid, _ := f.create(t, "victim@outside.test", true)
		mustExec(t, f.db, `DELETE FROM email_forwards WHERE uuid=$1`, uuid)
	}
	mustExec(t, f.db, `UPDATE transactional_emails SET created_at=now()-interval '2 hours'`)
	if _, err := f.replies.CreateEmailForward(ctx, f.user, f.org, &CreateEmailForwardInput{IdentityUUID: f.identityUUID, ForwardTo: "victim@outside.test", KeepCopy: true}); !errors.Is(err, ErrForwardVerifySendLimited) {
		t.Fatal("destination daily limit ignored", err)
	}

	// The per-user limit spans destinations, and a resend counts too.
	mustExec(t, f.db, `UPDATE transactional_emails SET created_at=now()-interval '2 days'`)
	var last string
	for i := 0; i < forwardVerifyUserHour; i++ {
		uuid, _ := f.create(t, "dest"+string(rune('a'+i))+"@outside.test", true)
		mustExec(t, f.db, `DELETE FROM email_forwards WHERE uuid=$1`, uuid)
	}
	mustExec(t, f.db, `UPDATE transactional_emails SET created_at=now()-interval '2 days' WHERE id IN (SELECT id FROM transactional_emails ORDER BY id DESC LIMIT 1)`)
	last, _ = f.create(t, "fresh@outside.test", true)
	mustExec(t, f.db, `UPDATE email_forwards SET verify_last_sent_at=now()-interval '2 minutes' WHERE uuid=$1`, last)
	if _, err := f.replies.ResendForwardVerification(ctx, f.user, f.org, last); !errors.Is(err, ErrForwardVerifySendLimited) {
		t.Fatal("user hourly limit ignored on resend", err)
	}
	if _, err := f.replies.CreateEmailForward(ctx, f.user, f.org, &CreateEmailForwardInput{IdentityUUID: f.identityUUID, ForwardTo: "another@outside.test", KeepCopy: true}); !errors.Is(err, ErrForwardVerifySendLimited) {
		t.Fatal("user hourly limit ignored", err)
	}
}

func TestForwardExecution(t *testing.T) {
	f := newForwardFixture(t)
	ctx := context.Background()
	uuid, token := f.create(t, "dest@outside.test", true)

	f.deliver(t, "pending-1", "alice@sender.test")
	if n := f.count(t, `SELECT COUNT(*) FROM mail_arrival_jobs WHERE kind='forward'`); n != 0 {
		t.Fatal("pending forward queued a job")
	}
	if err := f.replies.VerifyEmailForward(ctx, uuid, token); err != nil {
		t.Fatal(err)
	}

	raw := "From: Alice Example <alice@sender.test>\r\nReply-To: Desk <desk@sender.test>\r\nTo: owner@arrival.test\r\nSubject: Report\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/related; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>See <img src=\"cid:logo@x\"></p>\r\n" +
		"--b\r\nContent-Type: image/png\r\nContent-Disposition: inline; filename=logo.png\r\nContent-ID: <logo@x>\r\n\r\nPNGDATA\r\n--b--\r\n"
	f.deliverRaw(t, "fw-1", raw)
	f.runOnce(t)
	if status, result := f.job(t, "fw-1", "forward"); status != "done" {
		t.Fatal(status, result)
	}
	if len(f.dispatched) != 1 {
		t.Fatalf("dispatched %d", len(f.dispatched))
	}
	p := f.dispatched[0]
	var orgUUID string
	if err := f.db.QueryRow(`SELECT uuid::text FROM organizations WHERE id=$1`, f.org).Scan(&orgUUID); err != nil {
		t.Fatal(err)
	}
	tag := forwardLoopTag(orgUUID, uuid)
	if !strings.Contains(p.From, "Alice Example via Mailat") || !strings.Contains(p.From, "<owner@arrival.test>") || p.ReplyTo != "desk@sender.test" ||
		p.Subject != "Report" || !strings.Contains(p.HTMLBody, "cid:logo@x") || p.To[0] != "<dest@outside.test>" {
		t.Fatalf("%+v", p)
	}
	if p.Headers["X-Mailat-Loop"] != tag || p.Headers["X-Mailat-Forwarded-For"] != "owner@arrival.test" {
		t.Fatal(p.Headers)
	}
	if len(p.Attachments) != 1 || p.Attachments[0].S3Key == "" || len(p.Attachments[0].Data) != 0 || p.Attachments[0].CID != "logo@x" || p.Attachments[0].Disposition != "inline" {
		t.Fatalf("%+v", p.Attachments)
	}
	var kind, ref string
	var txID int64
	if err := f.db.QueryRow(`SELECT id,system_kind,system_ref::text FROM transactional_emails WHERE system_kind='forward'`).Scan(&txID, &kind, &ref); err != nil || ref != uuid {
		t.Fatal(kind, ref, err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM send_attachment_refs WHERE transactional_email_id=$1`, txID); n != 1 {
		t.Fatal("attachment reference missing", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM received_emails WHERE direction='outbound'`); n != 0 {
		t.Fatal("forward recorded a Sent copy")
	}
	if _, _, count := f.forward(t, uuid); count != 1 {
		t.Fatal("forward_count", count)
	}

	// Loops: this forward's own tag, or three earlier hops.
	f.deliver(t, "loop-own", "alice@sender.test", "X-Mailat-Loop: "+tag)
	f.deliver(t, "loop-hops", "alice@sender.test", "X-Mailat-Loop: a1, b2", "X-Mailat-Loop: c3")
	f.deliver(t, "self", "dest@outside.test")
	f.runOnce(t)
	for ses, want := range map[string]string{"loop-own": "skipped:mail-loop", "loop-hops": "skipped:mail-loop", "self": "skipped:sender-is-destination"} {
		if _, result := f.job(t, ses, "forward"); result != want {
			t.Errorf("%s: %s", ses, result)
		}
	}

	// Too large: skipped, local mail kept, the reason shown.
	f.runner.cfg.ForwardMaxBytes = 10
	f.deliver(t, "big", "alice@sender.test")
	f.runOnce(t)
	if _, result := f.job(t, "big", "forward"); result != "skipped:too-large" {
		t.Fatal(result)
	}
	if status, lastError, _ := f.forward(t, uuid); status != "active" || !strings.Contains(lastError, "too large") {
		t.Fatal(status, lastError)
	}
	f.runner.cfg.ForwardMaxBytes = 0

	// Daily cap.
	f.runner.cfg.ForwardDailyLimit = 1
	f.deliver(t, "capped", "alice@sender.test")
	f.runOnce(t)
	if _, result := f.job(t, "capped", "forward"); result != "skipped:daily-cap" {
		t.Fatal(result)
	}
	f.runner.cfg.ForwardDailyLimit = 0

	// keepCopy=false archives the local copy and never deletes it.
	if _, err := f.replies.UpdateEmailForward(ctx, f.user, uuid, &UpdateEmailForwardInput{KeepCopy: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	f.deliver(t, "archived", "alice@sender.test")
	var folder string
	var archived, read bool
	if err := f.db.QueryRow(`SELECT folder,is_archived,is_read FROM received_emails WHERE ses_message_id='archived'`).Scan(&folder, &archived, &read); err != nil || folder != "archive" || !archived || !read {
		t.Fatal(folder, archived, read, err)
	}

	// A complaint on a forwarded message suspends the forward.
	mustExec(t, f.db, `UPDATE transactional_emails SET provider_message_id='prov-fw-1',status='sent' WHERE id=$1`, txID)
	if err := f.receive.ProcessDeliveryEvent(ctx, f.auth, "complaint-1", campaignFeedback("Complaint", "prov-fw-1", "", "", "dest@outside.test")); err != nil {
		t.Fatal(err)
	}
	if status, lastError, _ := f.forward(t, uuid); status != "suspended" || !strings.Contains(lastError, "spam") {
		t.Fatal(status, lastError)
	}
	if active := f.count(t, `SELECT COUNT(*) FROM email_forwards WHERE uuid=$1 AND active`, uuid); active != 0 {
		t.Fatal("suspended forward still active")
	}
	f.runOnce(t)
	if _, result := f.job(t, "archived", "forward"); result != "skipped:forward-suspended" {
		t.Fatal(result)
	}
}

func TestForwardSuspensionAndSkips(t *testing.T) {
	f := newForwardFixture(t)
	ctx := context.Background()
	uuid, token := f.create(t, "dest@outside.test", true)
	if err := f.replies.VerifyEmailForward(ctx, uuid, token); err != nil {
		t.Fatal(err)
	}

	// A permanent bounce of a forwarded message suspends; a transient one does not.
	f.deliver(t, "b-1", "alice@sender.test")
	f.runOnce(t)
	mustExec(t, f.db, `UPDATE transactional_emails SET provider_message_id='prov-b-1',status='sent' WHERE system_kind='forward'`)
	if err := f.receive.ProcessDeliveryEvent(ctx, f.auth, "transient-1", campaignFeedback("Bounce", "prov-b-1", "", "Transient", "dest@outside.test")); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := f.forward(t, uuid); status != "active" {
		t.Fatal("transient bounce suspended", status)
	}
	if err := f.receive.ProcessDeliveryEvent(ctx, f.auth, "permanent-1", campaignFeedback("Bounce", "prov-b-1", "", "Permanent", "dest@outside.test")); err != nil {
		t.Fatal(err)
	}
	if status, lastError, _ := f.forward(t, uuid); status != "suspended" || !strings.Contains(lastError, "bounced") {
		t.Fatal(status, lastError)
	}

	// A destination suppressed after verification suspends at run time.
	mustExec(t, f.db, `UPDATE email_forwards SET status='active',active=true,last_error=NULL WHERE uuid=$1`, uuid)
	mustExec(t, f.db, `DELETE FROM suppression_list`)
	f.deliver(t, "s-1", "alice@sender.test")
	mustExec(t, f.db, `INSERT INTO suppression_list(org_id,email,reason,source) VALUES($1,'dest@outside.test','manual','manual')`, f.org)
	f.runOnce(t)
	if _, result := f.job(t, "s-1", "forward"); result != "skipped:suppressed" {
		t.Fatal(result)
	}
	if status, lastError, _ := f.forward(t, uuid); status != "suspended" || lastError == "" {
		t.Fatal(status, lastError)
	}

	// Spam, a deleted source and a lost identity are skipped without sending.
	mustExec(t, f.db, `DELETE FROM suppression_list`)
	mustExec(t, f.db, `UPDATE email_forwards SET status='active',active=true WHERE uuid=$1`, uuid)
	f.deliverWith(t, "spam-1", "alice@sender.test", func(r *model.SESReceipt) { r.SpamVerdict.Status = "FAIL" })
	f.deliver(t, "gone-1", "alice@sender.test")
	mustExec(t, f.db, `DELETE FROM received_emails WHERE ses_message_id='gone-1'`)
	f.runOnce(t)
	for ses, want := range map[string]string{"spam-1": "skipped:spam", "gone-1": "skipped:source-deleted"} {
		if _, result := f.job(t, ses, "forward"); result != want {
			t.Errorf("%s: %s", ses, result)
		}
	}
	f.deliver(t, "cant-1", "alice@sender.test")
	mustExec(t, f.db, `UPDATE identities SET can_send=false WHERE id=$1`, f.identity)
	f.runOnce(t)
	if _, result := f.job(t, "cant-1", "forward"); result != "skipped:identity-cannot-send" {
		t.Fatal(result)
	}
	before := len(f.dispatched)
	if before != 1 {
		t.Fatalf("only the first forward should have been sent, got %d", before)
	}
}
