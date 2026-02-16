package controller

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/golang-jwt/jwt/v5"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

type OAuthController struct {
	oauthService    *service.OAuthService
	auditLogService *service.AuditLogService
	cfg             *config.Config
}

func NewOAuthController(oauthService *service.OAuthService, auditLogService *service.AuditLogService, cfg *config.Config) *OAuthController {
	return &OAuthController{
		oauthService:    oauthService,
		auditLogService: auditLogService,
		cfg:             cfg,
	}
}

// GetProviders returns the list of configured OAuth providers
// GET /api/v1/oauth/providers
func (c *OAuthController) GetProviders(r *ghttp.Request) {
	providers := c.oauthService.GetSupportedProviders()
	response.Success(r, map[string]interface{}{
		"providers": providers,
	})
}

// InitiateOAuth starts the OAuth flow for a provider
// GET /api/v1/oauth/:provider
func (c *OAuthController) InitiateOAuth(r *ghttp.Request) {
	providerStr := r.Get("provider").String()
	provider := service.OAuthProvider(providerStr)

	if !c.oauthService.IsProviderConfigured(provider) {
		response.BadRequest(r, fmt.Sprintf("OAuth provider '%s' is not configured", providerStr))
		return
	}

	// Generate state token for CSRF protection
	state, err := generateState()
	if err != nil {
		response.InternalError(r, "Failed to generate state token")
		return
	}

	// Store state in a short-lived cookie
	r.Cookie.SetCookie("oauth_state", state, "", "/", 300) // 5 minutes

	authURL, err := c.oauthService.GetAuthURL(provider, state)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	// Redirect to OAuth provider
	r.Response.RedirectTo(authURL)
}

// HandleCallback handles the OAuth callback from a provider
// GET /api/v1/oauth/:provider/callback
func (c *OAuthController) HandleCallback(r *ghttp.Request) {
	providerStr := r.Get("provider").String()
	provider := service.OAuthProvider(providerStr)

	// Get authorization code and state from query params
	code := r.Get("code").String()
	state := r.Get("state").String()
	errorParam := r.Get("error").String()

	// Check for OAuth errors
	if errorParam != "" {
		errorDesc := r.Get("error_description").String()
		c.redirectWithError(r, fmt.Sprintf("OAuth error: %s - %s", errorParam, errorDesc))
		return
	}

	if code == "" {
		c.redirectWithError(r, "Authorization code not provided")
		return
	}

	// Verify state token
	storedState := r.Cookie.Get("oauth_state").String()
	if state == "" || state != storedState {
		c.redirectWithError(r, "Invalid state token. Please try again.")
		return
	}

	// Clear the state cookie
	r.Cookie.Remove("oauth_state")

	// Exchange code for tokens
	accessToken, refreshToken, tokenExpiry, err := c.oauthService.ExchangeCode(r.Context(), provider, code)
	if err != nil {
		c.redirectWithError(r, "Failed to exchange authorization code: "+err.Error())
		return
	}

	// Get user info from provider
	userInfo, err := c.oauthService.GetUserInfo(r.Context(), provider, accessToken)
	if err != nil {
		c.redirectWithError(r, "Failed to get user information: "+err.Error())
		return
	}

	if userInfo.Email == "" {
		c.redirectWithError(r, "Could not retrieve email from OAuth provider. Please ensure your email is public or try a different provider.")
		return
	}

	// Find or create user
	userID, orgID, isNewUser, err := c.oauthService.FindOrCreateUser(r.Context(), provider, userInfo, accessToken, refreshToken, tokenExpiry)
	if err != nil {
		c.redirectWithError(r, "Failed to create or find user: "+err.Error())
		return
	}

	// Log the login/registration
	action := service.AuditActionLogin
	description := fmt.Sprintf("Logged in via %s OAuth", providerStr)
	if isNewUser {
		action = "oauth_register"
		description = fmt.Sprintf("Registered via %s OAuth", providerStr)
	}

	c.auditLogService.LogAsync(&service.AuditLogInput{
		OrgID:       orgID,
		UserID:      &userID,
		Action:      action,
		Resource:    "user",
		ResourceID:  fmt.Sprintf("%d", userID),
		Description: description,
		IPAddress:   r.GetClientIp(),
		UserAgent:   r.UserAgent(),
	})

	// Generate JWT token
	token, err := c.generateJWT(userID, orgID, userInfo.Email, "member") // Role will be fetched from DB in real scenario
	if err != nil {
		c.redirectWithError(r, "Failed to generate authentication token")
		return
	}

	// Redirect to frontend with token
	redirectURL := fmt.Sprintf("%s/auth/callback?token=%s&new_user=%t", c.cfg.WebUrl, token, isNewUser)
	r.Response.RedirectTo(redirectURL)
}

// ConnectProvider connects an OAuth provider to an existing user account
// POST /api/v1/oauth/:provider/connect
func (c *OAuthController) ConnectProvider(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	providerStr := r.Get("provider").String()
	provider := service.OAuthProvider(providerStr)

	if !c.oauthService.IsProviderConfigured(provider) {
		response.BadRequest(r, fmt.Sprintf("OAuth provider '%s' is not configured", providerStr))
		return
	}

	// Generate state token
	state, err := generateState()
	if err != nil {
		response.InternalError(r, "Failed to generate state token")
		return
	}

	// Include user ID in state (for linking after callback)
	state = fmt.Sprintf("%s:%d", state, claims.UserID)

	// Store state in cookie
	r.Cookie.SetCookie("oauth_connect_state", state, "", "/", 300)

	authURL, err := c.oauthService.GetAuthURL(provider, state)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, map[string]interface{}{
		"authUrl": authURL,
	})
}

// GetConnections returns all OAuth connections for the current user
// GET /api/v1/oauth/connections
func (c *OAuthController) GetConnections(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	connections, err := c.oauthService.GetConnections(r.Context(), claims.UserID)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, connections)
}

// DisconnectProvider removes an OAuth connection
// DELETE /api/v1/oauth/:provider
func (c *OAuthController) DisconnectProvider(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	providerStr := r.Get("provider").String()
	provider := service.OAuthProvider(providerStr)

	err := c.oauthService.DisconnectProvider(r.Context(), claims.UserID, provider)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	c.auditLogService.LogAsync(&service.AuditLogInput{
		OrgID:       claims.OrgID,
		UserID:      &claims.UserID,
		Action:      "oauth_disconnect",
		Resource:    "oauth_connection",
		ResourceID:  providerStr,
		Description: fmt.Sprintf("Disconnected %s OAuth provider", providerStr),
		IPAddress:   r.GetClientIp(),
		UserAgent:   r.UserAgent(),
	})

	response.SuccessWithMessage(r, fmt.Sprintf("%s account disconnected", providerStr), nil)
}

// redirectWithError redirects to the frontend with an error message
func (c *OAuthController) redirectWithError(r *ghttp.Request, errorMsg string) {
	redirectURL := fmt.Sprintf("%s/auth/error?error=%s", c.cfg.WebUrl, errorMsg)
	r.Response.RedirectTo(redirectURL)
}

// generateState generates a random state token for CSRF protection
func generateState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// generateJWT generates a JWT token for a user
func (c *OAuthController) generateJWT(userID, orgID int64, email, role string) (string, error) {
	claims := jwt.MapClaims{
		"user_id": userID,
		"org_id":  orgID,
		"email":   email,
		"role":    role,
		"exp":     time.Now().Add(7 * 24 * time.Hour).Unix(),
		"iat":     time.Now().Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(c.cfg.JWTSecret))
}
