package httpserver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/statzavod/statzavod/internal/config"
	crypt "github.com/statzavod/statzavod/internal/crypto"
)

func TestOAuthRequestsRejectSameHostRedirectBeforeSecretsReachSink(t *testing.T) {
	tests := []struct {
		name string
		run  func(string) error
	}{
		{name: "authorization code exchange", run: func(endpoint string) error {
			return doOAuthForm(t.Context(), endpoint, url.Values{"code": {"code-canary"}, "client_secret": {"client-secret-canary"}}, &map[string]any{})
		}},
		{name: "bearer API", run: func(endpoint string) error {
			return doBearerJSON(t.Context(), endpoint, "bearer-canary", &map[string]any{})
		}},
		{name: "query token API", run: func(endpoint string) error {
			return doJSON(t.Context(), http.MethodGet, endpoint+"?access_token=query-token-canary", "", &map[string]any{})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var sinkCalls atomic.Int32
			mux := http.NewServeMux()
			mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/sink", http.StatusTemporaryRedirect)
			})
			mux.HandleFunc("/sink", func(w http.ResponseWriter, r *http.Request) {
				sinkCalls.Add(1)
				body, _ := io.ReadAll(r.Body)
				if r.Header.Get("Authorization") != "" || len(body) != 0 || r.URL.Query().Get("access_token") != "" {
					t.Errorf("redirect sink received OAuth credentials")
				}
			})
			server := httptest.NewServer(mux)
			defer server.Close()

			if err := test.run(server.URL + "/start"); err == nil {
				t.Fatal("same-host redirect was accepted")
			}
			if sinkCalls.Load() != 0 {
				t.Fatalf("redirect sink received %d requests", sinkCalls.Load())
			}
		})
	}
}

type oauthRoundTripperFunc func(*http.Request) (*http.Response, error)

func (fn oauthRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestOAuthFailureLogNeverIncludesTransportSecrets(t *testing.T) {
	oldTransport := http.DefaultTransport
	http.DefaultTransport = oauthRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return nil, errors.New("signed request failed: " + request.URL.String() + " client_secret=secret-canary access_token=token-canary")
	})
	t.Cleanup(func() { http.DefaultTransport = oldTransport })

	oldWriter := log.Writer()
	oldFlags := log.Flags()
	var output bytes.Buffer
	log.SetOutput(&output)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})

	err := doJSON(t.Context(), http.MethodGet, "https://graph.example.test/me?access_token=query-canary&client_secret=url-secret-canary", "", &map[string]any{})
	if err == nil {
		t.Fatal("custom transport error was not returned")
	}
	logOAuthFailure("INSTAGRAM", "identity", err)
	line := output.String()
	for _, secret := range []string{"query-canary", "url-secret-canary", "secret-canary", "token-canary", "graph.example.test", "access_token", "client_secret"} {
		if strings.Contains(line, secret) {
			t.Fatalf("OAuth log leaked %q: %s", secret, line)
		}
	}
	for _, field := range []string{`provider="INSTAGRAM"`, `phase="identity"`, `class="INTERNAL"`, "status=0", "correlation_id="} {
		if !strings.Contains(line, field) {
			t.Fatalf("OAuth diagnostic field %q is missing: %s", field, line)
		}
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
	if len(token.Scopes) != 0 {
		t.Fatalf("Instagram requested scopes must not be persisted as granted: %v", token.Scopes)
	}
	readiness := instagramPublishingReadiness(profile.AccountType, "", "ACTIVE", "ACTIVE", token.Scopes)
	if readiness.Compatible || !readiness.Reauth || len(readiness.MissingScopes) != 1 || readiness.MissingScopes[0] != "instagram_business_content_publish" {
		t.Fatalf("missing Instagram grant must fail publishing readiness: %#v", readiness)
	}
}

func TestConfigureInstagramFacebookAuthorizationUsesConfigInsteadOfScope(t *testing.T) {
	query := url.Values{"scope": {"must-be-removed"}}
	provider := oauthProvider{Flow: "FACEBOOK", Scopes: []string{"instagram_basic", "pages_show_list"}}

	configureInstagramAuthorization(query, provider, "config-1")

	if query.Has("scope") {
		t.Fatalf("Facebook Login for Business URL must not include scope: %s", query.Encode())
	}
	if query.Get("config_id") != "config-1" || query.Get("override_default_response_type") != "true" || query.Get("auth_type") != "rerequest" {
		t.Fatalf("unexpected Facebook Login for Business query: %s", query.Encode())
	}
}

