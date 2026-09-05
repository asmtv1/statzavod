package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestNormalizeAssignmentInputs(t *testing.T) {
	items, err := normalizeAssignmentInputs([]managerCompanyAssignmentInput{
		{CompanyID: "company-b", Permissions: []string{"creator_edit", "STATS_VIEW", "CREATOR_EDIT"}},
		{CompanyID: "company-a", Permissions: []string{"stats_export"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].CompanyID != "company-a" || items[1].CompanyID != "company-b" {
		t.Fatalf("assignments were not normalized deterministically: %#v", items)
	}
	if got := items[1].Permissions; len(got) != 2 || got[0] != "CREATOR_EDIT" || got[1] != "STATS_VIEW" {
		t.Fatalf("permissions were not normalized: %#v", got)
	}
	for _, test := range []struct {
		name  string
		items []managerCompanyAssignmentInput
	}{
		{"duplicate-company", []managerCompanyAssignmentInput{{CompanyID: "a"}, {CompanyID: "a"}}},
		{"missing-company", []managerCompanyAssignmentInput{{Permissions: []string{"STATS_VIEW"}}}},
		{"unknown-permission", []managerCompanyAssignmentInput{{CompanyID: "a", Permissions: []string{"USER_DELETE"}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := normalizeAssignmentInputs(test.items); err == nil {
				t.Fatal("invalid assignment input was accepted")
			}
		})
	}
}

func TestTeamContractRoutesRegistered(t *testing.T) {
	want := map[string]bool{
		http.MethodGet + " /api/v1/users":                          false,
		http.MethodPost + " /api/v1/users":                         false,
		http.MethodPatch + " /api/v1/users/{id}":                   false,
		http.MethodDelete + " /api/v1/users/{id}":                  false,
		http.MethodPut + " /api/v1/users/{id}/password":            false,
		http.MethodPut + " /api/v1/users/{id}/company-assignments": false,
		http.MethodGet + " /api/v1/creators/{id}/login-account":    false,
		http.MethodPut + " /api/v1/creators/{id}/login-account":    false,
		http.MethodPost + " /api/v1/users/invitations":             false,
		http.MethodPost + " /api/v1/auth/accept-invitation":        false,
	}
	routes, ok := (&Server{}).Router().(chi.Routes)
	if !ok {
		t.Fatal("router does not expose route contract")
	}
	if err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		key := method + " " + route
		if _, tracked := want[key]; tracked {
			want[key] = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for route, found := range want {
		if !found {
			t.Errorf("contract route is missing: %s", route)
		}
	}
}

func TestWorkspaceRoleCompatibilityMapping(t *testing.T) {
	if !validWorkspaceUserRole("OWNER") || !validWorkspaceUserRole("MANAGER") || validWorkspaceUserRole("CREATOR") {
		t.Fatal("direct workspace user creation role contract changed")
	}
	if legacyRoleForMembership(roleOwner) != "ADMIN" || legacyRoleForMembership(roleManager) != "VIEWER" {
		t.Fatal("expand-compatible legacy role mapping changed")
	}
}

func TestTeamEmailValidation(t *testing.T) {
	for _, email := range []string{"owner@example.com", "creator+portal@example.co.uk"} {
		if !validEmail(email) {
			t.Errorf("valid email rejected: %s", email)
		}
	}
	for _, email := range []string{"", "missing-at.example.com", "Name <owner@example.com>"} {
		if validEmail(email) {
			t.Errorf("invalid email accepted: %s", email)
		}
	}
}

func TestWorkspaceUserRoutesRequireOwner(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := (&Server{}).requireOwner(next)
	for _, test := range []struct {
		role string
		want int
	}{{roleOwner, http.StatusNoContent}, {roleManager, http.StatusForbidden}, {roleCreator, http.StatusForbidden}} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
		request = request.WithContext(context.WithValue(request.Context(), principalKey, principal{Role: test.role}))
		handler.ServeHTTP(recorder, request)
		if recorder.Code != test.want {
			t.Errorf("role %s received %d, want %d", test.role, recorder.Code, test.want)
		}
		if test.want == http.StatusForbidden && recorder.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("role %s denial is not a problem response", test.role)
		}
	}
}
