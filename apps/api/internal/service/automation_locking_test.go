package service

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/lib/pq"

	"github.com/dublyo/mailat/api/internal/model"
)

// waitForLockWait waits until another backend of this database is blocked on
// a lock while running a statement containing fragment.
func waitForLockWait(t *testing.T, db *sql.DB, fragment string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()
			AND wait_event_type='Lock' AND strpos(query, $1) > 0`, fragment).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
	}
	t.Fatalf("no backend waits on a lock in %q", fragment)
}

// concurrently runs f in a goroutine; the returned func waits for its error
// and may be called again (tests defer it so f ends before cleanup).
func concurrently(f func() error) func() error {
	done := make(chan struct{})
	var err error
	go func() { err = f(); close(done) }()
	return func() error {
		select {
		case <-done:
			return err
		case <-time.After(15 * time.Second):
			return context.DeadlineExceeded
		}
	}
}

// stepLikeTx opens a transaction that holds contact 100's enrollment the way
// a running step does.
func stepLikeTx(t *testing.T, db *sql.DB) (*sql.Tx, int64, int64, int64) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	var id, automationID, versionID int64
	if err := tx.QueryRow(`SELECT id, automation_id, version_id FROM automation_enrollments WHERE contact_id=100 AND status='active' FOR UPDATE`).
		Scan(&id, &automationID, &versionID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	return tx, id, automationID, versionID
}

func waitingFlow(t *testing.T) (*sql.DB, *AutomationService, *AutomationExecutor, string) {
	t.Helper()
	db, s := newAutomationFixture(t)
	x := NewAutomationExecutor(db, s.cfg, nil)
	w := buildGraph(t, []graphNode{
		{"t", "trigger", map[string]any{"event": "contact.subscribed", "listUuid": autoListA}},
		{"w", "delay", map[string]any{"duration": 1, "unit": "days"}},
		{"a", "action", map[string]any{"action": "add_to_list", "listUuid": autoListB}},
	}, [][3]string{{"t", "w", ""}, {"w", "a", ""}})
	a := publishGraph(t, s, w, "never")
	addAutoContact(t, db, 100, "active", `{}`)
	autoSubscribe(t, db, 1, 100, "api")
	mustRun(t, x.EnrollPending)
	mustRun(t, x.StepDue)
	return db, s, x, a.UUID
}

// TestAutomationArchiveWithInFlightStep: archive waits for a step that holds
// an enrollment and then inserts rows referencing the automation (FK KEY
// SHARE), instead of deadlocking with it.
func TestAutomationArchiveWithInFlightStep(t *testing.T) {
	db, s, _, uuid := waitingFlow(t)
	tx, enrollmentID, automationID, versionID := stepLikeTx(t, db)
	defer tx.Rollback()
	wait := concurrently(func() error { _, _, err := s.ArchiveAutomation(context.Background(), 1, uuid); return err })
	defer func() { tx.Rollback(); wait() }()
	waitForLockWait(t, db, "exit_reason = 'archived'")
	if _, err := tx.Exec(`INSERT INTO automation_step_runs(enrollment_id,automation_id,version_id,node_id,node_type,status,finished_at)
		VALUES($1,$2,$3,'x','action','succeeded',now())`, enrollmentID, automationID, versionID); err != nil {
		t.Fatalf("step insert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := wait(); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if got := enrollmentState(t, db, 100); got != "cancelled/w/archived" {
		t.Fatalf("state: %s", got)
	}
}

// TestAutomationErasureWithInFlightStep: erasure waits for a step holding the
// contact's enrollment that then needs the contact (FK KEY SHARE).
func TestAutomationErasureWithInFlightStep(t *testing.T) {
	db, s, _, _ := waitingFlow(t)
	c100 := contactUUID(t, s, 100)
	tx, _, _, _ := stepLikeTx(t, db)
	defer tx.Rollback()
	wait := concurrently(func() error {
		return NewComplianceService(db, s.cfg).DeleteContactData(context.Background(), 1, ContactActor{UserID: 1}, c100)
	})
	defer func() { tx.Rollback(); wait() }()
	waitForLockWait(t, db, "automation_enrollments")
	if _, err := tx.Exec(`SELECT 1 FROM contacts WHERE id=100 FOR KEY SHARE`); err != nil {
		t.Fatalf("step contact lock: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := wait(); err != nil {
		t.Fatalf("erasure: %v", err)
	}
	count(t, db, 0, `SELECT count(*) FROM contacts WHERE id=100`)
	count(t, db, 0, `SELECT count(*) FROM automation_enrollments`)
}

// TestAutomationActivateBlocksListDelete: a list read by an activation in
// progress cannot be deleted until it commits, and then the guard sees the
// new version.
func TestAutomationActivateBlocksListDelete(t *testing.T) {
	db, s := newAutomationFixture(t)
	ctx := context.Background()
	w := buildGraph(t, []graphNode{
		{"t", "trigger", map[string]any{"event": "contact.subscribed", "listUuid": autoListA}},
		{"a", "action", map[string]any{"action": "add_to_list", "listUuid": autoListB}},
	}, [][3]string{{"t", "a", ""}})
	a, err := s.CreateAutomation(ctx, 1, 1, &model.CreateAutomationRequest{Name: "Flow", Workflow: w})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	d, err := loadAutomationDraft(ctx, tx, 1, a.UUID, true)
	if err != nil {
		t.Fatal(err)
	}
	g, errs, err := compileDraft(ctx, tx, 1, 1, d)
	if err != nil || len(errs) > 0 {
		t.Fatalf("compile: %v %v", errs, err)
	}
	wait := concurrently(func() error { return NewListService(db, s.cfg).DeleteList(ctx, 1, autoListB) })
	defer func() { tx.Rollback(); wait() }()
	waitForLockWait(t, db, "FROM lists WHERE org_id = $1 AND uuid = $2 FOR UPDATE")
	var versionID int64
	if err := tx.QueryRow(`INSERT INTO automation_versions(automation_id,version,trigger_type,trigger_config,workflow,graph_hash,reentry_policy,resource_refs)
		VALUES($1,1,'contact.subscribed','{}','{}',$2,'never',$3) RETURNING id`, d.id, g.Hash, pq.Array(g.Refs)).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE automations SET status='active', published_version_id=$2 WHERE id=$1`, d.id, versionID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var ae *AutomationError
	if err := wait(); !errors.As(err, &ae) {
		t.Fatalf("delete was not refused: %v", err)
	}
	count(t, db, 1, `SELECT count(*) FROM lists WHERE uuid=$1`, autoListB)
}
