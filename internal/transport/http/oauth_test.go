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

func TestCompleteInstagramFacebookOAuthUsesLinkedPageAccessToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("grant_type") == "fb_exchange_token" {
			if r.URL.Query().Get("fb_exchange_token") != "short-user-token" {
				t.Fatal("short-lived Facebook user token was not exchanged")
			}
			writeJSON(w, http.StatusOK, map[string]any{"access_token": "long-user-token", "expires_in": 5_184_000})
			return
		}
		if r.URL.Query().Get("code") != "code" {
			t.Fatal("authorization code was not sent")
		}
		writeJSON(w, http.StatusOK, map[string]any{"access_token": "short-user-token", "expires_in": 3600})
	})
	mux.HandleFunc("/me/permissions", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("access_token") != "long-user-token" {
			t.Fatal("long-lived Facebook user token was not used for permission discovery")
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{
			map[string]any{"permission": "instagram_basic", "status": "granted"},
			map[string]any{"permission": "instagram_manage_insights", "status": "granted"},
			map[string]any{"permission": "pages_show_list", "status": "granted"},
			map[string]any{"permission": "pages_read_engagement", "status": "granted"},
		}})
	})
	mux.HandleFunc("/me/accounts", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("access_token") != "long-user-token" {
			t.Fatal("Facebook user token was not used to discover Pages")
		}
		fields := r.URL.Query().Get("fields")
		for _, field := range []string{"access_token", "tasks", "instagram_business_account"} {
			if !strings.Contains(fields, field) {
				t.Fatalf("Page discovery fields %q do not contain %q", fields, field)
			}
		}
		if r.URL.Query().Get("after") == "" {
			writeJSON(w, http.StatusOK, map[string]any{
				"data":   []any{map[string]any{"id": "page-without-instagram", "name": "Other Page", "access_token": "other-page-token"}},
				"paging": map[string]any{"cursors": map[string]any{"after": "next-page"}, "next": "https://graph.test/me/accounts?after=next-page"},
			})
			return
		}
		if r.URL.Query().Get("after") != "next-page" {
			t.Fatalf("unexpected Page cursor %q", r.URL.Query().Get("after"))
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{map[string]any{
			"id": "page-1", "name": "Creator Page", "access_token": "page-access-token",
			"instagram_business_account": map[string]any{"id": "ig-1"},
		}}})
	})
	mux.HandleFunc("/ig-1", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("access_token") != "page-access-token" {
			t.Fatal("Page access token was not used to load the linked Instagram account")
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "ig-1", "username": "creator", "name": "Creator", "profile_picture_url": "https://img.test/avatar.jpg"})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	s := &Server{config: config.Config{InstagramFacebookGraphAPIBase: server.URL}}
	provider := oauthProvider{
		ID: "INSTAGRAM", ClientID: "facebook-client", ClientSecret: "facebook-secret", RedirectURL: "https://app.test/callback", Flow: "FACEBOOK",
		Scopes: []string{"instagram_basic", "instagram_manage_insights", "pages_show_list", "pages_read_engagement"},
	}
	token, profile, err := s.completeInstagramFacebookOAuth(t.Context(), provider, "code")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "page-access-token" || token.RefreshToken != "long-user-token" {
		t.Fatalf("unexpected token result: %#v", token)
	}
	if profile.ExternalID != "ig-1" || profile.Username != "creator" || profile.Metadata["facebookPageId"] != "page-1" {
		t.Fatalf("unexpected profile result: %#v", profile)
	}
}

func TestRefreshInstagramFacebookAccessTokenRefreshesSelectedPageToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("client_id") != "facebook-client" || query.Get("client_secret") != "facebook-secret" || query.Get("fb_exchange_token") != "old-user-token" {
			t.Fatalf("unexpected Facebook user token refresh query: %s", query.Encode())
		}
		writeJSON(w, http.StatusOK, map[string]any{"access_token": "new-user-token", "expires_in": 5_184_000})
	})
	mux.HandleFunc("/page-1", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("access_token") != "new-user-token" {
			t.Fatal("refreshed Facebook user token was not used to reload the Page token")
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "page-1", "access_token": "new-page-token", "instagram_business_account": map[string]any{"id": "ig-1"},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	s := &Server{config: config.Config{
		InstagramFacebookGraphAPIBase: server.URL,
		InstagramFacebookClientID:     "facebook-client",
		InstagramFacebookClientSecret: "facebook-secret",
	}}
	token, err := s.refreshInstagramFacebookAccessToken(t.Context(), "old-user-token", "page-1", "ig-1")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "new-page-token" || token.RefreshToken != "new-user-token" || token.ExpiresIn != 5_184_000 {
		t.Fatalf("unexpected refreshed token: %#v", token)
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
		name          string
		platform      string
		refresh       []byte
		nonce         []byte
		facebookLogin bool
		want          string
	}{
		{name: "YouTube prefers refresh token", platform: "YOUTUBE", refresh: refreshCipher, nonce: refreshNonce, want: "refresh-token"},
		{name: "YouTube falls back to access token", platform: "YOUTUBE", want: "access-token"},
		{name: "TikTok uses access token", platform: "TIKTOK", refresh: refreshCipher, nonce: refreshNonce, want: "access-token"},
		{name: "Instagram via Facebook uses user token", platform: "INSTAGRAM", refresh: refreshCipher, nonce: refreshNonce, facebookLogin: true, want: "refresh-token"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := s.platformRevocationToken(platformRevocationConnection{
				platform:      test.platform,
				accessCipher:  accessCipher,
				accessNonce:   accessNonce,
				refreshCipher: test.refresh,
				refreshNonce:  test.nonce,
				facebookLogin: test.facebookLogin,
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
	if err := s.revokePlatform(t.Context(), "YOUTUBE", "refresh-token", false); err != nil {
		t.Fatal(err)
	}
}

func TestRevokeInstagramFacebookTokenUsesFacebookGraph(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/me/permissions" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("access_token") != "facebook-user-token" {
			t.Fatal("Facebook user token was not revoked")
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	}))
	defer server.Close()

	s := &Server{config: config.Config{InstagramFacebookGraphAPIBase: server.URL}}
	if err := s.revokePlatform(t.Context(), "INSTAGRAM", "facebook-user-token", true); err != nil {
		t.Fatal(err)
	}
}
