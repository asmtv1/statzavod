package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDeleteCreatorIntegration(t *testing.T) {
	databaseURL := os.Getenv("STATZAVOD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set STATZAVOD_TEST_DATABASE_URL to a disposable database migrated through 00017")
	}

	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var deletionContractExists bool
	err = pool.QueryRow(context.Background(), `SELECT EXISTS (
		SELECT 1 FROM pg_constraint
		WHERE conname='creator_account_assignments_creator_id_fkey' AND confdeltype='c'
	) AND EXISTS (
		SELECT 1 FROM pg_constraint
		WHERE conname='creators_created_by_fkey' AND confdeltype='n'
	)`).Scan(&deletionContractExists)
	if err != nil || !deletionContractExists {
		t.Fatalf("test database must be migrated through creator ownership changes (contract=%v, error=%v)", deletionContractExists, err)
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var organizationID, otherOrganizationID, adminID, ownerID, nonOwnerID, creatorID, adminDeletedCreatorID, accountID string
	mustScanID(t, pool, `INSERT INTO organizations(name,slug) VALUES($1,$2) RETURNING id`, &organizationID, "Deletion test", "deletion-test-"+suffix)
	mustScanID(t, pool, `INSERT INTO organizations(name,slug) VALUES($1,$2) RETURNING id`, &otherOrganizationID, "Other deletion test", "other-deletion-test-"+suffix)
	mustScanID(t, pool, `INSERT INTO users(email,password_hash,role,status) VALUES($1,'test','ADMIN','ACTIVE') RETURNING id`, &adminID, "deletion-admin-"+suffix+"@example.com")
	mustScanID(t, pool, `INSERT INTO users(email,password_hash,role,status) VALUES($1,'test','ANALYST','ACTIVE') RETURNING id`, &ownerID, "deletion-owner-"+suffix+"@example.com")
	mustScanID(t, pool, `INSERT INTO users(email,password_hash,role,status) VALUES($1,'test','ANALYST','ACTIVE') RETURNING id`, &nonOwnerID, "deletion-non-owner-"+suffix+"@example.com")
	mustScanID(t, pool, `INSERT INTO creators(organization_id,created_by,first_name,last_name,display_name) VALUES($1,$2,'Delete','Me','Delete Me') RETURNING id`, &creatorID, organizationID, ownerID)
	mustScanID(t, pool, `INSERT INTO platform_accounts(organization_id,platform,external_id,username,display_name) VALUES($1,'TIKTOK',$2,$2,'Preserved account') RETURNING id`, &accountID, organizationID, "deletion-test-"+suffix)
	server := &Server{pool: pool}

	createResponse := createCreatorResponse(server, principal{ID: ownerID, Role: "ANALYST", OrganizationID: organizationID})
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create creator returned %d; want 201; body=%s", createResponse.Code, createResponse.Body.String())
	}
	var createdPayload map[string]string
	if err := json.Unmarshal(createResponse.Body.Bytes(), &createdPayload); err != nil {
		t.Fatalf("decode created creator: %v", err)
	}
	adminDeletedCreatorID = createdPayload["id"]
	if adminDeletedCreatorID == "" {
		t.Fatal("create creator response has no id")
	}
	var createdBy string
	if err := pool.QueryRow(context.Background(), `SELECT created_by FROM creators WHERE id=$1`, adminDeletedCreatorID).Scan(&createdBy); err != nil {
		t.Fatalf("load creator ownership: %v", err)
	}
	if createdBy != ownerID {
		t.Fatalf("created_by = %s; want creator actor %s", createdBy, ownerID)
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_logs WHERE organization_id IN ($1,$2)`, organizationID, otherOrganizationID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM creators WHERE id IN ($1,$2)`, creatorID, adminDeletedCreatorID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM platform_accounts WHERE id=$1`, accountID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id IN ($1,$2,$3)`, adminID, ownerID, nonOwnerID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id IN ($1,$2)`, organizationID, otherOrganizationID)
	})

	ownedRows := seedCreatorOwnedRows(t, pool, organizationID, ownerID, creatorID, accountID, suffix)

	nonOwnerResponse := deleteCreatorResponse(server, creatorID, principal{ID: nonOwnerID, Role: "ANALYST", OrganizationID: organizationID})
	if nonOwnerResponse.Code != http.StatusNotFound {
		t.Fatalf("non-owner analyst delete returned %d; want 404", nonOwnerResponse.Code)
	}
	assertRowCount(t, pool, "creators", creatorID, 1)

	wrongOrganizationResponse := deleteCreatorResponse(server, creatorID, principal{ID: ownerID, Role: "ANALYST", OrganizationID: otherOrganizationID})
	if wrongOrganizationResponse.Code != http.StatusNotFound {
		t.Fatalf("delete through another organization returned %d; want 404", wrongOrganizationResponse.Code)
	}
	assertRowCount(t, pool, "creators", creatorID, 1)

	deleteResponse := deleteCreatorResponse(server, creatorID, principal{ID: ownerID, Role: "ANALYST", OrganizationID: organizationID})
	if deleteResponse.Code != http.StatusNoContent {
		t.Fatalf("delete creator returned %d; want 204; body=%s", deleteResponse.Code, deleteResponse.Body.String())
	}

	assertRowCount(t, pool, "creators", creatorID, 0)
	for table, id := range ownedRows {
		assertRowCount(t, pool, table, id, 0)
	}
	assertRowCount(t, pool, "platform_accounts", accountID, 1)

	secondDeleteResponse := deleteCreatorResponse(server, creatorID, principal{ID: ownerID, Role: "ANALYST", OrganizationID: organizationID})
	if secondDeleteResponse.Code != http.StatusNotFound {
		t.Fatalf("delete missing creator returned %d; want 404", secondDeleteResponse.Code)
	}

	adminDeleteResponse := deleteCreatorResponse(server, adminDeletedCreatorID, principal{ID: adminID, Role: "ADMIN", OrganizationID: organizationID})
	if adminDeleteResponse.Code != http.StatusNoContent {
		t.Fatalf("admin delete of another user's creator returned %d; want 204", adminDeleteResponse.Code)
	}
	assertRowCount(t, pool, "creators", adminDeletedCreatorID, 0)
}

func createCreatorResponse(server *Server, p principal) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/creators", strings.NewReader(`{"firstName":"Admin","lastName":"Deletes","displayName":"Admin Deletes"}`))
	request = request.WithContext(context.WithValue(request.Context(), principalKey, p))
	server.createCreator(recorder, request)
	return recorder
}

func seedCreatorOwnedRows(t *testing.T, pool *pgxpool.Pool, organizationID, actorID, creatorID, accountID, suffix string) map[string]string {
	t.Helper()
	rows := make(map[string]string)
	seed := func(table, query string, args ...any) {
		var id string
		mustScanID(t, pool, query, &id, args...)
		rows[table] = id
	}
	seed("creator_contacts", `INSERT INTO creator_contacts(creator_id,kind,value) VALUES($1,'EMAIL',$2) RETURNING id`, creatorID, "delete-"+suffix+"@example.com")
	seed("creator_credentials", `INSERT INTO creator_credentials(creator_id,section,field_key,value_ciphertext,value_nonce,updated_by) VALUES($1,'TIKTOK','login',$2,$3,$4) RETURNING id`, creatorID, []byte("ciphertext"), []byte("nonce"), actorID)
	seed("creator_account_assignments", `INSERT INTO creator_account_assignments(creator_id,platform_account_id,assigned_by) VALUES($1,$2,$3) RETURNING id`, creatorID, accountID, actorID)
	seed("oauth_states", `INSERT INTO oauth_states(organization_id,creator_id,platform,state_hash,nonce,expires_at,initiated_by) VALUES($1,$2,'TIKTOK',$3,$4,now()+interval '1 hour',$5) RETURNING id`, organizationID, creatorID, []byte("state-"+suffix), []byte("nonce"), actorID)
	seed("publications", `INSERT INTO publications(organization_id,creator_id,platform_account_id,platform,external_id,publication_type,published_at) VALUES($1,$2,$3,'TIKTOK',$4,'VIDEO',now()) RETURNING id`, organizationID, creatorID, accountID, "publication-"+suffix)
	seed("content_groups", `INSERT INTO content_groups(creator_id,name,created_by) VALUES($1,'Deletion group',$2) RETURNING id`, creatorID, actorID)

	var historyEventID string
	mustScanID(t, pool, `INSERT INTO creator_history_events(organization_id,creator_id,actor_id,block) VALUES($1,$2,$3,'PROFILE') RETURNING id`, &historyEventID, organizationID, creatorID, actorID)
	rows["creator_history_events"] = historyEventID
	seed("creator_history_changes", `INSERT INTO creator_history_changes(event_id,field_key,old_value,new_value) VALUES($1,'displayName','Before','After') RETURNING id`, historyEventID)
	return rows
}

func deleteCreatorResponse(server *Server, creatorID string, p principal) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/creators/"+creatorID, nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", creatorID)
	ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
	ctx = context.WithValue(ctx, principalKey, p)
	server.deleteCreator(recorder, request.WithContext(ctx))
	return recorder
}

func mustScanID(t *testing.T, pool *pgxpool.Pool, query string, destination any, args ...any) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), query, args...).Scan(destination); err != nil {
		t.Fatalf("seed test row: %v", err)
	}
}

func assertRowCount(t *testing.T, pool *pgxpool.Pool, table, id string, want int) {
	t.Helper()
	var got int
	query := fmt.Sprintf("SELECT count(*) FROM %s WHERE id=$1", table)
	if err := pool.QueryRow(context.Background(), query, id).Scan(&got); err != nil {
		t.Fatalf("count %s row: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s row count = %d; want %d", table, got, want)
	}
}
