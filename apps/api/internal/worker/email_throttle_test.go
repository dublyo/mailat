package worker

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/hibiken/asynq"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/dublyo/mailat/api/internal/testutil"
)

func TestBackoffBoundsAndJitter(t *testing.T) {
	fixed := func(v float64) func() float64 { return func() float64 { return v } }
	for _, tc := range []struct {
		attempt  int
		quota    bool
		rnd      float64
		min, max time.Duration
	}{
		{1, false, 0.5, 30 * time.Second, 30 * time.Second},
		{1, false, 0, 24 * time.Second, 24 * time.Second},
		{1, false, 1, 36 * time.Second, 36 * time.Second},
		{2, false, 0.5, time.Minute, time.Minute},
		{7, false, 0.5, 30 * time.Minute, 30 * time.Minute},
		{9, false, 1, 36 * time.Minute, 36 * time.Minute},
		{0, false, 0.5, 30 * time.Second, 30 * time.Second},
		{1, true, 0, time.Hour, time.Hour},
		{9, true, 1, time.Hour, time.Hour},
	} {
		got := backoff(tc.attempt, tc.quota, fixed(tc.rnd))
		if got < tc.min || got > tc.max {
			t.Errorf("backoff(%d,%v,%v)=%v want [%v,%v]", tc.attempt, tc.quota, tc.rnd, got, tc.min, tc.max)
		}
	}
}

// scriptedProvider returns the next scripted error on each call.
type scriptedProvider struct {
	provider.EmailProvider
	errs  []error
	calls int
}

func (p *scriptedProvider) SendEmail(context.Context, *provider.EmailMessage) (*provider.SendResult, error) {
	var err error
	if p.calls < len(p.errs) {
		err = p.errs[p.calls]
	}
	p.calls++
	if err != nil {
		return nil, err
	}
	return &provider.SendResult{MessageID: "ses-id"}, nil
}
func (p *scriptedProvider) Name() string { return "ses" }

