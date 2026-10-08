package controller

import (
	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/pkg/response"
)

// Readiness is the caller's API sending checklist for one domain.
// GET /api/v1/domains/:uuid/readiness
func (c *DomainController) Readiness(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}
	result, err := c.domainService.GetDomainReadiness(r.Context(), claims.OrgID, claims.UserID, r.Get("uuid").String())
	if err != nil {
		domainReadError(r, err)
		return
	}
	r.Response.Header().Set("Cache-Control", "no-store")
	response.Success(r, result)
}
