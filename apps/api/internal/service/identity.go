package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/pkg/crypto"
)

type IdentityService struct {
	db       *sql.DB
	cfg      *config.Config
	stalwart *StalwartClient
}

type StalwartClient struct {
	baseURL    string
	httpClient *http.Client
	username   string
	password   string
}

func NewStalwartClient(baseURL, adminPassword string) *StalwartClient {
	return &StalwartClient{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		username:   "admin",
		password:   adminPassword,
	}
}

func NewIdentityService(db *sql.DB, cfg *config.Config) *IdentityService {
	return &IdentityService{
		db:       db,
		cfg:      cfg,
		stalwart: NewStalwartClient(cfg.StalwartURL, cfg.StalwartAdminToken),
	}
}

// CreateIdentity creates a new email identity with Stalwart sync
func (s *IdentityService) CreateIdentity(ctx context.Context, userID int64, req *model.CreateIdentityRequest) (*model.Identity, error) {
	// Verify domain ownership
	var domainID int64
	var domainOrgID int64
	var domainStatus string
	var userOrgID int64

	err := s.db.QueryRowContext(ctx, `
		SELECT d.id, d.org_id, d.status, u.org_id
		FROM domains d
		JOIN users u ON u.id = $1
		WHERE d.uuid = $2
	`, userID, req.DomainId).Scan(&domainID, &domainOrgID, &domainStatus, &userOrgID)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("domain not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to verify domain: %w", err)
	}

	if domainOrgID != userOrgID {
		return nil, fmt.Errorf("domain does not belong to your organization")
	}

	if domainStatus != "active" {
		return nil, fmt.Errorf("domain is not active")
	}

	// Check if identity already exists
	var exists bool
	err = s.db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM identities WHERE email = $1)
	`, strings.ToLower(req.Email)).Scan(&exists)
	if err != nil {
		return nil, fmt.Errorf("failed to check existing identity: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("identity already exists")
	}

	// If catch-all is requested, check if one already exists for this domain
	if req.IsCatchAll {
		var catchAllExists bool
		err = s.db.QueryRowContext(ctx, `
			SELECT EXISTS(SELECT 1 FROM identities WHERE domain_id = $1 AND is_catch_all = true)
		`, domainID).Scan(&catchAllExists)
		if err != nil {
			return nil, fmt.Errorf("failed to check catch-all: %w", err)
		}
		if catchAllExists {
			return nil, fmt.Errorf("a catch-all identity already exists for this domain")
		}
	}

	// Auto-assign color based on identity count for user
	var identityCount int
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM identities WHERE user_id = $1
	`, userID).Scan(&identityCount)
	if err != nil {
		identityCount = 0
	}
	colors := []string{"#3B82F6", "#10B981", "#8B5CF6", "#F59E0B", "#EF4444", "#EC4899", "#06B6D4", "#84CC16"}
	assignedColor := colors[identityCount%len(colors)]

	// Hash password for local storage
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	// Encrypt password for JMAP authentication
	encryptedPassword, err := crypto.Encrypt(req.Password, s.cfg.EncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt password: %w", err)
	}

	// Default quota: 1GB
	quotaBytes := req.QuotaBytes
	if quotaBytes == 0 {
		quotaBytes = 1024 * 1024 * 1024 // 1GB
	}

	// Start transaction
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to start transaction: %w", err)
	}
	defer tx.Rollback()

	// Create identity in our database (matching Prisma schema - no status column)
	var identity model.Identity
	var stalwartAcctID sql.NullString
	var colorNull sql.NullString
	identityUUID := uuid.New().String()
	err = tx.QueryRowContext(ctx, `
		INSERT INTO identities (uuid, user_id, domain_id, email, display_name, is_default,
		                        is_catch_all, color, password_hash, encrypted_password, quota_bytes, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NOW())
		RETURNING id, uuid, user_id, domain_id, email, display_name, is_default, is_catch_all, color,
		          quota_bytes, used_bytes, stalwart_account_id, created_at, updated_at
	`, identityUUID, userID, domainID, strings.ToLower(req.Email), req.DisplayName,
		req.IsDefault, req.IsCatchAll, assignedColor, string(passwordHash), encryptedPassword, quotaBytes).Scan(
		&identity.ID, &identity.UUID, &identity.UserID, &identity.DomainID,
		&identity.Email, &identity.DisplayName, &identity.IsDefault, &identity.IsCatchAll, &colorNull,
		&identity.QuotaBytes, &identity.UsedBytes, &stalwartAcctID,
		&identity.CreatedAt, &identity.UpdatedAt,
	)
	if colorNull.Valid {
		identity.Color = colorNull.String
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create identity: %w", err)
	}
	if stalwartAcctID.Valid {
		identity.StalwartAcctID = stalwartAcctID.String
	}

	// If this is marked as default, unset other defaults for this user
	if req.IsDefault {
		_, err = tx.ExecContext(ctx, `
			UPDATE identities SET is_default = false, updated_at = NOW()
			WHERE user_id = $1 AND id != $2
		`, userID, identity.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to update default identity: %w", err)
		}
	}

	// Create account in Stalwart
	stalwartID, err := s.stalwart.CreateAccount(ctx, StalwartAccountRequest{
		Email:       req.Email,
		Password:    req.Password,
		DisplayName: req.DisplayName,
		QuotaBytes:  quotaBytes,
	})
	if err != nil {
		// Log error but don't fail - we can sync later
		fmt.Printf("Warning: Failed to create Stalwart account: %v\n", err)
	} else {
		// Update with Stalwart account ID
		_, err = tx.ExecContext(ctx, `
			UPDATE identities SET stalwart_account_id = $1, updated_at = NOW() WHERE id = $2
		`, stalwartID, identity.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to update Stalwart account ID: %w", err)
		}
		identity.StalwartAcctID = stalwartID
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	identity.Status = "active" // Virtual field

	return &identity, nil
}

