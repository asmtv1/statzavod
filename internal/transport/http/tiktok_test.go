package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/statzavod/statzavod/internal/config"
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

func TestRevokeTikTokAcceptsEmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v2/oauth/revoke/" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			t.Fatalf("unexpected content type %q", r.Header.Get("Content-Type"))
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("client_key") != "client" || r.Form.Get("client_secret") != "secret" || r.Form.Get("token") != "access-token" {
			t.Fatalf("unexpected revoke form: %#v", r.Form)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	s := &Server{config: config.Config{TikTokAPIBase: server.URL, TikTokClientKey: "client", TikTokClientSecret: "secret"}}
	if err := s.revokeTikTok(t.Context(), "access-token"); err != nil {
		t.Fatal(err)
	}
}

func TestRevokeTikTokReturnsProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"error": "invalid_request", "error_description": "invalid token", "log_id": "provider-log"})
	}))
	defer server.Close()

	s := &Server{config: config.Config{TikTokAPIBase: server.URL, TikTokClientKey: "client", TikTokClientSecret: "secret"}}
	if err := s.revokeTikTok(t.Context(), "invalid-token"); err == nil {
		t.Fatal("expected provider error")
	}
}
