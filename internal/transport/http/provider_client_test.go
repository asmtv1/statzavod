package httpserver

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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
