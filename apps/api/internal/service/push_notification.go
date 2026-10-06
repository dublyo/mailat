package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/eventoutbox"
	"github.com/dublyo/mailat/api/internal/provider"
)

// PushSubscription represents a web push subscription
type PushSubscription struct {
	ID             int64      `json:"id"`
	UUID           string     `json:"uuid"`
	UserID         int        `json:"userId"`
	Endpoint       string     `json:"endpoint"`
	DeviceName     string     `json:"deviceName,omitempty"`
	NotifyNewEmail bool       `json:"notifyNewEmail"`
	NotifyCampaign bool       `json:"notifyCampaign"`
	NotifyMentions bool       `json:"notifyMentions"`
	Active         bool       `json:"active"`
	LastUsedAt     *time.Time `json:"lastUsedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
}

// CreatePushSubscriptionInput is the input for creating a push subscription
type CreatePushSubscriptionInput struct {
	Endpoint   string `json:"endpoint"`
	P256dhKey  string `json:"p256dhKey"`
	AuthKey    string `json:"authKey"`
	DeviceName string `json:"deviceName,omitempty"`
}

var (
	ErrPushDisabled         = errors.New("push is not configured on this server (VAPID keys)")
	ErrPushEndpointTaken    = errors.New("this push endpoint belongs to another account")
	ErrPushSubscriptionGone = errors.New("subscription not found")
)

// PushInputError is a client mistake in a subscription request.
type PushInputError struct{ Message string }

func (e *PushInputError) Error() string { return e.Message }

const (
	pushMaxEndpoint     = 1024
	pushMaxFailures     = 5
	pushSubjectMaxRunes = 120
	pushTimeout         = 10 * time.Second
)

// PushNotificationService stores browser subscriptions and delivers RFC 8291
// encrypted, VAPID-signed notifications. Delivery is synchronous: the arrival
// runner calls it and decides whether to retry.
type PushNotificationService struct {
	db         *sql.DB
	cfg        *config.Config
	httpClient *http.Client
	keys       *provider.VAPIDKeys // nil when push is disabled
	suffixes   []string
	now        func() time.Time
}

// NewPushNotificationService loads the configured VAPID keys. Startup
// validation already rejected a partial or mismatched set.
func NewPushNotificationService(db *sql.DB, cfg *config.Config) *PushNotificationService {
	client := eventoutbox.SafeClient()
	client.Timeout = pushTimeout
	s := &PushNotificationService{db: db, cfg: cfg, httpClient: client, now: time.Now}
	if cfg != nil {
		s.suffixes = cfg.PushEndpointHostSuffixes
		if cfg.PushEnabled() {
			keys, err := provider.ParseVAPIDKeys(cfg.VAPIDPublicKey, cfg.VAPIDPrivateKey)
			if err != nil {
				log.Printf("Web push disabled: %v", err)
			} else {
				s.keys = keys
			}
		}
	}
	if len(s.suffixes) == 0 {
		s.suffixes = strings.Split(config.DefaultPushEndpointHostSuffixes, ",")
	}
	return s
}

// Enabled reports whether VAPID keys are configured.
func (s *PushNotificationService) Enabled() bool { return s != nil && s.keys != nil }

// GetVAPIDPublicKey returns the application server key, or "" when push is disabled.
func (s *PushNotificationService) GetVAPIDPublicKey() string {
	if !s.Enabled() {
		return ""
	}
	return s.keys.Public
}

// endpointAllowed accepts https URLs on a configured push service host.
func (s *PushNotificationService) endpointAllowed(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" || len(endpoint) > pushMaxEndpoint {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	for _, suffix := range s.suffixes {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

// Subscribe registers this device for the user. An endpoint owned by another
// account is never taken over.
func (s *PushNotificationService) Subscribe(ctx context.Context, userID int64, input *CreatePushSubscriptionInput) (*PushSubscription, error) {
	if !s.Enabled() {
		return nil, ErrPushDisabled
	}
	input.Endpoint = strings.TrimSpace(input.Endpoint)
	if !s.endpointAllowed(input.Endpoint) {
		return nil, &PushInputError{"endpoint must be an https URL of a supported push service (at most 1 KiB)"}
	}
	if _, _, err := provider.DecodeSubscriptionKeys(input.P256dhKey, input.AuthKey); err != nil {
		return nil, &PushInputError{err.Error()}
	}
	device := strings.TrimSpace(input.DeviceName)
	if len([]rune(device)) > 255 {
		return nil, &PushInputError{"deviceName must be at most 255 characters"}
	}
	var sub PushSubscription
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO push_subscriptions (user_id, endpoint, p256dh_key, auth_key, device_name, vapid_key_id, failure_count, active)
		VALUES ($1, $2, $3, $4, NULLIF($5,''), $6, 0, true)
		ON CONFLICT (endpoint) DO UPDATE
		SET p256dh_key = EXCLUDED.p256dh_key, auth_key = EXCLUDED.auth_key,
			device_name = COALESCE(EXCLUDED.device_name, push_subscriptions.device_name),
			vapid_key_id = EXCLUDED.vapid_key_id, failure_count = 0, active = true
		WHERE push_subscriptions.user_id = EXCLUDED.user_id
		RETURNING id, uuid, user_id, endpoint, COALESCE(device_name, ''), notify_new_email, notify_campaign, notify_mentions, active, last_used_at, created_at
	`, userID, input.Endpoint, input.P256dhKey, input.AuthKey, device, s.keys.KeyID,
	).Scan(&sub.ID, &sub.UUID, &sub.UserID, &sub.Endpoint, &sub.DeviceName,
		&sub.NotifyNewEmail, &sub.NotifyCampaign, &sub.NotifyMentions, &sub.Active, &sub.LastUsedAt, &sub.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPushEndpointTaken
	}
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
		return ErrPushSubscriptionGone
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
		WHERE uuid::text = $%d AND user_id = $%d
	`, joinUpdates(updates), argNum, argNum+1)

	args = append(args, subUUID, userID)

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to update preferences: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return ErrPushSubscriptionGone
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

	subs := []*PushSubscription{}
	for rows.Next() {
		var sub PushSubscription
		if err := rows.Scan(&sub.ID, &sub.UUID, &sub.UserID, &sub.Endpoint, &sub.DeviceName,
			&sub.NotifyNewEmail, &sub.NotifyCampaign, &sub.NotifyMentions, &sub.Active, &sub.LastUsedAt, &sub.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to list subscriptions: %w", err)
		}
		subs = append(subs, &sub)
	}
	return subs, rows.Err()
}

// newEmailPush is the encrypted payload the service worker shows.
type newEmailPush struct {
	Type     string `json:"type"`
	UUID     string `json:"uuid"`
	From     string `json:"from"`
	Subject  string `json:"subject"`
	Identity string `json:"identity"`
}

type pushTarget struct {
	id                     int64
	endpoint, p256dh, auth string
}

// errPushTransient marks a delivery the push service may accept later.
var errPushTransient = errors.New("push service temporarily unavailable")

// SendNewEmailNotification pushes a new-mail notice to every device of the
// user that wants one. It returns an error (so the job is retried) only when
// every device failed transiently; per-device outcomes are recorded on the
// subscription.
func (s *PushNotificationService) SendNewEmailNotification(ctx context.Context, userID int64, uuid, from, subject, identityEmail string) error {
	if !s.Enabled() {
		return nil
	}
	var wanted bool
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT new_email_notifications FROM user_settings WHERE user_id=$1), true)`, userID).Scan(&wanted)
	if err != nil {
		return err
	}
	if !wanted {
		return nil
	}
	// Subscriptions bound to a previous VAPID key can no longer be delivered;
	// the settings page offers to enable notifications again.
	if _, err = s.db.ExecContext(ctx, `UPDATE push_subscriptions SET active=false WHERE user_id=$1 AND active AND vapid_key_id IS DISTINCT FROM $2`, userID, s.keys.KeyID); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,endpoint,p256dh_key,auth_key FROM push_subscriptions
		WHERE user_id=$1 AND active AND notify_new_email AND vapid_key_id=$2 ORDER BY id`, userID, s.keys.KeyID)
	if err != nil {
		return err
	}
	var targets []pushTarget
	for rows.Next() {
		var t pushTarget
		if err = rows.Scan(&t.id, &t.endpoint, &t.p256dh, &t.auth); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, t)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}
	payload, err := json.Marshal(newEmailPush{Type: "new_email", UUID: uuid, From: from, Subject: clipRunes(subject, pushSubjectMaxRunes), Identity: identityEmail})
	if err != nil {
		return err
	}
	topicSum := sha256.Sum256([]byte(uuid))
	topic := base64.RawURLEncoding.EncodeToString(topicSum[:])[:22]
	transient := 0
	for _, t := range targets {
		if err = s.deliver(ctx, t, payload, topic); errors.Is(err, errPushTransient) {
			transient++
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if transient == len(targets) {
		return fmt.Errorf("%w for all %d devices", errPushTransient, transient)
	}
	return nil
}

// deliver sends one notification and records the outcome on the subscription.
func (s *PushNotificationService) deliver(ctx context.Context, t pushTarget, payload []byte, topic string) error {
	uaPublic, auth, err := provider.DecodeSubscriptionKeys(t.p256dh, t.auth)
	if err != nil || !s.endpointAllowed(t.endpoint) {
		return s.recordPush(ctx, t.id, http.StatusGone)
	}
	body, err := provider.EncryptWebPush(uaPublic, auth, payload)
	if err != nil {
		return err
	}
	authorization, err := s.keys.VAPIDAuthorization(t.endpoint, s.cfg.VAPIDSubject, s.now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", "86400")
	req.Header.Set("Urgency", "normal")
	req.Header.Set("Topic", topic)
	req.Header.Set("Authorization", authorization)
	res, err := s.httpClient.Do(req)
	status := 0
	if err == nil {
		status = res.StatusCode
		res.Body.Close()
	}
	if recErr := s.recordPush(ctx, t.id, status); recErr != nil {
		return recErr
	}
	if status == 0 || status == http.StatusTooManyRequests || status >= 500 {
		return errPushTransient
	}
	return nil
}

// recordPush applies one delivery outcome: success resets the failure count,
// 404/410 ends the subscription, anything else counts toward the limit.
func (s *PushNotificationService) recordPush(ctx context.Context, id int64, status int) error {
	var err error
	switch {
	case status >= 200 && status < 300:
		_, err = s.db.ExecContext(ctx, `UPDATE push_subscriptions SET failure_count=0, last_used_at=now() WHERE id=$1`, id)
	case status == http.StatusNotFound || status == http.StatusGone:
		_, err = s.db.ExecContext(ctx, `UPDATE push_subscriptions SET active=false WHERE id=$1`, id)
	default:
		_, err = s.db.ExecContext(ctx, `UPDATE push_subscriptions SET failure_count=failure_count+1, active=active AND failure_count+1<$2 WHERE id=$1`, id, pushMaxFailures)
	}
	return err
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
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
