package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/statzavod/statzavod/internal/config"
)

func TestFetchVKCommunityVideos(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/method/utils.resolveScreenName", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("screen_name") != "faadl_creator" || r.URL.Query().Get("access_token") != "token" {
			t.Fatal("VK community resolve request is incomplete")
		}
		writeJSON(w, http.StatusOK, map[string]any{"response": map[string]any{"type": "group", "object_id": 42}})
	})
	mux.HandleFunc("/method/video.get", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("owner_id") != "-42" || r.URL.Query().Get("count") != "200" {
			t.Fatal("VK video request has the wrong community owner")
		}
		writeJSON(w, http.StatusOK, map[string]any{"response": map[string]any{
			"count": 1,
			"items": []any{map[string]any{"id": 7, "owner_id": -42, "title": "Клип", "date": 1_700_000_000, "views": 123, "likes": map[string]any{"count": 4}, "comments": 2}},
		}})
	})
	mux.HandleFunc("/method/wall.get", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("owner_id") != "-42" || r.URL.Query().Get("count") != "100" {
			t.Fatal("VK wall request has the wrong community owner")
		}
		writeJSON(w, http.StatusOK, map[string]any{"response": map[string]any{
			"count": 1,
			"items": []any{map[string]any{
				"id": 9, "owner_id": -42, "date": 1_700_000_001, "text": "Пост с клипом",
				"views": map[string]any{"count": 321}, "likes": map[string]any{"count": 8}, "comments": map[string]any{"count": 3}, "reposts": map[string]any{"count": 2},
				"attachments": []any{map[string]any{"type": "video", "video": map[string]any{"id": 10, "owner_id": -42, "title": "Клип со стены"}}},
			}},
		}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	s := &Server{config: config.Config{VKAPIBase: server.URL, VKAPIVersion: "5.199"}}
	groupID, err := s.resolveVKCommunity(t.Context(), "token", "https://vk.ru/faadl_creator")
	if err != nil || groupID != 42 {
		t.Fatalf("unexpected resolved community: %d, %v", groupID, err)
	}
	videos, err := s.fetchVKCommunityVideos(t.Context(), "token", groupID)
	if err != nil {
		t.Fatal(err)
	}
	if len(videos) != 1 || videos[0].Views != 123 || videos[0].Likes.Count != 4 {
		t.Fatalf("unexpected videos: %#v", videos)
	}
	wallVideos, err := s.fetchVKCommunityWallVideos(t.Context(), "token", groupID)
	if err != nil {
		t.Fatal(err)
	}
	if len(wallVideos) != 1 || wallVideos[0].Views != 321 || wallVideos[0].Likes.Count != 8 || wallVideos[0].Shares != 2 || wallVideos[0].Permalink != "https://vk.ru/wall-42_9" {
		t.Fatalf("unexpected wall videos: %#v", wallVideos)
	}
}

func TestResolveVKCommunityRejectsProfile(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/method/utils.resolveScreenName", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"response": map[string]any{"type": "user", "object_id": 42}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	s := &Server{config: config.Config{VKAPIBase: server.URL, VKAPIVersion: "5.199"}}
	if _, err := s.resolveVKCommunity(t.Context(), "token", "https://vk.ru/person"); err == nil {
		t.Fatal("expected a user profile URL to be rejected")
	}
}

func TestFetchVKCurrentProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/method/users.get" || r.URL.Query().Get("access_token") != "token" {
			t.Fatalf("unexpected VK profile request: %s", r.URL.String())
		}
		writeJSON(w, http.StatusOK, map[string]any{"response": []any{map[string]any{
			"id": 7, "first_name": "Иван", "last_name": "Иванов",
			"screen_name": "current_name", "photo_200": "https://img.example/current.jpg",
		}}})
	}))
	defer server.Close()

	s := &Server{config: config.Config{VKAPIBase: server.URL, VKAPIVersion: "5.199"}}
	profile, err := s.fetchVKCurrentProfile(t.Context(), "token")
	if err != nil {
		t.Fatal(err)
	}
	if profile.Username != "current_name" || profile.DisplayName != "Иван Иванов" {
		t.Fatalf("unexpected VK identity: %#v", profile)
	}
	if profile.ProfileURL != "https://vk.ru/current_name" || profile.AvatarURL != "https://img.example/current.jpg" {
		t.Fatalf("unexpected VK links: %#v", profile)
	}
}
