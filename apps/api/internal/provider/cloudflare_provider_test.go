package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type cloudflareTestTransport func(*http.Request) (*http.Response, error)

func (f cloudflareTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCloudflareDNSPreflightPreservesOtherProviders(t *testing.T) {
	const hostname = "bounce.example.test"
	for _, tc := range []struct {
		name, recordType, value, status string
		existing                        []cloudflareDNSRecord
		posts                           int
	}{
		{"new MAIL FROM MX", "MX", "10 feedback-smtp.us-east-1.amazonses.com", "created", nil, 1},
		{"identical MX", "MX", "10 feedback-smtp.us-east-1.amazonses.com", "preserved", []cloudflareDNSRecord{{Type: "MX", Name: hostname, Content: "FEEDBACK-SMTP.US-EAST-1.AMAZONSES.COM.", Priority: 10}}, 0},
		{"another provider MX", "MX", "10 feedback-smtp.us-east-1.amazonses.com", "conflict", []cloudflareDNSRecord{{Type: "MX", Name: hostname, Content: "mail.other-provider.test", Priority: 10}}, 0},
		{"different MX priority", "MX", "10 feedback-smtp.us-east-1.amazonses.com", "conflict", []cloudflareDNSRecord{{Type: "MX", Name: hostname, Content: "feedback-smtp.us-east-1.amazonses.com", Priority: 20}}, 0},
		{"existing SPF", "TXT", "v=spf1 include:amazonses.com ~all", "conflict", []cloudflareDNSRecord{{Type: "TXT", Name: hostname, Content: "v=spf1 include:_spf.google.com ~all"}}, 0},
		{"existing DMARC", "TXT", "v=DMARC1; p=none", "conflict", []cloudflareDNSRecord{{Type: "TXT", Name: hostname, Content: "v=DMARC1; p=reject; rua=mailto:reports@example.test"}}, 0},
		{"unrelated TXT coexists with SPF", "TXT", "v=spf1 include:amazonses.com ~all", "created", []cloudflareDNSRecord{{Type: "TXT", Name: hostname, Content: "google-site-verification=test"}}, 1},
		{"identical SPF", "TXT", "v=spf1 include:amazonses.com ~all", "preserved", []cloudflareDNSRecord{{Type: "TXT", Name: hostname, Content: "v=spf1 include:amazonses.com ~all"}}, 0},
		{"duplicate SPF remains a conflict", "TXT", "v=spf1 include:amazonses.com ~all", "conflict", []cloudflareDNSRecord{{Type: "TXT", Name: hostname, Content: "v=spf1 include:amazonses.com ~all"}, {Type: "TXT", Name: hostname, Content: "v=spf1 include:_spf.google.com ~all"}}, 0},
		{"existing CNAME owns name", "MX", "10 feedback-smtp.us-east-1.amazonses.com", "conflict", []cloudflareDNSRecord{{Type: "CNAME", Name: hostname, Content: "other-provider.test"}}, 0},
		{"CNAME cannot replace TXT", "CNAME", "token.dkim.amazonses.com", "conflict", []cloudflareDNSRecord{{Type: "TXT", Name: hostname, Content: "v=DKIM1; p=test"}}, 0},
		{"proxied DKIM is not silently accepted", "CNAME", "token.dkim.amazonses.com", "conflict", []cloudflareDNSRecord{{Type: "CNAME", Name: hostname, Content: "token.dkim.amazonses.com", Proxied: true}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posts, gets := 0, 0
			previous := http.DefaultTransport
			http.DefaultTransport = cloudflareTestTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "api.cloudflare.com" || r.URL.Path != "/client/v4/zones/test-zone/dns_records" {
					return nil, fmt.Errorf("unexpected request")
				}
				var body []byte
				if r.Method == "GET" {
					gets++
					if r.URL.Query().Get("name") != hostname {
						t.Fatal("preflight did not restrict lookup to the intended owner name")
					}
					body, _ = json.Marshal(map[string]interface{}{"success": true, "result": tc.existing, "result_info": map[string]int{"total_pages": 1}})
				} else if r.Method == "POST" {
					posts++
					body = []byte(`{"success":true,"result":{"id":"created"}}`)
				} else {
					t.Fatalf("automatic setup attempted destructive method %s", r.Method)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = previous })
			status, err := CloudflareCreateDNSRecord(context.Background(), "test-token", "test-zone", tc.recordType, hostname, tc.value)
			if status != tc.status || (err != nil) != (tc.status == "conflict") {
				t.Fatalf("status=%q error=%v, want %q", status, err, tc.status)
			}
			if posts != tc.posts || gets != 1 {
				t.Fatalf("network operations GET=%d POST=%d, want GET=1 POST=%d", gets, posts, tc.posts)
			}
		})
	}
}

