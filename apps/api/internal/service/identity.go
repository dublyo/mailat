package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"regexp"
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
	var domainName string
	var sesVerified bool
	var userOrgID int64

	err := s.db.QueryRowContext(ctx, `
		SELECT d.id, d.org_id, d.status, u.org_id, d.name, COALESCE(d.ses_verified,false)
		FROM domains d
		JOIN users u ON u.id = $1
		WHERE d.uuid = $2
	`, userID, req.DomainId).Scan(&domainID, &domainOrgID, &domainStatus, &userOrgID, &domainName, &sesVerified)
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
	if s.cfg.EmailProvider == "ses" && !sesVerified {
		return nil, fmt.Errorf("verify the domain with SES before creating an address")
	}
	address, err := identityAddressForDomain(req.Email, domainName)
	if err != nil {
		return nil, err
	}
	req.Email = address
	if s.cfg.EmailProvider != "ses" && len(req.Password) < 8 {
		return nil, fmt.Errorf("password must be at least 8 characters for an SMTP mailbox")
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

	// SES addresses have no mailbox password; only legacy JMAP needs it.
	var passwordHash []byte
	var encryptedPassword string
	if s.cfg.EmailProvider != "ses" {
		passwordHash, err = bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmt.Errorf("failed to hash password: %w", err)
		}
		encryptedPassword, err = crypto.Encrypt(req.Password, s.cfg.EncryptionKey)
		if err != nil {
			return nil, fmt.Errorf("failed to encrypt password: %w", err)
		}
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
	// Lock the user then domain in a consistent order so concurrent creates and
	// updates cannot produce two defaults or catch-alls.
	if _, err = tx.ExecContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, userID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT id FROM domains WHERE id=$1 FOR UPDATE`, domainID); err != nil {
		return nil, err
	}
	if !s.cfg.DisableAppLimits {
		var limit, count int
		if err = tx.QueryRowContext(ctx, `SELECT max_identities FROM organizations WHERE id=$1 FOR UPDATE`, userOrgID).Scan(&limit); err != nil {
			return nil, err
		}
		// Count after acquiring the lock so a waiting transaction sees the
		// previous creator's committed identity in a fresh READ COMMITTED snapshot.
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM identities i JOIN users u ON u.id=i.user_id WHERE u.org_id=$1`, userOrgID).Scan(&count); err != nil {
			return nil, err
		}
		if limit > 0 && count >= limit {
			return nil, fmt.Errorf("organization identity limit reached")
		}
	}
	if req.IsDefault {
		if _, err = tx.ExecContext(ctx, `UPDATE identities SET is_default=false, updated_at=now() WHERE user_id=$1 AND is_default`, userID); err != nil {
			return nil, err
		}
	}

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

	// SES never needs a second mailbox server.
	if s.cfg.EmailProvider != "ses" {
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
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	identity.Status = "active" // Virtual field
	identity.CanSend, identity.CanReceive = true, true

	return &identity, nil
}

func identityAddressForDomain(value, domain string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || strings.ContainsAny(value, "\r\n") || len(value) > 255 {
		return "", fmt.Errorf("enter a valid email address without a display name")
	}
	parts := strings.Split(value, "@")
	if len(parts) != 2 || !strings.EqualFold(parts[1], domain) {
		return "", fmt.Errorf("email address must belong to the selected domain")
	}
	return strings.ToLower(value), nil
}

func (s *IdentityService) UpdateIdentity(ctx context.Context, userID int64, identityUUID string, req *model.UpdateIdentityRequest) (*model.Identity, error) {
	if req.DisplayName != nil && (len(*req.DisplayName) > 255 || strings.ContainsAny(*req.DisplayName, "\r\n")) {
		return nil, fmt.Errorf("invalid display name")
	}
	if req.Color != nil && !regexp.MustCompile(`^#[0-9a-fA-F]{6}$`).MatchString(*req.Color) {
		return nil, fmt.Errorf("invalid identity color")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, userID); err != nil {
		return nil, err
	}
	var id, domainID int64
	if err = tx.QueryRowContext(ctx, `SELECT id,domain_id FROM identities WHERE uuid=$1 AND user_id=$2 FOR UPDATE`, identityUUID, userID).Scan(&id, &domainID); err != nil {
		return nil, fmt.Errorf("identity not found")
	}
	if _, err = tx.ExecContext(ctx, `SELECT id FROM domains WHERE id=$1 FOR UPDATE`, domainID); err != nil {
		return nil, err
	}
	if req.IsCatchAll != nil && *req.IsCatchAll {
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM identities WHERE domain_id=$1 AND is_catch_all AND id<>$2)`, domainID, id).Scan(&exists); err != nil {
			return nil, err
		}
		if exists {
			return nil, fmt.Errorf("this domain already has a catch-all address")
		}
	}
	if req.IsDefault != nil && *req.IsDefault {
		if _, err = tx.ExecContext(ctx, `UPDATE identities SET is_default=false,updated_at=now() WHERE user_id=$1 AND id<>$2 AND is_default`, userID, id); err != nil {
			return nil, err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE identities SET display_name=COALESCE($1,display_name),is_default=COALESCE($2,is_default),is_catch_all=COALESCE($3,is_catch_all),color=COALESCE($4,color),updated_at=now() WHERE id=$5`, req.DisplayName, req.IsDefault, req.IsCatchAll, req.Color, id)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetIdentity(ctx, userID, identityUUID)
}

// GetIdentity retrieves an identity by UUID
func (s *IdentityService) GetIdentity(ctx context.Context, userID int64, identityUUID string) (*model.Identity, error) {
	var identity model.Identity
	var stalwartAcctID sql.NullString
	var colorNull sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, uuid, user_id, domain_id, email, display_name, is_default, is_catch_all, color,
		       stalwart_account_id, quota_bytes, used_bytes, created_at, updated_at, can_send, can_receive
		FROM identities
		WHERE uuid = $1 AND user_id = $2
	`, identityUUID, userID).Scan(
		&identity.ID, &identity.UUID, &identity.UserID, &identity.DomainID,
		&identity.Email, &identity.DisplayName, &identity.IsDefault, &identity.IsCatchAll, &colorNull,
		&stalwartAcctID, &identity.QuotaBytes, &identity.UsedBytes,
		&identity.CreatedAt, &identity.UpdatedAt, &identity.CanSend, &identity.CanReceive,
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
		       stalwart_account_id, quota_bytes, used_bytes, created_at, updated_at, can_send, can_receive
		FROM identities
		WHERE user_id = $1
		ORDER BY is_default DESC, email ASC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to query identities: %w", err)
	}
	defer rows.Close()

	identities := make([]*model.Identity, 0)
	for rows.Next() {
		var identity model.Identity
		var stalwartAcctID sql.NullString
		var colorNull sql.NullString
		if err := rows.Scan(&identity.ID, &identity.UUID, &identity.UserID, &identity.DomainID,
			&identity.Email, &identity.DisplayName, &identity.IsDefault, &identity.IsCatchAll, &colorNull,
			&stalwartAcctID, &identity.QuotaBytes, &identity.UsedBytes,
			&identity.CreatedAt, &identity.UpdatedAt, &identity.CanSend, &identity.CanReceive); err != nil {
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

	return identities, rows.Err()
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
