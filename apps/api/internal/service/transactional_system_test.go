package service

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/mail"
	"testing"

	"github.com/google/uuid"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
	"github.com/dublyo/mailat/api/internal/worker"
)

func sendAutomatedTx(t *testing.T, db *sql.DB, svc *TransactionalService, in *AutomatedSend, commit bool) (*model.SendEmailResponse, *worker.EmailSendPayload, error) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	resp, payload, err := svc.SendAutomated(context.Background(), tx, in)
	if err == nil && commit {
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	return resp, payload, err
}

func TestSendAutomatedQueuesSystemMailWithoutSentCopy(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	org, user, identity := mailboxFixture(t, db, "system.test")
	fake := &mailboxTestProvider{}
	content := []byte{0x89, 'P', 'N', 'G', 7}
	storage := mailboxTestStorage{"private-test/attachments/a": content}
	svc := &TransactionalService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: fake, attachments: storage}
	ref := uuid.NewString()
	in := &AutomatedSend{
		OrgID: org, IdentityID: identity, ActingUserID: user, To: "Sender <sender@elsewhere.test>",
		Subject: "Hello", Text: "body", HTML: "<p>body</p><img src=\"cid:img1\">", ReplyTo: "original@elsewhere.test",
		FromName: "Sender via Mailat", Headers: map[string]string{"X-Mailat-Loop": "aa,bb", "X-Mailat-Forwarded-For": "owner@system.test"},
		S3Attachments: []S3Attachment{{Bucket: "private-test", Key: "attachments/a", Name: "a.png", ContentType: "image/png", ContentID: "img1", Size: len(content), Inline: true}},
		Kind:          "forward", Ref: ref, DedupeKey: "mailat:fw:1:ses-1",
	}
	resp, payload, err := sendAutomatedTx(t, db, svc, in, true)
	if err != nil || payload == nil || resp.ID != payload.MessageUUID {
		t.Fatal(resp, payload, err)
	}
	if len(payload.Attachments) != 1 || len(payload.Attachments[0].Data) != 0 || payload.Attachments[0].S3Key != "attachments/a" {
		t.Fatalf("attachment must travel by reference: %+v", payload.Attachments)
	}
	var kind, gotRef string
	var systemUser int64
	if err = db.QueryRow(`SELECT system_kind,system_ref::text,system_user_id FROM transactional_emails WHERE uuid=$1`, resp.ID).Scan(&kind, &gotRef, &systemUser); err != nil {
		t.Fatal(err)
	}
	if kind != "forward" || gotRef != ref || systemUser != user {
		t.Fatal(kind, gotRef, systemUser)
	}
	var copies, refs int
	if err = db.QueryRow(`SELECT (SELECT count(*) FROM received_emails), (SELECT count(*) FROM send_attachment_refs)`).Scan(&copies, &refs); err != nil || copies != 0 || refs != 1 {
		t.Fatalf("sent copies=%d refs=%d %v", copies, refs, err)
	}

	// The durable worker sends it with loaded bytes and the allowlisted headers.
	if err = worker.NewEmailHandlerWithProvider(db, svc.cfg, fake).WithAttachmentStorage(storage).ProcessEmail(ctx, &worker.EmailSendPayload{EmailID: payload.EmailID, OrgID: org}); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	last := fake.last
	fake.mu.Unlock()
	from, err := mail.ParseAddress(last.From)
	if err != nil || from.Name != "Sender via Mailat" || from.Address != "owner@system.test" {
		t.Fatalf("from %q %v", last.From, err)
	}
	if last.ReplyTo != "original@elsewhere.test" || last.Headers["X-Mailat-Loop"] != "aa,bb" || last.Headers["X-Mailat-Message-ID"] != resp.ID {
		t.Fatalf("headers %+v reply-to %q", last.Headers, last.ReplyTo)
	}
	if len(last.Attachments) != 1 || !bytes.Equal(last.Attachments[0].Data, content) || !last.Attachments[0].Inline {
		t.Fatalf("attachment %+v", last.Attachments)
	}
	if err = db.QueryRow(`SELECT count(*) FROM send_attachment_refs`).Scan(&refs); err != nil || refs != 0 {
		t.Fatal("refs not cleared after send", refs, err)
	}

	// A replay returns the first response and writes nothing.
	again, againPayload, err := sendAutomatedTx(t, db, svc, in, true)
	if err != nil || again.ID != resp.ID || againPayload != nil {
		t.Fatal("replay", again, againPayload, err)
	}
	changed := *in
	changed.Subject = "Different"
	if _, _, err = sendAutomatedTx(t, db, svc, &changed, true); !errors.Is(err, ErrSubmissionConflict) {
		t.Fatal("changed replay", err)
	}
	var rows int
	if err = db.QueryRow(`SELECT count(*) FROM transactional_emails`).Scan(&rows); err != nil || rows != 1 {
		t.Fatal(rows, err)
	}
}

