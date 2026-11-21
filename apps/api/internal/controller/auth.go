package controller

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

type AuthController struct {
	authService *service.AuthService
	limiter     *service.RateLimiter
}

func NewAuthController(authService *service.AuthService, limiter *service.RateLimiter) *AuthController {
	return &AuthController{authService: authService, limiter: limiter}
}

// RegisterStatus checks if registration is open
// GET /api/v1/auth/register-status
func (c *AuthController) RegisterStatus(r *ghttp.Request) {
	open, err := c.authService.IsRegistrationOpen(r.Context())
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}
	response.Success(r, map[string]bool{"open": open})
}

// Register creates the first owner and organization. The email must be a
// valid address and the password 8–72 bytes. No session is issued yet: the
// result has requiresPinSetup and a challengeToken for POST /auth/pin/setup.
// Limited to 5 attempts per client IP per hour (429 with Retry-After).
// POST /api/v1/auth/register
func (c *AuthController) Register(r *ghttp.Request) {
	if rateLimited(r, c.limiter, service.RuleRegisterIP, middleware.ClientIP(r)) {
		return
	}
	var req model.RegisterRequest
	bodyBytes := r.GetBody()
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		response.BadRequest(r, "Invalid JSON: "+err.Error())
		return
	}

	// Manual validation
	if req.Email == "" {
		response.BadRequest(r, "email is required")
		return
	}
	if req.Password == "" {
		response.BadRequest(r, "password is required")
		return
	}
	if req.Name == "" {
		response.BadRequest(r, "name is required")
		return
	}

	result, err := c.authService.Register(r.Context(), &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Registration successful", result)
}

// Login checks the email and password. The result is a session token, or
// the next step with a challengeToken: requiresTwoFactor (POST
// /auth/2fa/challenge), then requiresPin (POST /auth/pin/verify) or
// requiresPinSetup (POST /auth/pin/setup). A deviceToken from an earlier PIN
// step of this account (30 days) skips the PIN step. Limits: 20 attempts per
// client IP per 15 minutes; failed attempts only (a correct password never
// counts) 10 per account and client IP and 100 per account per 15 minutes.
// IPv6 clients are counted per /64. When the per-account limit is reached, a
// request with a valid deviceToken of that account is still checked, so
// wrong guesses from elsewhere cannot lock out a remembered browser. Over a
// limit the API returns 429 with Retry-After.
// POST /api/v1/auth/login
func (c *AuthController) Login(r *ghttp.Request) {
	ip := middleware.ClientIP(r)
	if rateLimited(r, c.limiter, service.RuleLoginIP, ip) {
		return
	}
	var req model.LoginRequest
	bodyBytes := r.GetBody()
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		response.BadRequest(r, "Invalid JSON: "+err.Error())
		return
	}

	// Manual validation
	if req.Email == "" {
		response.BadRequest(r, "email is required")
		return
	}
	if req.Password == "" {
		response.BadRequest(r, "password is required")
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	pair := email + "|" + service.RateLimitIPKey(ip)
	refund, ok := c.reserveLoginFailure(r, email, pair, req.DeviceToken)
	if !ok {
		return
	}

	result, err := c.authService.Login(r.Context(), &req)
	if !errors.Is(err, service.ErrInvalidCredentials) {
		// The password was right: the reserved failures are given back.
		for _, rf := range refund {
			refundHit(r, c.limiter, rf.rule, rf.subject)
		}
	}
	if err != nil {
		response.Unauthorized(r, err.Error())
		return
	}

	response.Success(r, result)
}

type reservedHit struct {
	rule    service.RateRule
	subject string
}

