package eventoutbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

type DBTX interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
type Event struct {
	Type                      string
	OrgID, UserID, IdentityID int64
	MessageUUID, DedupeKey    string
	Data                      map[string]any
}
type Envelope struct {
	Version   string         `json:"version"`
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	CreatedAt time.Time      `json:"createdAt"`
	Data      map[string]any `json:"data"`
}

func CanonicalType(kind string) string {
	switch kind {
	case "email_received":
		return "email.received"
	case "email_sent":
		return "email.sent"
	case "bounce_received":
		return "email.bounced"
	case "complaint_received":
		return "email.complained"
	default:
		return strings.ReplaceAll(kind, "_", ".")
	}
}
func aliases(kind string) []string {
	values := []string{kind, strings.ReplaceAll(kind, ".", "_")}
	switch kind {
	case "email.bounced":
		values = append(values, "bounce_received")
	case "email.complained":
		values = append(values, "complaint_received")
	}
	return values
}
func MatchFilters(filters, data map[string]any) bool {
	for k, v := range filters {
		actual, ok := data[k]
		if !ok || fmt.Sprint(actual) != fmt.Sprint(v) {
			return false
		}
	}
	return true
}

// Emit must be called with the source transaction when the event represents a
// persisted mail mutation. A crash after commit cannot lose its delivery intent.
func Emit(ctx context.Context, q DBTX, event Event) error {
	if err := prepareEvent(ctx, q, &event); err != nil {
		return err
	}
	id, err := insertEvent(ctx, q, event)
	if err != nil || id == "" {
		return err
	}
	rows, err := q.QueryContext(ctx, `SELECT id,0::bigint,'{}'::jsonb FROM webhooks WHERE org_id=$1 AND active=true AND user_id=$2 AND events && $3::text[] UNION ALL SELECT 0::bigint,id,COALESCE(filters,'{}'::jsonb) FROM webhook_triggers WHERE org_id=$1 AND active=true AND user_id=$2 AND trigger_type=ANY($3::text[])`, event.OrgID, event.UserID, pq.Array(aliases(CanonicalType(event.Type))))
	if err != nil {
		return err
	}
	type target struct {
		webhook, trigger int64
		filters          map[string]any
	}
	var targets []target
	for rows.Next() {
		var t target
		var raw []byte
		if err := rows.Scan(&t.webhook, &t.trigger, &raw); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal(raw, &t.filters); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	data := eventData(event)
	for _, t := range targets {
		if !MatchFilters(t.filters, data) {
			continue
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO webhook_deliveries(event_id,webhook_id,trigger_id) VALUES($1,NULLIF($2,0),NULLIF($3,0)) ON CONFLICT DO NOTHING`, id, t.webhook, t.trigger); err != nil {
			return err
		}
	}
	return nil
}
func eventData(event Event) map[string]any {
	data := make(map[string]any, len(event.Data)+3)
	for k, v := range event.Data {
		data[k] = v
	}
	if event.MessageUUID != "" {
		data["messageUuid"] = event.MessageUUID
	}
	if event.IdentityID != 0 {
		data["identityId"] = event.IdentityID
	}
	return data
}
func insertEvent(ctx context.Context, q DBTX, event Event) (string, error) {
	if event.OrgID <= 0 || event.Type == "" {
		return "", fmt.Errorf("event organization and type are required")
	}
	id := uuid.NewString()
	key := event.DedupeKey
	if key == "" {
		key = id
	}
	payload, err := json.Marshal(Envelope{Version: "1", ID: id, Type: CanonicalType(event.Type), CreatedAt: time.Now().UTC(), Data: eventData(event)})
	if err != nil {
		return "", err
	}
	err = q.QueryRowContext(ctx, `INSERT INTO webhook_events(id,org_id,user_id,event_type,dedupe_key,payload) VALUES($1,$2,NULLIF($3,0),$4,$5,$6) ON CONFLICT(org_id,event_type,dedupe_key) DO NOTHING RETURNING id`, id, event.OrgID, event.UserID, CanonicalType(event.Type), key, string(payload)).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

// QueueTarget stores a test or a legacy already-selected destination using the
// same envelope and delivery engine as automatic events.
func QueueTarget(ctx context.Context, q DBTX, event Event, webhookID, triggerID int64) (string, string, error) {
	if err := prepareEvent(ctx, q, &event); err != nil {
		return "", "", err
	}
	id, err := insertEvent(ctx, q, event)
	if err != nil {
		return "", "", err
	}
	if id == "" {
		var delivery string
		err = q.QueryRowContext(ctx, `SELECT e.id,d.id FROM webhook_events e JOIN webhook_deliveries d ON d.event_id=e.id WHERE e.org_id=$1 AND e.event_type=$2 AND e.dedupe_key=$3 AND (d.webhook_id=NULLIF($4,0) OR d.trigger_id=NULLIF($5,0))`, event.OrgID, CanonicalType(event.Type), event.DedupeKey, webhookID, triggerID).Scan(&id, &delivery)
		return id, delivery, err
	}
	var delivery string
	err = q.QueryRowContext(ctx, `INSERT INTO webhook_deliveries(event_id,webhook_id,trigger_id) VALUES($1,NULLIF($2,0),NULLIF($3,0)) RETURNING id`, id, webhookID, triggerID).Scan(&delivery)
	return id, delivery, err
}

var Types = []string{"contact.subscribed", "email.received", "email.sent", "email.delivered", "email.failed", "email.unknown", "email.bounced", "email.complained"}

func KnownType(kind string) bool {
	for _, v := range Types {
		if CanonicalType(kind) == v {
			return true
		}
	}
	return false
}

// Legacy non-mail callers without an attributable owner cannot fan out private data.
func EmitLegacy(ctx context.Context, db *sql.DB, orgID int64, kind string, data map[string]any) error {
	var userID int64
	var messageUUID string
	if v, ok := data["userId"]; ok {
		fmt.Sscan(fmt.Sprint(v), &userID)
	}
	if v, ok := data["messageUuid"].(string); ok {
		messageUUID = v
	}
	if userID == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = Emit(ctx, tx, Event{Type: kind, OrgID: orgID, UserID: userID, MessageUUID: messageUUID, Data: data}); err != nil {
		return err
	}
	return tx.Commit()
}

// Public identity UUIDs let consumers filter without depending on database IDs.
// Source code cannot accidentally attribute an identity to a different user.
func prepareEvent(ctx context.Context, q DBTX, event *Event) error {
	if event.IdentityID == 0 {
		return nil
	}
	var identityUUID string
	if err := q.QueryRowContext(ctx, `SELECT i.uuid::text FROM identities i JOIN users u ON u.id=i.user_id WHERE i.id=$1 AND i.user_id=$2 AND u.org_id=$3`, event.IdentityID, event.UserID, event.OrgID).Scan(&identityUUID); err != nil {
		return fmt.Errorf("event identity ownership invalid: %w", err)
	}
	event.Data = eventData(*event)
	event.Data["identityUuid"] = identityUUID
	return nil
}
