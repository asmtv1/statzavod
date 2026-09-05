package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestCreatorPortalHasNoCreatorIDOrMutationRoutes(t *testing.T) {
	router := (&Server{}).Router()
	routes, ok := router.(chi.Routes)
	if !ok {
		t.Fatalf("router %T does not expose chi routes", router)
	}
	methods := map[string]map[string]bool{}
	if err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/api/v1/creator-portal/") {
			if methods[route] == nil {
				methods[route] = map[string]bool{}
			}
			methods[route][method] = true
			if strings.Contains(strings.ToLower(route), "creatorid") || strings.Contains(route, "{id}") {
				t.Fatalf("creator portal route accepts an arbitrary creator ID: %s %s", method, route)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"/api/v1/creator-portal/profiles": {http.MethodGet}, "/api/v1/creator-portal/profile": {http.MethodGet}, "/api/v1/creator-portal/socials": {http.MethodGet},
		"/api/v1/creator-portal/credentials": {http.MethodGet}, "/api/v1/creator-portal/credentials/{credentialID}/reveal": {http.MethodPost}, "/api/v1/creator-portal/publications": {http.MethodGet}, "/api/v1/creator-portal/stats": {http.MethodGet}, "/api/v1/creator-portal/export": {http.MethodGet},
		"/api/v1/creator-portal/content-items": {http.MethodGet, http.MethodPost}, "/api/v1/creator-portal/content-preflight": {http.MethodPost}, "/api/v1/creator-portal/content-items/{itemID}": {http.MethodGet, http.MethodPatch},
		"/api/v1/creator-portal/content-items/{itemID}/attempts": {http.MethodGet},
		"/api/v1/creator-portal/content-items/{itemID}/copy":     {http.MethodPost}, "/api/v1/creator-portal/content-items/{itemID}/preflight": {http.MethodPost}, "/api/v1/creator-portal/content-items/{itemID}/submit": {http.MethodPost}, "/api/v1/creator-portal/content-items/{itemID}/publish": {http.MethodPost}, "/api/v1/creator-portal/content-items/{itemID}/schedule": {http.MethodPost}, "/api/v1/creator-portal/content-items/{itemID}/retry": {http.MethodPost}, "/api/v1/creator-portal/content-items/{itemID}/cancel": {http.MethodPost},
		"/api/v1/creator-portal/media/uploads": {http.MethodPost}, "/api/v1/creator-portal/media/uploads/{uploadID}/parts/{partNumber}": {http.MethodPost}, "/api/v1/creator-portal/media/uploads/{uploadID}/complete": {http.MethodPost}, "/api/v1/creator-portal/media/uploads/{uploadID}": {http.MethodDelete},
	}
	if len(methods) != len(want) {
		t.Fatalf("creator portal routes=%v, want %v", methods, want)
	}
	for route, wantedMethods := range want {
		for _, method := range wantedMethods {
			if !methods[route][method] {
				t.Fatalf("route %s missing method %q; got %v", route, method, methods[route])
			}
		}
	}
}

func TestCreatorCannotEnterManagementMiddleware(t *testing.T) {
	companyID, creatorID := "company-a", "creator-a"
	p := principal{
		Role: roleCreator, ActiveCompanyID: &companyID, ActiveCreatorID: &creatorID,
		Companies:       map[string]companyAccess{companyID: testCompany(companyID)},
		CreatorProfiles: map[string]creatorProfileAccess{creatorID: {ID: creatorID, CompanyID: companyID}},
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, handler := range []http.Handler{
		(&Server{}).requireManagement(next),
		(&Server{}).requireCompanyAccess("creator")(next),
		(&Server{}).requireActiveCompanyPermission("STATS_VIEW")(next),
	} {
		request := httptest.NewRequest(http.MethodGet, "/creators/"+creatorID, nil)
		routeContext := chi.NewRouteContext()
		routeContext.URLParams.Add("id", creatorID)
		ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
		ctx = context.WithValue(ctx, principalKey, p)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request.WithContext(ctx))
		if response.Code != http.StatusForbidden {
			t.Fatalf("CREATOR management status=%d, want 403", response.Code)
		}
	}
}

func TestActiveCreatorContextSwitchesWithoutArbitraryIDs(t *testing.T) {
	companyA, companyB, profileA, profileB := "company-a", "company-b", "profile-a", "profile-b"
	p := principal{
		Role: roleCreator, ActiveCompanyID: &companyA, ActiveCreatorID: &profileA,
		CreatorProfiles: map[string]creatorProfileAccess{
			profileA: {ID: profileA, CompanyID: companyA},
			profileB: {ID: profileB, CompanyID: companyB},
		},
	}
	if profile, ok := activeCreatorContext(p); !ok || profile.ID != profileA {
		t.Fatalf("active profile=%#v ok=%v", profile, ok)
	}
	p.ActiveCompanyID, p.ActiveCreatorID = &companyB, &profileB
	if profile, ok := activeCreatorContext(p); !ok || profile.ID != profileB {
		t.Fatalf("switched profile=%#v ok=%v", profile, ok)
	}
}
