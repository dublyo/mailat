package service

import (
	"context"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/testutil"
)

func TestRateLimiterWindowsAndCleanup(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	l := NewRateLimiter(db, &config.Config{JWTSecret: "rate-limit-fixture-secret-0123456789"})
	rule := RateRule{Name: "fixture", Limit: 3, Window: time.Hour}
	for i := 0; i < 3; i++ {
		if ok, _, err := l.Allow(ctx, rule, "203.0.113.7"); err != nil || !ok {
			t.Fatalf("hit %d refused: %v", i+1, err)
		}
	}
	ok, retry, err := l.Allow(ctx, rule, "203.0.113.7")
	if err != nil || ok {
		t.Fatalf("limit not enforced: ok=%v err=%v", ok, err)
	}
	if retry <= 0 || retry > time.Hour {
		t.Fatalf("retryAfter %v outside the window", retry)
	}
	// Subjects and rules are independent buckets.
	if ok, _, _ := l.Allow(ctx, rule, "203.0.113.8"); !ok {
		t.Fatal("other subject limited")
	}
	if ok, _, _ := l.Allow(ctx, RateRule{Name: "other", Limit: 1, Window: time.Hour}, "203.0.113.7"); !ok {
		t.Fatal("other rule limited")
	}
	var raw int
	db.QueryRow(`SELECT count(*) FROM auth_rate_limits WHERE key LIKE '%203.0.113%'`).Scan(&raw)
	if raw != 0 {
		t.Fatal("raw subject stored")
	}
	// The next window starts from one hit again.
	if _, err = db.Exec(`UPDATE auth_rate_limits SET window_start=window_start-interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := l.Allow(ctx, rule, "203.0.113.7"); err != nil || !ok {
		t.Fatal("next window still limited", err)
	}
	var hits int
	db.QueryRow(`SELECT hits FROM auth_rate_limits WHERE key=$1`, l.storageKey(rule, "203.0.113.7")).Scan(&hits)
	if hits != 1 {
		t.Fatalf("new window hits=%d", hits)
	}
	if _, err = db.Exec(`UPDATE auth_rate_limits SET window_start=now()-interval '2 days' WHERE key<>$1`, l.storageKey(rule, "203.0.113.7")); err != nil {
		t.Fatal(err)
	}
	if err = l.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	var left int
	db.QueryRow(`SELECT count(*) FROM auth_rate_limits`).Scan(&left)
	if left != 1 {
		t.Fatalf("cleanup left %d rows", left)
	}
}

func TestRegistrationPolicy(t *testing.T) {
	for _, c := range []struct {
		email, password string
		ok              bool
	}{
		{"Owner@Example.com", "long-enough", true},
		{"owner@example.com", "short", false},
		{"owner@example.com", string(make([]byte, 73)), false},
		{"not-an-address", "long-enough", false},
		{"Owner <owner@example.com>", "long-enough", false},
		{"a@b.c, d@e.f", "long-enough", false},
	} {
		email, err := validateRegistration(c.email, c.password)
		if (err == nil) != c.ok {
			t.Fatalf("%q/%d: err=%v", c.email, len(c.password), err)
		}
		if c.ok && email != "owner@example.com" {
			t.Fatalf("email not normalized: %q", email)
		}
	}
}
