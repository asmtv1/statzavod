package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/statzavod/statzavod/internal/config"
)

func TestTikTokDirectPostContract(t *testing.T) {
	requests := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer redacted-test-token" {
			t.Fatalf("authorization = %q", got)
		}
		if r.Header.Get("Content-Type") != "application/json; charset=UTF-8" {
			t.Fatalf("content type = %q", r.Header.Get("Content-Type"))
		}
		switch r.URL.Path {
		case "/v2/post/publish/creator_info/query/":
			requests++
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"creator_username": "creator", "privacy_level_options": []string{"SELF_ONLY"}, "comment_disabled": false, "duet_disabled": false, "stitch_disabled": true, "max_video_post_duration_sec": 60}, "error": map[string]string{"code": "ok"}})
		case "/v2/post/publish/video/init/":
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			post := in["post_info"].(map[string]any)
			source := in["source_info"].(map[string]any)
			if post["privacy_level"] != "SELF_ONLY" || post["disable_comment"] != false || post["disable_duet"] != false || post["disable_stitch"] != true {
				t.Fatalf("unexpected post options: %#v", post)
			}
			if source["source"] != "PULL_FROM_URL" || !strings.HasPrefix(source["video_url"].(string), "https://media.statzavod.ru/") {
				t.Fatalf("unexpected source: %#v", source)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"publish_id": "publish-redacted"}, "error": map[string]string{"code": "ok"}})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()
	a := testTikTokAdapter(api.URL, true)
	a.creatorInfo = a.queryCreatorInfo
	a.loadTarget = func(context.Context, PublishRequest) (string, tiktokTargetOptions, int64, error) {
		return "caption", selectedTikTokOptions(), 30_000, nil
	}
	a.loadMedia = func(context.Context, PublishRequest) (string, int64, string, error) {
		return "media", 30_000, "ready/video.mp4", nil
	}
	a.providerMediaURL = func(context.Context, string) (string, error) {
		return "https://media.statzavod.ru/ready/video.mp4?X-Amz-Signature=redacted", nil
	}
	result, err := a.Publish(t.Context(), PublishRequest{OrganizationID: "org", CompanyID: "company", AccountID: "account", TargetID: "target"})
	if err != nil || !result.Pending || result.ProviderOperationID != "publish-redacted" || requests != 1 {
		t.Fatalf("Publish() = %#v, %v; creator requests=%d", result, err, requests)
	}
}

func TestTikTokPollContractAndTerminalErrors(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/post/publish/status/fetch/" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["publish_id"] != "publish-redacted" {
			t.Fatalf("body=%#v", in)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"status": "PUBLISH_COMPLETE", "publicaly_available_post_id": "video-redacted"}, "error": map[string]string{"code": "ok"}})
	}))
	defer api.Close()
	a := testTikTokAdapter(api.URL, true)
	result, err := a.Poll(t.Context(), PublishRequest{OrganizationID: "org", CompanyID: "company", AccountID: "account", ProviderOperationID: "publish-redacted"})
	if err != nil || result.ExternalID != "video-redacted" || result.Pending {
		t.Fatalf("Poll() = %#v, %v", result, err)
	}
}

func TestTikTokRejectsMissingConsentAndUnavailableInteraction(t *testing.T) {
	creator := tiktokCreatorInfo{PrivacyLevelOptions: []string{"SELF_ONLY"}, DuetDisabled: true}
	options := selectedTikTokOptions()
	options.ExplicitConsent = false
	if err := validateTikTokOptions(options, creator, 1000); err == nil {
		t.Fatal("missing explicit consent was accepted")
	}
	options = selectedTikTokOptions()
	enabled := true
	options.DuetEnabled = &enabled
	if err := validateTikTokOptions(options, creator, 1000); err == nil {
		t.Fatal("unavailable Duet was accepted")
	}
	options = selectedTikTokOptions()
	options.PrivacyLevel = ""
	if err := validateTikTokOptions(options, tiktokCreatorInfo{PrivacyLevelOptions: []string{"SELF_ONLY"}}, 1000); err == nil {
		t.Fatal("privacy default was accepted")
	}
}

