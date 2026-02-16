package controller

import (
	"strconv"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

// Phase5Controller handles Phase 5 feature endpoints
type Phase5Controller struct {
	webauthnService     *service.WebAuthnService
	sharedMailboxService *service.SharedMailboxService
	sieveService        *service.SieveService
	webhookTriggerService *service.WebhookTriggerService
	pushService         *service.PushNotificationService
	brandingService     *service.BrandingService
	auditLogService     *service.AuditLogService
}

// NewPhase5Controller creates a new Phase 5 controller
func NewPhase5Controller(
	webauthnService *service.WebAuthnService,
	sharedMailboxService *service.SharedMailboxService,
	sieveService *service.SieveService,
	webhookTriggerService *service.WebhookTriggerService,
	pushService *service.PushNotificationService,
	brandingService *service.BrandingService,
	auditLogService *service.AuditLogService,
) *Phase5Controller {
	return &Phase5Controller{
		webauthnService:      webauthnService,
		sharedMailboxService: sharedMailboxService,
		sieveService:         sieveService,
		webhookTriggerService: webhookTriggerService,
		pushService:          pushService,
		brandingService:      brandingService,
		auditLogService:      auditLogService,
	}
}

// ====================
// WEBAUTHN ENDPOINTS
// ====================

// BeginWebAuthnRegistration starts WebAuthn credential registration
// POST /api/v1/security/webauthn/register/begin
func (c *Phase5Controller) BeginWebAuthnRegistration(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	options, err := c.webauthnService.BeginRegistration(r.Context(), claims.UserID, claims.Email, claims.Email)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.Success(r, options)
}

// FinishWebAuthnRegistration completes WebAuthn credential registration
// POST /api/v1/security/webauthn/register/finish
func (c *Phase5Controller) FinishWebAuthnRegistration(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req struct {
		Name     string                       `json:"name"`
		Response *service.RegistrationResponse `json:"response"`
	}
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	if req.Name == "" {
		req.Name = "Security Key"
	}

	credential, err := c.webauthnService.FinishRegistration(r.Context(), claims.UserID, req.Name, req.Response)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	c.auditLogService.LogAsync(&service.AuditLogInput{
		OrgID:       claims.OrgID,
		UserID:      &claims.UserID,
		Action:      "webauthn_registered",
		Resource:    "webauthn_credential",
		ResourceID:  credential.UUID,
		Description: "Security key registered: " + req.Name,
		IPAddress:   r.GetClientIp(),
		UserAgent:   r.UserAgent(),
	})

	response.SuccessWithMessage(r, "Security key registered successfully", credential)
}

// BeginWebAuthnAuthentication starts WebAuthn authentication
// POST /api/v1/security/webauthn/authenticate/begin
func (c *Phase5Controller) BeginWebAuthnAuthentication(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	options, err := c.webauthnService.BeginAuthentication(r.Context(), claims.UserID)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.Success(r, options)
}

// FinishWebAuthnAuthentication completes WebAuthn authentication
// POST /api/v1/security/webauthn/authenticate/finish
func (c *Phase5Controller) FinishWebAuthnAuthentication(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req service.AuthenticationResponse
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	err := c.webauthnService.FinishAuthentication(r.Context(), claims.UserID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Authentication successful", nil)
}

// ListWebAuthnCredentials lists all WebAuthn credentials
// GET /api/v1/security/webauthn/credentials
func (c *Phase5Controller) ListWebAuthnCredentials(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	credentials, err := c.webauthnService.ListCredentials(r.Context(), claims.UserID)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, credentials)
}

// DeleteWebAuthnCredential deletes a WebAuthn credential
// DELETE /api/v1/security/webauthn/credentials/:uuid
func (c *Phase5Controller) DeleteWebAuthnCredential(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	uuid := r.Get("uuid").String()
	if uuid == "" {
		response.BadRequest(r, "Credential UUID is required")
		return
	}

	err := c.webauthnService.DeleteCredential(r.Context(), claims.UserID, uuid)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Security key deleted successfully", nil)
}

// ====================
// SHARED MAILBOX ENDPOINTS
// ====================

// CreateSharedMailbox creates a new shared mailbox
// POST /api/v1/shared-mailboxes
func (c *Phase5Controller) CreateSharedMailbox(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	if claims.Role != "admin" && claims.Role != "owner" {
		response.Forbidden(r, "Only admins can create shared mailboxes")
		return
	}

	var input service.CreateSharedMailboxInput
	if err := r.Parse(&input); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	mailbox, err := c.sharedMailboxService.Create(r.Context(), claims.OrgID, &input)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.Created(r, mailbox)
}

// ListSharedMailboxes lists all shared mailboxes
// GET /api/v1/shared-mailboxes
func (c *Phase5Controller) ListSharedMailboxes(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	mailboxes, err := c.sharedMailboxService.List(r.Context(), claims.OrgID)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, mailboxes)
}

