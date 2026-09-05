package httpserver

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPublicAttemptResponseRedactsCapabilityValues(t *testing.T) {
	rawURL := "https://pu.vk.com/upload.php?capability=secret"
	rawBase64 := "c2Vzc2lvbi11cmwtd2l0aC1zZWNyZXQtY2FwYWJpbGl0eQ"
	for _, raw := range []string{rawURL, rawBase64, "provider_operation_id=" + rawBase64} {
		message := publicContentAttemptMessage("provider failure: " + raw)
		response := httptest.NewRecorder()
		writeJSON(response, 200, map[string]any{"items": []map[string]any{{"id": "attempt-safe-id", "status": "FAILED", "errorMessage": message}}})
		body := response.Body.String()
		if strings.Contains(body, raw) || strings.Contains(body, "providerOperationId") || strings.Contains(body, "provider_operation_id") {
			t.Fatalf("attempt response leaked internal capability: %s", body)
		}
		if !strings.Contains(body, "attempt-safe-id") {
			t.Fatalf("safe attempt id missing: %s", body)
		}
	}
}

func TestContentDetailTargetsAreRedactedForManagementAndCreator(t *testing.T) {
	rawURL := "https://storage.example/private/object?X-Amz-Credential=secret"
	rawToken := "c2Vzc2lvbi11cmwtd2l0aC1zZWNyZXQtY2FwYWJpbGl0eQ"
	for _, role := range []string{"management", "creator"} {
		t.Run(role, func(t *testing.T) {
			target := publicContentTarget("target-id", "TIKTOK", "account-id", "FAILED", rawToken, rawURL, "media-id", rawToken, rawURL+" provider_operation_id="+rawToken, nil, time.Unix(1, 0), time.Unix(2, 0))
			body, err := json.Marshal(map[string]any{"role": role, "targets": []map[string]any{target}})
			if err != nil {
				t.Fatal(err)
			}
			text := string(body)
			for _, forbidden := range []string{rawURL, rawToken, "providerOperationId", "provider_operation_id", "externalUrl", "externalId", "X-Amz-"} {
				if strings.Contains(text, forbidden) {
					t.Fatalf("%s detail leaked %q: %s", role, forbidden, text)
				}
			}
			if !strings.Contains(text, "target-id") || !strings.Contains(text, "Publishing failed") {
				t.Fatalf("%s detail lost safe state: %s", role, text)
			}
		})
	}
}

func TestSuccessfulContentTargetsExposeOnlyCanonicalPublicationIdentity(t *testing.T) {
	tests := []struct {
		platform, externalID, externalURL, canonical string
	}{
		{"YOUTUBE", "AbCdEf_1234", "https://www.youtube.com/shorts/AbCdEf_1234/", "https://www.youtube.com/shorts/AbCdEf_1234"},
		{"INSTAGRAM", "123456789012345", "https://www.instagram.com/reel/C0de_safe-1/", "https://www.instagram.com/reel/C0de_safe-1/"},
		{"VK", "-12345_67890", "https://vk.ru/video-12345_67890/", "https://vk.ru/video-12345_67890"},
		{"TIKTOK", "7234567890123456789", "https://www.tiktok.com/@safe.creator/video/7234567890123456789/", "https://www.tiktok.com/@safe.creator/video/7234567890123456789"},
	}
	for _, role := range []string{"management", "creator"} {
		for _, test := range tests {
			t.Run(role+"/"+test.platform, func(t *testing.T) {
				target := publicContentTarget("target-id", test.platform, "account-id", "SUCCEEDED", test.externalID, test.externalURL, "media-id", "", "", nil, time.Unix(1, 0), time.Unix(2, 0))
				if target["externalId"] != test.externalID || target["externalUrl"] != test.canonical {
					t.Fatalf("public identity = %#v, want id=%q url=%q", target, test.externalID, test.canonical)
				}
				body, err := json.Marshal(map[string]any{"role": role, "targets": []map[string]any{target}})
				if err != nil || !strings.Contains(string(body), test.canonical) {
					t.Fatalf("%s detail lost canonical publication URL: %s (%v)", role, body, err)
				}
			})
		}
	}
}

func TestCanonicalPublicationURLRejectsPoisonedAndNonCanonicalValues(t *testing.T) {
	const youtubeID = "AbCdEf_1234"
	poisoned := []string{
		"http://www.youtube.com/shorts/" + youtubeID,
		"https://user:secret@www.youtube.com/shorts/" + youtubeID,
		"https://www.youtube.com:444/shorts/" + youtubeID,
		"https://youtube.com/shorts/" + youtubeID,
		"https://evil.example/shorts/" + youtubeID,
		"https://www.youtube.com/watch/" + youtubeID,
		"https://www.youtube.com/shorts/" + youtubeID + "?X-Amz-Signature=secret",
		"https://www.youtube.com/shorts/" + youtubeID + "#fragment",
		"https://www.youtube.com/shorts/upload",
		"https://www.youtube.com/shorts/session-" + youtubeID,
		"https://www.youtube.com/shorts/opaque-" + youtubeID,
		"https://www.youtube.com/shorts/base64-" + youtubeID,
	}
	for _, value := range poisoned {
		if got := canonicalPublicationURL("YOUTUBE", youtubeID, value); got != "" {
			t.Fatalf("poisoned URL %q was exposed as %q", value, got)
		}
	}

	for _, test := range []struct{ platform, id string }{
		{"YOUTUBE", "too-short"},
		{"INSTAGRAM", "media-1"},
		{"VK", "1_2_session"},
		{"TIKTOK", "c2Vzc2lvbl90b2tlbg"},
	} {
		if got := publicExternalID(test.platform, "SUCCEEDED", test.id); got != "" {
			t.Fatalf("unsafe %s external id %q was exposed", test.platform, got)
		}
	}
	if got := publicExternalID("YOUTUBE", "FAILED", youtubeID); got != "" {
		t.Fatalf("non-success target exposed external id %q", got)
	}
}

func TestPublicAttemptResponseKeepsSafeMessage(t *testing.T) {
	const message = "authorization expired; reconnect the account"
	if got := publicContentAttemptMessage(message); got != message {
		t.Fatalf("safe message=%q", got)
	}
}
