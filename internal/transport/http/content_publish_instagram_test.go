package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/statzavod/statzavod/internal/config"
)

func TestInstagramReelsContainerThenPublishThenPermalink(t *testing.T) {
	var creates, publishes int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer redacted-test-token" {
			t.Fatalf("missing bearer auth")
		}
		switch r.URL.Path {
		case "/ig-user/media":
			creates++
			_ = r.ParseForm()
			if r.Form.Get("media_type") != "REELS" || r.Form.Get("caption") != "exact sent caption" || !strings.HasPrefix(r.Form.Get("video_url"), "https://media.statzavod.ru/") {
				t.Fatalf("container form = %#v", r.Form)
			}
			_, _ = w.Write([]byte(`{"id":"container-1"}`))
		case "/container-1":
			_, _ = w.Write([]byte(`{"status_code":"FINISHED"}`))
		case "/ig-user/media_publish":
			publishes++
			_ = r.ParseForm()
			if r.Form.Get("creation_id") != "container-1" {
				t.Fatalf("publish form=%#v", r.Form)
			}
			_, _ = w.Write([]byte(`{"id":"media-1"}`))
		case "/media-1":
			_, _ = w.Write([]byte(`{"id":"media-1","permalink":"https://www.instagram.com/reel/media-1/"}`))
		default:
			t.Fatalf("unexpected %s", r.URL.Path)
		}
	}))
	defer api.Close()
	a := testInstagramAdapter(api.URL, true, "BUSINESS")
	first, err := a.Publish(t.Context(), instagramRequest())
	if err != nil || !first.Pending || first.ProviderOperationID != "ig:container:container-1" || creates != 1 {
		t.Fatalf("Publish=%#v err=%v creates=%d", first, err, creates)
	}
	second, err := a.Poll(t.Context(), instagramRequestWithOperation(first.ProviderOperationID))
	if err != nil || !second.Pending || second.ProviderOperationID != "ig:media:media-1" || publishes != 1 {
		t.Fatalf("container Poll=%#v err=%v publishes=%d", second, err, publishes)
	}
	final, err := a.Poll(t.Context(), instagramRequestWithOperation(second.ProviderOperationID))
	if err != nil || final.Pending || final.ExternalID != "media-1" || final.ExternalURL == "" {
		t.Fatalf("media Poll=%#v err=%v", final, err)
	}
}

