package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
)

type AuthService struct {
	db  *sql.DB
	cfg *config.Config
}

func NewAuthService(db *sql.DB, cfg *config.Config) *AuthService {
	return &AuthService{db: db, cfg: cfg}
}

// dummyPasswordHash equalizes Login timing for unknown accounts and accounts
// without a password (OAuth-only), so response time does not reveal which
// emails exist. Same cost as real hashes.
var dummyPasswordHash = func() []byte {
	h, err := bcrypt.GenerateFromPassword([]byte("mailat-timing-equalizer"), bcrypt.DefaultCost)
	if err != nil {
		panic("generate dummy password hash: " + err.Error())
	}
	return h
}()

// validateRegistration enforces the registration policy: a single valid
// address and a password bcrypt can hash without truncation (8–72 bytes).
func validateRegistration(email, password string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > 255 {
		return "", fmt.Errorf("enter a valid email address")
	}
	if len(password) < 8 || len(password) > 72 {
		return "", fmt.Errorf("password must be 8 to 72 bytes long")
	}
	return email, nil
}

// Register creates the first admin user and organization.
// Registration is one-time only — once an owner exists, new users must be invited.
func (s *AuthService) Register(ctx context.Context, req *model.RegisterRequest) (*model.AuthResponse, error) {
	email, err := validateRegistration(req.Email, req.Password)
	if err != nil {
		return nil, err
	}
	// One-time registration: reject if any users already exist
	var userCount int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&userCount)
	if err != nil {
		return nil, fmt.Errorf("failed to check existing users")
	}
	if userCount > 0 {
		return nil, fmt.Errorf("registration is closed — contact your admin for an invite")
	}

	// Auto-generate org name if not provided
	orgName := req.OrgName
	if orgName == "" {
		orgName = req.Name + "'s Workspace"
	}

	// Hash password
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password")
	}

	// Generate org slug
	slug := generateSlug(orgName)

	// Start transaction
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to start transaction")
	}
	defer tx.Rollback()

	// Serialize initial setup so concurrent registrations cannot create extra owners.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(20261004,1)`); err != nil {
		return nil, fmt.Errorf("unable to check registration")
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&userCount); err != nil {
		return nil, fmt.Errorf("unable to check registration")
	}
	if userCount > 0 {
		return nil, fmt.Errorf("registration is closed — contact your admin for an invite")
	}

	// Create organization (matching Prisma schema) with configurable limits
	var orgID int64
	orgUUID := uuid.New().String()
	err = tx.QueryRowContext(ctx, `
		INSERT INTO organizations (uuid, name, slug, plan, monthly_email_limit, max_domains, max_identities, max_contacts, updated_at)
		VALUES ($1, $2, $3, 'free', $4, $5, $6, $7, NOW())
		RETURNING id
	`, orgUUID, orgName, slug,
		s.cfg.DefaultMonthlyEmailLimit,
		s.cfg.DefaultMaxDomains,
		s.cfg.DefaultMaxIdentities,
		s.cfg.DefaultMaxContacts,
	).Scan(&orgID)
	if err != nil {
		return nil, fmt.Errorf("failed to create organization")
	}

	// Create user (matching Prisma schema)
	var user model.User
	userUUID := uuid.New().String()
	err = tx.QueryRowContext(ctx, `
		INSERT INTO users (uuid, org_id, email, password_hash, name, role, status, email_verified, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'owner', 'active', false, NOW())
		RETURNING id, uuid, org_id, email, name, role, status, email_verified, created_at, updated_at
	`, userUUID, orgID, email, string(passwordHash), req.Name).Scan(
		&user.ID, &user.UUID, &user.OrgID, &user.Email, &user.Name,
		&user.Role, &user.Status, &user.EmailVerified, &user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create user")
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction")
	}

	// Generate JWT token
	token, err := s.issueSession(ctx, &user, 0, false)
	if err != nil {
		return nil, fmt.Errorf("failed to generate token")
	}

	return &model.AuthResponse{
		Token: token,
		User:  &user,
	}, nil
}

// Login authenticates a user and returns a JWT token
func (s *AuthService) Login(ctx context.Context, req *model.LoginRequest) (*model.LoginResponse, error) {
	var id, version int64
	var passwordHash string
	err := s.db.QueryRowContext(ctx, `SELECT id,password_hash,auth_version FROM users WHERE email=$1 AND status='active'`, strings.ToLower(strings.TrimSpace(req.Email))).Scan(&id, &passwordHash, &version)
	if err != nil || passwordHash == "" {
		// Spend the same bcrypt work as a real comparison before failing.
		_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte(req.Password))
		return nil, fmt.Errorf("invalid email or password")
	}
	if bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.Password)) != nil {
		return nil, fmt.Errorf("invalid email or password")
	}
	return s.authenticateUser(ctx, id, &version)
}

// GetUserByID retrieves a user by ID
func (s *AuthService) GetUserByID(ctx context.Context, userID int64) (*model.User, error) {
	var user model.User

	err := s.db.QueryRowContext(ctx, `
		SELECT id, uuid, org_id, email, name, role, status, email_verified,
		       last_login_at, created_at, updated_at
		FROM users
		WHERE id = $1 AND status='active'
	`, userID).Scan(
		&user.ID, &user.UUID, &user.OrgID, &user.Email, &user.Name,
		&user.Role, &user.Status, &user.EmailVerified, &user.LastLoginAt,
		&user.CreatedAt, &user.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("user not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query user")
	}

	return &user, nil
}

