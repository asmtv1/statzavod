package httpserver

import (
	"testing"
	"time"
)

func TestAuditDateBoundary(t *testing.T) {
	start, err := auditDateBoundary("2026-08-11", false)
	if err != nil || start == nil || !start.Equal(time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("start=%v err=%v", start, err)
	}
	end, err := auditDateBoundary("2026-08-11", true)
	if err != nil || end == nil || !end.Equal(time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("end=%v err=%v", end, err)
	}
	if empty, err := auditDateBoundary("", false); err != nil || empty != nil {
		t.Fatalf("empty=%v err=%v", empty, err)
	}
	if _, err := auditDateBoundary("11.08.2026", false); err == nil {
		t.Fatal("non-ISO date was accepted")
	}
}
