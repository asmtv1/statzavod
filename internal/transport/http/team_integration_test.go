package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func teamIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("STATZAVOD_TEAM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set STATZAVOD_TEAM_TEST_DATABASE_URL to a disposable database migrated through 00021")
	}
	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func teamRequest(method, path, body string, p principal, params map[string]string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	routeContext := chi.NewRouteContext()
	for key, value := range params {
		routeContext.URLParams.Add(key, value)
	}
	ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
	ctx = context.WithValue(ctx, principalKey, p)
	return request.WithContext(ctx)
}

func TestWorkspaceTeamAndCreatorAccountLifecycleIntegration(t *testing.T) {
	pool := teamIntegrationPool(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var organizationID, ownerID, otherOwnerID, companyA, companyB string
	if err := pool.QueryRow(t.Context(), `INSERT INTO organizations(name,slug) VALUES('Team contract',$1) RETURNING id`, "team-contract-"+suffix).Scan(&organizationID); err != nil {
		t.Fatal(err)
	}
	seedUser := func(email, legacyRole, membershipRole string, target *string) {
		t.Helper()
		if err := pool.QueryRow(t.Context(), `INSERT INTO users(email,password_hash,role,status) VALUES($1,'integration-hash',$2,'ACTIVE') RETURNING id`, email, legacyRole).Scan(target); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(t.Context(), `INSERT INTO organization_memberships(organization_id,user_id,role,membership_role) VALUES($1,$2,$3,$4)`, organizationID, *target, legacyRole, membershipRole); err != nil {
			t.Fatal(err)
		}
	}
	seedUser("team-owner-"+suffix+"@test.local", "ADMIN", "OWNER", &ownerID)
	seedUser("team-other-owner-"+suffix+"@test.local", "ADMIN", "OWNER", &otherOwnerID)
	if err := pool.QueryRow(t.Context(), `INSERT INTO companies(organization_id,name) VALUES($1,'A') RETURNING id`, organizationID).Scan(&companyA); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO companies(organization_id,name) VALUES($1,'B') RETURNING id`, organizationID).Scan(&companyB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, organizationID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE email LIKE $1`, "%"+suffix+"%")
	})

	server := &Server{pool: pool}
	owner := principal{ID: ownerID, OrganizationID: organizationID, Role: roleOwner, Companies: map[string]companyAccess{companyA: testCompany(companyA), companyB: testCompany(companyB)}}
	managerEmail := "team-manager-" + suffix + "@test.local"
	createBody := fmt.Sprintf(`{"email":%q,"password":"manager-password-12","role":"MANAGER","companyAssignments":[{"companyId":%q,"permissions":["CREATOR_CREATE","CREATOR_ACCOUNT_MANAGE"]},{"companyId":%q,"permissions":["STATS_VIEW"]}]}`, managerEmail, companyA, companyB)
	createResponse := httptest.NewRecorder()
	server.createWorkspaceUser(createResponse, teamRequest(http.MethodPost, "/users", createBody, owner, nil))
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create manager status=%d body=%s", createResponse.Code, createResponse.Body.String())
	}
	var manager workspaceUserItem
	if err := json.Unmarshal(createResponse.Body.Bytes(), &manager); err != nil {
		t.Fatal(err)
	}
	duplicateResponse := httptest.NewRecorder()
	server.createWorkspaceUser(duplicateResponse, teamRequest(http.MethodPost, "/users", createBody, owner, nil))
	assertProblemResponse(t, duplicateResponse, http.StatusConflict)
	var companyACreate, companyAAccount, companyBStats bool
	if err := pool.QueryRow(t.Context(), `SELECT
		EXISTS(SELECT 1 FROM manager_company_assignments a JOIN manager_company_permissions p ON p.assignment_id=a.id WHERE a.manager_user_id=$1 AND a.company_id=$2 AND p.permission='CREATOR_CREATE'),
		EXISTS(SELECT 1 FROM manager_company_assignments a JOIN manager_company_permissions p ON p.assignment_id=a.id WHERE a.manager_user_id=$1 AND a.company_id=$2 AND p.permission='CREATOR_ACCOUNT_MANAGE'),
		EXISTS(SELECT 1 FROM manager_company_assignments a JOIN manager_company_permissions p ON p.assignment_id=a.id WHERE a.manager_user_id=$1 AND a.company_id=$3 AND p.permission='STATS_VIEW')`, manager.ID, companyA, companyB).Scan(&companyACreate, &companyAAccount, &companyBStats); err != nil {
		t.Fatal(err)
	}
	if !companyACreate || !companyAAccount || !companyBStats {
		t.Fatal("manager did not receive distinct company A/B permissions")
	}
	var assignmentCountBefore int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM manager_company_assignments WHERE manager_user_id=$1`, manager.ID).Scan(&assignmentCountBefore); err != nil {
		t.Fatal(err)
	}
	invalidPermission := httptest.NewRecorder()
	server.replaceManagerCompanyAssignments(invalidPermission, teamRequest(http.MethodPut, "/users/"+manager.ID+"/company-assignments", fmt.Sprintf(`{"items":[{"companyId":%q,"permissions":["USER_DELETE"]}]}`, companyA), owner, map[string]string{"id": manager.ID}))
	assertProblemResponse(t, invalidPermission, http.StatusBadRequest)
	var missingCompanyID string
	if err := pool.QueryRow(t.Context(), `SELECT uuidv7()::text`).Scan(&missingCompanyID); err != nil {
		t.Fatal(err)
	}
	missingCompany := httptest.NewRecorder()
	server.replaceManagerCompanyAssignments(missingCompany, teamRequest(http.MethodPut, "/users/"+manager.ID+"/company-assignments", fmt.Sprintf(`{"items":[{"companyId":%q,"permissions":["STATS_VIEW"]}]}`, missingCompanyID), owner, map[string]string{"id": manager.ID}))
	assertProblemResponse(t, missingCompany, http.StatusNotFound)
	var assignmentCountAfter int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM manager_company_assignments WHERE manager_user_id=$1`, manager.ID).Scan(&assignmentCountAfter); err != nil {
		t.Fatal(err)
	}
	if assignmentCountAfter != assignmentCountBefore {
		t.Fatalf("invalid atomic replacement changed assignments from %d to %d", assignmentCountBefore, assignmentCountAfter)
	}

	// Identity updates must revoke all outstanding sessions for the account.
	if _, err := pool.Exec(t.Context(), `INSERT INTO sessions(user_id,token_hash,expires_at) VALUES($1,$2,now()+interval '1 hour')`, manager.ID, []byte("team-session-"+suffix)); err != nil {
		t.Fatal(err)
	}
	updatedManagerEmail := "team-manager-updated-" + suffix + "@test.local"
	updateManager := httptest.NewRecorder()
	server.updateWorkspaceUser(updateManager, teamRequest(http.MethodPatch, "/users/"+manager.ID, fmt.Sprintf(`{"email":%q}`, updatedManagerEmail), owner, map[string]string{"id": manager.ID}))
	if updateManager.Code != http.StatusNoContent {
		t.Fatalf("update manager status=%d body=%s", updateManager.Code, updateManager.Body.String())
	}
	var activeSessions int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM sessions WHERE user_id=$1 AND revoked_at IS NULL`, manager.ID).Scan(&activeSessions); err != nil {
		t.Fatal(err)
	}
	if activeSessions != 0 {
		t.Fatalf("email change left %d active sessions", activeSessions)
	}

	managerAtA := principal{ID: manager.ID, OrganizationID: organizationID, Role: roleManager, ActiveCompanyID: &companyA, Companies: map[string]companyAccess{companyA: testCompany(companyA, "CREATOR_CREATE", "CREATOR_ACCOUNT_MANAGE")}}
	crossCompanyCreate := httptest.NewRecorder()
	server.createCreator(crossCompanyCreate, teamRequest(http.MethodPost, "/creators", fmt.Sprintf(`{"firstName":"Creator","lastName":"Wrong company","companyId":%q}`, companyB), managerAtA, nil))
	if crossCompanyCreate.Code != http.StatusBadRequest {
		t.Fatalf("manager created in inactive company status=%d body=%s", crossCompanyCreate.Code, crossCompanyCreate.Body.String())
	}
	createCreatorResponse := httptest.NewRecorder()
	server.createCreator(createCreatorResponse, teamRequest(http.MethodPost, "/creators", fmt.Sprintf(`{"firstName":"Creator","lastName":"A","companyId":%q}`, companyA), managerAtA, nil))
	if createCreatorResponse.Code != http.StatusCreated {
		t.Fatalf("manager create in permitted company status=%d body=%s", createCreatorResponse.Code, createCreatorResponse.Body.String())
	}
	var createdCreator map[string]string
	if err := json.Unmarshal(createCreatorResponse.Body.Bytes(), &createdCreator); err != nil {
		t.Fatal(err)
	}
	creatorA := createdCreator["id"]
	var creatorB string
	if err := pool.QueryRow(t.Context(), `INSERT INTO creators(organization_id,company_id,first_name,last_name,display_name,created_by) VALUES($1,$2,'Creator','B','Creator B',$3) RETURNING id`, organizationID, companyB, ownerID).Scan(&creatorB); err != nil {
		t.Fatal(err)
	}
	creatorEmail := "team-creator-" + suffix + "@test.local"
	accountResponse := httptest.NewRecorder()
	server.putCreatorLoginAccount(accountResponse, teamRequest(http.MethodPut, "/creators/"+creatorA+"/login-account", fmt.Sprintf(`{"email":%q,"password":"creator-password-12"}`, creatorEmail), managerAtA, map[string]string{"id": creatorA}))
	if accountResponse.Code != http.StatusOK {
		t.Fatalf("create creator account status=%d body=%s", accountResponse.Code, accountResponse.Body.String())
	}
	var account struct {
		ID      string `json:"id"`
		Created bool   `json:"created"`
	}
	if err := json.Unmarshal(accountResponse.Body.Bytes(), &account); err != nil || !account.Created {
		t.Fatalf("new creator account response=%s error=%v", accountResponse.Body.String(), err)
	}
	getAccountResponse := httptest.NewRecorder()
	server.getCreatorLoginAccount(getAccountResponse, teamRequest(http.MethodGet, "/creators/"+creatorA+"/login-account", "", managerAtA, map[string]string{"id": creatorA}))
	if getAccountResponse.Code != http.StatusOK || !strings.Contains(getAccountResponse.Body.String(), creatorEmail) {
		t.Fatalf("get creator login account status=%d body=%s", getAccountResponse.Code, getAccountResponse.Body.String())
	}
	invisibleAccount := httptest.NewRecorder()
	server.putCreatorLoginAccount(invisibleAccount, teamRequest(http.MethodPut, "/creators/"+missingCompanyID+"/login-account", fmt.Sprintf(`{"email":%q}`, creatorEmail), managerAtA, map[string]string{"id": missingCompanyID}))
	assertProblemResponse(t, invisibleAccount, http.StatusNotFound)
	linkResponse := httptest.NewRecorder()
	server.putCreatorLoginAccount(linkResponse, teamRequest(http.MethodPut, "/creators/"+creatorB+"/login-account", fmt.Sprintf(`{"email":%q}`, creatorEmail), owner, map[string]string{"id": creatorB}))
	if linkResponse.Code != http.StatusOK {
		t.Fatalf("link existing creator account status=%d body=%s", linkResponse.Code, linkResponse.Body.String())
	}
	var linkedUsers int
	if err := pool.QueryRow(t.Context(), `SELECT count(DISTINCT login_user_id) FROM creators WHERE id=ANY($1::uuid[])`, []string{creatorA, creatorB}).Scan(&linkedUsers); err != nil || linkedUsers != 1 {
		t.Fatalf("repeated email created another user: count=%d error=%v", linkedUsers, err)
	}
	resetResponse := httptest.NewRecorder()
	server.putCreatorLoginAccount(resetResponse, teamRequest(http.MethodPut, "/creators/"+creatorA+"/login-account", fmt.Sprintf(`{"email":%q,"password":"attempted-reset-12"}`, creatorEmail), managerAtA, map[string]string{"id": creatorA}))
	if resetResponse.Code != http.StatusForbidden {
		t.Fatalf("manager reset shared creator password status=%d body=%s", resetResponse.Code, resetResponse.Body.String())
	}

	deleteA := httptest.NewRecorder()
	server.deleteCreator(deleteA, teamRequest(http.MethodDelete, "/creators/"+creatorA, "", owner, map[string]string{"id": creatorA}))
	if deleteA.Code != http.StatusNoContent {
		t.Fatalf("delete profile A status=%d body=%s", deleteA.Code, deleteA.Body.String())
	}
	var accountExists bool
	if err := pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, account.ID).Scan(&accountExists); err != nil || !accountExists {
		t.Fatal("creator account was deleted while another profile remained")
	}
	deleteB := httptest.NewRecorder()
	server.deleteCreator(deleteB, teamRequest(http.MethodDelete, "/creators/"+creatorB, "", owner, map[string]string{"id": creatorB}))
	if deleteB.Code != http.StatusNoContent {
		t.Fatalf("delete profile B status=%d body=%s", deleteB.Code, deleteB.Body.String())
	}
	if err := pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, account.ID).Scan(&accountExists); err != nil || accountExists {
		t.Fatal("unused creator account was not removed after its last profile")
	}

	var businessCreator, businessAudit string
	if err := pool.QueryRow(t.Context(), `INSERT INTO creators(organization_id,company_id,first_name,last_name,display_name,created_by) VALUES($1,$2,'Business','Record','Business Record',$3) RETURNING id`, organizationID, companyA, manager.ID).Scan(&businessCreator); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,entity_id) VALUES($1,$2,'CREATE','CREATOR',$3) RETURNING id`, organizationID, manager.ID, businessCreator).Scan(&businessAudit); err != nil {
		t.Fatal(err)
	}
	deleteManager := httptest.NewRecorder()
	server.deleteWorkspaceUser(deleteManager, teamRequest(http.MethodDelete, "/users/"+manager.ID, "", owner, map[string]string{"id": manager.ID}))
	if deleteManager.Code != http.StatusNoContent {
		t.Fatalf("delete manager status=%d body=%s", deleteManager.Code, deleteManager.Body.String())
	}
	var creatorExists, creatorActorNull, auditActorNull bool
	if err := pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM creators WHERE id=$1),EXISTS(SELECT 1 FROM creators WHERE id=$1 AND created_by IS NULL),EXISTS(SELECT 1 FROM audit_logs WHERE id=$2 AND actor_id IS NULL)`, businessCreator, businessAudit).Scan(&creatorExists, &creatorActorNull, &auditActorNull); err != nil {
		t.Fatal(err)
	}
	if !creatorExists || !creatorActorNull || !auditActorNull {
		t.Fatal("manager deletion removed business data or retained actor references")
	}
	deleteSelfWithCoOwner := httptest.NewRecorder()
	server.deleteWorkspaceUser(deleteSelfWithCoOwner, teamRequest(http.MethodDelete, "/users/"+ownerID, "", owner, map[string]string{"id": ownerID}))
	if deleteSelfWithCoOwner.Code != http.StatusConflict {
		t.Fatalf("delete self with co-owner status=%d body=%s", deleteSelfWithCoOwner.Code, deleteSelfWithCoOwner.Body.String())
	}
	var ownerStillExists bool
	if err := pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, ownerID).Scan(&ownerStillExists); err != nil || !ownerStillExists {
		t.Fatal("generic team endpoint deleted its current owner")
	}

	deleteOtherOwner := httptest.NewRecorder()
	server.deleteWorkspaceUser(deleteOtherOwner, teamRequest(http.MethodDelete, "/users/"+otherOwnerID, "", owner, map[string]string{"id": otherOwnerID}))
	if deleteOtherOwner.Code != http.StatusNoContent {
		t.Fatalf("delete other owner status=%d body=%s", deleteOtherOwner.Code, deleteOtherOwner.Body.String())
	}
	deleteLastOwner := httptest.NewRecorder()
	server.deleteWorkspaceUser(deleteLastOwner, teamRequest(http.MethodDelete, "/users/"+ownerID, "", owner, map[string]string{"id": ownerID}))
	if deleteLastOwner.Code != http.StatusConflict {
		t.Fatalf("delete last owner status=%d body=%s", deleteLastOwner.Code, deleteLastOwner.Body.String())
	}
}

func assertProblemResponse(t *testing.T, response *httptest.ResponseRecorder, wantStatus int) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("problem status=%d body=%s, want %d", response.Code, response.Body.String(), wantStatus)
	}
	if response.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("problem content type=%q", response.Header().Get("Content-Type"))
	}
	var body struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Type == "" || body.Title == "" || body.Detail == "" || body.Status != wantStatus {
		t.Fatalf("incomplete problem response: %#v", body)
	}
}

func TestConcurrentLastCreatorProfilesDeleteAccountIntegration(t *testing.T) {
	pool := teamIntegrationPool(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var organizationID, ownerID, creatorUserID, companyA, companyB, creatorA, creatorB string
	if err := pool.QueryRow(t.Context(), `INSERT INTO organizations(name,slug) VALUES('Concurrent creator delete',$1) RETURNING id`, "concurrent-creator-delete-"+suffix).Scan(&organizationID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(email,password_hash,role,status) VALUES($1,'hash','ADMIN','ACTIVE') RETURNING id`, "concurrent-owner-"+suffix+"@test.local").Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(email,password_hash,role,status) VALUES($1,'hash','VIEWER','ACTIVE') RETURNING id`, "concurrent-creator-"+suffix+"@test.local").Scan(&creatorUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO organization_memberships(organization_id,user_id,role,membership_role) VALUES($1,$2,'ADMIN','OWNER'),($1,$3,'VIEWER','CREATOR')`, organizationID, ownerID, creatorUserID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO companies(organization_id,name) VALUES($1,'A') RETURNING id`, organizationID).Scan(&companyA); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO companies(organization_id,name) VALUES($1,'B') RETURNING id`, organizationID).Scan(&companyB); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO creators(organization_id,company_id,login_user_id,first_name,last_name,middle_name,display_name,internal_note,telegram_username,work_status,work_comment,created_by) VALUES($1,$2,$3,'Concurrent','A','Private A','Concurrent A','private note A','private_a','NEEDS_ATTENTION','private work A',$4) RETURNING id`, organizationID, companyA, creatorUserID, ownerID).Scan(&creatorA); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO creators(organization_id,company_id,login_user_id,first_name,last_name,middle_name,display_name,internal_note,telegram_username,work_status,work_comment,created_by) VALUES($1,$2,$3,'Concurrent','B','Private B','Concurrent B','private note B','private_b','NEEDS_ATTENTION','private work B',$4) RETURNING id`, organizationID, companyB, creatorUserID, ownerID).Scan(&creatorB); err != nil {
		t.Fatal(err)
	}
	for _, creatorID := range []string{creatorA, creatorB} {
		if _, err := pool.Exec(t.Context(), `INSERT INTO creator_contacts(creator_id,kind,value) VALUES($1,'EMAIL',$2)`, creatorID, "private-"+creatorID+"@test.local"); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(t.Context(), `INSERT INTO creator_credentials(creator_id,section,field_key,value_ciphertext,value_nonce,updated_by) VALUES($1,'TIKTOK','login',$2,$3,$4)`, creatorID, []byte("private-ciphertext"), []byte("private-nonce"), ownerID); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, organizationID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=ANY($1::uuid[])`, []string{ownerID, creatorUserID})
	})

	// Hold the same lifecycle key so both HTTP transactions reach the lock
	// concurrently after locking their distinct profile rows.
	blocker, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,1))`, creatorUserID); err != nil {
		t.Fatal(err)
	}
	owner := principal{ID: ownerID, OrganizationID: organizationID, Role: roleOwner, Companies: map[string]companyAccess{companyA: testCompany(companyA), companyB: testCompany(companyB)}}
	server := &Server{pool: pool}
	responses := make(chan int, 2)
	var group sync.WaitGroup
	for _, creatorID := range []string{creatorA, creatorB} {
		group.Add(1)
		go func(id string) {
			defer group.Done()
			response := httptest.NewRecorder()
			server.deleteCreator(response, teamRequest(http.MethodDelete, "/creators/"+id, "", owner, map[string]string{"id": id}))
			responses <- response.Code
		}(creatorID)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND wait_event='advisory'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= 2 {
			break
		}
		if time.Now().After(deadline) {
			_ = blocker.Rollback(t.Context())
			t.Fatal("creator deletion transactions did not concurrently wait on the account lifecycle lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	group.Wait()
	close(responses)
	for status := range responses {
		if status != http.StatusNoContent {
			t.Fatalf("concurrent profile deletion returned %d, want 204", status)
		}
	}
	var tombstoneCount, userCount, anonymousTombstones, privateRows, loginLinkage int
	creatorIDs := []string{creatorA, creatorB}
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM creators WHERE id=ANY($1::uuid[])`, creatorIDs).Scan(&tombstoneCount); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM users WHERE id=$1`, creatorUserID).Scan(&userCount); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM creators WHERE id=ANY($1::uuid[]) AND status='ARCHIVED' AND archived_at IS NOT NULL AND login_user_id IS NULL AND first_name='Deleted' AND last_name='Creator' AND middle_name IS NULL AND display_name='Deleted creator' AND internal_note='' AND telegram_username='' AND work_status='OK' AND work_comment=''`, creatorIDs).Scan(&anonymousTombstones); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM creator_contacts WHERE creator_id=ANY($1::uuid[])) + (SELECT count(*) FROM creator_credentials WHERE creator_id=ANY($1::uuid[]))`, creatorIDs).Scan(&privateRows); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM organization_memberships WHERE organization_id=$1 AND user_id=$2`, organizationID, creatorUserID).Scan(&loginLinkage); err != nil {
		t.Fatal(err)
	}
	if tombstoneCount != 2 || anonymousTombstones != 2 || userCount != 0 || privateRows != 0 || loginLinkage != 0 {
		t.Fatalf("concurrent deletion tombstones=%d anonymous=%d creator_accounts=%d private_rows=%d login_linkage=%d", tombstoneCount, anonymousTombstones, userCount, privateRows, loginLinkage)
	}
}

func TestConcurrentOwnerDeletionPreservesLastOwnerIntegration(t *testing.T) {
	pool := teamIntegrationPool(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var organizationID, ownerA, ownerB string
	if err := pool.QueryRow(t.Context(), `INSERT INTO organizations(name,slug) VALUES('Concurrent owner delete',$1) RETURNING id`, "concurrent-owner-delete-"+suffix).Scan(&organizationID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(email,password_hash,role,status) VALUES($1,'hash','ADMIN','ACTIVE') RETURNING id`, "concurrent-owner-a-"+suffix+"@test.local").Scan(&ownerA); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(email,password_hash,role,status) VALUES($1,'hash','ADMIN','ACTIVE') RETURNING id`, "concurrent-owner-b-"+suffix+"@test.local").Scan(&ownerB); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO organization_memberships(organization_id,user_id,role,membership_role) VALUES($1,$2,'ADMIN','OWNER'),($1,$3,'ADMIN','OWNER')`, organizationID, ownerA, ownerB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, organizationID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=ANY($1::uuid[])`, []string{ownerA, ownerB})
	})
	blocker, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, organizationID); err != nil {
		t.Fatal(err)
	}
	server := &Server{pool: pool}
	type deletion struct{ actor, target string }
	responses := make(chan int, 2)
	var group sync.WaitGroup
	for _, operation := range []deletion{{ownerA, ownerB}, {ownerB, ownerA}} {
		group.Add(1)
		go func(operation deletion) {
			defer group.Done()
			response := httptest.NewRecorder()
			actor := principal{ID: operation.actor, OrganizationID: organizationID, Role: roleOwner}
			server.deleteWorkspaceUser(response, teamRequest(http.MethodDelete, "/users/"+operation.target, "", actor, map[string]string{"id": operation.target}))
			responses <- response.Code
		}(operation)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND wait_event='advisory'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= 2 {
			break
		}
		if time.Now().After(deadline) {
			_ = blocker.Rollback(t.Context())
			t.Fatal("owner deletion transactions did not concurrently wait on the workspace lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	group.Wait()
	close(responses)
	statuses := map[int]int{}
	for status := range responses {
		statuses[status]++
	}
	if statuses[http.StatusNoContent] != 1 || statuses[http.StatusConflict] != 1 {
		t.Fatalf("concurrent owner deletion statuses=%v, want one 204 and one 409", statuses)
	}
	var owners int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM organization_memberships WHERE organization_id=$1 AND membership_role='OWNER'`, organizationID).Scan(&owners); err != nil {
		t.Fatal(err)
	}
	if owners != 1 {
		t.Fatalf("concurrent owner deletion left %d owners", owners)
	}
}

