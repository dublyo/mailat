package controller

import (
	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

type InboxController struct {
	inboxService *service.InboxService
}

func NewInboxController(inboxService *service.InboxService) *InboxController {
	return &InboxController{inboxService: inboxService}
}

// GetMailboxes returns all mailboxes across all user's identities
// GET /api/v1/inbox/mailboxes
func (c *InboxController) GetMailboxes(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	mailboxes, err := c.inboxService.GetUnifiedMailboxes(r.Context(), claims.UserID)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, mailboxes)
}

// GetInbox returns unified inbox with emails from all identities
// GET /api/v1/inbox
func (c *InboxController) GetInbox(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.UnifiedInboxRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	// Set defaults
	if req.Page <= 0 {
		req.Page = 1
	}
	if req.PageSize <= 0 {
		req.PageSize = 50
	}
	if req.PageSize > 100 {
		req.PageSize = 100
	}

	inbox, err := c.inboxService.GetUnifiedInbox(r.Context(), claims.UserID, &req)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, inbox)
}

// GetEmail returns a single email by ID
// GET /api/v1/inbox/emails/:id
func (c *InboxController) GetEmail(r *ghttp.Request) {
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

	email, err := c.inboxService.GetEmail(r.Context(), claims.UserID, emailID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, email)
}

// GetThread returns all emails in a thread
// GET /api/v1/inbox/threads/:id
func (c *InboxController) GetThread(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	threadID := r.Get("id").String()
	if threadID == "" {
		response.BadRequest(r, "Thread ID required")
		return
	}

	emails, err := c.inboxService.GetThread(r.Context(), claims.UserID, threadID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, emails)
}

// MarkRead marks emails as read or unread
// POST /api/v1/inbox/mark-read
func (c *InboxController) MarkRead(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.MarkReadRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	if len(req.EmailIDs) == 0 {
		response.BadRequest(r, "At least one email ID required")
		return
	}

	err := c.inboxService.MarkEmailsRead(r.Context(), claims.UserID, req.EmailIDs, req.Read)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Emails updated", nil)
}

// ToggleFlag toggles the flagged status of emails
// POST /api/v1/inbox/toggle-flag
func (c *InboxController) ToggleFlag(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.FlagEmailRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	if len(req.EmailIDs) == 0 {
		response.BadRequest(r, "At least one email ID required")
		return
	}

	err := c.inboxService.ToggleEmailFlag(r.Context(), claims.UserID, req.EmailIDs, req.Flagged)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Emails updated", nil)
}

// MoveEmails moves emails to a different mailbox
// POST /api/v1/inbox/move
func (c *InboxController) MoveEmails(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.MoveEmailRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	if len(req.EmailIDs) == 0 {
		response.BadRequest(r, "At least one email ID required")
		return
	}

	err := c.inboxService.MoveEmails(r.Context(), claims.UserID, req.EmailIDs, req.TargetMailboxID)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Emails moved", nil)
}

// DeleteEmails deletes or moves emails to trash
// POST /api/v1/inbox/delete
func (c *InboxController) DeleteEmails(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.DeleteEmailRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	if len(req.EmailIDs) == 0 {
		response.BadRequest(r, "At least one email ID required")
		return
	}

	err := c.inboxService.DeleteEmails(r.Context(), claims.UserID, req.EmailIDs, req.Permanent)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Emails deleted", nil)
}

// Search searches emails using full-text search
// GET /api/v1/inbox/search
func (c *InboxController) Search(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	query := r.Get("q").String()
	if query == "" {
		response.BadRequest(r, "Search query required")
		return
	}

	page := r.Get("page").Int()
	if page <= 0 {
		page = 1
	}
	pageSize := r.Get("pageSize").Int()
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}

	req := &model.UnifiedInboxRequest{
		Page:     page,
		PageSize: pageSize,
		Search:   query,
	}

	inbox, err := c.inboxService.GetUnifiedInbox(r.Context(), claims.UserID, req)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, inbox)
}
