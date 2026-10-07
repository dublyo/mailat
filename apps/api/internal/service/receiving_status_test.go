package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/testutil"
)

type fakeMXResolver struct {
	mu      sync.Mutex
	calls   int
	records []*net.MX
	err     error
}

func (f *fakeMXResolver) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.records, f.err
}

const sesInbound = "inbound-smtp.us-east-2.amazonaws.com"

func TestClassifyReceivingMX(t *testing.T) {
	notFound := &net.DNSError{Err: "no such host", Name: "x.test", IsNotFound: true}
	cases := []struct {
		name     string
		records  []*net.MX
		err      error
		want     string
		existing []string
	}{
		{"not found", nil, notFound, MXStatusMissing, []string{}},
		{"empty", []*net.MX{}, nil, MXStatusMissing, []string{}},
		{"timeout is unknown", nil, &net.DNSError{Err: "i/o timeout", IsTimeout: true}, MXStatusUnknown, []string{}},
		{"other error is unknown", nil, errors.New("boom"), MXStatusUnknown, []string{}},
		{"published", []*net.MX{{Host: sesInbound + ".", Pref: 10}}, nil, MXStatusPublished, []string{sesInbound}},
		{"published with backup", []*net.MX{{Host: "backup.example.net.", Pref: 20}, {Host: strings.ToUpper(sesInbound) + ".", Pref: 10}}, nil, MXStatusPublished, []string{sesInbound, "backup.example.net"}},
		{"elsewhere", []*net.MX{{Host: "aspmx.l.google.com.", Pref: 1}}, nil, MXStatusConflict, []string{"aspmx.l.google.com"}},
		{"equal priority elsewhere", []*net.MX{{Host: sesInbound + ".", Pref: 10}, {Host: "mx.other.test.", Pref: 10}}, nil, MXStatusConflict, []string{sesInbound, "mx.other.test"}},
		{"other region", []*net.MX{{Host: "inbound-smtp.us-east-1.amazonaws.com.", Pref: 10}}, nil, MXStatusConflict, []string{"inbound-smtp.us-east-1.amazonaws.com"}},
		{"null MX", []*net.MX{{Host: ".", Pref: 0}}, nil, MXStatusConflict, []string{"."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, existing, _ := classifyReceivingMX(tc.records, tc.err, sesInbound)
			if status != tc.want || !reflect.DeepEqual(existing, tc.existing) {
				t.Fatalf("got %s %v, want %s %v", status, existing, tc.want, tc.existing)
			}
		})
	}
}