func TestSendAutomatedRollbackReleasesKeyAndQuota(t *testing.T) {
	db := testutil.Database(t)
	org, user, identity := mailboxFixture(t, db, "rollback.test")
	if _, err := db.Exec(`UPDATE organizations SET monthly_email_limit=1 WHERE id=$1`, org); err != nil {
		t.Fatal(err)
	}
	svc := &TransactionalService{db: db, cfg: &config.Config{EmailProvider: "ses"}, emailProvider: &mailboxTestProvider{}}
	in := &AutomatedSend{OrgID: org, IdentityID: identity, ActingUserID: user, To: "a@elsewhere.test", Subject: "Re: hi", Text: "away",
		Headers: map[string]string{"Auto-Submitted": "auto-replied", "X-Auto-Response-Suppress": "All"}, Kind: "auto_reply", DedupeKey: "mailat:ar:1:ses-1"}
	// The caller's transaction rolls back (e.g. a lost lease): nothing persists.
	if _, payload, err := sendAutomatedTx(t, db, svc, in, false); err != nil || payload == nil {
		t.Fatal(payload, err)
	}
	var rows, keys int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM transactional_emails),(SELECT count(*) FROM email_submission_keys)`).Scan(&rows, &keys); err != nil || rows != 0 || keys != 0 {
		t.Fatal(rows, keys, err)
	}
	if _, _, err := sendAutomatedTx(t, db, svc, in, true); err != nil {
		t.Fatal("quota unit was not returned on rollback:", err)
	}
	next := *in
	next.DedupeKey = "mailat:ar:1:ses-2"
	if _, _, err := sendAutomatedTx(t, db, svc, &next, true); !errors.Is(err, ErrMonthlySendQuota) {
		t.Fatal("quota not enforced", err)
	}
}

func TestSendAutomatedGuards(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	org, user, identity := mailboxFixture(t, db, "guards.test")
	svc := &TransactionalService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: &mailboxTestProvider{}}
	base := AutomatedSend{OrgID: org, IdentityID: identity, ActingUserID: user, To: "a@elsewhere.test", Subject: "s", Text: "t", Kind: "invite", DedupeKey: "mailat:inv:x:1"}
	for name, mutate := range map[string]func(*AutomatedSend){
		"user key":        func(a *AutomatedSend) { a.DedupeKey = "user-key-123" },
		"unknown kind":    func(a *AutomatedSend) { a.Kind = "newsletter" },
		"bad ref":         func(a *AutomatedSend) { a.Ref = "not-a-uuid" },
		"unknown header":  func(a *AutomatedSend) { a.Headers = map[string]string{"X-Custom": "1"} },
		"message id":      func(a *AutomatedSend) { a.Headers = map[string]string{"X-Mailat-Message-ID": "forged"} },
		"bad recipient":   func(a *AutomatedSend) { a.To = "not an address" },
		"other org":       func(a *AutomatedSend) { a.OrgID = org + 1000 },
		"partial ref":     func(a *AutomatedSend) { a.S3Attachments = []S3Attachment{{Name: "x"}} },
		"header injected": func(a *AutomatedSend) { a.Subject = "a\r\nBcc: x@elsewhere.test" },
	} {
		in := base
		mutate(&in)
		if _, _, err := sendAutomatedTx(t, db, svc, &in, true); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := db.Exec(`INSERT INTO suppression_list(org_id,email,reason,source) VALUES($1,'a@elsewhere.test','bounce','ses')`, org); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sendAutomatedTx(t, db, svc, &base, true); !errors.Is(err, ErrSystemRecipientSuppressed) {
		t.Fatal("suppressed recipient", err)
	}
	other := base
	other.To = "b@elsewhere.test"
	if _, err := db.Exec(`UPDATE identities SET can_send=false WHERE id=$1`, identity); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sendAutomatedTx(t, db, svc, &other, true); err == nil {
		t.Fatal("identity without can_send accepted")
	}
	if _, _, err := (&TransactionalService{db: db, cfg: svc.cfg}).SendAutomated(ctx, nil, &other); !errors.Is(err, ErrProviderNotConfigured) {
		t.Fatal("missing provider", err)
	}
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM transactional_emails`).Scan(&rows); err != nil || rows != 0 {
		t.Fatal(rows, err)
	}

	// The system namespace is closed to user-chosen keys on every path.
	if err := validateSubmissionKey("MAILAT:ar:1:x"); err == nil {
		t.Fatal("reserved batch key accepted")
	}
	if _, err := db.Exec(`UPDATE identities SET can_send=true WHERE id=$1`, identity); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SendEmailForUser(ctx, org, user, &model.SendEmailRequest{From: "owner@guards.test", To: []string{"c@elsewhere.test"}, Subject: "s", Text: "t", IdempotencyKey: "mailat:ar:1:x"}); err == nil {
		t.Fatal("reserved transactional key accepted")
	}
}

func TestCancelClearsAttachmentRefs(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	org, _, _ := mailboxFixture(t, db, "cancel-refs.test")
	var id int64
	if err := db.QueryRow(`INSERT INTO transactional_emails(org_id,message_id,from_address,to_addresses,subject,status,updated_at) VALUES($1,'<c@cancel-refs.test>','a@cancel-refs.test','b@elsewhere.test','s','queued',now()) RETURNING id`, org).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO send_attachment_refs VALUES($1,'b','k')`, id); err != nil {
		t.Fatal(err)
	}
	var messageUUID string
	if err := db.QueryRow(`SELECT uuid FROM transactional_emails WHERE id=$1`, id).Scan(&messageUUID); err != nil {
		t.Fatal(err)
	}
	svc := &TransactionalService{db: db, cfg: &config.Config{}}
	if err := svc.CancelEmail(ctx, org, messageUUID); err != nil {
		t.Fatal(err)
	}
	if err := svc.CancelEmail(ctx, org, messageUUID); err == nil {
		t.Fatal("second cancel succeeded")
	}
	var refs int
	if err := db.QueryRow(`SELECT count(*) FROM send_attachment_refs`).Scan(&refs); err != nil || refs != 0 {
		t.Fatal(refs, err)
	}
}
