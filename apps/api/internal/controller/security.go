package controller

import (
	"strconv"
	"time"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

type SecurityController struct {
	twoFactorService *service.TwoFactorService
	auditLogService  *service.AuditLogService
	sessionService   *service.SessionService
}

func NewSecurityController(twoFactorService *service.TwoFactorService, auditLogService *service.AuditLogService, sessionService *service.SessionService) *SecurityController {
	return &SecurityController{
		twoFactorService: twoFactorService,
		auditLogService:  auditLogService,
		sessionService:   sessionService,
	}
}

// ====================
// TWO-FACTOR AUTHENTICATION
// ====================

// Setup2FA starts the 2FA setup process
// POST /api/v1/security/2fa/setup
func (c *SecurityController) Setup2FA(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	setup, err := c.twoFactorService.GenerateSetup(r.Context(), claims.UserID, claims.Email)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	// Don't log the secret in audit log for security
	c.auditLogService.LogAsync(&service.AuditLogInput{
		OrgID:       claims.OrgID,
		UserID:      &claims.UserID,
		Action:      "2fa_setup_started",
		Resource:    "user",
		ResourceID:  strconv.FormatInt(claims.UserID, 10),
		Description: "Started 2FA setup",
		IPAddress:   r.GetClientIp(),
		UserAgent:   r.UserAgent(),
	})

	response.Success(r, setup)
}

// Verify2FA verifies a TOTP code and enables 2FA
// POST /api/v1/security/2fa/verify
func (c *SecurityController) Verify2FA(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req struct {
		Code string `json:"code"`
	}
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	if req.Code == "" {
		response.BadRequest(r, "Verification code is required")
		return
	}

	backupCodes, err := c.twoFactorService.VerifyAndEnable(r.Context(), claims.UserID, req.Code)
	if err != nil {
		c.auditLogService.LogAsync(&service.AuditLogInput{
			OrgID:       claims.OrgID,
			UserID:      &claims.UserID,
			Action:      service.AuditAction2FAEnable,
			Resource:    "user",
			ResourceID:  strconv.FormatInt(claims.UserID, 10),
			Description: "Failed to enable 2FA: " + err.Error(),
			IPAddress:   r.GetClientIp(),
			UserAgent:   r.UserAgent(),
			Status:      "failure",
		})
		response.BadRequest(r, err.Error())
		return
	}

	c.auditLogService.LogAsync(&service.AuditLogInput{
		OrgID:       claims.OrgID,
		UserID:      &claims.UserID,
		Action:      service.AuditAction2FAEnable,
		Resource:    "user",
		ResourceID:  strconv.FormatInt(claims.UserID, 10),
		Description: "2FA enabled successfully",
		IPAddress:   r.GetClientIp(),
		UserAgent:   r.UserAgent(),
	})

	response.SuccessWithMessage(r, "2FA enabled successfully. Please save your backup codes.", map[string]interface{}{
		"backupCodes": backupCodes,
	})
}

// Disable2FA disables 2FA for the user
// POST /api/v1/security/2fa/disable
func (c *SecurityController) Disable2FA(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	if req.Password == "" {
		response.BadRequest(r, "Password is required")
		return
	}

	if req.Code == "" {
		response.BadRequest(r, "Verification code is required")
		return
	}

	err := c.twoFactorService.Disable(r.Context(), claims.UserID, req.Password, req.Code)
	if err != nil {
		c.auditLogService.LogAsync(&service.AuditLogInput{
			OrgID:       claims.OrgID,
			UserID:      &claims.UserID,
			Action:      service.AuditAction2FADisable,
			Resource:    "user",
			ResourceID:  strconv.FormatInt(claims.UserID, 10),
			Description: "Failed to disable 2FA: " + err.Error(),
			IPAddress:   r.GetClientIp(),
			UserAgent:   r.UserAgent(),
			Status:      "failure",
		})
		response.BadRequest(r, err.Error())
		return
	}

	c.auditLogService.LogAsync(&service.AuditLogInput{
		OrgID:       claims.OrgID,
		UserID:      &claims.UserID,
		Action:      service.AuditAction2FADisable,
		Resource:    "user",
		ResourceID:  strconv.FormatInt(claims.UserID, 10),
		Description: "2FA disabled successfully",
		IPAddress:   r.GetClientIp(),
		UserAgent:   r.UserAgent(),
	})

	response.SuccessWithMessage(r, "2FA disabled successfully", nil)
}

// RegenerateBackupCodes generates new backup codes
// POST /api/v1/security/2fa/backup-codes
func (c *SecurityController) RegenerateBackupCodes(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	backupCodes, err := c.twoFactorService.RegenerateBackupCodes(r.Context(), claims.UserID, req.Password, req.Code)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	c.auditLogService.LogAsync(&service.AuditLogInput{
		OrgID:       claims.OrgID,
		UserID:      &claims.UserID,
		Action:      "2fa_backup_codes_regenerated",
		Resource:    "user",
		ResourceID:  strconv.FormatInt(claims.UserID, 10),
		Description: "Backup codes regenerated",
		IPAddress:   r.GetClientIp(),
		UserAgent:   r.UserAgent(),
	})

	response.SuccessWithMessage(r, "New backup codes generated. Please save them.", map[string]interface{}{
		"backupCodes": backupCodes,
	})
}

// Get2FAStatus returns the 2FA status for the user
// GET /api/v1/security/2fa/status
func (c *SecurityController) Get2FAStatus(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	enabled, backupCodesCount, err := c.twoFactorService.GetStatus(r.Context(), claims.UserID)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, map[string]interface{}{
		"enabled":          enabled,
		"backupCodesCount": backupCodesCount,
	})
}

// ====================
// AUDIT LOGS
// ====================

