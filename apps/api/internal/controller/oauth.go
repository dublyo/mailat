package controller

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

type OAuthController struct {
	oauthService    *service.OAuthService
	auditLogService *service.AuditLogService
	cfg             *config.Config
	limiter         *service.RateLimiter
}

func NewOAuthController(oauthService *service.OAuthService, auditLogService *service.AuditLogService, cfg *config.Config, limiter *service.RateLimiter) *OAuthController {
	return &OAuthController{
		oauthService:    oauthService,
		auditLogService: auditLogService,
		cfg:             cfg,
		limiter:         limiter,
	}
}

const oauthStateCookie = "oauth_state"

// oauthNonce is the SPA's per-tab random value. It is echoed back in the login
// fragment so the SPA only adopts a session from a sign-in it started itself.
var oauthNonce = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// GetProviders returns the list of configured OAuth providers
// GET /api/v1/oauth/providers
func (c *OAuthController) GetProviders(r *ghttp.Request) {
	providers := c.oauthService.GetSupportedProviders()
	response.Success(r, map[string]interface{}{
		"providers": providers,
	})
}

// oauthLimited applies the per-IP OAuth limit. Browser navigations get a
// redirect to the login page instead of a JSON 429.
func (c *OAuthController) oauthLimited(r *ghttp.Request, redirect bool) bool {
	if !redirect {
		return rateLimited(r, c.limiter, service.RuleOAuthIP, middleware.ClientIP(r))
	}
	if c.limiter == nil {
		return false
	}
	allowed, _, err := c.limiter.Allow(r.Context(), service.RuleOAuthIP, middleware.ClientIP(r))
	if err != nil || !allowed {
		c.redirectWithError(r, "rate_limited")
		return true
	}
	return false
}

// secureCookies reports whether the browser reaches Mailat over HTTPS.
func (c *OAuthController) secureCookies() bool {
	return strings.HasPrefix(c.cfg.APIUrl, "https://") || strings.HasPrefix(c.cfg.WebUrl, "https://")
}

func (c *OAuthController) setStateCookie(r *ghttp.Request, value string, maxAge int) {
	cookie := &http.Cookie{Name: oauthStateCookie, Value: value, Path: "/api/v1/oauth", MaxAge: maxAge, HttpOnly: true, Secure: c.secureCookies(), SameSite: http.SameSiteLaxMode}
	r.Response.Header().Add("Set-Cookie", cookie.String())
}

// InitiateOAuth starts a sign-in with a provider. A one-use state is stored
// (hashed) for 10 minutes and bound to this browser with an HttpOnly cookie.
// The optional nonce (16-128 URL-safe characters) rides in that cookie and is
// returned as &nonce= in the login fragment. Failures redirect to
// /login?oauthError=<code>.
// GET /api/v1/oauth/:provider
func (c *OAuthController) InitiateOAuth(r *ghttp.Request) {
	if c.oauthLimited(r, true) {
		return
	}
	nonce := r.Get("nonce").String()
	if nonce != "" && !oauthNonce.MatchString(nonce) {
		c.redirectWithError(r, "invalid_state")
		return
	}
	provider := service.OAuthProvider(r.Get("provider").String())
	if !c.oauthService.IsProviderConfigured(provider) {
		c.redirectWithError(r, "provider_error")
		return
	}
	state, err := c.oauthService.SaveState(r.Context(), "login", provider, 0)
	if err != nil {
		c.redirectWithError(r, "provider_error")
		return
	}
	authURL, err := c.oauthService.GetAuthURL(provider, state)
	if err != nil {
		c.redirectWithError(r, "provider_error")
		return
	}
	cookie := state
	if nonce != "" {
		cookie += "." + nonce
	}
	c.setStateCookie(r, cookie, 600)
	r.Response.RedirectTo(authURL)
}

