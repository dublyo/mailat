package controller

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
	"github.com/gogf/gf/v2/net/ghttp"
)

func mailboxError(r *ghttp.Request, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidMailboxInput):
		response.BadRequest(r, err.Error())
	case errors.Is(err, service.ErrMailboxNotFound):
		response.NotFound(r, "Message or resource not found")
	case errors.Is(err, service.ErrMailboxConflict):
		r.Response.Status = 409
		response.Error(r, 409, err.Error())
	case errors.Is(err, service.ErrMailboxCursorExpired):
		r.Response.Status = 410
		response.Error(r, 410, err.Error())
	default:
		response.InternalError(r, "Mailbox operation failed")
	}
}
func (c *ReceivedInboxController) ListLabels(r *ghttp.Request) {
	x := middleware.GetClaims(r)
	result, err := c.inboxService.ListLabels(r.Context(), x.UserID)
	if err != nil {
		mailboxError(r, err)
		return
	}
	response.Success(r, result)
}
func (c *ReceivedInboxController) SaveLabel(r *ghttp.Request) {
	x := middleware.GetClaims(r)
	var req model.UpdateLabelRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, "Invalid label request")
		return
	}
	result, err := c.inboxService.SaveLabel(r.Context(), x.OrgID, x.UserID, r.Get("uuid").String(), req.Name, req.Color)
	if err != nil {
		mailboxError(r, err)
		return
	}
	response.Success(r, result)
}
func (c *ReceivedInboxController) DeleteLabel(r *ghttp.Request) {
	x := middleware.GetClaims(r)
	if err := c.inboxService.DeleteLabel(r.Context(), x.UserID, r.Get("uuid").String()); err != nil {
		mailboxError(r, err)
		return
	}
	response.SuccessWithMessage(r, "Label deleted", nil)
}
func (c *ReceivedInboxController) LabelEmails(r *ghttp.Request) {
	x := middleware.GetClaims(r)
	var req model.LabelEmailsRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, "Invalid label selection")
		return
	}
	if err := c.inboxService.LabelReceivedEmails(r.Context(), x.UserID, req.EmailUUIDs, req.AddLabels, req.RemoveLabels); err != nil {
		mailboxError(r, err)
		return
	}
	response.SuccessWithMessage(r, "Labels updated", nil)
}
func (c *ReceivedInboxController) ListFilters(r *ghttp.Request) {
	x := middleware.GetClaims(r)
	result, err := c.inboxService.ListFilters(r.Context(), x.UserID)
	if err != nil {
		mailboxError(r, err)
		return
	}
	response.Success(r, result)
}
func (c *ReceivedInboxController) GetFilter(r *ghttp.Request) {
	x := middleware.GetClaims(r)
	result, err := c.inboxService.GetFilter(r.Context(), x.UserID, r.Get("uuid").String())
	if err != nil {
		mailboxError(r, err)
		return
	}
	response.Success(r, result)
}
func (c *ReceivedInboxController) SaveFilter(r *ghttp.Request) {
	x := middleware.GetClaims(r)
	id := r.Get("uuid").String()
	f := &model.InboxFilter{Active: true, ConditionLogic: "all", Conditions: []model.FilterCondition{}, ActionLabels: []string{}}
	if id != "" {
		var err error
		f, err = c.inboxService.GetFilter(r.Context(), x.UserID, id)
		if err != nil {
			mailboxError(r, err)
			return
		}
	}
	// Merge only editable fields, preserving omitted values on PUT. Explicit
	// null identityId changes a scoped filter back to all the user's identities.
	var fields map[string]json.RawMessage
	if len(r.GetBody()) > 65536 || json.Unmarshal(r.GetBody(), &fields) != nil || fields == nil {
		response.BadRequest(r, "Invalid filter JSON")
		return
	}
	allowed := map[string]bool{"name": true, "identityId": true, "priority": true, "active": true, "conditions": true, "conditionLogic": true, "actionLabels": true, "actionFolder": true, "actionStar": true, "actionMarkRead": true, "actionArchive": true, "actionTrash": true, "actionForward": true}
	for key := range fields {
		if !allowed[key] {
			response.BadRequest(r, fmt.Sprintf("Unknown or read-only filter field: %s", key))
			return
		}
	}
	if err := json.Unmarshal(r.GetBody(), f); err != nil {
		response.BadRequest(r, "Invalid filter values")
		return
	}
	result, err := c.inboxService.SaveFilter(r.Context(), x.OrgID, x.UserID, id, f)
	if err != nil {
		mailboxError(r, err)
		return
	}
	response.Success(r, result)
}
func (c *ReceivedInboxController) DeleteFilter(r *ghttp.Request) {
	x := middleware.GetClaims(r)
	if err := c.inboxService.DeleteFilter(r.Context(), x.UserID, r.Get("uuid").String()); err != nil {
		mailboxError(r, err)
		return
	}
	response.SuccessWithMessage(r, "Filter deleted", nil)
}
func (c *ReceivedInboxController) TestFilter(r *ghttp.Request) {
	x := middleware.GetClaims(r)
	f, err := c.inboxService.GetFilter(r.Context(), x.UserID, r.Get("uuid").String())
	if err != nil {
		mailboxError(r, err)
		return
	}
	var req service.InboxFilterTestInput
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, "Invalid filter sample")
		return
	}
	response.Success(r, service.TestInboxFilter(f, req))
}
func (c *ReceivedInboxController) Changes(r *ghttp.Request) {
	x := middleware.GetClaims(r)
	result, err := c.inboxService.Changes(r.Context(), x.UserID, r.GetQuery("cursor").String(), r.GetQuery("limit", 100).Int())
	if err != nil {
		mailboxError(r, err)
		return
	}
	response.Success(r, result)
}
