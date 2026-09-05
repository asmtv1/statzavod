package httpserver

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/statzavod/statzavod/internal/config"
)

func authzIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("STATZAVOD_AUTHZ_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set STATZAVOD_AUTHZ_TEST_DATABASE_URL to a disposable database migrated through 00021")
	}
	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestPasswordChangeRevokesOldCookieIntegration(t *testing.T) {
	pool := authzIntegrationPool(t)
	suffix := strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	email := "authz-password-" + suffix + "@test.local"
	password := "initial-password-12"
	hash, err := hashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	var organizationID, userID string
	if err = pool.QueryRow(t.Context(), `INSERT INTO organizations(name,slug) VALUES($1,$2) RETURNING id`, "Authz", "authz-"+suffix).Scan(&organizationID); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), `INSERT INTO users(email,password_hash,role,status) VALUES($1,$2,'ADMIN','ACTIVE') RETURNING id`, email, hash).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, organizationID)
	})
	if _, err = pool.Exec(t.Context(), `INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,'ADMIN')`, organizationID, userID); err != nil {
		t.Fatal(err)
	}

	server := New(pool, config.Config{CookieName: "test_session", Environment: "production"})
	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"`+email+`","password":"`+password+`"}`))
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	server.Router().ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	cookies := loginResponse.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("login returned unsafe cookie: %#v", cookies)
	}

	newHash, err := hashPassword("replacement-password-12")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE users SET password_hash=$2 WHERE id=$1`, userID, newHash); err != nil {
		t.Fatal(err)
	}
	me := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	me.AddCookie(cookies[0])
	meResponse := httptest.NewRecorder()
	server.Router().ServeHTTP(meResponse, me)
	if meResponse.Code != http.StatusUnauthorized {
		t.Fatalf("old cookie after password change status=%d, want 401", meResponse.Code)
	}
}

func TestOAuthCallbackRechecksRevokedPermissionIntegration(t *testing.T) {
	pool := authzIntegrationPool(t)
	suffix := strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	var organizationID, ownerID, managerID, companyID, creatorID, assignmentID string
	if err := pool.QueryRow(t.Context(), `INSERT INTO organizations(name,slug) VALUES($1,$2) RETURNING id`, "OAuth authz", "oauth-authz-"+suffix).Scan(&organizationID); err != nil {
		t.Fatal(err)
	}
	createUser := func(email, role string, target *string) {
		t.Helper()
		if err := pool.QueryRow(t.Context(), `INSERT INTO users(email,password_hash,role,status) VALUES($1,'integration-hash',$2,'ACTIVE') RETURNING id`, email, role).Scan(target); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(t.Context(), `INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,$3)`, organizationID, *target, role); err != nil {
			t.Fatal(err)
		}
	}
	createUser("oauth-owner-"+suffix+"@test.local", "ADMIN", &ownerID)
	createUser("oauth-manager-"+suffix+"@test.local", "ANALYST", &managerID)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=ANY($1::uuid[])`, []string{ownerID, managerID})
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, organizationID)
	})
	if err := pool.QueryRow(t.Context(), `INSERT INTO companies(organization_id,name) VALUES($1,'Company') RETURNING id`, organizationID).Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO creators(organization_id,company_id,first_name,last_name,display_name,created_by) VALUES($1,$2,'A','Creator','A Creator',$3) RETURNING id`, organizationID, companyID, ownerID).Scan(&creatorID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO manager_company_assignments(organization_id,manager_user_id,company_id,created_by) VALUES($1,$2,$3,$4) RETURNING id`, organizationID, managerID, companyID, ownerID).Scan(&assignmentID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO manager_company_permissions(assignment_id,permission,granted_by) VALUES($1,'SOCIAL_CONNECT',$2)`, assignmentID, ownerID); err != nil {
		t.Fatal(err)
	}
	server := &Server{pool: pool}
	if !server.oauthInitiatorStillAuthorized(t.Context(), organizationID, managerID, &creatorID, nil) {
		t.Fatal("manager should be authorized before permission revocation")
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM manager_company_permissions WHERE assignment_id=$1 AND permission='SOCIAL_CONNECT'`, assignmentID); err != nil {
		t.Fatal(err)
	}
	if server.oauthInitiatorStillAuthorized(t.Context(), organizationID, managerID, &creatorID, nil) {
		t.Fatal("OAuth callback remained authorized after SOCIAL_CONNECT revocation")
	}
	state := "archived-creator-state-" + suffix
	stateHash := sha256.Sum256([]byte(state))
	if _, err := pool.Exec(t.Context(), `INSERT INTO oauth_states(organization_id,creator_id,platform,state_hash,pkce_verifier_ciphertext,nonce,expires_at,initiated_by) VALUES($1,$2,'YOUTUBE',$3,decode('aa','hex'),decode('bb','hex'),now()+interval '10 minutes',$4)`, organizationID, creatorID, stateHash[:], ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE creators SET archived_at=now() WHERE id=$1`, creatorID); err != nil {
		t.Fatal(err)
	}
	var providerCalls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		providerCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer provider.Close()
	callbackServer := New(pool, config.Config{PublicBaseURL: "https://app.test", YouTubeOAuthBase: provider.URL, YouTubeAPIBase: provider.URL})
	callback := httptest.NewRequest(http.MethodGet, "/api/v1/oauth/youtube/callback?state="+state+"&code=must-not-exchange", nil)
	callbackResponse := httptest.NewRecorder()
	callbackServer.Router().ServeHTTP(callbackResponse, callback)
	if callbackResponse.Code != http.StatusFound || !strings.Contains(callbackResponse.Header().Get("Location"), "permission-revoked") {
		t.Fatalf("archived callback status=%d location=%q", callbackResponse.Code, callbackResponse.Header().Get("Location"))
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("archived creator callback performed %d provider requests", providerCalls.Load())
	}
}

