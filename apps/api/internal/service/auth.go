package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/model"
)

type AuthService struct {
	db  *sql.DB
	cfg *config.Config
}

func NewAuthService(db *sql.DB, cfg *config.Config) *AuthService {
	return &AuthService{db: db, cfg: cfg}
}

// Register creates the first admin user and organization.
// Registration is one-time only — once an owner exists, new users must be invited.
func (s *AuthService) Register(ctx context.Context, req *model.RegisterRequest) (*model.AuthResponse, error) {
	// One-time registration: reject if any users already exist
	var userCount int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&userCount)
	if err != nil {
		return nil, fmt.Errorf("failed to check existing users: %w", err)
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
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	// Generate org slug
	slug := generateSlug(orgName)

	// Start transaction
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to start transaction: %w", err)
	}
	defer tx.Rollback()

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
		return nil, fmt.Errorf("failed to create organization: %w", err)
	}

	// Create user (matching Prisma schema)
	var user model.User
	userUUID := uuid.New().String()
	err = tx.QueryRowContext(ctx, `
		INSERT INTO users (uuid, org_id, email, password_hash, name, role, status, email_verified, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'owner', 'active', false, NOW())
		RETURNING id, uuid, org_id, email, name, role, status, email_verified, created_at, updated_at
	`, userUUID, orgID, strings.ToLower(req.Email), string(passwordHash), req.Name).Scan(
		&user.ID, &user.UUID, &user.OrgID, &user.Email, &user.Name,
		&user.Role, &user.Status, &user.EmailVerified, &user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Generate JWT token
	token, err := s.generateToken(&user)
	if err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}

	return &model.AuthResponse{
		Token: token,
		User:  &user,
	}, nil
}

// Login authenticates a user and returns a JWT token
func (s *AuthService) Login(ctx context.Context, req *model.LoginRequest) (*model.AuthResponse, error) {
	var user model.User
	var passwordHash string

	err := s.db.QueryRowContext(ctx, `
		SELECT id, uuid, org_id, email, password_hash, name, role, status, email_verified,
		       last_login_at, created_at, updated_at
		FROM users
		WHERE email = $1 AND status = 'active'
	`, strings.ToLower(req.Email)).Scan(
		&user.ID, &user.UUID, &user.OrgID, &user.Email, &passwordHash, &user.Name,
		&user.Role, &user.Status, &user.EmailVerified, &user.LastLoginAt,
		&user.CreatedAt, &user.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("invalid email or password")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query user: %w", err)
	}

	// Verify password
	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.Password)); err != nil {
		return nil, fmt.Errorf("invalid email or password")
	}

	// Update last login
	go func() {
		database.DB.ExecContext(context.Background(), `
			UPDATE users SET last_login_at = NOW() WHERE id = $1
		`, user.ID)
	}()

	// Generate JWT token
	token, err := s.generateToken(&user)
	if err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}

	return &model.AuthResponse{
		Token: token,
		User:  &user,
	}, nil
}

// GetUserByID retrieves a user by ID
func (s *AuthService) GetUserByID(ctx context.Context, userID int64) (*model.User, error) {
	var user model.User

	err := s.db.QueryRowContext(ctx, `
		SELECT id, uuid, org_id, email, name, role, status, email_verified,
		       last_login_at, created_at, updated_at
		FROM users
		WHERE id = $1
	`, userID).Scan(
		&user.ID, &user.UUID, &user.OrgID, &user.Email, &user.Name,
		&user.Role, &user.Status, &user.EmailVerified, &user.LastLoginAt,
		&user.CreatedAt, &user.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("user not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query user: %w", err)
	}

	return &user, nil
}

// CreateAPIKey generates a new API key for the organization
func (s *AuthService) CreateAPIKey(ctx context.Context, orgID int64, req *model.CreateApiKeyRequest) (*model.ApiKeyResponse, error) {
	// Generate API key
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		return nil, fmt.Errorf("failed to generate key: %w", err)
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
		if err == nil {
			expiresAt = sql.NullTime{Time: t, Valid: true}
		}
	}

	// Set default rate limit if not provided
	rateLimit := req.RateLimit
	if rateLimit <= 0 {
		rateLimit = 100 // default 100 requests per minute
	}
	if rateLimit > 10000 {
		rateLimit = 10000 // max 10000 requests per minute
	}

	// Insert into database
	var result model.ApiKeyResponse
	keyUUID := uuid.New().String()
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO api_keys (uuid, org_id, name, key_prefix, key_hash, permissions, rate_limit, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, uuid, name, key_prefix, permissions, rate_limit, expires_at, created_at
	`, keyUUID, orgID, req.Name, keyPrefix, keyHash, pq.Array(req.Permissions), rateLimit, expiresAt).Scan(
		&result.ID, &result.UUID, &result.Name, &result.KeyPrefix,
		pq.Array(&result.Permissions), &result.RateLimit, &result.ExpiresAt, &result.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create API key: %w", err)
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
		return nil, fmt.Errorf("failed to query API keys: %w", err)
	}
	defer rows.Close()

	var keys []*model.ApiKeyResponse
	for rows.Next() {
		var key model.ApiKeyResponse
		var lastUsedAt sql.NullTime
		if err := rows.Scan(&key.ID, &key.UUID, &key.Name, &key.KeyPrefix,
			pq.Array(&key.Permissions), &key.RateLimit, &lastUsedAt, &key.ExpiresAt, &key.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan API key: %w", err)
		}
		if lastUsedAt.Valid {
			key.LastUsedAt = &lastUsedAt.Time
		}
		keys = append(keys, &key)
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
		return fmt.Errorf("failed to delete API key: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("API key not found")
	}

	return nil
}

func (s *AuthService) generateToken(user *model.User) (string, error) {
	// Parse expiry duration (e.g., "7d")
	expiry := 7 * 24 * time.Hour // default 7 days
	if s.cfg.JWTExpiresIn != "" {
		if d, err := time.ParseDuration(s.cfg.JWTExpiresIn); err == nil {
			expiry = d
		}
	}

	claims := jwt.MapClaims{
		"userId": user.ID,
		"orgId":  user.OrgID,
		"email":  user.Email,
		"role":   user.Role,
		"exp":    time.Now().Add(expiry).Unix(),
		"iat":    time.Now().Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(s.cfg.JWTSecret))
}

func generateSlug(name string) string {
	slug := strings.ToLower(name)
	slug = strings.ReplaceAll(slug, " ", "-")
	// Add random suffix for uniqueness
	suffix := make([]byte, 4)
	rand.Read(suffix)
	return fmt.Sprintf("%s-%s", slug, hex.EncodeToString(suffix))
}
