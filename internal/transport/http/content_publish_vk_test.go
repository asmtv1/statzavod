package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/statzavod/statzavod/internal/config"
)

func TestVKOperationPhasesAreUnambiguous(t *testing.T) {
	op := vkOp("save", "target-1")
	if phase, parts := parseVKOperation(op); phase != "save" || len(parts) != 1 {
		t.Fatalf("save operation: %q %#v", phase, parts)
	}
	op = vkOp("video", "-42:17")
	if phase, parts := parseVKOperation(op); phase != "video" || len(parts) != 2 || parts[0] != "-42" || parts[1] != "17" {
		t.Fatalf("video operation: %q %#v", phase, parts)
	}
	if got := parseVKOwnerID("42", "community"); got != -42 {
		t.Fatalf("community owner=%d", got)
	}
}

func TestVKPreflightRequiresVideoScopeAndOwner(t *testing.T) {
	missing := publishingReadiness("VK", "1", "ACTIVE", "ACTIVE", nil)
	if missing.Compatible || len(missing.MissingScopes) != 1 || missing.MissingScopes[0] != "video" {
		t.Fatalf("missing scope result=%#v", missing)
	}
	ready := publishingReadiness("VK", "1", "ACTIVE", "ACTIVE", []string{"video"})
	if !ready.Compatible || len(ready.Required) == 0 {
		t.Fatalf("ready result=%#v", ready)
	}
}

func TestVKCreateOperationsAreOneShotAndFailClosed(t *testing.T) {
	var saveCalls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/method/video.save" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		saveCalls.Add(1)
		http.Error(w, `{"error":{"error_code":10,"error_msg":"accepted but response lost"}}`, http.StatusInternalServerError)
	}))
	defer api.Close()
	s := &Server{config: config.Config{ContentPublishingEnabled: true, ContentVKEnabled: true, VKAPIBase: api.URL, VKAPIVersion: "5.199"}}
	a := newVKPublishAdapter(s)
	a.accessToken = func(context.Context, PublishRequest) (string, error) { return "secret", nil }
	var persisted string
	a.persist = func(_ context.Context, _ PublishRequest, op string) error { persisted = op; return nil }
	raw, _ := json.Marshal(contentPublishSnapshot{Revision: 1, Caption: "video", MediaID: "media-1", MediaObjectKey: "ready/video.mp4", AccountExternalID: "42", AccountType: "user", PlatformOptions: json.RawMessage(`{"owner":"user","ownerId":42,"title":"video","privacy":"public"}`)})
	if _, err := a.Publish(t.Context(), PublishRequest{TargetID: "target-1", Snapshot: raw}); err == nil || !isProviderKind(err, providerPermanent) {
		t.Fatalf("save err=%v", err)
	}
	if saveCalls.Load() != 1 || persisted == "" {
		t.Fatalf("save calls=%d persisted=%q", saveCalls.Load(), persisted)
	}
	if _, err := a.Poll(t.Context(), PublishRequest{ProviderOperationID: persisted}); err == nil || !isProviderKind(err, providerPermanent) {
		t.Fatalf("save intent did not fail closed: %v", err)
	}
	if saveCalls.Load() != 1 {
		t.Fatalf("video.save replayed: %d", saveCalls.Load())
	}
}

func TestVKWallPostIsOneShotAndIntentFailsClosed(t *testing.T) {
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/method/wall.post" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		calls.Add(1)
		http.Error(w, `{"error":{"error_code":10,"error_msg":"accepted"}}`, http.StatusBadGateway)
	}))
	defer api.Close()
	s := &Server{config: config.Config{ContentPublishingEnabled: true, ContentVKEnabled: true, VKAPIBase: api.URL, VKAPIVersion: "5.199"}}
	a := newVKPublishAdapter(s)
	a.accessToken = func(context.Context, PublishRequest) (string, error) { return "secret", nil }
	var persisted string
	a.persist = func(_ context.Context, _ PublishRequest, op string) error { persisted = op; return nil }
	raw, _ := json.Marshal(contentPublishSnapshot{PlatformOptions: json.RawMessage(`{"wallPost":true}`)})
	if _, err := a.Poll(t.Context(), PublishRequest{TargetID: "target-1", ProviderOperationID: vkOp("video", "42:7"), Snapshot: raw}); err == nil || !isProviderKind(err, providerPermanent) {
		t.Fatalf("wall post err=%v", err)
	}
	if calls.Load() != 1 || persisted == "" {
		t.Fatalf("wall calls=%d persisted=%q", calls.Load(), persisted)
	}
	if _, err := a.Poll(t.Context(), PublishRequest{ProviderOperationID: persisted}); err == nil || !isProviderKind(err, providerPermanent) {
		t.Fatalf("wall intent did not fail closed: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("wall.post replayed: %d", calls.Load())
	}
}
