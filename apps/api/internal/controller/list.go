package controller

import (
	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

type ListController struct {
	listService *service.ListService
}

func NewListController(listService *service.ListService) *ListController {
	return &ListController{listService: listService}
}

// CreateList creates a new contact list
// POST /api/v1/lists
func (c *ListController) Create(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.CreateListRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	list, err := c.listService.CreateList(r.Context(), claims.OrgID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "List created", list)
}

// GetList retrieves a list by UUID
// GET /api/v1/lists/:uuid
func (c *ListController) Get(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	listUUID := r.Get("uuid").String()
	if listUUID == "" {
		response.BadRequest(r, "List UUID required")
		return
	}

	list, err := c.listService.GetList(r.Context(), claims.OrgID, listUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, list)
}

// ListLists retrieves all lists
// GET /api/v1/lists
func (c *ListController) List(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	lists, err := c.listService.ListLists(r.Context(), claims.OrgID)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, lists)
}

// UpdateList updates a list
// PUT /api/v1/lists/:uuid
func (c *ListController) Update(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	listUUID := r.Get("uuid").String()
	if listUUID == "" {
		response.BadRequest(r, "List UUID required")
		return
	}

	var req model.UpdateListRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	list, err := c.listService.UpdateList(r.Context(), claims.OrgID, listUUID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "List updated", list)
}

// DeleteList deletes a list
// DELETE /api/v1/lists/:uuid
func (c *ListController) Delete(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	listUUID := r.Get("uuid").String()
	if listUUID == "" {
		response.BadRequest(r, "List UUID required")
		return
	}

	err := c.listService.DeleteList(r.Context(), claims.OrgID, listUUID)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "List deleted", nil)
}

// AddContacts adds contacts to a list
// POST /api/v1/lists/:uuid/contacts
func (c *ListController) AddContacts(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	listUUID := r.Get("uuid").String()
	if listUUID == "" {
		response.BadRequest(r, "List UUID required")
		return
	}

	var req model.AddContactsToListRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	err := c.listService.AddContactsToList(r.Context(), claims.OrgID, listUUID, req.ContactUUIDs)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Contacts added to list", nil)
}

// RemoveContacts removes contacts from a list
// DELETE /api/v1/lists/:uuid/contacts
func (c *ListController) RemoveContacts(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	listUUID := r.Get("uuid").String()
	if listUUID == "" {
		response.BadRequest(r, "List UUID required")
		return
	}

	var req model.RemoveContactsFromListRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	err := c.listService.RemoveContactsFromList(r.Context(), claims.OrgID, listUUID, req.ContactUUIDs)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Contacts removed from list", nil)
}

// GetListContacts retrieves contacts in a list
// GET /api/v1/lists/:uuid/contacts
func (c *ListController) GetListContacts(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	listUUID := r.Get("uuid").String()
	if listUUID == "" {
		response.BadRequest(r, "List UUID required")
		return
	}

	page := r.GetQuery("page", 1).Int()
	pageSize := r.GetQuery("pageSize", 50).Int()

	result, err := c.listService.GetListContacts(r.Context(), claims.OrgID, listUUID, page, pageSize)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.Success(r, result)
}

// ImportContactsToList imports contacts directly to a list
// POST /api/v1/lists/:uuid/contacts/import
func (c *ListController) ImportContactsToList(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	listUUID := r.Get("uuid").String()
	if listUUID == "" {
		response.BadRequest(r, "List UUID required")
		return
	}

	var req model.ImportContactsToListRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	result, err := c.listService.ImportContactsToList(r.Context(), claims.OrgID, listUUID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Contacts imported to list", result)
}

// ManualAddContactToList creates a contact and adds it to a list
// POST /api/v1/lists/:uuid/contacts/manual
func (c *ListController) ManualAddContactToList(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	listUUID := r.Get("uuid").String()
	if listUUID == "" {
		response.BadRequest(r, "List UUID required")
		return
	}

	var req model.ManualAddContactToListRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	contact, err := c.listService.ManualAddContactToList(r.Context(), claims.OrgID, listUUID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Contact added to list", contact)
}
