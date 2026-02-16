package service

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
)

// PushSubscription represents a web push subscription
type PushSubscription struct {
	ID              int64      `json:"id"`
	UUID            string     `json:"uuid"`
	UserID          int        `json:"userId"`
	Endpoint        string     `json:"endpoint"`
	DeviceName      string     `json:"deviceName,omitempty"`
	NotifyNewEmail  bool       `json:"notifyNewEmail"`
	NotifyCampaign  bool       `json:"notifyCampaign"`
	NotifyMentions  bool       `json:"notifyMentions"`
	Active          bool       `json:"active"`
	LastUsedAt      *time.Time `json:"lastUsedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
}

// CreatePushSubscriptionInput is the input for creating a push subscription
type CreatePushSubscriptionInput struct {
	Endpoint   string `json:"endpoint"`
	P256dhKey  string `json:"p256dhKey"`
	AuthKey    string `json:"authKey"`
	DeviceName string `json:"deviceName,omitempty"`
}

// PushNotification represents a notification to be sent
type PushNotification struct {
	Title   string                 `json:"title"`
	Body    string                 `json:"body"`
	Icon    string                 `json:"icon,omitempty"`
	Badge   string                 `json:"badge,omitempty"`
	Tag     string                 `json:"tag,omitempty"`
	Data    map[string]interface{} `json:"data,omitempty"`
	Actions []NotificationAction   `json:"actions,omitempty"`
}

// NotificationAction represents an action button on a notification
type NotificationAction struct {
	Action string `json:"action"`
	Title  string `json:"title"`
	Icon   string `json:"icon,omitempty"`
}

// PushNotificationService handles push notification operations
type PushNotificationService struct {
	db         *sql.DB
	cfg        *config.Config
	httpClient *http.Client
	vapidKeys  *VAPIDKeys
}

// VAPIDKeys holds VAPID public/private keys for web push
type VAPIDKeys struct {
	PublicKey  string
	PrivateKey *ecdsa.PrivateKey
}

// NewPushNotificationService creates a new push notification service
func NewPushNotificationService(db *sql.DB, cfg *config.Config) *PushNotificationService {
	service := &PushNotificationService{
		db:         db,
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}

	// Generate or load VAPID keys
	service.initVAPIDKeys()

	return service
}

// initVAPIDKeys initializes VAPID keys (in production, load from config/storage)
func (s *PushNotificationService) initVAPIDKeys() {
	// Generate new ECDSA key pair for VAPID
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return
	}

	// Encode public key
	pubKeyBytes := elliptic.Marshal(elliptic.P256(), privateKey.PublicKey.X, privateKey.PublicKey.Y)
	publicKey := base64.RawURLEncoding.EncodeToString(pubKeyBytes)

	s.vapidKeys = &VAPIDKeys{
		PublicKey:  publicKey,
		PrivateKey: privateKey,
	}
}

// GetVAPIDPublicKey returns the VAPID public key for client subscription
func (s *PushNotificationService) GetVAPIDPublicKey() string {
	if s.vapidKeys == nil {
		return ""
	}
	return s.vapidKeys.PublicKey
}

// Subscribe creates a new push subscription
func (s *PushNotificationService) Subscribe(ctx context.Context, userID int64, input *CreatePushSubscriptionInput) (*PushSubscription, error) {
	if input.Endpoint == "" {
		return nil, fmt.Errorf("endpoint is required")
	}
	if input.P256dhKey == "" {
		return nil, fmt.Errorf("p256dh key is required")
	}
	if input.AuthKey == "" {
		return nil, fmt.Errorf("auth key is required")
	}

	var sub PushSubscription
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO push_subscriptions (user_id, endpoint, p256dh_key, auth_key, device_name)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (endpoint) DO UPDATE
		SET p256dh_key = EXCLUDED.p256dh_key, auth_key = EXCLUDED.auth_key, active = true
		RETURNING id, uuid, user_id, endpoint, COALESCE(device_name, ''), notify_new_email, notify_campaign, notify_mentions, active, last_used_at, created_at
	`, userID, input.Endpoint, input.P256dhKey, input.AuthKey, input.DeviceName,
	).Scan(&sub.ID, &sub.UUID, &sub.UserID, &sub.Endpoint, &sub.DeviceName,
		&sub.NotifyNewEmail, &sub.NotifyCampaign, &sub.NotifyMentions, &sub.Active, &sub.LastUsedAt, &sub.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create push subscription: %w", err)
	}

	return &sub, nil
}

