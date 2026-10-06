package controller_test

import (
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/controller"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/internal/testutil"
)

// Opens are recorded before the GIF is returned (no fire-and-forget), and
// clicks redirect only to signed http(s) targets.
func TestTrackingEndpointsHTTP(t *testing.T) {
	db := testutil.Database(t)
	const secret = "tracking-http-fixture-not-a-live-secret"
	cfg := &config.Config{JWTSecret: secret, WebUrl: "https://app.test"}
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'One','one',now());
		INSERT INTO lists(id,org_id,name,updated_at) VALUES(1,1,'L',now());
		INSERT INTO campaigns(id,org_id,name,subject,from_name,from_email,list_id,status,updated_at) VALUES(1,1,'C','S','F','f@one.test',1,'sending',now());
		INSERT INTO campaign_recipients(id,campaign_id,org_id,email,status) VALUES(1,1,1,'a@x.test','sent')`); err != nil {
		t.Fatal(err)
	}
	ctrl := controller.NewTrackingController(service.NewTrackingService(db, cfg))
	s := ghttp.GetServer(fmt.Sprintf("tracking-http-%d", time.Now().UnixNano()))
	s.SetAddr("127.0.0.1:0")
	s.SetDumpRouterMap(false)
	s.SetAccessLogEnabled(false)
	s.Group("/api/v1", func(g *ghttp.RouterGroup) {
		g.GET("/tracking/open/:token", ctrl.TrackOpen)
		g.GET("/tracking/click/:token", ctrl.TrackClick)
	})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Shutdown() })
	base := fmt.Sprintf("http://127.0.0.1:%d/api/v1/tracking", s.GetListenedPort())
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path string) *http.Response {
		t.Helper()
		res, err := client.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return res
	}

	for _, path := range []string{"/open/" + service.OpenToken(secret, 1, 1, 1) + ".gif", "/open/garbage.gif"} {
		res := get(path)
		if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/gif" || res.Header.Get("Cache-Control") == "" {
			t.Fatalf("%s: %d %v", path, res.StatusCode, res.Header)
		}
	}
	var opens int
	db.QueryRow(`SELECT open_count FROM campaigns WHERE id=1`).Scan(&opens)
	if opens != 1 {
		t.Fatalf("open not recorded synchronously: %d", opens)
	}

	for path, want := range map[string]string{
		"/click/" + service.ClickToken(secret, 1, 1, 1, 0, "https://example.test/a?b=1"): "https://example.test/a?b=1",
		"/click/" + service.ClickToken(secret, 1, 1, 1, 0, "javascript:alert(1)"):        "https://app.test",
		"/click/" + service.ClickToken("forged", 1, 1, 1, 0, "https://evil.test"):        "https://app.test",
	} {
		res := get(path)
		if res.StatusCode != http.StatusFound || res.Header.Get("Location") != want {
			t.Fatalf("%s: %d -> %q, want %q", path, res.StatusCode, res.Header.Get("Location"), want)
		}
	}
	var clicks int
	db.QueryRow(`SELECT click_count FROM campaign_recipients WHERE id=1`).Scan(&clicks)
	if clicks != 1 {
		t.Fatalf("clicks=%d", clicks)
	}
}
