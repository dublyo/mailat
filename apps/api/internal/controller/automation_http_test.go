package controller_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/controller"
	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/internal/testutil"
)

func TestAutomationHTTPErrorsCarryValidationData(t *testing.T) {
	db := testutil.Database(t)
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'One','one',now());
		INSERT INTO users(id,org_id,email,password_hash,updated_at) VALUES(1,1,'owner@one.test','x',now())`); err != nil {
		t.Fatal(err)
	}
	ctrl := controller.NewAutomationController(service.NewAutomationService(db, &config.Config{}))
	s := ghttp.GetServer(fmt.Sprintf("automation-http-%d", time.Now().UnixNano()))
	s.SetAddr("127.0.0.1:0")
	s.SetDumpRouterMap(false)
	s.SetAccessLogEnabled(false)
	s.Group("/api/v1", func(g *ghttp.RouterGroup) {
		g.Middleware(func(r *ghttp.Request) {
			r.SetCtx(context.WithValue(r.Context(), middleware.ClaimsContextKey, &model.JWTClaims{UserID: 1, OrgID: 1}))
			r.Middleware.Next()
		})
		g.POST("/automations", ctrl.Create)
		g.GET("/automations", ctrl.List)
		g.GET("/automations/:uuid", ctrl.Get)
		g.POST("/automations/:uuid/validate", ctrl.Validate)
		g.POST("/automations/:uuid/activate", ctrl.Activate)
		g.POST("/automations/:uuid/archive", ctrl.Archive)
		g.DELETE("/automations/:uuid", ctrl.Delete)
	})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Shutdown() })
	base := fmt.Sprintf("http://127.0.0.1:%d/api/v1", s.GetListenedPort())
	type envelope struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	call := func(method, path, body string, wantStatus int) envelope {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != wantStatus {
			t.Fatalf("%s %s: status %d, want %d: %s", method, path, res.StatusCode, wantStatus, raw)
		}
		var e envelope
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	errorsOf := func(e envelope) []model.AutomationValidationError {
		t.Helper()
		var d struct {
			Errors []model.AutomationValidationError `json:"errors"`
		}
		if err := json.Unmarshal(e.Data, &d); err != nil || len(d.Errors) == 0 {
			t.Fatalf("no data.errors in %s (%v)", e.Data, err)
		}
		return d.Errors
	}

	bad := call("POST", "/automations", `{"name":"Bad","workflow":{"nodes":[{"id":"x y","type":"workflow","data":{"type":"trigger"}}],"edges":[]}}`, 400)
	if bad.Message != "Automation is not valid" || errorsOf(bad)[0].Field != "id" {
		t.Fatalf("structural error: %+v", bad)
	}
	created := call("POST", "/automations", `{"name":"Draft"}`, 200)
	var a model.Automation
	if err := json.Unmarshal(created.Data, &a); err != nil || a.Status != "draft" {
		t.Fatal(err, string(created.Data))
	}

	call("GET", "/automations?status=running", "", 400)
	call("GET", "/automations?page=0&pageSize=1000", "", 200)
	call("GET", "/automations/e0000000-0000-4000-8000-000000000000", "", 404)
	call("GET", "/automations/not-a-uuid", "", 404)

	v := call("POST", "/automations/"+a.UUID+"/validate", "", 200)
	var result model.AutomationValidationResult
	if err := json.Unmarshal(v.Data, &result); err != nil || result.Valid || len(result.Errors) == 0 {
		t.Fatalf("validate: %s", v.Data)
	}
	errorsOf(call("POST", "/automations/"+a.UUID+"/activate", "", 400)) // an empty body publishes the draft
	// The lone trigger has no first step, so activation is refused with errors.
	if errs := errorsOf(call("POST", "/automations/"+a.UUID+"/activate", `{"publishDraft":false}`, 400)); errs[0].NodeID == "" && errs[0].Field == "" {
		t.Fatalf("activate errors: %+v", errs)
	}
	archived := call("POST", "/automations/"+a.UUID+"/archive", "", 200)
	if !strings.Contains(string(archived.Data), `"cancelledEnrollments":0`) {
		t.Fatalf("archive: %s", archived.Data)
	}
	call("POST", "/automations/"+a.UUID+"/archive", "", 400)
	call("DELETE", "/automations/"+a.UUID, "", 200)
	call("DELETE", "/automations/"+a.UUID, "", 404)
}
