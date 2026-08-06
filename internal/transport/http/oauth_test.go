package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/statzavod/statzavod/internal/config"
	crypt "github.com/statzavod/statzavod/internal/crypto"
)

func TestCompleteYouTubeOAuth(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		_ = r.ParseForm()
		if r.Form.Get("code_verifier") != "verifier" {
			t.Fatal("PKCE verifier was not sent")
		}
		writeJSON(w, http.StatusOK, map[string]any{"access_token": "access", "refresh_token": "refresh", "expires_in": 3600, "scope": "scope-a scope-b"})
	})
	mux.HandleFunc("/channels", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access" {
			t.Fatal("access token was not sent")
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"items": []any{
				map[string]any{
					"id": "channel-1",
					"snippet": map[string]any{
						"title":     "Channel",
						"customUrl": "@channel",
						"thumbnails": map[string]any{
							"default": map[string]any{"url": "https://img.test/avatar.jpg"},
						},
					},
				},
			},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	s := &Server{config: config.Config{YouTubeOAuthBase: server.URL, YouTubeAPIBase: server.URL}}
	provider := oauthProvider{ID: "YOUTUBE", ClientID: "client", ClientSecret: "secret", RedirectURL: "https://app.test/callback", Scopes: []string{"fallback"}}
	token, profile, err := s.completeYouTubeOAuth(t.Context(), provider, "code", "verifier")
	if err != nil {
		t.Fatal(err)
	}
	if token.RefreshToken != "refresh" || profile.ExternalID != "channel-1" || profile.Username != "channel" {
		t.Fatalf("unexpected result: %#v %#v", token, profile)
	}
}

func TestCompleteInstagramOAuth(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"access_token": "short", "user_id": 42})
	})
	mux.HandleFunc("/access_token", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"access_token": "long", "expires_in": 5_184_000})
	})
	mux.HandleFunc("/me", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("access_token") != "long" {
			t.Fatal("long-lived access token was not used")
		}
		writeJSON(w, http.StatusOK, map[string]any{"user_id": "42", "username": "creator", "name": "Creator", "profile_picture_url": "https://img.test/avatar.jpg", "account_type": "BUSINESS"})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	s := &Server{config: config.Config{InstagramTokenBase: server.URL, InstagramAPIBase: server.URL}}
	provider := oauthProvider{ID: "INSTAGRAM", ClientID: "client", ClientSecret: "secret", RedirectURL: "https://app.test/callback", Scopes: []string{"instagram_business_basic"}}
	token, profile, err := s.completeInstagramOAuth(t.Context(), provider, "code")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "long" || profile.ExternalID != "42" || profile.Username != "creator" {
		t.Fatalf("unexpected result: %#v %#v", token, profile)
	}
}

func TestCompleteVKOAuth(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth2/auth", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Query().Get("code_verifier") != "verifier" || r.URL.Query().Get("state") != "state" || r.URL.Query().Get("device_id") != "device" {
			t.Fatal("VK ID token request did not include PKCE verifier and device ID")
		}
		_ = r.ParseForm()
		if r.Form.Get("code") != "code" {
			t.Fatal("VK ID token request did not include code")
		}
		writeJSON(w, http.StatusOK, map[string]any{"access_token": "access", "refresh_token": "refresh", "expires_in": 3600})
	})
	mux.HandleFunc("/method/users.get", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"response": []any{
				map[string]any{"id": 7, "first_name": "Иван", "last_name": "Иванов", "screen_name": "ivan", "photo_200": "https://img.test/avatar.jpg"},
			},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	s := &Server{config: config.Config{VKOAuthBase: server.URL, VKAPIBase: server.URL, VKAPIVersion: "5.199"}}
	provider := oauthProvider{ID: "VK", ClientID: "client", ClientSecret: "secret", RedirectURL: "https://app.test/callback", Scopes: []string{"video"}}
	token, profile, err := s.completeVKOAuth(t.Context(), provider, "code", "verifier", "state", "device")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "access" || token.RefreshToken != "refresh" || profile.ExternalID != "7" || profile.Username != "ivan" {
		t.Fatalf("unexpected result: %#v %#v", token, profile)
	}
}

func TestPlatformRevocationToken(t *testing.T) {
	envelope, err := crypt.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	accessCipher, accessNonce, err := envelope.Encrypt([]byte("access-token"))
	if err != nil {
		t.Fatal(err)
	}
	refreshCipher, refreshNonce, err := envelope.Encrypt([]byte("refresh-token"))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{envelope: envelope}

	tests := []struct {
		name     string
		platform string
		refresh  []byte
		nonce    []byte
		want     string
	}{
		{name: "YouTube prefers refresh token", platform: "YOUTUBE", refresh: refreshCipher, nonce: refreshNonce, want: "refresh-token"},
		{name: "YouTube falls back to access token", platform: "YOUTUBE", want: "access-token"},
		{name: "TikTok uses access token", platform: "TIKTOK", refresh: refreshCipher, nonce: refreshNonce, want: "access-token"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := s.platformRevocationToken(platformRevocationConnection{
				platform:      test.platform,
				accessCipher:  accessCipher,
				accessNonce:   accessNonce,
				refreshCipher: test.refresh,
				refreshNonce:  test.nonce,
			})
			if got != test.want {
				t.Fatalf("platformRevocationToken() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRevokeYouTubeTokenAcceptsEmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/revoke" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			t.Fatalf("unexpected content type %q", r.Header.Get("Content-Type"))
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.Form.Get("token"); got != "refresh-token" {
			t.Fatalf("token = %q, want refresh-token", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	s := &Server{config: config.Config{YouTubeOAuthBase: server.URL}}
	if err := s.revokePlatform(t.Context(), "YOUTUBE", "refresh-token"); err != nil {
		t.Fatal(err)
	}
}
