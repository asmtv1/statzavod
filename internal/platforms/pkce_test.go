package platforms

import "testing"

func TestNewPKCE(t *testing.T) {
	state, verifier, challenge, err := NewPKCE()
	if err != nil || state == "" || verifier == "" || challenge == "" {
		t.Fatalf("invalid PKCE: %v", err)
	}
}
