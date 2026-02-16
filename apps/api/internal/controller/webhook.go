package controller

import (
	"strconv"

	"github.com/gogf/gf/v2/net/ghttp"

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
		response.BadRequest(r, err.Error())
		return
	}

	// Validate events
	validEvents := map[string]bool{
		"email.sent":       true,
		"email.delivered":  true,
		"email.bounced":    true,
		"email.complained": true,
		"email.opened":     true,
		"email.clicked":    true,
		"email.failed":     true,
	}

	for _, event := range req.Events {
		if !validEvents[event] {
			response.BadRequest(r, "Invalid event type: "+event)
			return
		}
	}

	webhook, err := c.webhookService.CreateWebhook(r.Context(), claims.OrgID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
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

	webhook, err := c.webhookService.GetWebhook(r.Context(), claims.OrgID, webhookUUID)
	if err != nil {
		response.NotFound(r, err.Error())
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

	webhooks, err := c.webhookService.ListWebhooks(r.Context(), claims.OrgID)
	if err != nil {
		response.InternalError(r, err.Error())
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
		response.BadRequest(r, err.Error())
		return
	}

	webhook, err := c.webhookService.UpdateWebhook(r.Context(), claims.OrgID, webhookUUID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
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

	err := c.webhookService.DeleteWebhook(r.Context(), claims.OrgID, webhookUUID)
	if err != nil {
		response.NotFound(r, err.Error())
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

	newSecret, err := c.webhookService.RotateSecret(r.Context(), claims.OrgID, webhookUUID)
	if err != nil {
		response.BadRequest(r, err.Error())
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

	calls, err := c.webhookService.GetWebhookCalls(r.Context(), claims.OrgID, webhookUUID, limit)
	if err != nil {
		response.InternalError(r, err.Error())
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

	// Get webhook
	webhook, err := c.webhookService.GetWebhook(r.Context(), claims.OrgID, webhookUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	// Send test event
	testPayload := map[string]interface{}{
		"test":    true,
		"message": "This is a test webhook event from mailat.co",
	}

	err = c.webhookService.DeliverWebhook(r.Context(), webhook.ID, "test", testPayload)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Test webhook queued for delivery", nil)
}
