package controller

import (
	"encoding/base64"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/service"
)

// Transparent 1x1 GIF
var transparentGIF = func() []byte {
	data, _ := base64.StdEncoding.DecodeString("R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7")
	return data
}()

type TrackingController struct {
	trackingService *service.TrackingService
}

func NewTrackingController(trackingService *service.TrackingService) *TrackingController {
	return &TrackingController{trackingService: trackingService}
}

// TrackOpen handles email open tracking requests
// GET /api/v1/tracking/open/:token.gif
func (c *TrackingController) TrackOpen(r *ghttp.Request) {
	token := r.Get("token").String()
	// Remove .gif extension if present
	if len(token) > 4 && token[len(token)-4:] == ".gif" {
		token = token[:len(token)-4]
	}

	// Get client info
	ipAddress := r.GetClientIp()
	userAgent := r.Header.Get("User-Agent")

	// Process the open event (fire and forget)
	go c.trackingService.ProcessOpenEvent(r.Context(), token, ipAddress, userAgent)

	// Return transparent 1x1 GIF
	r.Response.Header().Set("Content-Type", "image/gif")
	r.Response.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, proxy-revalidate")
	r.Response.Header().Set("Pragma", "no-cache")
	r.Response.Header().Set("Expires", "0")
	r.Response.Write(transparentGIF)
}

// TrackClick handles link click tracking requests
// GET /api/v1/tracking/click/:token
func (c *TrackingController) TrackClick(r *ghttp.Request) {
	token := r.Get("token").String()

	// Get client info
	ipAddress := r.GetClientIp()
	userAgent := r.Header.Get("User-Agent")

	// Process the click event and get the target URL
	targetURL, err := c.trackingService.ProcessClickEvent(r.Context(), token, ipAddress, userAgent)
	if err != nil || targetURL == "" {
		// Fallback to a generic page if decoding fails
		r.Response.RedirectTo("/")
		return
	}

	// Redirect to the original URL
	r.Response.RedirectTo(targetURL)
}
