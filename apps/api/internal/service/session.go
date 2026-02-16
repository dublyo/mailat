package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"golang.org/x/crypto/bcrypt"
)

// UserSession represents an active user session
type UserSession struct {
	ID         int64      `json:"id"`
	UUID       string     `json:"uuid"`
	UserID     int        `json:"userId"`
	OrgID      int        `json:"orgId"`
	DeviceName string     `json:"deviceName,omitempty"`
	DeviceType string     `json:"deviceType,omitempty"`
	Browser    string     `json:"browser,omitempty"`
	OS         string     `json:"os,omitempty"`
	IPAddress  string     `json:"ipAddress,omitempty"`
	Location   string     `json:"location,omitempty"`
	Active     bool       `json:"active"`
	LastSeenAt time.Time  `json:"lastSeenAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	CreatedAt  time.Time  `json:"createdAt"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	IsCurrent  bool       `json:"isCurrent"` // Indicates if this is the current session
}

// CreateSessionInput is the input for creating a session
type CreateSessionInput struct {
	UserID    int64
	OrgID     int64
	Token     string // JWT token (will be hashed)
	UserAgent string
	IPAddress string
	ExpiresAt time.Time
}

// SessionService handles user session management
type SessionService struct {
	db  *sql.DB
	cfg *config.Config
}

// NewSessionService creates a new session service
func NewSessionService(db *sql.DB, cfg *config.Config) *SessionService {
	return &SessionService{db: db, cfg: cfg}
}

