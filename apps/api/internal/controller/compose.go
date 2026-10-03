package controller

import (
	"errors"
	"io"
	"net/http"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

type ComposeController struct {
	composeService *service.ComposeService
}

func NewComposeController(composeService *service.ComposeService) *ComposeController {
	return &ComposeController{composeService: composeService}
}

// SendEmail sends a new email
// POST /api/v1/compose/send
func (c *ComposeController) SendEmail(r *ghttp.Request) {
	r.Request.Body = http.MaxBytesReader(r.Response.Writer, r.Request.Body, 18*1024*1024)
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.ComposeEmailRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	if len(req.To)+len(req.Cc)+len(req.Bcc) == 0 {
		response.BadRequest(r, "At least one recipient required")
		return
	}

	// Convert request to service model
	email := &service.ComposeEmail{
		IdentityID:    req.IdentityID,
		Subject:       req.Subject,
		TextBody:      req.TextBody,
		HTMLBody:      req.HTMLBody,
		InReplyTo:     req.InReplyTo,
		References:    req.References,
		From:          service.EmailAddress{Email: req.FromEmail},
		DraftID:       req.DraftID,
		DraftVersion:  req.DraftVersion,
		SubmissionKey: r.Header.Get("Idempotency-Key"),
	}

	// Convert addresses
	for _, addr := range req.To {
		email.To = append(email.To, service.EmailAddress{Name: addr.Name, Email: addr.Email})
	}
	for _, addr := range req.Cc {
		email.Cc = append(email.Cc, service.EmailAddress{Name: addr.Name, Email: addr.Email})
	}
	for _, addr := range req.Bcc {
		email.Bcc = append(email.Bcc, service.EmailAddress{Name: addr.Name, Email: addr.Email})
	}
	for _, addr := range req.ReplyTo {
		email.ReplyTo = append(email.ReplyTo, service.EmailAddress{Name: addr.Name, Email: addr.Email})
	}

	// Convert attachments
	for _, att := range req.Attachments {
		email.Attachments = append(email.Attachments, service.AttachmentRef{
			BlobID:      att.BlobID,
			Name:        att.Name,
			Type:        att.Type,
			Size:        att.Size,
			Disposition: att.Disposition,
			CID:         att.CID,
			Content:     att.Content,
		})
	}

	result, err := c.composeService.SendEmail(r.Context(), claims.UserID, email)
	if err != nil {
		writeComposeError(r, err)
		return
	}

	response.SuccessWithMessage(r, "Send attempt recorded", result)
}

// SaveDraft saves an email as a draft
// POST /api/v1/compose/drafts
func (c *ComposeController) SaveDraft(r *ghttp.Request) {
	r.Request.Body = http.MaxBytesReader(r.Response.Writer, r.Request.Body, 18*1024*1024)
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.SaveDraftRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	email := &service.ComposeEmail{
		IdentityID:   req.IdentityID,
		Subject:      req.Subject,
		TextBody:     req.TextBody,
		HTMLBody:     req.HTMLBody,
		InReplyTo:    req.InReplyTo,
		References:   req.References,
		IsDraft:      true,
		From:         service.EmailAddress{Email: req.FromEmail},
		DraftVersion: req.Version,
	}

	// Convert addresses
	for _, addr := range req.To {
		email.To = append(email.To, service.EmailAddress{Name: addr.Name, Email: addr.Email})
	}
	for _, addr := range req.Cc {
		email.Cc = append(email.Cc, service.EmailAddress{Name: addr.Name, Email: addr.Email})
	}
	for _, addr := range req.Bcc {
		email.Bcc = append(email.Bcc, service.EmailAddress{Name: addr.Name, Email: addr.Email})
	}

	for _, addr := range req.ReplyTo {
		email.ReplyTo = append(email.ReplyTo, service.EmailAddress{Name: addr.Name, Email: addr.Email})
	}

	// Convert attachments
	for _, att := range req.Attachments {
		email.Attachments = append(email.Attachments, service.AttachmentRef{
			BlobID:      att.BlobID,
			Name:        att.Name,
			Type:        att.Type,
			Size:        att.Size,
			Disposition: att.Disposition,
			CID:         att.CID,
			Content:     att.Content,
		})
	}

	result, err := c.composeService.SaveDraft(r.Context(), claims.UserID, email)
	if err != nil {
		writeComposeError(r, err)
		return
	}

	response.SuccessWithMessage(r, "Draft saved", result)
}

// UpdateDraft updates an existing draft
// PUT /api/v1/compose/drafts/:id
func (c *ComposeController) UpdateDraft(r *ghttp.Request) {
	r.Request.Body = http.MaxBytesReader(r.Response.Writer, r.Request.Body, 18*1024*1024)
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	draftID := r.Get("id").String()
	if draftID == "" {
		response.BadRequest(r, "Draft ID required")
		return
	}

	var req model.SaveDraftRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	email := &service.ComposeEmail{
		IdentityID:   req.IdentityID,
		Subject:      req.Subject,
		TextBody:     req.TextBody,
		HTMLBody:     req.HTMLBody,
		InReplyTo:    req.InReplyTo,
		References:   req.References,
		IsDraft:      true,
		From:         service.EmailAddress{Email: req.FromEmail},
		DraftVersion: req.Version,
	}

	// Convert addresses
	for _, addr := range req.To {
		email.To = append(email.To, service.EmailAddress{Name: addr.Name, Email: addr.Email})
	}
	for _, addr := range req.Cc {
		email.Cc = append(email.Cc, service.EmailAddress{Name: addr.Name, Email: addr.Email})
	}
	for _, addr := range req.Bcc {
		email.Bcc = append(email.Bcc, service.EmailAddress{Name: addr.Name, Email: addr.Email})
	}

	for _, addr := range req.ReplyTo {
		email.ReplyTo = append(email.ReplyTo, service.EmailAddress{Name: addr.Name, Email: addr.Email})
	}

	// Convert attachments
	for _, att := range req.Attachments {
		email.Attachments = append(email.Attachments, service.AttachmentRef{
			BlobID:      att.BlobID,
			Name:        att.Name,
			Type:        att.Type,
			Size:        att.Size,
			Disposition: att.Disposition,
			CID:         att.CID,
			Content:     att.Content,
		})
	}

	result, err := c.composeService.UpdateDraft(r.Context(), claims.UserID, draftID, email)
	if err != nil {
		writeComposeError(r, err)
		return
	}

	response.SuccessWithMessage(r, "Draft updated", result)
}

// DeleteDraft deletes a draft
// DELETE /api/v1/compose/drafts/:id
func (c *ComposeController) DeleteDraft(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	draftID := r.Get("id").String()
	if draftID == "" {
		response.BadRequest(r, "Draft ID required")
		return
	}

	err := c.composeService.DeleteDraft(r.Context(), claims.UserID, draftID)
	if err != nil {
		writeComposeError(r, err)
		return
	}

	response.SuccessWithMessage(r, "Draft deleted", nil)
}

// GetReplyContext gets context for replying to an email
// GET /api/v1/compose/reply/:id
func (c *ComposeController) GetReplyContext(r *ghttp.Request) {
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

	replyAll := r.Get("replyAll").Bool()

	context, err := c.composeService.GetReplyContext(r.Context(), claims.UserID, emailID, replyAll)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, context)
}

