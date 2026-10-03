package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/dublyo/mailat/api/internal/testutil"
)

type mailboxTestProvider struct {
	provider.EmailProvider
	mu    sync.Mutex
	calls int
	err   error
	last  *provider.EmailMessage
}

func (p *mailboxTestProvider) SendEmail(_ context.Context, m *provider.EmailMessage) (*provider.SendResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.last = m
	return &provider.SendResult{MessageID: fmt.Sprintf("ses-test-id-%d", p.calls), Success: p.err == nil}, p.err
}
func (p *mailboxTestProvider) Name() string { return "ses" }

type mailboxTestStorage map[string][]byte

func (s mailboxTestStorage) Put(_ context.Context, b, k string, d []byte) error {
	s[b+"/"+k] = append([]byte{}, d...)
	return nil
}
func (s mailboxTestStorage) Get(_ context.Context, b, k string) ([]byte, error) {
	d, ok := s[b+"/"+k]
	if !ok {
		return nil, fmt.Errorf("missing object")
	}
	return append([]byte{}, d...), nil
}

func mailboxFixture(t *testing.T, db *sql.DB, domainName string) (org, user, identity int64) {
	t.Helper()
	var domain int64
	if err := db.QueryRow(`INSERT INTO organizations(name,slug,updated_at) VALUES($1,$1,now()) RETURNING id`, domainName).Scan(&org); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO users(org_id,email,password_hash,updated_at) VALUES($1,$2,'unused',now()) RETURNING id`, org, "owner@"+domainName).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO domains(org_id,name,verification_token,status,ses_verified,receiving_s3_bucket,updated_at) VALUES($1,$2,'test','active',true,'private-test',now()) RETURNING id`, org, domainName).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO identities(user_id,domain_id,email,display_name,can_send,updated_at) VALUES($1,$2,$3,'Owner',true,now()) RETURNING id`, user, domain, "owner@"+domainName).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	return
}

func TestSenderAliasAndComposeHeaders(t *testing.T) {
	for _, value := range []string{"other@elsewhere.test", "Name <alias@example.test>", "alias@example.test\r\nBcc: bad@example.test"} {
		if _, err := normalizeSenderAlias(value, "example.test"); err == nil {
			t.Fatal("invalid alias allowed", value)
		}
	}
	if got, err := normalizeSenderAlias("Sales+tag@EXAMPLE.test", "example.test"); err != nil || got != "sales+tag@example.test" {
		t.Fatal(got, err)
	}
	if err := normalizeCompose(&ComposeEmail{Subject: "bad\nheader", To: []EmailAddress{{Email: "to@example.test"}}, TextBody: "body"}, true); err == nil {
		t.Fatal("header injection accepted")
	}
}

func TestMailboxDraftSendAndUnknownOutcomes(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	_, user, identity := mailboxFixture(t, db, "example.test")
	_, otherUser, otherIdentity := mailboxFixture(t, db, "other.test")
	fake := &mailboxTestProvider{}
	svc := &ComposeService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: fake, attachments: mailboxTestStorage{}}
	mail := &ComposeEmail{IdentityID: identity, From: EmailAddress{Email: "sales@example.test"}, To: []EmailAddress{{Email: "recipient@example.net"}}, Cc: []EmailAddress{{Email: "copy@example.net"}}, ReplyTo: []EmailAddress{{Email: "replies@example.test"}}, Subject: "Test draft", TextBody: "hello", Attachments: []AttachmentRef{{Name: "report.txt", Type: "text/plain", Content: base64.StdEncoding.EncodeToString([]byte("attachment content"))}}}
	draft, err := svc.SaveDraft(ctx, user, mail)
	if err != nil {
		t.Fatal(err)
	}
	var attachmentID string
	if err = db.QueryRow(`SELECT a.uuid FROM email_attachments a JOIN received_emails e ON e.id=a.received_email_id WHERE e.uuid=$1`, draft.ID).Scan(&attachmentID); err != nil {
		t.Fatal(err)
	}
	mail.Attachments = []AttachmentRef{{BlobID: attachmentID}}
	mail.DraftVersion = draft.Version
	updated, err := svc.UpdateDraft(ctx, user, draft.ID, mail)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.UpdateDraft(ctx, user, draft.ID, mail); !errors.Is(err, ErrDraftConflict) {
		t.Fatal("stale update did not conflict", err)
	}
	mail.DraftVersion = updated.Version
	updated, err = svc.UpdateDraft(ctx, user, draft.ID, mail)
	if err != nil {
		t.Fatal("stable attachment reference failed on repeated save", err)
	}
	if err = svc.DeleteDraft(ctx, otherUser, draft.ID); err == nil {
		t.Fatal("cross-user draft delete allowed")
	}
	stolen := &ComposeEmail{IdentityID: otherIdentity, Subject: "stolen", TextBody: "test", Attachments: []AttachmentRef{{BlobID: attachmentID}}}
	if _, err = svc.SaveDraft(ctx, otherUser, stolen); err == nil {
		t.Fatal("cross-user attachment reference allowed")
	}
	mail.DraftID = draft.ID
	mail.DraftVersion = updated.Version
	mail.SubmissionKey = "test-submission-one"
	sent, err := svc.SendEmail(ctx, user, mail)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Status != "sent" {
		t.Fatal(sent)
	}
	if fake.calls != 1 || len(fake.last.Attachments) != 1 || fake.last.ReplyTo != "replies@example.test" {
		t.Fatal("provider payload incorrect")
	}
	replay, err := svc.SendEmail(ctx, user, mail)
	if err != nil || replay.EmailID != sent.EmailID || fake.calls != 1 {
		t.Fatal("replay resubmitted", err)
	}
	var folder, status, from string
	if err = db.QueryRow(`SELECT folder,send_status,from_email FROM received_emails WHERE uuid=$1`, sent.EmailID).Scan(&folder, &status, &from); err != nil {
		t.Fatal(err)
	}
	if folder != "sent" || status != "sent" || from != "sales@example.test" {
		t.Fatal(folder, status, from)
	}
	// A later delivery notification must survive permanent deletion of the Sent copy.
	if _, err = db.Exec(`UPDATE received_emails SET send_status='delivered' WHERE uuid=$1`, sent.EmailID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM received_emails WHERE uuid=$1`, sent.EmailID); err != nil {
		t.Fatal(err)
	}
	afterDelete, err := svc.SendEmail(ctx, user, mail)
	if err != nil || afterDelete.EmailID != sent.EmailID || afterDelete.Status != "delivered" || fake.calls != 1 {
		t.Fatal("deleting Sent removed its submission receipt", afterDelete, err)
	}
	mail.Subject = "different"
	if _, err = svc.SendEmail(ctx, user, mail); err == nil {
		t.Fatal("changed body reused key")
	}
	fake.err = errors.New("provider response timed out")
	uncertain := &ComposeEmail{IdentityID: identity, Subject: "uncertain", TextBody: "hello", To: []EmailAddress{{Email: "recipient@example.net"}}, SubmissionKey: "unknown-submission"}
	unknown, err := svc.SendEmail(ctx, user, uncertain)
	if err != nil || unknown.Status != "unknown" {
		t.Fatal(unknown, err)
	}
	if _, err = svc.SendEmail(ctx, user, uncertain); err != nil || fake.calls != 2 {
		t.Fatal("unknown outcome resubmitted", err)
	}
	if _, err = db.Exec(`DELETE FROM received_emails WHERE uuid=$1`, unknown.EmailID); err != nil {
		t.Fatal(err)
	}
	afterDelete, err = svc.SendEmail(ctx, user, uncertain)
	if err != nil || afterDelete.EmailID != unknown.EmailID || afterDelete.Status != "unknown" || fake.calls != 2 {
		t.Fatal("deleting uncertain Outbox removed its submission receipt", afterDelete, err)
	}
	fake.err = nil
	var attempts sync.WaitGroup
	results := make(chan *SendEmailResult, 2)
	failures := make(chan error, 2)
	for i := 0; i < 2; i++ {
		attempts.Add(1)
		go func() {
			defer attempts.Done()
			result, err := svc.SendEmail(ctx, user, &ComposeEmail{IdentityID: identity, Subject: "concurrent", TextBody: "hello", To: []EmailAddress{{Email: "recipient@example.net"}}, SubmissionKey: "concurrent-submission"})
			results <- result
			failures <- err
		}()
	}
	attempts.Wait()
	for i := 0; i < 2; i++ {
		if err := <-failures; err != nil {
			t.Fatal(err)
		}
	}
	first, second := <-results, <-results
	if first.EmailID != second.EmailID || fake.calls != 3 {
		t.Fatal("concurrent submission duplicated provider call")
	}
}