func TestSessionDigestUsesOpaqueCookie(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: "session", Value: "opaque-token"})
	digest, err := sessionDigest(r, "session")
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte("opaque-token"))
	if string(digest) != string(want[:]) {
		t.Fatal("session cookie was not SHA-256 digested")
	}
}

func TestExportAndContentGroupCreatorIDORIntegration(t *testing.T) {
	pool := authzIntegrationPool(t)
	suffix := strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	var organizationID, ownerID, managerID, companyA, companyB, creatorA, creatorB, assignmentID, accountB, vkAccountB string
	if err := pool.QueryRow(t.Context(), `INSERT INTO organizations(name,slug) VALUES($1,$2) RETURNING id`, "Action authz", "action-authz-"+suffix).Scan(&organizationID); err != nil {
		t.Fatal(err)
	}
	createUser := func(email, role string, target *string) {
		t.Helper()
		if err := pool.QueryRow(t.Context(), `INSERT INTO users(email,password_hash,role,status) VALUES($1,'integration-hash',$2,'ACTIVE') RETURNING id`, email, role).Scan(target); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(t.Context(), `INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,$3)`, organizationID, *target, role); err != nil {
			t.Fatal(err)
		}
	}
	createUser("action-owner-"+suffix+"@test.local", "ADMIN", &ownerID)
	createUser("action-manager-"+suffix+"@test.local", "ANALYST", &managerID)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=ANY($1::uuid[])`, []string{ownerID, managerID})
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, organizationID)
	})
	if err := pool.QueryRow(t.Context(), `INSERT INTO companies(organization_id,name) VALUES($1,'Company A') RETURNING id`, organizationID).Scan(&companyA); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO companies(organization_id,name) VALUES($1,'Company B') RETURNING id`, organizationID).Scan(&companyB); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO creators(organization_id,company_id,first_name,last_name,display_name,created_by) VALUES($1,$2,'Creator','A','Creator A',$3) RETURNING id`, organizationID, companyA, ownerID).Scan(&creatorA); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO creators(organization_id,company_id,first_name,last_name,display_name,created_by) VALUES($1,$2,'Creator','B','Creator B',$3) RETURNING id`, organizationID, companyB, ownerID).Scan(&creatorB); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name) VALUES($1,$2,'INSTAGRAM','cross-company-account','original-b','Company B account') RETURNING id`, organizationID, companyB).Scan(&accountB); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO creator_account_assignments(creator_id,platform_account_id) VALUES($1,$2)`, creatorB, accountB); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO company_vk_accounts(organization_id,company_id,created_by,updated_by) VALUES($1,$2,$3,$3) RETURNING id`, organizationID, companyB, ownerID).Scan(&vkAccountB); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO manager_company_assignments(organization_id,manager_user_id,company_id,created_by) VALUES($1,$2,$3,$4) RETURNING id`, organizationID, managerID, companyA, ownerID).Scan(&assignmentID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO manager_company_permissions(assignment_id,permission,granted_by) VALUES($1,'STATS_EXPORT',$2),($1,'SOCIAL_CONNECT',$2)`, assignmentID, ownerID); err != nil {
		t.Fatal(err)
	}
	server := &Server{pool: pool}
	manager := principal{
		ID: managerID, OrganizationID: organizationID, Role: roleManager, ActiveCompanyID: &companyA,
		Companies: map[string]companyAccess{companyA: testCompany(companyA, "STATS_EXPORT", "SOCIAL_CONNECT")},
	}
	if got := server.validateCreatorExportScope(t.Context(), manager, []string{creatorB}); got != http.StatusNotFound {
		t.Fatalf("company B export IDOR status=%d, want 404", got)
	}
	if got := server.validateCreatorExportScope(t.Context(), manager, []string{creatorA}); got != 0 {
		t.Fatalf("company A export status=%d, want allowed", got)
	}
	if got := server.authorizeCreatorAction(t.Context(), manager, creatorB, "CREATOR_EDIT"); got != http.StatusNotFound {
		t.Fatalf("company B content-group IDOR status=%d, want 404", got)
	}
	if got := server.authorizeCreatorAction(t.Context(), manager, creatorA, "CREATOR_EDIT"); got != http.StatusForbidden {
		t.Fatalf("visible creator without CREATOR_EDIT status=%d, want 403", got)
	}
	accountRequest := httptest.NewRequest(http.MethodPost, "/creators/"+creatorA+"/accounts", strings.NewReader(`{"platform":"INSTAGRAM","externalId":"cross-company-account","username":"attacker-change","displayName":"Changed"}`))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", creatorA)
	requestContext := context.WithValue(accountRequest.Context(), chi.RouteCtxKey, routeContext)
	requestContext = context.WithValue(requestContext, principalKey, manager)
	accountResponse := httptest.NewRecorder()
	server.createCreatorAccount(accountResponse, accountRequest.WithContext(requestContext))
	if accountResponse.Code != http.StatusNotFound {
		t.Fatalf("cross-company account upsert status=%d body=%s, want 404", accountResponse.Code, accountResponse.Body.String())
	}
	var unchangedUsername string
	if err := pool.QueryRow(t.Context(), `SELECT username FROM platform_accounts WHERE id=$1`, accountB).Scan(&unchangedUsername); err != nil {
		t.Fatal(err)
	}
	if unchangedUsername != "original-b" {
		t.Fatalf("cross-company account was mutated to %q", unchangedUsername)
	}
	managerAssignments, err := loadInstagramAssignments(t.Context(), pool, organizationID, []string{companyA}, []string{"cross-company-account"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(managerAssignments) != 0 {
		t.Fatalf("manager selection leaked company B assignment: %#v", managerAssignments)
	}
	unavailable, err := loadInstagramUnavailableAccountIDs(t.Context(), pool, organizationID, []string{companyA}, []string{"cross-company-account"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, blocked := unavailable["cross-company-account"]; !blocked {
		t.Fatal("company B Instagram account was shown as available in company A")
	}
	candidates := []instagramFacebookCandidate{{Profile: platformProfile{ExternalID: "cross-company-account", Username: "provider-visible"}}}
	items := selectionAccounts(candidates, managerAssignments, unavailable, creatorA)
	if len(items) != 1 || items[0].Selectable || items[0].ConnectedCreator != "" || items[0].ConnectionState != "UNAVAILABLE" {
		t.Fatalf("cross-company selection leaked assignment details: %#v", items)
	}
	vkRequest := httptest.NewRequest(http.MethodPut, "/creators/"+creatorA+"/vk-access", strings.NewReader(`{"accountId":"`+vkAccountB+`","communityUrl":"https://vk.ru/club123","recipientAccountUrl":"https://vk.ru/id123"}`))
	vkRouteContext := chi.NewRouteContext()
	vkRouteContext.URLParams.Add("id", creatorA)
	vkContext := context.WithValue(vkRequest.Context(), chi.RouteCtxKey, vkRouteContext)
	vkContext = context.WithValue(vkContext, principalKey, manager)
	vkResponse := httptest.NewRecorder()
	server.saveCreatorVKAccess(vkResponse, vkRequest.WithContext(vkContext))
	if vkResponse.Code != http.StatusNotFound {
		t.Fatalf("cross-company VK access status=%d body=%s, want 404", vkResponse.Code, vkResponse.Body.String())
	}
	var vkAssignments int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM creator_vk_assignments WHERE creator_id=$1`, creatorA).Scan(&vkAssignments); err != nil {
		t.Fatal(err)
	}
	if vkAssignments != 0 {
		t.Fatal("cross-company VK access mutated creator A")
	}
	creatorPrincipal := principal{
		Role: roleCreator, OrganizationID: organizationID, ActiveCompanyID: &companyA, ActiveCreatorID: &creatorA,
		Companies: map[string]companyAccess{companyA: testCompany(companyA)}, CreatorProfiles: map[string]creatorProfileAccess{creatorA: {ID: creatorA, CompanyID: companyA}},
	}
	if got := server.authorizeCreatorAction(t.Context(), creatorPrincipal, creatorA, "CREATOR_EDIT"); got != http.StatusForbidden {
		t.Fatalf("CREATOR content-group mutation status=%d, want 403", got)
	}
	ownerAll := principal{Role: roleOwner, OrganizationID: organizationID, Companies: map[string]companyAccess{companyA: testCompany(companyA), companyB: testCompany(companyB)}}
	if got := server.validateCreatorExportScope(t.Context(), ownerAll, []string{creatorA, creatorB}); got != 0 {
		t.Fatalf("OWNER all-company export status=%d, want allowed", got)
	}
	ownerAssignments, err := loadInstagramAssignments(t.Context(), pool, organizationID, ownerAll.oauthSelectionCompanyIDs(companyA), []string{"cross-company-account"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if ownerAssignments["cross-company-account"].CreatorName != "Creator B" {
		t.Fatalf("OWNER all-company selection did not retain authorized assignment visibility: %#v", ownerAssignments)
	}
	ownerSelected := ownerAll
	ownerSelected.ActiveCompanyID = &companyA
	if got := server.validateCreatorExportScope(t.Context(), ownerSelected, []string{creatorB}); got != http.StatusNotFound {
		t.Fatalf("OWNER selected-company export status=%d, want 404", got)
	}
}

func TestLiveTwoWorkspaceTwoCompanyRolePermissionMatrix(t *testing.T) {
	pool := authzIntegrationPool(t)
	suffix := strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	type workspaceFixture struct {
		organizationID, companyA, companyB string
		owner, manager, creator            principal
		userIDs                            []string
	}
	fixtures := make([]workspaceFixture, 0, 2)
	for workspaceIndex := 1; workspaceIndex <= 2; workspaceIndex++ {
		var fixture workspaceFixture
		if err := pool.QueryRow(t.Context(), `INSERT INTO organizations(name,slug) VALUES($1,$2) RETURNING id`, "Matrix", "matrix-"+suffix+"-"+string(rune('0'+workspaceIndex))).Scan(&fixture.organizationID); err != nil {
			t.Fatal(err)
		}
		createMembershipUser := func(role, membershipRole, label string) string {
			t.Helper()
			var userID string
			email := "matrix-" + suffix + "-" + label + "-" + string(rune('0'+workspaceIndex)) + "@test.local"
			if err := pool.QueryRow(t.Context(), `INSERT INTO users(email,password_hash,role,status) VALUES($1,'matrix-hash',$2,'ACTIVE') RETURNING id`, email, role).Scan(&userID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(t.Context(), `INSERT INTO organization_memberships(organization_id,user_id,role,membership_role) VALUES($1,$2,$3,$4)`, fixture.organizationID, userID, role, membershipRole); err != nil {
				t.Fatal(err)
			}
			fixture.userIDs = append(fixture.userIDs, userID)
			return userID
		}
		ownerID := createMembershipUser("ADMIN", roleOwner, "owner")
		managerID := createMembershipUser("ANALYST", roleManager, "manager")
		creatorUserID := createMembershipUser("VIEWER", roleCreator, "creator")
		if err := pool.QueryRow(t.Context(), `INSERT INTO companies(organization_id,name) VALUES($1,'Company A') RETURNING id`, fixture.organizationID).Scan(&fixture.companyA); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(t.Context(), `INSERT INTO companies(organization_id,name) VALUES($1,'Company B') RETURNING id`, fixture.organizationID).Scan(&fixture.companyB); err != nil {
			t.Fatal(err)
		}
		var assignmentID, creatorProfileID string
		if err := pool.QueryRow(t.Context(), `INSERT INTO manager_company_assignments(organization_id,manager_user_id,company_id,created_by) VALUES($1,$2,$3,$4) RETURNING id`, fixture.organizationID, managerID, fixture.companyA, ownerID).Scan(&assignmentID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(t.Context(), `INSERT INTO manager_company_permissions(assignment_id,permission,granted_by) SELECT $1,permission,$2 FROM unnest($3::manager_permission[]) permission`, assignmentID, ownerID, managerPermissions); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(t.Context(), `INSERT INTO creators(organization_id,company_id,login_user_id,first_name,last_name,display_name,created_by) VALUES($1,$2,$3,'Matrix','Creator','Matrix Creator',$4) RETURNING id`, fixture.organizationID, fixture.companyA, creatorUserID, ownerID).Scan(&creatorProfileID); err != nil {
			t.Fatal(err)
		}
		createSessionPrincipal := func(userID, label string, activeCompanyID, activeCreatorID any) principal {
			t.Helper()
			token := "matrix-token-" + suffix + "-" + label + "-" + string(rune('0'+workspaceIndex))
			digest := sha256.Sum256([]byte(token))
			if _, err := pool.Exec(t.Context(), `INSERT INTO sessions(user_id,token_hash,expires_at,active_company_id,active_creator_id) VALUES($1,$2,now()+interval '1 hour',$3,$4)`, userID, digest[:], activeCompanyID, activeCreatorID); err != nil {
				t.Fatal(err)
			}
			loaded, err := (&Server{pool: pool}).principalForSession(t.Context(), digest[:])
			if err != nil {
				t.Fatal(err)
			}
			return loaded
		}
		fixture.owner = createSessionPrincipal(ownerID, "owner", nil, nil)
		fixture.manager = createSessionPrincipal(managerID, "manager", fixture.companyA, nil)
		fixture.creator = createSessionPrincipal(creatorUserID, "creator", fixture.companyA, creatorProfileID)
		fixtures = append(fixtures, fixture)
	}
	t.Cleanup(func() {
		for _, fixture := range fixtures {
			_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=ANY($1::uuid[])`, fixture.userIDs)
			_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, fixture.organizationID)
		}
	})
	for fixtureIndex, fixture := range fixtures {
		foreignCompany := fixtures[1-fixtureIndex].companyA
		if fixture.owner.Role != roleOwner || fixture.manager.Role != roleManager || fixture.creator.Role != roleCreator {
			t.Fatalf("workspace %d membership roles were not loaded", fixtureIndex+1)
		}
		for _, permission := range managerPermissions {
			if !fixture.owner.hasPermission(permission) || !fixture.manager.hasPermission(permission) || fixture.creator.hasPermission(permission) {
				t.Fatalf("workspace %d permission matrix failed for %s", fixtureIndex+1, permission)
			}
		}
		if authorizeCompanyAction(fixture.manager, fixture.companyA, "CREATOR_EDIT") != 0 {
			t.Fatalf("workspace %d manager lost active company", fixtureIndex+1)
		}
		for _, invisible := range []string{fixture.companyB, foreignCompany} {
			if authorizeCompanyAction(fixture.manager, invisible, "CREATOR_EDIT") != http.StatusNotFound {
				t.Fatalf("workspace %d manager IDOR for company %s was not 404", fixtureIndex+1, invisible)
			}
			if authorizeCompanyAction(fixture.creator, invisible, "") != http.StatusNotFound {
				t.Fatalf("workspace %d creator IDOR for company %s was not 404", fixtureIndex+1, invisible)
			}
		}
		if authorizeCompanyAction(fixture.owner, foreignCompany, "") != http.StatusNotFound {
			t.Fatalf("workspace %d owner crossed workspace boundary", fixtureIndex+1)
		}
		guard := (&Server{pool: pool}).requireCompanyAccess("company")((&Server{}).requireOwner(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})))
		for _, guardCase := range []struct {
			companyID string
			want      int
		}{{fixture.companyA, http.StatusForbidden}, {fixture.companyB, http.StatusNotFound}, {foreignCompany, http.StatusNotFound}, {"00000000-0000-0000-0000-000000000000", http.StatusNotFound}} {
			request := httptest.NewRequest(http.MethodDelete, "/companies/"+guardCase.companyID, nil)
			routeContext := chi.NewRouteContext()
			routeContext.URLParams.Add("id", guardCase.companyID)
			requestContext := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
			requestContext = context.WithValue(requestContext, principalKey, fixture.manager)
			response := httptest.NewRecorder()
			guard.ServeHTTP(response, request.WithContext(requestContext))
			if response.Code != guardCase.want {
				t.Fatalf("workspace %d owner-only company %s status=%d, want %d", fixtureIndex+1, guardCase.companyID, response.Code, guardCase.want)
			}
		}
	}
}
