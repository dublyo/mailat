package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/testutil"
)

// pendingFor counts the stored challenges of one user.
func (s *WebAuthnService) pendingFor(userID int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.challenges {
		if c.UserID == userID {
			n++
		}
	}
	return n
}

// With WEBAUTHN_ENABLED off every ceremony refuses before touching the store
// or the database (db is nil here, so any query would panic).
func TestWebAuthnDisabledRefusesCeremonies(t *testing.T) {
	ctx := context.Background()
	for _, cfg := range []*config.Config{nil, {}} {
		s := NewWebAuthnService(nil, cfg)
		if s.Enabled() {
			t.Fatal("enabled by default")
		}
		if _, err := s.BeginRegistration(ctx, 1, "a@example.test", "A"); !errors.Is(err, ErrWebAuthnDisabled) {
			t.Fatalf("BeginRegistration: %v", err)
		}
		if _, err := s.FinishRegistration(ctx, 1, "key", &RegistrationResponse{}); !errors.Is(err, ErrWebAuthnDisabled) {
			t.Fatalf("FinishRegistration: %v", err)
		}
		if _, err := s.BeginAuthentication(ctx, 1); !errors.Is(err, ErrWebAuthnDisabled) {
			t.Fatalf("BeginAuthentication: %v", err)
		}
		if err := s.FinishAuthentication(ctx, 1, &AuthenticationResponse{}); !errors.Is(err, ErrWebAuthnDisabled) {
			t.Fatalf("FinishAuthentication: %v", err)
		}
		if n := s.pendingChallenges(); n != 0 {
			t.Fatalf("disabled service stored %d challenges", n)
		}
	}
}

func TestWebAuthnChallengeStoreBounds(t *testing.T) {
	clock := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	s := NewWebAuthnService(nil, &config.Config{WebAuthnEnabled: true})
	s.now = func() time.Time { return clock }
	tick := func(d time.Duration) { clock = clock.Add(d) }

	// Per-user cap: the oldest challenges are evicted.
	for i := 0; i < webAuthnMaxPendingPerUser+2; i++ {
		s.storeChallenge(fmt.Sprintf("u1-%d", i), 1, "registration")
		tick(time.Second)
	}
	if n := s.pendingFor(1); n != webAuthnMaxPendingPerUser {
		t.Fatalf("user holds %d challenges", n)
	}
	for _, gone := range []string{"u1-0", "u1-1"} {
		if err := s.takeChallenge(gone, 1, "registration"); err == nil {
			t.Fatalf("%s should have been evicted", gone)
		}
	}
	// Another user or ceremony cannot consume (or burn) the challenge.
	if err := s.takeChallenge("u1-2", 2, "registration"); err == nil {
		t.Fatal("other user consumed challenge")
	}
	if err := s.takeChallenge("u1-2", 1, "authentication"); err == nil {
		t.Fatal("wrong ceremony consumed challenge")
	}
	if err := s.takeChallenge("u1-2", 1, "registration"); err != nil {
		t.Fatalf("owner could not consume: %v", err)
	}
	if err := s.takeChallenge("u1-2", 1, "registration"); err == nil {
		t.Fatal("challenge reused")
	}

	// Expiry: an expired challenge is refused and pruned on the next write.
	tick(webAuthnChallengeTTL)
	if err := s.takeChallenge("u1-3", 1, "registration"); err == nil {
		t.Fatal("expired challenge accepted")
	}
	s.storeChallenge("fresh", 3, "authentication")
	if n := s.pendingChallenges(); n != 1 {
		t.Fatalf("expired entries not pruned: %d left", n)
	}
	tick(time.Second)

	// Global cap: the oldest challenges of any user are evicted.
	for i := 0; i < webAuthnMaxPending+50; i++ {
		s.storeChallenge(fmt.Sprintf("g-%d", i), int64(1000+i), "registration")
	}
	if n := s.pendingChallenges(); n != webAuthnMaxPending {
		t.Fatalf("global store holds %d challenges", n)
	}
	if err := s.takeChallenge("fresh", 3, "authentication"); err == nil {
		t.Fatal("oldest challenge not evicted at the global cap")
	}
}

// Concurrent writers, readers and consumers must not race (go test -race) and
// the store stays bounded.
func TestWebAuthnChallengeStoreConcurrent(t *testing.T) {
	s := NewWebAuthnService(nil, &config.Config{WebAuthnEnabled: true})
	var wg sync.WaitGroup
	for g := 0; g < 32; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				key := fmt.Sprintf("%d-%d", g, i)
				user := int64(g % 4)
				s.storeChallenge(key, user, "registration")
				if i%3 == 0 {
					_ = s.takeChallenge(key, user, "registration")
				}
				_ = s.pendingChallenges()
			}
		}(g)
	}
	wg.Wait()
	for user := int64(0); user < 4; user++ {
		if n := s.pendingFor(user); n > webAuthnMaxPendingPerUser {
			t.Fatalf("user %d holds %d challenges", user, n)
		}
	}
	if n := s.pendingChallenges(); n > 4*webAuthnMaxPendingPerUser {
		t.Fatalf("store holds %d challenges", n)
	}
}

// Hammering the real Begin* ceremonies concurrently used to crash the process
// with "concurrent map writes".
func TestWebAuthnBeginConcurrentRace(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Test','test',now());
		INSERT INTO users(id,org_id,email,name,password_hash,role,updated_at) VALUES(1,1,'one@example.test','One','unused','owner',now()),(2,1,'two@example.test','Two','unused','member',now());
		INSERT INTO webauthn_credentials(user_id,credential_id,public_key,name) VALUES(2,'\x01','\x02','key')`); err != nil {
		t.Fatal(err)
	}
	s := NewWebAuthnService(db, &config.Config{WebAuthnEnabled: true, AppDomain: "example.test", AppName: "Mailat"})
	var wg sync.WaitGroup
	errs := make(chan error, 64*10)
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				var err error
				if g%2 == 0 {
					_, err = s.BeginRegistration(ctx, 1, "one@example.test", "One")
				} else {
					_, err = s.BeginAuthentication(ctx, 2)
				}
				if err != nil {
					errs <- err
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for _, user := range []int64{1, 2} {
		if n := s.pendingFor(user); n != webAuthnMaxPendingPerUser {
			t.Fatalf("user %d holds %d challenges", user, n)
		}
	}
	if n := s.pendingChallenges(); n != 2*webAuthnMaxPendingPerUser {
		t.Fatalf("store holds %d challenges", n)
	}
}
