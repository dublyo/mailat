package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestCloudflareDMARCConditionalSetup(t *testing.T) {
	const domain = "example.test"
	const hostname = "_dmarc.example.test"
	for _, tc := range []struct {
		name, status, zone        string
		existing                  []cloudflareDNSRecord
		resolver                  *dmarcTestResolver
		readFailure, externalRace bool
		posts                     int
	}{
		{name: "missing", status: "created", posts: 1},
		{name: "existing none", status: "preserved", existing: []cloudflareDNSRecord{{Type: "TXT", Name: hostname, Content: "v=DMARC1; p=none; rua=mailto:existing@example.test"}}},
		{name: "existing reject", status: "preserved", existing: []cloudflareDNSRecord{{Type: "TXT", Name: hostname, Content: "v=DMARC1; p=reject; adkim=s; sp=none"}}},
		{name: "existing default none", status: "preserved", existing: []cloudflareDNSRecord{{Type: "TXT", Name: hostname, Content: "v=DMARC1"}}},
		{name: "multiple", status: "conflict", existing: []cloudflareDNSRecord{{Type: "TXT", Name: hostname, Content: DefaultDMARCValue}, {Type: "TXT", Name: hostname, Content: "v=DMARC1; p=none"}}},
		{name: "invalid", status: "conflict", existing: []cloudflareDNSRecord{{Type: "TXT", Name: hostname, Content: "v=DMARC1; p=invalid"}}},
		{name: "delegated", status: "conflict", existing: []cloudflareDNSRecord{{Type: "CNAME", Name: hostname, Content: "policy.provider.test"}}},
		{name: "provider permission failure", status: "failed", readFailure: true},
		{name: "DNS timeout", status: "failed", resolver: &dmarcTestResolver{err: &net.DNSError{IsTimeout: true}}},
		{name: "inherited", status: "preserved", resolver: &dmarcTestResolver{txt: map[string][]string{"_dmarc.test": {"v=DMARC1; p=reject; psd=y"}}}},
		{name: "wrong zone", status: "failed", zone: "another.test"},
		{name: "external writer race", status: "conflict", externalRace: true, posts: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			records := append([]cloudflareDNSRecord(nil), tc.existing...)
			posts := 0
			previous := http.DefaultTransport
			http.DefaultTransport = cloudflareTestTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "api.cloudflare.com" {
					return nil, fmt.Errorf("unexpected host")
				}
				var payload interface{}
				switch {
				case r.Method == "GET" && r.URL.Path == "/client/v4/zones/test-zone":
					zone := domain
					if tc.zone != "" {
						zone = tc.zone
					}
					payload = map[string]interface{}{"success": true, "result": map[string]string{"id": "test-zone", "name": zone}}
				case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/dns_records"):
					if tc.readFailure {
						return nil, fmt.Errorf("simulated read denial")
					}
					if r.URL.Query().Get("name") != hostname {
						t.Fatal("unexpected owner queried")
					}
					payload = map[string]interface{}{"success": true, "result": records, "result_info": map[string]int{"total_pages": 1}}
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/dns_records"):
					posts++
					var rec cloudflareDNSRecord
					if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
						t.Fatal(err)
					}
					if rec.Name != hostname || rec.Type != "TXT" || rec.Content != DefaultDMARCValue {
						t.Fatalf("unexpected DMARC write: %+v", rec)
					}
					records = append(records, rec)
					if tc.externalRace {
						records = append(records, cloudflareDNSRecord{Type: "TXT", Name: hostname, Content: "v=DMARC1; p=reject"})
					}
					payload = map[string]interface{}{"success": true, "result": map[string]string{"id": "created"}}
				default:
					t.Fatalf("unexpected/destructive method %s %s", r.Method, r.URL.Path)
				}
				body, _ := json.Marshal(payload)
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = previous })
			resolver := tc.resolver
			if resolver == nil {
				resolver = &dmarcTestResolver{}
			}
			status, got, err := CloudflareEnsureDMARC(context.Background(), "test-token", "test-zone", domain, resolver)
			if status != tc.status || (err != nil) != (tc.status == "failed" || tc.status == "conflict") {
				t.Fatalf("status=%s got=%+v err=%v", status, got, err)
			}
			if posts != tc.posts {
				t.Fatalf("POST=%d want %d", posts, tc.posts)
			}
			if got.CanCreate {
				t.Fatal("completed write inspection exposed unsafe create flag")
			}
			if len(tc.existing) > 0 && tc.status == "preserved" && got.Value != tc.existing[0].Content {
				t.Fatal("existing policy mutated")
			}
			if tc.name == "missing" {
				status, _, err = CloudflareEnsureDMARC(context.Background(), "test-token", "test-zone", domain, resolver)
				if err != nil || status != "preserved" || posts != 1 {
					t.Fatalf("repeat setup duplicated policy: status=%s posts=%d err=%v", status, posts, err)
				}
			}
		})
	}
}

