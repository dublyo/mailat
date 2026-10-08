package controller

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/gogf/gf/v2/net/ghttp"
)

// Invalid keys must stop at the HTTP boundary before any DB/provider operation.
func TestTransactionalHTTPRequiresStableRequestKeys(t *testing.T) {
	c := NewTransactionalController(nil)
	s := ghttp.GetServer(fmt.Sprintf("send-key-%d", time.Now().UnixNano()))
	s.SetAddr("127.0.0.1:0")
	s.SetDumpRouterMap(false)
	s.SetAccessLogEnabled(false)
	s.Group("/", func(g *ghttp.RouterGroup) {
		g.Middleware(func(r *ghttp.Request) {
			r.SetCtx(context.WithValue(r.Context(), middleware.ClaimsContextKey, &model.JWTClaims{UserID: 1, OrgID: 1}))
			r.Middleware.Next()
		})
		g.POST("/emails", c.SendEmail)
		g.POST("/emails/batch", c.BatchSendEmail)
	})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	for _, tc := range []struct{ name, path, key, body, contains string }{
		{"missing", "/emails", "", `{"from":"from@test.test","to":["to@test.test"],"subject":"Test","text":"body"}`, "Idempotency-Key"},
		{"short", "/emails", "short", `{"from":"from@test.test","to":["to@test.test"],"subject":"Test","text":"body"}`, "Idempotency-Key"},
		{"mismatch", "/emails", "header-key-123", `{"from":"from@test.test","to":["to@test.test"],"subject":"Test","text":"body","idempotencyKey":"body-key-123"}`, "must match"},
		{"batch-body-is-not-header", "/emails/batch", "", `{"idempotencyKey":"batch-body-key","emails":[{"from":"from@test.test","to":["to@test.test"],"subject":"Test","text":"body","idempotencyKey":"item-key-123"}]}`, "header"},
		{"bcc-only-reaches-key-check", "/emails", "", `{"from":"from@test.test","bcc":["hidden@test.test"],"subject":"Test","text":"body"}`, "Idempotency-Key"},
		// A display name is valid and must reach the next check, not fail validation.
		{"display-name-from-reaches-key-check", "/emails", "", `{"from":"Support Team <from@test.test>","to":["to@test.test"],"subject":"Test","text":"body"}`, "Idempotency-Key"},
		{"plus-from-reaches-key-check", "/emails", "", `{"from":"from+tag@test.test","to":["to@test.test"],"subject":"Test","text":"body"}`, "Idempotency-Key"},
		{"empty-from-fails-validation", "/emails", "valid-key-123", `{"from":"","to":["to@test.test"],"subject":"Test","text":"body"}`, "rom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d%s", s.GetListenedPort(), tc.path), strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.key != "" {
				req.Header.Set("Idempotency-Key", tc.key)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			body, _ := io.ReadAll(res.Body)
			if res.StatusCode != 400 || !strings.Contains(string(body), tc.contains) {
				t.Fatalf("%d %s", res.StatusCode, body)
			}
		})
	}
}
