package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
)

func TestIdentityAddressDomainBoundary(t *testing.T) {
	for _, tc := range []struct {
		address, domain string
		valid           bool
	}{
		{"Sales@Example.com", "example.com", true},
		{"sales+offer@example.com", "example.com", true},
		{"sales@elsewhere.com", "example.com", false},
		{"Display <sales@example.com>", "example.com", false},
		{"sales@example.com\r\nBcc: x@elsewhere.com", "example.com", false},
		{"sales@sub.example.com", "example.com", false},
	} {
		t.Run(tc.address, func(t *testing.T) {
			_, err := identityAddressForDomain(tc.address, tc.domain)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
		})
	}
}

func TestExplicitIdentityCapAcrossConcurrentUsers(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	var org int64
	if err := db.QueryRow(`INSERT INTO organizations(name,slug,max_identities,updated_at) VALUES('Capped','capped',1,now()) RETURNING id`).Scan(&org); err != nil {
		t.Fatal(err)
	}
	start, results := make(chan struct{}), make(chan error, 2)
	svc := NewIdentityService(db, &config.Config{EmailProvider: "ses"})
	for n := 0; n < 2; n++ {
		var user int64
		var domain string
		name := fmt.Sprintf("cap%d.test", n)
		if err := db.QueryRow(`INSERT INTO users(org_id,email,password_hash,updated_at) VALUES($1,$2,'unused',now()) RETURNING id`, org, "owner@"+name).Scan(&user); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`INSERT INTO domains(org_id,name,verification_token,status,ses_verified,updated_at) VALUES($1,$2,'test','active',true,now()) RETURNING uuid`, org, name).Scan(&domain); err != nil {
			t.Fatal(err)
		}
		go func(user int64, domain, name string) {
			<-start
			_, err := svc.CreateIdentity(ctx, user, &model.CreateIdentityRequest{DomainId: domain, Email: "mail@" + name})
			results <- err
		}(user, domain, name)
	}
	close(start)
	success := 0
	for n := 0; n < 2; n++ {
		if <-results == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("wanted one creation under shared cap, got %d", success)
	}
}

func TestDomainCapAndMissingSESProvider(t *testing.T) {
	ctx := context.Background()
	unavailable := &DomainService{cfg: &config.Config{EmailProvider: "ses"}}
	if _, err := unavailable.CreateDomain(ctx, 1, &model.CreateDomainRequest{Name: "example.test"}); err == nil {
		t.Fatal("SES domain creation silently fell back without a provider")
	}
	db := testutil.Database(t)
	var org int64
	if err := db.QueryRow(`INSERT INTO organizations(name,slug,max_domains,updated_at) VALUES('Capped','capped',1,now()) RETURNING id`).Scan(&org); err != nil {
		t.Fatal(err)
	}
	svc := &DomainService{db: db, cfg: &config.Config{EmailProvider: "smtp"}}
	start, results := make(chan struct{}), make(chan error, 2)
	for n := 0; n < 2; n++ {
		go func(n int) {
			<-start
			_, err := svc.CreateDomain(ctx, org, &model.CreateDomainRequest{Name: fmt.Sprintf("cap%d.test", n)})
			results <- err
		}(n)
	}
	close(start)
	success := 0
	for n := 0; n < 2; n++ {
		if <-results == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("wanted one domain under shared cap, got %d", success)
	}
}

