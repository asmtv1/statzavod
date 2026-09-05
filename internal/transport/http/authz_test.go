package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func testCompany(id string, permissions ...string) companyAccess {
	grants := make(map[string]struct{}, len(permissions))
	for _, permission := range permissions {
		grants[permission] = struct{}{}
	}
	return companyAccess{ID: id, Name: id, Permissions: grants}
}

func TestRolePermissionTable(t *testing.T) {
	companyA, companyB := "company-a", "company-b"
	tests := []struct {
		name       string
		principal  principal
		companyID  string
		permission string
		want       int
	}{
		{"owner-all", principal{Role: roleOwner, Companies: map[string]companyAccess{companyA: testCompany(companyA)}}, companyA, "SECRET_REVEAL", 0},
		{"owner-selected-company", principal{Role: roleOwner, ActiveCompanyID: &companyA, Companies: map[string]companyAccess{companyA: testCompany(companyA), companyB: testCompany(companyB)}}, companyB, "SECRET_REVEAL", http.StatusNotFound},
		{"owner-cross-workspace-invisible", principal{Role: roleOwner, Companies: map[string]companyAccess{companyA: testCompany(companyA)}}, companyB, "STATS_VIEW", http.StatusNotFound},
		{"manager-granted", principal{Role: roleManager, ActiveCompanyID: &companyA, Companies: map[string]companyAccess{companyA: testCompany(companyA, "CREATOR_EDIT")}}, companyA, "CREATOR_EDIT", 0},
		{"manager-visible-denied", principal{Role: roleManager, ActiveCompanyID: &companyA, Companies: map[string]companyAccess{companyA: testCompany(companyA)}}, companyA, "CREATOR_EDIT", http.StatusForbidden},
		{"manager-assigned-but-inactive-invisible", principal{Role: roleManager, ActiveCompanyID: &companyA, Companies: map[string]companyAccess{companyA: testCompany(companyA), companyB: testCompany(companyB, "CREATOR_EDIT")}}, companyB, "CREATOR_EDIT", http.StatusNotFound},
		{"creator-own-company-visible-denied", principal{Role: roleCreator, ActiveCompanyID: &companyA, Companies: map[string]companyAccess{companyA: testCompany(companyA)}}, companyA, "CREATOR_EDIT", http.StatusForbidden},
		{"creator-other-company-invisible", principal{Role: roleCreator, ActiveCompanyID: &companyA, Companies: map[string]companyAccess{companyA: testCompany(companyA)}}, companyB, "CREATOR_EDIT", http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := authorizeCompanyAction(test.principal, test.companyID, test.permission); got != test.want {
				t.Fatalf("authorizeCompanyAction()=%d, want %d", got, test.want)
			}
		})
	}
}

func TestTwoWorkspaceTwoCompanyIDORMatrix(t *testing.T) {
	// Creator and platform-account identifiers resolve to one of these company
	// scopes before this matrix is evaluated. A resource from workspace 2 is
	// absent from workspace 1's principal and indistinguishable from missing.
	companyA, companyB, foreignA := "w1-a", "w1-b", "w2-a"
	for _, resource := range []string{"company", "creator-profile", "platform-account"} {
		t.Run(resource, func(t *testing.T) {
			owner := principal{Role: roleOwner, Companies: map[string]companyAccess{companyA: testCompany(companyA), companyB: testCompany(companyB)}}
			if authorizeCompanyAction(owner, companyB, "") != 0 || authorizeCompanyAction(owner, foreignA, "") != http.StatusNotFound {
				t.Fatal("owner tenant boundary is not enforced")
			}
			manager := principal{Role: roleManager, ActiveCompanyID: &companyA, Companies: map[string]companyAccess{companyA: testCompany(companyA), companyB: testCompany(companyB)}}
			if authorizeCompanyAction(manager, companyB, "") != http.StatusNotFound {
				t.Fatal("manager active-company IDOR must be invisible")
			}
			creator := principal{Role: roleCreator, ActiveCompanyID: &companyA, Companies: map[string]companyAccess{companyA: testCompany(companyA)}}
			if authorizeCompanyAction(creator, companyB, "") != http.StatusNotFound {
				t.Fatal("creator cross-profile IDOR must be invisible")
			}
		})
	}
}

func TestRequireOwnCreatorProfileIsInvisible(t *testing.T) {
	active, other := "creator-a", "creator-b"
	p := principal{Role: roleCreator, ActiveCreatorID: &active, CreatorProfiles: map[string]creatorProfileAccess{active: {ID: active}}}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := (&Server{}).requireOwnCreatorProfile(next)

	for _, test := range []struct {
		id   string
		want int
	}{{active, http.StatusNoContent}, {other, http.StatusNotFound}} {
		r := httptest.NewRequest(http.MethodGet, "/creators/"+test.id, nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", test.id)
		ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rctx)
		ctx = context.WithValue(ctx, principalKey, p)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r.WithContext(ctx))
		if recorder.Code != test.want {
			t.Fatalf("creator %q status=%d, want %d", test.id, recorder.Code, test.want)
		}
	}
}

