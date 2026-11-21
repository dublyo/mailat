package controller

import (
	"errors"
	"log"
	"strconv"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

// LoginPinController serves the login PIN step and the signed-in user's PIN
// and remembered-device settings. Every user role has a PIN.
type LoginPinController struct {
	authService     *service.AuthService
	auditLogService *service.AuditLogService
	limiter         *service.RateLimiter
}

func NewLoginPinController(authService *service.AuthService, auditLogService *service.AuditLogService, limiter *service.RateLimiter) *LoginPinController {
	return &LoginPinController{authService: authService, auditLogService: auditLogService, limiter: limiter}
}

// PinStepRequest answers a PIN challenge from login, the TOTP step, OAuth,
// an invite or registration.
type PinStepRequest struct {
	ChallengeToken string `json:"challengeToken" v:"required"`
	Pin            string `json:"pin" v:"required"` // Exactly 4 digits.
	// Remember this browser for 30 days: the response then carries a
	// deviceToken to send with later logins of this account.
	TrustDevice bool `json:"trustDevice"`
}

// ChangePinRequest sets a new login PIN.
type ChangePinRequest struct {
	CurrentPassword string `json:"currentPassword" v:"required"`
	NewPin          string `json:"newPin" v:"required"` // 4 digits, not all the same and not a run like 1234 or 4321.
}

// writePinError answers a PIN step refusal with its status; 429 carries
// Retry-After, and a wrong PIN carries data.attemptsLeft.
func writePinError(r *ghttp.Request, err error) {
	var pinErr *service.PinError
	if errors.As(err, &pinErr) {
		if pinErr.RetryAfter > 0 {
			response.TooManyRequests(r, pinErr.RetryAfter, pinErr.Message)
			return
		}
		var data interface{}
		if pinErr.AttemptsLeft != nil {
			data = map[string]int{"attemptsLeft": *pinErr.AttemptsLeft}
		}
		response.WithStatus(r, pinErr.Status, pinErr.Status, pinErr.Message, data)
		return
	}
	log.Printf("login PIN request failed: %v", err)
	response.InternalError(r, "Unable to complete the request")
}

func (c *LoginPinController) deviceMeta(r *ghttp.Request) service.DeviceMeta {
	return service.DeviceMeta{UserAgent: r.UserAgent(), IP: middleware.ClientIP(r)}
}

// Verify completes the PIN step and signs in. The challenge lasts 5 minutes
// and allows 5 attempts. Wrong PINs per user are limited to 5 per 15 minutes
// and 20 per 24 hours (429 with Retry-After); a wrong PIN answers 401 with
// data.attemptsLeft. Also limited to 20 requests per client IP per 15
// minutes. trustDevice returns a 30-day deviceToken.
// POST /api/v1/auth/pin/verify
func (c *LoginPinController) Verify(r *ghttp.Request) {
	if rateLimited(r, c.limiter, service.RulePinIP, middleware.ClientIP(r)) {
		return
	}
	var req PinStepRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, "challengeToken and pin are required")
		return
	}
	result, err := c.authService.VerifyLoginPin(r.Context(), req.ChallengeToken, req.Pin, req.TrustDevice, c.deviceMeta(r))
	if err != nil {
		writePinError(r, err)
		return
	}
	response.Success(r, result)
}

// Setup chooses the first login PIN of an account that has none (after
// registration, an invite, an admin PIN reset or a server-side recovery) and
// signs in. The PIN is 4 digits, not all the same and not a run like 1234 or
// 4321 (400 otherwise). Limited to 20 requests per client IP per 15 minutes.
// POST /api/v1/auth/pin/setup
func (c *LoginPinController) Setup(r *ghttp.Request) {
	if rateLimited(r, c.limiter, service.RulePinIP, middleware.ClientIP(r)) {
		return
	}
	var req PinStepRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, "challengeToken and pin are required")
		return
	}
	result, err := c.authService.SetupLoginPin(r.Context(), req.ChallengeToken, req.Pin, req.TrustDevice, c.deviceMeta(r))
	if err != nil {
		writePinError(r, err)
		return
	}
	response.Success(r, result)
}

// Status reports whether your login PIN is set and when.
// GET /api/v1/security/pin
func (c *LoginPinController) Status(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}
	status, err := c.authService.LoginPinStatus(r.Context(), claims.UserID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}
	response.Success(r, status)
}

// Change sets a new login PIN after your current password (the current PIN
// is not needed). Every remembered device must enter the new PIN at its next
// sign-in; sessions stay. Limited to 10 per user per 15 minutes.
// PUT /api/v1/security/pin
func (c *LoginPinController) Change(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}
	if rateLimited(r, c.limiter, service.RulePinManageUser, strconv.FormatInt(claims.UserID, 10)) {
		return
	}
	var req ChangePinRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, "currentPassword and newPin are required")
		return
	}
	if err := c.authService.ChangeLoginPin(r.Context(), claims.UserID, req.CurrentPassword, req.NewPin); err != nil {
		c.audit(r, "login_pin_change", "Failed to change the login PIN: "+err.Error(), "failure")
		writePinError(r, err)
		return
	}
	c.audit(r, "login_pin_change", "Changed the login PIN; remembered devices were forgotten", "")
	status, err := c.authService.LoginPinStatus(r.Context(), claims.UserID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}
	response.SuccessWithMessage(r, "Login PIN changed", status)
}

// ListDevices lists your remembered browsers that skip the PIN step, newest
// first: created, last used, user agent and fixed 30-day expiry.
// GET /api/v1/security/trusted-devices
func (c *LoginPinController) ListDevices(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}
	devices, err := c.authService.ListTrustedDevices(r.Context(), claims.UserID)
	if err != nil {
		writePinError(r, err)
		return
	}
	response.Success(r, devices)
}

// ForgetDevice makes one remembered browser ask for the PIN again.
// DELETE /api/v1/security/trusted-devices/:uuid
func (c *LoginPinController) ForgetDevice(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}
	if err := c.authService.ForgetTrustedDevice(r.Context(), claims.UserID, r.Get("uuid").String()); err != nil {
		writePinError(r, err)
		return
	}
	c.audit(r, "trusted_device_forget", "Forgot a remembered device", "")
	response.SuccessWithMessage(r, "Device forgotten", nil)
}

// ForgetAllDevices makes every remembered browser ask for the PIN again.
// count is the number forgotten.
// POST /api/v1/security/trusted-devices/forget-all
func (c *LoginPinController) ForgetAllDevices(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}
	count, err := c.authService.ForgetAllTrustedDevices(r.Context(), claims.UserID)
	if err != nil {
		writePinError(r, err)
		return
	}
	c.audit(r, "trusted_device_forget_all", "Forgot all remembered devices", "")
	response.Success(r, map[string]interface{}{"count": count})
}

func (c *LoginPinController) audit(r *ghttp.Request, action, description, status string) {
	claims := middleware.GetClaims(r)
	if c.auditLogService == nil || claims == nil {
		return
	}
	c.auditLogService.LogAsync(&service.AuditLogInput{
		OrgID:       claims.OrgID,
		UserID:      &claims.UserID,
		Action:      action,
		Resource:    "user",
		ResourceID:  strconv.FormatInt(claims.UserID, 10),
		Description: description,
		IPAddress:   middleware.ClientIP(r),
		UserAgent:   r.UserAgent(),
		Status:      status,
	})
}