// CreateSession creates a new session record
func (s *SessionService) CreateSession(ctx context.Context, input *CreateSessionInput) (*UserSession, error) {
	// Hash the token for storage
	tokenHash := hashToken(input.Token)

	// Parse user agent to extract device info
	deviceName, deviceType, browser, os := parseUserAgent(input.UserAgent)

	var session UserSession
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO user_sessions (user_id, org_id, token_hash, device_name, device_type, browser, os, ip_address, expires_at, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
		RETURNING id, uuid, user_id, org_id, COALESCE(device_name, ''), COALESCE(device_type, ''), COALESCE(browser, ''), COALESCE(os, ''), COALESCE(ip_address, ''), COALESCE(location, ''), active, last_seen_at, expires_at, created_at, revoked_at
	`, input.UserID, input.OrgID, tokenHash, deviceName, deviceType, browser, os, input.IPAddress, input.ExpiresAt,
	).Scan(&session.ID, &session.UUID, &session.UserID, &session.OrgID, &session.DeviceName, &session.DeviceType,
		&session.Browser, &session.OS, &session.IPAddress, &session.Location, &session.Active,
		&session.LastSeenAt, &session.ExpiresAt, &session.CreatedAt, &session.RevokedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	return &session, nil
}

// GetSessionByToken retrieves a session by its token
func (s *SessionService) GetSessionByToken(ctx context.Context, token string) (*UserSession, error) {
	tokenHash := hashToken(token)

	var session UserSession
	err := s.db.QueryRowContext(ctx, `
		SELECT id, uuid, user_id, org_id, COALESCE(device_name, ''), COALESCE(device_type, ''), COALESCE(browser, ''), COALESCE(os, ''), COALESCE(ip_address, ''), COALESCE(location, ''), active, last_seen_at, expires_at, created_at, revoked_at
		FROM user_sessions
		WHERE token_hash = $1 AND active = true AND expires_at > NOW()
	`, tokenHash).Scan(&session.ID, &session.UUID, &session.UserID, &session.OrgID, &session.DeviceName, &session.DeviceType,
		&session.Browser, &session.OS, &session.IPAddress, &session.Location, &session.Active,
		&session.LastSeenAt, &session.ExpiresAt, &session.CreatedAt, &session.RevokedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("session not found or expired")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	return &session, nil
}

// UpdateLastSeen updates the last seen timestamp for a session
func (s *SessionService) UpdateLastSeen(ctx context.Context, token string, ipAddress string) error {
	tokenHash := hashToken(token)

	_, err := s.db.ExecContext(ctx, `
		UPDATE user_sessions
		SET last_seen_at = NOW(), ip_address = COALESCE($2, ip_address)
		WHERE token_hash = $1 AND active = true
	`, tokenHash, ipAddress)
	return err
}

// ListUserSessions lists all sessions for a user
func (s *SessionService) ListUserSessions(ctx context.Context, userID int64, currentToken string) ([]*UserSession, error) {
	currentTokenHash := hashToken(currentToken)

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, uuid, user_id, org_id, COALESCE(device_name, ''), COALESCE(device_type, ''), COALESCE(browser, ''), COALESCE(os, ''), COALESCE(ip_address, ''), COALESCE(location, ''), active, last_seen_at, expires_at, created_at, revoked_at, token_hash
		FROM user_sessions
		WHERE user_id = $1 AND active = true AND expires_at > NOW()
		ORDER BY last_seen_at DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []*UserSession
	for rows.Next() {
		var session UserSession
		var tokenHash string
		if err := rows.Scan(&session.ID, &session.UUID, &session.UserID, &session.OrgID, &session.DeviceName,
			&session.DeviceType, &session.Browser, &session.OS, &session.IPAddress, &session.Location,
			&session.Active, &session.LastSeenAt, &session.ExpiresAt, &session.CreatedAt, &session.RevokedAt, &tokenHash); err != nil {
			continue
		}

		// Mark current session
		session.IsCurrent = (tokenHash == currentTokenHash)

		sessions = append(sessions, &session)
	}

	return sessions, nil
}

// RevokeSession revokes a specific session
func (s *SessionService) RevokeSession(ctx context.Context, userID int64, sessionUUID string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE user_sessions
		SET active = false, revoked_at = NOW()
		WHERE uuid = $1 AND user_id = $2 AND active = true
	`, sessionUUID, userID)
	if err != nil {
		return fmt.Errorf("failed to revoke session: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("session not found")
	}

	return nil
}

// RevokeAllSessions revokes all sessions for a user except the current one
func (s *SessionService) RevokeAllSessions(ctx context.Context, userID int64, exceptToken string) (int, error) {
	exceptHash := hashToken(exceptToken)

	result, err := s.db.ExecContext(ctx, `
		UPDATE user_sessions
		SET active = false, revoked_at = NOW()
		WHERE user_id = $1 AND active = true AND token_hash != $2
	`, userID, exceptHash)
	if err != nil {
		return 0, fmt.Errorf("failed to revoke sessions: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	return int(rowsAffected), nil
}

// RevokeAllUserSessions revokes ALL sessions for a user (including current)
func (s *SessionService) RevokeAllUserSessions(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE user_sessions
		SET active = false, revoked_at = NOW()
		WHERE user_id = $1 AND active = true
	`, userID)
	return err
}

// CleanupExpiredSessions removes expired sessions
func (s *SessionService) CleanupExpiredSessions(ctx context.Context) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM user_sessions
		WHERE expires_at < NOW() - INTERVAL '30 days'
	`)
	if err != nil {
		return 0, fmt.Errorf("failed to cleanup sessions: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	return int(rowsAffected), nil
}

// GetActiveSessionCount returns the number of active sessions for a user
func (s *SessionService) GetActiveSessionCount(ctx context.Context, userID int64) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM user_sessions
		WHERE user_id = $1 AND active = true AND expires_at > NOW()
	`, userID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count sessions: %w", err)
	}
	return count, nil
}

// ChangePassword changes the user's password
func (s *SessionService) ChangePassword(ctx context.Context, userID int64, currentPassword, newPassword string) error {
	// First verify the current password
	var storedHash string
	err := s.db.QueryRowContext(ctx, `
		SELECT password_hash FROM users WHERE id = $1
	`, userID).Scan(&storedHash)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("user not found")
		}
		return fmt.Errorf("failed to get user: %w", err)
	}

	// Verify current password
	if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(currentPassword)); err != nil {
		return fmt.Errorf("current password is incorrect")
	}

	// Hash the new password
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}

	// Update the password
	_, err = s.db.ExecContext(ctx, `
		UPDATE users SET password_hash = $1, updated_at = NOW() WHERE id = $2
	`, string(newHash), userID)
	if err != nil {
		return fmt.Errorf("failed to update password: %w", err)
	}

	return nil
}

// hashToken creates a SHA256 hash of a token
func hashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

// parseUserAgent extracts device info from a user agent string
func parseUserAgent(ua string) (deviceName, deviceType, browser, os string) {
	ua = strings.ToLower(ua)

	// Detect OS
	switch {
	case strings.Contains(ua, "windows"):
		os = "Windows"
	case strings.Contains(ua, "macintosh") || strings.Contains(ua, "mac os"):
		os = "macOS"
	case strings.Contains(ua, "linux"):
		os = "Linux"
	case strings.Contains(ua, "iphone"):
		os = "iOS"
	case strings.Contains(ua, "ipad"):
		os = "iPadOS"
	case strings.Contains(ua, "android"):
		os = "Android"
	default:
		os = "Unknown"
	}

	// Detect browser
	switch {
	case strings.Contains(ua, "chrome") && !strings.Contains(ua, "edge"):
		browser = "Chrome"
	case strings.Contains(ua, "firefox"):
		browser = "Firefox"
	case strings.Contains(ua, "safari") && !strings.Contains(ua, "chrome"):
		browser = "Safari"
	case strings.Contains(ua, "edge"):
		browser = "Edge"
	case strings.Contains(ua, "opera"):
		browser = "Opera"
	default:
		browser = "Unknown"
	}

	// Detect device type
	switch {
	case strings.Contains(ua, "mobile") || strings.Contains(ua, "iphone") || strings.Contains(ua, "android"):
		if strings.Contains(ua, "tablet") || strings.Contains(ua, "ipad") {
			deviceType = "tablet"
		} else {
			deviceType = "mobile"
		}
	default:
		deviceType = "desktop"
	}

	// Create friendly device name
	deviceName = fmt.Sprintf("%s on %s", browser, os)

	return deviceName, deviceType, browser, os
}