func TestSESIdentitiesAndExplicitMonthlyQuota(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	var org, user, domain int64
	var domainUUID string
	if err := db.QueryRow(`INSERT INTO organizations(name,slug,monthly_email_limit,updated_at) VALUES('Test','test',2,now()) RETURNING id`).Scan(&org); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO users(org_id,email,password_hash,updated_at) VALUES($1,'owner@example.test','unused',now()) RETURNING id`, org).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO domains(org_id,name,verification_token,status,ses_verified,updated_at) VALUES($1,'example.test','test','active',true,now()) RETURNING id,uuid`, org).Scan(&domain, &domainUUID); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{EmailProvider: "ses", DisableAppLimits: true}
	svc := NewIdentityService(db, cfg)
	a, err := svc.CreateIdentity(ctx, user, &model.CreateIdentityRequest{DomainId: domainUUID, Email: "a@example.test", DisplayName: "A", IsDefault: true, IsCatchAll: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.CreateIdentity(ctx, user, &model.CreateIdentityRequest{DomainId: domainUUID, Email: "b@example.test", DisplayName: "B", IsDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.CreateIdentity(ctx, user, &model.CreateIdentityRequest{DomainId: domainUUID, Email: "steal@other.test", DisplayName: "Bad"}); err == nil {
		t.Fatal("unowned domain allowed")
	}
	loaded, err := svc.GetIdentity(ctx, user, a.UUID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.IsDefault || !loaded.CanSend || !loaded.CanReceive {
		t.Fatalf("incorrect identity flags: default=%v send=%v receive=%v", loaded.IsDefault, loaded.CanSend, loaded.CanReceive)
	}
	on, off := true, false
	if _, err = svc.UpdateIdentity(ctx, user, b.UUID, &model.UpdateIdentityRequest{IsCatchAll: &on}); err == nil {
		t.Fatal("second catch-all allowed")
	}
	if _, err = svc.UpdateIdentity(ctx, user, a.UUID, &model.UpdateIdentityRequest{IsCatchAll: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.UpdateIdentity(ctx, user, b.UUID, &model.UpdateIdentityRequest{IsCatchAll: &on}); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.UpdateIdentity(ctx, user+1000, b.UUID, &model.UpdateIdentityRequest{IsDefault: &on}); err == nil {
		t.Fatal("cross-user identity update allowed")
	}
	for n := 0; n < 4; n++ {
		if err = reserveMonthlySend(ctx, db, cfg, org); err != nil {
			t.Fatal("unlimited blocked", err)
		}
	}
	cfg.DisableAppLimits = false
	for n := 0; n < 2; n++ {
		if err = reserveMonthlySend(ctx, db, cfg, org); err != nil {
			t.Fatal(err)
		}
	}
	if err = reserveMonthlySend(ctx, db, cfg, org); err == nil {
		t.Fatal("positive quota not enforced")
	}
}

func TestIdentityReadsNullableLegacyMetadata(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Legacy identity','legacy-identity',now());
INSERT INTO users(id,org_id,email,password_hash,updated_at) VALUES(1,1,'legacy@example.test','unused',now());
INSERT INTO domains(id,org_id,name,verification_token,updated_at) VALUES(1,1,'example.test','legacy-token',now());
INSERT INTO identities(id,user_id,domain_id,email,display_name,color,stalwart_account_id,can_send,can_receive,updated_at) VALUES(1,1,1,'legacy@example.test',NULL,NULL,NULL,false,false,now());`); err != nil {
		t.Fatal(err)
	}
	svc := &IdentityService{db: db}
	listed, err := svc.ListIdentities(ctx, 1)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list nullable identity metadata: %+v %v", listed, err)
	}
	got, err := svc.GetIdentity(ctx, 1, listed[0].UUID)
	if err != nil {
		t.Fatal("get nullable identity metadata:", err)
	}
	for _, identity := range []*model.Identity{listed[0], got} {
		if identity.DisplayName != "" || identity.Color != "" || identity.StalwartAcctID != "" || identity.CanSend || identity.CanReceive || identity.Email != "legacy@example.test" {
			t.Fatalf("nullable identity defaults changed metadata or permissions: %+v", identity)
		}
	}
	if _, err := svc.GetIdentity(ctx, 2, got.UUID); !errors.Is(err, ErrIdentityNotFound) {
		t.Fatalf("cross-user identity should be hidden: %v", err)
	}
	if empty, err := svc.ListIdentities(ctx, 2); err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty identity list: %+v %v", empty, err)
	}
}