func TestTikTokFeatureFlagAndPullURLValidation(t *testing.T) {
	a := testTikTokAdapter("https://example.invalid", false)
	if _, err := a.Publish(t.Context(), PublishRequest{}); err == nil {
		t.Fatal("disabled TikTok adapter published")
	}
	for _, value := range []string{"http://media.statzavod.ru/video.mp4", "https:///video.mp4", "https://user@media.statzavod.ru/video.mp4"} {
		if err := validateTikTokPullURL(value); err == nil {
			t.Fatalf("invalid URL accepted: %s", value)
		}
	}
	if err := validateTikTokPullURL("https://media.statzavod.ru/video.mp4?X-Amz-Signature=redacted"); err != nil {
		t.Fatal(err)
	}
}

func TestTikTokProviderErrorAndNoDuplicateAfterTimeout(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/post/publish/status/fetch/" {
			http.Error(w, `{"error":{"code":"internal_error","message":"temporary"}}`, http.StatusBadGateway)
			return
		}
		t.Fatalf("unexpected init; a known publish_id must only poll")
	}))
	defer api.Close()
	a := testTikTokAdapter(api.URL, true)
	if _, err := a.Poll(t.Context(), PublishRequest{OrganizationID: "org", CompanyID: "company", AccountID: "account", ProviderOperationID: "publish-redacted"}); err == nil {
		t.Fatal("retryable provider error was swallowed")
	}
}

func TestTikTokProviderPayloadCanariesNeverEscape(t *testing.T) {
	canaries := []string{
		"https://evil.example/callback?access_token=token-canary&sig=signed-query-canary",
		"Bearer access-token-canary",
		"b3BhcXVlLWJhc2U2NC1jYW5hcnktaWRlbnRpZmllcg==",
		"opaque-provider-id-canary-01HZZZZZZZZZZZZZZZZZZZZZZZ",
	}
	raw := strings.Join(canaries, " ")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/post/publish/video/init/":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":             map[string]string{"code": "scope_not_authorized", "message": raw, "log_id": canaries[3]},
				"error_description": raw,
				"log_id":            canaries[3],
			})
		case "/v2/post/publish/status/fetch/":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"status": "FAILED", "fail_reason": raw}})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	a := testTikTokAdapter(api.URL, true)
	a.creatorInfo = func(context.Context, string) (tiktokCreatorInfo, error) {
		return tiktokCreatorInfo{PrivacyLevelOptions: []string{"SELF_ONLY"}, MaxVideoPostDurationSeconds: 60}, nil
	}
	a.loadTarget = func(context.Context, PublishRequest) (string, tiktokTargetOptions, int64, error) {
		return "caption", selectedTikTokOptions(), 1_000, nil
	}
	a.loadMedia = func(context.Context, PublishRequest) (string, int64, string, error) {
		return "media", 1_000, "ready/video.mp4", nil
	}
	a.providerMediaURL = func(context.Context, string) (string, error) {
		return "https://media.statzavod.ru/video.mp4", nil
	}

	_, initErr := a.Publish(t.Context(), PublishRequest{TargetID: "target"})
	_, pollErr := a.Poll(t.Context(), PublishRequest{ProviderOperationID: "publish-safe"})
	for _, err := range []error{initErr, pollErr} {
		if err == nil {
			t.Fatal("provider failure was swallowed")
		}
		persisted := errorMessage(err)
		public := publicContentAttemptMessage(persisted.(string))
		var captured bytes.Buffer
		oldWriter, oldFlags := log.Writer(), log.Flags()
		log.SetOutput(&captured)
		log.SetFlags(0)
		log.Printf("content publish: %v", err)
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
		combined := err.Error() + " " + persisted.(string) + " " + public + " " + captured.String()
		for _, canary := range canaries {
			if strings.Contains(combined, canary) {
				t.Fatalf("provider canary escaped boundary: %q in %q", canary, combined)
			}
		}
	}

	// A custom adapter cannot bypass the worker's persistence boundary.
	unsafe := &providerError{Platform: "TikTok", Kind: providerRetryable, Message: raw}
	if got := errorMessage(unsafe).(string); got != safeTikTokProviderMessage(providerRetryable) {
		t.Fatalf("worker persistence sanitizer=%q", got)
	}
}

