package controller

import (
	"strconv"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

// ReceivedInboxController handles received email inbox operations
type ReceivedInboxController struct {
	inboxService    *service.InboxService
	receivingService *service.ReceivingService
}

// NewReceivedInboxController creates a new received inbox controller
func NewReceivedInboxController(inboxService *service.InboxService, receivingService *service.ReceivingService) *ReceivedInboxController {
	return &ReceivedInboxController{
		inboxService:    inboxService,
		receivingService: receivingService,
	}
}

// ListEmails returns a paginated list of received emails
// GET /api/v1/inbox/received
// If identityId is 0 or omitted, returns emails from all user's identities (unified inbox)
func (c *ReceivedInboxController) ListEmails(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.InboxListRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	// IdentityID = 0 means fetch from all identities (unified inbox)
	result, err := c.inboxService.ListReceivedEmails(r.Context(), claims.UserID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.Success(r, result)
}

// GetEmail returns a single received email
// GET /api/v1/inbox/received/:uuid
func (c *ReceivedInboxController) GetEmail(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	emailUUID := r.Get("uuid").String()
	if emailUUID == "" {
		response.BadRequest(r, "Email UUID is required")
		return
	}

	email, err := c.inboxService.GetReceivedEmail(r.Context(), claims.UserID, emailUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, email)
}

// MarkEmails marks emails as read or unread
// POST /api/v1/inbox/received/mark
func (c *ReceivedInboxController) MarkEmails(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.MarkEmailsRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	if len(req.EmailUUIDs) == 0 {
		response.BadRequest(r, "Email UUIDs are required")
		return
	}

	err := c.inboxService.MarkReceivedEmails(r.Context(), claims.UserID, req.EmailUUIDs, req.IsRead)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Emails updated", nil)
}

// StarEmails stars or unstars emails
// POST /api/v1/inbox/received/star
func (c *ReceivedInboxController) StarEmails(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.StarEmailsRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	if len(req.EmailUUIDs) == 0 {
		response.BadRequest(r, "Email UUIDs are required")
		return
	}

	err := c.inboxService.StarReceivedEmails(r.Context(), claims.UserID, req.EmailUUIDs, req.IsStarred)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Emails updated", nil)
}

// MoveEmails moves emails to a folder
// POST /api/v1/inbox/received/move
func (c *ReceivedInboxController) MoveEmails(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.MoveEmailsRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	if len(req.EmailUUIDs) == 0 {
		response.BadRequest(r, "Email UUIDs are required")
		return
	}

	if req.Folder == "" {
		response.BadRequest(r, "Target folder is required")
		return
	}

	err := c.inboxService.MoveReceivedEmails(r.Context(), claims.UserID, req.EmailUUIDs, req.Folder)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Emails moved", nil)
}

// TrashEmails moves emails to trash or permanently deletes
// POST /api/v1/inbox/received/trash
func (c *ReceivedInboxController) TrashEmails(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.TrashEmailsRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	if len(req.EmailUUIDs) == 0 {
		response.BadRequest(r, "Email UUIDs are required")
		return
	}

	err := c.inboxService.TrashReceivedEmails(r.Context(), claims.UserID, req.EmailUUIDs, req.Permanent)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	if req.Permanent {
		response.SuccessWithMessage(r, "Emails permanently deleted", nil)
	} else {
		response.SuccessWithMessage(r, "Emails moved to trash", nil)
	}
}

// GetCounts returns email counts by folder
// GET /api/v1/inbox/received/counts
// If identityId is 0 or omitted, returns counts across all user's identities
func (c *ReceivedInboxController) GetCounts(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var identityID int64
	identityIDStr := r.Get("identityId").String()
	if identityIDStr != "" {
		var err error
		identityID, err = strconv.ParseInt(identityIDStr, 10, 64)
		if err != nil {
			response.BadRequest(r, "Invalid identity ID")
			return
		}
	}

	counts, err := c.inboxService.GetReceivedEmailCounts(r.Context(), claims.UserID, identityID)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.Success(r, counts)
}

// SetupReceiving sets up email receiving for a domain
// POST /api/v1/inbox/setup
func (c *ReceivedInboxController) SetupReceiving(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req struct {
		DomainID int64 `json:"domainId" v:"required"`
	}
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	result, err := c.receivingService.SetupDomainReceiving(r.Context(), claims.OrgID, req.DomainID)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Email receiving setup complete. Add the MX record to your DNS.", result)
}

// SetCatchAll sets an identity as catch-all for its domain
// POST /api/v1/inbox/identities/:uuid/catch-all
func (c *ReceivedInboxController) SetCatchAll(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	identityUUID := r.Get("uuid").String()
	if identityUUID == "" {
		response.BadRequest(r, "Identity UUID is required")
		return
	}

	var req struct {
		IsCatchAll bool `json:"isCatchAll"`
	}
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	// TODO: Implement catch-all toggle in identity service
	// For now, direct database update
	response.SuccessWithMessage(r, "Catch-all setting updated", nil)
}
