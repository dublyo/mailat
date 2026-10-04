package eventoutbox

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Dispatcher struct {
	DB     *sql.DB
	Client *http.Client
}

func NewDispatcher(db *sql.DB) *Dispatcher { return &Dispatcher{DB: db, Client: SafeClient()} }

type Result struct {
	EventID    string `json:"eventId"`
	DeliveryID string `json:"deliveryId"`
	Status     string `json:"status"`
	HTTPStatus int    `json:"httpStatus"`
	Error      string `json:"error,omitempty"`
}
type claimed struct {
	ID, EventID, Token, URL, Secret string
	Body                            []byte
	Attempts, Replay                int
	WebhookID, TriggerID            int64
	Active                          bool
}

func (d *Dispatcher) claim(ctx context.Context, id string) (*claimed, error) {
	token := uuid.NewString()
	var c claimed
	err := d.DB.QueryRowContext(ctx, `UPDATE webhook_deliveries SET status='delivering',locked_until=NOW()+INTERVAL '1 minute',claim_token=$1,updated_at=NOW() WHERE id=(SELECT id FROM webhook_deliveries WHERE ($2='' OR id::text=$2) AND ((status IN ('pending','retry') AND next_attempt_at<=NOW()) OR (status='delivering' AND locked_until<NOW())) ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING id,event_id,COALESCE(webhook_id,0),COALESCE(trigger_id,0),attempts,replay_count`, token, id).Scan(&c.ID, &c.EventID, &c.WebhookID, &c.TriggerID, &c.Attempts, &c.Replay)
	if err != nil {
		return nil, err
	}
	c.Token = token
	err = d.DB.QueryRowContext(ctx, `SELECT payload FROM webhook_events WHERE id=$1`, c.EventID).Scan(&c.Body)
	if err != nil {
		return nil, err
	}
	if c.WebhookID != 0 {
		err = d.DB.QueryRowContext(ctx, `SELECT w.url,w.secret,w.active AND EXISTS(SELECT 1 FROM users u WHERE u.id=w.user_id AND u.status='active') FROM webhooks w WHERE w.id=$1`, c.WebhookID).Scan(&c.URL, &c.Secret, &c.Active)
	} else {
		err = d.DB.QueryRowContext(ctx, `SELECT w.webhook_url,w.secret,w.active AND EXISTS(SELECT 1 FROM users u WHERE u.id=w.user_id AND u.status='active') FROM webhook_triggers w WHERE w.id=$1`, c.TriggerID).Scan(&c.URL, &c.Secret, &c.Active)
	}
	return &c, err
}

