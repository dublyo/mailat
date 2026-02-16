package controller

import (
	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

type SettingsController struct {
	settingsService *service.SettingsService
}

func NewSettingsController(settingsService *service.SettingsService) *SettingsController {
	return &SettingsController{settingsService: settingsService}
}

// GetSettings returns the current user's settings
// GET /api/v1/settings
func (c *SettingsController) GetSettings(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	// Settings require user authentication (not API key)
	if claims.UserID == 0 {
		response.BadRequest(r, "Settings endpoint requires user authentication, not API key")
		return
	}

	settings, err := c.settingsService.GetSettings(r.Context(), claims.UserID)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.Success(r, settings)
}

// UpdateSettings updates the current user's settings
// PUT /api/v1/settings
func (c *SettingsController) UpdateSettings(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	// Settings require user authentication (not API key)
	if claims.UserID == 0 {
		response.BadRequest(r, "Settings endpoint requires user authentication, not API key")
		return
	}

	var req service.UpdateSettingsRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	settings, err := c.settingsService.UpdateSettings(r.Context(), claims.UserID, &req)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Settings updated successfully", settings)
}
