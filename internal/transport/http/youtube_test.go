package httpserver

import "testing"

func TestYouTubeChannelProfileUsesCurrentPublicIdentity(t *testing.T) {
	var channel youtubeChannel
	channel.ID = "channel-id"
	channel.Snippet.Title = "Current title"
	channel.Snippet.CustomURL = "@current_handle"
	channel.Snippet.Description = "Current description"
	channel.Snippet.Thumbnails = map[string]struct {
		URL string `json:"url"`
	}{"high": {URL: "https://img.example/current.jpg"}}

	profile := youtubeChannelProfile(channel)
	if got, want := profile.Username, "current_handle"; got != want {
		t.Fatalf("username = %q, want %q", got, want)
	}
	if got, want := profile.DisplayName, "Current title"; got != want {
		t.Fatalf("display name = %q, want %q", got, want)
	}
	if got, want := profile.ProfileURL, "https://www.youtube.com/channel/channel-id"; got != want {
		t.Fatalf("profile URL = %q, want %q", got, want)
	}
	if got, want := profile.AvatarURL, "https://img.example/current.jpg"; got != want {
		t.Fatalf("avatar URL = %q, want %q", got, want)
	}
}