func TestConfigureInstagramLoginUsesScopes(t *testing.T) {
	query := url.Values{}
	provider := oauthProvider{Scopes: []string{"instagram_business_basic", "instagram_business_manage_insights"}}

	configureInstagramAuthorization(query, provider, "")

	if query.Get("scope") != "instagram_business_basic,instagram_business_manage_insights" || query.Has("config_id") {
		t.Fatalf("unexpected Instagram Login query: %s", query.Encode())
	}
}

func TestPublishingOAuthProvidersRequestRequiredScopes(t *testing.T) {
	providers := (&Server{}).oauthProviders()
	tests := map[string][]string{
		"instagram":          {"instagram_business_content_publish"},
		"instagram-facebook": {"instagram_content_publish"},
		"tiktok":             {"video.publish"},
		"youtube":            {"https://www.googleapis.com/auth/youtube.upload"},
		"vk":                 {"video", "wall", "groups"},
	}
	for key, want := range tests {
		have := make(map[string]bool)
		for _, scope := range providers[key].Scopes {
			have[scope] = true
		}
		for _, scope := range want {
			if !have[scope] {
				t.Fatalf("%s OAuth scopes do not request %q: %v", key, scope, providers[key].Scopes)
			}
		}
	}
}

func TestRequestedScopesAreNotProofOfGrantedScopes(t *testing.T) {
	requested := []string{"https://www.googleapis.com/auth/youtube.readonly", "https://www.googleapis.com/auth/youtube.upload"}
	granted := splitScopes("", requested)
	if granted == nil || len(granted) != 0 {
		t.Fatalf("missing token scope must be represented as a known empty grant set, got %#v", granted)
	}

	readiness := publishingReadiness("YOUTUBE", "account", "ACTIVE", "ACTIVE", granted)
	if readiness.Compatible || !readiness.Reauth || len(readiness.MissingScopes) != 1 || readiness.MissingScopes[0] != "https://www.googleapis.com/auth/youtube.upload" {
		t.Fatalf("unknown grants must fail publishing readiness: %#v", readiness)
	}
}

func TestExplicitAndPartialGrantedScopesDriveReadiness(t *testing.T) {
	explicit := splitScopes("https://www.googleapis.com/auth/youtube.readonly https://www.googleapis.com/auth/youtube.upload", nil)
	if readiness := publishingReadiness("YOUTUBE", "account", "ACTIVE", "ACTIVE", explicit); !readiness.Compatible || readiness.Reauth || len(readiness.MissingScopes) != 0 {
		t.Fatalf("explicit publishing grant must pass readiness: %#v", readiness)
	}

	partial := splitScopes("https://www.googleapis.com/auth/youtube.readonly", []string{"https://www.googleapis.com/auth/youtube.upload"})
	readiness := publishingReadiness("YOUTUBE", "account", "ACTIVE", "ACTIVE", partial)
	if readiness.Compatible || !readiness.Reauth || len(readiness.MissingScopes) != 1 || readiness.MissingScopes[0] != "https://www.googleapis.com/auth/youtube.upload" {
		t.Fatalf("partial grant must report the exact missing publishing scope: %#v", readiness)
	}
}

func TestScopeOmissionOnReconnectAndRefreshStaysFailClosed(t *testing.T) {
	requested := []string{"video.publish", "user.info.basic"}
	for _, phase := range []string{"reconnect", "refresh"} {
		t.Run(phase, func(t *testing.T) {
			granted := splitScopes("", requested)
			readiness := publishingReadiness("TIKTOK", "account", "ACTIVE", "ACTIVE", granted)
			if readiness.Compatible || !readiness.Reauth || len(readiness.MissingScopes) != 1 || readiness.MissingScopes[0] != "video.publish" {
				t.Fatalf("%s without provider grant evidence became publish-ready: %#v", phase, readiness)
			}
		})
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
			map[string]any{"permission": "business_management", "status": "granted"},
		}})
	})
	mux.HandleFunc("/me", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("access_token") != "long-user-token" || r.URL.Query().Get("fields") != "id" {
			t.Fatal("long-lived Facebook user token was not used to load the user identity")
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "facebook-user-1"})
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
		Scopes: []string{"instagram_basic", "instagram_manage_insights", "pages_show_list", "pages_read_engagement", "business_management"},
	}
	token, profile, err := s.completeInstagramFacebookOAuth(t.Context(), provider, "code")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "page-access-token" || token.RefreshToken != "long-user-token" {
		t.Fatalf("unexpected token result: %#v", token)
	}
	if profile.ExternalID != "ig-1" || profile.Username != "creator" || profile.Metadata["facebookPageId"] != "page-1" || profile.Metadata["facebookUserId"] != "facebook-user-1" {
		t.Fatalf("unexpected profile result: %#v", profile)
	}
}