func TestTikTokInitIsOneShotAndIntentFailsClosed(t *testing.T) {
	var initCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/post/publish/video/init/" {
			initCalls++
			http.Error(w, `{"error":{"code":"internal_error","message":"accepted but response lost"}}`, http.StatusInternalServerError)
			return
		}
		t.Fatalf("unexpected path %s", r.URL.Path)
	}))
	defer api.Close()
	a := testTikTokAdapter(api.URL, true)
	a.creatorInfo = func(context.Context, string) (tiktokCreatorInfo, error) {
		return tiktokCreatorInfo{PrivacyLevelOptions: []string{"SELF_ONLY"}, MaxVideoPostDurationSeconds: 60}, nil
	}
	a.loadTarget = func(context.Context, PublishRequest) (string, tiktokTargetOptions, int64, error) {
		return "caption", selectedTikTokOptions(), 1_000, nil
	}
	a.loadMedia = func(context.Context, PublishRequest) (string, int64, string, error) {
		return "m", 1_000, "ready/video.mp4", nil
	}
	a.providerMediaURL = func(context.Context, string) (string, error) { return "https://media.statzavod.ru/video.mp4", nil }
	var persisted string
	a.persistOperation = func(_ context.Context, _ PublishRequest, op string) error { persisted = op; return nil }

	if _, err := a.Publish(t.Context(), PublishRequest{TargetID: "target-1"}); err == nil || !isProviderKind(err, providerPermanent) {
		t.Fatalf("one-shot init err=%v", err)
	}
	if initCalls != 1 || persisted != "tiktok:init:target-1" {
		t.Fatalf("init calls=%d persisted=%q", initCalls, persisted)
	}
	if _, err := a.Poll(t.Context(), PublishRequest{ProviderOperationID: persisted}); err == nil || !isProviderKind(err, providerPermanent) {
		t.Fatalf("intent poll did not fail closed: %v", err)
	}
	if initCalls != 1 {
		t.Fatalf("init was replayed: %d", initCalls)
	}
}

func TestTikTokUsesFrozenPublishSnapshot(t *testing.T) {
	options, _ := json.Marshal(selectedTikTokOptions())
	raw, _ := json.Marshal(contentPublishSnapshot{Revision: 3, Caption: "frozen caption", PlatformOptions: options, MediaID: "media", MediaObjectKey: "immutable/video.mp4", MediaDurationMS: 30_000})
	a := newTikTokPublishAdapter(&Server{})
	caption, parsed, duration, err := a.defaultLoadTarget(t.Context(), PublishRequest{Snapshot: raw})
	if err != nil || caption != "frozen caption" || duration != 30_000 || parsed.PrivacyLevel != "SELF_ONLY" {
		t.Fatalf("target=%q %#v %d %v", caption, parsed, duration, err)
	}
	mediaID, mediaDuration, key, err := a.loadTargetMedia(t.Context(), PublishRequest{Snapshot: raw})
	if err != nil || mediaID != "media" || mediaDuration != 30_000 || key != "immutable/video.mp4" {
		t.Fatalf("media=%q %d %q %v", mediaID, mediaDuration, key, err)
	}
}

func testTikTokAdapter(apiBase string, enabled bool) *tiktokPublishAdapter {
	s := &Server{config: config.Config{ContentPublishingEnabled: true, ContentTikTokEnabled: enabled, TikTokAPIBase: apiBase, MediaPublicBaseURL: "https://media.statzavod.ru"}}
	a := newTikTokPublishAdapter(s)
	a.accessToken = func(context.Context, PublishRequest) (string, error) { return "redacted-test-token", nil }
	a.persistOperation = func(context.Context, PublishRequest, string) error { return nil }
	return a
}
func selectedTikTokOptions() tiktokTargetOptions {
	comments, duet, stitch := true, true, false
	return tiktokTargetOptions{PrivacyLevel: "SELF_ONLY", CommentsEnabled: &comments, DuetEnabled: &duet, StitchEnabled: &stitch, ExplicitConsent: true, MusicUsageConfirmed: true, CommercialDisclosure: &struct {
		BrandContent bool `json:"brandContent"`
		BrandOrganic bool `json:"brandOrganic"`
	}{}}
}
