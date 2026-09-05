package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/statzavod/statzavod/internal/config"
)

func TestYouTubeResumableUploadUsesFrozenOptionsAndSession(t *testing.T) {
	var initCount, uploadCount int
	var operations []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /videos":
			initCount++
			if r.URL.Query().Get("notifySubscribers") != "true" {
				t.Errorf("notifySubscribers query missing")
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			snippet := body["snippet"].(map[string]any)
			if snippet["title"] != "frozen title" || snippet["categoryId"] != "22" {
				t.Errorf("snapshot options = %#v", snippet)
			}
			w.Header().Set("Location", "https://upload.youtube.com/session-1")
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer api.Close()
	s := &Server{config: config.Config{ContentPublishingEnabled: true, ContentYouTubeEnabled: true, YouTubeAPIBase: api.URL, YouTubeUploadBase: api.URL}}
	a := newYouTubePublishAdapter(s)
	a.sessionHTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://upload.youtube.com/session-1" {
			t.Fatalf("session URL=%s", r.URL)
		}
		if strings.HasPrefix(r.Header.Get("Content-Range"), "bytes */") {
			return testHTTPResponse(http.StatusPermanentRedirect, "", nil), nil
		}
		uploadCount++
		if !strings.HasPrefix(r.Header.Get("Content-Range"), "bytes 0-") {
			t.Errorf("range=%q", r.Header.Get("Content-Range"))
		}
		return testHTTPResponse(http.StatusOK, `{"id":"video-1"}`, nil), nil
	})
	a.accessToken = func(context.Context, PublishRequest) (string, error) { return "token", nil }
	a.mediaSize = func(context.Context, PublishRequest) (int64, error) { return 4, nil }
	a.openMedia = func(context.Context, PublishRequest, int64) (io.ReadCloser, int64, error) {
		return io.NopCloser(strings.NewReader("\x00\x01\x02\x03")), 4, nil
	}
	a.persistOperation = func(_ context.Context, request PublishRequest, op string) error {
		if (len(operations) == 0 && request.ProviderOperationID != "") || (len(operations) > 0 && request.ProviderOperationID != operations[len(operations)-1]) {
			t.Fatalf("checkpoint expected=%q operations=%#v", request.ProviderOperationID, operations)
		}
		operations = append(operations, op)
		return nil
	}
	a.persistResume = func(_ context.Context, request PublishRequest, session string) (string, error) {
		if request.ProviderOperationID != operations[len(operations)-1] || session != "https://upload.youtube.com/session-1" {
			t.Fatalf("resume checkpoint request=%q session=%q operations=%#v", request.ProviderOperationID, session, operations)
		}
		op := "yt:session:opaque-reference"
		operations = append(operations, op)
		return op, nil
	}
	a.loadResume = func(context.Context, PublishRequest) (string, error) {
		return "https://upload.youtube.com/session-1", nil
	}
	raw, _ := json.Marshal(contentPublishSnapshot{Revision: 1, MediaID: "m", MediaObjectKey: "k", PlatformOptions: json.RawMessage(`{"title":"frozen title","description":"frozen description","categoryId":"22","privacyStatus":"private","madeForKids":true,"notifySubscribers":true}`)})
	result, err := a.Publish(t.Context(), PublishRequest{Snapshot: raw})
	if err != nil || !result.Pending || initCount != 1 || uploadCount != 1 || len(operations) != 3 {
		t.Fatalf("result=%#v err=%v init=%d upload=%d operations=%#v", result, err, initCount, uploadCount, operations)
	}
	if phase, _ := parseYouTubeOperation(operations[0]); phase != "init" {
		t.Fatalf("initial operation=%q", operations[0])
	}
	if phase, _ := parseYouTubeOperation(operations[1]); phase != "session" {
		t.Fatalf("session operation=%q", operations[1])
	}
	if phase, _ := parseYouTubeOperation(operations[2]); phase != "video" {
		t.Fatalf("video operation=%q", operations[2])
	}
}

