package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"net/netip"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
)

// RateRule is a fixed-window limit: at most Limit hits per Window per subject.
type RateRule struct {
	Name   string
	Limit  int
	Window time.Duration
}

// Auth and public endpoint limits (M2 C2). Subjects are client IPs (see
// RateLimitIPKey), lowercased account emails or user IDs depending on the rule.
var (
	RuleLoginIP = RateRule{Name: "login-ip", Limit: 20, Window: 15 * time.Minute}
	// Failed logins only (wrong password or unknown account): a correct
	// password never uses the budget, so knowing an address is not enough to
	// lock its owner out. Strict per address and client IP, loose per address.
	RuleLoginFailure        = RateRule{Name: "login-failure", Limit: 10, Window: 15 * time.Minute}
	RuleLoginAccountFailure = RateRule{Name: "login-account-failure", Limit: 100, Window: 15 * time.Minute}
	RulePinIP               = RateRule{Name: "pin-ip", Limit: 20, Window: 15 * time.Minute}
	RulePinManageUser       = RateRule{Name: "pin-manage-user", Limit: 10, Window: 15 * time.Minute}
	RuleRegisterIP          = RateRule{Name: "register-ip", Limit: 5, Window: time.Hour}
	Rule2FAChallengeIP      = RateRule{Name: "2fa-challenge-ip", Limit: 20, Window: 15 * time.Minute}
	Rule2FAManageUser       = RateRule{Name: "2fa-manage-user", Limit: 10, Window: 15 * time.Minute}
	RuleOAuthIP             = RateRule{Name: "oauth-ip", Limit: 30, Window: 15 * time.Minute}
	RulePublicComplianceIP  = RateRule{Name: "public-compliance-ip", Limit: 60, Window: 15 * time.Minute}
	RuleForwardVerifyIP     = RateRule{Name: "forward-verify-ip", Limit: 20, Window: 15 * time.Minute}
	RuleInviteLookupIP      = RateRule{Name: "invite-lookup-ip", Limit: 20, Window: 15 * time.Minute}
	RuleInviteAcceptIP      = RateRule{Name: "invite-accept-ip", Limit: 10, Window: 15 * time.Minute}
	RuleWebAuthnBeginUser   = RateRule{Name: "webauthn-begin-user", Limit: 10, Window: 15 * time.Minute}
)

// RateLimiter counts hits in Postgres so limits hold across API replicas.
// Stored keys are HMACs; raw IPs and emails never reach the table.
type RateLimiter struct {
	db  *sql.DB
	key []byte
}

func NewRateLimiter(db *sql.DB, cfg *config.Config) *RateLimiter {
	k := sha256.Sum256([]byte("mailat-rate-limit:" + cfg.JWTSecret))
	return &RateLimiter{db: db, key: k[:]}
}

func (l *RateLimiter) storageKey(rule RateRule, subject string) string {
	m := hmac.New(sha256.New, l.key)
	m.Write([]byte(rule.Name + ":" + subject))
	return hex.EncodeToString(m.Sum(nil))
}

// Allow records one hit and reports whether it is within the rule. When it is
// not, retryAfter is the time left in the current window.
func (l *RateLimiter) Allow(ctx context.Context, rule RateRule, subject string) (bool, time.Duration, error) {
	return l.allowN(ctx, l.db, rule, subject, 1)
}

// allowN records n hits at once, or none when they would exceed the rule, so a
// multi-recipient send is counted atomically. q may be a transaction, letting
// several rules commit or roll back together.
func (l *RateLimiter) allowN(ctx context.Context, q queryer, rule RateRule, subject string, n int) (bool, time.Duration, error) {
	window := int64(rule.Window / time.Second)
	if window < 1 || rule.Limit < 1 || n < 1 {
		return false, 0, fmt.Errorf("invalid rate rule %s", rule.Name)
	}
	var err error
	if n <= rule.Limit {
		var hits int
		err = q.QueryRowContext(ctx, `
		WITH w AS (SELECT to_timestamp(floor(extract(epoch FROM now())/$3::int)*$3::int) AS ws)
		INSERT INTO auth_rate_limits(key,window_start,hits) SELECT $1,ws,$4::int FROM w
		ON CONFLICT(key) DO UPDATE SET window_start=EXCLUDED.window_start,
			hits=CASE WHEN auth_rate_limits.window_start=EXCLUDED.window_start THEN auth_rate_limits.hits+$4::int ELSE $4::int END
		WHERE auth_rate_limits.window_start<>EXCLUDED.window_start OR auth_rate_limits.hits<=$2::int-$4::int
		RETURNING hits`, l.storageKey(rule, subject), rule.Limit, window, n).Scan(&hits)
		if err == nil {
			return true, 0, nil
		}
		if err != sql.ErrNoRows {
			return false, 0, err
		}
	}
	var seconds float64
	err = q.QueryRowContext(ctx, `SELECT extract(epoch FROM to_timestamp(floor(extract(epoch FROM now())/$1::int)*$1::int) + make_interval(secs => $1::int) - now())`, window).Scan(&seconds)
	if err != nil {
		return false, 0, err
	}
	return false, time.Duration(seconds * float64(time.Second)), nil
}