// ListAuditLogs returns audit logs for the organization
// GET /api/v1/security/audit-logs
func (c *SecurityController) ListAuditLogs(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	// Only admins and owners can view audit logs
	if claims.Role != "admin" && claims.Role != "owner" {
		response.Forbidden(r, "Only admins can view audit logs")
		return
	}

	filter := &service.AuditLogFilter{
		Limit:  r.Get("limit").Int(),
		Offset: r.Get("offset").Int(),
	}

	if userID := r.Get("userId").Int64(); userID > 0 {
		filter.UserID = &userID
	}

	filter.Action = r.Get("action").String()
	filter.Resource = r.Get("resource").String()
	filter.ResourceID = r.Get("resourceId").String()
	filter.Status = r.Get("status").String()

	if startDate := r.Get("startDate").String(); startDate != "" {
		if t, err := time.Parse(time.RFC3339, startDate); err == nil {
			filter.StartDate = &t
		}
	}

	if endDate := r.Get("endDate").String(); endDate != "" {
		if t, err := time.Parse(time.RFC3339, endDate); err == nil {
			filter.EndDate = &t
		}
	}

	logs, total, err := c.auditLogService.List(r.Context(), claims.OrgID, filter)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, map[string]interface{}{
		"logs":  logs,
		"total": total,
	})
}

// GetSecurityEvents returns security-related events
// GET /api/v1/security/events
func (c *SecurityController) GetSecurityEvents(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	// Only admins and owners can view security events
	if claims.Role != "admin" && claims.Role != "owner" {
		response.Forbidden(r, "Only admins can view security events")
		return
	}

	limit := r.Get("limit").Int()
	if limit <= 0 {
		limit = 100
	}

	logs, err := c.auditLogService.GetSecurityEvents(r.Context(), claims.OrgID, limit)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, logs)
}

// GetMyActivity returns the current user's activity
// GET /api/v1/security/my-activity
func (c *SecurityController) GetMyActivity(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	limit := r.Get("limit").Int()
	if limit <= 0 {
		limit = 50
	}

	logs, err := c.auditLogService.GetUserActivity(r.Context(), claims.OrgID, claims.UserID, limit)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, logs)
}

// ====================
// PASSWORD MANAGEMENT
// ====================

// ChangePassword changes the user's password
// POST /api/v1/auth/change-password
func (c *SecurityController) ChangePassword(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req struct {
		CurrentPassword string `json:"currentPassword" v:"required"`
		NewPassword     string `json:"newPassword" v:"required|min-length:8"`
	}
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	err := c.sessionService.ChangePassword(r.Context(), claims.UserID, req.CurrentPassword, req.NewPassword)
	if err != nil {
		c.auditLogService.LogAsync(&service.AuditLogInput{
			OrgID:       claims.OrgID,
			UserID:      &claims.UserID,
			Action:      "password_change",
			Resource:    "user",
			ResourceID:  strconv.FormatInt(claims.UserID, 10),
			Description: "Failed to change password: " + err.Error(),
			IPAddress:   r.GetClientIp(),
			UserAgent:   r.UserAgent(),
			Status:      "failure",
		})
		response.BadRequest(r, err.Error())
		return
	}

	c.auditLogService.LogAsync(&service.AuditLogInput{
		OrgID:       claims.OrgID,
		UserID:      &claims.UserID,
		Action:      "password_change",
		Resource:    "user",
		ResourceID:  strconv.FormatInt(claims.UserID, 10),
		Description: "Password changed successfully",
		IPAddress:   r.GetClientIp(),
		UserAgent:   r.UserAgent(),
	})

	response.SuccessWithMessage(r, "Password changed successfully", nil)
}

// ====================
// SESSION MANAGEMENT
// ====================

// ListSessions returns all active sessions for the current user
// GET /api/v1/security/sessions
func (c *SecurityController) ListSessions(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	// Get current token from Authorization header
	token := middleware.ExtractToken(r)

	sessions, err := c.sessionService.ListUserSessions(r.Context(), claims.UserID, token)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, map[string]interface{}{
		"sessions": sessions,
	})
}

// RevokeSession revokes a specific session
// DELETE /api/v1/security/sessions/:uuid
func (c *SecurityController) RevokeSession(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	sessionUUID := r.Get("uuid").String()
	if sessionUUID == "" {
		response.BadRequest(r, "Session UUID is required")
		return
	}

	err := c.sessionService.RevokeSession(r.Context(), claims.UserID, sessionUUID)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	c.auditLogService.LogAsync(&service.AuditLogInput{
		OrgID:       claims.OrgID,
		UserID:      &claims.UserID,
		Action:      "session_revoked",
		Resource:    "session",
		ResourceID:  sessionUUID,
		Description: "Session revoked",
		IPAddress:   r.GetClientIp(),
		UserAgent:   r.UserAgent(),
	})

	response.SuccessWithMessage(r, "Session revoked successfully", nil)
}

// RevokeAllSessions revokes all sessions except the current one
// POST /api/v1/security/sessions/revoke-all
func (c *SecurityController) RevokeAllSessions(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	// Get current token to exclude from revocation
	currentToken := middleware.ExtractToken(r)

	count, err := c.sessionService.RevokeAllSessions(r.Context(), claims.UserID, currentToken)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	c.auditLogService.LogAsync(&service.AuditLogInput{
		OrgID:       claims.OrgID,
		UserID:      &claims.UserID,
		Action:      "all_sessions_revoked",
		Resource:    "session",
		Description: "All other sessions revoked",
		IPAddress:   r.GetClientIp(),
		UserAgent:   r.UserAgent(),
	})

	response.SuccessWithMessage(r, "All other sessions revoked", map[string]interface{}{
		"revokedCount": count,
	})
}
