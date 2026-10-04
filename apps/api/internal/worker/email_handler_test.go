package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/dublyo/mailat/api/internal/testutil"
	"github.com/hibiken/asynq"
)

type guardTestProvider struct {
	provider.EmailProvider
	calls int
	err   error
	last  *provider.EmailMessage
}

func (p *guardTestProvider) SendEmail(_ context.Context, msg *provider.EmailMessage) (*provider.SendResult, error) {
	p.calls++
	p.last = msg
	return &provider.SendResult{MessageID: "test-provider-id"}, p.err
}
func (p *guardTestProvider) Name() string { return "ses" }

func TestEmailWorkerDoesNotResendTerminalOrUncertainAttempts(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	var org int64
	if err := db.QueryRow(`INSERT INTO organizations(name,slug,updated_at) VALUES('Worker','worker',now()) RETURNING id`).Scan(&org); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		providerErr error
		expected    string
	}{{"accepted", nil, "sent"}, {"uncertain", errors.New("timeout"), "unknown"}, {"rejected", &provider.MailValidationError{Message: "invalid message"}, "failed"}} {
		t.Run(tc.name, func(t *testing.T) {
			var id int64
			if err := db.QueryRow(`INSERT INTO transactional_emails(org_id,message_id,from_address,to_addresses,subject,status,updated_at) VALUES($1,$2,'from@example.test','to@example.test','test','queued',now()) RETURNING id`, org, "<"+tc.name+"@example.test>").Scan(&id); err != nil {
				t.Fatal(err)
			}
			fake := &guardTestProvider{err: tc.providerErr}
			handler := &EmailHandler{db: db, cfg: &config.Config{}, emailProvider: fake}
			payload := NewEmailSendPayload(id, org, "from@example.test", []string{"to@example.test"}, "test", "", "hello", "<id@example.test>")
			data, err := payload.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			task := asynq.NewTask(TypeEmailSend, data)
			for i := 0; i < 2; i++ {
				if err = handler.HandleEmailSend(ctx, task); err != nil {
					t.Fatal(err)
				}
			}
			if fake.calls != 1 {
				t.Fatalf("provider called %d times", fake.calls)
			}
			var status string
			if err = db.QueryRow(`SELECT status FROM transactional_emails WHERE id=$1`, id).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != tc.expected {
				t.Fatal(status)
			}
		})
	}
}

// Inject the event exactly when the provider ID becomes visible, before the worker
// persists its acceptance result. This reproduces a fast SNS notification deterministically.
func TestEmailWorkerPreservesFastDeliveryEvents(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	if _, err := db.Exec(`CREATE FUNCTION simulate_fast_sns() RETURNS trigger AS $$
    BEGIN
      IF OLD.provider_message_id IS NULL AND NEW.provider_message_id IS NOT NULL THEN
        NEW.status := NEW.subject;
      END IF;
      RETURN NEW;
    END; $$ LANGUAGE plpgsql;
    CREATE TRIGGER simulate_fast_sns BEFORE UPDATE ON transactional_emails
    FOR EACH ROW EXECUTE FUNCTION simulate_fast_sns()`); err != nil {
		t.Fatal(err)
	}
	var org int64
	if err := db.QueryRow(`INSERT INTO organizations(name,slug,updated_at) VALUES('Fast events','fast-events',now()) RETURNING id`).Scan(&org); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		status  string
		sendErr error
	}{
		{"delivered", nil}, {"bounced", nil}, {"complained", nil}, {"delivered", errors.New("uncertain provider response")},
	} {
		var id int64
		if err := db.QueryRow(`INSERT INTO transactional_emails(org_id,message_id,from_address,to_addresses,subject,status,updated_at) VALUES($1,gen_random_uuid()::text,'from@example.test','to@example.test',$2,'queued',now()) RETURNING id`, org, tc.status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		fake := &guardTestProvider{err: tc.sendErr}
		handler := &EmailHandler{db: db, cfg: &config.Config{}, emailProvider: fake}
		payload := NewEmailSendPayload(id, org, "from@example.test", []string{"to@example.test"}, tc.status, "", "hello", "<fast@example.test>")
		data, err := payload.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if err = handler.HandleEmailSend(ctx, asynq.NewTask(TypeEmailSend, data)); err != nil {
			t.Fatal(err)
		}
		var status string
		if err = db.QueryRow(`SELECT status FROM transactional_emails WHERE id=$1`, id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != tc.status {
			t.Fatalf("fast event regressed: want %s, got %s", tc.status, status)
		}
	}
}

func TestWorkerRecoversDurableAttachmentPayloadOnce(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	var org, id int64
	if err := db.QueryRow(`INSERT INTO organizations(name,slug,updated_at) VALUES('Recovery','recovery',NOW()) RETURNING id`).Scan(&org); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO transactional_emails(org_id,message_id,from_address,to_addresses,subject,status,scheduled_for,updated_at) VALUES($1,'<recovery@test>','from@test.test','to@test.test','test','queued',NOW()+INTERVAL '1 hour',NOW()) RETURNING id`, org).Scan(&id); err != nil {
		t.Fatal(err)
	}
	payload := NewEmailSendPayload(id, org, "from@test.test", []string{"to@test.test"}, "test", "", "hello", "<recovery@test>")
	payload.Attachments = []AttachmentInfo{{Name: "binary.bin", Type: "application/octet-stream", Data: []byte{0, 255, 2}, Disposition: "inline", CID: "binary"}}
	data, _ := json.Marshal(payload)
	if _, err := db.Exec(`UPDATE transactional_emails SET send_payload=$2 WHERE id=$1`, id, string(data)); err != nil {
		t.Fatal(err)
	}
	fake := &guardTestProvider{}
	handler := &EmailHandler{db: db, cfg: &config.Config{}, emailProvider: fake}
	if err := handler.RecoverPending(ctx); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 0 {
		t.Fatal("future job sent early")
	}
	if _, err := db.Exec(`UPDATE transactional_emails SET scheduled_for=NOW()-INTERVAL '1 minute' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := handler.RecoverPending(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if fake.calls != 1 || len(fake.last.Attachments) != 1 || !bytes.Equal(fake.last.Attachments[0].Data, []byte{0, 255, 2}) || !fake.last.Attachments[0].Inline || fake.last.Attachments[0].ContentID != "binary" {
		t.Fatalf("durable attachment changed: %+v", fake.last)
	}
}