// DispatchOne leases a due delivery. Expired leases are reclaimable after a
// crash; receivers deduplicate the stable event ID because delivery is at least once.
func (d *Dispatcher) DispatchOne(ctx context.Context, id string) (*Result, error) {
	c, err := d.claim(ctx, id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := &Result{EventID: c.EventID, DeliveryID: c.ID, Status: "retry"}
	if c.Secret == "" {
		result.Status = "dead_letter"
		result.Error = "Endpoint signing secret is missing; rotate it before replay"
		return result, d.finish(ctx, c, result, "", 0)
	}
	if !c.Active {
		result.Status = "cancelled"
		result.Error = "Endpoint or its owner is inactive"
		return result, d.finish(ctx, c, result, "", 0)
	}
	started := time.Now()
	responseBody := ""
	if err = ValidateDestination(c.URL); err == nil {
		var req *http.Request
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(c.Body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "Mailat-Webhook/1")
			req.Header.Set("X-Webhook-ID", c.EventID)
			req.Header.Set("X-Webhook-Signature", Sign(c.Body, c.Secret, time.Now()))
			var res *http.Response
			res, err = d.Client.Do(req)
			if res != nil {
				result.HTTPStatus = res.StatusCode
				raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
				res.Body.Close()
				// Receivers may return arbitrary bytes; PostgreSQL text must be valid UTF-8 without NUL.
				responseBody = strings.ReplaceAll(strings.ToValidUTF8(string(raw), "�"), "\x00", "")
			}
		}
	}
	if err != nil {
		result.Error = "Webhook request failed or destination is not permitted"
	} else if result.HTTPStatus < 200 || result.HTTPStatus >= 300 {
		result.Error = fmt.Sprintf("Endpoint returned HTTP %d", result.HTTPStatus)
	} else {
		result.Status = "delivered"
	}
	if result.Status != "delivered" && c.Attempts+1 >= 8 {
		result.Status = "dead_letter"
	}
	return result, d.finish(ctx, c, result, responseBody, time.Since(started))
}
func (d *Dispatcher) finish(ctx context.Context, c *claimed, r *Result, body string, duration time.Duration) error {
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	delays := []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour}
	index := c.Attempts
	if index >= len(delays) {
		index = len(delays) - 1
	}
	next := time.Now().Add(delays[index])
	res, err := tx.ExecContext(ctx, `UPDATE webhook_deliveries SET status=$3,attempts=attempts+1,next_attempt_at=$4,locked_until=NULL,claim_token=NULL,last_http_status=$5,last_error=$6,updated_at=NOW() WHERE id=$1 AND claim_token=$2`, c.ID, c.Token, r.Status, next, r.HTTPStatus, r.Error)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("delivery lease was superseded")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO webhook_delivery_attempts(delivery_id,attempt,replay,http_status,error,response_body,duration_ms) VALUES($1,$2,$3,$4,$5,$6,$7)`, c.ID, c.Attempts+1, c.Replay, r.HTTPStatus, r.Error, body, duration.Milliseconds()); err != nil {
		return err
	}
	if c.WebhookID != 0 {
		_, err = tx.ExecContext(ctx, `UPDATE webhooks SET last_triggered_at=NOW(),success_count=success_count+CASE WHEN $2 THEN 1 ELSE 0 END,failure_count=failure_count+CASE WHEN $2 THEN 0 ELSE 1 END,last_success_at=CASE WHEN $2 THEN NOW() ELSE last_success_at END,last_failure_at=CASE WHEN $2 THEN last_failure_at ELSE NOW() END WHERE id=$1`, c.WebhookID, r.Status == "delivered")
	} else if r.Status == "delivered" {
		_, err = tx.ExecContext(ctx, `UPDATE webhook_triggers SET trigger_count=trigger_count+1,last_triggered_at=NOW() WHERE id=$1`, c.TriggerID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func Run(ctx context.Context, db *sql.DB) {
	d := NewDispatcher(db)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	lastCleanup := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for i := 0; i < 20; i++ {
				result, err := d.DispatchOne(ctx, "")
				if err != nil {
					log.Printf("Webhook outbox will retry: %v", err)
					break
				}
				if result == nil {
					break
				}
			}
			if time.Since(lastCleanup) > time.Hour {
				_, _ = db.ExecContext(ctx, `DELETE FROM webhook_events WHERE id IN(SELECT e.id FROM webhook_events e WHERE e.created_at<NOW()-INTERVAL '30 days' AND NOT EXISTS(SELECT 1 FROM webhook_deliveries d WHERE d.event_id=e.id AND (d.status NOT IN ('delivered','cancelled','dead_letter') OR (d.status='dead_letter' AND d.updated_at>NOW()-INTERVAL '90 days'))) LIMIT 500)`)
				lastCleanup = time.Now()
			}
		}
	}
}

type Delivery struct {
	ID            string          `json:"id"`
	EventID       string          `json:"eventId"`
	Type          string          `json:"type"`
	Status        string          `json:"status"`
	Attempts      int             `json:"attempts"`
	ReplayCount   int             `json:"replayCount"`
	HTTPStatus    int             `json:"httpStatus"`
	Error         string          `json:"error,omitempty"`
	CreatedAt     time.Time       `json:"createdAt"`
	NextAttemptAt time.Time       `json:"nextAttemptAt"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}
