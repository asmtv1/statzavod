package httpserver

import (
	"net/http/httptest"
	"testing"
)

func TestEnglishRequest(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		header string
		want   bool
	}{
		{name: "query", url: "/exports?locale=en", want: true},
		{name: "regional header", url: "/analytics/summary", header: "en-US,en;q=0.9", want: true},
		{name: "russian", url: "/analytics/summary", header: "ru-RU,ru;q=0.9", want: false},
		{name: "query overrides header", url: "/exports?locale=ru", header: "en-US", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", test.url, nil)
			request.Header.Set("Accept-Language", test.header)
			if got := englishRequest(request); got != test.want {
				t.Fatalf("englishRequest() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestLocalized(t *testing.T) {
	request := httptest.NewRequest("GET", "/?locale=en", nil)
	if got := localized(request, "Просмотры", "Views"); got != "Views" {
		t.Fatalf("localized() = %q, want Views", got)
	}
}

func TestContainsCyrillic(t *testing.T) {
	if !containsCyrillic("Ошибка синхронизации") {
		t.Fatal("expected Cyrillic text to be detected")
	}
	if containsCyrillic("Synchronization failed") {
		t.Fatal("did not expect English text to be detected as Cyrillic")
	}
}