func TestTransactionalIdempotencyScopeAndContent(t *testing.T) {
	db := testutil.Database(t)
	org, user, identity := mailboxFixture(t, db, "one.test")
	otherOrg, _, _ := mailboxFixture(t, db, "two.test")
	svc := &TransactionalService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: &mailboxTestProvider{}}
	req := &model.SendEmailRequest{From: "owner@one.test", To: []string{"recipient@example.net"}, Subject: "test", Text: "hello", IdempotencyKey: "shared-client-key"}
	first, err := svc.SendEmail(context.Background(), org, req)
	if err != nil {
		t.Fatal(err)
	}
	waitForTransactionalSend(t, db, first.ID)
	replay, err := svc.SendEmail(context.Background(), org, req)
	if err != nil || replay.ID != first.ID {
		t.Fatal("same-org replay failed", err)
	}
	req.Subject = "changed"
	if _, err = svc.SendEmail(context.Background(), org, req); err == nil {
		t.Fatal("changed request did not conflict")
	}
	req.Subject = "test"
	req.From = "owner@two.test"
	second, err := svc.SendEmail(context.Background(), otherOrg, req)
	if err != nil || second.ID == first.ID {
		t.Fatal("cross-org key collision", err)
	}
	waitForTransactionalSend(t, db, second.ID)

	var colleague int64
	if err = db.QueryRow(`INSERT INTO users(org_id,email,password_hash,updated_at) VALUES($1,'reserved@one.test','unused',now()) RETURNING id`, org).Scan(&colleague); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO identities(user_id,domain_id,email,can_send,updated_at) SELECT $1,domain_id,'reserved@one.test',true,now() FROM identities WHERE id=$2`, colleague, identity); err != nil {
		t.Fatal(err)
	}
	req.From, req.IdempotencyKey = "reserved@one.test", "reserved-address-attempt"
	if _, err = svc.SendEmailForUser(context.Background(), org, user, req); err == nil {
		t.Fatal("other user's explicit address allowed")
	}
	compose := &ComposeService{db: db}
	if _, err = compose.authorizeMailboxSender(context.Background(), user, identity, req.From); err == nil {
		t.Fatal("compose allowed another user's explicit address")
	}
	req.From, req.IdempotencyKey = "alias@one.test", "owned-domain-alias"
	alias, err := svc.SendEmailForUser(context.Background(), org, user, req)
	if err != nil {
		t.Fatal("owned-domain alias rejected", err)
	}
	waitForTransactionalSend(t, db, alias.ID)
	if _, err = compose.authorizeMailboxSender(context.Background(), user, identity, req.From); err != nil {
		t.Fatal("compose rejected owned-domain alias", err)
	}
	if _, err = svc.SendEmailForUser(context.Background(), org, colleague, &model.SendEmailRequest{From: "owner@one.test", To: []string{"recipient@example.net"}, Subject: "test", Text: "hello", IdempotencyKey: "shared-client-key"}); err == nil {
		t.Fatal("idempotency replay bypassed actor authorization")
	}
	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	req.ScheduledFor, req.IdempotencyKey = &future, "scheduled-without-queue"
	if _, err = svc.SendEmailForUser(context.Background(), org, user, req); err == nil {
		t.Fatal("scheduled message accepted without queue")
	}
	var scheduledRows int
	if err = db.QueryRow(`SELECT count(*) FROM transactional_emails WHERE idempotency_key=$1`, req.IdempotencyKey).Scan(&scheduledRows); err != nil || scheduledRows != 0 {
		t.Fatal("invalid schedule persisted", err)
	}
}

func TestFailedDraftRetryUsesDurableOutboxAttachments(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	_, user, identity := mailboxFixture(t, db, "retry.test")
	fake := &mailboxTestProvider{err: &provider.MailValidationError{Message: "SES rejected test request"}}
	svc := &ComposeService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: fake, attachments: mailboxTestStorage{}}
	content := []byte("keep this attachment on retry")
	mail := &ComposeEmail{IdentityID: identity, Subject: "retry draft", TextBody: "hello", To: []EmailAddress{{Email: "recipient@example.net"}}, Attachments: []AttachmentRef{{Name: "report.txt", Type: "text/plain", Content: base64.StdEncoding.EncodeToString(content)}}}
	draft, err := svc.SaveDraft(ctx, user, mail)
	if err != nil {
		t.Fatal(err)
	}
	var oldAttachment string
	if err = db.QueryRow(`SELECT a.uuid FROM email_attachments a JOIN received_emails e ON e.id=a.received_email_id WHERE e.uuid=$1`, draft.ID).Scan(&oldAttachment); err != nil {
		t.Fatal(err)
	}
	mail.Attachments = []AttachmentRef{{BlobID: oldAttachment}}
	mail.DraftID, mail.DraftVersion, mail.SubmissionKey = draft.ID, draft.Version, "definitive-failed-key"
	failed, err := svc.SendEmail(ctx, user, mail)
	if err != nil || failed.Status != "failed" {
		t.Fatal(failed, err)
	}
	// Match the UI retry contract: hydrate refs from the owned durable Outbox copy.
	var newAttachment string
	if err = db.QueryRow(`SELECT a.uuid FROM email_attachments a JOIN received_emails e ON e.id=a.received_email_id JOIN identities i ON i.id=e.identity_id WHERE e.uuid=$1 AND i.user_id=$2 AND e.send_status='failed'`, failed.EmailID, user).Scan(&newAttachment); err != nil {
		t.Fatal(err)
	}
	if newAttachment == oldAttachment {
		t.Fatal("outgoing copy unexpectedly reused the consumed draft attachment ID")
	}
	fake.err = nil
	mail.DraftID, mail.DraftVersion, mail.SubmissionKey = "", 0, "deliberate-new-attempt"
	mail.Attachments = []AttachmentRef{{BlobID: newAttachment}}
	retried, err := svc.SendEmail(ctx, user, mail)
	if err != nil || retried.Status != "sent" || fake.calls != 2 {
		t.Fatal(retried, err)
	}
	if len(fake.last.Attachments) != 1 || !bytes.Equal(fake.last.Attachments[0].Data, content) {
		t.Fatal("retry lost attachment content")
	}
}

func TestComposeReplayDatabaseFailureRemainsInfrastructureError(t *testing.T) {
	db := testutil.Database(t)
	_, user, identity := mailboxFixture(t, db, "db-error.test")
	fake := &mailboxTestProvider{err: errors.New("provider connection timed out")}
	svc := &ComposeService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: fake, attachments: mailboxTestStorage{}}
	mail := &ComposeEmail{IdentityID: identity, Subject: "uncertain", TextBody: "hello", To: []EmailAddress{{Email: "recipient@example.net"}}, SubmissionKey: "lost-response-key"}
	first, err := svc.SendEmail(context.Background(), user, mail)
	if err != nil || first.Status != "unknown" {
		t.Fatal(first, err)
	}
	if _, err = db.Exec(`ALTER TABLE compose_submission_keys RENAME TO unavailable_compose_submission_keys`); err != nil {
		t.Fatal(err)
	}
	_, err = svc.SendEmail(context.Background(), user, mail)
	var validation *provider.MailValidationError
	if err == nil || errors.As(err, &validation) || errors.Is(err, ErrSubmissionConflict) || errors.Is(err, ErrDraftConflict) {
		t.Fatal("database failure was classified as a request error", err)
	}
	if fake.calls != 1 {
		t.Fatal("replay lookup failure attempted another provider submission")
	}
}

// Wait for the fallback goroutine before the isolated schema is removed.
func waitForTransactionalSend(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		var status string
		if err := db.QueryRow(`SELECT status FROM transactional_emails WHERE uuid=$1`, id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "sent" {
			return
		}
		if status != "queued" && status != "sending" {
			t.Fatalf("unexpected transactional status %q", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("transactional send did not complete")
}