func TestInstagramContainerProcessingAndRejection(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		wantErr     bool
	}{{"processing", "IN_PROGRESS", false}, {"rejected", "ERROR", true}} {
		t.Run(tc.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"status_code":"` + tc.state + `"}`))
			}))
			defer api.Close()
			a := testInstagramAdapter(api.URL, true, "CREATOR")
			result, err := a.Poll(t.Context(), instagramRequestWithOperation("ig:container:container-1"))
			if (err != nil) != tc.wantErr {
				t.Fatalf("Poll=%#v err=%v", result, err)
			}
			if !tc.wantErr && (!result.Pending || result.ProviderOperationID != "ig:container:container-1") {
				t.Fatalf("processing result=%#v", result)
			}
		})
	}
}

func TestInstagramAuthExpiryAndTimeoutAreNormalized(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   providerErrorKind
	}{{"expired", 400, providerAuth}, {"timeout", 503, providerRetryable}} {
		t.Run(tc.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				payload := `{"error":{"code":190,"message":"token=secret"}}`
				if tc.status >= 500 {
					payload = `{"error":{"code":2,"message":"token=secret"}}`
				}
				http.Error(w, payload, tc.status)
			}))
			defer api.Close()
			a := testInstagramAdapter(api.URL, true, "BUSINESS")
			_, err := a.Poll(t.Context(), instagramRequestWithOperation("ig:container:container-1"))
			var pe *providerError
			if !errorsAsProvider(err, &pe) || pe.Kind != tc.want || strings.Contains(pe.Message, "secret") {
				t.Fatalf("error not normalized: %#v", err)
			}
		})
	}
}

func TestInstagramKnownContainerNeverCreatesAnother(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ig-user/media" {
			t.Fatal("known container must only poll")
		}
		_, _ = w.Write([]byte(`{"status_code":"IN_PROGRESS"}`))
	}))
	defer api.Close()
	a := testInstagramAdapter(api.URL, true, "BUSINESS")
	if _, err := a.Poll(t.Context(), instagramRequestWithOperation("ig:container:known")); err != nil {
		t.Fatal(err)
	}
}

func TestInstagramRejectsPersonalAndUsesFeatureFlag(t *testing.T) {
	a := testInstagramAdapter("https://example.invalid", true, "PERSONAL")
	if _, err := a.Publish(t.Context(), instagramRequest()); err == nil {
		t.Fatal("personal account published")
	}
	disabled := testInstagramAdapter("https://example.invalid", false, "BUSINESS")
	if _, err := disabled.Publish(t.Context(), instagramRequest()); err == nil {
		t.Fatal("disabled Instagram adapter published")
	}
}

func testInstagramAdapter(apiBase string, enabled bool, accountType string) *instagramPublishAdapter {
	s := &Server{config: config.Config{ContentPublishingEnabled: true, ContentInstagramEnabled: enabled, InstagramAPIBase: apiBase, MediaPublicBaseURL: "https://media.statzavod.ru"}}
	a := newInstagramPublishAdapter(s)
	a.accessToken = func(context.Context, PublishRequest) (string, error) { return "redacted-test-token", nil }
	a.loadTarget = func(context.Context, PublishRequest) (instagramPublishTarget, error) {
		return instagramPublishTarget{AccountExternalID: "ig-user", AccountType: accountType, Caption: "exact sent caption", ObjectKey: "ready/video.mp4"}, nil
	}
	a.providerMediaURL = func(context.Context, string) (string, error) {
		return "https://media.statzavod.ru/ready/video.mp4?X-Amz-Signature=redacted", nil
	}
	a.persistDispatch = func(context.Context, PublishRequest, string, string) error { return nil }
	return a
}
func instagramRequest() PublishRequest {
	return PublishRequest{OrganizationID: "org", CompanyID: "company", AccountID: "account", TargetID: "target"}
}
func instagramRequestWithOperation(operation string) PublishRequest {
	r := instagramRequest()
	r.ProviderOperationID = operation
	return r
}
func errorsAsProvider(err error, target **providerError) bool { return errors.As(err, target) }

func TestInstagramUsesFrozenPublishSnapshot(t *testing.T) {
	raw, _ := json.Marshal(contentPublishSnapshot{Revision: 7, Caption: "frozen caption", PlatformOptions: json.RawMessage(`{}`), MediaID: "media", MediaObjectKey: "hash/video.mp4", AccountExternalID: "ig-user", AccountType: "BUSINESS", ConnectionMode: "FACEBOOK"})
	a := newInstagramPublishAdapter(&Server{})
	target, err := a.defaultLoadTarget(t.Context(), PublishRequest{Snapshot: raw})
	if err != nil || target.Caption != "frozen caption" || target.ObjectKey != "hash/video.mp4" || target.ConnectionMode != "FACEBOOK" {
		t.Fatalf("snapshot target=%#v err=%v", target, err)
	}
}

func TestInstagramDispatchRecoveryFailsClosedWithoutSecondPublish(t *testing.T) {
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		t.Fatalf("provider call %s must not occur after durable dispatch intent", r.URL.Path)
	}))
	defer api.Close()
	a := testInstagramAdapter(api.URL, true, "BUSINESS")
	result, err := a.Poll(t.Context(), instagramRequestWithOperation("ig:publish-dispatched:container-1"))
	if err == nil || result.ProviderOperationID != "ig:publish-dispatched:container-1" || calls != 0 {
		t.Fatalf("recovery result=%#v err=%v calls=%d", result, err, calls)
	}
}

func TestInstagramCrashAfterPublishDispatchDoesNotRepeatMediaPublish(t *testing.T) {
	publishCalls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/container-1":
			_, _ = w.Write([]byte(`{"status_code":"FINISHED"}`))
		case "/ig-user/media_publish":
			publishCalls++
			http.Error(w, `{"error":{"code":2}}`, http.StatusBadGateway) // accepted outcome is unknown
		default:
			t.Fatalf("unexpected recovery request %s", r.URL.Path)
		}
	}))
	defer api.Close()
	a := testInstagramAdapter(api.URL, true, "BUSINESS")
	persisted := ""
	a.persistDispatch = func(_ context.Context, _ PublishRequest, expected, intent string) error {
		if expected != "ig:container:container-1" {
			t.Fatalf("expected operation=%q", expected)
		}
		persisted = intent
		return nil
	}
	result, err := a.Poll(t.Context(), instagramRequestWithOperation("ig:container:container-1"))
	if err == nil || result.ProviderOperationID != "ig:publish-dispatched:container-1" || persisted != result.ProviderOperationID || publishCalls != 1 {
		t.Fatalf("dispatch result=%#v err=%v persisted=%q calls=%d", result, err, persisted, publishCalls)
	}
	_, err = a.Poll(t.Context(), instagramRequestWithOperation(persisted))
	if err == nil || publishCalls != 1 {
		t.Fatalf("recovery reissued media_publish: err=%v calls=%d", err, publishCalls)
	}
}

func TestInstagramContainerCreateIntentSurvivesUncertainResponse(t *testing.T) {
	createCalls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		createCalls++
		http.Error(w, `{"error":{"code":2}}`, http.StatusServiceUnavailable)
	}))
	defer api.Close()
	a := testInstagramAdapter(api.URL, true, "BUSINESS")
	persisted := ""
	a.persistDispatch = func(_ context.Context, _ PublishRequest, expected, next string) error {
		if expected != persisted {
			t.Fatalf("checkpoint expected=%q durable=%q", expected, persisted)
		}
		persisted = next
		return nil
	}
	result, err := a.Publish(t.Context(), instagramRequest())
	if err == nil || !isProviderKind(err, providerPermanent) || !strings.Contains(err.Error(), "manual reconciliation") {
		t.Fatalf("uncertain create result=%#v err=%v", result, err)
	}
	if result.ProviderOperationID != "ig:create:target" || persisted != result.ProviderOperationID || createCalls != 1 {
		t.Fatalf("result=%#v persisted=%q calls=%d", result, persisted, createCalls)
	}
	result, err = a.Publish(t.Context(), instagramRequestWithOperation(persisted))
	if err == nil || result.ProviderOperationID != persisted || createCalls != 1 {
		t.Fatalf("retry reissued create: result=%#v err=%v calls=%d", result, err, createCalls)
	}
}

func TestInstagramContainerCreateConnectionBreakIsFailClosed(t *testing.T) {
	var createCalls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		createCalls.Add(1)
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("test server does not support connection hijacking")
		}
		connection, _, err := hijacker.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		_ = connection.Close() // request acceptance before EOF is unknowable
	}))
	defer api.Close()
	a := testInstagramAdapter(api.URL, true, "BUSINESS")
	persisted := ""
	a.persistDispatch = func(_ context.Context, _ PublishRequest, expected, next string) error {
		if expected != persisted {
			t.Fatalf("checkpoint expected=%q durable=%q", expected, persisted)
		}
		persisted = next
		return nil
	}
	result, err := a.Publish(t.Context(), instagramRequest())
	if err == nil || !isProviderKind(err, providerPermanent) || !strings.Contains(err.Error(), "manual reconciliation") || persisted != "ig:create:target" || result.ProviderOperationID != persisted || createCalls.Load() != 1 {
		t.Fatalf("result=%#v err=%v persisted=%q calls=%d", result, err, persisted, createCalls.Load())
	}
	_, err = a.Publish(t.Context(), instagramRequestWithOperation(persisted))
	if err == nil || createCalls.Load() != 1 {
		t.Fatalf("retry reissued create: err=%v calls=%d", err, createCalls.Load())
	}
}

func TestInstagramContainerSuccessReplacesIntent(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"container-1"}`))
	}))
	defer api.Close()
	a := testInstagramAdapter(api.URL, true, "BUSINESS")
	persisted := ""
	a.persistDispatch = func(_ context.Context, _ PublishRequest, expected, next string) error {
		if expected != persisted {
			t.Fatalf("checkpoint expected=%q durable=%q", expected, persisted)
		}
		persisted = next
		return nil
	}
	result, err := a.Publish(t.Context(), instagramRequest())
	if err != nil || result.ProviderOperationID != "ig:container:container-1" || persisted != result.ProviderOperationID {
		t.Fatalf("result=%#v persisted=%q err=%v", result, persisted, err)
	}
}
