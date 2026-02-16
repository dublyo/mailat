package controller

import (
	"encoding/json"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

type AuthController struct {
	authService *service.AuthService
}

func NewAuthController(authService *service.AuthService) *AuthController {
	return &AuthController{authService: authService}
}

// RegisterStatus checks if registration is open
// GET /api/v1/auth/register-status
func (c *AuthController) RegisterStatus(r *ghttp.Request) {
	open, err := c.authService.IsRegistrationOpen(r.Context())
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}
	response.Success(r, map[string]bool{"open": open})
}

// Register creates a new user and organization
// POST /api/v1/auth/register
func (c *AuthController) Register(r *ghttp.Request) {
	var req model.RegisterRequest
	bodyBytes := r.GetBody()
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		response.BadRequest(r, "Invalid JSON: "+err.Error())
		return
	}

	// Manual validation
	if req.Email == "" {
		response.BadRequest(r, "email is required")
		return
	}
	if req.Password == "" {
		response.BadRequest(r, "password is required")
		return
	}
	if req.Name == "" {
		response.BadRequest(r, "name is required")
		return
	}

	result, err := c.authService.Register(r.Context(), &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Registration successful", result)
}

// Login authenticates a user
// POST /api/v1/auth/login
func (c *AuthController) Login(r *ghttp.Request) {
	var req model.LoginRequest
	bodyBytes := r.GetBody()
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		response.BadRequest(r, "Invalid JSON: "+err.Error())
		return
	}

	// Manual validation
	if req.Email == "" {
		response.BadRequest(r, "email is required")
		return
	}
	if req.Password == "" {
		response.BadRequest(r, "password is required")
		return
	}

	result, err := c.authService.Login(r.Context(), &req)
	if err != nil {
		response.Unauthorized(r, err.Error())
		return
	}

	response.Success(r, result)
}

// Me returns the current user's profile
// GET /api/v1/auth/me
func (c *AuthController) Me(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	user, err := c.authService.GetUserByID(r.Context(), claims.UserID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.Success(r, user)
}

// CreateAPIKey generates a new API key
// POST /api/v1/api-keys
func (c *AuthController) CreateAPIKey(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.CreateApiKeyRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	result, err := c.authService.CreateAPIKey(r.Context(), claims.OrgID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "API key created. Save the key now - it won't be shown again.", result)
}

// ListAPIKeys returns all API keys for the organization
// GET /api/v1/api-keys
func (c *AuthController) ListAPIKeys(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	keys, err := c.authService.ListAPIKeys(r.Context(), claims.OrgID)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, keys)
}

// DeleteAPIKey revokes an API key
// DELETE /api/v1/api-keys/:uuid
func (c *AuthController) DeleteAPIKey(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	keyUUID := r.Get("uuid").String()
	if keyUUID == "" {
		response.BadRequest(r, "API key UUID required")
		return
	}

	err := c.authService.DeleteAPIKey(r.Context(), claims.OrgID, keyUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "API key revoked", nil)
}
