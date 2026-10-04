package controller

import (
	"errors"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/google/uuid"
	"net/http"
	"strings"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
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
	r.Request.Body = http.MaxBytesReader(r.Response.Writer, r.Request.Body, 18*1024*1024)
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

	if header := r.Header.Get("Idempotency-Key"); header != "" {
		if req.IdempotencyKey != "" && req.IdempotencyKey != header {
			response.BadRequest(r, "header and body idempotency keys must match")
			return
		}
		req.IdempotencyKey = header
	}
	if !validHTTPSubmissionKey(req.IdempotencyKey) {
		response.BadRequest(r, "an Idempotency-Key of 8 to 128 characters is required")
		return
	}

	// Validate at least one recipient
	if len(req.To)+len(req.Cc)+len(req.Bcc) == 0 {
		response.BadRequest(r, "At least one recipient required")
		return
	}

	// Validate body or template
	if req.HTML == "" && req.Text == "" && req.TemplateID == "" && len(req.Attachments) == 0 {
		response.BadRequest(r, "Email body or templateId required")
		return
	}

	result, err := c.transactionalService.SendEmailForUser(r.Context(), claims.OrgID, claims.UserID, &req)
	if err != nil {
		writeTransactionalError(r, err)
		return
	}

	response.SuccessWithMessage(r, "Email queued", result)
}

// BatchSendEmail sends multiple emails in batch
// POST /api/v1/emails/batch
func (c *TransactionalController) BatchSendEmail(r *ghttp.Request) {
	r.Request.Body = http.MaxBytesReader(r.Response.Writer, r.Request.Body, 18*1024*1024)
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

	req.IdempotencyKey = r.Header.Get("Idempotency-Key")
	if !validHTTPSubmissionKey(req.IdempotencyKey) {
		response.BadRequest(r, "an Idempotency-Key header of 8 to 128 characters is required")
		return
	}
	if len(req.Emails) == 0 {
		response.BadRequest(r, "At least one email required")
		return
	}

	result, err := c.transactionalService.BatchSendEmailForUser(r.Context(), claims.OrgID, claims.UserID, &req)
	if err != nil {
		writeTransactionalError(r, err)
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
	if _, err := uuid.Parse(emailID); err != nil {
		response.BadRequest(r, "A valid email UUID is required")
		return
	}

	result, err := c.transactionalService.GetEmailStatusForUser(r.Context(), claims.OrgID, claims.UserID, emailID)
	if err != nil {
		if err.Error() == "email not found" {
			response.NotFound(r, "email not found")
		} else {
			writeTransactionalError(r, err)
		}
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
	if _, err := uuid.Parse(emailID); err != nil {
		response.BadRequest(r, "A valid email UUID is required")
		return
	}

	err := c.transactionalService.CancelEmailForUser(r.Context(), claims.OrgID, claims.UserID, emailID)
	if err != nil {
		if err.Error() == "email not found or cannot be cancelled" {
			response.BadRequest(r, "email not found or cannot be cancelled")
		} else {
			writeTransactionalError(r, err)
		}
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

func validHTTPSubmissionKey(key string) bool {
	return len(key) >= 8 && len(key) <= 128 && !strings.ContainsAny(key, "\r\n")
}
func writeTransactionalError(r *ghttp.Request, err error) {
	if errors.Is(err, service.ErrSubmissionConflict) {
		r.Response.Status = http.StatusConflict
		response.Error(r, http.StatusConflict, err.Error())
		return
	}
	var validation *provider.MailValidationError
	if errors.As(err, &validation) {
		response.BadRequest(r, validation.Error())
		return
	}
	r.Response.Status = http.StatusServiceUnavailable
	response.Error(r, http.StatusServiceUnavailable, "Mail service is temporarily unavailable; retry unchanged content with the same idempotency key")
}