// GetIdentity retrieves an identity by UUID
func (s *IdentityService) GetIdentity(ctx context.Context, userID int64, identityUUID string) (*model.Identity, error) {
	var identity model.Identity
	var stalwartAcctID sql.NullString
	var colorNull sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, uuid, user_id, domain_id, email, display_name, is_default, is_catch_all, color,
		       stalwart_account_id, quota_bytes, used_bytes, created_at, updated_at
		FROM identities
		WHERE uuid = $1 AND user_id = $2
	`, identityUUID, userID).Scan(
		&identity.ID, &identity.UUID, &identity.UserID, &identity.DomainID,
		&identity.Email, &identity.DisplayName, &identity.IsDefault, &identity.IsCatchAll, &colorNull,
		&stalwartAcctID, &identity.QuotaBytes, &identity.UsedBytes,
		&identity.CreatedAt, &identity.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("identity not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query identity: %w", err)
	}

	if stalwartAcctID.Valid {
		identity.StalwartAcctID = stalwartAcctID.String
	}
	if colorNull.Valid {
		identity.Color = colorNull.String
	}
	identity.Status = "active" // Virtual field

	return &identity, nil
}

// ListIdentities returns all identities for a user
func (s *IdentityService) ListIdentities(ctx context.Context, userID int64) ([]*model.Identity, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, uuid, user_id, domain_id, email, display_name, is_default, is_catch_all, color,
		       stalwart_account_id, quota_bytes, used_bytes, created_at, updated_at
		FROM identities
		WHERE user_id = $1
		ORDER BY is_default DESC, email ASC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to query identities: %w", err)
	}
	defer rows.Close()

	var identities []*model.Identity
	for rows.Next() {
		var identity model.Identity
		var stalwartAcctID sql.NullString
		var colorNull sql.NullString
		if err := rows.Scan(&identity.ID, &identity.UUID, &identity.UserID, &identity.DomainID,
			&identity.Email, &identity.DisplayName, &identity.IsDefault, &identity.IsCatchAll, &colorNull,
			&stalwartAcctID, &identity.QuotaBytes, &identity.UsedBytes,
			&identity.CreatedAt, &identity.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan identity: %w", err)
		}
		if stalwartAcctID.Valid {
			identity.StalwartAcctID = stalwartAcctID.String
		}
		if colorNull.Valid {
			identity.Color = colorNull.String
		}
		identity.Status = "active" // Virtual field
		identities = append(identities, &identity)
	}

	return identities, nil
}

// UpdateIdentityPassword updates the password for an identity
func (s *IdentityService) UpdateIdentityPassword(ctx context.Context, userID int64, identityUUID string, newPassword string) error {
	// Get identity
	identity, err := s.GetIdentity(ctx, userID, identityUUID)
	if err != nil {
		return err
	}

	// Hash new password
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}

	// Encrypt new password for JMAP authentication
	encryptedPassword, err := crypto.Encrypt(newPassword, s.cfg.EncryptionKey)
	if err != nil {
		return fmt.Errorf("failed to encrypt password: %w", err)
	}

	// Update in database
	_, err = s.db.ExecContext(ctx, `
		UPDATE identities SET password_hash = $1, encrypted_password = $2, updated_at = NOW()
		WHERE id = $3
	`, string(passwordHash), encryptedPassword, identity.ID)
	if err != nil {
		return fmt.Errorf("failed to update password: %w", err)
	}

	// Update in Stalwart
	if identity.StalwartAcctID != "" {
		err = s.stalwart.UpdatePassword(ctx, identity.StalwartAcctID, newPassword)
		if err != nil {
			fmt.Printf("Warning: Failed to update Stalwart password: %v\n", err)
		}
	}

	return nil
}

// DeleteIdentity removes an identity
func (s *IdentityService) DeleteIdentity(ctx context.Context, userID int64, identityUUID string) error {
	// Get identity first
	identity, err := s.GetIdentity(ctx, userID, identityUUID)
	if err != nil {
		return err
	}

	// Delete from database
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM identities WHERE uuid = $1 AND user_id = $2
	`, identityUUID, userID)
	if err != nil {
		return fmt.Errorf("failed to delete identity: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("identity not found")
	}

	// Delete from Stalwart
	if identity.StalwartAcctID != "" {
		err = s.stalwart.DeleteAccount(ctx, identity.StalwartAcctID)
		if err != nil {
			fmt.Printf("Warning: Failed to delete Stalwart account: %v\n", err)
		}
	}

	return nil
}