type Attempt struct {
	Attempt      int       `json:"attempt"`
	Replay       int       `json:"replay"`
	HTTPStatus   int       `json:"httpStatus"`
	Error        string    `json:"error,omitempty"`
	ResponseBody string    `json:"responseBody,omitempty"`
	DurationMS   int64     `json:"durationMs"`
	CreatedAt    time.Time `json:"createdAt"`
}
type DeliveryPage struct {
	Deliveries []Delivery `json:"deliveries"`
	Page       int        `json:"page"`
	PageSize   int        `json:"pageSize"`
	Total      int        `json:"total"`
}

func List(ctx context.Context, db *sql.DB, org, user int64, status string, page, size int) (*DeliveryPage, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 50
	}
	out := &DeliveryPage{Deliveries: []Delivery{}, Page: page, PageSize: size}
	where := ` FROM webhook_deliveries d JOIN webhook_events e ON e.id=d.event_id WHERE e.org_id=$1 AND e.user_id=$2 AND ($3='' OR d.status=$3)`
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*)`+where, org, user, status).Scan(&out.Total); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT d.id,e.id,e.event_type,d.status,d.attempts,d.replay_count,COALESCE(d.last_http_status,0),d.last_error,d.created_at,d.next_attempt_at`+where+` ORDER BY d.created_at DESC,d.id LIMIT $4 OFFSET $5`, org, user, status, size, (page-1)*size)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var x Delivery
		if err = rows.Scan(&x.ID, &x.EventID, &x.Type, &x.Status, &x.Attempts, &x.ReplayCount, &x.HTTPStatus, &x.Error, &x.CreatedAt, &x.NextAttemptAt); err != nil {
			return nil, err
		}
		out.Deliveries = append(out.Deliveries, x)
	}
	return out, rows.Err()
}
func Get(ctx context.Context, db *sql.DB, org, user int64, id string) (*Delivery, []Attempt, error) {
	var x Delivery
	err := db.QueryRowContext(ctx, `SELECT d.id,e.id,e.event_type,d.status,d.attempts,d.replay_count,COALESCE(d.last_http_status,0),d.last_error,d.created_at,d.next_attempt_at,e.payload FROM webhook_deliveries d JOIN webhook_events e ON e.id=d.event_id WHERE d.id::text=$1 AND e.org_id=$2 AND e.user_id=$3`, id, org, user).Scan(&x.ID, &x.EventID, &x.Type, &x.Status, &x.Attempts, &x.ReplayCount, &x.HTTPStatus, &x.Error, &x.CreatedAt, &x.NextAttemptAt, &x.Payload)
	if err != nil {
		return nil, nil, err
	}
	attempts := []Attempt{}
	rows, err := db.QueryContext(ctx, `SELECT attempt,replay,http_status,error,response_body,duration_ms,created_at FROM webhook_delivery_attempts WHERE delivery_id=$1 ORDER BY id`, id)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Attempt
		if err = rows.Scan(&a.Attempt, &a.Replay, &a.HTTPStatus, &a.Error, &a.ResponseBody, &a.DurationMS, &a.CreatedAt); err != nil {
			return nil, nil, err
		}
		attempts = append(attempts, a)
	}
	return &x, attempts, rows.Err()
}
func Replay(ctx context.Context, db *sql.DB, org, user int64, id string) error {
	res, err := db.ExecContext(ctx, `UPDATE webhook_deliveries d SET status='pending',attempts=0,replay_count=replay_count+1,next_attempt_at=NOW(),last_error='',locked_until=NULL,claim_token=NULL,updated_at=NOW() FROM webhook_events e WHERE d.event_id=e.id AND d.id::text=$1 AND e.org_id=$2 AND e.user_id=$3 AND d.status IN ('dead_letter','delivered','cancelled')`, id, org, user)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("delivery not found or still pending")
	}
	return nil
}
