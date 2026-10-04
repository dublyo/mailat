package provider

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
)

type dmarcTestResolver struct {
	txt     map[string][]string
	cnames  map[string]string
	err     error
	queried []string
}

func (r *dmarcTestResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	r.queried = append(r.queried, name)
	return r.txt[name], r.err
}
func (r *dmarcTestResolver) LookupCNAME(_ context.Context, name string) (string, error) {
	if value, ok := r.cnames[name]; ok {
		return value, r.err
	}
	return name, r.err
}

func TestDMARCRecordValidationPreservesExistingPolicies(t *testing.T) {
	for _, value := range []string{
		"v=DMARC1; p=none", DefaultDMARCValue, "v=DMARC1; p=reject; rua=mailto:Reports@example.test,https://reports.example.test/dmarc; adkim=s; aspf=r; sp=none; np=reject; psd=n; t=y; fo=1:d; ruf=mailto:failure@example.test!10m; xextension=opaque",
		"v=DMARC1", "v=DMARC1; rua=mailto:report@example.test;", "v=DMARC1; p=NONE; xunknown=a; xunknown=b; pct=legacy-unknown; ri=legacy-unknown", `"v=DMARC1; " "p=reject; rua=mailto:reports@example.test"`,
	} {
		t.Run(value, func(t *testing.T) {
			p, err := dmarcRecordSet([]string{"site-verification=dmarc-token", value})
			if err != nil || p == nil {
				t.Fatalf("valid policy rejected: %v", err)
			}
			if p.value != value {
				t.Fatal("existing policy was rewritten")
			}
			if p.tags["p"] == "" {
				t.Fatal("default policy missing")
			}
		})
	}
	for _, records := range [][]string{
		{"v=dmarc1; p=reject"}, {"v=DMARC2; p=reject"}, {"v=DMARC1suffix; p=reject"}, {"v=DMARC1; p=invalid"}, {"v=DMARC1; p=reject; p=none"}, {"v=DMARC1; p=reject; sp=none; sp=reject"}, {"v=DMARC1; sp=bad; rua=mailto:reports@example.test"}, {"v=DMARC1; adkim=bad"}, {"v=DMARC1; fo=0:1"}, {"v=DMARC1; rua=not-a-uri"}, {"v=DMARC1; p=reject", "v=DMARC1; p=none"}, {"v=DMARC1; p=none", "v=DMARC1; p=none"}, {"p=reject; v=DMARC1"}, {"p=reject"}, {`"v=DMARC1; p=none`},
	} {
		t.Run(fmt.Sprint(records), func(t *testing.T) {
			if p, err := dmarcRecordSet(records); err == nil {
				t.Fatalf("ambiguous/invalid policy accepted: %#v", p)
			}
		})
	}
	p, err := dmarcRecordSet([]string{"google-site-verification=dmarc-service", "unrelated=value"})
	if err != nil || p != nil {
		t.Fatal("unrelated TXT treated as DMARC")
	}
}

func TestDMARCDiscoveryAndBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, domain, status, policy, source string
		txt                                  map[string][]string
		cnames                               map[string]string
		err                                  error
	}{
		{name: "absent", domain: "example.test", status: "absent"},
		{name: "direct none", domain: "example.test", status: "existing", policy: "none", source: "_dmarc.example.test", txt: map[string][]string{"_dmarc.example.test": {"v=DMARC1; p=none"}}},
		{name: "missing p defaults none", domain: "example.test", status: "existing", policy: "none", source: "_dmarc.example.test", txt: map[string][]string{"_dmarc.example.test": {"v=DMARC1"}}},
		{name: "direct beats parent", domain: "mail.example.test", status: "existing", policy: "quarantine", source: "_dmarc.mail.example.test", txt: map[string][]string{"_dmarc.mail.example.test": {DefaultDMARCValue}, "_dmarc.example.test": {"v=DMARC1; p=reject"}}},
		{name: "highest ancestor", domain: "a.mail.example.test", status: "inherited", policy: "none", source: "_dmarc.example.test", txt: map[string][]string{"_dmarc.mail.example.test": {"v=DMARC1; p=reject"}, "_dmarc.example.test": {"v=DMARC1; p=quarantine; sp=none"}}},
		{name: "organizational boundary", domain: "a.mail.example.test", status: "inherited", policy: "reject", source: "_dmarc.mail.example.test", txt: map[string][]string{"_dmarc.mail.example.test": {"v=DMARC1; p=reject; psd=n"}, "_dmarc.example.test": {"v=DMARC1; p=none"}}},
		{name: "PSD uses organization below", domain: "a.example.test", status: "inherited", policy: "none", source: "_dmarc.example.test", txt: map[string][]string{"_dmarc.example.test": {"v=DMARC1; p=none"}, "_dmarc.test": {"v=DMARC1; p=reject; psd=y"}}},
		{name: "PSD fallback", domain: "a.example.test", status: "inherited", policy: "reject", source: "_dmarc.test", txt: map[string][]string{"_dmarc.test": {"v=DMARC1; p=reject; psd=y"}}},
		{name: "test mode direct", domain: "example.test", status: "existing", policy: "quarantine", source: "_dmarc.example.test", txt: map[string][]string{"_dmarc.example.test": {"v=DMARC1; p=reject; t=y"}}},
		{name: "test mode inherited", domain: "a.example.test", status: "inherited", policy: "none", source: "_dmarc.example.test", txt: map[string][]string{"_dmarc.example.test": {"v=DMARC1; p=reject; sp=quarantine; t=y"}}},
		{name: "np needs existence review", domain: "a.example.test", status: "conflict", policy: "none", source: "_dmarc.example.test", txt: map[string][]string{"_dmarc.example.test": {"v=DMARC1; p=none; np=reject"}}},
		{name: "delegation resolved", domain: "example.test", status: "existing", policy: "reject", source: "_dmarc.example.test", txt: map[string][]string{"_dmarc.example.test": {"v=DMARC1; p=reject"}}, cnames: map[string]string{"_dmarc.example.test": "policy.provider.test."}},
		{name: "delegation unresolved", domain: "example.test", status: "conflict", source: "_dmarc.example.test", cnames: map[string]string{"_dmarc.example.test": "policy.provider.test."}},
		{name: "timeout", domain: "example.test", status: "unknown", err: &net.DNSError{IsTimeout: true}},
		{name: "server failure", domain: "example.test", status: "unknown", err: &net.DNSError{IsTemporary: true}},
		{name: "confirmed missing", domain: "example.test", status: "absent", err: &net.DNSError{IsNotFound: true}},
		{name: "multiple direct", domain: "example.test", status: "conflict", source: "_dmarc.example.test", txt: map[string][]string{"_dmarc.example.test": {"v=DMARC1; p=none", "v=DMARC1; p=reject"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &dmarcTestResolver{txt: tc.txt, cnames: tc.cnames, err: tc.err}
			got := InspectDMARC(context.Background(), tc.domain, r)
			if got.Status != tc.status || got.Policy != tc.policy || got.PolicyHostname != tc.source {
				t.Fatalf("got %+v", got)
			}
			if got.CanCreate != (tc.status == "absent") {
				t.Fatal("unsafe creation flag")
			}
			if got.Verified != (tc.status == "existing" || tc.status == "inherited") {
				t.Fatal("wrong verification flag")
			}
			if got.Reason == "" || got.CheckedAt.IsZero() || got.SuggestedValue != DefaultDMARCValue {
				t.Fatal("missing status metadata")
			}
		})
	}
	r := &dmarcTestResolver{}
	InspectDMARC(context.Background(), "a.b.c.d.e.f.g.h.i.j.example.test", r)
	expected := []string{"_dmarc.a.b.c.d.e.f.g.h.i.j.example.test", "_dmarc.f.g.h.i.j.example.test", "_dmarc.g.h.i.j.example.test", "_dmarc.h.i.j.example.test", "_dmarc.i.j.example.test", "_dmarc.j.example.test", "_dmarc.example.test", "_dmarc.test"}
	if !reflect.DeepEqual(r.queried, expected) {
		t.Fatalf("unbounded/incorrect treewalk: %v", r.queried)
	}
}

func TestDMARCDiscoveryDeadlineAndMalformedNames(t *testing.T) {
	for _, domain := range []string{"", "name/@example.test", strings.Repeat("a", 254)} {
		r := &dmarcTestResolver{}
		got := InspectDMARC(context.Background(), domain, r)
		if got.CanCreate || len(r.queried) != 0 {
			t.Fatal("invalid domain queried")
		}
	}
}
