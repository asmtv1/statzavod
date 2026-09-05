package httpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/statzavod/statzavod/internal/config"
)

func TestTikTokRequestsRejectSameHostRedirectBeforeSecretsReachSink(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Server) error
	}{
		{name: "authorization code exchange", run: func(s *Server) error {
			_, err := s.exchangeTikTokCode(t.Context(), "code-canary")
			return err
		}},
		{name: "refresh token exchange", run: func(s *Server) error {
			_, err := s.refreshTikTokToken(t.Context(), "refresh-token-canary")
			return err
		}},
		{name: "bearer API", run: func(s *Server) error {
			_, err := s.fetchTikTokUser(t.Context(), "bearer-canary")
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var sinkCalls atomic.Int32
			mux := http.NewServeMux()
			mux.HandleFunc("/v2/oauth/token/", func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/sink", http.StatusTemporaryRedirect)
			})
			mux.HandleFunc("/v2/user/info/", func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/sink", http.StatusTemporaryRedirect)
			})
			mux.HandleFunc("/sink", func(w http.ResponseWriter, r *http.Request) {
				sinkCalls.Add(1)
				body, _ := io.ReadAll(r.Body)
				if r.Header.Get("Authorization") != "" || len(body) != 0 {
					t.Errorf("redirect sink received TikTok credentials")
				}
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			s := &Server{config: config.Config{
				TikTokAPIBase: server.URL, TikTokClientKey: "client-canary", TikTokClientSecret: "client-secret-canary",
			}}

			if err := test.run(s); err == nil {
				t.Fatal("same-host redirect was accepted")
			}
			if sinkCalls.Load() != 0 {
				t.Fatalf("redirect sink received %d requests", sinkCalls.Load())
			}
		})
	}
}

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

func TestClassifyTikTokRefreshErrors(t *testing.T) {
	if got := classifyTikTokError("invalid_refresh_token"); got != providerAuth {
		t.Fatalf("expected auth error, got %s", got)
	}
	if got := classifyTikTokError("rate_limit_exceeded"); got != providerRateLimit {
		t.Fatalf("expected rate-limit error, got %s", got)
	}
	if got := classifyTikTokError("internal_error"); got != providerRetryable {
		t.Fatalf("expected retryable error, got %s", got)
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

func TestFetchTikTokUserIncludesCurrentProfileFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/user/info/" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer access-token" {
			t.Fatalf("unexpected authorization %q", r.Header.Get("Authorization"))
		}
		fields := r.URL.Query().Get("fields")
		for _, field := range []string{"username", "avatar_url", "profile_deep_link", "display_name"} {
			if !strings.Contains(fields, field) {
				t.Fatalf("profile field %q was not requested: %q", field, fields)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"user": map[string]any{
			"open_id": "open-id", "username": "anastas_o_faadl", "display_name": "Anastasia",
			"avatar_url": "https://cdn.example/avatar.jpg", "profile_deep_link": "https://www.tiktok.com/@anastas_o_faadl",
		}}})
	}))
	defer server.Close()

	s := &Server{config: config.Config{TikTokAPIBase: server.URL}}
	user, err := s.fetchTikTokUser(t.Context(), "access-token")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := tiktokUsername(user), "anastas_o_faadl"; got != want {
		t.Fatalf("username = %q, want %q", got, want)
	}
	if got, want := tiktokProfileURL(user), "https://www.tiktok.com/@anastas_o_faadl"; got != want {
		t.Fatalf("profile URL = %q, want %q", got, want)
	}
	if got, want := user.AvatarURL, "https://cdn.example/avatar.jpg"; got != want {
		t.Fatalf("avatar URL = %q, want %q", got, want)
	}
}

func TestLegacyTikTokCallbackScopesFailClosed(t *testing.T) {
	missing := splitScopes("", strings.Split(tiktokScopes, ","))
	if missing == nil || len(missing) != 0 {
		t.Fatalf("missing TikTok token scope must not inherit requested scopes: %#v", missing)
	}
	readiness := publishingReadiness("TIKTOK", "account", "ACTIVE", "ACTIVE", missing)
	if readiness.Compatible || !readiness.Reauth || len(readiness.MissingScopes) != 1 || readiness.MissingScopes[0] != "video.publish" {
		t.Fatalf("legacy callback without grant evidence became publish-ready: %#v", readiness)
	}

	explicit := splitScopes("user.info.basic,video.publish", nil)
	readiness = publishingReadiness("TIKTOK", "account", "ACTIVE", "ACTIVE", explicit)
	if !readiness.Compatible || readiness.Reauth || len(readiness.MissingScopes) != 0 {
		t.Fatalf("explicit TikTok publishing grant was rejected: %#v", readiness)
	}
}
