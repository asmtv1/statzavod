package crypto

import "testing"

func TestEnvelopeRoundTrip(t *testing.T) {
	e, err := New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, nonce, err := e.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := e.Decrypt(ciphertext, nonce)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != "secret" {
		t.Fatalf("got %q", plain)
	}
}
func TestEnvelopeRejectsShortKey(t *testing.T) {
	if _, err := New(make([]byte, 31)); err == nil {
		t.Fatal("expected error")
	}
}
