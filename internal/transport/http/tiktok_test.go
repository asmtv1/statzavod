package httpserver

import (
	"encoding/json"
	"testing"
)

func TestTikTokAPIError(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		description string
		logID       string
		wantCode    string
		wantMessage string
		wantLogID   string
	}{
		{name: "success object", raw: `{"code":"ok","message":""}`},
		{name: "success string", raw: `"ok"`},
		{name: "oauth error", raw: `"invalid_request"`, description: "Redirect URI mismatch", logID: "abc", wantCode: "invalid_request", wantMessage: "Redirect URI mismatch", wantLogID: "abc"},
		{name: "api error", raw: `{"code":"scope_not_authorized","message":"Scope is unavailable","log_id":"provider-log"}`, wantCode: "scope_not_authorized", wantMessage: "Scope is unavailable", wantLogID: "provider-log"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, message, logID := tikTokAPIError(json.RawMessage(test.raw), test.description, test.logID)
			if code != test.wantCode || message != test.wantMessage || logID != test.wantLogID {
				t.Fatalf("tikTokAPIError() = (%q, %q, %q), want (%q, %q, %q)", code, message, logID, test.wantCode, test.wantMessage, test.wantLogID)
			}
		})
	}
}
