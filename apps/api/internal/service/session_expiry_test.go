package service

import (
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
)

func TestSessionExpiryAcceptsDays(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
	}{{"7d", 7 * 24 * time.Hour}, {"30d", 30 * 24 * time.Hour}, {"24h", 24 * time.Hour}, {"", 7 * 24 * time.Hour}, {"bogus", 7 * 24 * time.Hour}} {
		s := &AuthService{cfg: &config.Config{JWTExpiresIn: tc.in}}
		got := time.Until(s.sessionExpiry())
		if got < tc.want-2*time.Second || got > tc.want {
			t.Errorf("%q: expiry in %s, want about %s", tc.in, got, tc.want)
		}
	}
}
