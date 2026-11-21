package service

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/provider"
)

// User-initiated outbound limits. Unlike the monthly organization quota
// (send_limits.go) these ignore DISABLE_APP_LIMITS: they cap how many
// recipients one person, or one API key, can reach per fixed UTC hour and day,
// so a stolen session or leaked key cannot burn the SES account's reputation.
//
// Only HTTP controllers opt a send in, with WithSendQuota. System mail (invites,
// verification links, signup confirmations, alert digests, auto-replies,
// forwards) and campaign or automation delivery never carry a subject and are
// not counted here; they have their own caps.

// SendQuotaSubject is who a user-initiated send counts against. API-key
// requests count per key (APIKeyID > 0) and not toward the owner's session limit.
type SendQuotaSubject struct {
	UserID   int64
	APIKeyID int64
}

type sendQuotaContextKey struct{}

// WithSendQuota marks ctx as a user-initiated send by subject.
func WithSendQuota(ctx context.Context, subject SendQuotaSubject) context.Context {
	return context.WithValue(ctx, sendQuotaContextKey{}, subject)
}

func sendQuotaSubject(ctx context.Context) (SendQuotaSubject, bool) {
	s, _ := ctx.Value(sendQuotaContextKey{}).(SendQuotaSubject)
	return s, s.UserID > 0 || s.APIKeyID > 0
}

// withoutSendQuota is used once a whole request (a batch) has been reserved,
// so its individual messages are not counted a second time.
func withoutSendQuota(ctx context.Context) context.Context {
	return context.WithValue(ctx, sendQuotaContextKey{}, SendQuotaSubject{})
}

// SendLimitError refuses a send before anything is recorded or attempted.
// RetryAfter is zero when waiting would not help (more recipients than a window allows).
type SendLimitError struct {
	Message    string
	RetryAfter time.Duration
}

func (e *SendLimitError) Error() string { return e.Message }

// sesHeadroomFraction of the SES 24-hour quota is the most user and API-key
// sends may use; the remainder stays available for system mail.
const sesHeadroomFraction = 0.9

const sendQuotaCacheTTL = 5 * time.Minute

type sendQuotaReader interface {
	GetSendQuota(ctx context.Context) (*provider.SendQuota, error)
}

// SendLimiter enforces the per-user and per-API-key recipient windows and the
// SES headroom guard. A nil *SendLimiter allows everything.
type SendLimiter struct {
	rates     *RateLimiter
	userRules []RateRule
	keyRules  []RateRule
	quota     sendQuotaReader // nil without SES; then only the windows apply
	now       func() time.Time

	mu       sync.Mutex
	quotaAt  time.Time
	cachedSQ *provider.SendQuota
}

// NewSendLimiter builds the limiter from SEND_LIMIT_* settings. quota may be
// nil (no SES); a zero limit disables that window.
func NewSendLimiter(rates *RateLimiter, cfg *config.Config, quota sendQuotaReader) *SendLimiter {
	rules := func(prefix string, hour, day int) []RateRule {
		var out []RateRule
		if hour > 0 {
			out = append(out, RateRule{Name: prefix + "-hour", Limit: hour, Window: time.Hour})
		}
		if day > 0 {
			out = append(out, RateRule{Name: prefix + "-day", Limit: day, Window: 24 * time.Hour})
		}
		return out
	}
	return &SendLimiter{
		rates:     rates,
		userRules: rules("send-user", cfg.SendLimitUserPerHour, cfg.SendLimitUserPerDay),
		keyRules:  rules("send-key", cfg.SendLimitKeyPerHour, cfg.SendLimitKeyPerDay),
		quota:     quota,
		now:       time.Now,
	}
}

// Reserve counts recipients against the subject in ctx, in its own
// transaction, or returns a *SendLimitError. Without a subject it does nothing.
// A reservation is kept even if the send later fails validation or delivery.
func (l *SendLimiter) Reserve(ctx context.Context, recipients int) error {
	if l == nil || recipients <= 0 {
		return nil
	}
	subject, ok := sendQuotaSubject(ctx)
	if !ok {
		return nil
	}
	// The SES check may call AWS, so it runs before a connection is held.
	if err := l.checkSESHeadroom(ctx); err != nil {
		return err
	}
	tx, err := l.rates.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("reserve send limit: %w", err)
	}
	defer tx.Rollback()
	if err = l.reserveWindows(ctx, tx, subject, recipients); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("reserve send limit: %w", err)
	}
	return nil
}

