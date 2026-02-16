package controller

import (
	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

type ComplianceController struct {
	complianceService *service.ComplianceService
}

func NewComplianceController(complianceService *service.ComplianceService) *ComplianceController {
	return &ComplianceController{complianceService: complianceService}
}

// OneClickUnsubscribe handles RFC 8058 one-click unsubscribe (POST only)
// POST /api/v1/unsubscribe/:token
func (c *ComplianceController) OneClickUnsubscribe(r *ghttp.Request) {
	token := r.Get("token").String()
	if token == "" {
		response.BadRequest(r, "Invalid unsubscribe link")
		return
	}

	ipAddress := r.GetClientIp()
	userAgent := r.Header.Get("User-Agent")

	err := c.complianceService.ProcessOneClickUnsubscribe(r.Context(), token, ipAddress, userAgent)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Successfully unsubscribed", nil)
}

// GetUnsubscribePage returns data for the unsubscribe landing page
// GET /api/v1/unsubscribe/:token
func (c *ComplianceController) GetUnsubscribePage(r *ghttp.Request) {
	token := r.Get("token").String()
	if token == "" {
		response.BadRequest(r, "Invalid unsubscribe link")
		return
	}

	data, err := c.complianceService.GetUnsubscribePage(r.Context(), token)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.Success(r, data)
}

// ConfirmUnsubscribe handles confirmed unsubscribe from landing page
// DELETE /api/v1/unsubscribe/:token
func (c *ComplianceController) ConfirmUnsubscribe(r *ghttp.Request) {
	token := r.Get("token").String()
	if token == "" {
		response.BadRequest(r, "Invalid unsubscribe link")
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}
	r.Parse(&req)

	ipAddress := r.GetClientIp()
	userAgent := r.Header.Get("User-Agent")

	err := c.complianceService.ConfirmUnsubscribe(r.Context(), token, req.Reason, ipAddress, userAgent)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Successfully unsubscribed", nil)
}

// GetPreferences returns preference center data
// GET /api/v1/preferences/:token
func (c *ComplianceController) GetPreferences(r *ghttp.Request) {
	token := r.Get("token").String()
	if token == "" {
		response.BadRequest(r, "Invalid preferences link")
		return
	}

	data, err := c.complianceService.GetPreferenceCenter(r.Context(), token)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.Success(r, data)
}

// UpdatePreferences updates subscriber preferences
// PUT /api/v1/preferences/:token
func (c *ComplianceController) UpdatePreferences(r *ghttp.Request) {
	token := r.Get("token").String()
	if token == "" {
		response.BadRequest(r, "Invalid preferences link")
		return
	}

	var req struct {
		ListIDs []int `json:"listIds"`
	}
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	ipAddress := r.GetClientIp()
	userAgent := r.Header.Get("User-Agent")

	err := c.complianceService.UpdatePreferences(r.Context(), token, req.ListIDs, ipAddress, userAgent)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Preferences updated", nil)
}

// ConfirmDoubleOptIn handles double opt-in confirmation
// GET /api/v1/confirm/:token
func (c *ComplianceController) ConfirmDoubleOptIn(r *ghttp.Request) {
	token := r.Get("token").String()
	if token == "" {
		response.BadRequest(r, "Invalid confirmation link")
		return
	}

	ipAddress := r.GetClientIp()
	userAgent := r.Header.Get("User-Agent")

	err := c.complianceService.ConfirmDoubleOptIn(r.Context(), token, ipAddress, userAgent)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Subscription confirmed", nil)
}

// ExportContactData exports all data for a contact (GDPR)
// GET /api/v1/contacts/:uuid/export
func (c *ComplianceController) ExportContactData(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	contactUUID := r.Get("uuid").String()
	if contactUUID == "" {
		response.BadRequest(r, "Contact UUID required")
		return
	}

	data, err := c.complianceService.ExportContactData(r.Context(), claims.OrgID, contactUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, data)
}

// DeleteContactData deletes all data for a contact (GDPR)
// DELETE /api/v1/contacts/:uuid/gdpr
func (c *ComplianceController) DeleteContactData(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	contactUUID := r.Get("uuid").String()
	if contactUUID == "" {
		response.BadRequest(r, "Contact UUID required")
		return
	}

	err := c.complianceService.DeleteContactData(r.Context(), claims.OrgID, contactUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Contact data deleted", nil)
}

// GetConsentAuditTrail retrieves consent audit trail for a contact
// GET /api/v1/contacts/:uuid/consent-audit
func (c *ComplianceController) GetConsentAuditTrail(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	contactUUID := r.Get("uuid").String()
	if contactUUID == "" {
		response.BadRequest(r, "Contact UUID required")
		return
	}

	records, err := c.complianceService.GetConsentAuditTrail(r.Context(), claims.OrgID, contactUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, records)
}
