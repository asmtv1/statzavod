package httpserver

import "testing"

func TestContentETagMatchesExactRevision(t *testing.T) {
	if got := contentETag(12); got != `"revision-12"` {
		t.Fatalf("contentETag(12) = %q", got)
	}
	for _, header := range []string{`"revision-12"`, `"old", "revision-12"`} {
		if !matchesContentETag(header, 12) {
			t.Fatalf("expected %q to match revision 12", header)
		}
	}
	for _, header := range []string{"", `W/"revision-12"`, `"revision-1"`, `"revision-120"`} {
		if matchesContentETag(header, 12) {
			t.Fatalf("did not expect %q to match revision 12", header)
		}
	}
}
