package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

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
	Scopes       []string
	RedirectURL  string
}

// OAuthUserInfo holds user information from an OAuth provider
type OAuthUserInfo struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

// OAuthConnection represents a stored OAuth connection
type OAuthConnection struct {
	ID             int        `json:"id"`
	UserID         int        `json:"userId"`
	Provider       string     `json:"provider"`
	ProviderUserID string     `json:"providerUserId"`
	Email          string     `json:"email,omitempty"`
	Name           string     `json:"name,omitempty"`
	AvatarURL      string     `json:"avatarUrl,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

// OAuthService handles OAuth2 authentication
type OAuthService struct {
	db       *sql.DB
	cfg      *config.Config
	configs  map[OAuthProvider]*OAuthConfig
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

	// Add access_type for Google (to get refresh token)
	if provider == ProviderGoogle {
		params.Set("access_type", "offline")
		params.Set("prompt", "consent")
	}

	return cfg.AuthURL + "?" + params.Encode(), nil
}

// ExchangeCode exchanges an authorization code for tokens
func (s *OAuthService) ExchangeCode(ctx context.Context, provider OAuthProvider, code string) (string, string, time.Time, error) {
	cfg, ok := s.configs[provider]
	if !ok {
		return "", "", time.Time{}, fmt.Errorf("unsupported OAuth provider: %s", provider)
	}

	// Build request
	data := url.Values{
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"code":          {code},
		"redirect_uri":  {cfg.RedirectURL},
		"grant_type":    {"authorization_code"},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", cfg.TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to exchange code: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", "", time.Time{}, fmt.Errorf("token exchange failed: %s", string(body))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}

	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to parse token response: %w", err)
	}

	expiry := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

	return tokenResp.AccessToken, tokenResp.RefreshToken, expiry, nil
}

// GetUserInfo fetches user information from the OAuth provider
func (s *OAuthService) GetUserInfo(ctx context.Context, provider OAuthProvider, accessToken string) (*OAuthUserInfo, error) {
	cfg, ok := s.configs[provider]
	if !ok {
		return nil, fmt.Errorf("unsupported OAuth provider: %s", provider)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", cfg.UserInfoURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch user info: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get user info: %s", string(body))
	}

	// Parse based on provider
	switch provider {
	case ProviderGoogle:
		return s.parseGoogleUserInfo(body)
	case ProviderGitHub:
		return s.parseGitHubUserInfo(body, accessToken)
	case ProviderMicrosoft:
		return s.parseMicrosoftUserInfo(body)
	default:
		return nil, fmt.Errorf("unsupported provider: %s", provider)
	}
}

func (s *OAuthService) parseGoogleUserInfo(body []byte) (*OAuthUserInfo, error) {
	var data struct {
		ID      string `json:"id"`
		Email   string `json:"email"`
		Name    string `json:"name"`
		Picture string `json:"picture"`
	}

	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}

	return &OAuthUserInfo{
		ID:        data.ID,
		Email:     data.Email,
		Name:      data.Name,
		AvatarURL: data.Picture,
	}, nil
}

func (s *OAuthService) parseGitHubUserInfo(body []byte, accessToken string) (*OAuthUserInfo, error) {
	var data struct {
		ID        int    `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}

	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}

	// GitHub email might be private, fetch from emails endpoint
	email := data.Email
	if email == "" {
		email = s.fetchGitHubEmail(accessToken)
	}

	name := data.Name
	if name == "" {
		name = data.Login
	}

	return &OAuthUserInfo{
		ID:        fmt.Sprintf("%d", data.ID),
		Email:     email,
		Name:      name,
		AvatarURL: data.AvatarURL,
	}, nil
}

func (s *OAuthService) fetchGitHubEmail(accessToken string) string {
	req, err := http.NewRequest("GET", "https://api.github.com/user/emails", nil)
	if err != nil {
		return ""
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&emails); err != nil {
		return ""
	}

	// Return primary verified email
	for _, e := range emails {
		if e.Primary && e.Verified {
			return e.Email
		}
	}

	// Return any verified email
	for _, e := range emails {
		if e.Verified {
			return e.Email
		}
	}

	return ""
}