func TestDiscoverInstagramFacebookAccountsReturnsEveryLinkedPage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("grant_type") == "fb_exchange_token" {
			writeJSON(w, http.StatusOK, map[string]any{"access_token": "long-user-token", "expires_in": 5_184_000})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"access_token": "short-user-token", "expires_in": 3600})
	})
	mux.HandleFunc("/me", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"id": "facebook-user-1"})
	})
	mux.HandleFunc("/me/permissions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{
			map[string]any{"permission": "instagram_basic", "status": "granted"},
			map[string]any{"permission": "pages_show_list", "status": "granted"},
		}})
	})
	mux.HandleFunc("/me/accounts", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{
			map[string]any{"id": "page-1", "name": "First Page", "access_token": "page-token-1", "instagram_business_account": map[string]any{"id": "ig-1"}},
			map[string]any{"id": "page-2", "name": "Second Page", "access_token": "page-token-2", "instagram_business_account": map[string]any{"id": "ig-2"}},
		}})
	})
	for _, accountID := range []string{"ig-1", "ig-2"} {
		accountID := accountID
		mux.HandleFunc("/"+accountID, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"id": accountID, "username": accountID + "-username", "name": accountID + " name"})
		})
	}
	server := httptest.NewServer(mux)
	defer server.Close()
	s := &Server{config: config.Config{InstagramFacebookGraphAPIBase: server.URL}}
	provider := oauthProvider{ID: "INSTAGRAM", ClientID: "client", ClientSecret: "secret", RedirectURL: "https://app.test/callback", Flow: "FACEBOOK", Scopes: []string{"instagram_basic", "pages_show_list"}}

	candidates, err := s.discoverInstagramFacebookAccounts(t.Context(), provider, "code")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || candidates[0].Profile.ExternalID != "ig-1" || candidates[1].Profile.ExternalID != "ig-2" {
		t.Fatalf("unexpected candidates: %#v", candidates)
	}
	if candidates[0].Token.AccessToken != "page-token-1" || candidates[1].Token.AccessToken != "page-token-2" || candidates[0].Token.RefreshToken != "long-user-token" {
		t.Fatalf("unexpected candidate tokens: %#v", candidates)
	}
}