func TestCloudflareDMARCPreflightChecksAllPages(t *testing.T) {
	previous := http.DefaultTransport
	gets := 0
	http.DefaultTransport = cloudflareTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			t.Fatalf("unexpected DMARC write")
		}
		gets++
		var body string
		if r.URL.Query().Get("page") == "1" {
			body = `{"success":true,"result":[{"type":"TXT","name":"_dmarc.example.test","content":"v=DMARC1; p=none"}],"result_info":{"total_pages":2}}`
		} else {
			body = `{"success":true,"result":[{"type":"TXT","name":"_dmarc.example.test","content":"v=DMARC1; p=reject"}],"result_info":{"total_pages":2}}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	defer func() { http.DefaultTransport = previous }()
	status, err := CloudflareCreateDNSRecord(context.Background(), "test-token", "test-zone", "TXT", "_dmarc.example.test", DefaultDMARCValue)
	if err == nil || status != "conflict" || gets != 2 {
		t.Fatalf("missed duplicate on later page: %s %v (%d reads)", status, err, gets)
	}
}

func TestCloudflareDMARCWritePreflightPreservesChangedPolicy(t *testing.T) {
	previous := http.DefaultTransport
	http.DefaultTransport = cloudflareTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			t.Fatal("write attempted over existing alternate policy")
		}
		body := `{"success":true,"result":[{"type":"TXT","name":"_dmarc.example.test","content":"v=DMARC1; p=none; rua=mailto:existing@example.test"}]}`
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	defer func() { http.DefaultTransport = previous }()
	status, err := CloudflareCreateDNSRecord(context.Background(), "test-token", "test-zone", "TXT", "_dmarc.example.test", DefaultDMARCValue)
	if err != nil || status != "preserved" {
		t.Fatalf("alternate valid policy rejected: %s %v", status, err)
	}
}

func TestCloudflareDMARCReadAndUncertainWriteFailuresDoNotRetry(t *testing.T) {
	for _, stage := range []string{"second preflight", "POST", "postflight"} {
		t.Run(stage, func(t *testing.T) {
			previous := http.DefaultTransport
			reads, posts := 0, 0
			http.DefaultTransport = cloudflareTestTransport(func(r *http.Request) (*http.Response, error) {
				body := `{"success":true,"result":[]}`
				if r.Method == "GET" && r.URL.Path == "/client/v4/zones/test-zone" {
					body = `{"success":true,"result":{"name":"example.test"}}`
				} else if r.Method == "GET" {
					reads++
					if (stage == "second preflight" && reads == 2) || (stage == "postflight" && reads == 3) {
						return nil, fmt.Errorf("simulated provider read failure")
					}
				} else if r.Method == "POST" {
					posts++
					if stage == "POST" {
						return nil, fmt.Errorf("connection lost after write may have reached provider")
					}
					body = `{"success":true,"result":{"id":"created"}}`
				} else {
					t.Fatal("destructive request")
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = previous })
			status, inspection, err := CloudflareEnsureDMARC(context.Background(), "test-token", "test-zone", "example.test", &dmarcTestResolver{})
			if err == nil || status != "failed" || inspection.Status != "unknown" || inspection.CanCreate || inspection.Verified {
				t.Fatalf("failed read/write misclassified: %s %+v %v", status, inspection, err)
			}
			wantPosts := 1
			if stage == "second preflight" {
				wantPosts = 0
			}
			if posts != wantPosts {
				t.Fatalf("unsafe write/retry count: %d", posts)
			}
		})
	}
}
