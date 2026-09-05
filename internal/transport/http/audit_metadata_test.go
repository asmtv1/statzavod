package httpserver

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestSafeAuditMetadataRecursivelyRedactsSecretKeys(t *testing.T) {
	canary := "CANARY_SECRET_DO_NOT_AUDIT_91f2"
	metadata, err := safeAuditMetadata(map[string]any{
		"safe": "kept",
		"request": map[string]any{
			"body": []any{
				map[string]any{"password": canary},
				map[string]any{"oauthToken": canary},
				map[string]any{"nested": map[string]any{"ciphertext": canary, "cookieHash": canary}},
			},
		},
		"revealed_credentials": canary,
		"headers":              map[string]any{"Authorization": canary},
		"nested": []any{map[string]any{
			"apiKey": canary, "access_key": canary, "private-key": canary,
			"sessionId": canary, "nonce": canary,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, canary) {
		t.Fatalf("secret canary leaked into audit metadata: %s", text)
	}
	if !strings.Contains(text, `"safe":"kept"`) || strings.Count(text, "[REDACTED]") < 8 {
		t.Fatalf("unexpected redacted metadata: %s", text)
	}
}

func TestSafeAuditMetadataRejectsNonJSONValues(t *testing.T) {
	if _, err := safeAuditMetadata(map[string]any{"number": math.NaN()}); err == nil {
		t.Fatal("non-JSON audit metadata was accepted")
	}
}
