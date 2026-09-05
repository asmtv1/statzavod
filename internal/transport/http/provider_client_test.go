package httpserver

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/statzavod/statzavod/internal/config"
)

func TestProviderClientRetriesRetryableResponses(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"message": "temporarily unavailable"}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))
	defer server.Close()

	var response map[string]string
	if err := newProviderClient("Test").JSON(t.Context(), http.MethodGet, server.URL, "", "", nil, &response); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || response["status"] != "ok" {
		t.Fatalf("unexpected response after %d calls: %#v", calls.Load(), response)
	}
}

func TestProviderClientOneShotNeverRetriesUncertainResponse(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"error":"accepted but response lost"}`, http.StatusServiceUnavailable)
	}))
	defer server.Close()
	err := newProviderClient("Create").JSONWithMode(t.Context(), http.MethodPost, server.URL, "", "application/json", strings.NewReader(`{}`), &map[string]any{}, providerOneShot)
	if calls.Load() != 1 || !isProviderKind(err, providerPermanent) || !strings.Contains(err.Error(), "manual reconciliation") {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
}

func TestProviderClientOneShotNeverRetriesConnectionBreak(t *testing.T) {
	var calls atomic.Int32
	client := newProviderClient("Create")
	client.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("connection broke after write")
	})
	err := client.JSONWithMode(t.Context(), http.MethodPost, "https://provider.example/create", "", "application/json", strings.NewReader(`{}`), &map[string]any{}, providerOneShot)
	if calls.Load() != 1 || !isProviderKind(err, providerPermanent) {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
}

func TestProviderClientOneShotDoesNotFollowRedirect(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "/create-again", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	err := newProviderClient("Create").JSONWithMode(t.Context(), http.MethodPost, server.URL+"/create", "", "application/json", strings.NewReader(`{}`), &map[string]any{}, providerOneShot)
	if calls.Load() != 1 || err == nil {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
}

func TestProviderClientRejectsSameHostRedirectBeforeBearerOrBodyReachSink(t *testing.T) {
	var sinkCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/sink", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/sink", func(w http.ResponseWriter, r *http.Request) {
		sinkCalls.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Errorf("redirect sink received authorization")
		}
		body, _ := io.ReadAll(r.Body)
		if len(body) != 0 {
			t.Errorf("redirect sink received body")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	err := newProviderClient("Test").JSON(t.Context(), http.MethodPost, server.URL+"/start", "bearer-canary", "application/json", strings.NewReader(`{"secret":"body-canary"}`), &map[string]any{})
	if err == nil {
		t.Fatal("same-host redirect was accepted")
	}
	if sinkCalls.Load() != 0 {
		t.Fatalf("redirect sink received %d requests", sinkCalls.Load())
	}
}

func TestProviderURLSecurityMatrix(t *testing.T) {
	for _, value := range []string{
		"http://upload.youtube.com/session",
		"https://localhost/session",
		"https://127.0.0.1/session",
		"https://10.0.0.1/session",
		"https://169.254.1.1/session",
		"https://user@upload.youtube.com/session",
		"https://upload.youtube.com:8443/session",
		"https://upload.youtube.com.evil.example/session",
	} {
		if err := validateYouTubeSessionURL(value); err == nil {
			t.Errorf("unsafe URL accepted: %s", value)
		}
	}
	for _, value := range []string{"https://upload.youtube.com/session", "https://www.googleapis.com/upload/youtube/v3/videos"} {
		if err := validateYouTubeSessionURL(value); err != nil {
			t.Errorf("official URL rejected: %s: %v", value, err)
		}
	}
	for _, value := range []string{"https://pu.vk.com/upload.php", "https://cs123.vkuserlive.net/upload"} {
		if err := validateVKUploadURL(value); err != nil {
			t.Errorf("official VK URL rejected: %s: %v", value, err)
		}
	}
	if err := validateVKUploadURL("https://vk.com.evil.example/upload"); err == nil {
		t.Fatal("foreign VK lookalike accepted")
	}
}

func TestValidatedProviderClientRejectsCrossHostRedirectBeforeSecrets(t *testing.T) {
	var foreignCalls atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignCalls.Add(1)
	}))
	defer foreign.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	client := newValidatedProviderHTTPClient(validateVKUploadURL)
	req, _ := http.NewRequest(http.MethodPost, origin.URL+"/upload", strings.NewReader("media-secret"))
	req.Header.Set("Authorization", "Bearer token-secret")
	resp, err := client.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), errProviderRedirect.Error()) {
		t.Fatalf("redirect err=%v", err)
	}
	if foreignCalls.Load() != 0 {
		t.Fatalf("foreign host received %d requests", foreignCalls.Load())
	}
}

func TestProviderClientClassifiesAuthorization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"message": "expired token"}})
	}))
	defer server.Close()

	err := newProviderClient("Test").JSON(t.Context(), http.MethodGet, server.URL, "", "", nil, &map[string]any{})
	if !isProviderKind(err, providerAuth) {
		t.Fatalf("expected auth error, got %v", err)
	}
}

func TestProviderClientClassifiesOAuthInvalidGrantAsAuthorization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
	}))
	defer server.Close()

	err := newProviderClient("Test").JSON(t.Context(), http.MethodPost, server.URL, "", "application/x-www-form-urlencoded", strings.NewReader("grant_type=refresh_token"), &map[string]any{})
	if !isProviderKind(err, providerAuth) {
		t.Fatalf("expected auth error, got %v", err)
	}
}

func TestProviderClientClassifiesMetaInvalidTokenAsAuthorization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": 190, "message": "Invalid OAuth access token"}})
	}))
	defer server.Close()

	err := newProviderClient("Instagram").JSON(t.Context(), http.MethodGet, server.URL, "", "", nil, &map[string]any{})
	if !isProviderKind(err, providerAuth) {
		t.Fatalf("expected auth error, got %v", err)
	}
}

func TestVerifyInstagramSignedRequest(t *testing.T) {
	const secret = "secret"
	payload, _ := json.Marshal(map[string]string{"algorithm": "HMAC-SHA256", "user_id": "42"})
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(encodedPayload))
	signed := base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) + "." + encodedPayload

	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(url.Values{"signed_request": {signed}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	server := &Server{config: config.Config{InstagramClientSecret: secret}}
	parsed, err := server.verifyInstagramSignedRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.UserID != "42" {
		t.Fatalf("unexpected user id %q", parsed.UserID)
	}
}

func TestParseISO8601Duration(t *testing.T) {
	if got := parseISO8601Duration("PT1H2M3S").Seconds(); got != 3723 {
		t.Fatalf("expected 3723 seconds, got %.0f", got)
	}
	if got := parseISO8601Duration("P1DT2M").Hours(); got != 24+(2.0/60.0) {
		t.Fatalf("unexpected duration %.2f hours", got)
	}
}

func TestInstagramInsightValue(t *testing.T) {
	response := instagramInsightResponse{}
	itemJSON := `{"data":[{"name":"reach","total_value":{"value":123}}]}`
	if err := json.Unmarshal([]byte(itemJSON), &response); err != nil {
		t.Fatal(err)
	}
	value := instagramInsightValue(response)
	if value == nil || *value != 123 {
		t.Fatalf("unexpected insight value %#v", value)
	}
}