// ReserveTx is Reserve inside the caller's transaction: a refusal or a later
// rollback leaves no partial count, as long as the caller rolls back on error.
func (l *SendLimiter) ReserveTx(ctx context.Context, tx *sql.Tx, recipients int) error {
	if l == nil || recipients <= 0 {
		return nil
	}
	if err := l.headroom(ctx); err != nil {
		return err
	}
	return l.reserveInTx(ctx, tx, recipients)
}

// headroom is Reserve's SES headroom check alone, for callers that count with
// reserveInTx inside a transaction they open afterwards: the check may call
// AWS, so it runs before a connection is held. Without a subject it does nothing.
func (l *SendLimiter) headroom(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if _, ok := sendQuotaSubject(ctx); !ok {
		return nil
	}
	return l.checkSESHeadroom(ctx)
}

// reserveInTx counts recipients in the caller's transaction without the SES
// check (the caller ran headroom first). A send that claims an idempotency key
// reserves right after a successful claim, so a concurrent request with the
// same key, which loses the claim, counts nothing.
func (l *SendLimiter) reserveInTx(ctx context.Context, tx *sql.Tx, recipients int) error {
	if l == nil || recipients <= 0 {
		return nil
	}
	subject, ok := sendQuotaSubject(ctx)
	if !ok {
		return nil
	}
	return l.reserveWindows(ctx, tx, subject, recipients)
}

func (l *SendLimiter) reserveWindows(ctx context.Context, tx *sql.Tx, subject SendQuotaSubject, recipients int) error {
	rules, id := l.userRules, subject.UserID
	if subject.APIKeyID > 0 {
		rules, id = l.keyRules, subject.APIKeyID
	}
	// Windows are taken in a fixed order (hour, then day), so concurrent
	// reservations queue on the same rows instead of deadlocking.
	for _, rule := range rules {
		allowed, retryAfter, err := l.rates.allowN(ctx, tx, rule, strconv.FormatInt(id, 10), recipients)
		if err != nil {
			return fmt.Errorf("reserve send limit: %w", err)
		}
		if !allowed {
			return sendLimitRefusal(rule, recipients, retryAfter)
		}
	}
	return nil
}

func sendLimitRefusal(rule RateRule, recipients int, retryAfter time.Duration) *SendLimitError {
	period := "hour"
	if rule.Window >= 24*time.Hour {
		period = "day"
	}
	if recipients > rule.Limit {
		return &SendLimitError{Message: fmt.Sprintf("Sending limit reached: %d recipients per %s. This send has %d recipients; send it to fewer people at a time.", rule.Limit, period, recipients)}
	}
	return &SendLimitError{Message: fmt.Sprintf("Sending limit reached: %d recipients per %s. Try again %s.", rule.Limit, period, retryPhrase(retryAfter)), RetryAfter: retryAfter}
}

func retryPhrase(d time.Duration) string {
	minutes := int(math.Ceil(d.Minutes()))
	switch {
	case minutes <= 1:
		return "in 1 minute"
	case minutes < 120:
		return fmt.Sprintf("in %d minutes", minutes)
	default:
		return fmt.Sprintf("in %d hours", int(math.Ceil(d.Hours())))
	}
}

// checkSESHeadroom refuses user and API-key sends once the account has used
// 90% of its SES 24-hour quota. An unknown quota allows the send.
func (l *SendLimiter) checkSESHeadroom(ctx context.Context) error {
	q := l.sesQuota(ctx)
	if q == nil || q.Max24HourSend <= 0 || q.SentLast24Hours < sesHeadroomFraction*q.Max24HourSend {
		return nil
	}
	return &SendLimitError{Message: "Sending is paused for now: this server has used 90% of its daily Amazon SES quota, and the rest is kept for system mail. Try again later.", RetryAfter: sendQuotaCacheTTL}
}

// sesQuota returns the SES quota, read at most once per five minutes and never
// per request. A failed read is logged and treated as unknown (fail open) until
// the next refresh.
func (l *SendLimiter) sesQuota(ctx context.Context) *provider.SendQuota {
	if l.quota == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.quotaAt.IsZero() && l.now().Sub(l.quotaAt) < sendQuotaCacheTTL {
		return l.cachedSQ
	}
	qctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	q, err := l.quota.GetSendQuota(qctx)
	l.quotaAt = l.now()
	if err != nil || q == nil {
		log.Printf("send limits: SES quota unavailable, allowing user sends until the next check: %v", err)
		l.cachedSQ = nil
		return nil
	}
	l.cachedSQ = q
	return q
}
