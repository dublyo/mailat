package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/testutil"
)

func TestReserveMonthlySendsBatches(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	cfg := &config.Config{}
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,monthly_email_limit,updated_at) VALUES(1,'Quota','quota',5,now()),(2,'Free','free',0,now())`); err != nil {
		t.Fatal(err)
	}
	attempts := func() (n int64) {
		t.Helper()
		if err := db.QueryRow(`SELECT COALESCE(SUM(attempts),0) FROM organization_send_usage WHERE org_id=1`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if got, err := reserveMonthlySends(ctx, db, cfg, 1, 3); err != nil || got != 3 {
		t.Fatalf("first batch: %d %v", got, err)
	}
	if got, err := reserveMonthlySends(ctx, db, cfg, 1, 3); err != nil || got != 2 {
		t.Fatalf("partial batch: %d %v", got, err)
	}
	if got, err := reserveMonthlySends(ctx, db, cfg, 1, 1); err != nil || got != 0 {
		t.Fatalf("exhausted: %d %v", got, err)
	}
	if err := reserveMonthlySend(ctx, db, cfg, 1); err == nil || err.Error() != "monthly application send quota exceeded" {
		t.Fatalf("single-send error text changed: %v", err)
	}
	if n := attempts(); n != 5 {
		t.Fatalf("attempts = %d", n)
	}

	// A rolled-back reservation charges nothing; a refund frees quota and floors at 0.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = refundMonthlySendsTx(ctx, tx, 1, 2); err != nil {
		t.Fatal(err)
	}
	if got, err := reserveMonthlySendsTx(ctx, tx, cfg, 1, 4); err != nil || got != 2 {
		t.Fatalf("after refund: %d %v", got, err)
	}
	tx.Rollback()
	if n := attempts(); n != 5 {
		t.Fatalf("rollback charged quota: %d", n)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = refundMonthlySendsTx(ctx, tx, 1, 50); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := attempts(); n != 0 {
		t.Fatalf("refund did not floor at zero: %d", n)
	}

	// No limit, or limits disabled, grants everything without tracking usage.
	if got, err := reserveMonthlySends(ctx, db, cfg, 2, 7); err != nil || got != 7 {
		t.Fatalf("unlimited org: %d %v", got, err)
	}
	if got, err := reserveMonthlySends(ctx, db, &config.Config{DisableAppLimits: true}, 1, 9); err != nil || got != 9 {
		t.Fatalf("limits disabled: %d %v", got, err)
	}
	if got, err := reserveMonthlySends(ctx, db, cfg, 1, 0); err != nil || got != 0 {
		t.Fatalf("zero request: %d %v", got, err)
	}
}

func TestReserveMonthlySendsConcurrentNeverExceedsLimit(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	cfg := &config.Config{}
	const limit = 25
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,monthly_email_limit,updated_at) VALUES(1,'Race','race',$1,now())`, limit); err != nil {
		t.Fatal(err)
	}
	var granted atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				n, err := reserveMonthlySends(ctx, db, cfg, 1, 3)
				granted.Add(n)
				errs <- err
				return
			}
			if err := reserveMonthlySend(ctx, db, cfg, 1); err == nil {
				granted.Add(1)
			}
			errs <- nil
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var attempts int64
	if err := db.QueryRow(`SELECT attempts FROM organization_send_usage WHERE org_id=1`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	// 10 batches of 3 plus 10 singles request 40, so the limit must bind exactly.
	if granted.Load() != limit || attempts != limit {
		t.Fatalf("granted=%d attempts=%d, want %d", granted.Load(), attempts, limit)
	}
}
