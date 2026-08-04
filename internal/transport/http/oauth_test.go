package httpserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/statzavod/statzavod/internal/config"
)

func TestInstagramAuthorizationURL(t *testing.T) {
	s := &Server{config: config.Config{}}
	provider := oauthProvider{
		ID:           "INSTAGRAM",
		ClientID:     "client-id",
		RedirectURL:  "https://statzavod.test/api/v1/oauth/instagram/callback",
		AuthorizeURL: "https://www.instagram.com/oauth/authorize",
		Scopes:       []string{"instagram_business_basic", "instagram_business_manage_insights"},
	}
	authorizationURL := s.oauthAuthorizationURL(provider, "opaque-state", "unused-challenge")
	parsed, err := url.Parse(authorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("client_id") != provider.ClientID || query.Get("redirect_uri") != provider.RedirectURL || query.Get("state") != "opaque-state" {
		t.Fatalf("unexpected authorization query: %s", parsed.RawQuery)
	}
	if query.Get("scope") != "instagram_business_basic,instagram_business_manage_insights" {
		t.Fatalf("unexpected Instagram scopes: %q", query.Get("scope"))
	}
	if query.Has("code_challenge") {
		t.Fatal("Instagram authorization unexpectedly included PKCE parameters")
	}
}

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
