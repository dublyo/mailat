package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/dublyo/mailat/api/internal/config"
)

// OAuthProvider represents a supported OAuth provider
type OAuthProvider string

const (
	ProviderGoogle    OAuthProvider = "google"
	ProviderGitHub    OAuthProvider = "github"
	ProviderMicrosoft OAuthProvider = "microsoft"
)

// OAuthConfig holds OAuth2 configuration for a provider
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	UserInfoURL  string
	// EmailsURL lists GitHub addresses with their verified flag.
	EmailsURL   string
	Scopes      []string
	RedirectURL string
}

// OAuthUserInfo holds user information from an OAuth provider
type OAuthUserInfo struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	// EmailVerified is true only when the provider asserts the address is
	// verified. Only verified identities may bootstrap the first owner.
	EmailVerified bool `json:"email_verified"`
}

// OAuthConnection represents a stored OAuth connection
type OAuthConnection struct {
	ID             int       `json:"id"`
	UserID         int       `json:"userId"`
	Provider       string    `json:"provider"`
	ProviderUserID string    `json:"providerUserId"`
	Email          string    `json:"email,omitempty"`
	Name           string    `json:"name,omitempty"`
	AvatarURL      string    `json:"avatarUrl,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// OAuthService handles OAuth2 authentication
type OAuthService struct {
	db         *sql.DB
	cfg        *config.Config
	configs    map[OAuthProvider]*OAuthConfig
	httpClient *http.Client
}

// NewOAuthService creates a new OAuth service
func NewOAuthService(db *sql.DB, cfg *config.Config) *OAuthService {
	service := &OAuthService{
		db:         db,
		cfg:        cfg,
		configs:    make(map[OAuthProvider]*OAuthConfig),
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}

	// Configure providers from config
	service.configureProviders()

	return service
}

// configureProviders sets up OAuth configurations for each provider
func (s *OAuthService) configureProviders() {
	// Use API URL for OAuth callbacks (not WebUrl which is frontend)
	apiURL := s.cfg.APIUrl

	// Google OAuth2
	if s.cfg.GoogleClientID != "" && s.cfg.GoogleClientSecret != "" {
		s.configs[ProviderGoogle] = &OAuthConfig{
			ClientID:     s.cfg.GoogleClientID,
			ClientSecret: s.cfg.GoogleClientSecret,
			AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL:     "https://oauth2.googleapis.com/token",
			UserInfoURL:  "https://www.googleapis.com/oauth2/v2/userinfo",
			Scopes:       []string{"email", "profile"},
			RedirectURL:  apiURL + "/api/v1/oauth/google/callback",
		}
	}

	// GitHub OAuth2
	if s.cfg.GitHubClientID != "" && s.cfg.GitHubClientSecret != "" {
		s.configs[ProviderGitHub] = &OAuthConfig{
			ClientID:     s.cfg.GitHubClientID,
			ClientSecret: s.cfg.GitHubClientSecret,
			AuthURL:      "https://github.com/login/oauth/authorize",
			TokenURL:     "https://github.com/login/oauth/access_token",
			UserInfoURL:  "https://api.github.com/user",
			EmailsURL:    "https://api.github.com/user/emails",
			Scopes:       []string{"user:email"},
			RedirectURL:  apiURL + "/api/v1/oauth/github/callback",
		}
	}

	// Microsoft OAuth2
	if s.cfg.MicrosoftClientID != "" && s.cfg.MicrosoftClientSecret != "" {
		s.configs[ProviderMicrosoft] = &OAuthConfig{
			ClientID:     s.cfg.MicrosoftClientID,
			ClientSecret: s.cfg.MicrosoftClientSecret,
			AuthURL:      "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
			TokenURL:     "https://login.microsoftonline.com/common/oauth2/v2.0/token",
			UserInfoURL:  "https://graph.microsoft.com/v1.0/me",
			Scopes:       []string{"openid", "email", "profile"},
			RedirectURL:  apiURL + "/api/v1/oauth/microsoft/callback",
		}
	}
}

// GetAuthURL returns the authorization URL for a provider
func (s *OAuthService) GetAuthURL(provider OAuthProvider, state string) (string, error) {
	cfg, ok := s.configs[provider]
	if !ok {
		return "", fmt.Errorf("unsupported OAuth provider: %s", provider)
	}

	params := url.Values{
		"client_id":     {cfg.ClientID},
		"redirect_uri":  {cfg.RedirectURL},
		"response_type": {"code"},
		"scope":         {strings.Join(cfg.Scopes, " ")},
		"state":         {state},
	}
	// No offline access: Mailat only reads the profile once per sign-in and
	// never stores provider tokens.

	return cfg.AuthURL + "?" + params.Encode(), nil
}

// Typed OAuth outcomes. Controllers map them to fixed error codes; provider
// text is never echoed to the browser.
var (
	ErrOAuthInvalidState    = errors.New("invalid or expired OAuth state")
	ErrOAuthProvider        = errors.New("OAuth provider request failed")
	ErrOAuthNotLinked       = errors.New("this sign-in is not linked to a Mailat account")
	ErrOAuthEmailUnverified = errors.New("the provider did not confirm this email address")
	ErrOAuthInvalidTicket   = errors.New("this link request expired; connect the account again")
	ErrOAuthLinkMismatch    = errors.New("this link request belongs to a different signed-in user")
	ErrOAuthAlreadyLinked   = errors.New("this sign-in is already linked to another account")
	ErrOAuthProviderInUse   = errors.New("a different account from this provider is already connected; disconnect it first")
)

const oauthStateTTL = 10 * time.Minute

// ProviderConfig exposes a configured provider so tests can point its
// endpoints at a local stub. It returns nil when the provider is not set up.
func (s *OAuthService) ProviderConfig(provider OAuthProvider) *OAuthConfig {
	return s.configs[provider]
}

func randomOAuthToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func oauthHash(v string) string {
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:])
}

// SaveState stores a one-use state for a login (userID 0) or link (userID of
// the signed-in user) flow and returns the raw state for the provider URL.
// Only its hash is stored.
func (s *OAuthService) SaveState(ctx context.Context, purpose string, provider OAuthProvider, userID int64) (string, error) {
	if purpose != "login" && purpose != "link" {
		return "", fmt.Errorf("invalid OAuth purpose")
	}
	state, err := randomOAuthToken()
	if err != nil {
		return "", err
	}
	var owner sql.NullInt64
	if purpose == "link" {
		owner = sql.NullInt64{Int64: userID, Valid: true}
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO oauth_states(state_hash,purpose,provider,user_id,expires_at) VALUES($1,$2,$3,$4,now()+make_interval(secs => $5))`,
		oauthHash(state), purpose, string(provider), owner, oauthStateTTL.Seconds())
	if err != nil {
		return "", err
	}
	return state, nil
}

