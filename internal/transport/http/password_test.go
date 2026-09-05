package httpserver

import "testing"

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := hashPassword("a-long-enough-password")
	if err != nil {
		t.Fatal(err)
	}
	if !verifyPassword(hash, "a-long-enough-password") {
		t.Fatal("password should verify")
	}
	if verifyPassword(hash, "another-password") {
		t.Fatal("wrong password should not verify")
	}
}

func TestPasswordPolicyAndArgon2id(t *testing.T) {
	if _, err := hashPassword("short"); err == nil {
		t.Fatal("passwords shorter than 12 characters must be rejected")
	}
	hash, err := hashPassword("twelve-chars+")
	if err != nil {
		t.Fatal(err)
	}
	if len(hash) < len("$argon2id$") || hash[:len("$argon2id$")] != "$argon2id$" {
		t.Fatalf("password is not Argon2id: %q", hash)
	}
}

func TestPasswordPolicyCountsUnicodeCharacters(t *testing.T) {
	if validPassword("пароль") { // 12 UTF-8 bytes, but only 6 characters.
		t.Fatal("six Cyrillic characters must not satisfy the 12-character policy")
	}
	if !validPassword("парольпароль") {
		t.Fatal("twelve Cyrillic characters must satisfy the policy")
	}
	if validPassword(string([]byte{0xff, 0xfe}) + "123456789012") {
		t.Fatal("invalid UTF-8 password must be rejected")
	}
}
