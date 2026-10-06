package service

import (
	"context"
	"testing"

	"github.com/dublyo/mailat/api/internal/eventoutbox"
	"github.com/dublyo/mailat/api/internal/model"
)

func TestAutomationWebhookStep(t *testing.T) {
	db, s := newAutomationFixture(t)
	x := NewAutomationExecutor(db, s.cfg, nil)
	w := buildGraph(t, []graphNode{
		{"t", "trigger", map[string]any{"event": "contact.subscribed", "listUuid": autoListA}},
		{"h", "webhook", map[string]any{"webhookUuid": autoWebhook}},
	}, [][3]string{{"t", "h", ""}})
	a := publishGraph(t, s, w, "never")
	addAutoContact(t, db, 100, "active", `{}`)
	addAutoContact(t, db, 101, "active", `{}`)
	autoSubscribe(t, db, 1, 100, "api")
	mustRun(t, x.EnrollPending)
	mustRun(t, x.StepDue)

	if got := enrollmentState(t, db, 100) + " " + stepRuns(t, db, 100); got != "completed/h/ h:succeeded:queued" {
		t.Fatalf("webhook step: %s", got)
	}
	var enrUUID string
	if err := db.QueryRow(`SELECT uuid::text FROM automation_enrollments WHERE contact_id=100`).Scan(&enrUUID); err != nil {
		t.Fatal(err)
	}
	count(t, db, 1, `SELECT count(*) FROM webhook_events e JOIN automation_step_runs r ON r.webhook_event_id=e.id
		WHERE e.event_type='automation.webhook' AND e.user_id=1 AND e.dedupe_key=$1
		AND e.payload->'data'->>'automationUuid'=$2 AND e.payload->'data'->>'email'='c100@example.net' AND e.payload->'data'->>'nodeId'='h'`,
		"automation:"+enrUUID+":h", a.UUID)
	count(t, db, 1, `SELECT count(*) FROM webhook_deliveries WHERE webhook_id=1 AND trigger_id IS NULL`)

	// A replayed step (lost step run) reuses the event and its delivery.
	mustExec(t, db, `DELETE FROM automation_step_runs; UPDATE automation_enrollments SET status='active', next_run_at=now()-interval '1 second' WHERE contact_id=100`)
	mustRun(t, x.StepDue)
	count(t, db, 1, `SELECT count(*) FROM webhook_events`)
	count(t, db, 1, `SELECT count(*) FROM webhook_deliveries`)
	count(t, db, 1, `SELECT count(*) FROM automation_step_runs WHERE webhook_event_id IS NOT NULL`)

	// A webhook deactivated after publish fails the step permanently.
	mustExec(t, db, `UPDATE webhooks SET active=false WHERE id=1`)
	autoSubscribe(t, db, 1, 101, "api")
	mustRun(t, x.EnrollPending)
	mustRun(t, x.StepDue)
	count(t, db, 1, `SELECT count(*) FROM automation_enrollments WHERE contact_id=101 AND status='failed' AND error_message LIKE '%webhook%no longer available'`)

	// automation.webhook can be targeted but never subscribed to.
	if eventoutbox.KnownType("automation.webhook") {
		t.Fatal("automation.webhook must not be subscribable")
	}
	if _, err := NewWebhookService(db, s.cfg).ForUser(1).CreateWebhook(context.Background(), 1, &model.CreateWebhookRequest{
		Name: "x", URL: "https://hooks.example.net/z", Events: []string{"automation.webhook"}}); err == nil {
		t.Fatal("subscribing to automation.webhook was accepted")
	}
}