func TestYouTubeRejectsUnsafeLocationAfterPersistingCreateIntent(t *testing.T) {
	for _, location := range []string{"http://upload.youtube.com/session", "https://127.0.0.1/session", "https://user@upload.youtube.com/session", "https://evil.example/session"} {
		t.Run(location, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", location)
				w.WriteHeader(http.StatusOK)
			}))
			defer api.Close()
			a := newYouTubePublishAdapter(&Server{config: config.Config{ContentPublishingEnabled: true, ContentYouTubeEnabled: true, YouTubeUploadBase: api.URL}})
			a.accessToken = func(context.Context, PublishRequest) (string, error) { return "token-secret", nil }
			a.mediaSize = func(context.Context, PublishRequest) (int64, error) { return 4, nil }
			var operations []string
			a.persistOperation = func(_ context.Context, _ PublishRequest, operation string) error {
				operations = append(operations, operation)
				return nil
			}
			raw, _ := json.Marshal(contentPublishSnapshot{Revision: 1, MediaID: "media", MediaObjectKey: "object", PlatformOptions: json.RawMessage(`{"title":"title","categoryId":"22","privacyStatus":"private","madeForKids":true,"notifySubscribers":false}`)})
			if _, err := a.Publish(t.Context(), PublishRequest{Snapshot: raw}); err == nil || !isProviderKind(err, providerPermanent) {
				t.Fatalf("location accepted, err=%v", err)
			}
			if len(operations) != 1 || !strings.HasPrefix(operations[0], "yt:init:") {
				t.Fatalf("operations=%#v; unsafe session must not replace intent", operations)
			}
		})
	}
}

func TestYouTubeInitIntentSurvivesUncertainResponsesWithoutReplay(t *testing.T) {
	for _, tc := range []struct {
		name      string
		transport http.RoundTripper
	}{
		{name: "500", transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return testHTTPResponse(http.StatusServiceUnavailable, `{"error":"accepted but response lost"}`, nil), nil
		})},
		{name: "connection break", transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection broke after request write")
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			a := testYouTubeInitAdapter()
			a.initHTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				return tc.transport.RoundTrip(r)
			})
			persisted := ""
			a.persistOperation = func(_ context.Context, request PublishRequest, operation string) error {
				if request.ProviderOperationID != persisted {
					t.Fatalf("checkpoint expected=%q durable=%q", request.ProviderOperationID, persisted)
				}
				persisted = operation
				return nil
			}
			r := youtubeTestRequest()
			result, err := a.Publish(t.Context(), r)
			if err == nil || !isProviderKind(err, providerPermanent) || !strings.Contains(err.Error(), "manual reconciliation") {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			if result.ProviderOperationID != "yt:init:target" || persisted != result.ProviderOperationID || calls != 1 {
				t.Fatalf("result=%#v persisted=%q calls=%d", result, persisted, calls)
			}
			r.ProviderOperationID = persisted
			result, err = a.Publish(t.Context(), r)
			if err == nil || result.ProviderOperationID != persisted || calls != 1 {
				t.Fatalf("retry reissued init: result=%#v err=%v calls=%d", result, err, calls)
			}
		})
	}
}

func testYouTubeInitAdapter() *youtubePublishAdapter {
	a := newYouTubePublishAdapter(&Server{config: config.Config{ContentPublishingEnabled: true, ContentYouTubeEnabled: true, YouTubeUploadBase: "https://www.googleapis.com/upload/youtube/v3"}})
	a.accessToken = func(context.Context, PublishRequest) (string, error) { return "token", nil }
	a.mediaSize = func(context.Context, PublishRequest) (int64, error) { return 4, nil }
	return a
}

func youtubeTestRequest() PublishRequest {
	raw, _ := json.Marshal(contentPublishSnapshot{Revision: 1, MediaID: "media", MediaObjectKey: "object", PlatformOptions: json.RawMessage(`{"title":"title","categoryId":"22","privacyStatus":"private","madeForKids":true,"notifySubscribers":false}`)})
	return PublishRequest{TargetID: "target", Snapshot: raw}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testHTTPResponse(status int, body string, headers http.Header) *http.Response {
	if headers == nil {
		headers = make(http.Header)
	}
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body))}
}

func TestYouTubeFeatureFlag(t *testing.T) {
	a := newYouTubePublishAdapter(&Server{config: config.Config{ContentPublishingEnabled: true, ContentYouTubeEnabled: false}})
	result, err := a.Publish(t.Context(), PublishRequest{})
	if err == nil || result.Pending || !isProviderKind(err, providerPermanent) {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}