func TestCloudflareDNSPreflightFailsClosedAndChecksEveryPage(t *testing.T) {
	for _, unavailable := range []bool{true, false} {
		t.Run(fmt.Sprint("lookup unavailable ", unavailable), func(t *testing.T) {
			previous := http.DefaultTransport
			gets := 0
			http.DefaultTransport = cloudflareTestTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" {
					t.Fatalf("POST attempted without establishing an unused DNS name")
				}
				gets++
				if unavailable {
					return nil, fmt.Errorf("simulated read permission failure")
				}
				body := `{"success":true,"result":[],"result_info":{"total_pages":2}}`
				if gets == 2 {
					body = `{"success":true,"result":[{"type":"MX","name":"bounce.example.test","content":"other-provider.test","priority":10}],"result_info":{"total_pages":2}}`
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = previous })
			_, err := CloudflareCreateDNSRecord(context.Background(), "test-token", "test-zone", "MX", "bounce.example.test", "10 feedback-smtp.us-east-1.amazonses.com")
			if err == nil {
				t.Fatal("expected a non-mutating failure")
			}
			if !unavailable && gets != 2 {
				t.Fatal("preflight ignored later DNS pages")
			}
		})
	}
}

func TestCloudflareMailFromPairPreservesOccupiedNamespace(t *testing.T) {
	const hostname = "bounce.example.test"
	pair := []DNSRecord{
		{Type: "MX", Name: hostname, Value: "feedback-smtp.us-east-1.amazonses.com", Priority: 10},
		{Type: "TXT", Name: hostname, Value: "v=spf1 include:amazonses.com ~all"},
	}
	for _, existing := range []cloudflareDNSRecord{
		{Type: "MX", Name: hostname, Content: "bounce.other-provider.test", Priority: 10},
		{Type: "TXT", Name: hostname, Content: "v=spf1 include:other-provider.test ~all"},
		{Type: "CNAME", Name: hostname, Content: "bounce.other-provider.test"},
	} {
		t.Run("foreign "+existing.Type+" only", func(t *testing.T) {
			previous := http.DefaultTransport
			posts := 0
			http.DefaultTransport = cloudflareTestTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" {
					posts++
					return nil, fmt.Errorf("unexpected write to another provider's MAIL FROM namespace")
				}
				body, _ := json.Marshal(map[string]interface{}{"success": true, "result": []cloudflareDNSRecord{existing}})
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = previous })
			for _, wanted := range pair {
				value := wanted.Value
				if wanted.Type == "MX" {
					value = fmt.Sprintf("%d %s", wanted.Priority, value)
				}
				status, err := CloudflareCreateDNSRecord(context.Background(), "test-token", "test-zone", wanted.Type, hostname, value, pair...)
				if status != "conflict" || err == nil {
					t.Fatalf("%s ignored the occupied MAIL FROM pair: status=%q err=%v", wanted.Type, status, err)
				}
			}
			if posts != 0 {
				t.Fatalf("wrote %d records into another provider's MAIL FROM namespace", posts)
			}
		})
	}
}
