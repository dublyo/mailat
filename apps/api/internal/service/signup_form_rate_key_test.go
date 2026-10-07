package service

import "testing"

func TestSignupFormRateKeyIsCanonical(t *testing.T) {
	want := "form:3f2b8c1e-9d4a-4b7e-8f60-1a2b3c4d5e6f"
	for _, id := range []string{
		"3f2b8c1e-9d4a-4b7e-8f60-1a2b3c4d5e6f",
		"3F2B8C1E-9D4A-4B7E-8F60-1A2B3C4D5E6F",
		"{3f2b8c1e-9d4a-4b7e-8f60-1a2b3c4d5e6f}",
		"urn:uuid:3F2B8C1E-9d4a-4b7e-8f60-1a2b3c4d5e6f",
		"3f2b8c1e9d4a4b7e8f601a2b3c4d5e6f",
	} {
		if got := signupFormRateKey(id); got != want {
			t.Errorf("signupFormRateKey(%q) = %q, want %q", id, got, want)
		}
	}
}
