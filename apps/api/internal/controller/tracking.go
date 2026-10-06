package controller

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/service"
)

// Transparent 1x1 GIF
var transparentGIF = func() []byte {
	data, _ := base64.StdEncoding.DecodeString("R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7")
	return data
}()

// trackingTimeout bounds the synchronous event write; it is detached from the
// request so a reader closing the connection does not abort the write.
const trackingTimeout = 3 * time.Second

type TrackingController struct {
	trackingService *service.TrackingService
}

func NewTrackingController(trackingService *service.TrackingService) *TrackingController {
	return &TrackingController{trackingService: trackingService}
}

func trackingContext(r *ghttp.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), trackingTimeout)
}

// TrackOpen records a campaign open and always returns the 1x1 GIF.
// GET /api/v1/tracking/open/:token.gif
func (c *TrackingController) TrackOpen(r *ghttp.Request) {
	token := strings.TrimSuffix(r.Get("token").String(), ".gif")
	ctx, cancel := trackingContext(r)
	if err := c.trackingService.ProcessOpenEvent(ctx, token, middleware.ClientIP(r), r.Header.Get("User-Agent")); err != nil {
		g.Log().Warningf(ctx, "tracking open: %v", err)
	}
	cancel()

	r.Response.Header().Set("Content-Type", "image/gif")
	r.Response.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, proxy-revalidate")
	r.Response.Header().Set("Pragma", "no-cache")
	r.Response.Header().Set("Expires", "0")
	r.Response.Write(transparentGIF)
}

// TrackClick records a campaign click and redirects (302) to the signed
// http(s) target, or to the web app when the token is invalid.
// GET /api/v1/tracking/click/:token
func (c *TrackingController) TrackClick(r *ghttp.Request) {
	ctx, cancel := trackingContext(r)
	target, err := c.trackingService.ProcessClickEvent(ctx, r.Get("token").String(), middleware.ClientIP(r), r.Header.Get("User-Agent"))
	if err != nil {
		g.Log().Warningf(ctx, "tracking click: %v", err)
	}
	cancel()
	if target == "" {
		target = c.trackingService.HomeURL()
	}
	r.Response.Header().Set("Cache-Control", "no-store")
	r.Response.RedirectTo(target, http.StatusFound)
}