func TestFetchFacebookBusinessPortfolioPagesUsesBusinessManagementEdges(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/me/businesses", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("access_token") != "user-token" || r.URL.Query().Get("fields") != "id,name" {
			t.Fatalf("unexpected Business Portfolio request: %s", r.URL.RawQuery)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{map[string]any{"id": "business-1", "name": "Creator Portfolio"}}})
	})
	mux.HandleFunc("/business-1/owned_pages", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("access_token") != "user-token" || r.URL.Query().Get("fields") != "id,name" {
			t.Fatalf("unexpected owned Pages request: %s", r.URL.RawQuery)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{map[string]any{"id": "page-1", "name": "Creator Page"}}})
	})
	mux.HandleFunc("/page-1", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("access_token") != "user-token" {
			t.Fatal("user access token was not used to resolve the Business Page")
		}
		fields := r.URL.Query().Get("fields")
		for _, field := range []string{"access_token", "tasks", "instagram_business_account"} {
			if !strings.Contains(fields, field) {
				t.Fatalf("Business Page fields %q do not contain %q", fields, field)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "page-1", "name": "Creator Page", "access_token": "page-token",
			"instagram_business_account": map[string]any{"id": "ig-1"},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	s := &Server{config: config.Config{InstagramFacebookGraphAPIBase: server.URL}}
	pages, err := s.fetchFacebookBusinessPortfolioPages(t.Context(), "user-token")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].ID != "page-1" || pages[0].AccessToken != "page-token" || pages[0].InstagramBusinessAccount.ID != "ig-1" {
		t.Fatalf("unexpected Business Portfolio Pages: %#v", pages)
	}
}

func TestMergeFacebookPagesKeepsBusinessPageAccessToken(t *testing.T) {
	pages := mergeFacebookPages(
		[]facebookPage{{ID: "page-1", Name: "Business Page", AccessToken: "business-token"}},
		[]facebookPage{{ID: "page-1", Name: "Direct Page", AccessToken: "direct-token"}, {ID: "page-2", Name: "Direct only", AccessToken: "page-2-token"}},
	)
	if len(pages) != 2 || pages[0].AccessToken != "business-token" || pages[1].ID != "page-2" {
		t.Fatalf("unexpected merged Pages: %#v", pages)
	}
}

func TestSelectionAccountsMarksExistingAssignments(t *testing.T) {
	candidates := []instagramFacebookCandidate{
		{Profile: platformProfile{ExternalID: "available", Username: "available", Metadata: map[string]any{"facebookPageName": "Available Page"}}},
		{Profile: platformProfile{ExternalID: "current", Username: "current", Metadata: map[string]any{}}},
		{Profile: platformProfile{ExternalID: "other", Username: "other", Metadata: map[string]any{}}},
		{Profile: platformProfile{ExternalID: "invisible", Username: "provider-visible", Metadata: map[string]any{}}},
	}
	assignments := map[string]instagramAssignment{
		"current": {CreatorID: "creator-1", CreatorName: "Current creator"},
		"other":   {CreatorID: "creator-2", CreatorName: "Other creator"},
	}
	items := selectionAccounts(candidates, assignments, map[string]struct{}{"invisible": {}}, "creator-1")
	if items[0].ConnectionState != "AVAILABLE" || !items[0].Selectable || items[0].FacebookPageName != "Available Page" {
		t.Fatalf("unexpected available item: %#v", items[0])
	}
	if items[1].ConnectionState != "CONNECTED_HERE" || !items[1].Selectable {
		t.Fatalf("unexpected current item: %#v", items[1])
	}
	if items[2].ConnectionState != "CONNECTED_ELSEWHERE" || items[2].Selectable || items[2].ConnectedCreator != "Other creator" {
		t.Fatalf("unexpected assigned item: %#v", items[2])
	}
	if items[3].ConnectionState != "UNAVAILABLE" || items[3].Selectable || items[3].ConnectedCreator != "" {
		t.Fatalf("cross-company item leaked assignment details: %#v", items[3])
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
	if len(token.Scopes) != 0 {
		t.Fatalf("VK token response omitted scope but requested scopes were treated as granted: %v", token.Scopes)
	}
	readiness := publishingReadiness("VK", "account", "ACTIVE", "ACTIVE", token.Scopes)
	if readiness.Compatible || !readiness.Reauth || len(readiness.MissingScopes) != 1 || readiness.MissingScopes[0] != "video" {
		t.Fatalf("VK connection without granted-scope evidence must fail readiness: %#v", readiness)
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

func TestDefaultLifecycleTokenRevokeUsesCorrectInstagramAPI(t *testing.T) {
	tests := []struct {
		name          string
		facebookLogin bool
		standardCalls int
		facebookCalls int
	}{
		{name: "Instagram Login", standardCalls: 1},
		{name: "Facebook Login", facebookLogin: true, facebookCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			standardCalls, facebookCalls := 0, 0
			newProvider := func(counter *int) *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					*counter++
					if r.Method != http.MethodDelete || r.URL.Path != "/me/permissions" {
						t.Fatalf("unexpected revoke request %s %s", r.Method, r.URL.Path)
					}
					if r.URL.Query().Get("access_token") != "provider-token" || r.URL.Query().Has("token") {
						t.Fatalf("unexpected revoke query %s", r.URL.RawQuery)
					}
					if r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" {
						t.Fatal("Instagram revoke must not use the legacy form request")
					}
					writeJSON(w, http.StatusOK, map[string]any{"success": true})
				}))
			}
			standard := newProvider(&standardCalls)
			defer standard.Close()
			facebook := newProvider(&facebookCalls)
			defer facebook.Close()
			s := &Server{config: config.Config{InstagramAPIBase: standard.URL, InstagramFacebookGraphAPIBase: facebook.URL}}
			if err := s.defaultLifecycleTokenRevoke(t.Context(), "INSTAGRAM", "provider-token", test.facebookLogin); err != nil {
				t.Fatal(err)
			}
			if standardCalls != test.standardCalls || facebookCalls != test.facebookCalls {
				t.Fatalf("revoke calls standard=%d facebook=%d, want standard=%d facebook=%d", standardCalls, facebookCalls, test.standardCalls, test.facebookCalls)
			}
		})
	}
}

