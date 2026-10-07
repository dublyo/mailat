package main

import (
	"strings"
	"testing"
)

func TestStartupBannerNamesOnlyServedSurfaces(t *testing.T) {
	got := startupBanner(8000, "sha-abc123")
	for _, want := range []string{"Mailat API sha-abc123", "http://localhost:8000/docs/", "http://localhost:8000/api/v1/openapi.json"} {
		if !strings.Contains(got, want) {
			t.Errorf("banner lacks %q:\n%s", want, got)
		}
	}
	for _, stale := range []string{"140+", "openapi.yaml", "/analytics", "mailat.co"} {
		if strings.Contains(got, stale) {
			t.Errorf("banner still mentions %q:\n%s", stale, got)
		}
	}
	if lines := strings.Count(strings.TrimSpace(got), "\n") + 1; lines != 3 {
		t.Errorf("banner has %d lines, want 3:\n%s", lines, got)
	}
}