// CreateAPIKey generates a new API key for the organization
func (s *AuthService) CreateAPIKey(ctx context.Context, orgID int64, userID int64, req *model.CreateApiKeyRequest) (*model.ApiKeyResponse, error) {
	// Validate the advertised contract before creating a credential. Unknown scopes
	// must not become accidentally privileged when new routes are added later.
	if len(req.Permissions) == 0 {
		return nil, fmt.Errorf("select at least one permission")
	}
	seen := map[string]bool{}
	for _, permission := range req.Permissions {
		if !middleware.APIKeyPermissions[permission] || seen[permission] {
			return nil, fmt.Errorf("invalid or duplicate API permission")
		}
		seen[permission] = true
	}
	var allowed bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND org_id=$2 AND status='active' AND role IN ('owner','admin'))`, userID, orgID).Scan(&allowed); err != nil || !allowed {
		return nil, fmt.Errorf("only workspace administrators can create API keys")
	}
	// Generate API key
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		return nil, fmt.Errorf("failed to generate key")
	}
	apiKey := "ue_" + hex.EncodeToString(keyBytes)
	keyPrefix := apiKey[:10]

	// Hash the key for storage
	hash := sha256.Sum256([]byte(apiKey))
	keyHash := hex.EncodeToString(hash[:])

	// Parse expiry
	var expiresAt sql.NullTime
	if req.ExpiresAt != nil {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil || !t.After(time.Now()) {
			return nil, fmt.Errorf("expiresAt must be a future RFC3339 timestamp")
		}
		expiresAt = sql.NullTime{Time: t, Valid: true}
	}

	// Set default rate limit if not provided
	rateLimit := req.RateLimit
	if rateLimit == 0 {
		rateLimit = 100
	}
	if rateLimit < 1 || rateLimit > 10000 {
		return nil, fmt.Errorf("rateLimit must be between 1 and 10000 requests per minute")
	}

	// Insert into database
	var result model.ApiKeyResponse
	keyUUID := uuid.New().String()
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO api_keys (uuid, org_id, user_id, name, key_prefix, key_hash, permissions, rate_limit, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, uuid, name, key_prefix, permissions, rate_limit, expires_at, created_at
	`, keyUUID, orgID, userID, req.Name, keyPrefix, keyHash, pq.Array(req.Permissions), rateLimit, expiresAt).Scan(
		&result.ID, &result.UUID, &result.Name, &result.KeyPrefix,
		pq.Array(&result.Permissions), &result.RateLimit, &result.ExpiresAt, &result.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to create API key")
	}

	// Only return full key on creation
	result.Key = apiKey

	return &result, nil
}

// ListAPIKeys returns all API keys for an organization
func (s *AuthService) ListAPIKeys(ctx context.Context, orgID int64) ([]*model.ApiKeyResponse, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, uuid, name, key_prefix, permissions, rate_limit, last_used_at, expires_at, created_at
		FROM api_keys
		WHERE org_id = $1 AND (expires_at IS NULL OR expires_at > NOW())
		ORDER BY created_at DESC
	`, orgID)
	if err != nil {
		return nil, fmt.Errorf("unable to list API keys")
	}
	defer rows.Close()

	keys := make([]*model.ApiKeyResponse, 0)
	for rows.Next() {
		var key model.ApiKeyResponse
		var lastUsedAt sql.NullTime
		if err := rows.Scan(&key.ID, &key.UUID, &key.Name, &key.KeyPrefix,
			pq.Array(&key.Permissions), &key.RateLimit, &lastUsedAt, &key.ExpiresAt, &key.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan API key")
		}
		if lastUsedAt.Valid {
			key.LastUsedAt = &lastUsedAt.Time
		}
		keys = append(keys, &key)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("unable to list API keys")
	}
	return keys, nil
}

// DeleteAPIKey deletes an API key
func (s *AuthService) DeleteAPIKey(ctx context.Context, orgID int64, keyUUID string) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM api_keys
		WHERE uuid = $1 AND org_id = $2
	`, keyUUID, orgID)
	if err != nil {
		return fmt.Errorf("unable to revoke API key")
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("API key not found")
	}

	return nil
}

// IsRegistrationOpen checks if registration is still available (no users exist yet)
func (s *AuthService) IsRegistrationOpen(ctx context.Context) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check users")
	}
	return count == 0, nil
}

func (s *AuthService) sessionExpiry() time.Time {
	// config.Load rejects bad values at startup; directly built configs (tests)
	// fall back to the documented 7d default instead of failing silently.
	expiry, err := config.ParseSessionDuration(s.cfg.JWTExpiresIn)
	if err != nil {
		expiry = 7 * 24 * time.Hour
	}
	return time.Now().Add(expiry).Truncate(time.Second)
}
func (s *AuthService) generateToken(user *model.User) (string, error) {
	claims := model.AccessClaims{UserID: user.ID, OrgID: user.OrgID, Purpose: "session", RegisteredClaims: jwt.RegisteredClaims{Issuer: "mailat", Audience: jwt.ClaimStrings{"mailat-api"}, ID: uuid.NewString(), IssuedAt: jwt.NewNumericDate(time.Now()), ExpiresAt: jwt.NewNumericDate(s.sessionExpiry())}}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.cfg.JWTSecret))
}

func generateSlug(name string) string {
	slug := strings.ToLower(name)
	slug = strings.ReplaceAll(slug, " ", "-")
	// Add random suffix for uniqueness
	suffix := make([]byte, 4)
	rand.Read(suffix)
	return fmt.Sprintf("%s-%s", slug, hex.EncodeToString(suffix))
}
