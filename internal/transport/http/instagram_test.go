package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/statzavod/statzavod/internal/config"
)

func TestParseInstagramTimestamp(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Time
	}{
		{
			name:  "RFC3339 offset",
			value: "2026-07-30T18:32:10+00:00",
			want:  time.Date(2026, 7, 30, 18, 32, 10, 0, time.UTC),
		},
		{
			name:  "Meta compact offset",
			value: "2026-07-30T18:32:10+0000",
			want:  time.Date(2026, 7, 30, 18, 32, 10, 0, time.UTC),
		},
		{
			name:  "Meta compact offset with fractional seconds",
			value: "2026-07-30T22:32:10.125+0400",
			want:  time.Date(2026, 7, 30, 18, 32, 10, 125000000, time.UTC),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseInstagramTimestamp(test.value)
			if err != nil {
				t.Fatalf("parseInstagramTimestamp(%q): %v", test.value, err)
			}
			if !got.Equal(test.want) {
				t.Fatalf("parseInstagramTimestamp(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestParseInstagramTimestampRejectsInvalidValue(t *testing.T) {
	if _, err := parseInstagramTimestamp("not-a-date"); err == nil {
		t.Fatal("parseInstagramTimestamp accepted an invalid value")
	}
}

func TestFetchInstagramMediaInsightsBatchesMetrics(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/media-1/insights" {
			t.Fatalf("request path = %q", r.URL.Path)
		}
		metrics := strings.Split(r.URL.Query().Get("metric"), ",")
		if len(metrics) != 7 {
			t.Fatalf("metric count = %d, want 7", len(metrics))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"name":"views","total_value":{"value":120}},{"name":"reach","total_value":{"value":80}},{"name":"shares","total_value":{"value":5}}]}`))
	}))
	defer server.Close()

	provider := newProviderClient("Instagram")
	provider.client = server.Client()
	app := &Server{}
	app.config.InstagramAPIBase = server.URL
	got, err := app.fetchInstagramMediaInsights(context.Background(), provider, "token", instagramMedia{
		ID:            "media-1",
		LikeCount:     11,
		CommentsCount: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want 1", requests)
	}
	if got.Views == nil || *got.Views != 120 || got.Reach == nil || *got.Reach != 80 || got.Shares == nil || *got.Shares != 5 {
		t.Fatalf("unexpected insight metrics: %+v", got)
	}
	if got.Likes == nil || *got.Likes != 11 || got.Comments == nil || *got.Comments != 3 {
		t.Fatalf("media counters were not preserved: %+v", got)
	}
}

func TestFetchInstagramCollaborativeMedia(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/collaborative_media" {
			t.Fatalf("path = %q, want collaborative media edge", r.URL.Path)
		}
		if r.URL.Query().Get("access_token") != "token" {
			t.Fatal("access token was not included")
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"collab-1","media_type":"VIDEO","timestamp":"2026-07-01T12:00:00+0000"}]}`))
	}))
	defer server.Close()

	app := &Server{config: config.Config{InstagramAPIBase: server.URL}}
	items, err := app.fetchInstagramCollaborativeMedia(context.Background(), newProviderClient("Instagram"), "token")
	if err != nil {
		t.Fatalf("fetchInstagramCollaborativeMedia: %v", err)
	}
	if len(items) != 1 || items[0].ID != "collab-1" {
		t.Fatalf("items = %+v, want collab-1", items)
	}
}

func TestMergeInstagramMediaDeduplicatesByMediaID(t *testing.T) {
	items := mergeInstagramMedia(
		[]instagramMedia{{ID: "owned-1"}, {ID: "shared-1"}},
		[]instagramMedia{{ID: "shared-1"}, {ID: "collab-1"}, {ID: ""}},
	)
	if len(items) != 3 {
		t.Fatalf("len(items) = %d, want 3", len(items))
	}
	got := make(map[string]bool, len(items))
	for _, item := range items {
		got[item.ID] = true
	}
	for _, id := range []string{"owned-1", "shared-1", "collab-1"} {
		if !got[id] {
			t.Errorf("missing media %q", id)
		}
	}
}
