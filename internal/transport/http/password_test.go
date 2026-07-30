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
