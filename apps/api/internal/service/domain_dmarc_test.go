package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/dublyo/mailat/api/internal/testutil"
)

type serviceDMARCResolver struct {
	mu    sync.Mutex
	txt   map[string][]string
	calls int
}

func (r *serviceDMARCResolver) LookupCNAME(_ context.Context, name string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return name, nil
}
func (r *serviceDMARCResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.txt[name], nil
}

func TestDMARCOwnershipAndRealPolicyVerification(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	var org int64
	if err := db.QueryRow(`INSERT INTO organizations(name,slug,updated_at) VALUES('DMARC verification','dmarc-verification',now()) RETURNING id`).Scan(&org); err != nil {
		t.Fatal(err)
	}
	resolver := &serviceDMARCResolver{}
	svc := &DomainService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: &domainOnboardingProvider{}, dmarcResolver: resolver}
	domain, err := svc.CreateDomain(ctx, org, &model.CreateDomainRequest{Name: "mail.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.GetDMARC(ctx, org+1000, domain.UUID); err == nil || resolver.calls != 0 {
		t.Fatal("other organization reached DNS inspection")
	}
	// This regression isolates the DMARC branch; unrelated DNS and AWS checks are
	// already covered elsewhere and must not contact real services in this test.
	if _, err = db.Exec(`DELETE FROM domain_dns_records WHERE domain_id=$1 AND hostname<>$2`, domain.ID, "_dmarc."+domain.Name); err != nil {
		t.Fatal(err)
	}
	svc.emailProvider = nil
	for _, tc := range []struct {
		name     string
		txt      map[string][]string
		verified bool
		value    string
	}{
		{name: "existing none", txt: map[string][]string{"_dmarc.mail.example.test": {"v=DMARC1; p=none; rua=mailto:original@example.test"}}, verified: true, value: "v=DMARC1; p=none; rua=mailto:original@example.test"},
		{name: "existing reject", txt: map[string][]string{"_dmarc.mail.example.test": {"v=DMARC1; p=reject; adkim=s"}}, verified: true, value: "v=DMARC1; p=reject; adkim=s"},
		{name: "inherited", txt: map[string][]string{"_dmarc.example.test": {"v=DMARC1; p=reject; sp=none"}}, verified: true, value: "v=DMARC1; p=reject; sp=none"},
		{name: "invalid prefix", txt: map[string][]string{"_dmarc.mail.example.test": {"v=DMARC1; p=invalid"}}},
		{name: "multiple policies", txt: map[string][]string{"_dmarc.mail.example.test": {"v=DMARC1; p=none", "v=DMARC1; p=reject"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver.txt = tc.txt
			inspection, err := svc.GetDMARC(ctx, org, domain.UUID)
			if err != nil {
				t.Fatal(err)
			}
			if inspection.Verified != tc.verified {
				t.Fatalf("inspection mismatch %+v", inspection)
			}
			if _, err = svc.VerifyDNS(ctx, domain.ID); err != nil {
				t.Fatal(err)
			}
			var flag, recordFlag bool
			var actual, expected string
			if err = db.QueryRow(`SELECT d.dmarc_verified,r.verified,r.actual_value,r.expected_value FROM domains d JOIN domain_dns_records r ON r.domain_id=d.id WHERE d.id=$1`, domain.ID).Scan(&flag, &recordFlag, &actual, &expected); err != nil {
				t.Fatal(err)
			}
			if flag != tc.verified || recordFlag != tc.verified || actual != tc.value {
				t.Fatalf("persisted flags/value %v %v %q", flag, recordFlag, actual)
			}
			if expected != provider.DefaultDMARCValue {
				t.Fatal("inspection overwrote the stored suggestion")
			}
		})
	}
}

func TestCloudflareDMARCConcurrentOrganizationsCreateOnce(t *testing.T) {
	db := testutil.Database(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resolver := &serviceDMARCResolver{}
	svc := &DomainService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: &domainOnboardingProvider{}, dmarcResolver: resolver}
	var domains []*model.Domain
	for i := 0; i < 2; i++ {
		var org int64
		if err := db.QueryRow(`INSERT INTO organizations(name,slug,updated_at) VALUES($1,$2,now()) RETURNING id`, fmt.Sprint("DMARC ", i), fmt.Sprint("dmarc-concurrent-", i)).Scan(&org); err != nil {
			t.Fatal(err)
		}
		d, err := svc.CreateDomain(ctx, org, &model.CreateDomainRequest{Name: "concurrent.example.test"})
		if err != nil {
			t.Fatal(err)
		}
		domains = append(domains, d)
	}
	// Prove old database suggestions cannot change the approved conditional default.
	if _, err := db.Exec(`UPDATE domain_dns_records SET expected_value='v=DMARC1; p=none' WHERE hostname='_dmarc.concurrent.example.test'`); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	records := map[string][]map[string]interface{}{}
	dmarcPosts := 0
	previous := http.DefaultTransport
	http.DefaultTransport = domainCloudflareTransport(func(r *http.Request) (*http.Response, error) {
		var payload interface{}
		switch {
		case r.Method == "GET" && r.URL.Path == "/client/v4/zones/test-zone":
			payload = map[string]interface{}{"success": true, "result": map[string]string{"id": "test-zone", "name": "example.test"}}
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/dns_records"):
			mu.Lock()
			saved := append([]map[string]interface{}(nil), records[r.URL.Query().Get("name")]...)
			mu.Unlock()
			payload = map[string]interface{}{"success": true, "result": saved}
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/dns_records"):
			var record map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
				return nil, err
			}
			name := record["name"].(string)
			if name == "_dmarc.concurrent.example.test" {
				if record["content"] != provider.DefaultDMARCValue {
					return nil, fmt.Errorf("wrong missing-policy default")
				}
				time.Sleep(75 * time.Millisecond)
				mu.Lock()
				dmarcPosts++
				mu.Unlock()
			}
			if name == "concurrent.example.test" {
				return nil, fmt.Errorf("attempted root DNS write")
			}
			mu.Lock()
			records[name] = append(records[name], record)
			mu.Unlock()
			payload = map[string]interface{}{"success": true, "result": map[string]string{"id": "created"}}
		default:
			return nil, fmt.Errorf("unexpected/destructive request: %s %s", r.Method, r.URL.Path)
		}
		body, _ := json.Marshal(payload)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	type outcome struct {
		results []map[string]interface{}
		err     error
	}
	outcomes := make(chan outcome, 2)
	start := make(chan struct{})
	for _, domain := range domains {
		go func(id int64) {
			<-start
			result, err := svc.AddDNSToCloudflare(ctx, id, "test-token", "test-zone")
			outcomes <- outcome{result, err}
		}(domain.ID)
	}
	close(start)
	statuses := map[string]int{}
	for i := 0; i < 2; i++ {
		out := <-outcomes
		if out.err != nil {
			t.Fatal(out.err)
		}
		for _, result := range out.results {
			if result["hostname"] == "_dmarc.concurrent.example.test" {
				if result["success"] != true {
					t.Fatalf("DMARC setup failed: %+v", result)
				}
				statuses[result["status"].(string)]++
			}
		}
	}
	if dmarcPosts != 1 || statuses["created"] != 1 || statuses["preserved"] != 1 {
		t.Fatalf("duplicate or skipped creation: posts=%d statuses=%v", dmarcPosts, statuses)
	}
}

func TestCloudflareZoneMismatchStopsAllWrites(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	var org int64
	if err := db.QueryRow(`INSERT INTO organizations(name,slug,updated_at) VALUES('Wrong DNS zone','wrong-zone',now()) RETURNING id`).Scan(&org); err != nil {
		t.Fatal(err)
	}
	svc := &DomainService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: &domainOnboardingProvider{}}
	domain, err := svc.CreateDomain(ctx, org, &model.CreateDomainRequest{Name: "correct.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	previous := http.DefaultTransport
	http.DefaultTransport = domainCloudflareTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Path != "/client/v4/zones/test-zone" {
			t.Fatal("records accessed before zone ownership check")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"success":true,"result":{"id":"test-zone","name":"wrong.example.test"}}`))}, nil
	})
	defer func() { http.DefaultTransport = previous }()
	if _, err = svc.AddDNSToCloudflare(ctx, domain.ID, "test-token", "test-zone"); err == nil {
		t.Fatal("zone mismatch accepted")
	}
}