func TestWorkerDefersThrottledSendsWithoutDuplicates(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	var org int64
	if err := db.QueryRow(`INSERT INTO organizations(name,slug,updated_at) VALUES('Throttle','throttle',now()) RETURNING id`).Scan(&org); err != nil {
		t.Fatal(err)
	}
	queue := func(name string) int64 {
		t.Helper()
		var id int64
		if err := db.QueryRow(`INSERT INTO transactional_emails(org_id,message_id,from_address,to_addresses,subject,status,updated_at) VALUES($1,$2,'from@example.test','to@example.test','test','queued',now()) RETURNING id`, org, "<"+name+"@example.test>").Scan(&id); err != nil {
			t.Fatal(err)
		}
		payload := NewEmailSendPayload(id, org, "from@example.test", []string{"to@example.test"}, "test", "", "hello", "<"+name+"@example.test>")
		data, err := payload.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`UPDATE transactional_emails SET send_payload=$2 WHERE id=$1`, id, string(data)); err != nil {
			t.Fatal(err)
		}
		return id
	}
	makeDue := func(id int64) {
		t.Helper()
		if _, err := db.Exec(`UPDATE transactional_emails SET next_attempt_at=NOW()-INTERVAL '1 second' WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("throttled twice then sent", func(t *testing.T) {
		id := queue("throttled")
		throttle := &types.TooManyRequestsException{}
		fake := &scriptedProvider{errs: []error{throttle, throttle}}
		handler := &EmailHandler{db: db, cfg: &config.Config{}, emailProvider: fake}
		for want := 1; want <= 2; want++ {
			if err := handler.RecoverPending(ctx); err != nil {
				t.Fatal("RecoverPending after throttle:", err)
			}
			var attempts int
			var status string
			var future, scheduledNull bool
			if err := db.QueryRow(`SELECT status,send_attempts,next_attempt_at>NOW(),scheduled_for IS NULL FROM transactional_emails WHERE id=$1`, id).Scan(&status, &attempts, &future, &scheduledNull); err != nil {
				t.Fatal(err)
			}
			if status != "queued" || attempts != want || !future || !scheduledNull {
				t.Fatalf("after throttle %d: status=%s attempts=%d future=%v scheduledNull=%v", want, status, attempts, future, scheduledNull)
			}
			// Not yet due: another pass must not call the provider.
			if err := handler.RecoverPending(ctx); err != nil {
				t.Fatal(err)
			}
			if fake.calls != want {
				t.Fatalf("provider called before next_attempt_at: %d", fake.calls)
			}
			makeDue(id)
		}
		if err := handler.RecoverPending(ctx); err != nil {
			t.Fatal(err)
		}
		var status string
		var deferred, sentEvents int
		if err := db.QueryRow(`SELECT status,(SELECT count(*) FROM transactional_delivery_events WHERE email_id=$1 AND event_type='deferred'),
			(SELECT count(*) FROM webhook_events WHERE event_type='email.sent' AND org_id=$2) FROM transactional_emails WHERE id=$1`, id, org).Scan(&status, &deferred, &sentEvents); err != nil {
			t.Fatal(err)
		}
		if fake.calls != 3 || status != "sent" || deferred != 2 || sentEvents != 1 {
			t.Fatalf("calls=%d status=%s deferred=%d sentEvents=%d", fake.calls, status, deferred, sentEvents)
		}
	})

	t.Run("gives up at ten attempts", func(t *testing.T) {
		id := queue("give-up")
		if _, err := db.Exec(`UPDATE transactional_emails SET send_attempts=9 WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
		fake := &scriptedProvider{errs: []error{&types.LimitExceededException{}}}
		handler := &EmailHandler{db: db, cfg: &config.Config{}, emailProvider: fake}
		if err := handler.RecoverPending(ctx); err != nil {
			t.Fatal(err)
		}
		var status string
		var attempts int
		if err := db.QueryRow(`SELECT status,send_attempts FROM transactional_emails WHERE id=$1`, id).Scan(&status, &attempts); err != nil {
			t.Fatal(err)
		}
		if status != "failed" || attempts != 10 {
			t.Fatalf("status=%s attempts=%d", status, attempts)
		}
	})

	t.Run("gives up after a day", func(t *testing.T) {
		id := queue("too-old")
		if _, err := db.Exec(`UPDATE transactional_emails SET created_at=NOW()-INTERVAL '25 hours' WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
		handler := &EmailHandler{db: db, cfg: &config.Config{}, emailProvider: &scriptedProvider{errs: []error{&types.TooManyRequestsException{}}}}
		if err := handler.RecoverPending(ctx); err != nil {
			t.Fatal(err)
		}
		var status string
		if err := db.QueryRow(`SELECT status FROM transactional_emails WHERE id=$1`, id).Scan(&status); err != nil || status != "failed" {
			t.Fatalf("status=%s %v", status, err)
		}
	})

	t.Run("scheduled send measures age from scheduled_for", func(t *testing.T) {
		// Created three days ago, scheduled two days after creation: due for a day only.
		id := queue("scheduled")
		if _, err := db.Exec(`UPDATE transactional_emails SET created_at=NOW()-INTERVAL '3 days',scheduled_for=NOW()-INTERVAL '1 day'+INTERVAL '1 minute' WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
		handler := &EmailHandler{db: db, cfg: &config.Config{}, emailProvider: &scriptedProvider{errs: []error{&types.TooManyRequestsException{}}}}
		if err := handler.RecoverPending(ctx); err != nil {
			t.Fatal(err)
		}
		var status string
		var attempts int
		if err := db.QueryRow(`SELECT status,send_attempts FROM transactional_emails WHERE id=$1`, id).Scan(&status, &attempts); err != nil {
			t.Fatal(err)
		}
		if status != "queued" || attempts != 1 {
			t.Fatalf("status=%s attempts=%d, want a deferral", status, attempts)
		}
	})

	t.Run("give-up is atomic and never left sending", func(t *testing.T) {
		id := queue("atomic")
		if _, err := db.Exec(`UPDATE transactional_emails SET send_attempts=9 WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
		handler := &EmailHandler{db: db, cfg: &config.Config{}, emailProvider: &scriptedProvider{errs: []error{&types.TooManyRequestsException{}}}}
		if err := handler.RecoverPending(ctx); err != nil {
			t.Fatal(err)
		}
		var status, reason string
		var attempts, failedEvents, outbox int
		if err := db.QueryRow(`SELECT status,send_attempts,last_deferral_reason,
			(SELECT count(*) FROM transactional_delivery_events WHERE email_id=$1 AND event_type='failed'),
			(SELECT count(*) FROM webhook_events WHERE event_type='email.failed' AND org_id=$2 AND dedupe_key='failed:'||transactional_emails.uuid::text)
			FROM transactional_emails WHERE id=$1`, id, org).Scan(&status, &attempts, &reason, &failedEvents, &outbox); err != nil {
			t.Fatal(err)
		}
		if status != "failed" || attempts != 10 || reason == "" || failedEvents != 1 || outbox != 1 {
			t.Fatalf("status=%s attempts=%d reason=%q failedEvents=%d outbox=%d", status, attempts, reason, failedEvents, outbox)
		}
	})

	t.Run("timeout stays unknown", func(t *testing.T) {
		id := queue("timeout")
		fake := &scriptedProvider{errs: []error{context.DeadlineExceeded}}
		handler := &EmailHandler{db: db, cfg: &config.Config{}, emailProvider: fake}
		for i := 0; i < 2; i++ {
			if err := handler.RecoverPending(ctx); err != nil {
				t.Fatal(err)
			}
			makeDue(id)
		}
		var status string
		if err := db.QueryRow(`SELECT status FROM transactional_emails WHERE id=$1`, id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "unknown" || fake.calls != 1 {
			t.Fatalf("status=%s calls=%d", status, fake.calls)
		}
	})

	t.Run("throttle stops the pass and the asynq task succeeds", func(t *testing.T) {
		a, b := queue("first"), queue("second")
		throttle := &types.TooManyRequestsException{}
		fake := &scriptedProvider{errs: []error{throttle, throttle}}
		handler := &EmailHandler{db: db, cfg: &config.Config{}, emailProvider: fake}
		if err := handler.RecoverPending(ctx); err != nil {
			t.Fatal(err)
		}
		if fake.calls != 1 {
			t.Fatalf("pass continued after throttle: %d calls", fake.calls)
		}
		data, err := NewEmailSendPayload(b, org, "from@example.test", []string{"to@example.test"}, "test", "", "hello", "<second@example.test>").Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if err = handler.HandleEmailSend(ctx, asynq.NewTask(TypeEmailSend, data)); err != nil {
			t.Fatal("throttled task must not be retried by asynq:", err)
		}
		var queued int
		if err := db.QueryRow(`SELECT count(*) FROM transactional_emails WHERE id IN ($1,$2) AND status='queued' AND send_attempts=1`, a, b).Scan(&queued); err != nil || queued != 2 || fake.calls != 2 {
			t.Fatalf("queued=%d calls=%d %v", queued, fake.calls, err)
		}
	})
}