// ConsumeState atomically deletes a login or link state for provider and
// returns its purpose and owner. Expired, replayed or foreign states fail.
func (s *OAuthService) ConsumeState(ctx context.Context, state string, provider OAuthProvider) (string, int64, error) {
	if len(state) != 64 {
		return "", 0, ErrOAuthInvalidState
	}
	var purpose string
	var owner sql.NullInt64
	err := s.db.QueryRowContext(ctx, `DELETE FROM oauth_states WHERE state_hash=$1 AND provider=$2 AND expires_at>now() AND purpose IN ('login','link') RETURNING purpose,user_id`,
		oauthHash(state), string(provider)).Scan(&purpose, &owner)
	if err == sql.ErrNoRows {
		return "", 0, ErrOAuthInvalidState
	}
	if err != nil {
		return "", 0, err
	}
	return purpose, owner.Int64, nil
}

// RunStateCleanup deletes expired states and tickets every 10 minutes.
func (s *OAuthService) RunStateCleanup(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.db.ExecContext(ctx, `DELETE FROM oauth_states WHERE expires_at<now()`); err != nil && ctx.Err() == nil {
				log.Printf("oauth state cleanup failed: %v", err)
			}
		}
	}
}

// ExchangeCode exchanges an authorization code for an access token. The token
// is used once to read the profile and is never stored.
func (s *OAuthService) ExchangeCode(ctx context.Context, provider OAuthProvider, code string) (string, error) {
	cfg, ok := s.configs[provider]
	if !ok {
		return "", ErrOAuthProvider
	}
	data := url.Values{
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"code":          {code},
		"redirect_uri":  {cfg.RedirectURL},
		"grant_type":    {"authorization_code"},
	}
	req, err := http.NewRequestWithContext(ctx, "POST", cfg.TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", ErrOAuthProvider
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	body, err := s.fetch(req)
	if err != nil {
		return "", err
	}
	var tokenResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil || tokenResp.AccessToken == "" {
		return "", ErrOAuthProvider
	}
	return tokenResp.AccessToken, nil
}

// fetch performs a provider request with a bounded body. Provider error text
// is logged server-side only.
func (s *OAuthService) fetch(req *http.Request) ([]byte, error) {
	resp, err := s.httpClient.Do(req)
	if err != nil {
		log.Printf("oauth provider request failed: %v", err)
		return nil, ErrOAuthProvider
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, ErrOAuthProvider
	}
	if resp.StatusCode != http.StatusOK {
		log.Printf("oauth provider %s returned HTTP %d", req.URL.Host, resp.StatusCode)
		return nil, ErrOAuthProvider
	}
	return body, nil
}

func (s *OAuthService) getJSON(ctx context.Context, endpoint, accessToken string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, ErrOAuthProvider
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	return s.fetch(req)
}

// GetUserInfo fetches the provider profile, including whether the provider
// vouches for the email address.
func (s *OAuthService) GetUserInfo(ctx context.Context, provider OAuthProvider, accessToken string) (*OAuthUserInfo, error) {
	cfg, ok := s.configs[provider]
	if !ok {
		return nil, ErrOAuthProvider
	}
	body, err := s.getJSON(ctx, cfg.UserInfoURL, accessToken)
	if err != nil {
		return nil, err
	}
	var info *OAuthUserInfo
	switch provider {
	case ProviderGoogle:
		info, err = parseGoogleUserInfo(body)
	case ProviderGitHub:
		var emails []byte
		if emails, err = s.getJSON(ctx, cfg.EmailsURL, accessToken); err == nil {
			info, err = parseGitHubUserInfo(body, emails)
		}
	case ProviderMicrosoft:
		info, err = parseMicrosoftUserInfo(body)
	default:
		return nil, ErrOAuthProvider
	}
	if err != nil || info == nil || info.ID == "" {
		return nil, ErrOAuthProvider
	}
	info.Email = strings.ToLower(strings.TrimSpace(info.Email))
	return info, nil
}

func parseGoogleUserInfo(body []byte) (*OAuthUserInfo, error) {
	var data struct {
		ID            string `json:"id"`
		Email         string `json:"email"`
		VerifiedEmail bool   `json:"verified_email"`
		Name          string `json:"name"`
		Picture       string `json:"picture"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	return &OAuthUserInfo{ID: data.ID, Email: data.Email, Name: data.Name, AvatarURL: data.Picture, EmailVerified: data.VerifiedEmail && data.Email != ""}, nil
}

// parseGitHubUserInfo uses only the primary verified address from
// /user/emails. The public profile email is user-editable and never trusted.
func parseGitHubUserInfo(body, emailsBody []byte) (*OAuthUserInfo, error) {
	var data struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := json.Unmarshal(emailsBody, &emails); err != nil {
		return nil, err
	}
	info := &OAuthUserInfo{Name: data.Name, AvatarURL: data.AvatarURL}
	if data.ID > 0 {
		info.ID = fmt.Sprintf("%d", data.ID)
	}
	if info.Name == "" {
		info.Name = data.Login
	}
	for _, e := range emails {
		if e.Primary && e.Verified && e.Email != "" {
			info.Email, info.EmailVerified = e.Email, true
			break
		}
	}
	return info, nil
}

// parseMicrosoftUserInfo never marks the address verified: Graph's mail and
// userPrincipalName are tenant-controlled, so Microsoft can sign in after
// linking but can never bootstrap an owner.
func parseMicrosoftUserInfo(body []byte) (*OAuthUserInfo, error) {
	var data struct {
		ID                string `json:"id"`
		DisplayName       string `json:"displayName"`
		Mail              string `json:"mail"`
		UserPrincipalName string `json:"userPrincipalName"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	email := data.Mail
	if email == "" {
		email = data.UserPrincipalName
	}
	return &OAuthUserInfo{ID: data.ID, Email: email, Name: data.DisplayName}, nil
}

// FindLoginUser resolves a provider identity to a user only through an
// existing connection. It never matches by email. On an empty instance a
// verified identity bootstraps the owner; otherwise the sign-in is not linked.
func (s *OAuthService) FindLoginUser(ctx context.Context, provider OAuthProvider, info *OAuthUserInfo) (int64, int64, bool, error) {
	var userID, orgID int64
	err := s.db.QueryRowContext(ctx, `
		SELECT oc.user_id, u.org_id
		FROM oauth_connections oc
		JOIN users u ON u.id = oc.user_id
		WHERE oc.provider = $1 AND oc.provider_user_id = $2
	`, string(provider), info.ID).Scan(&userID, &orgID)
	if err == nil {
		// A removed (disabled) member never signs in or relinks through a
		// leftover connection.
		var active bool
		if err = s.db.QueryRowContext(ctx, `SELECT status='active' FROM users WHERE id=$1`, userID).Scan(&active); err != nil {
			return 0, 0, false, err
		}
		if !active {
			return 0, 0, false, ErrOAuthNotLinked
		}
	}
	if err == nil {
		// Profile fields only; tokens are never kept.
		if _, err = s.db.ExecContext(ctx, `
			UPDATE oauth_connections
			SET email = $3, name = $4, avatar_url = $5, access_token = NULL, refresh_token = NULL, token_expiry = NULL, updated_at = NOW()
			WHERE provider = $1 AND provider_user_id = $2
		`, string(provider), info.ID, info.Email, info.Name, info.AvatarURL); err != nil {
			return 0, 0, false, err
		}
		return userID, orgID, false, nil
	}
	if err != sql.ErrNoRows {
		return 0, 0, false, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, false, err
	}
	defer tx.Rollback()
	// Same lock as password registration: only one first owner can be created.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(20261004,1)`); err != nil {
		return 0, 0, false, err
	}
	var existing int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&existing); err != nil {
		return 0, 0, false, err
	}
	if existing > 0 {
		return 0, 0, false, ErrOAuthNotLinked
	}
	if !info.EmailVerified || info.Email == "" {
		return 0, 0, false, ErrOAuthEmailUnverified
	}
	name := strings.TrimSpace(info.Name)
	if name == "" {
		name = strings.Split(info.Email, "@")[0]
	}
	err = tx.QueryRowContext(ctx, `
		INSERT INTO organizations (name, slug, monthly_email_limit, max_domains, max_identities, max_contacts, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
		RETURNING id
	`, name+"'s Workspace", generateSlug(name), s.cfg.DefaultMonthlyEmailLimit, s.cfg.DefaultMaxDomains, s.cfg.DefaultMaxIdentities, s.cfg.DefaultMaxContacts).Scan(&orgID)
	if err != nil {
		return 0, 0, false, err
	}
	// No password: the owner signs in with this provider until one is set.
	err = tx.QueryRowContext(ctx, `
		INSERT INTO users (org_id, email, password_hash, name, role, status, email_verified, email_verified_at, updated_at)
		VALUES ($1, $2, '', $3, 'owner', 'active', true, NOW(), NOW())
		RETURNING id
	`, orgID, info.Email, name).Scan(&userID)
	if err != nil {
		return 0, 0, false, err
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO oauth_connections (user_id, provider, provider_user_id, email, name, avatar_url, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
	`, userID, string(provider), info.ID, info.Email, info.Name, info.AvatarURL); err != nil {
		return 0, 0, false, err
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, false, err
	}
	return userID, orgID, true, nil
}

// CreateLinkTicket records a provider identity returned to a link flow. The
// signed-in user must confirm it (ConfirmLink) before a connection exists.
func (s *OAuthService) CreateLinkTicket(ctx context.Context, userID int64, provider OAuthProvider, info *OAuthUserInfo) (string, error) {
	ticket, err := randomOAuthToken()
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO oauth_states(state_hash,purpose,provider,user_id,provider_user_id,provider_email,provider_name,expires_at) VALUES($1,'link_confirm',$2,$3,$4,$5,$6,now()+make_interval(secs => $7))`,
		oauthHash(ticket), string(provider), userID, info.ID, info.Email, info.Name, oauthStateTTL.Seconds())
	if err != nil {
		return "", err
	}
	return ticket, nil
}

// ConfirmLink consumes a link ticket for the signed-in user and creates the
// connection without tokens. A ticket issued to another user is consumed and
// rejected, which closes link CSRF. Relinking the same identity is a no-op.
func (s *OAuthService) ConfirmLink(ctx context.Context, userID int64, ticket string) (string, error) {
	if len(ticket) != 64 {
		return "", ErrOAuthInvalidTicket
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var owner int64
	var provider, providerUserID string
	var email, name sql.NullString
	err = tx.QueryRowContext(ctx, `DELETE FROM oauth_states WHERE state_hash=$1 AND purpose='link_confirm' AND expires_at>now() RETURNING user_id,provider,provider_user_id,provider_email,provider_name`,
		oauthHash(ticket)).Scan(&owner, &provider, &providerUserID, &email, &name)
	if err == sql.ErrNoRows {
		return "", ErrOAuthInvalidTicket
	}
	if err != nil {
		return "", err
	}
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT status='active' FROM users WHERE id=$1`, userID).Scan(&active); err != nil && err != sql.ErrNoRows {
		return "", err
	}
	if owner != userID || !active {
		// Keep the ticket consumed; nothing is linked.
		if err = tx.Commit(); err != nil {
			return "", err
		}
		return "", ErrOAuthLinkMismatch
	}
	var linkedTo int64
	err = tx.QueryRowContext(ctx, `SELECT user_id FROM oauth_connections WHERE provider=$1 AND provider_user_id=$2`, provider, providerUserID).Scan(&linkedTo)
	switch {
	case err == nil && linkedTo == userID:
		return provider, tx.Commit()
	case err == nil:
		return "", ErrOAuthAlreadyLinked
	case err != sql.ErrNoRows:
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO oauth_connections (user_id, provider, provider_user_id, email, name, updated_at) VALUES ($1, $2, $3, $4, $5, NOW())`,
		userID, provider, providerUserID, email, name)
	if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23505" {
		if pqErr.Constraint == "oauth_connections_user_id_provider_key" {
			return "", ErrOAuthProviderInUse
		}
		return "", ErrOAuthAlreadyLinked
	}
	if err != nil {
		return "", err
	}
	return provider, tx.Commit()
}

