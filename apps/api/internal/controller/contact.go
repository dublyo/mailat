package controller

import (
	"errors"

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

// contactError maps typed contact errors to their HTTP status and code; the
// code is returned as data.error so clients can branch without parsing text.
func contactError(r *ghttp.Request, err error, fallback func(*ghttp.Request, string)) {
	var ce *service.ContactError
	if errors.As(err, &ce) {
		r.Response.Status = ce.Status
		r.Response.WriteJsonExit(response.Response{Code: ce.Status, Message: ce.Message, Data: map[string]string{"error": ce.Code}})
		return
	}
	fallback(r, err.Error())
}

// CreateContact creates a new contact. The email is lowercased; listIds from
// another organization are rejected (400 unknown_list) and suppressed
// addresses are refused (409 suppressed).
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
		contactError(r, err, response.BadRequest)
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

// ListContacts retrieves contacts with pagination. query matches email and
// names literally (case-insensitive substring); sortBy is one of createdAt,
// updatedAt, email, firstName, lastName.
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

// UpdateContact updates a contact. status accepts only "active" or
// "unsubscribed": unsubscribing is always allowed, reactivation only for a
// bounced, unsuppressed address (409 reactivation_blocked otherwise). Other
// statuses are system-managed (400). 409 duplicate_email if the new email is taken.
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

	actor := service.ContactActor{UserID: claims.UserID, IP: middleware.ClientIP(r), UA: r.UserAgent()}
	contact, err := c.contactService.UpdateContact(r.Context(), claims.OrgID, actor, contactUUID, &req)
	if err != nil {
		contactError(r, err, response.BadRequest)
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

// ImportContacts bulk imports up to 10,000 contacts. Rows are imported
// independently: invalid rows are reported in errors (1-based row numbers)
// while the rest commit. Suppressed or non-active addresses are not added to
// lists and are counted in suppressed. Foreign listIds return 400 unknown_list.
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

	result, err := c.contactService.ImportContacts(r.Context(), claims.OrgID, &req)
	if err != nil {
		contactError(r, err, response.InternalError)
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