// reserveLoginFailure spends one failure of each login failure budget before
// bcrypt, atomically, so concurrent wrong guesses cannot exceed them and a
// locked address+IP costs no hashing work. It returns the hits to refund when
// the password turns out right, or ok=false after writing the response.
// When only the per-account budget is spent, a deviceToken of a live
// remembered device of that account still gets its password checked (a hash
// lookup, before bcrypt; the token is unguessable).
func (c *AuthController) reserveLoginFailure(r *ghttp.Request, email, pair, deviceToken string) ([]reservedHit, bool) {
	allowed, retryAfter, err := reserveHit(r, c.limiter, service.RuleLoginFailure, pair)
	if err != nil {
		response.InternalError(r, "Unable to process the request right now")
		return nil, false
	}
	if !allowed {
		response.TooManyRequests(r, retryAfter, tooManyAttemptsMessage(retryAfter))
		return nil, false
	}
	refund := []reservedHit{{service.RuleLoginFailure, pair}}
	allowed, retryAfter, err = reserveHit(r, c.limiter, service.RuleLoginAccountFailure, email)
	if err == nil && allowed {
		return append(refund, reservedHit{service.RuleLoginAccountFailure, email}), true
	}
	if err == nil && c.authService.HasTrustedDevice(r.Context(), email, deviceToken) {
		return refund, true
	}
	refundHit(r, c.limiter, service.RuleLoginFailure, pair)
	if err != nil {
		response.InternalError(r, "Unable to process the request right now")
	} else {
		response.TooManyRequests(r, retryAfter, tooManyAttemptsMessage(retryAfter))
	}
	return nil, false
}

// Me returns the current user's profile
// GET /api/v1/auth/me
func (c *AuthController) Me(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	user, err := c.authService.GetUserByID(r.Context(), claims.UserID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, user)
}

// CreateAPIKey generates a new API key
// POST /api/v1/api-keys
func (c *AuthController) CreateAPIKey(r *ghttp.Request) {
	if !requireHumanAdmin(r) {
		return
	}
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.CreateApiKeyRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	result, err := c.authService.CreateAPIKey(r.Context(), claims.OrgID, claims.UserID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "API key created. Save the key now - it won't be shown again.", result)
}

// ListAPIKeys returns all API keys for the organization
// GET /api/v1/api-keys
func (c *AuthController) ListAPIKeys(r *ghttp.Request) {
	if !requireHumanAdmin(r) {
		return
	}
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	keys, err := c.authService.ListAPIKeys(r.Context(), claims.OrgID)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, keys)
}

// DeleteAPIKey revokes an API key
// DELETE /api/v1/api-keys/:uuid
func (c *AuthController) DeleteAPIKey(r *ghttp.Request) {
	if !requireHumanAdmin(r) {
		return
	}
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	keyUUID := r.Get("uuid").String()
	if keyUUID == "" {
		response.BadRequest(r, "API key UUID required")
		return
	}

	err := c.authService.DeleteAPIKey(r.Context(), claims.OrgID, keyUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "API key revoked", nil)
}

func requireHumanAdmin(r *ghttp.Request) bool {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Authentication required")
		return false
	}
	if claims.Role != "owner" && claims.Role != "admin" {
		response.Forbidden(r, "A workspace administrator session is required")
		return false
	}
	return true
}

// CompleteChallenge accepts a one-use password/OAuth TOTP challenge, never an
// API key. A correct code continues to the PIN step (requiresPin or
// requiresPinSetup with a new challengeToken) unless the login carried a
// trusted deviceToken. Limited to 20 attempts per client IP per 15 minutes,
// in addition to five attempts per challenge.
func (c *AuthController) CompleteChallenge(r *ghttp.Request) {
	if rateLimited(r, c.limiter, service.Rule2FAChallengeIP, middleware.ClientIP(r)) {
		return
	}
	var req struct {
		ChallengeToken string `json:"challengeToken" v:"required"`
		Code           string `json:"code" v:"required"`
	}
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, "Challenge token and verification code are required")
		return
	}
	result, err := c.authService.CompleteChallenge(r.Context(), req.ChallengeToken, req.Code)
	if err != nil {
		response.Unauthorized(r, "Invalid or expired verification code. Sign in again if needed.")
		return
	}
	response.Success(r, result)
}
func (c *AuthController) Logout(r *ghttp.Request) {
	if err := c.authService.RevokeToken(r.Context(), middleware.ExtractToken(r)); err != nil {
		response.InternalError(r, "Unable to end session")
		return
	}
	response.SuccessWithMessage(r, "Signed out", nil)
}
func (c *AuthController) StreamToken(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil || claims.Role == "api" {
		response.Forbidden(r, "A user session is required")
		return
	}
	token, expires, err := c.authService.StreamToken(r.Context(), middleware.ExtractToken(r), claims)
	if err != nil {
		response.Unauthorized(r, "Active user session required")
		return
	}
	response.Success(r, map[string]interface{}{"token": token, "expiresAt": expires})
}