func TestCreateCreatorAndCompanyArchiveAreSerializedIntegration(t *testing.T) {
	pool := teamIntegrationPool(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var organizationID, ownerID, companyID string
	if err := pool.QueryRow(t.Context(), `INSERT INTO organizations(name,slug) VALUES('Creator archive race',$1) RETURNING id`, "creator-archive-race-"+suffix).Scan(&organizationID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(email,password_hash,role,status) VALUES($1,'hash','ADMIN','ACTIVE') RETURNING id`, "creator-archive-owner-"+suffix+"@test.local").Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO organization_memberships(organization_id,user_id,role,membership_role) VALUES($1,$2,'ADMIN','OWNER')`, organizationID, ownerID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO companies(organization_id,name) VALUES($1,'Race company') RETURNING id`, organizationID).Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, organizationID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, ownerID)
	})

	// Model archiveCompany's lock/update but hold the transaction open. The
	// create request must wait for this decision, then recheck archived_at.
	archiveTx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = archiveTx.Exec(t.Context(), `SELECT id FROM companies WHERE id=$1 FOR UPDATE`, companyID); err != nil {
		t.Fatal(err)
	}
	if _, err = archiveTx.Exec(t.Context(), `UPDATE companies SET archived_at=now(),updated_at=now() WHERE id=$1`, companyID); err != nil {
		t.Fatal(err)
	}
	owner := principal{ID: ownerID, OrganizationID: organizationID, Role: roleOwner, ActiveCompanyID: &companyID, Companies: map[string]companyAccess{companyID: testCompany(companyID)}}
	server := &Server{pool: pool}
	responseChannel := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		server.createCreator(response, teamRequest(http.MethodPost, "/creators", `{"firstName":"Race","lastName":"Creator"}`, owner, nil))
		responseChannel <- response
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FOR KEY SHARE%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= 1 {
			break
		}
		if time.Now().After(deadline) {
			_ = archiveTx.Rollback(t.Context())
			t.Fatal("creator creation did not wait for the company archive lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = archiveTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	response := <-responseChannel
	if response.Code != http.StatusNotFound {
		t.Fatalf("create racing with archive status=%d body=%s, want 404", response.Code, response.Body.String())
	}
	var creators int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM creators WHERE company_id=$1`, companyID).Scan(&creators); err != nil {
		t.Fatal(err)
	}
	if creators != 0 {
		t.Fatalf("archived company received %d creator profiles", creators)
	}
}
