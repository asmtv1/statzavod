package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This is intentionally gated only by TEST_DATABASE_URL. When supplied it
// executes the calendar SQL against PostgreSQL, rather than accepting a mock
// that could hide alias, keyset, or partial-index errors.
func TestContentPlanningPostgresIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to a disposable database migrated through 00025")
	}
	pool, err := pgxpool.New(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var ready bool
	if err = pool.QueryRow(t.Context(), `SELECT to_regclass('public.content_publish_jobs_one_active_target_idx') IS NOT NULL`).Scan(&ready); err != nil || !ready {
		t.Fatalf("TEST_DATABASE_URL must include planning index: %v %v", ready, err)
	}
	suffix := time.Now().UTC().Format("20060102150405.000000000")
	var org, companyA, companyB, user, creatorA, creatorB, item, revision, target string
	mustScanID(t, pool, `INSERT INTO organizations(name,slug) VALUES($1,$2) RETURNING id`, &org, "planning", "planning-"+suffix)
	mustScanID(t, pool, `INSERT INTO companies(organization_id,name) VALUES($1,'A') RETURNING id`, &companyA, org)
	mustScanID(t, pool, `INSERT INTO companies(organization_id,name) VALUES($1,'B') RETURNING id`, &companyB, org)
	mustScanID(t, pool, `INSERT INTO users(email,password_hash,role,status) VALUES($1,'x','ADMIN','ACTIVE') RETURNING id`, &user, "planning-"+suffix+"@test")
	if _, err = pool.Exec(t.Context(), `INSERT INTO organization_memberships(organization_id,user_id,role,membership_role) VALUES($1,$2,'ADMIN','OWNER')`, org, user); err != nil {
		t.Fatal(err)
	}
	mustScanID(t, pool, `INSERT INTO creators(organization_id,company_id,created_by,first_name,last_name,display_name) VALUES($1,$2,$3,'A','Creator','A Creator') RETURNING id`, &creatorA, org, companyA, user)
	mustScanID(t, pool, `INSERT INTO creators(organization_id,company_id,created_by,first_name,last_name,display_name) VALUES($1,$2,$3,'B','Creator','B Creator') RETURNING id`, &creatorB, org, companyB, user)
	mustScanID(t, pool, `INSERT INTO content_items(organization_id,company_id,creator_id,created_by) VALUES($1,$2,$3,$4) RETURNING id`, &item, org, companyA, creatorA, user)
	mustScanID(t, pool, `INSERT INTO content_revisions(content_item_id,organization_id,revision,description,status,created_by) VALUES($1,$2,1,'calendar','PARTIALLY_PUBLISHED',$3) RETURNING id`, &revision, item, org, user)
	if _, err = pool.Exec(t.Context(), `UPDATE content_items SET current_revision=1 WHERE id=$1`, item); err != nil {
		t.Fatal(err)
	}
	var account string
	mustScanID(t, pool, `INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name,status) VALUES($1,$2,'YOUTUBE',$3,'test','test','ACTIVE') RETURNING id`, &account, org, companyA, "planning-"+suffix)
	if _, err = pool.Exec(t.Context(), `INSERT INTO creator_account_assignments(creator_id,platform_account_id) VALUES($1,$2)`, creatorA, account); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `INSERT INTO oauth_connections(organization_id,platform_account_id,access_token_ciphertext,nonce,access_token_nonce,status,scopes) VALUES($1,$2,decode('01','hex'),decode('02','hex'),decode('02','hex'),'ACTIVE',ARRAY['https://www.googleapis.com/auth/youtube.upload'])`, org, account); err != nil {
		t.Fatal(err)
	}
	mustScanID(t, pool, `INSERT INTO content_publish_targets(content_revision_id,organization_id,company_id,creator_id,platform_account_id,platform,status,scheduled_at) VALUES($1,$2,$3,$4,$5,'YOUTUBE','FAILED',$6) RETURNING id`, &target, revision, org, companyA, creatorA, account, time.Date(2026, 8, 31, 21, 0, 0, 0, time.UTC))
	var media, account2, unrelated string
	mustScanID(t, pool, `INSERT INTO media_assets(organization_id,company_id,creator_id,status,object_key,content_sha256,original_filename,width,height) VALUES($1,$2,$3,'READY','planning/test',decode('aa','hex'),'test.mp4',9,16) RETURNING id`, &media, org, companyA, creatorA)
	if _, err = pool.Exec(t.Context(), `UPDATE content_publish_targets SET media_asset_id=$1 WHERE id=$2`, media, target); err != nil {
		t.Fatal(err)
	}
	mustScanID(t, pool, `INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name,status) VALUES($1,$2,'VK',$3,'other','other','ACTIVE') RETURNING id`, &account2, org, companyA, "other-"+suffix)
	if _, err = pool.Exec(t.Context(), `INSERT INTO creator_account_assignments(creator_id,platform_account_id) VALUES($1,$2)`, creatorA, account2); err != nil {
		t.Fatal(err)
	}
	mustScanID(t, pool, `INSERT INTO content_publish_targets(content_revision_id,organization_id,company_id,creator_id,platform_account_id,platform,status) VALUES($1,$2,$3,$4,$5,'VK','FAILED') RETURNING id`, &unrelated, revision, org, companyA, creatorA, account2)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, org) })
	s := &Server{pool: pool, now: func() time.Time { return time.Now().UTC() }}
	request := httptest.NewRequest(http.MethodGet, "/content-items?from=2026-08-01T00:00:00Z&until=2026-09-01T00:00:00Z&limit=10", nil)
	request = request.WithContext(context.WithValue(request.Context(), principalKey, principal{ID: user, OrganizationID: org, Role: roleOwner, ActiveCompanyID: &companyA, Companies: map[string]companyAccess{companyA: testCompany(companyA)}}))
	response := httptest.NewRecorder()
	s.listContentItems(response, request)
	if response.Code != 200 {
		t.Fatalf("calendar list=%d: %s", response.Code, response.Body.String())
	}
	// A selected retry must not be blocked by the unrelated failed target that
	// deliberately has no media asset.
	retryBody := httptest.NewRequest(http.MethodPost, "/content-items/"+item+"/retry", strings.NewReader(`{"targetIds":["`+target+`"]}`))
	retryBody.Header.Set("Content-Type", "application/json")
	retryBody.Header.Set("Idempotency-Key", "retry-"+suffix)
	retryRoute := chi.NewRouteContext()
	retryRoute.URLParams.Add("id", item)
	retryBody = retryBody.WithContext(context.WithValue(context.WithValue(retryBody.Context(), chi.RouteCtxKey, retryRoute), principalKey, principal{ID: user, OrganizationID: org, Role: roleOwner, ActiveCompanyID: &companyA, Companies: map[string]companyAccess{companyA: testCompany(companyA)}}))
	retryResponse := httptest.NewRecorder()
	s.config.ContentPublishingEnabled = true
	s.config.ContentYouTubeEnabled = true
	s.retryContentTargets(retryResponse, retryBody)
	if retryResponse.Code != 200 {
		t.Fatalf("selected retry=%d: %s", retryResponse.Code, retryResponse.Body.String())
	}
	// The exact command/key replays rather than enqueueing another target job.
	retryReplay := httptest.NewRequest(http.MethodPost, "/content-items/"+item+"/retry", strings.NewReader(`{"targetIds":["`+target+`"]}`))
	retryReplay.Header.Set("Content-Type", "application/json")
	retryReplay.Header.Set("Idempotency-Key", "retry-"+suffix)
	replayRoute := chi.NewRouteContext()
	replayRoute.URLParams.Add("id", item)
	retryReplay = retryReplay.WithContext(context.WithValue(context.WithValue(retryReplay.Context(), chi.RouteCtxKey, replayRoute), principalKey, principal{ID: user, OrganizationID: org, Role: roleOwner, ActiveCompanyID: &companyA, Companies: map[string]companyAccess{companyA: testCompany(companyA)}}))
	replayResponse := httptest.NewRecorder()
	s.retryContentTargets(replayResponse, retryReplay)
	if replayResponse.Code != http.StatusOK || strings.TrimSpace(replayResponse.Body.String()) != strings.TrimSpace(retryResponse.Body.String()) {
		t.Fatalf("retry replay=%d %s, original=%d %s", replayResponse.Code, replayResponse.Body.String(), retryResponse.Code, retryResponse.Body.String())
	}
	conflict := httptest.NewRequest(http.MethodPost, "/content-items/"+item+"/retry", strings.NewReader(`{"targetIds":[]}`))
	conflict.Header.Set("Content-Type", "application/json")
	conflict.Header.Set("Idempotency-Key", "retry-"+suffix)
	conflictRoute := chi.NewRouteContext()
	conflictRoute.URLParams.Add("id", item)
	conflict = conflict.WithContext(context.WithValue(context.WithValue(conflict.Context(), chi.RouteCtxKey, conflictRoute), principalKey, principal{ID: user, OrganizationID: org, Role: roleOwner, ActiveCompanyID: &companyA, Companies: map[string]companyAccess{companyA: testCompany(companyA)}}))
	conflictResponse := httptest.NewRecorder()
	s.retryContentTargets(conflictResponse, conflict)
	if conflictResponse.Code != http.StatusConflict {
		t.Fatalf("retry conflict=%d: %s", conflictResponse.Code, conflictResponse.Body.String())
	}
	// Company B must not see company A's planning item.
	otherReq := httptest.NewRequest(http.MethodGet, "/content-items?from=2026-08-01T00:00:00Z&until=2026-09-01T00:00:00Z", nil)
	otherReq = otherReq.WithContext(context.WithValue(otherReq.Context(), principalKey, principal{ID: user, OrganizationID: org, Role: roleOwner, ActiveCompanyID: &companyB, Companies: map[string]companyAccess{companyB: testCompany(companyB)}}))
	otherResp := httptest.NewRecorder()
	s.listContentItems(otherResp, otherReq)
	var other struct {
		Items []json.RawMessage `json:"items"`
	}
	_ = json.NewDecoder(otherResp.Body).Decode(&other)
	if otherResp.Code != 200 || len(other.Items) != 0 {
		t.Fatalf("cross-company list status=%d items=%d", otherResp.Code, len(other.Items))
	}
	var got struct {
		Items []struct {
			ID string `json:"id"`
		}
		NextBefore string `json:"nextBefore"`
	}
	if err = json.NewDecoder(response.Body).Decode(&got); err != nil || len(got.Items) != 1 || got.Items[0].ID != item {
		t.Fatalf("calendar result=%+v err=%v", got, err)
	}
	// The partial unique index makes duplicate enqueue idempotent.
	for range 2 {
		if _, err = pool.Exec(t.Context(), `INSERT INTO content_publish_jobs(target_id,content_revision_id,organization_id,company_id,run_at,status) VALUES($1,$2,$3,$4,now(),'READY') ON CONFLICT DO NOTHING`, target, revision, org, companyA); err != nil {
			t.Fatal(err)
		}
	}
	var jobs int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM content_publish_jobs WHERE target_id=$1 AND status IN ('READY','RUNNING','RETRY_SCHEDULED')`, target).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("active jobs=%d err=%v", jobs, err)
	}
}
