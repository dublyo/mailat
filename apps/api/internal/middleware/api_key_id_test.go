package middleware

import (
	"context"
	"testing"
)

// Send limits key API-key requests by key and session requests by user.
func TestAPIKeyID(t *testing.T) {
	if id := APIKeyID(context.Background()); id != 0 {
		t.Fatal(id)
	}
	session := context.WithValue(context.Background(), credentialContextKey, credential{SessionHash: "hash"})
	if id := APIKeyID(session); id != 0 {
		t.Fatal("session reported as API key", id)
	}
	key := context.WithValue(context.Background(), credentialContextKey, credential{KeyID: 42})
	if id := APIKeyID(key); id != 42 {
		t.Fatal(id)
	}
}
