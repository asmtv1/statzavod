package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPlatformConnectionDeletionRoles(t *testing.T) {
	tests := []struct {
		name       string
		role       string
		wantStatus int
	}{
		{name: "admin", role: "ADMIN", wantStatus: http.StatusNoContent},
		{name: "analyst", role: "ANALYST", wantStatus: http.StatusNoContent},
		{name: "viewer", role: "VIEWER", wantStatus: http.StatusForbidden},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
			handler := (&Server{}).require("ADMIN", "ANALYST")(next)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodDelete, "/platform-accounts/account-id/connection", nil)
			request = request.WithContext(context.WithValue(request.Context(), principalKey, principal{Role: test.role}))

			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("role %s received status %d; want %d", test.role, recorder.Code, test.wantStatus)
			}
		})
	}
}

func TestCreatorPermanentDeletionRoles(t *testing.T) {
	tests := []struct {
		name       string
		role       string
		wantStatus int
	}{
		{name: "admin", role: "ADMIN", wantStatus: http.StatusNoContent},
		{name: "analyst", role: "ANALYST", wantStatus: http.StatusNoContent},
		{name: "viewer", role: "VIEWER", wantStatus: http.StatusForbidden},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
			handler := (&Server{}).require("ADMIN", "ANALYST")(next)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodDelete, "/creators/creator-id", nil)
			request = request.WithContext(context.WithValue(request.Context(), principalKey, principal{Role: test.role}))

			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("role %s received status %d; want %d", test.role, recorder.Code, test.wantStatus)
			}
		})
	}
}
