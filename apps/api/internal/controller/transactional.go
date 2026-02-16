package controller

import (
	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

type TransactionalController struct {
	transactionalService *service.TransactionalService
}

func NewTransactionalController(transactionalService *service.TransactionalService) *TransactionalController {
	return &TransactionalController{transactionalService: transactionalService}
}

// SendEmail sends a single transactional email
// POST /api/v1/emails
func (c *TransactionalController) SendEmail(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.SendEmailRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	// Get idempotency key from header
	req.IdempotencyKey = r.Header.Get("Idempotency-Key")

	// Validate at least one recipient
	if len(req.To) == 0 {
		response.BadRequest(r, "At least one recipient required")
		return
	}

	// Validate body or template
	if req.HTML == "" && req.Text == "" && req.TemplateID == "" {
		response.BadRequest(r, "Email body or templateId required")
		return
	}

	result, err := c.transactionalService.SendEmail(r.Context(), claims.OrgID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Email queued", result)
}

// BatchSendEmail sends multiple emails in batch
// POST /api/v1/emails/batch
func (c *TransactionalController) BatchSendEmail(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.BatchSendRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	if len(req.Emails) == 0 {
		response.BadRequest(r, "At least one email required")
		return
	}

	result, err := c.transactionalService.BatchSendEmail(r.Context(), claims.OrgID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.Success(r, result)
}

// GetEmailStatus retrieves the status of a sent email
// GET /api/v1/emails/:id
func (c *TransactionalController) GetEmailStatus(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	emailID := r.Get("id").String()
	if emailID == "" {
		response.BadRequest(r, "Email ID required")
		return
	}

	result, err := c.transactionalService.GetEmailStatus(r.Context(), claims.OrgID, emailID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, result)
}

// CancelEmail cancels a scheduled email
// DELETE /api/v1/emails/:id
func (c *TransactionalController) CancelEmail(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	emailID := r.Get("id").String()
	if emailID == "" {
		response.BadRequest(r, "Email ID required")
		return
	}

	err := c.transactionalService.CancelEmail(r.Context(), claims.OrgID, emailID)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Email cancelled", nil)
}

// Template endpoints

// CreateTemplate creates a new email template
// POST /api/v1/templates
func (c *TransactionalController) CreateTemplate(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.CreateTemplateRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	template, err := c.transactionalService.CreateTemplate(r.Context(), claims.OrgID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Template created", template)
}

// GetTemplate retrieves a template by UUID
// GET /api/v1/templates/:uuid
func (c *TransactionalController) GetTemplate(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	templateUUID := r.Get("uuid").String()
	if templateUUID == "" {
		response.BadRequest(r, "Template UUID required")
		return
	}

	template, err := c.transactionalService.GetTemplate(r.Context(), claims.OrgID, templateUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, template)
}

// ListTemplates returns all templates for the organization
// GET /api/v1/templates
func (c *TransactionalController) ListTemplates(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	templates, err := c.transactionalService.ListTemplates(r.Context(), claims.OrgID)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, templates)
}

// UpdateTemplate updates a template
// PUT /api/v1/templates/:uuid
func (c *TransactionalController) UpdateTemplate(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	templateUUID := r.Get("uuid").String()
	if templateUUID == "" {
		response.BadRequest(r, "Template UUID required")
		return
	}

	var req model.UpdateTemplateRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	template, err := c.transactionalService.UpdateTemplate(r.Context(), claims.OrgID, templateUUID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Template updated", template)
}

// DeleteTemplate deletes a template
// DELETE /api/v1/templates/:uuid
func (c *TransactionalController) DeleteTemplate(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	templateUUID := r.Get("uuid").String()
	if templateUUID == "" {
		response.BadRequest(r, "Template UUID required")
		return
	}

	err := c.transactionalService.DeleteTemplate(r.Context(), claims.OrgID, templateUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Template deleted", nil)
}

// PreviewTemplate renders a template with variables
// POST /api/v1/templates/:uuid/preview
func (c *TransactionalController) PreviewTemplate(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	templateUUID := r.Get("uuid").String()
	if templateUUID == "" {
		response.BadRequest(r, "Template UUID required")
		return
	}

	var req model.PreviewTemplateRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	preview, err := c.transactionalService.PreviewTemplate(r.Context(), claims.OrgID, templateUUID, req.Variables)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, preview)
}
