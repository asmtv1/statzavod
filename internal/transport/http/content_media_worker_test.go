package httpserver

import "testing"

func TestValidateVerticalVideoRejectsSquareAndLandscape(t *testing.T) {
	for name, probe := range map[string]mediaProbe{
		"square":    {Width: 1080, Height: 1080},
		"landscape": {Width: 1920, Height: 1080},
		"missing":   {Width: 0, Height: 1920},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateVerticalVideo(probe); err == nil {
				t.Fatal("non-vertical video was accepted")
			}
		})
	}
	if err := validateVerticalVideo(mediaProbe{Width: 1080, Height: 1920}); err != nil {
		t.Fatalf("portrait video rejected: %v", err)
	}
}
