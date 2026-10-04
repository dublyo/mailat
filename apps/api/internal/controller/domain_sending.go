package controller

import (
	"errors"
	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/dublyo/mailat/api/pkg/response"
	"github.com/gogf/gf/v2/net/ghttp"
	"net/http"
)

func (c *DomainController) SendingStatus(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}
	result, err := c.domainService.GetSendingStatus(r.Context(), claims.OrgID, r.Get("uuid").String())
	if err != nil {
		response.NotFound(r, "domain not found")
		return
	}
	response.Success(r, result)
}
func (c *DomainController) SetupSending(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}
	domain, err := c.domainService.GetDomain(r.Context(), claims.OrgID, r.Get("uuid").String())
	if err != nil {
		response.NotFound(r, "domain not found")
		return
	}
	result, err := c.domainService.EnsureDomainSendingResources(r.Context(), claims.OrgID, domain.ID)
	if err != nil {
		var validation *provider.MailValidationError
		if errors.As(err, &validation) {
			response.BadRequest(r, validation.Error())
		} else {
			r.Response.Status = http.StatusServiceUnavailable
			response.Error(r, http.StatusServiceUnavailable, "Sending setup is temporarily unavailable; check configuration and retry")
		}
		return
	}
	response.Success(r, result)
}