// Stalwart API integration

type StalwartAccountRequest struct {
	Email       string
	Password    string
	DisplayName string
	QuotaBytes  int64
}

func (c *StalwartClient) CreateAccount(ctx context.Context, req StalwartAccountRequest) (string, error) {
	// Stalwart API endpoint for account creation
	endpoint := fmt.Sprintf("%s/api/principal", c.baseURL)

	// Stalwart 0.15.4+ requires explicit enabledPermissions for JMAP access
	body, _ := json.Marshal(map[string]interface{}{
		"type":        "individual",
		"name":        req.Email,
		"emails":      []string{req.Email},
		"secrets":     []string{req.Password},
		"description": req.DisplayName,
		"quota":       req.QuotaBytes,
		"enabledPermissions": []string{
			"authenticate",
			"email-send",
			"email-receive",
			"jmap-email-get",
			"jmap-mailbox-get",
			"jmap-thread-get",
			"jmap-email-query",
			"jmap-mailbox-query",
			"jmap-email-set",
			"jmap-mailbox-set",
			"jmap-email-changes",
			"jmap-mailbox-changes",
			"jmap-thread-changes",
			"jmap-blob-get",
			"jmap-email-copy",
			"jmap-email-import",
			"jmap-email-parse",
			"jmap-identity-get",
			"jmap-identity-set",
			"jmap-email-submission-get",
			"jmap-email-submission-set",
		},
	})

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.SetBasicAuth(c.username, c.password)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("stalwart API error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("stalwart returned %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		// Some versions may return empty body on success
		return req.Email, nil
	}

	// Return the account ID (usually the email)
	if data, ok := result["data"]; ok {
		if id, ok := data.(float64); ok {
			return fmt.Sprintf("%d", int(id)), nil
		}
	}
	return req.Email, nil
}

func (c *StalwartClient) UpdatePassword(ctx context.Context, accountID, newPassword string) error {
	endpoint := fmt.Sprintf("%s/api/principal/%s", c.baseURL, accountID)

	body, _ := json.Marshal(map[string]interface{}{
		"secrets": []string{newPassword},
	})

	req, err := http.NewRequestWithContext(ctx, "PATCH", endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("stalwart update password failed: %d", resp.StatusCode)
	}

	return nil
}

func (c *StalwartClient) DeleteAccount(ctx context.Context, accountID string) error {
	endpoint := fmt.Sprintf("%s/api/principal/%s", c.baseURL, accountID)

	req, err := http.NewRequestWithContext(ctx, "DELETE", endpoint, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.username, c.password)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 && resp.StatusCode != 404 {
		return fmt.Errorf("stalwart delete account failed: %d", resp.StatusCode)
	}

	return nil
}