func TestReceivingStatusIsLiveScopedAndCached(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	var org, other, domainID int64
	var domainUUID string
	if err := db.QueryRow(`INSERT INTO organizations(name,slug,updated_at) VALUES('Receiving status','receiving-status',now()) RETURNING id`).Scan(&org); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO organizations(name,slug,updated_at) VALUES('Other org','receiving-status-other',now()) RETURNING id`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO domains(org_id,name,status,verification_token,ses_verified,updated_at) VALUES($1,'Recv.Example.Test','active','token',true,now()) RETURNING id,uuid`, org).Scan(&domainID, &domainUUID); err != nil {
		t.Fatal(err)
	}
	resolver := &fakeMXResolver{records: []*net.MX{{Host: "mx.existing-provider.test.", Pref: 5}}}
	svc := &DomainService{db: db, cfg: &config.Config{EmailProvider: "ses", AWSRegion: "us-east-2"}, mxResolver: resolver}

	// Off: the record is still shown, existing MX is reported, nothing claims published.
	st, err := svc.GetReceivingStatus(ctx, org, domainUUID, false)
	if err != nil {
		t.Fatal(err)
	}
	want := ReceivingMXRecord{Type: "MX", Host: "recv.example.test", Name: "@", Value: "10 " + sesInbound, Priority: 10, Target: sesInbound}
	if st.Enabled || st.MXStatus != MXStatusNotEnabled || st.MXRecord != want || !reflect.DeepEqual(st.ExistingMX, []string{"mx.existing-provider.test"}) {
		t.Fatalf("receiving off: %+v", st)
	}
	if _, err := svc.GetReceivingStatus(ctx, other, domainUUID, false); !errors.Is(err, ErrDomainNotFound) {
		t.Fatalf("another organization read the domain: %v", err)
	}
	if _, err := svc.GetReceivingStatus(ctx, org, "not-a-uuid", false); !errors.Is(err, ErrDomainNotFound) {
		t.Fatalf("invalid uuid: %v", err)
	}

	// On: the saved record and the receiving region win over the config region.
	if _, err := db.Exec(`UPDATE domains SET receiving_enabled=true WHERE id=$1`, domainID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO receiving_configs(org_id,s3_bucket,s3_region,sns_topic_arn,ses_rule_set_name,webhook_secret,status,updated_at) VALUES($1,'b','eu-west-1','arn','rules','secret','active',now())`, org); err != nil {
		t.Fatal(err)
	}
	st, err = svc.GetReceivingStatus(ctx, org, domainUUID, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.MXRecord.Target != "inbound-smtp.eu-west-1.amazonaws.com" || st.MXStatus != MXStatusConflict {
		t.Fatalf("receiving region / conflict: %+v", st)
	}
	if resolver.calls != 1 {
		t.Fatalf("cached lookup repeated: %d calls", resolver.calls)
	}
	if _, err := db.Exec(`INSERT INTO domain_dns_records(domain_id,record_type,hostname,expected_value,verified) VALUES($1,'MX','recv.example.test','10 `+sesInbound+`',false)`, domainID); err != nil {
		t.Fatal(err)
	}
	resolver.records = nil
	resolver.err = &net.DNSError{Err: "no such host", IsNotFound: true}
	st, _ = svc.GetReceivingStatus(ctx, org, domainUUID, true)
	if st.MXRecord.Value != "10 "+sesInbound || st.MXStatus != MXStatusMissing || len(st.ExistingMX) != 0 || resolver.calls != 2 {
		t.Fatalf("missing after refresh: %+v calls=%d", st, resolver.calls)
	}
	resolver.records, resolver.err = []*net.MX{{Host: sesInbound + ".", Pref: 10}}, nil
	st, _ = svc.GetReceivingStatus(ctx, org, domainUUID, true)
	if st.MXStatus != MXStatusPublished {
		t.Fatalf("published: %+v", st)
	}
	// A failed lookup is unknown, never published, and is not cached.
	resolver.records, resolver.err = nil, &net.DNSError{Err: "server misbehaving", IsTemporary: true}
	st, _ = svc.GetReceivingStatus(ctx, org, domainUUID, true)
	if st.MXStatus != MXStatusUnknown {
		t.Fatalf("lookup error: %+v", st)
	}
	resolver.records, resolver.err = []*net.MX{{Host: sesInbound, Pref: 10}}, nil
	calls := resolver.calls
	st, _ = svc.GetReceivingStatus(ctx, org, domainUUID, false)
	if st.MXStatus != MXStatusPublished || resolver.calls != calls+1 {
		t.Fatalf("transient failure was cached: %+v", st)
	}
}

// Root MX automation is opt-in: written only while receiving is enabled and the
// zone holds no other root MX; a conflict leaves the zone unchanged.
func TestCloudflareReceivingMXOptIn(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	var org int64
	if err := db.QueryRow(`INSERT INTO organizations(name,slug,updated_at) VALUES('Receiving MX','receiving-mx',now()) RETURNING id`).Scan(&org); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		enabled   bool
		only      bool
		zoneRoot  string // Existing root records, as JSON objects.
		wantErr   error
		wantRoot  string // created, preserved, conflict or "" when not attempted.
		wantPosts int    // Root MX posts.
	}{
		{"off never offers root MX", false, false, ``, nil, "", 0},
		{"off refuses scoped MX", false, true, ``, ErrReceivingNotEnabled, "", 0},
		{"on adds root MX", true, false, ``, nil, "created", 1},
		{"on scoped adds only MX despite root SPF", true, true, `{"type":"TXT","name":"%s","content":"v=spf1 include:_spf.google.com ~all"},{"type":"A","name":"%s","content":"192.0.2.1"}`, nil, "created", 1},
		{"identical MX preserved", true, true, `{"type":"MX","name":"%s","content":"` + sesInbound + `","priority":10}`, nil, "preserved", 0},
		{"other root MX conflicts", true, false, `{"type":"MX","name":"%s","content":"aspmx.l.google.com","priority":1}`, nil, "conflict", 0},
		{"same host other priority conflicts", true, true, `{"type":"MX","name":"%s","content":"` + sesInbound + `","priority":20}`, nil, "conflict", 0},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := fmt.Sprintf("mx%d.receive.example.test", i)
			var id int64
			if err := db.QueryRow(`INSERT INTO domains(org_id,name,status,verification_token,ses_verified,email_provider,receiving_enabled,updated_at) VALUES($1,$2,'active','token',true,'ses',$3,now()) RETURNING id`, org, name, tc.enabled).Scan(&id); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO domain_dns_records(domain_id,record_type,hostname,expected_value,verified) VALUES($1,'MX',$2,'10 `+sesInbound+`',false),($1,'CNAME',$3,'tok.dkim.amazonses.com',false)`, id, name, "tok._domainkey."+name); err != nil {
				t.Fatal(err)
			}
			existing := tc.zoneRoot
			if existing != "" {
				existing = strings.ReplaceAll(existing, "%s", name)
			}
			var rootPosts, otherPosts int
			var rootPayload map[string]interface{}
			previous := http.DefaultTransport
			http.DefaultTransport = domainCloudflareTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "api.cloudflare.com" {
					return nil, fmt.Errorf("unexpected network request")
				}
				body := `{"success":true,"result":[],"result_info":{"total_pages":1}}`
				switch {
				case r.Method == "GET" && r.URL.Path == "/client/v4/zones/test-zone":
					body = fmt.Sprintf(`{"success":true,"result":{"id":"test-zone","name":%q}}`, name)
				case r.Method == "GET" && r.URL.Query().Get("name") == name:
					body = `{"success":true,"result":[` + existing + `],"result_info":{"total_pages":1}}`
				case r.Method == "POST":
					var payload map[string]interface{}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						return nil, err
					}
					if payload["name"] == name {
						rootPosts++
						rootPayload = payload
					} else {
						otherPosts++
					}
					body = `{"success":true,"result":{"id":"created"}}`
				case r.Method != "GET":
					t.Fatalf("automatic setup attempted destructive method %s", r.Method)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = previous })
			svc := &DomainService{db: db, cfg: &config.Config{EmailProvider: "ses", AWSRegion: "us-east-2"}}
			results, err := svc.AddDNSToCloudflareScoped(ctx, id, "test-token", "test-zone", tc.only)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if rootPosts != tc.wantPosts {
				t.Fatalf("root posts = %d, want %d", rootPosts, tc.wantPosts)
			}
			if rootPayload != nil && (rootPayload["type"] != "MX" || rootPayload["content"] != sesInbound || rootPayload["priority"] != float64(10)) {
				t.Fatalf("root MX payload: %v", rootPayload)
			}
			if tc.only && otherPosts != 0 {
				t.Fatalf("scoped receiving MX touched %d other records", otherPosts)
			}
			if !tc.only && tc.wantErr == nil && otherPosts != 1 {
				t.Fatalf("full setup lost its other records: %d posts", otherPosts)
			}
			root := ""
			mxResults := 0
			for _, result := range results {
				if result["type"] == "MX" && result["hostname"] == name {
					root, _ = result["status"].(string)
					mxResults++
					if root == "conflict" && (result["skipped"] != true || result["success"] != false || !strings.Contains(fmt.Sprint(result["reason"]), "another MX")) {
						t.Fatalf("conflict not reported: %v", result)
					}
				}
			}
			if root != tc.wantRoot || (tc.wantRoot != "" && mxResults != 1) {
				t.Fatalf("root MX status %q (%d results), want %q: %v", root, mxResults, tc.wantRoot, results)
			}
		})
	}
}