func TestSessionContextValidation(t *testing.T) {
	company, profile := "company-a", "creator-a"
	tests := []struct {
		name string
		p    principal
		want bool
	}{
		{"owner-all", principal{Role: roleOwner, Companies: map[string]companyAccess{}}, true},
		{"owner-archived-company", principal{Role: roleOwner, ActiveCompanyID: &company, Companies: map[string]companyAccess{}}, false},
		{"manager-revoked-assignment", principal{Role: roleManager, ActiveCompanyID: &company, Companies: map[string]companyAccess{}}, false},
		{"creator-own-profile", principal{Role: roleCreator, ActiveCompanyID: &company, ActiveCreatorID: &profile, CreatorProfiles: map[string]creatorProfileAccess{profile: {ID: profile, CompanyID: company}}}, true},
		{"creator-deleted-profile", principal{Role: roleCreator, ActiveCompanyID: &company, ActiveCreatorID: &profile, CreatorProfiles: map[string]creatorProfileAccess{}}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.p.contextValid(); got != test.want {
				t.Fatalf("contextValid()=%v, want %v", got, test.want)
			}
		})
	}
}

func TestSessionCookieSecurity(t *testing.T) {
	development := sessionCookie("session", "token", false)
	production := sessionCookie("session", "token", true)
	if !development.HttpOnly || development.SameSite != http.SameSiteLaxMode || development.Path != "/" || development.MaxAge != 86400 {
		t.Fatalf("unsafe development cookie: %#v", development)
	}
	if !production.Secure || !production.HttpOnly {
		t.Fatalf("unsafe production cookie: %#v", production)
	}
}

func TestOAuthCallbackPermissionRevoked(t *testing.T) {
	company := "company-a"
	before := principal{Role: roleManager, ActiveCompanyID: &company, Companies: map[string]companyAccess{company: testCompany(company, "SOCIAL_CONNECT")}}
	after := principal{Role: roleManager, ActiveCompanyID: &company, Companies: map[string]companyAccess{company: testCompany(company)}}
	if authorizeCompanyAction(before, company, "SOCIAL_CONNECT") != 0 {
		t.Fatal("OAuth authorization should initially be allowed")
	}
	if authorizeCompanyAction(after, company, "SOCIAL_CONNECT") != http.StatusForbidden {
		t.Fatal("OAuth callback must reject a permission revoked after authorization")
	}
}

func TestExportScopeOwnerAllAndSelectedContext(t *testing.T) {
	companyA, companyB := "company-a", "company-b"
	companies := map[string]companyAccess{companyA: testCompany(companyA), companyB: testCompany(companyB)}
	ownerAll := principal{Role: roleOwner, Companies: companies}
	ownerSelected := principal{Role: roleOwner, ActiveCompanyID: &companyA, Companies: companies}
	manager := principal{Role: roleManager, ActiveCompanyID: &companyA, Companies: companies}
	if !ownerAll.canExportCompany(companyA) || !ownerAll.canExportCompany(companyB) {
		t.Fatal("OWNER all-company mode must allow a multi-company export")
	}
	if !ownerSelected.canExportCompany(companyA) || ownerSelected.canExportCompany(companyB) {
		t.Fatal("OWNER selected-company mode must constrain export to that company")
	}
	if !manager.canExportCompany(companyA) || manager.canExportCompany(companyB) {
		t.Fatal("MANAGER export must be constrained to the active company")
	}
}

func TestOAuthSelectionCompanyScope(t *testing.T) {
	companyA, companyB := "company-a", "company-b"
	companies := map[string]companyAccess{companyA: testCompany(companyA), companyB: testCompany(companyB)}
	ownerAll := principal{Role: roleOwner, Companies: companies}
	if got := ownerAll.oauthSelectionCompanyIDs(companyA); len(got) != 2 || got[0] != companyA || got[1] != companyB {
		t.Fatalf("OWNER all-company selection scope=%v", got)
	}
	ownerSelected := principal{Role: roleOwner, ActiveCompanyID: &companyA, Companies: companies}
	if got := ownerSelected.oauthSelectionCompanyIDs(companyA); len(got) != 1 || got[0] != companyA {
		t.Fatalf("OWNER selected-company selection scope=%v", got)
	}
	manager := principal{Role: roleManager, ActiveCompanyID: &companyA, Companies: companies}
	if got := manager.oauthSelectionCompanyIDs(companyA); len(got) != 1 || got[0] != companyA {
		t.Fatalf("MANAGER active-company selection scope=%v", got)
	}
	if got := manager.oauthSelectionCompanyIDs(companyB); len(got) != 0 {
		t.Fatalf("MANAGER received inactive company selection scope=%v", got)
	}
}

func TestAllManagerPermissionsAcrossRoles(t *testing.T) {
	company := "company-a"
	for _, permission := range managerPermissions {
		t.Run(permission, func(t *testing.T) {
			owner := principal{Role: roleOwner, Companies: map[string]companyAccess{company: testCompany(company)}}
			managerGranted := principal{Role: roleManager, ActiveCompanyID: &company, Companies: map[string]companyAccess{company: testCompany(company, permission)}}
			managerDenied := principal{Role: roleManager, ActiveCompanyID: &company, Companies: map[string]companyAccess{company: testCompany(company)}}
			creator := principal{Role: roleCreator, ActiveCompanyID: &company, Companies: map[string]companyAccess{company: testCompany(company)}}
			if !owner.hasPermission(permission) || !managerGranted.hasPermission(permission) {
				t.Fatalf("%s must be available to OWNER and explicitly granted MANAGER", permission)
			}
			if managerDenied.hasPermission(permission) || creator.hasPermission(permission) {
				t.Fatalf("%s leaked to ungranted MANAGER or CREATOR", permission)
			}
		})
	}
}
