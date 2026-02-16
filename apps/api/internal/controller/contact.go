package controller

import (
	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

type ContactController struct {
	contactService *service.ContactService
}

func NewContactController(contactService *service.ContactService) *ContactController {
	return &ContactController{contactService: contactService}
}

// CreateContact creates a new contact
// POST /api/v1/contacts
func (c *ContactController) Create(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.CreateContactRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	contact, err := c.contactService.CreateContact(r.Context(), claims.OrgID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Contact created", contact)
}

// GetContact retrieves a contact by UUID
// GET /api/v1/contacts/:uuid
func (c *ContactController) Get(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	contactUUID := r.Get("uuid").String()
	if contactUUID == "" {
		response.BadRequest(r, "Contact UUID required")
		return
	}

	contact, err := c.contactService.GetContact(r.Context(), claims.OrgID, contactUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, contact)
}

// ListContacts retrieves contacts with pagination
// GET /api/v1/contacts
func (c *ContactController) List(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.ContactSearchRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	result, err := c.contactService.ListContacts(r.Context(), claims.OrgID, &req)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, result)
}

// UpdateContact updates a contact
// PUT /api/v1/contacts/:uuid
func (c *ContactController) Update(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	contactUUID := r.Get("uuid").String()
	if contactUUID == "" {
		response.BadRequest(r, "Contact UUID required")
		return
	}

	var req model.UpdateContactRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	contact, err := c.contactService.UpdateContact(r.Context(), claims.OrgID, contactUUID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Contact updated", contact)
}

// DeleteContact deletes a contact
// DELETE /api/v1/contacts/:uuid
func (c *ContactController) Delete(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	contactUUID := r.Get("uuid").String()
	if contactUUID == "" {
		response.BadRequest(r, "Contact UUID required")
		return
	}

	err := c.contactService.DeleteContact(r.Context(), claims.OrgID, contactUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Contact deleted", nil)
}

// ImportContacts bulk imports contacts
// POST /api/v1/contacts/import
func (c *ContactController) Import(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.ImportContactsRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	if len(req.Contacts) > 1000 {
		response.BadRequest(r, "Cannot import more than 1000 contacts at once")
		return
	}

	result, err := c.contactService.ImportContacts(r.Context(), claims.OrgID, &req)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Import completed", result)
}

// Unsubscribe marks a contact as unsubscribed
// POST /api/v1/contacts/unsubscribe
func (c *ContactController) Unsubscribe(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req struct {
		Email string `json:"email" v:"required|email"`
	}
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	err := c.contactService.Unsubscribe(r.Context(), claims.OrgID, req.Email)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Contact unsubscribed", nil)
}

// ExportContacts exports contacts as JSON
// POST /api/v1/contacts/export
func (c *ContactController) Export(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.ExportContactsRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	contacts, err := c.contactService.ExportContacts(r.Context(), claims.OrgID, &req)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, contacts)
}
