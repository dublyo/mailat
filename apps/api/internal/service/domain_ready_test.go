package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/dublyo/mailat/api/internal/testutil"
)

// sesStatusProvider answers SES verification checks without AWS.
type sesStatusProvider struct {
	domainOnboardingProvider
	verified bool
	err      error
}

func (p *sesStatusProvider) CheckDomainVerification(_ context.Context, domain string) (*provider.DomainIdentity, error) {
	if p.err != nil {
		return nil, p.err
	}
	return &provider.DomainIdentity{Domain: domain, Verified: p.verified}, nil
}

type readyFixture struct {
	db                *sql.DB
	org, owner, admin int64
	domain            int64
	domainUUID, name  string
	svc               *DomainService
	ses               *sesStatusProvider
	setup             *fakeSendingSetup
	dmarc             *serviceDMARCResolver
}

// newReadyFixture adds a pending SES domain created by an admin, with fakes
// for SES, the SNS/S3 setup and DNS. Automation runs inline.
func newReadyFixture(t *testing.T, name string) *readyFixture {
	t.Helper()
	db := testutil.Database(t)
	f := &readyFixture{db: db, name: name}
	if err := db.QueryRow(`INSERT INTO organizations(name,slug,updated_at) VALUES('Acme',$1,now()) RETURNING id`, name).Scan(&f.org); err != nil {
		t.Fatal(err)
	}
	for role, id := range map[string]*int64{"owner": &f.owner, "admin": &f.admin} {
		if err := db.QueryRow(`INSERT INTO users(org_id,email,password_hash,role,updated_at) VALUES($1,$2,'unused',$3,now()) RETURNING id`, f.org, role+"@people.test", role).Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	f.ses = &sesStatusProvider{}
	f.setup = &fakeSendingSetup{db: db, org: f.org}
	f.dmarc = &serviceDMARCResolver{txt: map[string][]string{}}
	f.svc = &DomainService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: f.ses,
		sendingProvider: f.setup, dmarcResolver: f.dmarc, mxResolver: &fakeMXResolver{}, async: func(run func()) { run() }}
	d, err := f.svc.CreateDomainBy(context.Background(), f.org, f.admin, &model.CreateDomainRequest{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	f.domain, f.domainUUID = d.ID, d.UUID
	return f
}

func (f *readyFixture) identities(t *testing.T) []string {
	t.Helper()
	rows, err := f.db.Query(`SELECT i.email||':'||u.role||':'||i.can_send||':'||i.can_receive||':'||i.is_catch_all||':'||i.is_default FROM identities i JOIN users u ON u.id=i.user_id WHERE i.domain_id=$1 ORDER BY i.id`, f.domain)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// verify makes the domain active (as VerifyDNS would after its DNS checks)
// and then runs the SES status check, which is the transition under test.
func (f *readyFixture) verify(t *testing.T) {
	t.Helper()
	if _, err := f.db.Exec(`UPDATE domains SET status='active',verified_at=now() WHERE id=$1`, f.domain); err != nil {
		t.Fatal(err)
	}
	f.ses.verified = true
	if _, err := f.svc.CheckSESVerificationStatus(context.Background(), f.domain, true); err != nil {
		t.Fatal(err)
	}
}

func readinessItem(t *testing.T, r *DomainReadiness, key string) DomainReadinessItem {
	t.Helper()
	for _, it := range r.Items {
		if it.Key == key {
			return it
		}
	}
	t.Fatalf("readiness item %s missing: %+v", key, r.Items)
	return DomainReadinessItem{}
}

func TestReadyAutomationRunsOnceOnVerification(t *testing.T) {
	f := newReadyFixture(t, "fresh.example.test")
	ctx := context.Background()
	var createdBy sql.NullInt64
	if err := f.db.QueryRow(`SELECT created_by FROM domains WHERE id=$1`, f.domain).Scan(&createdBy); err != nil || createdBy.Int64 != f.admin {
		t.Fatal("creator not recorded", createdBy, err)
	}
	// Not yet SES verified: nothing runs.
	if _, err := f.svc.CheckSESVerificationStatus(ctx, f.domain, true); err != nil {
		t.Fatal(err)
	}
	if f.setup.creates != 0 || len(f.identities(t)) != 0 {
		t.Fatal("automation ran before verification")
	}
	f.verify(t)
	if f.setup.creates != 1 || f.setup.subscribes != 1 {
		t.Fatal("sending setup did not run once", f.setup.creates, f.setup.subscribes)
	}
	got := f.identities(t)
	if len(got) != 1 || got[0] != "noreply@fresh.example.test:admin:true:true:false:true" {
		t.Fatal("owner identity", got)
	}
	var name string
	if err := f.db.QueryRow(`SELECT display_name FROM identities WHERE domain_id=$1`, f.domain).Scan(&name); err != nil || name != "Acme" {
		t.Fatal("display name", name, err)
	}
	// Re-checks are idempotent: no second setup, no second identity.
	f.svc.afterDomainCheck(f.domain)
	if _, err := f.svc.CheckSESVerificationStatus(ctx, f.domain, true); err != nil {
		t.Fatal(err)
	}
	if f.setup.creates != 1 || f.setup.subscribes != 1 || len(f.identities(t)) != 1 {
		t.Fatal("automation ran twice")
	}
	// DNS and receiving are never changed.
	var receiving bool
	var rootMX int
	if err := f.db.QueryRow(`SELECT receiving_enabled,(SELECT count(*) FROM domain_dns_records WHERE domain_id=$1 AND record_type='MX' AND hostname=$2) FROM domains WHERE id=$1`, f.domain, f.name).Scan(&receiving, &rootMX); err != nil || receiving || rootMX != 0 {
		t.Fatal("receiving or root MX changed", receiving, rootMX, err)
	}
	r, err := f.svc.GetDomainReadiness(ctx, f.org, f.admin, f.domainUUID, ReadinessOptions{Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	if it := readinessItem(t, r, "sending_identity"); it.Status != ReadinessOK || it.Value != "noreply@fresh.example.test" {
		t.Fatal("admin identity not ready", it)
	}
	if it := readinessItem(t, r, "sending_resources"); it.Status != ReadinessPending || it.State != "awaiting_confirmation" || it.Fix != "" || r.AutomaticSetup {
		t.Fatal("pending subscription offers no fix", it, r.AutomaticSetup)
	}
	// The owner did not add the domain and gets no identity; the checklist says so.
	r, err = f.svc.GetDomainReadiness(ctx, f.org, f.owner, f.domainUUID, ReadinessOptions{Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	if it := readinessItem(t, r, "sending_identity"); it.Status != ReadinessMissing || it.Fix != "create_identity" || r.SuggestedIdentity != "" ||
		it.Detail != "You have no sending identity on fresh.example.test. Add one with Add Identity." {
		t.Fatal("owner identity item", it, r.SuggestedIdentity)
	}
}

func TestReadyAutomationRecordsSetupFailureAndFallsBackToOwner(t *testing.T) {
	f := newReadyFixture(t, "fail.example.test")
	ctx := context.Background()
	// The adder was removed: the owner gets the identity. Setup has no provider
	// and an http API_URL, so it fails and is recorded, not retried.
	if _, err := f.db.Exec(`UPDATE users SET status='disabled',removed_at=now() WHERE id=$1`, f.admin); err != nil {
		t.Fatal(err)
	}
	f.svc.sendingProvider = nil
	f.svc.cfg = &config.Config{EmailProvider: "ses", AWSAccessKeyID: "test", APIUrl: "http://api.example.test", DisableAppLimits: true}
	f.verify(t)
	var reason string
	var ready bool
	if err := f.db.QueryRow(`SELECT sending_setup_error,sending_feedback_ready FROM domains WHERE id=$1`, f.domain).Scan(&reason, &ready); err != nil || ready ||
		!strings.Contains(reason, "API_URL must be a public HTTPS URL") {
		t.Fatal("setup failure not recorded", reason, ready, err)
	}
	status, err := f.svc.GetSendingStatus(ctx, f.org, f.domainUUID)
	if err != nil || status.Reason != reason {
		t.Fatal("sending status does not show the failure", status, err)
	}
	got := f.identities(t)
	if len(got) != 1 || got[0] != "noreply@fail.example.test:owner:true:true:false:true" {
		t.Fatal("owner fallback identity", got)
	}
	r, err := f.svc.GetDomainReadiness(ctx, f.org, f.owner, f.domainUUID, ReadinessOptions{Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	if it := readinessItem(t, r, "sending_resources"); it.Status != ReadinessAttention || it.Detail != reason || it.Fix != "setup_sending" {
		t.Fatal("needs attention item", it)
	}
	// A manual Set up still works afterwards.
	f.svc.sendingProvider = f.setup
	if s, err := f.svc.EnsureDomainSendingResources(ctx, f.org, f.domain); err != nil || !s.StorageReady || s.Reason == reason {
		t.Fatal("manual setup after failure", s, err)
	}
}

func TestReadyAutomationForeignTopicLeavesNeedsAttention(t *testing.T) {
	f := newReadyFixture(t, "foreign.example.test")
	f.setup.feedbackErr = provider.ErrSendingFeedbackConflict
	f.verify(t)
	status, err := f.svc.GetSendingStatus(context.Background(), f.org, f.domainUUID)
	if err != nil || status.FeedbackReady || status.Reason != provider.ErrSendingFeedbackConflict.Error() {
		t.Fatal("foreign topic not left as needs attention", status, err)
	}
}

func TestReadyAutomationSkipsTakenAddressesAndCaps(t *testing.T) {
	cases := map[string]func(t *testing.T, f *readyFixture){
		"existing identity": func(t *testing.T, f *readyFixture) {
			if _, err := f.db.Exec(`INSERT INTO identities(user_id,domain_id,email,display_name,updated_at) VALUES($1,$2,'noreply@'||$3,'Other',now())`, f.owner, f.domain, f.name); err != nil {
				t.Fatal(err)
			}
		},
		"send-as alias": func(t *testing.T, f *readyFixture) {
			var id int64
			if err := f.db.QueryRow(`INSERT INTO identities(user_id,domain_id,email,display_name,updated_at) VALUES($1,$2,'sales@'||$3,'Sales',now()) RETURNING id`, f.owner, f.domain, f.name).Scan(&id); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Exec(`INSERT INTO identity_send_aliases(identity_id,address) VALUES($1,'noreply@'||$2)`, id, f.name); err != nil {
				t.Fatal(err)
			}
		},
		"pending invite": func(t *testing.T, f *readyFixture) {
			if _, err := f.db.Exec(`INSERT INTO org_invites(org_id,email,role,token_hash,expires_at) VALUES($1,'noreply@'||$2,'member',repeat('a',64),now()+interval '1 day')`, f.org, f.name); err != nil {
				t.Fatal(err)
			}
		},
		"another person's login": func(t *testing.T, f *readyFixture) {
			if _, err := f.db.Exec(`INSERT INTO users(org_id,email,password_hash,role,updated_at) VALUES($1,'noreply@'||$2,'unused','member',now())`, f.org, f.name); err != nil {
				t.Fatal(err)
			}
		},
		"identity cap": func(t *testing.T, f *readyFixture) {
			f.svc.cfg = &config.Config{EmailProvider: "ses"}
			if _, err := f.db.Exec(`UPDATE organizations SET max_identities=1 WHERE id=$1`, f.org); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Exec(`INSERT INTO identities(user_id,domain_id,email,display_name,updated_at) VALUES($1,$2,'first@'||$3,'First',now())`, f.owner, f.domain, f.name); err != nil {
				t.Fatal(err)
			}
		},
		"adder already has one": func(t *testing.T, f *readyFixture) {
			if _, err := f.db.Exec(`INSERT INTO identities(user_id,domain_id,email,display_name,can_send,updated_at) VALUES($1,$2,'hello@'||$3,'Hello',false,now())`, f.admin, f.domain, f.name); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			f := newReadyFixture(t, strings.ReplaceAll(name, " ", "-")+".skip.test")
			prepare(t, f)
			before := f.identities(t)
			f.verify(t)
			if after := f.identities(t); len(after) != len(before) {
				t.Fatal("identity created although it should be skipped", before, after)
			}
			if f.setup.creates != 1 {
				t.Fatal("sending setup must still run")
			}
		})
	}
}

func TestReadyAutomationNeverAppliesToAlreadyReadyDomainsOrSMTP(t *testing.T) {
	f := newReadyFixture(t, "legacy.example.test")
	// The migration stamps already-ready domains, so a later check does nothing.
	if _, err := f.db.Exec(`UPDATE domains SET status='active',ses_verified=true,ready_automation_at=now() WHERE id=$1`, f.domain); err != nil {
		t.Fatal(err)
	}
	f.verify(t)
	if f.setup.creates != 0 || len(f.identities(t)) != 0 {
		t.Fatal("automation ran for an already-ready domain")
	}
	g := newReadyFixture(t, "smtp.example.test")
	g.svc.cfg = &config.Config{EmailProvider: "smtp", DisableAppLimits: true}
	g.verify(t)
	if g.setup.creates != 0 || len(g.identities(t)) != 0 {
		t.Fatal("automation ran in SMTP mode")
	}
}

func TestDomainReadinessChecklist(t *testing.T) {
	f := newReadyFixture(t, "check.example.test")
	ctx := context.Background()
	// The DNS answers change between reads below; Re-check (refresh) must see them.
	previousMinAge := mxRefreshMinAge
	mxRefreshMinAge = 0
	t.Cleanup(func() { mxRefreshMinAge = previousMinAge })
	if _, err := f.svc.GetDomainReadiness(ctx, f.org+1, f.owner, f.domainUUID, ReadinessOptions{Admin: true}); !errors.Is(err, ErrDomainNotFound) {
		t.Fatal("another organization read the checklist", err)
	}
	if _, err := f.svc.GetDomainReadiness(ctx, f.org, f.owner, "not-a-uuid", ReadinessOptions{Admin: true}); !errors.Is(err, ErrDomainNotFound) {
		t.Fatal(err)
	}
	r, err := f.svc.GetDomainReadiness(ctx, f.org, f.owner, f.domainUUID, ReadinessOptions{Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, it := range r.Items {
		keys = append(keys, it.Key+"="+it.Status+"/"+it.Fix)
	}
	want := "verified=missing/verify dmarc=missing/dmarc sending_resources=missing/ sending_identity=missing/ receiving=off/receiving"
	if it := readinessItem(t, r, "sending_resources"); !strings.Contains(it.Detail, "after the domain is verified") {
		t.Fatal("unverified sending resources must say why there is no fix", it)
	}
	if it := readinessItem(t, r, "sending_identity"); !strings.Contains(it.Detail, "after the domain is verified") {
		t.Fatal("unverified identity must say why there is no fix", it)
	}
	if it := readinessItem(t, r, "receiving"); it.Value != "" {
		t.Fatal("no copyable root MX while receiving is off", it)
	}
	if strings.Join(keys, " ") != want || r.Ready || r.SuggestedIdentity != "noreply@check.example.test" {
		t.Fatal("pending checklist", strings.Join(keys, " "), r.Ready, r.SuggestedIdentity)
	}
	if it := readinessItem(t, r, "dmarc"); it.Value != provider.DefaultDMARCValue {
		t.Fatal("copyable DMARC record", it)
	}
	if !readinessItem(t, r, "receiving").Optional {
		t.Fatal("receiving must be optional")
	}

	// Verified with a published policy, confirmed feedback and an own identity: ready,
	// even though receiving is still off.
	f.verify(t)
	f.dmarc.txt["_dmarc.check.example.test"] = []string{"v=DMARC1; p=quarantine"}
	if _, err = f.db.Exec(`UPDATE sending_configs SET status='active' WHERE org_id=$1`, f.org); err != nil {
		t.Fatal(err)
	}
	r, err = f.svc.GetDomainReadiness(ctx, f.org, f.admin, f.domainUUID, ReadinessOptions{Admin: true, Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	keys = nil
	for _, it := range r.Items {
		keys = append(keys, it.Key+"="+it.Status+"/"+it.State)
	}
	want = "verified=ok/verified dmarc=ok/published sending_resources=ok/ready sending_identity=ok/present receiving=off/not_enabled"
	if strings.Join(keys, " ") != want || !r.Ready || r.SuggestedIdentity != "" {
		t.Fatal("ready checklist", strings.Join(keys, " "), r.Ready, r.SuggestedIdentity)
	}
	// An inherited policy also counts; a member without an identity is not ready.
	delete(f.dmarc.txt, "_dmarc.check.example.test")
	f.dmarc.txt["_dmarc.example.test"] = []string{"v=DMARC1; p=reject"}
	var member int64
	if err = f.db.QueryRow(`INSERT INTO users(org_id,email,password_hash,role,updated_at) VALUES($1,'member@people.test','unused','member',now()) RETURNING id`, f.org).Scan(&member); err != nil {
		t.Fatal(err)
	}
	r, err = f.svc.GetDomainReadiness(ctx, f.org, member, f.domainUUID, ReadinessOptions{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if it := readinessItem(t, r, "dmarc"); it.Status != ReadinessOK || it.State != "inherited" {
		t.Fatal("inherited DMARC", it)
	}
	if it := readinessItem(t, r, "sending_identity"); r.Ready || it.Status != ReadinessMissing || it.Fix != "create_identity" ||
		it.Detail != "You have no sending identity on check.example.test. Ask an organization owner or admin to add one for you." {
		t.Fatal("member without identity", it, r.Ready)
	}
}

// An API send from a domain where the caller has no identity says how to fix it.
func TestNoSendingIdentityErrorSaysWhatToDo(t *testing.T) {
	db := testutil.Database(t)
	org, _, _, _, _, _, _ := sendAsFixture(t, db)
	var admin int64
	if err := db.QueryRow(`INSERT INTO users(org_id,email,password_hash,role,updated_at) VALUES($1,'noid@people.test','unused','admin',now()) RETURNING id`, org).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	svc := &TransactionalService{db: db, cfg: &config.Config{EmailProvider: "ses", DisableAppLimits: true}, emailProvider: &mailboxTestProvider{}}
	_, err := svc.SendEmailForUser(context.Background(), org, SendActor{UserID: admin, Admin: true}, &model.SendEmailRequest{From: "noreply@send.test", To: []string{"r@external.test"}, Subject: "s", Text: "t", IdempotencyKey: "no-identity-key"})
	want := "you have no sending identity on send.test; add one (e.g. noreply@send.test) under Domains → send.test → Add identity"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
	// A member cannot add identities, so the error says whom to ask.
	var member int64
	if err := db.QueryRow(`INSERT INTO users(org_id,email,password_hash,role,updated_at) VALUES($1,'noid-member@people.test','unused','member',now()) RETURNING id`, org).Scan(&member); err != nil {
		t.Fatal(err)
	}
	_, err = svc.SendEmailForUser(context.Background(), org, SendActor{UserID: member}, &model.SendEmailRequest{From: "noreply@send.test", To: []string{"r@external.test"}, Subject: "s", Text: "t", IdempotencyKey: "no-identity-member-key"})
	want = "you have no sending identity on send.test; ask an organization owner or admin to add one for you (e.g. noreply@send.test) under Domains → send.test → Add identity"
	if err == nil || err.Error() != want {
		t.Fatalf("member: got %v, want %q", err, want)
	}
}

// Members and read-only keys reach GET ses-status; only an owner or admin
// session lets a verified result start the one-time setup.
func TestSESStatusStartsAutomationOnlyForAdmins(t *testing.T) {
	f := newReadyFixture(t, "readonly.example.test")
	if _, err := f.db.Exec(`UPDATE domains SET status='active',verified_at=now() WHERE id=$1`, f.domain); err != nil {
		t.Fatal(err)
	}
	f.ses.verified = true
	if _, err := f.svc.CheckSESVerificationStatus(context.Background(), f.domain, false); err != nil {
		t.Fatal(err)
	}
	var claimed bool
	if err := f.db.QueryRow(`SELECT ready_automation_at IS NOT NULL FROM domains WHERE id=$1`, f.domain).Scan(&claimed); err != nil || claimed {
		t.Fatal("a read-only check claimed the automation", claimed, err)
	}
	if f.setup.creates != 0 || len(f.identities(t)) != 0 {
		t.Fatal("a read-only check ran the setup")
	}
	f.verify(t)
	if f.setup.creates != 1 || len(f.identities(t)) != 1 {
		t.Fatal("an admin check must still run it")
	}
}

// While the background setup runs, the checklist says so instead of offering
// fixes that would race it, and shows the result once it is done.
func TestReadinessShowsAutomaticSetupInProgress(t *testing.T) {
	f := newReadyFixture(t, "inflight.example.test")
	ctx := context.Background()
	var queued []func()
	f.svc.async = func(run func()) { queued = append(queued, run) }
	f.verify(t)
	if len(queued) == 0 {
		t.Fatal("automation was not started")
	}
	check := func(user int64, admin bool) *DomainReadiness {
		t.Helper()
		r, err := f.svc.GetDomainReadiness(ctx, f.org, user, f.domainUUID, ReadinessOptions{Admin: admin})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	// Claimed when the check returned, still running: reported as running.
	r := check(f.admin, true)
	res, id := readinessItem(t, r, "sending_resources"), readinessItem(t, r, "sending_identity")
	if !r.AutomaticSetup || res.State != "automatic_setup" || res.Fix != "" || id.State != "automatic_setup" || id.Fix != "" {
		t.Fatal("in-progress state", r.AutomaticSetup, res, id)
	}
	// The owner did not add the domain: their identity is not being created.
	if id := readinessItem(t, check(f.owner, true), "sending_identity"); id.Status != ReadinessMissing || id.Fix != "create_identity" {
		t.Fatal("owner identity is not automatic", id)
	}
	for _, run := range queued {
		run()
	}
	r = check(f.admin, true)
	res, id = readinessItem(t, r, "sending_resources"), readinessItem(t, r, "sending_identity")
	if r.AutomaticSetup || res.State != "awaiting_confirmation" || id.Status != ReadinessOK {
		t.Fatal("finished state", r.AutomaticSetup, res, id)
	}
}

// The person is re-checked under the row lock: removed, disabled or turned
// into a mailbox user since they were chosen means no identity.
func TestOwnerIdentityRechecksUserUnderLock(t *testing.T) {
	f := newReadyFixture(t, "recheck.example.test")
	ctx := context.Background()
	for _, change := range []string{`UPDATE users SET role='mailbox' WHERE id=$1`, `UPDATE users SET status='disabled' WHERE id=$1`, `UPDATE users SET removed_at=now() WHERE id=$1`} {
		var user int64
		if err := f.db.QueryRow(`INSERT INTO users(org_id,email,password_hash,role,updated_at) VALUES($1,'u'||floor(random()*1e9)::text||'@people.test','unused','admin',now()) RETURNING id`, f.org).Scan(&user); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Exec(change, user); err != nil {
			t.Fatal(err)
		}
		if err := f.svc.createOwnerIdentity(ctx, f.org, f.domain, f.name, user); err != nil {
			t.Fatal(change, err)
		}
		if got := f.identities(t); len(got) != 0 {
			t.Fatal(change, "gave an ineligible user an identity", got)
		}
	}
	if err := f.svc.createOwnerIdentity(ctx, f.org, f.domain, f.name, f.admin); err != nil || len(f.identities(t)) != 1 {
		t.Fatal("an eligible user gets the identity", err, f.identities(t))
	}
}

// Another person's login address is never suggested or given away; the
// caller's own login address is fine.
func TestSuggestedIdentitySkipsOtherLogins(t *testing.T) {
	f := newReadyFixture(t, "login.example.test")
	ctx := context.Background()
	var user int64
	if err := f.db.QueryRow(`INSERT INTO users(org_id,email,password_hash,role,updated_at) VALUES($1,'noreply@login.example.test','unused','admin',now()) RETURNING id`, f.org).Scan(&user); err != nil {
		t.Fatal(err)
	}
	r, err := f.svc.GetDomainReadiness(ctx, f.org, f.owner, f.domainUUID, ReadinessOptions{Admin: true})
	if err != nil || r.SuggestedIdentity != "" {
		t.Fatal("suggested another person's login", r, err)
	}
	if r, err = f.svc.GetDomainReadiness(ctx, f.org, user, f.domainUUID, ReadinessOptions{Admin: true}); err != nil || r.SuggestedIdentity != "noreply@login.example.test" {
		t.Fatal("own login address should be suggested", r, err)
	}
}

// DMARC answers are cached briefly so every card does not run a live lookup;
// a refresh re-checks.
func TestReadinessCachesDMARC(t *testing.T) {
	f := newReadyFixture(t, "cache.example.test")
	ctx := context.Background()
	read := func(refresh bool) DomainReadinessItem {
		t.Helper()
		r, err := f.svc.GetDomainReadiness(ctx, f.org, f.owner, f.domainUUID, ReadinessOptions{Admin: true, Refresh: refresh})
		if err != nil {
			t.Fatal(err)
		}
		return readinessItem(t, r, "dmarc")
	}
	if it := read(false); it.State != "missing" {
		t.Fatal(it)
	}
	calls := f.dmarc.calls
	f.dmarc.txt["_dmarc.cache.example.test"] = []string{"v=DMARC1; p=reject"}
	if it := read(false); it.State != "missing" || f.dmarc.calls != calls {
		t.Fatal("second read was not cached", it, f.dmarc.calls, calls)
	}
	f.svc.dmarcCache.mu.Lock()
	e := f.svc.dmarcCache.entries["cache.example.test"]
	e.at = e.at.Add(-mxRefreshMinAge - time.Second)
	f.svc.dmarcCache.entries["cache.example.test"] = e
	f.svc.dmarcCache.mu.Unlock()
	if it := read(true); it.State != "published" {
		t.Fatal("refresh did not re-check", it)
	}
}

// Only a run claimed within the window is reported as running: a recently
// verified domain that was never claimed, or one the migrations stamped, shows
// the normal missing state with fixes.
func TestReadinessAutomaticSetupNeedsARecentClaim(t *testing.T) {
	f := newReadyFixture(t, "unclaimed.example.test")
	ctx := context.Background()
	for _, stamp := range []string{"NULL", "timestamptz 'epoch'", "now() - interval '3 minutes'"} {
		if _, err := f.db.Exec(`UPDATE domains SET status='active',ses_verified=true,verified_at=now(),updated_at=now(),ready_automation_at=`+stamp+` WHERE id=$1`, f.domain); err != nil {
			t.Fatal(err)
		}
		r, err := f.svc.GetDomainReadiness(ctx, f.org, f.admin, f.domainUUID, ReadinessOptions{Admin: true})
		if err != nil {
			t.Fatal(err)
		}
		res, id := readinessItem(t, r, "sending_resources"), readinessItem(t, r, "sending_identity")
		if r.AutomaticSetup || res.State != "not_set_up" || res.Fix != "setup_sending" || id.State != "missing" || id.Fix != "create_identity" {
			t.Fatal(stamp, "reported as automatic", r.AutomaticSetup, res, id)
		}
	}
}

// When the claimed run would skip the chosen user's noreply identity, their
// item shows the normal missing state with its fix, not "Creating…".
func TestReadinessAutomaticIdentityOnlyWhenItWillBeCreated(t *testing.T) {
	cases := map[string]func(t *testing.T, f *readyFixture){
		"identity cap": func(t *testing.T, f *readyFixture) {
			f.svc.cfg = &config.Config{EmailProvider: "ses"}
			if _, err := f.db.Exec(`UPDATE organizations SET max_identities=1 WHERE id=$1`, f.org); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Exec(`INSERT INTO identities(user_id,domain_id,email,display_name,updated_at) VALUES($1,$2,'first@'||$3,'First',now())`, f.owner, f.domain, f.name); err != nil {
				t.Fatal(err)
			}
		},
		"identity that cannot send": func(t *testing.T, f *readyFixture) {
			if _, err := f.db.Exec(`INSERT INTO identities(user_id,domain_id,email,display_name,can_send,updated_at) VALUES($1,$2,'hello@'||$3,'Hello',false,now())`, f.admin, f.domain, f.name); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			f := newReadyFixture(t, strings.ReplaceAll(name, " ", "-")+".auto.test")
			prepare(t, f)
			var queued []func()
			f.svc.async = func(run func()) { queued = append(queued, run) }
			f.verify(t)
			if len(queued) == 0 {
				t.Fatal("automation was not claimed")
			}
			r, err := f.svc.GetDomainReadiness(context.Background(), f.org, f.admin, f.domainUUID, ReadinessOptions{Admin: true})
			if err != nil {
				t.Fatal(err)
			}
			id := readinessItem(t, r, "sending_identity")
			if !r.AutomaticSetup || id.State != "missing" || id.Fix != "create_identity" || strings.Contains(id.Detail, "automatically") {
				t.Fatal("skipped identity shown as automatic", r.AutomaticSetup, id)
			}
		})
	}
	// A chosen user who is no longer eligible gets nothing either.
	f := newReadyFixture(t, "ineligible.auto.test")
	var queued []func()
	f.svc.async = func(run func()) { queued = append(queued, run) }
	f.verify(t)
	if _, err := f.db.Exec(`UPDATE users SET role='mailbox' WHERE id=$1`, f.admin); err != nil {
		t.Fatal(err)
	}
	// readyAutomationUser now picks the owner, who did not add the domain but is eligible.
	r, err := f.svc.GetDomainReadiness(context.Background(), f.org, f.owner, f.domainUUID, ReadinessOptions{Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	if id := readinessItem(t, r, "sending_identity"); id.State != "automatic_setup" {
		t.Fatal("the eligible fallback owner gets the identity", id)
	}
	if _, err := f.db.Exec(`UPDATE users SET status='disabled' WHERE id=$1`, f.owner); err != nil {
		t.Fatal(err)
	}
	r, err = f.svc.GetDomainReadiness(context.Background(), f.org, f.owner, f.domainUUID, ReadinessOptions{Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	if id := readinessItem(t, r, "sending_identity"); id.State != "missing" {
		t.Fatal("ineligible user shown as automatic", id)
	}
}

// Before verification a member is told an admin adds identities, not that
// they can.
func TestReadinessMemberIdentityBeforeVerification(t *testing.T) {
	f := newReadyFixture(t, "member.example.test")
	var member int64
	if err := f.db.QueryRow(`INSERT INTO users(org_id,email,password_hash,role,updated_at) VALUES($1,'m@people.test','unused','member',now()) RETURNING id`, f.org).Scan(&member); err != nil {
		t.Fatal(err)
	}
	r, err := f.svc.GetDomainReadiness(context.Background(), f.org, member, f.domainUUID, ReadinessOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if id := readinessItem(t, r, "sending_identity"); id.Status != ReadinessMissing || id.Fix != "" ||
		id.Detail != "You have no sending identity on member.example.test yet. An organization owner or admin can add one after the domain is verified." {
		t.Fatal("member identity before verification", id)
	}
}

// The recorded failure names no button: the checklist offers "Retry sending
// setup" and the sending panel "Set up sending resources".
func TestAutomaticSetupFailureWording(t *testing.T) {
	if strings.Contains(automaticSetupFailed, "Retry sending setup") || !strings.Contains(automaticSetupFailed, "Set up sending resources again") {
		t.Fatal(automaticSetupFailed)
	}
}
