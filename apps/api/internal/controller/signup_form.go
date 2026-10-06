package controller

import (
	"encoding/json"
	"errors"
	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
	"github.com/gogf/gf/v2/net/ghttp"
	"io"
	"net"
	"net/http"
	"strings"
)

type SignupFormController struct{ service *service.SignupFormService }

func NewSignupFormController(s *service.SignupFormService) *SignupFormController {
	return &SignupFormController{s}
}
func signupFailure(r *ghttp.Request, err error) {
	var e *service.SignupError
	if errors.As(err, &e) {
		if e.Status == 429 {
			r.Response.Header().Set("Retry-After", "3600")
		}
		r.Response.Status = e.Status
		response.Error(r, e.Status, e.Message)
		return
	}
	response.InternalError(r, "Unable to process signup forms right now")
}
func signupBody(r *ghttp.Request, target any) bool {
	r.Body = http.MaxBytesReader(r.Response.Writer, r.Body, 16<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		response.BadRequest(r, "Invalid or oversized form data")
		return false
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		response.BadRequest(r, "Send one JSON object")
		return false
	}
	return true
}
func signupIP(r *ghttp.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	trusted := func(raw string) bool {
		ip := net.ParseIP(raw)
		if ip == nil || config.Cfg == nil {
			return false
		}
		for _, part := range strings.Split(config.Cfg.TrustedProxyCIDRs, ",") {
			_, network, e := net.ParseCIDR(strings.TrimSpace(part))
			if e == nil && network.Contains(ip) {
				return true
			}
		}
		return false
	}
	if !trusted(host) {
		return host
	}
	// Walk from the immediate peer toward the client and stop at the first
	// untrusted hop. A visitor cannot spoof rate limits with a prepended address.
	chain := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(chain) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(chain[i])
		if net.ParseIP(candidate) == nil {
			continue
		}
		host = candidate
		if !trusted(host) {
			break
		}
	}
	return host
}

func (c *SignupFormController) List(r *ghttp.Request) {
	a := middleware.GetClaims(r)
	if a == nil {
		response.Unauthorized(r, "Authorization required")
		return
	}
	x, e := c.service.List(r.Context(), a.OrgID, a.UserID)
	if e != nil {
		signupFailure(r, e)
		return
	}
	response.Success(r, x)
}
func (c *SignupFormController) Get(r *ghttp.Request) {
	a := middleware.GetClaims(r)
	if a == nil {
		response.Unauthorized(r, "Authorization required")
		return
	}
	x, e := c.service.Get(r.Context(), a.OrgID, a.UserID, r.Get("uuid").String())
	if e != nil {
		signupFailure(r, e)
		return
	}
	response.Success(r, x)
}
func (c *SignupFormController) Create(r *ghttp.Request) { c.save(r, "") }
func (c *SignupFormController) Update(r *ghttp.Request) { c.save(r, r.Get("uuid").String()) }
func (c *SignupFormController) save(r *ghttp.Request, id string) {
	a := middleware.GetClaims(r)
	if a == nil {
		response.Unauthorized(r, "Authorization required")
		return
	}
	var req model.SaveSignupFormRequest
	if !signupBody(r, &req) {
		return
	}
	x, e := c.service.Save(r.Context(), a.OrgID, a.UserID, id, &req)
	if e != nil {
		signupFailure(r, e)
		return
	}
	response.Success(r, x)
}
func (c *SignupFormController) Delete(r *ghttp.Request) {
	a := middleware.GetClaims(r)
	if a == nil {
		response.Unauthorized(r, "Authorization required")
		return
	}
	if e := c.service.Delete(r.Context(), a.OrgID, a.UserID, r.Get("uuid").String()); e != nil {
		signupFailure(r, e)
		return
	}
	response.Success(r, nil)
}
func (c *SignupFormController) Entries(r *ghttp.Request) {
	a := middleware.GetClaims(r)
	if a == nil {
		response.Unauthorized(r, "Authorization required")
		return
	}
	x, e := c.service.Entries(r.Context(), a.OrgID, a.UserID, r.Get("uuid").String(), r.Get("page", 1).Int())
	if e != nil {
		signupFailure(r, e)
		return
	}
	response.Success(r, x)
}
func (c *SignupFormController) Public(r *ghttp.Request) {
	r.Response.Header().Set("Cache-Control", "no-store")
	x, e := c.service.Public(r.Context(), r.Get("uuid").String())
	if e != nil {
		signupFailure(r, e)
		return
	}
	response.Success(r, x)
}
func (c *SignupFormController) Submit(r *ghttp.Request) {
	r.Response.Header().Set("Cache-Control", "no-store")
	var req model.SubmitSignupRequest
	if !signupBody(r, &req) {
		return
	}
	x, e := c.service.Submit(r.Context(), r.Get("uuid").String(), signupIP(r), &req)
	if e != nil {
		signupFailure(r, e)
		return
	}
	response.Success(r, x)
}
func (c *SignupFormController) Confirm(r *ghttp.Request) {
	r.Response.Header().Set("Cache-Control", "no-store")
	var req model.ConfirmSignupRequest
	if !signupBody(r, &req) {
		return
	}
	x, e := c.service.Confirm(r.Context(), req.Token, signupIP(r))
	if e != nil {
		signupFailure(r, e)
		return
	}
	response.Success(r, x)
}
