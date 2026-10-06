package controller_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/controller"
	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/internal/testutil"
)

// Mailbox providers send RFC 8058 POSTs from a few shared servers: valid
// tokens must never hit the per-IP limit, invalid ones still do.
func TestOneClickUnsubscribeSkipsIPLimitForSignedTokensHTTP(t *testing.T) {
	db := testutil.Database(t)
	cfg := &config.Config{JWTSecret: "compliance-http-fixture-not-a-live-secret", EmailProvider: "ses", APIUrl: "http://api.test", AppDomain: "example.test"}
	oldDB, oldCfg := database.DB, config.Cfg
	database.DB, config.Cfg = db, cfg
	t.Cleanup(func() { database.DB, config.Cfg = oldDB, oldCfg })
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'One','one',now())`); err != nil {
		t.Fatal(err)
	}
	const n = 61 // one over RulePublicComplianceIP
	if _, err := db.Exec(`INSERT INTO contacts(id,org_id,email,status,updated_at) SELECT g,1,'c'||g||'@example.test','active',now() FROM generate_series(1,$1) g`, n); err != nil {
		t.Fatal(err)
	}
	compliance := service.NewComplianceService(db, cfg)
	ctrl := controller.NewComplianceController(compliance, service.NewRateLimiter(db, cfg))
	s := ghttp.GetServer(fmt.Sprintf("compliance-http-%d", time.Now().UnixNano()))
	s.SetAddr("127.0.0.1:0")
	s.SetDumpRouterMap(false)
	s.SetAccessLogEnabled(false)
	s.Group("/api/v1", func(g *ghttp.RouterGroup) {
		g.POST("/unsubscribe/:token", ctrl.OneClickUnsubscribe)
	})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Shutdown() })
	base := fmt.Sprintf("http://127.0.0.1:%d", s.GetListenedPort())
	post := func(token string) int {
		t.Helper()
		res, err := http.Post(base+"/api/v1/unsubscribe/"+token, "application/x-www-form-urlencoded", strings.NewReader("List-Unsubscribe=One-Click"))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	for id := int64(1); id <= n; id++ {
		header, _ := compliance.GenerateListUnsubscribeHeader(id, 1, id)
		token := strings.SplitN(strings.TrimPrefix(header, "<http://api.test/api/v1/unsubscribe/"), ">", 2)[0]
		if status := post(token); status != 200 {
			t.Fatalf("one-click %d from a shared IP: status %d", id, status)
		}
	}
	var unsubscribed int
	db.QueryRow(`SELECT count(*) FROM contacts WHERE status='unsubscribed'`).Scan(&unsubscribed)
	if unsubscribed != n {
		t.Fatalf("unsubscribed %d of %d", unsubscribed, n)
	}

	// Forged tokens are still limited per IP.
	got429 := false
	for i := 0; i < 61 && !got429; i++ {
		switch status := post(fmt.Sprintf("forged-%d", i)); status {
		case 429:
			got429 = true
		case 400:
		default:
			t.Fatalf("forged token: status %d", status)
		}
	}
	if !got429 {
		t.Fatal("invalid tokens were never rate limited")
	}
}
