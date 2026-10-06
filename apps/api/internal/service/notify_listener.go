package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/lib/pq"

	"github.com/dublyo/mailat/api/internal/config"
)

// NotifyListener listens for PostgreSQL NOTIFY events
type NotifyListener struct {
	db          *sql.DB
	cfg         *config.Config
	listener    *pq.Listener
	handlers    map[string][]NotifyHandler
	handlersMux sync.RWMutex
	stopCh      chan struct{}
	wg          sync.WaitGroup
}

// NotifyHandler is a function that handles notification payloads
type NotifyHandler func(payload NotifyPayload) error

// NotifyPayload represents a notification from PostgreSQL
type NotifyPayload struct {
	Table     string                 `json:"table"`
	Action    string                 `json:"action"` // INSERT, UPDATE, DELETE
	EmailID   int64                  `json:"email_id,omitempty"`
	OrgID     int64                  `json:"org_id,omitempty"`
	OldStatus string                 `json:"old_status,omitempty"`
	NewStatus string                 `json:"new_status,omitempty"`
	EventType string                 `json:"event_type,omitempty"`
	Data      map[string]interface{} `json:"data,omitempty"`
}

// NewNotifyListener creates a new PostgreSQL NOTIFY listener
func NewNotifyListener(db *sql.DB, cfg *config.Config) (*NotifyListener, error) {
	// Create a pq listener using the database URL
	listener := pq.NewListener(cfg.DatabaseURL, 10*time.Second, time.Minute, func(ev pq.ListenerEventType, err error) {
		if err != nil {
			fmt.Printf("PG Listener error: %v\n", err)
		}
	})

	return &NotifyListener{
		db:       db,
		cfg:      cfg,
		listener: listener,
		handlers: make(map[string][]NotifyHandler),
		stopCh:   make(chan struct{}),
	}, nil
}

// Subscribe adds a handler for a specific channel
func (l *NotifyListener) Subscribe(channel string, handler NotifyHandler) {
	l.handlersMux.Lock()
	defer l.handlersMux.Unlock()

	l.handlers[channel] = append(l.handlers[channel], handler)
}

// Start begins listening for notifications
func (l *NotifyListener) Start(ctx context.Context) error {
	// Subscribe to channels
	channels := []string{
		"email_status_changed",
		"delivery_event_created",
	}

	for _, ch := range channels {
		if err := l.listener.Listen(ch); err != nil {
			return fmt.Errorf("failed to listen on channel %s: %w", ch, err)
		}
		fmt.Printf("Listening on PostgreSQL channel: %s\n", ch)
	}

	l.wg.Add(1)
	go l.listen(ctx)

	return nil
}

// Stop stops the listener
func (l *NotifyListener) Stop() {
	close(l.stopCh)
	l.listener.Close()
	l.wg.Wait()
}

// listen processes incoming notifications
func (l *NotifyListener) listen(ctx context.Context) {
	defer l.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case <-l.stopCh:
			return
		case notification := <-l.listener.Notify:
			if notification == nil {
				continue
			}

			l.processNotification(notification)

		case <-time.After(90 * time.Second):
			// Ping to keep connection alive
			go l.listener.Ping()
		}
	}
}

// processNotification handles a single notification
func (l *NotifyListener) processNotification(notification *pq.Notification) {
	var payload NotifyPayload
	if err := json.Unmarshal([]byte(notification.Extra), &payload); err != nil {
		fmt.Printf("Failed to unmarshal notification payload: %v\n", err)
		return
	}

	l.handlersMux.RLock()
	handlers := l.handlers[notification.Channel]
	l.handlersMux.RUnlock()

	for _, handler := range handlers {
		if err := handler(payload); err != nil {
			fmt.Printf("Handler error for channel %s: %v\n", notification.Channel, err)
		}
	}
}

// DeliveryTracker handles delivery event processing
type DeliveryTracker struct {
	db       *sql.DB
	cfg      *config.Config
	listener *NotifyListener
}

