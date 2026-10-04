package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/internal/testutil"
	"github.com/gogf/gf/v2/net/ghttp"
	"golang.org/x/crypto/bcrypt"
)

// Exercise the real router, credential middleware and SES ingestion against a
// migrated disposable schema. Only the external S3 transport is substituted.
func TestMailboxAutomationHTTP(t *testing.T) {
	db := testutil.Database(t)
	cfg := &config.Config{JWTSecret: "local-automation-fixture-only", JWTExpiresIn: "24h", EmailProvider: "ses", AWSRegion: "us-east-1", AWSAccessKeyID: "fixture", AWSSecretAccessKey: "fixture", DisableAppLimits: true}
	previousDB, previousCfg := database.DB, config.Cfg
	database.DB, config.Cfg = db, cfg
	t.Cleanup(func() { database.DB, config.Cfg = previousDB, previousCfg })
	password, _ := bcrypt.GenerateFromPassword([]byte("mailat-local-test"), bcrypt.MinCost)
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Local QA','local-qa',now()),(2,'Other','other',now());
`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,org_id,email,password_hash,name,role,updated_at) VALUES(1,1,'qa@example.test',$1,'Local QA','owner',now()),(2,2,'other@example.test',$1,'Other','owner',now());`, string(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO domains(id,org_id,name,status,verification_token,receiving_enabled,ses_verified,updated_at) VALUES(1,1,'fixture.test','active','fixture',true,true,now()),(2,2,'other.test','active','other',true,true,now());
INSERT INTO identities(id,user_id,domain_id,email,updated_at) VALUES(1,1,1,'qa@fixture.test',now()),(2,2,2,'other@other.test',now());
INSERT INTO receiving_configs(org_id,s3_bucket,s3_region,sns_topic_arn,ses_rule_set_name,webhook_secret,status,updated_at) VALUES(1,'fixture-bucket','us-east-1','arn:aws:sns:us-east-1:123456789012:fixture','fixture','fixture-secret','active',now());`); err != nil {
		t.Fatal(err)
	}
	raw := "From: sender@example.test\r\nTo: qa@fixture.test\r\nSubject: Invoice HTTP fixture\r\nMessage-ID: <http-fixture@example.test>\r\nContent-Type: text/plain\r\n\r\nLocal automation acceptance."
	reportXML := `<feedback><report_metadata><org_name>Example reporter</org_name><report_id>local-dmarc</report_id><date_range><begin>1790985600</begin><end>1791071999</end></date_range></report_metadata><policy_published><domain>fixture.test</domain><p>quarantine</p></policy_published><record><row><source_ip>192.0.2.1</source_ip><count>1</count><policy_evaluated><disposition>none</disposition><dkim>pass</dkim><spf>pass</spf></policy_evaluated></row><identifiers><header_from>fixture.test</header_from></identifiers><auth_results><dkim><domain>fixture.test</domain><result>pass</result></dkim></auth_results></record></feedback>`
	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("unexpected storage write %s", r.Method)
			w.WriteHeader(500)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/dmarc.xml") {
			io.WriteString(w, reportXML)
			return
		}
		io.WriteString(w, raw)
	}))
	t.Cleanup(s3.Close)
	t.Setenv("AWS_ENDPOINT_URL_S3", s3.URL)
	s := ghttp.GetServer(fmt.Sprintf("mailbox-http-%d", time.Now().UnixNano()))
	address := os.Getenv("MAILAT_UI_TEST_ADDRESS")
	if address == "" {
		address = "127.0.0.1:0"
	}
	s.SetAddr(address)
	s.SetDumpRouterMap(false)
	s.SetAccessLogEnabled(false)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	SetupWithContext(ctx, s, cfg)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Shutdown() })
	base := fmt.Sprintf("http://127.0.0.1:%d", s.GetListenedPort())
	call := func(method, path, key string, body any, status int) json.RawMessage {
		t.Helper()
		data, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, base+path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != status {
			t.Fatalf("%s %s: got %d expected %d: %s", method, path, res.StatusCode, status, b)
		}
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(b, &envelope); err != nil {
			t.Fatalf("invalid JSON: %s", b)
		}
		return envelope.Data
	}
	auth := service.NewAuthService(db, cfg)
	key, err := auth.CreateAPIKey(context.Background(), 1, 1, &model.CreateApiKeyRequest{Name: "Local HTTP", Permissions: []string{"email:read", "email:manage", "domains:read", "identities:read"}, RateLimit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	other, err := auth.CreateAPIKey(context.Background(), 2, 2, &model.CreateApiKeyRequest{Name: "Other", Permissions: []string{"email:read", "email:manage", "domains:read", "identities:read"}, RateLimit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	call("GET", "/api/v1/openapi.json", "", nil, 200)
	call("GET", "/docs/openapi.json", "", nil, 200)
	var domains []model.Domain
	if err := json.Unmarshal(call("GET", "/api/v1/domains", key.Key, nil, 200), &domains); err != nil || len(domains) != 1 || domains[0].Name != "fixture.test" || domains[0].DKIMPublicKey != "" {
		t.Fatalf("legacy-null domain list: %+v %v", domains, err)
	}
	call("GET", "/api/v1/domains/"+domains[0].UUID, key.Key, nil, 200)
	call("GET", "/api/v1/domains/"+domains[0].UUID, other.Key, nil, 404)
	var identities []model.Identity
	if err := json.Unmarshal(call("GET", "/api/v1/identities", key.Key, nil, 200), &identities); err != nil || len(identities) != 1 || identities[0].Email != "qa@fixture.test" || identities[0].DisplayName != "" {
		t.Fatalf("legacy-null identity list: %+v %v", identities, err)
	}
	call("GET", "/api/v1/identities/"+identities[0].UUID, key.Key, nil, 200)
	call("GET", "/api/v1/identities/"+identities[0].UUID, other.Key, nil, 404)
	// Renaming only this isolated fixture table provokes a real driver error.
	// Both endpoints must return a generic failure, never a successful partial list.
	func() {
		if _, err := db.Exec(`ALTER TABLE domain_dns_records RENAME TO temporarily_unavailable_dns`); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := db.Exec(`ALTER TABLE temporarily_unavailable_dns RENAME TO domain_dns_records`); err != nil {
				t.Fatal(err)
			}
		}()
		for _, path := range []string{"/api/v1/domains", "/api/v1/domains/" + domains[0].UUID} {
			req, _ := http.NewRequest("GET", base+path, nil)
			req.Header.Set("Authorization", "Bearer "+key.Key)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var envelope struct {
				Message string `json:"message"`
			}
			err = json.NewDecoder(res.Body).Decode(&envelope)
			res.Body.Close()
			if err != nil || res.StatusCode != 500 || envelope.Message != "Unable to load domain DNS records" {
				t.Fatalf("unsafe or untruthful domain failure: status=%d envelope=%+v err=%v", res.StatusCode, envelope, err)
			}
		}
	}()
	func() {
		if _, err := db.Exec(`ALTER TABLE identities RENAME TO temporarily_unavailable_identities`); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := db.Exec(`ALTER TABLE temporarily_unavailable_identities RENAME TO identities`); err != nil {
				t.Fatal(err)
			}
		}()
		for path, message := range map[string]string{"/api/v1/identities": "Unable to load identities", "/api/v1/identities/" + identities[0].UUID: "Unable to load identity"} {
			req, _ := http.NewRequest("GET", base+path, nil)
			req.Header.Set("Authorization", "Bearer "+key.Key)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var envelope struct {
				Message string `json:"message"`
			}
			err = json.NewDecoder(res.Body).Decode(&envelope)
			res.Body.Close()
			if err != nil || res.StatusCode != 500 || envelope.Message != message {
				t.Fatalf("unsafe identity failure: status=%d envelope=%+v err=%v", res.StatusCode, envelope, err)
			}
		}
	}()
	call("POST", "/api/v1/labels", other.Key, map[string]any{"name": "Foreign"}, 200)
	var label model.EmailLabel
	json.Unmarshal(call("POST", "/api/v1/labels", key.Key, map[string]any{"name": "Invoices", "color": "#2563eb"}, 200), &label)
	var filter model.InboxFilter
	json.Unmarshal(call("POST", "/api/v1/inbox/filters", key.Key, map[string]any{"name": "Invoice automation", "conditions": []map[string]string{{"field": "subject", "operator": "contains", "value": "Invoice"}}, "conditionLogic": "all", "actionLabels": []string{"Invoices"}, "actionFolder": "archive", "active": true}, 200), &filter)
	call("POST", "/api/v1/inbox/filters", key.Key, map[string]any{"name": "Foreign identity", "identityId": 2, "conditions": filter.Conditions, "actionStar": true}, 404)
	call("GET", "/api/v1/inbox/filters/"+filter.UUID, other.Key, nil, 404)
	call("POST", "/api/v1/inbox/filters/"+filter.UUID+"/test", key.Key, map[string]any{"subject": "Invoice sample"}, 200)
	receiving, err := service.NewReceivingService(db, "us-east-1", "fixture", "fixture", base)
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := receiving.AuthorizeNotification(context.Background(), "arn:aws:sns:us-east-1:123456789012:fixture", "fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	n := &model.SESNotification{NotificationType: "Received"}
	n.Mail.MessageId = "http-fixture"
	n.Receipt = &model.SESReceipt{Timestamp: time.Now().UTC().Format(time.RFC3339), Recipients: []string{"qa@fixture.test"}, Action: model.SESAction{Type: "S3", BucketName: "fixture-bucket", ObjectKey: "incoming/fixture.test/http-fixture"}}
	if err := receiving.ProcessIncomingEmail(context.Background(), authorization, n); err != nil {
		t.Fatal(err)
	}
	var list model.InboxListResponse
	json.Unmarshal(call("GET", "/api/v1/inbox/received?folder=archive&labels=Invoices", key.Key, nil, 200), &list)
	if len(list.Emails) != 1 || list.Emails[0].IsRead || len(list.Emails[0].Labels) != 1 {
		t.Fatalf("HTTP-created filter did not run: %+v", list)
	}
	id := list.Emails[0].UUID
	call("GET", "/api/v1/inbox/received/"+id, key.Key, nil, 200)
	var read bool
	if err := db.QueryRow(`SELECT is_read FROM received_emails WHERE uuid=$1`, id).Scan(&read); err != nil || read {
		t.Fatal("detail changed read status", err)
	}
	call("GET", "/api/v1/inbox/received/"+id, other.Key, nil, 404)
	call("GET", "/api/v1/inbox/received/not-a-uuid", key.Key, nil, 400)
	call("POST", "/api/v1/inbox/received/labels", key.Key, map[string]any{"emailUuids": []string{id}, "addLabels": []string{"Foreign"}}, 400)
	call("PUT", "/api/v1/labels/"+label.UUID, key.Key, map[string]any{"name": "Paid", "color": "#16a34a"}, 200)
	call("POST", "/api/v1/inbox/received/mark", key.Key, map[string]any{"emailUuids": []string{id}, "isRead": true}, 200)
	call("GET", "/api/v1/inbox/changes?cursor=0&limit=1", key.Key, nil, 200)
	call("GET", "/api/v1/inbox/changes?cursor=invalid", key.Key, nil, 400)
	call("PUT", "/api/v1/inbox/filters/"+filter.UUID, key.Key, map[string]any{"name": "Renamed filter"}, 200)
	call("DELETE", "/api/v1/inbox/filters/"+filter.UUID, key.Key, nil, 200)
	call("DELETE", "/api/v1/labels/"+label.UUID, key.Key, nil, 200)
	if got := string(call("GET", "/api/v1/labels", key.Key, nil, 200)); got != "[]" {
		t.Fatalf("empty collection: %s", got)
	}
	t.Run("DMARC folder and preference contracts", func(t *testing.T) {
		var login struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(call("POST", "/api/v1/auth/login", "", map[string]string{"email": "qa@example.test", "password": "mailat-local-test"}, 200), &login); err != nil || login.Token == "" {
			t.Fatal("local human login failed", err)
		}
		var settings service.UserSettings
		json.Unmarshal(call("GET", "/api/v1/settings", login.Token, nil, 200), &settings)
		if !settings.AutoOrganizeDMARCReports {
			t.Fatal("new account did not default to organizing reports")
		}
		call("PUT", "/api/v1/settings", key.Key, map[string]bool{"autoOrganizeDmarcReports": false}, 403)
		json.Unmarshal(call("PUT", "/api/v1/settings", login.Token, map[string]bool{"autoOrganizeDmarcReports": false}, 200), &settings)
		if settings.AutoOrganizeDMARCReports {
			t.Fatal("explicit false was not saved")
		}
		json.Unmarshal(call("PUT", "/api/v1/settings", login.Token, map[string]string{"density": "comfortable"}, 200), &settings)
		if settings.AutoOrganizeDMARCReports {
			t.Fatal("omitted preference reset the opt-out")
		}
		json.Unmarshal(call("GET", "/api/v1/settings", login.Token, nil, 200), &settings)
		if settings.AutoOrganizeDMARCReports {
			t.Fatal("opt-out did not persist")
		}
		call("PUT", "/api/v1/settings", login.Token, map[string]bool{"autoOrganizeDmarcReports": true}, 200)

		// Stored fixtures isolate management from classifier acceptance, which is
		// exercised against actual MIME/SES ingestion in the service tests.
		var report, foreign string
		if err := db.QueryRow(`INSERT INTO received_emails(org_id,domain_id,identity_id,message_id,from_email,to_emails,subject,folder,is_starred,labels,received_at,updated_at)
VALUES(1,1,1,'<dmarc-folder@fixture.test>','reporter@example.test',ARRAY['dmarc@fixture.test'],'Report domain: fixture.test Submitter: example.test Report-ID: local-dmarc','inbox',true,ARRAY['Keep label'],now(),now()) RETURNING uuid`).Scan(&report); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`INSERT INTO received_emails(org_id,domain_id,identity_id,message_id,from_email,to_emails,subject,folder,received_at,updated_at)
VALUES(2,2,2,'<foreign-dmarc@other.test>','reporter@example.test',ARRAY['other@other.test'],'Foreign report','dmarc-reports',now(),now()) RETURNING uuid`).Scan(&foreign); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO email_attachments(received_email_id,filename,content_type,size_bytes,s3_bucket,s3_key)
SELECT id,'dmarc.xml','application/xml',$2,'fixture-bucket','dmarc.xml' FROM received_emails WHERE uuid=$1`, report, len(reportXML)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE received_emails SET has_attachments=true WHERE uuid=$1`, report); err != nil {
			t.Fatal(err)
		}
		var before model.InboxCountsResponse
		json.Unmarshal(call("GET", "/api/v1/inbox/received/counts", key.Key, nil, 200), &before)
		call("POST", "/api/v1/inbox/received/move", key.Key, map[string]any{"emailUuids": []string{report, foreign}, "folder": "dmarc-reports"}, 404)
		var unchanged model.ReceivedEmail
		json.Unmarshal(call("GET", "/api/v1/inbox/received/"+report, key.Key, nil, 200), &unchanged)
		if unchanged.Folder != "inbox" {
			t.Fatal("cross-account selection partially moved mail")
		}
		call("POST", "/api/v1/inbox/received/move", key.Key, map[string]any{"emailUuids": []string{report}, "folder": "dmarc-reports"}, 200)
		var counts model.InboxCountsResponse
		json.Unmarshal(call("GET", "/api/v1/inbox/received/counts", key.Key, nil, 200), &counts)
		if counts.Inbox != before.Inbox-1 || counts.InboxUnread != before.InboxUnread-1 || counts.Unread != before.Unread || counts.DMARCReports != 1 || counts.DMARCReportsUnread != 1 {
			t.Fatalf("folder badges or global unread are inconsistent: before=%+v after=%+v", before, counts)
		}
		var reports model.InboxListResponse
		json.Unmarshal(call("GET", "/api/v1/inbox/received?folder=dmarc-reports&search=local-dmarc&hasAttachments=true&pageSize=1", key.Key, nil, 200), &reports)
		if reports.Total != 1 || len(reports.Emails) != 1 || reports.Emails[0].UUID != report || reports.Emails[0].IsRead || !reports.Emails[0].IsStarred || len(reports.Emails[0].Labels) != 1 {
			t.Fatalf("folder list or preserved message state: %+v", reports)
		}
		call("GET", "/api/v1/inbox/received?folder=dmarc-reports&identityId=2", key.Key, nil, 400)
		call("GET", "/api/v1/inbox/received/"+report, other.Key, nil, 404)
		var detail model.ReceivedEmail
		json.Unmarshal(call("GET", "/api/v1/inbox/received/"+report, key.Key, nil, 200), &detail)
		if len(detail.Attachments) != 1 || detail.IsRead {
			t.Fatalf("attachment detail changed mail: %+v", detail)
		}
		attachmentPath := detail.Attachments[0].DownloadURL
		call("GET", attachmentPath, other.Key, nil, 404)
		attachmentRequest, _ := http.NewRequest("GET", base+attachmentPath, nil)
		attachmentRequest.Header.Set("Authorization", "Bearer "+key.Key)
		attachmentResponse, err := http.DefaultClient.Do(attachmentRequest)
		if err != nil {
			t.Fatal(err)
		}
		attachmentBytes, err := io.ReadAll(attachmentResponse.Body)
		attachmentResponse.Body.Close()
		if err != nil || attachmentResponse.StatusCode != 200 || string(attachmentBytes) != reportXML {
			t.Fatal("private report attachment changed", err)
		}
		var all model.InboxListResponse
		json.Unmarshal(call("GET", "/api/v1/inbox/received?folder=all&search=local-dmarc", key.Key, nil, 200), &all)
		if all.Total != 1 {
			t.Fatal("report missing from All Mail")
		}
		call("POST", "/api/v1/inbox/received/mark", key.Key, map[string]any{"emailUuids": []string{report}, "isRead": true}, 200)
		json.Unmarshal(call("GET", "/api/v1/inbox/received/counts", key.Key, nil, 200), &counts)
		if counts.DMARCReportsUnread != 0 || counts.DMARCReports != 1 || counts.Unread != before.Unread-1 {
			t.Fatalf("report read count: %+v", counts)
		}
		call("POST", "/api/v1/inbox/received/move", key.Key, map[string]any{"emailUuids": []string{report}, "folder": "inbox"}, 200)
		json.Unmarshal(call("GET", "/api/v1/inbox/received/"+report, key.Key, nil, 200), &unchanged)
		if unchanged.Folder != "inbox" || !unchanged.IsRead || !unchanged.IsStarred || unchanged.IsArchived || unchanged.IsSpam || unchanged.IsTrashed {
			t.Fatalf("restore changed state: %+v", unchanged)
		}
		call("POST", "/api/v1/inbox/received/move", key.Key, map[string]any{"emailUuids": []string{report}, "folder": "dmarc-reports"}, 200)
		call("POST", "/api/v1/inbox/received/mark", key.Key, map[string]any{"emailUuids": []string{report}, "isRead": false}, 200)
		var reportFilter model.InboxFilter
		json.Unmarshal(call("POST", "/api/v1/inbox/filters", key.Key, map[string]any{"name": "Explicit report folder", "conditions": []map[string]string{{"field": "subject", "operator": "equals", "value": "Manually organize this"}}, "conditionLogic": "all", "actionFolder": "dmarc-reports", "active": true}, 200), &reportFilter)
		call("POST", "/api/v1/inbox/filters/"+reportFilter.UUID+"/test", key.Key, map[string]string{"subject": "Manually organize this"}, 200)
		call("DELETE", "/api/v1/inbox/filters/"+reportFilter.UUID, key.Key, nil, 200)
		var legacyReportRule service.EmailRule
		json.Unmarshal(call("POST", "/api/v1/rules", key.Key, map[string]any{"name": "Legacy report folder", "conditions": []map[string]string{{"field": "subject", "operator": "equals", "value": "Legacy folder rule"}}, "actions": []map[string]string{{"type": "move_to_folder", "value": "dmarc-reports"}}, "active": true}, 200), &legacyReportRule)
		var synced []model.InboxFilter
		json.Unmarshal(call("GET", "/api/v1/inbox/filters", key.Key, nil, 200), &synced)
		if len(synced) != 1 || synced[0].ActionFolder != "dmarc-reports" {
			t.Fatalf("legacy DMARC destination not synchronized: %+v", synced)
		}
		call("DELETE", fmt.Sprintf("/api/v1/rules/%d", legacyReportRule.ID), key.Key, nil, 200)
	})
	// Optional hold supports native-browser acceptance against this exact fixture.
	// It never loads project .env files or starts workers/real sending providers.
	if stop := os.Getenv("MAILAT_UI_TEST_STOP_FILE"); stop != "" {
		t.Log("Local UI fixture ready at " + base)
		deadline := time.Now().Add(30 * time.Minute)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(stop); err == nil {
				break
			}
			time.Sleep(time.Second)
		}
	}
}
