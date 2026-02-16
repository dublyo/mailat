package controller

import (
	"encoding/json"
	"fmt"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

type IdentityController struct {
	identityService *service.IdentityService
}

func NewIdentityController(identityService *service.IdentityService) *IdentityController {
	return &IdentityController{identityService: identityService}
}

// Create adds a new identity/mailbox
// POST /api/v1/identities
func (c *IdentityController) Create(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	// Manual JSON parsing to debug binding issues
	var req model.CreateIdentityRequest
	bodyBytes := r.GetBody()
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		fmt.Printf("JSON unmarshal error: %v, body: %s\n", err, string(bodyBytes))
		response.BadRequest(r, "Invalid JSON: "+err.Error())
		return
	}
	fmt.Printf("Parsed identity request: email=%s, domainId=%s, displayName=%s, password_len=%d\n",
		req.Email, req.DomainId, req.DisplayName, len(req.Password))

	// Manual validation
	if req.DomainId == "" {
		response.BadRequest(r, "domainId is required")
		return
	}
	if req.Email == "" {
		response.BadRequest(r, "email is required")
		return
	}
	if req.DisplayName == "" {
		response.BadRequest(r, "displayName is required")
		return
	}
	if len(req.Password) < 8 {
		response.BadRequest(r, "password must be at least 8 characters")
		return
	}

	identity, err := c.identityService.CreateIdentity(r.Context(), claims.UserID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Identity created", identity)
}

// List returns all identities for the user
// GET /api/v1/identities
func (c *IdentityController) List(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	identities, err := c.identityService.ListIdentities(r.Context(), claims.UserID)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, identities)
}

// Get returns a single identity
// GET /api/v1/identities/:uuid
func (c *IdentityController) Get(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	identityUUID := r.Get("uuid").String()
	if identityUUID == "" {
		response.BadRequest(r, "Identity UUID required")
		return
	}

	identity, err := c.identityService.GetIdentity(r.Context(), claims.UserID, identityUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, identity)
}

// UpdatePassword changes the identity password
// PUT /api/v1/identities/:uuid/password
func (c *IdentityController) UpdatePassword(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	identityUUID := r.Get("uuid").String()
	if identityUUID == "" {
		response.BadRequest(r, "Identity UUID required")
		return
	}

	var req struct {
		Password string `json:"password" v:"required|min-length:8"`
	}
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	err := c.identityService.UpdateIdentityPassword(r.Context(), claims.UserID, identityUUID, req.Password)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Password updated", nil)
}

// Delete removes an identity
// DELETE /api/v1/identities/:uuid
func (c *IdentityController) Delete(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	identityUUID := r.Get("uuid").String()
	if identityUUID == "" {
		response.BadRequest(r, "Identity UUID required")
		return
	}

	err := c.identityService.DeleteIdentity(r.Context(), claims.UserID, identityUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Identity deleted", nil)
}
