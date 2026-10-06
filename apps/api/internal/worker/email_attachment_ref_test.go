package worker

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/dublyo/mailat/api/internal/testutil"
)

type refTestStorage struct {
	objects map[string][]byte
	err     error
}

func (s *refTestStorage) Put(context.Context, string, string, []byte) error { return nil }
func (s *refTestStorage) Get(_ context.Context, bucket, key string) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	data, ok := s.objects[bucket+"/"+key]
	if !ok {
		return nil, provider.ErrAttachmentNotFound
	}
	return data, nil
}

func TestEmailWorkerLoadsReferencedAttachmentsAndHeaders(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	var org int64
	if err := db.QueryRow(`INSERT INTO organizations(name,slug,updated_at) VALUES('Refs','refs',now()) RETURNING id`).Scan(&org); err != nil {
		t.Fatal(err)
	}
	queue := func(name string) *EmailSendPayload {
		t.Helper()
		var id int64
		if err := db.QueryRow(`INSERT INTO transactional_emails(org_id,message_id,from_address,to_addresses,subject,status,updated_at) VALUES($1,$2,'from@example.test','to@example.test','s','queued',now()) RETURNING id`, org, "<"+name+"@example.test>").Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO send_attachment_refs VALUES($1,'bucket',$2)`, id, name); err != nil {
			t.Fatal(err)
		}
		p := NewEmailSendPayload(id, org, "from@example.test", []string{"to@example.test"}, "s", "", "hello", "<"+name+"@example.test>")
		p.MessageUUID = "11111111-2222-3333-4444-555555555555"
		p.Headers = map[string]string{"Auto-Submitted": "auto-replied", "x-mailat-message-id": "forged", "X-Custom": "drop"}
		p.Attachments = []AttachmentInfo{{Name: "a.png", Type: "image/png", Disposition: "inline", CID: "img1", S3Bucket: "bucket", S3Key: name}}
		return p
	}
	state := func(id int64) (status string, refs int) {
		t.Helper()
		if err := db.QueryRow(`SELECT status,(SELECT count(*) FROM send_attachment_refs WHERE transactional_email_id=$1) FROM transactional_emails WHERE id=$1`, id).Scan(&status, &refs); err != nil {
			t.Fatal(err)
		}
		return
	}
	content := []byte{0x89, 'P', 'N', 'G', 0, 1}
	storage := &refTestStorage{objects: map[string][]byte{"bucket/present": content}}

	t.Run("loaded", func(t *testing.T) {
		fake := &guardTestProvider{}
		h := NewEmailHandlerWithProvider(db, &config.Config{}, fake).WithAttachmentStorage(storage)
		p := queue("present")
		if err := h.ProcessEmail(ctx, p); err != nil {
			t.Fatal(err)
		}
		if fake.calls != 1 || len(fake.last.Attachments) != 1 || !bytes.Equal(fake.last.Attachments[0].Data, content) || fake.last.Attachments[0].ContentID != "img1" || !fake.last.Attachments[0].Inline {
			t.Fatalf("attachment not loaded: %+v", fake.last)
		}
		if fake.last.Headers["X-Mailat-Message-ID"] != p.MessageUUID || fake.last.Headers["Auto-Submitted"] != "auto-replied" || fake.last.Headers["X-Custom"] != "" || fake.last.Headers["x-mailat-message-id"] != "" {
			t.Fatalf("headers %+v", fake.last.Headers)
		}
		if status, refs := state(p.EmailID); status != "sent" || refs != 0 {
			t.Fatal(status, refs)
		}
	})
	t.Run("missing", func(t *testing.T) {
		fake := &guardTestProvider{}
		p := queue("missing")
		if err := NewEmailHandlerWithProvider(db, &config.Config{}, fake).WithAttachmentStorage(storage).ProcessEmail(ctx, p); err != nil {
			t.Fatal(err)
		}
		if status, refs := state(p.EmailID); fake.calls != 0 || status != "failed" || refs != 0 {
			t.Fatal(fake.calls, status, refs)
		}
	})
	t.Run("transient", func(t *testing.T) {
		fake := &guardTestProvider{}
		p := queue("transient")
		err := NewEmailHandlerWithProvider(db, &config.Config{}, fake).WithAttachmentStorage(&refTestStorage{err: errors.New("timeout")}).ProcessEmail(ctx, p)
		if !errors.Is(err, ErrAttachmentDeferred) {
			t.Fatal(err)
		}
		var later bool
		if err = db.QueryRow(`SELECT next_attempt_at>now() FROM transactional_emails WHERE id=$1`, p.EmailID).Scan(&later); err != nil || !later {
			t.Fatal("not deferred", err)
		}
		if status, refs := state(p.EmailID); fake.calls != 0 || status != "queued" || refs != 1 {
			t.Fatal(fake.calls, status, refs)
		}
	})
}
