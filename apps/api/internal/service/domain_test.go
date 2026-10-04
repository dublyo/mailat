package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/dublyo/mailat/api/internal/testutil"
)

type domainOnboardingProvider struct{ provider.EmailProvider }

func (*domainOnboardingProvider) Name() string { return "ses" }
func (*domainOnboardingProvider) VerifyDomain(_ context.Context, domain string) (*provider.DomainVerificationResult, error) {
	result := &provider.DomainVerificationResult{Domain: domain,
		SPFRecord:   &provider.DNSRecord{Type: "TXT", Name: domain, Value: "v=spf1 include:amazonses.com ~all"},
		DMARCRecord: &provider.DNSRecord{Type: "TXT", Name: "_dmarc." + domain, Value: "v=DMARC1; p=none"},
	}
	result.MailFromRecords = []provider.DNSRecord{
		{Type: "MX", Name: "bounce." + domain, Value: "feedback-smtp.us-east-1.amazonses.com", Priority: 10},
		{Type: "TXT", Name: "bounce." + domain, Value: "v=spf1 include:amazonses.com ~all"},
	}
	for _, token := range []string{"test-dkim-one", "test-dkim-two", "test-dkim-three"} {
		result.DKIMRecords = append(result.DKIMRecords, provider.DNSRecord{Type: "CNAME", Name: token + "._domainkey." + domain, Value: token + ".dkim.amazonses.com"})
	}
	return result, nil
}

func TestCreateDomainResponseIncludesProviderMetadata(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	var org int64
	if err := db.QueryRow(`INSERT INTO organizations(name,slug,updated_at) VALUES('Domain onboarding','domain-onboarding',now()) RETURNING id`).Scan(&org); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"ses", "smtp"} {
		t.Run(mode, func(t *testing.T) {
			svc := &DomainService{db: db, cfg: &config.Config{EmailProvider: mode, DisableAppLimits: true}}
			if mode == "ses" {
				svc.emailProvider = &domainOnboardingProvider{}
			}
			created, err := svc.CreateDomain(ctx, org, &model.CreateDomainRequest{Name: mode + ".example.test"})
			if err != nil {
				t.Fatal(err)
			}
			// Check the actual serialized response consumed by the newly-created card.
			raw, err := json.Marshal(created)
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				EmailProvider string   `json:"emailProvider"`
				Tokens        []string `json:"sesDkimTokens"`
			}
			if err = json.Unmarshal(raw, &response); err != nil {
				t.Fatal(err)
			}
			if response.EmailProvider != mode {
				t.Fatalf("create response provider=%q, want %q", response.EmailProvider, mode)
			}
			if mode == "ses" && !reflect.DeepEqual(response.Tokens, []string{"test-dkim-one", "test-dkim-two", "test-dkim-three"}) {
				t.Fatalf("create response lost SES DKIM tokens: %v", response.Tokens)
			}
			reloaded, err := svc.GetDomain(ctx, org, created.UUID)
			if err != nil {
				t.Fatal(err)
			}
			if created.EmailProvider != reloaded.EmailProvider || !reflect.DeepEqual(created.SESDKIMTokens, reloaded.SESDKIMTokens) {
				t.Fatal("create and reload disagree about provider metadata")
			}
			if mode == "ses" {
				assertSESSendingDNS(t, svc, created)
			}
		})
	}
}

