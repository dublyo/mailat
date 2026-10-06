package controller_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/controller"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/internal/testutil"
)

// The public verify route answers every failure identically and is limited per IP.
func TestForwardVerifyIsGenericAndThrottledHTTP(t *testing.T) {
	db := testutil.Database(t)
	cfg := &config.Config{JWTSecret: "forward-verify-http-fixture-not-a-live-secret", EmailProvider: "ses"}
	ctrl := controller.NewEmailRulesController(nil, service.NewAutoReplyService(db, cfg), service.NewRateLimiter(db, cfg))
	s := ghttp.GetServer(fmt.Sprintf("forward-verify-http-%d", time.Now().UnixNano()))
	s.SetAddr("127.0.0.1:0")
	s.SetDumpRouterMap(false)
	s.SetAccessLogEnabled(false)
	s.Group("/api/v1", func(g *ghttp.RouterGroup) {
		g.POST("/forwards/verify", ctrl.VerifyEmailForward)
	})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Shutdown() })
	url := fmt.Sprintf("http://127.0.0.1:%d/api/v1/forwards/verify", s.GetListenedPort())
	post := func(body string) (int, string) {
		t.Helper()
		res, err := http.Post(url, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	status, first := post(`{"uuid":"00000000-0000-0000-0000-000000000000","token":"abc"}`)
	if status != 400 || !strings.Contains(first, "invalid or expired verification link") {
		t.Fatal(status, first)
	}
	for _, body := range []string{`{"uuid":"not-a-uuid","token":"abc"}`, `{"uuid":"00000000-0000-0000-0000-000000000000"}`, `not json`} {
		if status, got := post(body); status != 400 || got != first {
			t.Fatalf("%s: %d %s", body, status, got)
		}
	}
	for i := 4; i < service.RuleForwardVerifyIP.Limit; i++ {
		post(`{}`)
	}
	if status, _ := post(`{}`); status != 429 {
		t.Fatal("not throttled", status)
	}
}
