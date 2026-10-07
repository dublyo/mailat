package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/pkg/crypto"
)

var ErrIdentityNotFound = errors.New("identity not found")

// ErrIdentityAdminRequired is returned when a non-admin changes catch-all or receiving.
var ErrIdentityAdminRequired = errors.New("only an organization owner or admin can change catch-all or receiving")

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
	// An admin may create the identity for another active member of the org.
	ownerID := userID
	if req.OwnerUserUuid != "" {
		if _, err = uuid.Parse(req.OwnerUserUuid); err != nil {
			return nil, fmt.Errorf("owner is not an active member of this organization")
		}
		err = s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE uuid=$1 AND org_id=$2 AND status='active'`, req.OwnerUserUuid, userOrgID).Scan(&ownerID)
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("owner is not an active member of this organization")
		}
		if err != nil {
			return nil, fmt.Errorf("failed to verify owner: %w", err)
		}
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
		SELECT COUNT(*) FROM identities WHERE user_id = $1 AND kind='personal'
	`, ownerID).Scan(&identityCount)
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
	if _, err = tx.ExecContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, ownerID); err != nil {
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
		if _, err = tx.ExecContext(ctx, `UPDATE identities SET is_default=false, updated_at=now() WHERE user_id=$1 AND kind='personal' AND is_default`, ownerID); err != nil {
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
	`, identityUUID, ownerID, domainID, strings.ToLower(req.Email), req.DisplayName,
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
			WHERE user_id = $1 AND kind='personal' AND id != $2
		`, ownerID, identity.ID)
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
	identity.Kind = "personal"

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

// UpdateIdentity changes one of the caller's personal identities. Catch-all
// and receiving changes need isAdmin (owner or admin).
func (s *IdentityService) UpdateIdentity(ctx context.Context, userID int64, identityUUID string, req *model.UpdateIdentityRequest, isAdmin bool) (*model.Identity, error) {
	if !isAdmin && (req.IsCatchAll != nil || req.CanReceive != nil) {
		return nil, ErrIdentityAdminRequired
	}
	if _, err := uuid.Parse(identityUUID); err != nil {
		return nil, ErrIdentityNotFound
	}
	if req.DisplayName != nil && (len(*req.DisplayName) > 255 || strings.ContainsAny(*req.DisplayName, "\r\n")) {
		return nil, fmt.Errorf("invalid display name")
	}
	if req.Color != nil && !regexp.MustCompile(`^#[0-9a-fA-F]{6}$`).MatchString(*req.Color) {
		return nil, fmt.Errorf("invalid identity color")
	}
	for _, sig := range []*string{req.SignatureHtml, req.SignatureText} {
		if sig != nil && (len(*sig) > maxSignatureBytes || strings.ContainsRune(*sig, 0)) {
			return nil, fmt.Errorf("signature must be at most 20 KB")
		}
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
	if err = tx.QueryRowContext(ctx, `SELECT id,domain_id FROM identities WHERE uuid=$1 AND user_id=$2 AND kind='personal' FOR UPDATE`, identityUUID, userID).Scan(&id, &domainID); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrIdentityNotFound
		}
		return nil, err
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
		if _, err = tx.ExecContext(ctx, `UPDATE identities SET is_default=false,updated_at=now() WHERE user_id=$1 AND kind='personal' AND id<>$2 AND is_default`, userID, id); err != nil {
			return nil, err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE identities SET display_name=COALESCE($1,display_name),is_default=COALESCE($2,is_default),is_catch_all=COALESCE($3,is_catch_all),color=COALESCE($4,color),can_receive=COALESCE($6,can_receive),
	 signature_html=COALESCE($7,signature_html),signature_text=COALESCE($8,signature_text),updated_at=now() WHERE id=$5`,
		req.DisplayName, req.IsDefault, req.IsCatchAll, req.Color, id, req.CanReceive, req.SignatureHtml, req.SignatureText)
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
	if _, err := uuid.Parse(identityUUID); err != nil {
		return nil, ErrIdentityNotFound
	}
	var identity model.Identity
	var stalwartAcctID sql.NullString
	var colorNull sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, uuid, user_id, domain_id, email, COALESCE(display_name, ''), is_default, is_catch_all, color,
		       stalwart_account_id, quota_bytes, used_bytes, created_at, updated_at, can_send, can_receive, kind,
		       COALESCE(signature_html, ''), COALESCE(signature_text, ''), wildcard_sender
		FROM identities
		WHERE uuid = $1 AND user_id = $2 AND kind = 'personal'
	`, identityUUID, userID).Scan(
		&identity.ID, &identity.UUID, &identity.UserID, &identity.DomainID,
		&identity.Email, &identity.DisplayName, &identity.IsDefault, &identity.IsCatchAll, &colorNull,
		&stalwartAcctID, &identity.QuotaBytes, &identity.UsedBytes,
		&identity.CreatedAt, &identity.UpdatedAt, &identity.CanSend, &identity.CanReceive, &identity.Kind,
		&identity.SignatureHtml, &identity.SignatureText, &identity.WildcardSender,
	)

	if err == sql.ErrNoRows {
		return nil, ErrIdentityNotFound
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
	if err = s.attachSendAliases(ctx, []*model.Identity{&identity}); err != nil {
		return nil, err
	}
	return &identity, nil
}

// ListIdentities returns all identities for a user
func (s *IdentityService) ListIdentities(ctx context.Context, userID int64) ([]*model.Identity, error) {
	// Personal identities the user owns, then shared identities the user is a
	// member of; for those, canSend is the member's permission and the identity's.
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, uuid, user_id, domain_id, email, COALESCE(display_name, ''), is_default, is_catch_all, color,
		       stalwart_account_id, quota_bytes, used_bytes, created_at, updated_at, can_send, can_receive, kind,
		       true, false, '', '', COALESCE(signature_html, ''), COALESCE(signature_text, ''), wildcard_sender
		FROM identities
		WHERE user_id = $1 AND kind = 'personal'
		UNION ALL
		SELECT i.id, i.uuid, i.user_id, i.domain_id, i.email, COALESCE(i.display_name, ''), false, false, i.color,
		       i.stalwart_account_id, i.quota_bytes, i.used_bytes, i.created_at, i.updated_at, i.can_send AND m.can_send, i.can_receive, i.kind,
		       m.can_read, m.can_manage, sm.uuid::text, sm.name, '', '', false
		FROM shared_mailbox_members m JOIN shared_mailboxes sm ON sm.id = m.shared_mailbox_id
		JOIN identities i ON i.id = sm.identity_id AND i.kind = 'shared'
		WHERE m.user_id = $1
		ORDER BY 17, 7 DESC, 5
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
			&identity.CreatedAt, &identity.UpdatedAt, &identity.CanSend, &identity.CanReceive, &identity.Kind,
			&identity.CanRead, &identity.CanManage, &identity.SharedMailboxUuid, &identity.SharedMailboxName,
			&identity.SignatureHtml, &identity.SignatureText, &identity.WildcardSender); err != nil {
			return nil, fmt.Errorf("failed to scan identity: %w", err)
		}
		if stalwartAcctID.Valid {
			identity.StalwartAcctID = stalwartAcctID.String
		}
		if colorNull.Valid {
			identity.Color = colorNull.String
		}
		if identity.Kind == "shared" {
			// The steward is not exposed to members.
			identity.Shared, identity.UserID = true, 0
		}
		identity.Status = "active" // Virtual field
		identities = append(identities, &identity)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if err = s.attachSendAliases(ctx, identities); err != nil {
		return nil, err
	}
	return identities, nil
}

const maxSignatureBytes = 20 * 1024

// attachSendAliases fills SendAliases for personal identities.
func (s *IdentityService) attachSendAliases(ctx context.Context, identities []*model.Identity) error {
	byID := map[int64]*model.Identity{}
	ids := []int64{}
	for _, identity := range identities {
		if identity.Kind == "personal" {
			identity.SendAliases = []string{}
			byID[identity.ID] = identity
			ids = append(ids, identity.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT identity_id,address FROM identity_send_aliases WHERE identity_id=ANY($1) ORDER BY address`, pq.Array(ids))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var address string
		if err = rows.Scan(&id, &address); err != nil {
			return err
		}
		byID[id].SendAliases = append(byID[id].SendAliases, address)
	}
	return rows.Err()
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

// DeleteIdentity removes a personal identity of the caller's organization and
// its received mail. The route is admin-only; stored objects are queued for
// cleanup in the same transaction.
func (s *IdentityService) DeleteIdentity(ctx context.Context, userID int64, identityUUID string) error {
	if _, err := uuid.Parse(identityUUID); err != nil {
		return ErrIdentityNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	var stalwartID sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT i.id,i.stalwart_account_id FROM identities i JOIN domains d ON d.id=i.domain_id
		WHERE i.uuid=$1 AND i.kind='personal' AND d.org_id=(SELECT org_id FROM users WHERE id=$2) FOR UPDATE OF i`, identityUUID, userID).Scan(&id, &stalwartID)
	if err == sql.ErrNoRows {
		return ErrIdentityNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to load identity: %w", err)
	}
	if err = queueMailStorageCleanup(ctx, tx, `e.identity_id=$1`, id); err != nil {
		return fmt.Errorf("failed to queue storage cleanup: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM identities WHERE id=$1`, id); err != nil {
		return fmt.Errorf("failed to delete identity: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if stalwartID.Valid && stalwartID.String != "" {
		if err = s.stalwart.DeleteAccount(ctx, stalwartID.String); err != nil {
			fmt.Printf("Warning: Failed to delete Stalwart account: %v\n", err)
		}
	}
	return nil
}

// queueMailStorageCleanup queues the raw and attachment objects of the
// received_emails rows matching where (alias e). The cleanup worker rechecks
// every object for remaining references before deleting it.
func queueMailStorageCleanup(ctx context.Context, tx *sql.Tx, where string, args ...any) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO storage_cleanup_jobs(bucket,object_key)
 SELECT e.raw_s3_bucket,e.raw_s3_key FROM received_emails e WHERE `+where+` AND COALESCE(e.raw_s3_bucket,'')!='' AND COALESCE(e.raw_s3_key,'')!=''
 UNION SELECT a.s3_bucket,a.s3_key FROM email_attachments a JOIN received_emails e ON e.id=a.received_email_id WHERE `+where+` AND a.s3_bucket!='' AND a.s3_key!=''
 ON CONFLICT(bucket,object_key) DO UPDATE SET next_attempt_at=NOW()`, args...)
	return err
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
