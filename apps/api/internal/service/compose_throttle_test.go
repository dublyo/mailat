package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/dublyo/mailat/api/internal/testutil"
)

// throttlingTestProvider throttles the first n calls and then accepts.
type throttlingTestProvider struct {
	provider.EmailProvider
	mu        sync.Mutex
	throttles int
	calls     int
}

func (p *throttlingTestProvider) SendEmail(context.Context, *provider.EmailMessage) (*provider.SendResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.calls <= p.throttles {
		return nil, &types.TooManyRequestsException{}
	}
	return &provider.SendResult{MessageID: "ses-accepted", Success: true}, nil
}
func (p *throttlingTestProvider) Name() string { return "ses" }

func TestComposeRetriesThrottlesWithinRequest(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	_, user, identity := mailboxFixture(t, db, "throttle.test")
	var slept []time.Duration
	newService := func(p provider.EmailProvider) *ComposeService {
		return &ComposeService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: p, attachments: mailboxTestStorage{}, sleep: func(d time.Duration) { slept = append(slept, d) }}
	}
	mail := func(key string) *ComposeEmail {
		return &ComposeEmail{IdentityID: identity, Subject: "throttled", TextBody: "hello", To: []EmailAddress{{Email: "recipient@example.net"}}, SubmissionKey: key}
	}

	recovering := &throttlingTestProvider{throttles: 2}
	sent, err := newService(recovering).SendEmail(ctx, user, mail("throttle-then-ok"))
	if err != nil || sent.Status != "sent" || sent.Retryable || recovering.calls != 3 {
		t.Fatalf("result=%+v err=%v calls=%d", sent, err, recovering.calls)
	}
	if len(slept) != 2 || slept[0] < 400*time.Millisecond || slept[0] > 600*time.Millisecond || slept[1] < 800*time.Millisecond || slept[1] > 1200*time.Millisecond {
		t.Fatalf("unexpected pauses %v", slept)
	}

	stuck := &throttlingTestProvider{throttles: 100}
	svc := newService(stuck)
	failed, err := svc.SendEmail(ctx, user, mail("throttle-always"))
	if err != nil || failed.Status != "failed" || !failed.Retryable || stuck.calls != 3 {
		t.Fatalf("result=%+v err=%v calls=%d", failed, err, stuck.calls)
	}
	var status string
	if err = db.QueryRow(`SELECT send_status FROM received_emails WHERE uuid=$1`, failed.EmailID).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("outbox status %q %v", status, err)
	}
	replay, err := svc.SendEmail(ctx, user, mail("throttle-always"))
	if err != nil || replay.EmailID != failed.EmailID || replay.Status != "failed" || stuck.calls != 3 {
		t.Fatalf("replay=%+v err=%v calls=%d", replay, err, stuck.calls)
	}
}
