package service

import (
	"context"
	"encoding/json"
	"reflect"
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
	}
}