// HandleCallback completes a provider round trip. Sign-in only succeeds for an
// identity already linked to a user, or for a verified identity on an empty
// instance (first owner). It never links by email. A link flow redirects to
// Settings with a ticket that the signed-in user must confirm. Errors redirect
// to /login?oauthError= with one of invalid_state, provider_error, not_linked,
// email_unverified, registration_closed or rate_limited.
// GET /api/v1/oauth/:provider/callback
func (c *OAuthController) HandleCallback(r *ghttp.Request) {
	if c.oauthLimited(r, true) {
		return
	}
	provider := service.OAuthProvider(r.Get("provider").String())
	state := r.Get("state").String()
	if !c.oauthService.IsProviderConfigured(provider) || state == "" {
		c.redirectWithError(r, "invalid_state")
		return
	}
	purpose, ownerID, err := c.oauthService.ConsumeState(r.Context(), state, provider)
	if err != nil {
		c.redirectWithError(r, "invalid_state")
		return
	}
	var nonce string
	if purpose == "login" {
		cookie, cookieNonce, _ := strings.Cut(r.Cookie.Get(oauthStateCookie).String(), ".")
		nonce = cookieNonce
		c.setStateCookie(r, "", -1)
		if cookie == "" || subtle.ConstantTimeCompare([]byte(cookie), []byte(state)) != 1 {
			c.redirectWithError(r, "invalid_state")
			return
		}
	}
	code := r.Get("code").String()
	if r.Get("error").String() != "" || code == "" {
		c.redirectWithError(r, "provider_error")
		return
	}
	accessToken, err := c.oauthService.ExchangeCode(r.Context(), provider, code)
	if err != nil {
		c.redirectWithError(r, "provider_error")
		return
	}
	userInfo, err := c.oauthService.GetUserInfo(r.Context(), provider, accessToken)
	if err != nil {
		c.redirectWithError(r, "provider_error")
		return
	}

	if purpose == "link" {
		ticket, err := c.oauthService.CreateLinkTicket(r.Context(), ownerID, provider, userInfo)
		if err != nil {
			c.redirectWithError(r, "provider_error")
			return
		}
		r.Response.RedirectTo(strings.TrimRight(c.cfg.WebUrl, "/") + "/settings?tab=security&oauthLink=" + url.QueryEscape(ticket))
		return
	}

	userID, orgID, isNewUser, err := c.oauthService.FindLoginUser(r.Context(), provider, userInfo)
	switch {
	case errors.Is(err, service.ErrOAuthNotLinked):
		c.redirectWithError(r, "not_linked")
		return
	case errors.Is(err, service.ErrOAuthEmailUnverified):
		c.redirectWithError(r, "email_unverified")
		return
	case err != nil:
		c.redirectWithError(r, "provider_error")
		return
	}

	action := service.AuditActionLogin
	description := fmt.Sprintf("Logged in via %s OAuth", provider)
	if isNewUser {
		action = "oauth_register"
		description = fmt.Sprintf("Registered via %s OAuth", provider)
	}
	c.auditLogService.LogAsync(&service.AuditLogInput{
		OrgID:       orgID,
		UserID:      &userID,
		Action:      action,
		Resource:    "user",
		ResourceID:  fmt.Sprintf("%d", userID),
		Description: description,
		IPAddress:   middleware.ClientIP(r),
		UserAgent:   r.UserAgent(),
	})

	result, err := c.oauthService.AuthenticateLogin(r.Context(), userID)
	if err != nil {
		c.redirectWithError(r, "not_linked")
		return
	}
	// Fragments never reach the server access log or Referer header.
	fragment := "session=" + result.Token
	if result.RequiresTwoFactor {
		fragment = "challenge=" + result.ChallengeToken
	}
	if nonce != "" {
		fragment += "&nonce=" + nonce
	}
	r.Response.RedirectTo(strings.TrimRight(c.cfg.WebUrl, "/") + "/login#" + fragment)
}

// ConnectProvider starts linking a provider to the signed-in user. Navigate
// the browser to the returned authUrl; the provider returns to Settings with
// a ticket that must be confirmed with POST /oauth/link/confirm.
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
	state, err := c.oauthService.SaveState(r.Context(), "link", provider, claims.UserID)
	if err != nil {
		response.InternalError(r, "Failed to start the connection")
		return
	}
	authURL, err := c.oauthService.GetAuthURL(provider, state)
	if err != nil {
		response.InternalError(r, "Failed to start the connection")
		return
	}
	response.Success(r, map[string]interface{}{
		"authUrl": authURL,
	})
}

// ConfirmLink completes a provider link for the signed-in user with the
// one-use ticket from the Settings redirect. 403 link_mismatch when the ticket
// was issued to another user, 409 already_linked when the identity belongs to
// another account (or a different identity of this provider is connected),
// 410 invalid_ticket when it expired or was used. Human sessions only.
// POST /api/v1/oauth/link/confirm
func (c *OAuthController) ConfirmLink(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}
	if middleware.SessionHash(r.Context()) == "" {
		response.Forbidden(r, "A user session is required")
		return
	}
	if c.oauthLimited(r, false) {
		return
	}
	var req struct {
		Ticket string `json:"ticket"`
	}
	if err := r.Parse(&req); err != nil || req.Ticket == "" {
		response.BadRequest(r, "ticket is required")
		return
	}
	provider, err := c.oauthService.ConfirmLink(r.Context(), claims.UserID, req.Ticket)
	switch {
	case errors.Is(err, service.ErrOAuthInvalidTicket):
		response.WithStatus(r, 410, 410, err.Error(), map[string]string{"error": "invalid_ticket"})
		return
	case errors.Is(err, service.ErrOAuthLinkMismatch):
		response.WithStatus(r, 403, 403, err.Error(), map[string]string{"error": "link_mismatch"})
		return
	case errors.Is(err, service.ErrOAuthAlreadyLinked), errors.Is(err, service.ErrOAuthProviderInUse):
		response.WithStatus(r, 409, 409, err.Error(), map[string]string{"error": "already_linked"})
		return
	case err != nil:
		response.InternalError(r, "Unable to link the account")
		return
	}
	c.auditLogService.LogAsync(&service.AuditLogInput{
		OrgID:       claims.OrgID,
		UserID:      &claims.UserID,
		Action:      "oauth_connect",
		Resource:    "oauth_connection",
		ResourceID:  provider,
		Description: fmt.Sprintf("Connected %s sign-in", provider),
		IPAddress:   middleware.ClientIP(r),
		UserAgent:   r.UserAgent(),
	})
	response.Success(r, map[string]string{"provider": provider})
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
		IPAddress:   middleware.ClientIP(r),
		UserAgent:   r.UserAgent(),
	})

	response.SuccessWithMessage(r, fmt.Sprintf("%s account disconnected", providerStr), nil)
}

// redirectWithError sends the browser to the SPA login page with a fixed
// error code; provider or internal text is never forwarded.
func (c *OAuthController) redirectWithError(r *ghttp.Request, code string) {
	r.Response.RedirectTo(strings.TrimRight(c.cfg.WebUrl, "/") + "/login?oauthError=" + url.QueryEscape(code))
}