// GetForwardContext gets context for forwarding an email
// GET /api/v1/compose/forward/:id
func (c *ComposeController) GetForwardContext(r *ghttp.Request) {
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

	context, err := c.composeService.GetForwardContext(r.Context(), claims.UserID, emailID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, context)
}

// UploadAttachment uploads an attachment
// POST /api/v1/compose/attachments
func (c *ComposeController) UploadAttachment(r *ghttp.Request) {
	r.Request.Body = http.MaxBytesReader(r.Response.Writer, r.Request.Body, provider.MaxAttachmentBytes+1024*1024)
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	identityID := r.Get("identityId").Int64()
	if identityID == 0 {
		response.BadRequest(r, "Identity ID required")
		return
	}

	file := r.GetUploadFile("file")
	if file == nil {
		response.BadRequest(r, "File required")
		return
	}

	// Read file data
	f, err := file.Open()
	if err != nil {
		response.BadRequest(r, "Failed to read file")
		return
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, provider.MaxAttachmentBytes+1))
	if err != nil {
		response.BadRequest(r, "Failed to read file data")
		return
	}
	if len(data) > provider.MaxAttachmentBytes {
		response.BadRequest(r, "attachment exceeds 10 MiB")
		return
	}

	contentType := file.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	result, err := c.composeService.UploadAttachment(r.Context(), claims.UserID, identityID, data, file.Filename, contentType)
	if err != nil {
		writeComposeError(r, err)
		return
	}

	response.Success(r, result)
}

// Only positively identified request errors can release a client's frozen send key.
// Database, S3 and other unclassified failures may occur while replaying an accepted send.
func composeErrorDetails(err error) (int, string) {
	if errors.Is(err, service.ErrDraftConflict) || errors.Is(err, service.ErrSubmissionConflict) {
		return http.StatusConflict, err.Error()
	}
	var validation *provider.MailValidationError
	if errors.As(err, &validation) {
		return http.StatusBadRequest, validation.Error()
	}
	return http.StatusServiceUnavailable, "Mail service is temporarily unavailable. Keep the same submission key and unchanged content when retrying; delivery may already have been attempted."
}

func writeComposeError(r *ghttp.Request, err error) {
	status, message := composeErrorDetails(err)
	r.Response.Status = status
	response.Error(r, status, message)
}
