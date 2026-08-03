package httpserver

import "testing"

func TestNormalizeVKCommunityURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{name: "vk ru", input: "https://vk.ru/club240646151", want: "https://vk.ru/club240646151", ok: true},
		{name: "vk com shorthand", input: "vk.com/faadl", want: "https://vk.com/faadl", ok: true},
		{name: "www normalized", input: "https://www.vk.ru/public123", want: "https://vk.ru/public123", ok: true},
		{name: "reject http", input: "http://vk.ru/club1", ok: false},
		{name: "reject other host", input: "https://example.com/club1", ok: false},
		{name: "reject nested path", input: "https://vk.ru/club1/video", ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := normalizeVKCommunityURL(test.input)
			if ok != test.ok || got != test.want {
				t.Fatalf("normalizeVKCommunityURL(%q) = %q, %v; want %q, %v", test.input, got, ok, test.want, test.ok)
			}
		})
	}
}
