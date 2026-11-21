package controller

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/service"
)

// Send limit refusals reach clients as 429 with the human message, from every
// send controller's error writer, and never as a retryable 503.
func TestSendLimitRefusalsAre429(t *testing.T) {
	s := ghttp.GetServer(fmt.Sprintf("send-limit-%d", time.Now().UnixNano()))
	s.SetAddr("127.0.0.1:0")
	s.SetDumpRouterMap(false)
	s.SetAccessLogEnabled(false)
	waiting := fmt.Errorf("send: %w", &service.SendLimitError{Message: "Sending limit reached: 300 recipients per hour. Try again in 12 minutes.", RetryAfter: 11*time.Minute + 30*time.Second})
	oversized := &service.SendLimitError{Message: "Sending limit reached: 300 recipients per hour. This send has 350 recipients; send it to fewer people at a time."}
	s.BindHandler("/compose", func(r *ghttp.Request) { writeComposeError(r, waiting) })
	s.BindHandler("/emails", func(r *ghttp.Request) { writeTransactionalError(r, oversized) })
	s.BindHandler("/campaign", func(r *ghttp.Request) { writeCampaignError(r, waiting) })
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	for _, tc := range []struct{ path, retryAfter, message string }{
		{"/compose", "690", "300 recipients per hour. Try again in 12 minutes."},
		{"/emails", "", "This send has 350 recipients"},
		{"/campaign", "690", "Try again in 12 minutes."},
	} {
		res, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", s.GetListenedPort(), tc.path))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("Retry-After") != tc.retryAfter || !strings.Contains(string(body), tc.message) {
			t.Fatalf("%s: %d Retry-After=%q %s", tc.path, res.StatusCode, res.Header.Get("Retry-After"), body)
		}
	}
}
