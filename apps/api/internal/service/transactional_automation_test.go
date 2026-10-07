package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
	"github.com/dublyo/mailat/api/internal/worker"
)

func TestTransactionalAttachmentsAndUnifiedSent(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	org, user, _ := mailboxFixture(t, db, "automated.test")
	// Sending storage is independent from receiving, which remains disabled.
	if _, err := db.Exec(`UPDATE domains SET attachment_s3_bucket='send-private',receiving_s3_bucket=NULL,receiving_enabled=false`); err != nil {
		t.Fatal(err)
	}
	fake := &mailboxTestProvider{}
	storage := mailboxTestStorage{}
	svc := &TransactionalService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: fake, attachments: storage}
	content := []byte{0, 1, 2, 3, 255, 10, 13}
	req := &model.SendEmailRequest{From: "alias@automated.test", To: []string{"to@external.test"}, Bcc: []string{"hidden@external.test"}, Subject: "Attachment", Text: "hello", IdempotencyKey: "attachment-key", Attachments: []model.AttachmentDTO{{Name: "picture.bin", Type: "application/octet-stream", Content: base64.StdEncoding.EncodeToString(content), CID: "image-1", Disposition: "inline"}}}
	result, err := svc.SendEmailForUser(ctx, org, SendActor{UserID: user, Admin: true}, req)
	if err != nil {
		t.Fatal(err)
	}
	waitForTransactionalSend(t, db, result.ID)
	fake.mu.Lock()
	last := fake.last
	calls := fake.calls
	fake.mu.Unlock()
	if calls != 1 || len(last.Attachments) != 1 || !bytes.Equal(last.Attachments[0].Data, content) || last.Attachments[0].ContentID != "image-1" || !last.Attachments[0].Inline {
		t.Fatalf("attachment changed: %+v", last)
	}
	mailbox, err := (&InboxService{db: db}).GetReceivedEmail(ctx, user, result.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mailbox.Folder != "sent" || mailbox.FromEmail != "alias@automated.test" || len(mailbox.Attachments) != 1 || len(mailbox.BccEmails) != 1 {
		t.Fatalf("wrong Sent copy %+v", mailbox)
	}
	var encoded []byte
	if err = db.QueryRow(`SELECT send_payload FROM transactional_emails WHERE uuid=$1`, result.ID).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var payload worker.EmailSendPayload
	if err = json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.MessageUUID != result.ID || !bytes.Equal(payload.Attachments[0].Data, content) {
		t.Fatal("durable attachment payload changed")
	}
	again, err := svc.SendEmailForUser(ctx, org, SendActor{UserID: user, Admin: true}, req)
	if err != nil || again.ID != result.ID {
		t.Fatal("replay", again, err)
	}
	_, other, _ := mailboxFixture(t, db, "other-auto.test")
	if _, err = svc.GetEmailStatusForUser(ctx, org, other, result.ID); err == nil {
		t.Fatal("foreign user can read status")
	}
	var events int
	if err = db.QueryRow(`SELECT count(*) FROM webhook_events WHERE org_id=$1 AND event_type='email.sent'`, org).Scan(&events); err != nil || events != 1 {
		t.Fatal("sent event is not durable", events, err)
	}
}

func TestBatchPartialRetryConcurrencyAndChangedPayload(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	org, user, _ := mailboxFixture(t, db, "batch.test")
	fake := &mailboxTestProvider{}
	svc := &TransactionalService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: fake}
	req := model.BatchSendRequest{IdempotencyKey: "batch-client-key", Emails: []model.SendEmailRequest{{From: "owner@batch.test", To: []string{"to@external.test"}, Subject: "valid", Text: "body"}, {From: "owner@batch.test", To: []string{"invalid"}, Subject: "invalid", Text: "body"}}}
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	ids := make(chan string, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := svc.BatchSendEmailForUser(ctx, org, SendActor{UserID: user, Admin: true}, &req)
			if e != nil {
				errs <- e
				return
			}
			if len(r.Results) != 2 || r.Results[1].Status != "failed" {
				errs <- errors.New("partial validation outcome changed")
				return
			}
			ids <- r.Results[0].ID
		}()
	}
	wg.Wait()
	close(errs)
	close(ids)
	for err := range errs {
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if first != id {
			t.Fatal("concurrent retry changed UUID")
		}
	}
	waitForTransactionalSend(t, db, first)
	fake.mu.Lock()
	calls := fake.calls
	fake.mu.Unlock()
	if calls != 1 {
		t.Fatalf("duplicate send: %d", calls)
	}
	req.Emails[0].Text = "changed"
	if _, err := svc.BatchSendEmailForUser(ctx, org, SendActor{UserID: user, Admin: true}, &req); !errors.Is(err, ErrSubmissionConflict) {
		t.Fatal("changed batch must conflict", err)
	}
	req.IdempotencyKey = ""
	if _, err := svc.BatchSendEmailForUser(ctx, org, SendActor{UserID: user, Admin: true}, &req); err == nil {
		t.Fatal("missing batch key allowed")
	}
}

func TestTemplateVariablesRecomputeAndEmptyArrays(t *testing.T) {
	db := testutil.Database(t)
	org, _, _ := mailboxFixture(t, db, "template.test")
	svc := &TransactionalService{db: db}
	ctx := context.Background()
	list, err := svc.ListTemplates(ctx, org)
	if err != nil || list == nil {
		t.Fatal("empty list must be []", list, err)
	}
	item, err := svc.CreateTemplate(ctx, org, &model.CreateTemplateRequest{Name: "Template", Subject: "{{first}}", HTML: "<p>{{body}}</p>"})
	if err != nil {
		t.Fatal(err)
	}
	item, err = svc.UpdateTemplate(ctx, org, item.UUID, &model.UpdateTemplateRequest{Subject: "{{second}}"})
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]bool{}
	for _, v := range item.Variables {
		vars[v] = true
	}
	if vars["first"] || !vars["second"] || !vars["body"] {
		t.Fatal("stale variables", item.Variables)
	}
	item, err = svc.UpdateTemplate(ctx, org, item.UUID, &model.UpdateTemplateRequest{Subject: "plain", HTML: "plain"})
	if err != nil || item.Variables == nil || len(item.Variables) != 0 {
		t.Fatal("expected empty variables array", item, err)
	}
}