// Peek reports whether rule/subject still has budget in the current window
// without recording a hit. Pair it with Record for failure-only limits.
func (l *RateLimiter) Peek(ctx context.Context, rule RateRule, subject string) (bool, time.Duration, error) {
	window := int64(rule.Window / time.Second)
	if window < 1 || rule.Limit < 1 {
		return false, 0, fmt.Errorf("invalid rate rule %s", rule.Name)
	}
	var hits int
	var seconds float64
	err := l.db.QueryRowContext(ctx, `
		WITH w AS (SELECT to_timestamp(floor(extract(epoch FROM now())/$2::int)*$2::int) AS ws)
		SELECT COALESCE((SELECT a.hits FROM auth_rate_limits a, w WHERE a.key=$1 AND a.window_start=w.ws),0),
			extract(epoch FROM (SELECT ws FROM w) + make_interval(secs => $2::int) - now())`,
		l.storageKey(rule, subject), window).Scan(&hits, &seconds)
	if err != nil {
		return false, 0, err
	}
	if hits >= rule.Limit {
		return false, time.Duration(seconds * float64(time.Second)), nil
	}
	return true, 0, nil
}

// Refund gives back one hit recorded by Allow in the current window, never
// going below zero. Failure-only limits reserve a hit before the check (so
// concurrent requests cannot exceed the limit) and refund it when the check
// passed. A refund after the window rolled over takes one hit off the new
// window, at most one per refunded request.
func (l *RateLimiter) Refund(ctx context.Context, rule RateRule, subject string) error {
	window := int64(rule.Window / time.Second)
	if window < 1 {
		return fmt.Errorf("invalid rate rule %s", rule.Name)
	}
	_, err := l.db.ExecContext(ctx, `UPDATE auth_rate_limits SET hits=hits-1
		WHERE key=$1 AND hits>0 AND window_start=to_timestamp(floor(extract(epoch FROM now())/$2::int)*$2::int)`,
		l.storageKey(rule, subject), window)
	return err
}

// RateLimitIPKey is the rate-limit subject for a client address: an IPv4
// address as is, an IPv6 address as its /64 network, because one client
// usually holds a whole /64 and could otherwise rotate through 2^64 buckets.
// IPv4-mapped IPv6 addresses count as IPv4. Anything that is not an IP
// address is returned unchanged.
func RateLimitIPKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.Unmap().WithZone("")
	if addr.Is4() {
		return addr.String()
	}
	prefix, err := addr.Prefix(64)
	if err != nil {
		return ip
	}
	return prefix.String()
}

// Record counts one hit for rule/subject (a failure, for failure-only
// limits). The count stops at the limit, which Peek then reports.
func (l *RateLimiter) Record(ctx context.Context, rule RateRule, subject string) error {
	_, _, err := l.Allow(ctx, rule, subject)
	return err
}

// RunCleanup deletes expired windows hourly until ctx is cancelled.
func (l *RateLimiter) RunCleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := l.Cleanup(ctx); err != nil && ctx.Err() == nil {
				log.Printf("rate limit cleanup failed: %v", err)
			}
		}
	}
}

func (l *RateLimiter) Cleanup(ctx context.Context) error {
	_, err := l.db.ExecContext(ctx, `DELETE FROM auth_rate_limits WHERE window_start<now()-interval '1 day'`)
	return err
}