func TestDefaultLifecycleVKRevokeRejectsRedirectBeforeSecretsReachSink(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		sameHost bool
	}{
		{name: "301 cross host", status: http.StatusMovedPermanently},
		{name: "302 same host", status: http.StatusFound, sameHost: true},
		{name: "307 cross host", status: http.StatusTemporaryRedirect},
		{name: "308 same host", status: http.StatusPermanentRedirect, sameHost: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const (
				token        = "vk-token-canary"
				clientID     = "vk-client-id-canary"
				clientSecret = "vk-client-secret-canary"
			)
			var sinkCalls atomic.Int32
			var sinkBody atomic.Value
			sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sinkCalls.Add(1)
				body, _ := io.ReadAll(r.Body)
				sinkBody.Store(string(body))
				w.WriteHeader(http.StatusNoContent)
			}))
			defer sink.Close()

			mux := http.NewServeMux()
			mux.HandleFunc("/oauth2/revoke", func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("method = %s, want POST", r.Method)
				}
				if err := r.ParseForm(); err != nil {
					t.Errorf("parse revoke form: %v", err)
				}
				if r.Form.Get("token") != token || r.Form.Get("client_id") != clientID || r.Form.Get("client_secret") != clientSecret {
					t.Errorf("initial revoke request did not contain the expected credentials")
				}
				location := sink.URL + "/capture"
				if test.sameHost {
					location = "/sink"
				}
				w.Header().Set("Location", location)
				w.WriteHeader(test.status)
			})
			mux.HandleFunc("/sink", func(w http.ResponseWriter, r *http.Request) {
				sinkCalls.Add(1)
				body, _ := io.ReadAll(r.Body)
				sinkBody.Store(string(body))
				w.WriteHeader(http.StatusNoContent)
			})
			provider := httptest.NewServer(mux)
			defer provider.Close()

			s := &Server{config: config.Config{
				VKOAuthBase:    provider.URL,
				VKClientID:     clientID,
				VKClientSecret: clientSecret,
			}}
			err := s.defaultLifecycleTokenRevoke(t.Context(), "VK", token, false)
			if !errors.Is(err, errProviderRedirect) {
				t.Fatalf("error = %v, want redirect rejection", err)
			}
			if err.Error() != errProviderRedirect.Error() {
				t.Fatalf("redirect error exposed request metadata: %q", err)
			}
			for _, secret := range []string{token, clientID, clientSecret, provider.URL, sink.URL} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("redirect error leaked %q: %q", secret, err)
				}
			}
			if calls := sinkCalls.Load(); calls != 0 {
				body, _ := sinkBody.Load().(string)
				t.Fatalf("redirect sink received %d requests with body %q", calls, body)
			}
		})
	}
}

func TestDefaultLifecycleVKRevokeSuccessAndTimeout(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/oauth2/revoke" {
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("token") != "token" || r.Form.Get("client_id") != "client" || r.Form.Get("client_secret") != "secret" {
				t.Fatalf("unexpected revoke form fields")
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		defer provider.Close()

		s := &Server{config: config.Config{VKOAuthBase: provider.URL, VKClientID: "client", VKClientSecret: "secret"}}
		if err := s.defaultLifecycleTokenRevoke(t.Context(), "VK", "token", false); err != nil {
			t.Fatalf("successful revoke: %v", err)
		}
	})

	t.Run("context timeout", func(t *testing.T) {
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(150 * time.Millisecond)
			w.WriteHeader(http.StatusNoContent)
		}))
		defer provider.Close()

		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		s := &Server{config: config.Config{VKOAuthBase: provider.URL}}
		err := s.defaultLifecycleTokenRevoke(ctx, "VK", "token", false)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want context deadline exceeded", err)
		}
	})
}
