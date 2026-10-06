package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
)

// RateRule is a fixed-window limit: at most Limit hits per Window per subject.
type RateRule struct {
	Name   string
	Limit  int
	Window time.Duration
}

// Auth and public endpoint limits (M2 C2). Subjects are client IPs, lowercased
// account emails or user IDs depending on the rule.
var (
	RuleLoginIP            = RateRule{Name: "login-ip", Limit: 20, Window: 15 * time.Minute}
	RuleLoginAccount       = RateRule{Name: "login-account", Limit: 10, Window: 15 * time.Minute}
	RuleRegisterIP         = RateRule{Name: "register-ip", Limit: 5, Window: time.Hour}
	Rule2FAChallengeIP     = RateRule{Name: "2fa-challenge-ip", Limit: 20, Window: 15 * time.Minute}
	Rule2FAManageUser      = RateRule{Name: "2fa-manage-user", Limit: 10, Window: 15 * time.Minute}
	RuleOAuthIP            = RateRule{Name: "oauth-ip", Limit: 30, Window: 15 * time.Minute}
	RulePublicComplianceIP = RateRule{Name: "public-compliance-ip", Limit: 60, Window: 15 * time.Minute}
	RuleForwardVerifyIP    = RateRule{Name: "forward-verify-ip", Limit: 20, Window: 15 * time.Minute}
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
	window := int64(rule.Window / time.Second)
	if window < 1 || rule.Limit < 1 {
		return false, 0, fmt.Errorf("invalid rate rule %s", rule.Name)
	}
	var hits int
	err := l.db.QueryRowContext(ctx, `
		WITH w AS (SELECT to_timestamp(floor(extract(epoch FROM now())/$3::int)*$3::int) AS ws)
		INSERT INTO auth_rate_limits(key,window_start,hits) SELECT $1,ws,1 FROM w
		ON CONFLICT(key) DO UPDATE SET window_start=EXCLUDED.window_start,
			hits=CASE WHEN auth_rate_limits.window_start=EXCLUDED.window_start THEN auth_rate_limits.hits+1 ELSE 1 END
		WHERE auth_rate_limits.window_start<>EXCLUDED.window_start OR auth_rate_limits.hits<$2
		RETURNING hits`, l.storageKey(rule, subject), rule.Limit, window).Scan(&hits)
	if err == nil {
		return true, 0, nil
	}
	if err != sql.ErrNoRows {
		return false, 0, err
	}
	var seconds float64
	err = l.db.QueryRowContext(ctx, `SELECT extract(epoch FROM to_timestamp(floor(extract(epoch FROM now())/$1::int)*$1::int) + make_interval(secs => $1::int) - now())`, window).Scan(&seconds)
	if err != nil {
		return false, 0, err
	}
	return false, time.Duration(seconds * float64(time.Second)), nil
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
