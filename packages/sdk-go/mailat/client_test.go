package mailat

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestDMARCFolderContract(t *testing.T) {
	var query string
	var body Object
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"code":0,"data":{"inbox":5,"inboxUnread":2,"dmarcReports":3,"dmarcReportsUnread":1,"unread":7,"starred":0,"sent":0,"drafts":0,"spam":4,"trash":0}}`)
	}))
	defer server.Close()
	client := NewClient("ue_fixture", WithBaseURL(server.URL))
	ctx := context.Background()
	counts, err := client.Inbox.FolderCounts(ctx, 42)
	if err != nil || counts.InboxUnread != 2 || counts.DMARCReports != 3 || counts.DMARCReportsUnread != 1 || counts.Unread != 7 || query != "identityId=42" {
		t.Fatalf("typed counts: %+v %s %v", counts, query, err)
	}
	generic, err := client.Inbox.Counts(ctx)
	if err != nil || generic["unread"] != float64(7) {
		t.Fatal("generic contract changed", generic, err)
	}
	if _, err = client.Inbox.List(ctx, url.Values{"folder": {FolderDMARCReports}}); err != nil || query != "folder=dmarc-reports" {
		t.Fatal(query, err)
	}
	if err = client.Inbox.Move(ctx, []string{"message-1"}, FolderDMARCReports); err != nil || body["folder"] != "dmarc-reports" {
		t.Fatal(body, err)
	}
	off := false
	for _, tc := range []struct {
		value UpdateDMARCReportsSettings
		json  string
	}{{UpdateDMARCReportsSettings{}, `{}`}, {UpdateDMARCReportsSettings{AutoOrganizeDMARCReports: &off}, `{"autoOrganizeDmarcReports":false}`}} {
		b, err := json.Marshal(tc.value)
		if err != nil || string(b) != tc.json {
			t.Fatal(string(b), err)
		}
	}
}

func fixtureSign(body []byte, stamp int64) string {
	prefix := fmt.Sprintf("%d", stamp)
	mac := hmac.New(sha256.New, []byte("fixture-secret"))
	mac.Write([]byte(prefix + "."))
	mac.Write(body)
	return "t=" + prefix + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}
func TestWebhookWireVerification(t *testing.T) {
	body := []byte(`{"version":"1","id":"event-1","type":"email.received","createdAt":"2026-10-04T00:00:00Z","data":{"messageUuid":"mail-1","subject":"مرحبا"}}`)
	signature := fixtureSign(body, time.Now().Unix())
	if !VerifyWebhookSignature(body, signature, "fixture-secret", 0) {
		t.Fatal("real signature rejected")
	}
	for _, sig := range []string{signature + ",t=1", "t=," + signature, "x=1," + signature, fixtureSign(body, time.Now().Add(-301*time.Second).Unix()), fixtureSign(body, time.Now().Add(301*time.Second).Unix())} {
		if VerifyWebhookSignature(body, sig, "fixture-secret", 5*time.Minute) {
			t.Fatalf("invalid signature accepted: %s", sig)
		}
	}
	if VerifyWebhookSignature(append(body, ' '), signature, "fixture-secret", 0) {
		t.Fatal("mutated body accepted")
	}
	seen := false
	claim := func(id string) (bool, error) {
		if id != "event-1" {
			t.Fatal("wrong event ID")
		}
		if seen {
			return false, nil
		}
		seen = true
		return true, nil
	}
	event, err := ParseWebhookPayload(body, signature, "fixture-secret", claim)
	if err != nil || event.Type != "email.received" {
		t.Fatalf("event: %#v %v", event, err)
	}
	if _, err = ParseWebhookPayload(body, signature, "fixture-secret", claim); err == nil {
		t.Fatal("duplicate event accepted")
	}
}
func TestHTTPContracts(t *testing.T) {
	var lastPath, lastKey string
	var lastBody Object
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastPath, lastKey = r.URL.Path, r.Header.Get("Idempotency-Key")
		lastBody = Object{}
		_ = json.NewDecoder(r.Body).Decode(&lastBody)
		if r.Header.Get("Authorization") != "Bearer ue_fixture" {
			t.Error("missing API key")
		}
		if r.URL.Path == "/inbox/received/mail-1/attachments/attachment-1" {
			w.Write([]byte{0, 255, 1})
			return
		}
		if r.URL.Path == "/inbox/received/limited" {
			w.Header().Set("Retry-After", "11")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"code":429,"message":"rate limited"}`)
			return
		}
		var data interface{} = Object{}
		switch r.URL.Path {
		case "/emails":
			data = Object{"id": "mail-1", "messageId": "ses-1", "status": "sent", "acceptedAt": "2026-10-04T00:00:00Z"}
		case "/emails/batch":
			data = Object{"results": []Object{{"index": 0, "id": "mail-1", "messageId": "ses-1", "status": "sent"}}}
		case "/templates":
			data = []Object{{"id": 1, "uuid": "template-1", "name": "T", "subject": "S", "htmlBody": "<p>hello</p>", "textBody": "hello", "isActive": true, "createdAt": "2026-10-04T00:00:00Z", "updatedAt": "2026-10-04T00:00:00Z"}}
		case "/webhooks":
			data = []Object{}
		case "/webhook-triggers/trigger-1/test":
			data = Object{"eventId": "event-1", "deliveryId": "delivery-1", "status": "retry", "httpStatus": 500}
		}
		_ = json.NewEncoder(w).Encode(Object{"code": 0, "data": data})
	}))
	defer server.Close()
	client := NewClient("ue_fixture", WithBaseURL(server.URL))
	ctx := context.Background()
	req := &SendEmailRequest{From: "a@fixture.invalid", To: []string{"b@fixture.invalid"}, Subject: "Hello", Text: "body", Attachments: []Attachment{{Name: "test.txt", Content: "YQ==", Type: "text/plain"}}}
	if _, err := client.Emails.Send(ctx, req, nil); err == nil {
		t.Fatal("missing key accepted")
	}
	sent, err := client.Emails.Send(ctx, req, &SendOptions{IdempotencyKey: "same-send-key"})
	if err != nil || sent.MessageID != "ses-1" || lastKey != "same-send-key" {
		t.Fatalf("send: %#v %v key=%s", sent, err, lastKey)
	}
	attachment := lastBody["attachments"].([]interface{})[0].(map[string]interface{})
	if attachment["type"] != "text/plain" {
		t.Fatal(attachment)
	}
	batch, err := client.Emails.SendBatch(ctx, []SendEmailRequest{*req}, &SendOptions{IdempotencyKey: "batch-send-key"})
	if err != nil || batch.Results[0].MessageID != "ses-1" || lastKey != "batch-send-key" {
		t.Fatalf("batch: %#v %v", batch, err)
	}
	templates, err := client.Templates.List(ctx)
	if err != nil || templates[0].ID != 1 || templates[0].UUID != "template-1" || templates[0].HTML != "<p>hello</p>" {
		t.Fatalf("templates: %#v %v", templates, err)
	}
	hooks, err := client.Webhooks.List(ctx)
	if err != nil || hooks == nil || len(hooks) != 0 {
		t.Fatalf("empty list %#v %v", hooks, err)
	}
	if err = client.Inbox.Mark(ctx, []string{"mail-1"}, false); err != nil || lastBody["isRead"] != false {
		t.Fatal(lastBody, err)
	}
	if err = client.Inbox.AssignLabels(ctx, []string{"mail-1"}, []string{"Invoices"}, []string{"Old"}); err != nil || lastBody["addLabels"] == nil || lastBody["removeLabels"] == nil {
		t.Fatal(lastBody, err)
	}
	data, err := client.Inbox.Attachment(ctx, "mail-1", "attachment-1")
	if err != nil || len(data) != 3 || data[1] != 255 {
		t.Fatal(data, err)
	}
	_, err = client.Domains.SetupSending(ctx, "domain-1")
	if err != nil || lastPath != "/domains/domain-1/setup-sending" {
		t.Fatal(lastPath, err)
	}
	result, err := client.Triggers.Test(ctx, "trigger-1")
	if err != nil || result.HTTPStatus != 500 {
		t.Fatal(result, err)
	}
	_, err = client.Deliveries.Replay(ctx, "delivery-1")
	if err != nil || lastPath != "/webhook-deliveries/delivery-1/replay" {
		t.Fatal(lastPath, err)
	}
	_, err = client.Inbox.Get(ctx, "limited")
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.RetryAfter != "11" || apiErr.StatusCode != 429 {
		t.Fatalf("error: %#v", err)
	}
}
