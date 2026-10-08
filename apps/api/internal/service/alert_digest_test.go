package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/testutil"
	"github.com/dublyo/mailat/api/internal/worker"
)

func TestAlertDigestUsesOwnerIdentityOncePerDay(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	org, user, _ := mailboxFixture(t, db, "digest.test")
	if _, err := db.Exec(`UPDATE organizations SET name='<b>Acme</b>' WHERE id=$1`, org); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE users SET role='owner' WHERE id=$1`, user); err != nil {
		t.Fatal(err)
	}
	// An org whose owner has no verified sending identity is skipped.
	var bare, bareOwner int64
	if err := db.QueryRow(`INSERT INTO organizations(name,slug,updated_at) VALUES('Bare','bare',now()) RETURNING id`).Scan(&bare); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO users(org_id,email,password_hash,role,updated_at) VALUES($1,'owner@bare.test','unused','owner',now()) RETURNING id`, bare).Scan(&bareOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO alerts(org_id,type,severity,title,message) VALUES($1,'bounce','critical','a','a'),($1,'bounce','warning','b','b'),($2,'bounce','warning','c','c')`, org, bare); err != nil {
		t.Fatal(err)
	}

	if err := worker.NewScheduledTaskHandler(db, &config.Config{}, nil).HandleAlertDigest(ctx, nil); err != nil {
		t.Fatal("nil sender:", err)
	}

	fake := &mailboxTestProvider{}
	svc := &TransactionalService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: fake}
	handler := worker.NewScheduledTaskHandler(db, &config.Config{WebUrl: "https://mail.digest.test"}, svc.AlertDigestSender())
	key := fmt.Sprintf("alert-digest:%d:%s", org, time.Now().UTC().Format("2006-01-02"))
	if err := handler.HandleAlertDigest(ctx, nil); err != nil {
		t.Fatal(err)
	}
	var id, from string
	if err := db.QueryRow(`SELECT uuid,from_address FROM transactional_emails WHERE org_id=$1 AND idempotency_key=$2`, org, key).Scan(&id, &from); err != nil {
		t.Fatal("digest row:", err)
	}
	waitForTransactionalSend(t, db, id)
	if !strings.Contains(from, "owner@digest.test") {
		t.Fatalf("digest sent from %q", from)
	}
	fake.mu.Lock()
	body := fake.last.HTMLBody
	if !strings.Contains(fake.last.TextBody, "unacknowledged alerts") {
		t.Fatalf("digest has no plain-text alternative: %q", fake.last.TextBody)
	}
	fake.mu.Unlock()
	if !strings.Contains(body, "&lt;b&gt;Acme&lt;/b&gt;") || strings.Contains(body, "<b>Acme") || !strings.Contains(body, "https://mail.digest.test/health") {
		t.Fatalf("digest body not escaped or missing link: %s", body)
	}

	// A replica running again (same counts, then changed counts) sends nothing new.
	if err := handler.HandleAlertDigest(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO alerts(org_id,type,severity,title,message) VALUES($1,'bounce','warning','d','d')`, org); err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleAlertDigest(ctx, nil); err != nil {
		t.Fatal(err)
	}
	var rows, bareRows int
	if err := db.QueryRow(`SELECT count(*) FILTER (WHERE org_id=$1), count(*) FILTER (WHERE org_id=$2) FROM transactional_emails WHERE idempotency_key LIKE 'alert-digest:%'`, org, bare).Scan(&rows, &bareRows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || bareRows != 0 {
		t.Fatalf("digest rows org=%d bare=%d", rows, bareRows)
	}
	fake.mu.Lock()
	calls := fake.calls
	fake.mu.Unlock()
	if calls != 1 {
		t.Fatalf("provider called %d times", calls)
	}
}
