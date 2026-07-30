package httpserver

import (
	"testing"
	"time"
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
