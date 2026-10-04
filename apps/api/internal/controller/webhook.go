package controller

import (
	"strconv"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/eventoutbox"
	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

type WebhookController struct {
	webhookService *service.WebhookService
}

func NewWebhookController(webhookService *service.WebhookService) *WebhookController {
	return &WebhookController{webhookService: webhookService}
}

// CreateWebhook creates a new webhook endpoint
// POST /api/v1/webhooks
func (c *WebhookController) CreateWebhook(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.CreateWebhookRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, "Invalid webhook request; use a public HTTPS URL and supported email events")
		return
	}

	if len(req.Events) == 0 {
		response.BadRequest(r, "Choose at least one email event")
		return
	}
	for _, kind := range req.Events {
		if !eventoutbox.KnownType(kind) {
			response.BadRequest(r, "Unsupported event type")
			return
		}
	}

	webhook, err := c.webhookService.ForUser(claims.UserID).CreateWebhook(r.Context(), claims.OrgID, &req)
	if err != nil {
		response.BadRequest(r, "Invalid webhook request; use a public HTTPS URL and supported email events")
		return
	}

	response.SuccessWithMessage(r, "Webhook created", webhook)
}

// GetWebhook retrieves a webhook by UUID
// GET /api/v1/webhooks/:uuid
func (c *WebhookController) GetWebhook(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	webhookUUID := r.Get("uuid").String()
	if webhookUUID == "" {
		response.BadRequest(r, "Webhook UUID required")
		return
	}

	webhook, err := c.webhookService.ForUser(claims.UserID).GetWebhook(r.Context(), claims.OrgID, webhookUUID)
	if err != nil {
		response.NotFound(r, "Webhook not found")
		return
	}

	response.Success(r, webhook)
}

// ListWebhooks returns all webhooks for the organization
// GET /api/v1/webhooks
func (c *WebhookController) ListWebhooks(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	webhooks, err := c.webhookService.ForUser(claims.UserID).ListWebhooks(r.Context(), claims.OrgID)
	if err != nil {
		response.InternalError(r, "Could not load webhook information")
		return
	}

	response.Success(r, webhooks)
}

// UpdateWebhook updates a webhook
// PUT /api/v1/webhooks/:uuid
func (c *WebhookController) UpdateWebhook(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	webhookUUID := r.Get("uuid").String()
	if webhookUUID == "" {
		response.BadRequest(r, "Webhook UUID required")
		return
	}

	var req model.UpdateWebhookRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, "Invalid webhook request; use a public HTTPS URL and supported email events")
		return
	}

	webhook, err := c.webhookService.ForUser(claims.UserID).UpdateWebhook(r.Context(), claims.OrgID, webhookUUID, &req)
	if err != nil {
		response.BadRequest(r, "Invalid webhook request; use a public HTTPS URL and supported email events")
		return
	}

	response.SuccessWithMessage(r, "Webhook updated", webhook)
}

// DeleteWebhook deletes a webhook
// DELETE /api/v1/webhooks/:uuid
func (c *WebhookController) DeleteWebhook(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	webhookUUID := r.Get("uuid").String()
	if webhookUUID == "" {
		response.BadRequest(r, "Webhook UUID required")
		return
	}

	err := c.webhookService.ForUser(claims.UserID).DeleteWebhook(r.Context(), claims.OrgID, webhookUUID)
	if err != nil {
		response.NotFound(r, "Webhook not found")
		return
	}

	response.SuccessWithMessage(r, "Webhook deleted", nil)
}

// RotateSecret generates a new secret for a webhook
// POST /api/v1/webhooks/:uuid/rotate-secret
func (c *WebhookController) RotateSecret(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	webhookUUID := r.Get("uuid").String()
	if webhookUUID == "" {
		response.BadRequest(r, "Webhook UUID required")
		return
	}

	newSecret, err := c.webhookService.ForUser(claims.UserID).RotateSecret(r.Context(), claims.OrgID, webhookUUID)
	if err != nil {
		response.BadRequest(r, "Invalid webhook request; use a public HTTPS URL and supported email events")
		return
	}

	response.SuccessWithMessage(r, "Secret rotated", model.RotateSecretResponse{Secret: newSecret})
}

// GetWebhookCalls returns recent webhook delivery attempts
// GET /api/v1/webhooks/:uuid/calls
func (c *WebhookController) GetWebhookCalls(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	webhookUUID := r.Get("uuid").String()
	if webhookUUID == "" {
		response.BadRequest(r, "Webhook UUID required")
		return
	}

	limit := 50
	if limitStr := r.Get("limit").String(); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	calls, err := c.webhookService.ForUser(claims.UserID).GetWebhookCalls(r.Context(), claims.OrgID, webhookUUID, limit)
	if err != nil {
		response.InternalError(r, "Could not load webhook information")
		return
	}

	response.Success(r, calls)
}

// TestWebhook sends a test event to a webhook
// POST /api/v1/webhooks/:uuid/test
func (c *WebhookController) TestWebhook(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	webhookUUID := r.Get("uuid").String()
	if webhookUUID == "" {
		response.BadRequest(r, "Webhook UUID required")
		return
	}

	result, err := c.webhookService.ForUser(claims.UserID).TestDelivery(r.Context(), claims.OrgID, webhookUUID)
	if err != nil {
		response.BadRequest(r, "Could not test webhook: check the endpoint and its status")
		return
	}
	response.Success(r, result)
}