func TestSESReinitPreservesAndRepairsOwnershipVerificationTXT(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	var org int64
	if err := db.QueryRow(`INSERT INTO organizations(name,slug,updated_at) VALUES('Reinit domain','reinit-domain',now()) RETURNING id`).Scan(&org); err != nil {
		t.Fatal(err)
	}
	svc := &DomainService{db: db, cfg: &config.Config{EmailProvider: "ses", AWSRegion: "us-east-1", DisableAppLimits: true}, emailProvider: &domainOnboardingProvider{}}
	domain, err := svc.CreateDomain(ctx, org, &model.CreateDomainRequest{Name: "onboarding.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	hostname := "_verification." + domain.Name
	var originalID int64
	if err = db.QueryRow(`UPDATE domain_dns_records SET verified=true WHERE domain_id=$1 AND record_type='TXT' AND hostname=$2 RETURNING id`, domain.ID, hostname).Scan(&originalID); err != nil {
		t.Fatal(err)
	}
	for _, repairMissing := range []bool{false, true} {
		// Simulate records persisted by old onboarding. Re-init must not recreate them.
		if _, err = db.Exec(`INSERT INTO domain_dns_records(domain_id,record_type,hostname,expected_value,verified) VALUES($1,'MX',$2,'10 inbound-smtp.us-east-1.amazonaws.com',false),($1,'TXT',$2,'v=spf1 include:amazonses.com ~all',false)`, domain.ID, domain.Name); err != nil {
			t.Fatal(err)
		}
		assertSESSendingDNS(t, svc, domain)
		if repairMissing {
			if _, err = db.Exec(`DELETE FROM domain_dns_records WHERE domain_id=$1 AND hostname=$2`, domain.ID, hostname); err != nil {
				t.Fatal(err)
			}
		}
		records, err := svc.InitiateSESVerification(ctx, domain.ID)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, record := range records {
			if record["name"] == domain.Name && (record["type"] == "MX" || strings.HasPrefix(record["value"], "v=spf1")) {
				t.Fatal("SES re-init returned inbound root MX or root SPF")
			}
			if record["type"] == "TXT" && record["name"] == hostname && record["value"] == domain.VerificationToken {
				found = true
			}
		}
		if !found {
			t.Fatal("wizard response omits ownership verification TXT")
		}
		var id int64
		var value string
		var verified bool
		if err = db.QueryRow(`SELECT id,expected_value,verified FROM domain_dns_records WHERE domain_id=$1 AND record_type='TXT' AND hostname=$2`, domain.ID, hostname).Scan(&id, &value, &verified); err != nil {
			t.Fatal(err)
		}
		if value != domain.VerificationToken {
			t.Fatal("re-init changed the required ownership token")
		}
		if !repairMissing && (id != originalID || !verified) {
			t.Fatal("re-init discarded the existing ownership verification state")
		}
		// GetDNSRecords supplies VerifyDNS; its activation precondition must remain satisfiable.
		stored, err := svc.GetDNSRecords(ctx, domain.ID)
		if err != nil {
			t.Fatal(err)
		}
		matches := 0
		for _, record := range stored {
			if record.RecordType == "TXT" && record.Hostname == hostname && record.Value == domain.VerificationToken {
				matches++
			}
		}
		if matches != 1 {
			t.Fatalf("VerifyDNS sees %d ownership records, want 1", matches)
		}
		assertSESSendingDNS(t, svc, domain)
	}
}

func assertSESSendingDNS(t *testing.T, svc *DomainService, domain *model.Domain) {
	t.Helper()
	records, err := svc.GetDNSRecords(context.Background(), domain.ID)
	if err != nil {
		t.Fatal(err)
	}
	bounce := map[string]bool{}
	for _, record := range records {
		if record.Hostname == domain.Name && (record.RecordType == "MX" || strings.HasPrefix(record.Value, "v=spf1")) {
			t.Fatal("SES instructions/VerifyDNS included inbound root MX or root SPF")
		}
		if record.Hostname == "bounce."+domain.Name {
			bounce[record.RecordType] = true
		}
		if record.Hostname == "_dmarc."+domain.Name && record.Value != provider.DefaultDMARCValue {
			t.Fatal("new SES instructions lost the approved conditional DMARC default")
		}
	}
	if !bounce["MX"] || !bounce["TXT"] {
		t.Fatal("SES MAIL FROM lost its required MX/SPF records")
	}
}

type domainCloudflareTransport func(*http.Request) (*http.Response, error)

func (f domainCloudflareTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCloudflareSetupPreservesLegacyRootRoutingAndPolicies(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	var org int64
	if err := db.QueryRow(`INSERT INTO organizations(name,slug,updated_at) VALUES('DNS coexistence','dns-coexistence',now()) RETURNING id`).Scan(&org); err != nil {
		t.Fatal(err)
	}
	// SMTP still exposes legacy root records, so this exercises the write guard
	// independently of the SES sending-instruction filter.
	svc := &DomainService{db: db, cfg: &config.Config{EmailProvider: "smtp", DisableAppLimits: true}}
	domain, err := svc.CreateDomain(ctx, org, &model.CreateDomainRequest{Name: "coexist.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"@", "", "COEXIST.EXAMPLE.TEST.", "other.example.test", "bounce." + domain.Name} {
		if _, err := db.Exec(`INSERT INTO domain_dns_records(domain_id,record_type,hostname,expected_value,verified) VALUES($1,'MX',$2,'10 feedback-smtp.us-east-1.amazonses.com',false)`, domain.ID, name); err != nil {
			t.Fatal(err)
		}
	}
	var posts []map[string]interface{}
	previous := http.DefaultTransport
	http.DefaultTransport = domainCloudflareTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.cloudflare.com" {
			return nil, fmt.Errorf("unexpected network request")
		}
		body := `{"success":true,"result":[],"result_info":{"total_pages":1}}`
		if r.Method == "GET" && r.URL.Path == "/client/v4/zones/test-zone" {
			body = fmt.Sprintf(`{"success":true,"result":{"id":"test-zone","name":%q}}`, domain.Name)
		}
		if r.Method == "POST" {
			var payload map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				return nil, err
			}
			posts = append(posts, payload)
			body = `{"success":true,"result":{"id":"created"}}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	results, err := svc.AddDNSToCloudflare(ctx, domain.ID, "test-token", "test-zone")
	if err != nil {
		t.Fatal(err)
	}
	skipped := 0
	for _, result := range results {
		if result["skipped"] == true {
			skipped++
			if result["success"] != false || result["status"] != "skipped" || result["reason"] == "" {
				t.Fatalf("missing safe-skip response metadata: %v", result)
			}
		}
	}
	if skipped != 7 { // Five root/outside MX variants, root SPF, and manual DMARC.
		t.Fatalf("got %d skipped records, want 7", skipped)
	}
	bounceMX := false
	for _, post := range posts {
		if post["type"] == "MX" {
			if post["name"] != "bounce."+domain.Name || post["priority"] != float64(10) {
				t.Fatalf("automatic setup attempted to change inbound routing: %v", post)
			}
			bounceMX = true
		}
		if post["type"] == "TXT" && (strings.HasPrefix(post["content"].(string), "v=spf1") || strings.HasPrefix(post["content"].(string), "v=DMARC1")) {
			t.Fatal("automatic setup attempted a root authentication policy write")
		}
	}
	if !bounceMX {
		t.Fatal("automatic setup skipped the separate MAIL FROM MX")
	}
}

// Exercise the persisted-record/service boundary as well as the provider helper:
// either member of an existing provider's MAIL FROM pair must block both writes.
func TestCloudflareSetupPreservesOccupiedMailFromPair(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	var org int64
	if err := db.QueryRow(`INSERT INTO organizations(name,slug,updated_at) VALUES('MAIL FROM coexistence','mail-from-coexistence',now()) RETURNING id`).Scan(&org); err != nil {
		t.Fatal(err)
	}
	for _, existingType := range []string{"MX", "TXT", "CNAME"} {
		t.Run(existingType, func(t *testing.T) {
			svc := &DomainService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: &domainOnboardingProvider{}}
			domain, err := svc.CreateDomain(ctx, org, &model.CreateDomainRequest{Name: strings.ToLower(existingType) + ".pair.example.test"})
			if err != nil {
				t.Fatal(err)
			}
			hostname := "bounce." + domain.Name
			content := "bounce.other-provider.test"
			if existingType == "TXT" {
				content = "v=spf1 include:other-provider.test ~all"
			}
			var posts []map[string]interface{}
			previous := http.DefaultTransport
			http.DefaultTransport = domainCloudflareTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "api.cloudflare.com" {
					return nil, fmt.Errorf("unexpected request")
				}
				body := `{"success":true,"result":[]}`
				if r.URL.Path == "/client/v4/zones/test-zone" {
					body = fmt.Sprintf(`{"success":true,"result":{"id":"test-zone","name":%q}}`, domain.Name)
				} else if r.URL.Query().Get("name") == "_dmarc."+domain.Name {
					body = fmt.Sprintf(`{"success":true,"result":[{"type":"TXT","name":%q,"content":"v=DMARC1; p=none"}]}`, "_dmarc."+domain.Name)
				}
				switch r.Method {
				case "GET":
					if r.URL.Query().Get("name") == hostname {
						encoded, _ := json.Marshal(map[string]interface{}{"success": true, "result": []map[string]interface{}{{"type": existingType, "name": hostname, "content": content, "priority": 10}}})
						body = string(encoded)
					}
				case "POST":
					var payload map[string]interface{}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						return nil, err
					}
					posts = append(posts, payload)
					body = `{"success":true,"result":{"id":"created"}}`
				default:
					t.Fatalf("automatic setup attempted destructive method %s", r.Method)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = previous })
			results, err := svc.AddDNSToCloudflare(ctx, domain.ID, "test-token", "test-zone")
			if err != nil {
				t.Fatal(err)
			}
			conflicts := 0
			for _, result := range results {
				if result["hostname"] == hostname {
					if result["success"] != false || result["skipped"] != true || result["status"] != "conflict" || result["reason"] == "" {
						t.Fatalf("MAIL FROM %s was not reported as a preserved conflict: %v", result["type"], result)
					}
					conflicts++
				}
			}
			if conflicts != 2 {
				t.Fatalf("preserved %d members of the occupied MAIL FROM pair, want 2", conflicts)
			}
			for _, post := range posts {
				if post["name"] == hostname {
					t.Fatalf("automatic setup changed another provider's MAIL FROM namespace: %v", post)
				}
			}
			if len(posts) != 4 { // Three DKIM CNAMEs and ownership TXT remain independent.
				t.Fatalf("created %d independent records, want 4", len(posts))
			}
		})
	}
}
