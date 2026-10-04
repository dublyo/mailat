package controller

import (
	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/pkg/response"
	"github.com/gogf/gf/v2/net/ghttp"
)

func (c *WebhookController) ListDeliveries(r *ghttp.Request) {
	cl := middleware.GetClaims(r)
	if cl == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}
	v, err := c.webhookService.Deliveries(r.Context(), cl.OrgID, cl.UserID, r.Get("status").String(), r.Get("page").Int(), r.Get("pageSize").Int())
	if err != nil {
		response.InternalError(r, "Could not load webhook deliveries")
		return
	}
	response.Success(r, v)
}
func (c *WebhookController) GetDelivery(r *ghttp.Request) {
	cl := middleware.GetClaims(r)
	if cl == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}
	d, a, err := c.webhookService.Delivery(r.Context(), cl.OrgID, cl.UserID, r.Get("uuid").String())
	if err != nil {
		response.NotFound(r, "Webhook delivery not found")
		return
	}
	response.Success(r, map[string]any{"delivery": d, "attempts": a})
}
func (c *WebhookController) ReplayDelivery(r *ghttp.Request) {
	cl := middleware.GetClaims(r)
	if cl == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}
	if err := c.webhookService.Replay(r.Context(), cl.OrgID, cl.UserID, r.Get("uuid").String()); err != nil {
		response.BadRequest(r, "Delivery not found or still pending")
		return
	}
	response.SuccessWithMessage(r, "Delivery queued for replay", nil)
}
func (c *Phase5Controller) UpdateWebhookTrigger(r *ghttp.Request) {
	cl := middleware.GetClaims(r)
	if cl == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}
	s := c.webhookTriggerService.ForUser(cl.UserID)
	id, err := s.ResolveID(r.Context(), cl.OrgID, r.Get("id").String())
	if err != nil {
		response.NotFound(r, "Webhook trigger not found")
		return
	}
	var input struct {
		Name        *string                 `json:"name"`
		Description *string                 `json:"description"`
		WebhookURL  *string                 `json:"webhookUrl"`
		Filters     *map[string]interface{} `json:"filters"`
		Active      *bool                   `json:"active"`
	}
	if err = r.Parse(&input); err != nil {
		response.BadRequest(r, "Invalid trigger fields")
		return
	}
	v, err := s.Update(r.Context(), cl.OrgID, id, input.Name, input.Description, input.WebhookURL, input.Filters, input.Active)
	if err != nil {
		response.BadRequest(r, "Could not update trigger; use a public HTTPS endpoint")
		return
	}
	response.Success(r, v)
}
func (c *Phase5Controller) RotateWebhookTriggerSecret(r *ghttp.Request) {
	cl := middleware.GetClaims(r)
	if cl == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}
	s := c.webhookTriggerService.ForUser(cl.UserID)
	id, err := s.ResolveID(r.Context(), cl.OrgID, r.Get("id").String())
	if err != nil {
		response.NotFound(r, "Webhook trigger not found")
		return
	}
	secret, err := s.RotateSecret(r.Context(), cl.OrgID, id)
	if err != nil {
		response.BadRequest(r, "Could not rotate secret")
		return
	}
	response.Success(r, model.RotateSecretResponse{Secret: secret})
}