// GetSharedMailbox gets a shared mailbox
// GET /api/v1/shared-mailboxes/:id
func (c *Phase5Controller) GetSharedMailbox(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	id := r.Get("id").Int()
	mailbox, err := c.sharedMailboxService.Get(r.Context(), claims.OrgID, id)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, mailbox)
}

// DeleteSharedMailbox deletes a shared mailbox
// DELETE /api/v1/shared-mailboxes/:id
func (c *Phase5Controller) DeleteSharedMailbox(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	if claims.Role != "admin" && claims.Role != "owner" {
		response.Forbidden(r, "Only admins can delete shared mailboxes")
		return
	}

	id := r.Get("id").Int()
	err := c.sharedMailboxService.Delete(r.Context(), claims.OrgID, id)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Shared mailbox deleted", nil)
}

// AddSharedMailboxMember adds a member to a shared mailbox
// POST /api/v1/shared-mailboxes/:id/members
func (c *Phase5Controller) AddSharedMailboxMember(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	id := r.Get("id").Int()

	var input service.AddMemberInput
	if err := r.Parse(&input); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	member, err := c.sharedMailboxService.AddMember(r.Context(), claims.OrgID, id, &input)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.Created(r, member)
}

// ListSharedMailboxMembers lists members of a shared mailbox
// GET /api/v1/shared-mailboxes/:id/members
func (c *Phase5Controller) ListSharedMailboxMembers(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	id := r.Get("id").Int()

	members, err := c.sharedMailboxService.ListMembers(r.Context(), claims.OrgID, id)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, members)
}

// RemoveSharedMailboxMember removes a member from a shared mailbox
// DELETE /api/v1/shared-mailboxes/:id/members/:userId
func (c *Phase5Controller) RemoveSharedMailboxMember(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	id := r.Get("id").Int()
	userID := r.Get("userId").Int()

	err := c.sharedMailboxService.RemoveMember(r.Context(), claims.OrgID, id, userID)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Member removed", nil)
}

// ====================
// SIEVE SCRIPT ENDPOINTS
// ====================

// CreateSieveScript creates a new Sieve script
// POST /api/v1/sieve-scripts
func (c *Phase5Controller) CreateSieveScript(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var input service.CreateSieveScriptInput
	if err := r.Parse(&input); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	script, err := c.sieveService.Create(r.Context(), claims.UserID, claims.OrgID, &input)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.Created(r, script)
}

// ListSieveScripts lists all Sieve scripts
// GET /api/v1/sieve-scripts
func (c *Phase5Controller) ListSieveScripts(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	scripts, err := c.sieveService.List(r.Context(), claims.UserID)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, scripts)
}

// GetSieveScript gets a Sieve script
// GET /api/v1/sieve-scripts/:id
func (c *Phase5Controller) GetSieveScript(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	id := r.Get("id").Int()
	script, err := c.sieveService.Get(r.Context(), claims.UserID, id)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, script)
}

// UpdateSieveScript updates a Sieve script
// PUT /api/v1/sieve-scripts/:id
func (c *Phase5Controller) UpdateSieveScript(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	id := r.Get("id").Int()

	var req struct {
		Name   *string `json:"name,omitempty"`
		Script *string `json:"script,omitempty"`
		Active *bool   `json:"active,omitempty"`
	}
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	script, err := c.sieveService.Update(r.Context(), claims.UserID, id, req.Name, req.Script, req.Active)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.Success(r, script)
}

// DeleteSieveScript deletes a Sieve script
// DELETE /api/v1/sieve-scripts/:id
func (c *Phase5Controller) DeleteSieveScript(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	id := r.Get("id").Int()
	err := c.sieveService.Delete(r.Context(), claims.UserID, id)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Sieve script deleted", nil)
}

// ValidateSieveScript validates a Sieve script
// POST /api/v1/sieve-scripts/validate
func (c *Phase5Controller) ValidateSieveScript(r *ghttp.Request) {
	var req struct {
		Script string `json:"script"`
	}
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	isValid, errorMsg := c.sieveService.Validate(req.Script)
	response.Success(r, map[string]interface{}{
		"valid": isValid,
		"error": errorMsg,
	})
}

// ====================
// WEBHOOK TRIGGER ENDPOINTS
// ====================

// CreateWebhookTrigger creates a new webhook trigger
// POST /api/v1/webhook-triggers
func (c *Phase5Controller) CreateWebhookTrigger(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var input service.CreateWebhookTriggerInput
	if err := r.Parse(&input); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	trigger, err := c.webhookTriggerService.Create(r.Context(), claims.UserID, claims.OrgID, &input)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.Created(r, trigger)
}

// ListWebhookTriggers lists all webhook triggers
// GET /api/v1/webhook-triggers
func (c *Phase5Controller) ListWebhookTriggers(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	triggers, err := c.webhookTriggerService.List(r.Context(), claims.OrgID)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, triggers)
}

// GetWebhookTrigger gets a webhook trigger
// GET /api/v1/webhook-triggers/:id
func (c *Phase5Controller) GetWebhookTrigger(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	id := r.Get("id").Int()
	trigger, err := c.webhookTriggerService.Get(r.Context(), claims.OrgID, id)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, trigger)
}