// NewDeliveryTracker creates a new delivery tracker
func NewDeliveryTracker(db *sql.DB, cfg *config.Config) (*DeliveryTracker, error) {
	listener, err := NewNotifyListener(db, cfg)
	if err != nil {
		return nil, err
	}

	tracker := &DeliveryTracker{
		db:       db,
		cfg:      cfg,
		listener: listener,
	}

	// Register handlers
	listener.Subscribe("email_status_changed", tracker.handleEmailStatusChange)
	listener.Subscribe("delivery_event_created", tracker.handleDeliveryEvent)

	return tracker, nil
}

// Start begins tracking delivery events
func (t *DeliveryTracker) Start(ctx context.Context) error {
	return t.listener.Start(ctx)
}

// Stop stops the delivery tracker
func (t *DeliveryTracker) Stop() {
	t.listener.Stop()
}

// handleEmailStatusChange processes email status change notifications
func (t *DeliveryTracker) handleEmailStatusChange(payload NotifyPayload) error {
	fmt.Printf("Email status changed: email_id=%d, %s -> %s\n",
		payload.EmailID, payload.OldStatus, payload.NewStatus)

	// Handle specific status transitions
	switch payload.NewStatus {
	case "sent":
		// Email was sent successfully
		t.updateOrgStats(payload.OrgID, "sent")

	case "delivered":
		// Email was delivered
		t.updateOrgStats(payload.OrgID, "delivered")

	case "bounced":
		t.handleBounce(payload)

	case "failed":
		// Email failed to send
		t.updateOrgStats(payload.OrgID, "failed")
	}

	// Queue webhook delivery if configured
	t.queueWebhookDelivery(payload.OrgID, payload.EmailID, payload.NewStatus)

	return nil
}

// handleDeliveryEvent processes new delivery event notifications
func (t *DeliveryTracker) handleDeliveryEvent(payload NotifyPayload) error {
	fmt.Printf("Delivery event created: email_id=%d, event=%s\n",
		payload.EmailID, payload.EventType)

	// Handle specific event types; others (for example the worker's "deferred")
	// need no tracker work.
	switch payload.EventType {
	case "opened":
		// Update open count
		_, err := t.db.Exec(`
			UPDATE transactional_emails
			SET opened_at = COALESCE(opened_at, NOW())
			WHERE id = $1
		`, payload.EmailID)
		if err != nil {
			fmt.Printf("Failed to update opened_at: %v\n", err)
		}

	case "clicked":
		// Update click count
		_, err := t.db.Exec(`
			UPDATE transactional_emails
			SET clicked_at = COALESCE(clicked_at, NOW())
			WHERE id = $1
		`, payload.EmailID)
		if err != nil {
			fmt.Printf("Failed to update clicked_at: %v\n", err)
		}

	case "complained":
		t.handleComplaint(payload)
	}

	// Queue webhook delivery for the event
	t.queueWebhookDelivery(payload.OrgID, payload.EmailID, payload.EventType)

	return nil
}

// updateOrgStats updates organization sending statistics
func (t *DeliveryTracker) updateOrgStats(orgID int64, statType string) {
	// This would update daily/monthly stats in a stats table
	// For now, just log
	fmt.Printf("Updating org stats: org_id=%d, stat=%s\n", orgID, statType)
}

// handleBounce records bounce stats. Suppression is written by the SNS path
// (ProcessDeliveryEvent), which knows the exact affected recipients.
func (t *DeliveryTracker) handleBounce(payload NotifyPayload) {
	t.updateOrgStats(payload.OrgID, "bounced")
}

// handleComplaint records complaint stats; suppression is written by the SNS path.
func (t *DeliveryTracker) handleComplaint(payload NotifyPayload) {
	t.updateOrgStats(payload.OrgID, "complained")
}

// queueWebhookDelivery queues a webhook delivery for an event
func (t *DeliveryTracker) queueWebhookDelivery(orgID, emailID int64, eventType string) {
	// This will be implemented fully when webhooks are added
	// For now, just check if there are webhooks configured
	var count int
	err := t.db.QueryRow(`
		SELECT COUNT(*) FROM webhooks
		WHERE org_id = $1 AND active = true AND $2 = ANY(events)
	`, orgID, "email."+eventType).Scan(&count)
	if err != nil {
		fmt.Printf("Failed to check webhooks: %v\n", err)
		return
	}

	if count > 0 {
		fmt.Printf("Would queue %d webhook(s) for event: email.%s, email_id=%d\n",
			count, eventType, emailID)
	}
}