func (s *OAuthService) parseMicrosoftUserInfo(body []byte) (*OAuthUserInfo, error) {
	var data struct {
		ID          string `json:"id"`
		DisplayName string `json:"displayName"`
		Mail        string `json:"mail"`
		UserPrincipalName string `json:"userPrincipalName"`
	}

	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}

	email := data.Mail
	if email == "" {
		email = data.UserPrincipalName
	}

	return &OAuthUserInfo{
		ID:    data.ID,
		Email: email,
		Name:  data.DisplayName,
	}, nil
}

// FindOrCreateUser finds an existing user by OAuth connection or creates a new one
func (s *OAuthService) FindOrCreateUser(ctx context.Context, provider OAuthProvider, userInfo *OAuthUserInfo, accessToken, refreshToken string, tokenExpiry time.Time) (int64, int64, bool, error) {
	// First, check if there's an existing OAuth connection
	var userID int64
	var orgID int64
	err := s.db.QueryRowContext(ctx, `
		SELECT oc.user_id, u.org_id
		FROM oauth_connections oc
		JOIN users u ON u.id = oc.user_id
		WHERE oc.provider = $1 AND oc.provider_user_id = $2
	`, string(provider), userInfo.ID).Scan(&userID, &orgID)

	if err == nil {
		// Found existing connection, update tokens
		s.db.ExecContext(ctx, `
			UPDATE oauth_connections
			SET access_token = $3, refresh_token = $4, token_expiry = $5, email = $6, name = $7, avatar_url = $8, updated_at = NOW()
			WHERE user_id = $1 AND provider = $2
		`, userID, string(provider), accessToken, refreshToken, tokenExpiry, userInfo.Email, userInfo.Name, userInfo.AvatarURL)

		return userID, orgID, false, nil
	}

	if err != sql.ErrNoRows {
		return 0, 0, false, fmt.Errorf("failed to check existing connection: %w", err)
	}

	// No existing connection. Check if user with this email exists
	err = s.db.QueryRowContext(ctx, `
		SELECT id, org_id FROM users WHERE email = $1
	`, userInfo.Email).Scan(&userID, &orgID)

	if err == nil {
		// User exists, create OAuth connection
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO oauth_connections (user_id, provider, provider_user_id, access_token, refresh_token, token_expiry, email, name, avatar_url)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		`, userID, string(provider), userInfo.ID, accessToken, refreshToken, tokenExpiry, userInfo.Email, userInfo.Name, userInfo.AvatarURL)
		if err != nil {
			return 0, 0, false, fmt.Errorf("failed to create OAuth connection: %w", err)
		}

		return userID, orgID, false, nil
	}

	if err != sql.ErrNoRows {
		return 0, 0, false, fmt.Errorf("failed to check existing user: %w", err)
	}

	// Create new user and organization
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, false, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Create organization
	orgSlug := generateSlug(userInfo.Name)
	err = tx.QueryRowContext(ctx, `
		INSERT INTO organizations (name, slug)
		VALUES ($1, $2)
		RETURNING id
	`, userInfo.Name+"'s Organization", orgSlug).Scan(&orgID)
	if err != nil {
		return 0, 0, false, fmt.Errorf("failed to create organization: %w", err)
	}

	// Create user (with empty password since they use OAuth)
	err = tx.QueryRowContext(ctx, `
		INSERT INTO users (org_id, email, password_hash, name, role, status, email_verified, email_verified_at, updated_at)
		VALUES ($1, $2, '', $3, 'owner', 'active', true, NOW(), NOW())
		RETURNING id
	`, orgID, userInfo.Email, userInfo.Name).Scan(&userID)
	if err != nil {
		return 0, 0, false, fmt.Errorf("failed to create user: %w", err)
	}

	// Create OAuth connection
	_, err = tx.ExecContext(ctx, `
		INSERT INTO oauth_connections (user_id, provider, provider_user_id, access_token, refresh_token, token_expiry, email, name, avatar_url)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, userID, string(provider), userInfo.ID, accessToken, refreshToken, tokenExpiry, userInfo.Email, userInfo.Name, userInfo.AvatarURL)
	if err != nil {
		return 0, 0, false, fmt.Errorf("failed to create OAuth connection: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return 0, 0, false, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return userID, orgID, true, nil
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
