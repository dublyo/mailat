package service

import (
	"context"
	"sync"
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

func TestRateLimitIPKey(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.7":             "203.0.113.7",
		"::ffff:203.0.113.7":      "203.0.113.7",
		"2001:db8:1:2::1":         "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff:1:2:3": "2001:db8:1:2::/64",
		"2001:DB8:1:2:0:0:0:9":    "2001:db8:1:2::/64",
		"fe80::1%eth0":            "fe80::/64",
		"::1":                     "::/64",
		"42":                      "42",
		"":                        "",
		"owner@example.test":      "owner@example.test",
		"owner@example.test|::1":  "owner@example.test|::1",
		"2001:db8:1:3::1":         "2001:db8:1:3::/64",
	} {
		if got := RateLimitIPKey(in); got != want {
			t.Errorf("RateLimitIPKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// Refund gives back a reserved hit in the current window only and never
// goes below zero; Allow stays atomic under concurrency.
func TestRateLimiterRefundAndConcurrentAllow(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	l := NewRateLimiter(db, &config.Config{JWTSecret: "rate-limit-fixture-secret-0123456789"})
	rule := RateRule{Name: "fixture-refund", Limit: 2, Window: time.Hour}
	hits := func() int {
		var n int
		db.QueryRow(`SELECT COALESCE((SELECT hits FROM auth_rate_limits WHERE key=$1),-1)`, l.storageKey(rule, "s")).Scan(&n)
		return n
	}
	// Refunding a subject with no row is a no-op.
	if err := l.Refund(ctx, rule, "s"); err != nil || hits() != -1 {
		t.Fatalf("refund without row: %v hits=%d", err, hits())
	}
	for i := 0; i < 2; i++ {
		if ok, _, err := l.Allow(ctx, rule, "s"); err != nil || !ok {
			t.Fatal("allow", i, err)
		}
	}
	if ok, _, _ := l.Allow(ctx, rule, "s"); ok {
		t.Fatal("limit not enforced")
	}
	if err := l.Refund(ctx, rule, "s"); err != nil || hits() != 1 {
		t.Fatalf("refund: %v hits=%d", err, hits())
	}
	if ok, _, _ := l.Allow(ctx, rule, "s"); !ok {
		t.Fatal("refunded hit not usable")
	}
	for i := 0; i < 4; i++ {
		if err := l.Refund(ctx, rule, "s"); err != nil {
			t.Fatal(err)
		}
	}
	if hits() != 0 {
		t.Fatalf("refund went below zero: %d", hits())
	}
	// A row from an earlier window is not touched.
	l.Allow(ctx, rule, "s")
	db.Exec(`UPDATE auth_rate_limits SET window_start=window_start-interval '1 hour' WHERE key=$1`, l.storageKey(rule, "s"))
	if err := l.Refund(ctx, rule, "s"); err != nil || hits() != 1 {
		t.Fatalf("old window refunded: %v hits=%d", err, hits())
	}

	race := RateRule{Name: "fixture-race", Limit: 10, Window: time.Hour}
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _, err := l.Allow(ctx, race, "203.0.113.7")
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 10 {
		t.Fatalf("concurrent allow accepted %d of limit 10", allowed)
	}
}