// Unsubscribe removes a push subscription
func (s *PushNotificationService) Unsubscribe(ctx context.Context, userID int64, endpoint string) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM push_subscriptions WHERE user_id = $1 AND endpoint = $2
	`, userID, endpoint)
	if err != nil {
		return fmt.Errorf("failed to unsubscribe: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("subscription not found")
	}

	return nil
}

// UpdatePreferences updates notification preferences for a subscription
func (s *PushNotificationService) UpdatePreferences(ctx context.Context, userID int64, subUUID string, notifyNewEmail, notifyCampaign, notifyMentions *bool) error {
	updates := []string{}
	args := []interface{}{}
	argNum := 1

	if notifyNewEmail != nil {
		updates = append(updates, fmt.Sprintf("notify_new_email = $%d", argNum))
		args = append(args, *notifyNewEmail)
		argNum++
	}

	if notifyCampaign != nil {
		updates = append(updates, fmt.Sprintf("notify_campaign = $%d", argNum))
		args = append(args, *notifyCampaign)
		argNum++
	}

	if notifyMentions != nil {
		updates = append(updates, fmt.Sprintf("notify_mentions = $%d", argNum))
		args = append(args, *notifyMentions)
		argNum++
	}

	if len(updates) == 0 {
		return nil
	}

	query := fmt.Sprintf(`
		UPDATE push_subscriptions
		SET %s
		WHERE uuid = $%d AND user_id = $%d
	`, joinUpdates(updates), argNum, argNum+1)

	args = append(args, subUUID, userID)

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to update preferences: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("subscription not found")
	}

	return nil
}

// ListSubscriptions lists all push subscriptions for a user
func (s *PushNotificationService) ListSubscriptions(ctx context.Context, userID int64) ([]*PushSubscription, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, uuid, user_id, endpoint, COALESCE(device_name, ''), notify_new_email, notify_campaign, notify_mentions, active, last_used_at, created_at
		FROM push_subscriptions
		WHERE user_id = $1 AND active = true
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list subscriptions: %w", err)
	}
	defer rows.Close()

	var subs []*PushSubscription
	for rows.Next() {
		var sub PushSubscription
		if err := rows.Scan(&sub.ID, &sub.UUID, &sub.UserID, &sub.Endpoint, &sub.DeviceName,
			&sub.NotifyNewEmail, &sub.NotifyCampaign, &sub.NotifyMentions, &sub.Active, &sub.LastUsedAt, &sub.CreatedAt); err != nil {
			continue
		}
		subs = append(subs, &sub)
	}

	return subs, nil
}

// SendToUser sends a notification to all of a user's subscriptions
func (s *PushNotificationService) SendToUser(ctx context.Context, userID int64, notificationType string, notification *PushNotification) error {
	// Get all active subscriptions for this user
	rows, err := s.db.QueryContext(ctx, `
		SELECT endpoint, p256dh_key, auth_key, notify_new_email, notify_campaign, notify_mentions
		FROM push_subscriptions
		WHERE user_id = $1 AND active = true
	`, userID)
	if err != nil {
		return fmt.Errorf("failed to get subscriptions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var endpoint, p256dh, auth string
		var notifyEmail, notifyCampaign, notifyMentions bool

		if err := rows.Scan(&endpoint, &p256dh, &auth, &notifyEmail, &notifyCampaign, &notifyMentions); err != nil {
			continue
		}

		// Check if user wants this type of notification
		switch notificationType {
		case "email":
			if !notifyEmail {
				continue
			}
		case "campaign":
			if !notifyCampaign {
				continue
			}
		case "mention":
			if !notifyMentions {
				continue
			}
		}

		// Send notification asynchronously
		go s.sendPushNotification(endpoint, p256dh, auth, notification)
	}

	return nil
}

// sendPushNotification sends a web push notification
func (s *PushNotificationService) sendPushNotification(endpoint, p256dh, auth string, notification *PushNotification) {
	// Prepare payload
	payload, err := json.Marshal(notification)
	if err != nil {
		return
	}

	// Create request
	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(payload))
	if err != nil {
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("TTL", "86400") // 24 hours

	// In production, you would use proper web push encryption here
	// This is a simplified version - use github.com/SherClockHolmes/webpush-go for full implementation

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	// If endpoint is gone (410), mark subscription as inactive
	if resp.StatusCode == http.StatusGone {
		s.db.ExecContext(context.Background(), `
			UPDATE push_subscriptions SET active = false WHERE endpoint = $1
		`, endpoint)
	}

	// Update last used
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		s.db.ExecContext(context.Background(), `
			UPDATE push_subscriptions SET last_used_at = NOW() WHERE endpoint = $1
		`, endpoint)
	}
}

// SendNewEmailNotification sends a new email notification
func (s *PushNotificationService) SendNewEmailNotification(ctx context.Context, userID int64, from, subject string) error {
	notification := &PushNotification{
		Title: "New Email",
		Body:  fmt.Sprintf("From: %s\n%s", from, subject),
		Icon:  "/icons/email.png",
		Tag:   "new-email",
		Data: map[string]interface{}{
			"type": "new_email",
			"from": from,
		},
		Actions: []NotificationAction{
			{Action: "view", Title: "View"},
			{Action: "archive", Title: "Archive"},
		},
	}

	return s.SendToUser(ctx, userID, "email", notification)
}

// CleanupInactiveSubscriptions removes old inactive subscriptions
func (s *PushNotificationService) CleanupInactiveSubscriptions(ctx context.Context) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM push_subscriptions
		WHERE active = false OR last_used_at < NOW() - INTERVAL '90 days'
	`)
	if err != nil {
		return 0, fmt.Errorf("failed to cleanup subscriptions: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	return int(rowsAffected), nil
}
