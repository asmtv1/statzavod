package migrations_test

import (
	"os"
	"strings"
	"testing"
)

func TestAuthSessionInvalidationMigration(t *testing.T) {
	contents, err := os.ReadFile("00021_auth_session_invalidation.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(contents))
	for _, required := range []string{
		"after update of email, password_hash, status on users",
		"update sessions",
		"revoked_at = coalesce(revoked_at, now())",
		"where user_id = new.id and revoked_at is null",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("session invalidation migration is missing %q", required)
		}
	}
}
