package config

import "testing"

func TestNormalizeRedisURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{{"redis://", "redis://"}, {"rediss://host:6380", "rediss://host:6380"}, {"host:6379", "redis://host:6379"}} {
		if got := normalizeRedisURL(tc.in); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
	}
}
