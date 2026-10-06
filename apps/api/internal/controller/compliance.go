package controller

import (
	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

type ComplianceController struct {
	complianceService *service.ComplianceService
	limiter           *service.RateLimiter
}

func NewComplianceController(complianceService *service.ComplianceService, limiter *service.RateLimiter) *ComplianceController {
	return &ComplianceController{complianceService: complianceService, limiter: limiter}
}

// publicLimited applies the shared per-IP limit for public token endpoints
// (60 per 15 minutes).
func (c *ComplianceController) publicLimited(r *ghttp.Request) bool {
	return rateLimited(r, c.limiter, service.RulePublicComplianceIP, middleware.ClientIP(r))
}

// OneClickUnsubscribe handles RFC 8058 one-click unsubscribe (POST only).
// Mailbox providers send these POSTs from a few shared servers, so a validly
// signed token is never IP-limited; the signature proves authority and the
// operation is idempotent. Invalid tokens still count against the IP limit.
// POST /api/v1/unsubscribe/:token
func (c *ComplianceController) OneClickUnsubscribe(r *ghttp.Request) {
	token := r.Get("token").String()
	if !c.complianceService.ValidUnsubscribeToken(token) {
		if c.publicLimited(r) {
			return
		}
		response.BadRequest(r, "Invalid unsubscribe link")
		return
	}

	ipAddress := middleware.ClientIP(r)
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
	if c.publicLimited(r) {
		return
	}
	token := r.Get("token").String()
	if token == "" {
		response.BadRequest(r, "Invalid unsubscribe link")
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}
	r.Parse(&req)

	ipAddress := middleware.ClientIP(r)
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

// UpdatePreferences sets the contact's list memberships from the preference
// center. listIds must belong to the token's organization (400 unknown_list).
// Leaving lists is always allowed; joining is refused for unsubscribed or
// suppressed addresses (409 reactivation_blocked). An empty selection
// unsubscribes and suppresses the address.
// PUT /api/v1/preferences/:token
func (c *ComplianceController) UpdatePreferences(r *ghttp.Request) {
	if c.publicLimited(r) {
		return
	}
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

	ipAddress := middleware.ClientIP(r)
	userAgent := r.Header.Get("User-Agent")

	err := c.complianceService.UpdatePreferences(r.Context(), token, req.ListIDs, ipAddress, userAgent)
	if err != nil {
		contactError(r, err, response.BadRequest)
		return
	}

	response.SuccessWithMessage(r, "Preferences updated", nil)
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

// DeleteContactData erases a contact (GDPR right to erasure) in one
// transaction. Erased: every case variant of the address in the organization,
// list memberships, consent history, signup requests, automation enrollments
// and logs, campaign email recipients/subject/content and delivery event data,
// and webhook payloads naming the address or contact (pending deliveries are
// cancelled). A hash-only suppression (SHA-256 of the lowercased address) is
// kept so the address is never mailed again; this pseudonymous hash is
// retained under the legitimate interest of honoring the objection.
// Not erased: the transactional suppression_list (SES deliverability data),
// the organization users' own mailboxes and raw stored mail; a webhook
// delivery already in flight may still be sent once.
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

	actor := service.ContactActor{UserID: claims.UserID, IP: middleware.ClientIP(r), UA: r.UserAgent()}
	err := c.complianceService.DeleteContactData(r.Context(), claims.OrgID, actor, contactUUID)
	if err != nil {
		contactError(r, err, response.InternalError)
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