// GetConnections returns all OAuth connections for a user
func (s *OAuthService) GetConnections(ctx context.Context, userID int64) ([]*OAuthConnection, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, provider, provider_user_id, COALESCE(email, ''), COALESCE(name, ''), COALESCE(avatar_url, ''), created_at, updated_at
		FROM oauth_connections
		WHERE user_id = $1
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list connections: %w", err)
	}
	defer rows.Close()

	var connections []*OAuthConnection
	for rows.Next() {
		var conn OAuthConnection
		if err := rows.Scan(&conn.ID, &conn.UserID, &conn.Provider, &conn.ProviderUserID,
			&conn.Email, &conn.Name, &conn.AvatarURL, &conn.CreatedAt, &conn.UpdatedAt); err != nil {
			continue
		}
		connections = append(connections, &conn)
	}

	return connections, nil
}

// DisconnectProvider removes an OAuth connection
func (s *OAuthService) DisconnectProvider(ctx context.Context, userID int64, provider OAuthProvider) error {
	// Check if user has a password set (to ensure they can still log in)
	var passwordHash string
	var connectionCount int

	err := s.db.QueryRowContext(ctx, `
		SELECT password_hash, (SELECT COUNT(*) FROM oauth_connections WHERE user_id = $1)
		FROM users WHERE id = $1
	`, userID).Scan(&passwordHash, &connectionCount)
	if err != nil {
		return fmt.Errorf("user not found")
	}

	// Prevent disconnection if this is the only auth method
	if passwordHash == "" && connectionCount <= 1 {
		return fmt.Errorf("cannot disconnect the only authentication method. Please set a password first.")
	}

	result, err := s.db.ExecContext(ctx, `
		DELETE FROM oauth_connections WHERE user_id = $1 AND provider = $2
	`, userID, string(provider))
	if err != nil {
		return fmt.Errorf("failed to disconnect provider: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("OAuth connection not found")
	}

	return nil
}

// GetSupportedProviders returns a list of configured OAuth providers
func (s *OAuthService) GetSupportedProviders() []string {
	providers := make([]string, 0, len(s.configs))
	for p := range s.configs {
		providers = append(providers, string(p))
	}
	return providers
}

// IsProviderConfigured checks if a provider is configured
func (s *OAuthService) IsProviderConfigured(provider OAuthProvider) bool {
	_, ok := s.configs[provider]
	return ok
}

// Note: generateSlug is defined in auth.go and shared across the service package