// DeleteWebhookTrigger deletes a webhook trigger
// DELETE /api/v1/webhook-triggers/:id
func (c *Phase5Controller) DeleteWebhookTrigger(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	id := r.Get("id").Int()
	err := c.webhookTriggerService.Delete(r.Context(), claims.OrgID, id)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Webhook trigger deleted", nil)
}

// TestWebhookTrigger tests a webhook trigger
// POST /api/v1/webhook-triggers/:id/test
func (c *Phase5Controller) TestWebhookTrigger(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	id := r.Get("id").Int()
	err := c.webhookTriggerService.Test(r.Context(), claims.OrgID, id)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Test webhook sent", nil)
}

// GetWebhookTriggerTypes returns available trigger types
// GET /api/v1/webhook-triggers/types
func (c *Phase5Controller) GetWebhookTriggerTypes(r *ghttp.Request) {
	types := c.webhookTriggerService.GetAvailableTriggerTypes()
	response.Success(r, types)
}

// ====================
// PUSH NOTIFICATION ENDPOINTS
// ====================

// GetVAPIDKey returns the VAPID public key
// GET /api/v1/push/vapid-key
func (c *Phase5Controller) GetVAPIDKey(r *ghttp.Request) {
	key := c.pushService.GetVAPIDPublicKey()
	response.Success(r, map[string]string{"publicKey": key})
}

// SubscribePush subscribes to push notifications
// POST /api/v1/push/subscribe
func (c *Phase5Controller) SubscribePush(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var input service.CreatePushSubscriptionInput
	if err := r.Parse(&input); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	sub, err := c.pushService.Subscribe(r.Context(), claims.UserID, &input)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.Created(r, sub)
}

// UnsubscribePush unsubscribes from push notifications
// POST /api/v1/push/unsubscribe
func (c *Phase5Controller) UnsubscribePush(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req struct {
		Endpoint string `json:"endpoint"`
	}
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	err := c.pushService.Unsubscribe(r.Context(), claims.UserID, req.Endpoint)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Unsubscribed successfully", nil)
}

// ListPushSubscriptions lists push subscriptions
// GET /api/v1/push/subscriptions
func (c *Phase5Controller) ListPushSubscriptions(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	subs, err := c.pushService.ListSubscriptions(r.Context(), claims.UserID)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, subs)
}

// UpdatePushPreferences updates push notification preferences
// PUT /api/v1/push/subscriptions/:uuid/preferences
func (c *Phase5Controller) UpdatePushPreferences(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	uuid := r.Get("uuid").String()

	var req struct {
		NotifyNewEmail  *bool `json:"notifyNewEmail,omitempty"`
		NotifyCampaign  *bool `json:"notifyCampaign,omitempty"`
		NotifyMentions  *bool `json:"notifyMentions,omitempty"`
	}
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	err := c.pushService.UpdatePreferences(r.Context(), claims.UserID, uuid, req.NotifyNewEmail, req.NotifyCampaign, req.NotifyMentions)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Preferences updated", nil)
}

// ====================
// BRANDING ENDPOINTS
// ====================

// GetBranding gets organization branding
// GET /api/v1/branding
func (c *Phase5Controller) GetBranding(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	branding, err := c.brandingService.Get(r.Context(), claims.OrgID)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, branding)
}

// UpdateBranding updates organization branding
// PUT /api/v1/branding
func (c *Phase5Controller) UpdateBranding(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	if claims.Role != "admin" && claims.Role != "owner" {
		response.Forbidden(r, "Only admins can update branding")
		return
	}

	var input service.UpdateBrandingInput
	if err := r.Parse(&input); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	branding, err := c.brandingService.Update(r.Context(), claims.OrgID, &input)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	c.auditLogService.LogAsync(&service.AuditLogInput{
		OrgID:       claims.OrgID,
		UserID:      &claims.UserID,
		Action:      "branding_updated",
		Resource:    "branding",
		ResourceID:  strconv.FormatInt(claims.OrgID, 10),
		Description: "Organization branding updated",
		IPAddress:   r.GetClientIp(),
		UserAgent:   r.UserAgent(),
	})

	response.Success(r, branding)
}

// VerifyCustomDomain initiates custom domain verification
// POST /api/v1/branding/verify-domain
func (c *Phase5Controller) VerifyCustomDomain(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	if claims.Role != "admin" && claims.Role != "owner" {
		response.Forbidden(r, "Only admins can verify domains")
		return
	}

	verified, token, err := c.brandingService.VerifyCustomDomain(r.Context(), claims.OrgID)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.Success(r, map[string]interface{}{
		"verified":          verified,
		"verificationToken": token,
		"instructions":      "Add a TXT record with this token to verify your domain",
	})
}

// GetBrandingCSS returns CSS variables for branding
// GET /api/v1/branding/css
func (c *Phase5Controller) GetBrandingCSS(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	css, err := c.brandingService.GetBrandingCSS(r.Context(), claims.OrgID)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	r.Response.Header().Set("Content-Type", "text/css")
	r.Response.Write(css)
}
