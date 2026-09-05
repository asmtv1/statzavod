package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/statzavod/statzavod/internal/config"
)

const lifecycleTestPassword = "lifecycle-password-12"

type lifecycleFixture struct {
	pool                      *pgxpool.Pool
	server                    *Server
	organizationID, companyID string
	creatorID, ownerID        string
	cookie                    *http.Cookie
	accountIDs, syncTargetIDs []string
	oauthConnectionIDs        []string
}

func lifecycleIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("STATZAVOD_LIFECYCLE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set STATZAVOD_LIFECYCLE_TEST_DATABASE_URL to a disposable database migrated through 00023")
	}
	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newLifecycleFixture(t *testing.T, ownerCount int, withConnections bool) lifecycleFixture {
	t.Helper()
	pool := lifecycleIntegrationPool(t)
	suffix := strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	key := base64.StdEncoding.EncodeToString(bytesOf(32, 7))
	server := New(pool, config.Config{CookieName: "lifecycle_session", TokenEncryptionKey: key})
	fixture := lifecycleFixture{pool: pool, server: server}
	if err := pool.QueryRow(t.Context(), `INSERT INTO organizations(name,slug) VALUES($1,$2) RETURNING id::text`, "Lifecycle", "lifecycle-"+suffix).Scan(&fixture.organizationID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		if tx, cleanupErr := pool.Begin(cleanupCtx); cleanupErr == nil {
			cleanupErr = purgeWorkspaceContentDomain(cleanupCtx, tx, fixture.organizationID)
			if cleanupErr == nil {
				_, cleanupErr = tx.Exec(cleanupCtx, `DELETE FROM users WHERE id IN (SELECT user_id FROM organization_memberships WHERE organization_id=$1)`, fixture.organizationID)
			}
			if cleanupErr == nil {
				_, cleanupErr = tx.Exec(cleanupCtx, `DELETE FROM organizations WHERE id=$1`, fixture.organizationID)
			}
			if cleanupErr == nil {
				cleanupErr = tx.Commit(cleanupCtx)
			} else {
				_ = tx.Rollback(cleanupCtx)
			}
		}
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM media_cleanup_tasks WHERE organization_id=$1`, fixture.organizationID)
	})
	hash, err := hashPassword(lifecycleTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < ownerCount; index++ {
		var userID string
		email := fmt.Sprintf("lifecycle-%s-%d@test.local", suffix, index)
		if err = pool.QueryRow(t.Context(), `INSERT INTO users(email,password_hash,role,status) VALUES($1,$2,'ADMIN','ACTIVE') RETURNING id::text`, email, hash).Scan(&userID); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(t.Context(), `INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,'ADMIN')`, fixture.organizationID, userID); err != nil {
			t.Fatal(err)
		}
		cookie := insertLifecycleSession(t, pool, userID, nil)
		if index == 0 {
			fixture.ownerID, fixture.cookie = userID, cookie
		}
	}
	if err = pool.QueryRow(t.Context(), `INSERT INTO companies(organization_id,name) VALUES($1,'Lifecycle company') RETURNING id::text`, fixture.organizationID).Scan(&fixture.companyID); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), `INSERT INTO creators(organization_id,company_id,first_name,last_name,display_name,created_by) VALUES($1,$2,'Life','Cycle','Life Cycle',$3) RETURNING id::text`, fixture.organizationID, fixture.companyID, fixture.ownerID).Scan(&fixture.creatorID); err != nil {
		t.Fatal(err)
	}
	if withConnections {
		accessCipher, accessNonce, encryptErr := server.envelope.Encrypt([]byte("shared-canary-oauth-token"))
		if encryptErr != nil {
			t.Fatal(encryptErr)
		}
		for index := 0; index < 2; index++ {
			var accountID, connectionID, targetID string
			if err = pool.QueryRow(t.Context(), `INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name,status) VALUES($1,$2,'YOUTUBE',$3,$3,$3,'ACTIVE') RETURNING id::text`, fixture.organizationID, fixture.companyID, fmt.Sprintf("external-%s-%d", suffix, index)).Scan(&accountID); err != nil {
				t.Fatal(err)
			}
			if err = pool.QueryRow(t.Context(), `INSERT INTO oauth_connections(organization_id,platform_account_id,access_token_ciphertext,refresh_token_ciphertext,nonce,access_token_nonce,refresh_token_nonce,status) VALUES($1,$2,$3,$3,$4,$4,$4,$5) RETURNING id::text`, fixture.organizationID, accountID, accessCipher, accessNonce, map[bool]string{true: "REAUTH_REQUIRED", false: "ACTIVE"}[index == 1]).Scan(&connectionID); err != nil {
				t.Fatal(err)
			}
			if err = pool.QueryRow(t.Context(), `INSERT INTO sync_targets(organization_id,company_id,target_type,target_id,operation,status) VALUES($1,$2,'PLATFORM_ACCOUNT',$3,$4,$5) RETURNING id::text`, fixture.organizationID, fixture.companyID, accountID, fmt.Sprintf("YOUTUBE_IMPORT_%d", index), map[bool]string{true: "PAUSED", false: "ACTIVE"}[index == 1]).Scan(&targetID); err != nil {
				t.Fatal(err)
			}
			fixture.accountIDs = append(fixture.accountIDs, accountID)
			fixture.oauthConnectionIDs = append(fixture.oauthConnectionIDs, connectionID)
			fixture.syncTargetIDs = append(fixture.syncTargetIDs, targetID)
		}
	}
	return fixture
}

func bytesOf(length int, value byte) []byte {
	result := make([]byte, length)
	for index := range result {
		result[index] = value
	}
	return result
}

func insertLifecycleSession(t *testing.T, pool *pgxpool.Pool, userID string, companyID *string) *http.Cookie {
	t.Helper()
	token := makeToken()
	digest := sha256.Sum256([]byte(token))
	if _, err := pool.Exec(t.Context(), `INSERT INTO sessions(user_id,token_hash,expires_at,active_company_id) VALUES($1,$2,now()+interval '1 hour',$3)`, userID, digest[:], companyID); err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: "lifecycle_session", Value: token}
}

func lifecycleRequest(server *Server, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	server.Router().ServeHTTP(response, request)
	return response
}

func seedLifecycleContentDomain(t *testing.T, fixture lifecycleFixture) {
	t.Helper()
	if len(fixture.accountIDs) == 0 {
		t.Fatal("content lifecycle fixture requires a platform account")
	}
	var itemID, revisionID, mediaID, targetID, jobID string
	if _, err := fixture.pool.Exec(t.Context(), `INSERT INTO creator_account_assignments(creator_id,platform_account_id,assigned_by) VALUES($1,$2,$3)`, fixture.creatorID, fixture.accountIDs[0], fixture.ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(t.Context(), `INSERT INTO creator_content_approval_policies(creator_id,organization_id,company_id,updated_by) VALUES($1,$2,$3,$4)`, fixture.creatorID, fixture.organizationID, fixture.companyID, fixture.ownerID); err != nil {
		t.Fatal(err)
	}
	mustScanID(t, fixture.pool, `INSERT INTO content_items(organization_id,company_id,creator_id,created_by) VALUES($1,$2,$3,$4) RETURNING id`, &itemID, fixture.organizationID, fixture.companyID, fixture.creatorID, fixture.ownerID)
	mustScanID(t, fixture.pool, `INSERT INTO content_revisions(content_item_id,organization_id,revision,description,status,created_by) VALUES($1,$2,1,'retained successful publication','PUBLISHED',$3) RETURNING id`, &revisionID, itemID, fixture.organizationID, fixture.ownerID)
	mustScanID(t, fixture.pool, `INSERT INTO media_assets(organization_id,company_id,creator_id,status,object_key,content_sha256,original_filename,width,height,metadata,ready_at) VALUES($1,$2,$3,'READY',$4,decode('aa','hex'),'retained.mp4',9,16,jsonb_build_object('temporaryObjectKey',$5::text),now()) RETURNING id`, &mediaID, fixture.organizationID, fixture.companyID, fixture.creatorID, "lifecycle/"+itemID+".mp4", "temporary/lifecycle-"+itemID+".mp4")
	if _, err := fixture.pool.Exec(t.Context(), `INSERT INTO media_upload_sessions(media_asset_id,organization_id,actor_id,multipart_upload_id,expected_bytes,expected_mime,status,completed_at) VALUES($1,$2,$3,'completed-lifecycle-upload',1024,'video/mp4','COMPLETED',now())`, mediaID, fixture.organizationID, fixture.ownerID); err != nil {
		t.Fatal(err)
	}
	var activeUploadAsset string
	mustScanID(t, fixture.pool, `INSERT INTO media_assets(organization_id,company_id,creator_id,status,object_key,original_filename,bytes) VALUES($1,$2,$3,'UPLOADING',$4,'active-upload.mp4',1024) RETURNING id`, &activeUploadAsset, fixture.organizationID, fixture.companyID, fixture.creatorID, "temporary/active-"+itemID+".mp4")
	if _, err := fixture.pool.Exec(t.Context(), `INSERT INTO media_upload_sessions(media_asset_id,organization_id,actor_id,multipart_upload_id,expected_bytes,expected_mime,status) VALUES($1,$2,$3,$4,1024,'video/mp4','ACTIVE')`, activeUploadAsset, fixture.organizationID, fixture.ownerID, "active-lifecycle-"+itemID); err != nil {
		t.Fatal(err)
	}
	mustScanID(t, fixture.pool, `INSERT INTO content_publish_targets(content_revision_id,organization_id,company_id,creator_id,platform_account_id,platform,media_asset_id,status,external_id,external_url) VALUES($1,$2,$3,$4,$5,'YOUTUBE',$6,'SUCCEEDED',$7,'https://provider.example/retained') RETURNING id`, &targetID, revisionID, fixture.organizationID, fixture.companyID, fixture.creatorID, fixture.accountIDs[0], mediaID, "retained-"+itemID)
	mustScanID(t, fixture.pool, `INSERT INTO content_publish_jobs(target_id,content_revision_id,organization_id,company_id,run_at,status,execution_count) VALUES($1,$2,$3,$4,now(),'SUCCEEDED',1) RETURNING id`, &jobID, targetID, revisionID, fixture.organizationID, fixture.companyID)
	if _, err := fixture.pool.Exec(t.Context(), `INSERT INTO content_publish_attempts(job_id,target_id,organization_id,attempt_number,status,finished_at) VALUES($1,$2,$3,1,'SUCCEEDED',now())`, jobID, targetID, fixture.organizationID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(t.Context(), `INSERT INTO content_approval_requests(content_item_id,content_revision_id,organization_id,requested_by,status,decided_at) VALUES($1,$2,$3,$4,'CANCELLED',now())`, itemID, revisionID, fixture.organizationID, fixture.ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(t.Context(), `INSERT INTO content_notifications(organization_id,company_id,recipient_id,content_item_id,kind) VALUES($1,$2,$3,$4,'PUBLISH_SUCCEEDED')`, fixture.organizationID, fixture.companyID, fixture.ownerID, itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(t.Context(), `INSERT INTO publications(organization_id,creator_id,platform_account_id,platform,external_id,publication_type,published_at,content_item_id,content_publish_target_id) VALUES($1,$2,$3,'YOUTUBE',$4,'VIDEO',now(),$5,$6)`, fixture.organizationID, fixture.creatorID, fixture.accountIDs[0], "retained-publication-"+itemID, itemID, targetID); err != nil {
		t.Fatal(err)
	}
}

func assertLifecycleContentRetained(t *testing.T, fixture lifecycleFixture) {
	t.Helper()
	queries := []string{
		`SELECT count(*) FROM content_items WHERE company_id=$1 AND organization_id=$2`,
		`SELECT count(*) FROM content_publish_targets WHERE company_id=$1 AND organization_id=$2 AND status='SUCCEEDED'`,
		`SELECT count(*) FROM media_assets WHERE company_id=$1 AND organization_id=$2 AND status='READY'`,
		`SELECT count(*) FROM publications publication JOIN creators creator ON creator.id=publication.creator_id AND creator.organization_id=publication.organization_id WHERE creator.company_id=$1 AND publication.organization_id=$2 AND publication.content_publish_target_id IS NOT NULL`,
	}
	for _, query := range queries {
		var count int
		if err := fixture.pool.QueryRow(t.Context(), query, fixture.companyID, fixture.organizationID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("archive did not retain successful content for %q: count=%d err=%v", query, count, err)
		}
	}
}

func TestCompanyArchiveRestoreRetentionAndTwoWorkersIntegration(t *testing.T) {
	fixture := newLifecycleFixture(t, 1, true)
	seedLifecycleContentDomain(t, fixture)
	base := time.Date(2032, time.March, 4, 5, 6, 7, 0, time.UTC)
	fixture.server.now = func() time.Time { return base }
	selectionCipher, selectionNonce, err := fixture.server.envelope.Encrypt([]byte(`[{"token":{"access_token":"must-be-deleted"}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.pool.Exec(t.Context(), `INSERT INTO oauth_account_selections(organization_id,creator_id,initiated_by,platform,payload_ciphertext,nonce,expires_at) VALUES($1,$2,$3,'INSTAGRAM',$4,$5,now()+interval '10 minutes')`, fixture.organizationID, fixture.creatorID, fixture.ownerID, selectionCipher, selectionNonce); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.pool.Exec(t.Context(), `INSERT INTO oauth_states(organization_id,creator_id,platform,state_hash,pkce_verifier_ciphertext,nonce,expires_at,initiated_by) VALUES($1,$2,'YOUTUBE',$3,$4,$5,now()+interval '10 minutes',$6)`, fixture.organizationID, fixture.creatorID, []byte("archive-state"), selectionCipher, selectionNonce, fixture.ownerID); err != nil {
		t.Fatal(err)
	}
	response := lifecycleRequest(fixture.server, http.MethodDelete, "/api/v1/companies/"+fixture.companyID, "", fixture.cookie)
	if response.Code != http.StatusNoContent {
		t.Fatalf("archive status=%d body=%s", response.Code, response.Body.String())
	}
	var archivedAt, purgeAt time.Time
	if err := fixture.pool.QueryRow(t.Context(), `SELECT archived_at,purge_at FROM companies WHERE id=$1`, fixture.companyID).Scan(&archivedAt, &purgeAt); err != nil {
		t.Fatal(err)
	}
	if !archivedAt.Equal(base) || !purgeAt.Equal(base.Add(90*24*time.Hour)) {
		t.Fatalf("archive window archived=%s purge=%s", archivedAt, purgeAt)
	}
	var selectionCount int
	if err = fixture.pool.QueryRow(t.Context(), `SELECT count(*) FROM oauth_account_selections WHERE creator_id=$1`, fixture.creatorID).Scan(&selectionCount); err != nil || selectionCount != 0 {
		t.Fatalf("archived company retained %d OAuth credential selections: %v", selectionCount, err)
	}
	var stateCount int
	if err = fixture.pool.QueryRow(t.Context(), `SELECT count(*) FROM oauth_states WHERE creator_id=$1`, fixture.creatorID).Scan(&stateCount); err != nil || stateCount != 0 {
		t.Fatalf("archived company retained %d OAuth PKCE states: %v", stateCount, err)
	}
	assertLifecycleStatuses(t, fixture, "PAUSED", true, "REAUTH_REQUIRED", false, "PAUSED", true, "PAUSED", false)
	assertLifecycleContentRetained(t, fixture)
	var activeUploadStatus, activeUploadMediaStatus string
	var activeUploadExpiry time.Time
	if err = fixture.pool.QueryRow(t.Context(), `SELECT session.status,session.expires_at,media.status FROM media_upload_sessions session JOIN media_assets media ON media.id=session.media_asset_id AND media.organization_id=session.organization_id WHERE media.organization_id=$1 AND media.company_id=$2 AND media.original_filename='active-upload.mp4'`, fixture.organizationID, fixture.companyID).Scan(&activeUploadStatus, &activeUploadExpiry, &activeUploadMediaStatus); err != nil {
		t.Fatal(err)
	}
	if activeUploadStatus != "ACTIVE" || activeUploadExpiry.After(base) || activeUploadMediaStatus != "DELETE_PENDING" {
		t.Fatalf("archived upload status=%s expires=%s media=%s", activeUploadStatus, activeUploadExpiry, activeUploadMediaStatus)
	}

	fixture.server.now = func() time.Time { return base.Add(89*24*time.Hour + 23*time.Hour + 59*time.Minute) }
	// The lifecycle worker is intentionally global. Drain unrelated residue from
	// other integration fixtures, then assert this exact company is not eligible
	// one minute before its retention deadline.
	for {
		processed, runErr := fixture.server.RunLifecycle(t.Context(), "before-90", 20)
		if runErr != nil {
			t.Fatalf("89d23:59 drain err=%v", runErr)
		}
		if processed == 0 {
			break
		}
	}
	var retainedCompany int
	if err = fixture.pool.QueryRow(t.Context(), `SELECT count(*) FROM companies WHERE id=$1 AND lifecycle_state='ARCHIVED'`, fixture.companyID).Scan(&retainedCompany); err != nil || retainedCompany != 1 {
		t.Fatalf("company was purged before 90 days: count=%d err=%v", retainedCompany, err)
	}
	if response = lifecycleRequest(fixture.server, http.MethodPost, "/api/v1/companies/"+fixture.companyID+"/restore", "", fixture.cookie); response.Code != http.StatusNoContent {
		t.Fatalf("restore status=%d body=%s", response.Code, response.Body.String())
	}
	assertLifecycleStatuses(t, fixture, "ACTIVE", false, "REAUTH_REQUIRED", false, "ACTIVE", false, "PAUSED", false)
	assertLifecycleContentRetained(t, fixture)
	var referencedMediaStatus string
	if err = fixture.pool.QueryRow(t.Context(), `SELECT status FROM media_assets WHERE organization_id=$1 AND company_id=$2 AND original_filename='retained.mp4'`, fixture.organizationID, fixture.companyID).Scan(&referencedMediaStatus); err != nil || referencedMediaStatus != "READY" {
		t.Fatalf("restore referenced media status=%s err=%v", referencedMediaStatus, err)
	}

	fixture.server.now = func() time.Time { return base }
	if response = lifecycleRequest(fixture.server, http.MethodDelete, "/api/v1/companies/"+fixture.companyID, "", fixture.cookie); response.Code != http.StatusNoContent {
		t.Fatalf("second archive status=%d body=%s", response.Code, response.Body.String())
	}
	fixture.server.now = func() time.Time { return base.Add(90 * 24 * time.Hour) }
	var revokeCalls atomic.Int64
	fixture.server.revokeLifecycleToken = func(context.Context, string, string, bool) error {
		revokeCalls.Add(1)
		return errors.New("provider returned 500")
	}
	var processed [2]int
	var workerErr [2]error
	var wait sync.WaitGroup
	for index := range processed {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			processed[index], workerErr[index] = fixture.server.RunLifecycle(t.Context(), fmt.Sprintf("worker-%d", index), 1)
		}(index)
	}
	wait.Wait()
	if workerErr[0] != nil || workerErr[1] != nil || processed[0]+processed[1] != 1 {
		t.Fatalf("two workers processed=%v errors=%v", processed, workerErr)
	}
	if revokeCalls.Load() != 1 {
		t.Fatalf("deduplicated provider revokes=%d, want 1", revokeCalls.Load())
	}
	assertCompanyOrphans(t, fixture)
	var cleanupTasks, retainedPublications int
	if err = fixture.pool.QueryRow(t.Context(), `SELECT count(*) FROM media_cleanup_tasks WHERE organization_id=$1 AND former_company_id=$2 AND status='PENDING'`, fixture.organizationID, fixture.companyID).Scan(&cleanupTasks); err != nil || cleanupTasks != 4 {
		t.Fatalf("durable company media cleanup tasks=%d err=%v", cleanupTasks, err)
	}
	if err = fixture.pool.QueryRow(t.Context(), `SELECT count(*) FROM publications WHERE organization_id=$1 AND external_id LIKE 'retained-publication-%'`, fixture.organizationID).Scan(&retainedPublications); err != nil || retainedPublications != 1 {
		t.Fatalf("successful publication metadata retained=%d err=%v", retainedPublications, err)
	}
	var purgeAudits int
	if err := fixture.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_logs WHERE organization_id=$1 AND action='SYSTEM_PURGE_COMPANY'`, fixture.organizationID).Scan(&purgeAudits); err != nil || purgeAudits != 1 {
		t.Fatalf("system purge audit=%d err=%v", purgeAudits, err)
	}
}

func TestOAuthFinalSaveDoesNotPersistAfterCompanyArchiveWinsIntegration(t *testing.T) {
	fixture := newLifecycleFixture(t, 1, false)
	saveConnection, err := fixture.pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer saveConnection.Release()
	archiveConnection, err := fixture.pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer archiveConnection.Release()

	saveTx, err := saveConnection.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer saveTx.Rollback(context.Background())
	archiveTx, err := archiveConnection.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer archiveTx.Rollback(context.Background())

	// This is the lifecycle lock used by archiveCompany. Leave the archive
	// transaction open so the final OAuth save demonstrably waits on it, then
	// rechecks the row version that won the race.
	var lockedCompanyID string
	if err = archiveTx.QueryRow(t.Context(), `SELECT id::text FROM companies WHERE id=$1 AND organization_id=$2 AND archived_at IS NULL FOR UPDATE`, fixture.companyID, fixture.organizationID).Scan(&lockedCompanyID); err != nil {
		t.Fatal(err)
	}
	archivedAt := time.Date(2033, time.January, 2, 3, 4, 5, 0, time.UTC)
	if _, err = archiveTx.Exec(t.Context(), `UPDATE companies SET archived_at=$2,purge_at=$3,updated_at=$2 WHERE id=$1`, fixture.companyID, archivedAt, archivedAt.Add(90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	externalID := "archive-race-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	saveResult := make(chan error, 1)
	go func() {
		saveResult <- fixture.server.savePlatformConnectionTx(context.Background(), saveTx, fixture.organizationID, fixture.creatorID,
			oauthProvider{ID: "YOUTUBE"},
			oauthToken{AccessToken: "must-not-persist", RefreshToken: "must-not-persist", Scopes: []string{"scope"}},
			platformProfile{ExternalID: externalID, Username: externalID, DisplayName: "Archive race", AccountType: "CHANNEL"})
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		var blockerCount int
		if err = fixture.pool.QueryRow(t.Context(), `SELECT cardinality(pg_blocking_pids($1))`, int32(saveConnection.Conn().PgConn().PID())).Scan(&blockerCount); err != nil {
			t.Fatal(err)
		}
		if blockerCount > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("OAuth final save did not wait for the company archive lock")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err = archiveTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-saveResult:
		if !errors.Is(err, errOAuthTargetUnavailable) {
			t.Fatalf("final save error=%v, want OAuth target unavailable", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OAuth final save did not resume after archive commit")
	}
	if err = saveTx.Rollback(t.Context()); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		t.Fatal(err)
	}

	var accountCount, activeConnectionCount, activeSyncCount int
	if err = fixture.pool.QueryRow(t.Context(), `SELECT count(*) FROM platform_accounts WHERE organization_id=$1 AND external_id=$2`, fixture.organizationID, externalID).Scan(&accountCount); err != nil {
		t.Fatal(err)
	}
	if err = fixture.pool.QueryRow(t.Context(), `SELECT count(*) FROM oauth_connections connection JOIN platform_accounts account ON account.id=connection.platform_account_id WHERE account.organization_id=$1 AND account.external_id=$2 AND connection.status='ACTIVE'`, fixture.organizationID, externalID).Scan(&activeConnectionCount); err != nil {
		t.Fatal(err)
	}
	if err = fixture.pool.QueryRow(t.Context(), `SELECT count(*) FROM sync_targets target JOIN platform_accounts account ON account.id=target.target_id WHERE account.organization_id=$1 AND account.external_id=$2 AND target.status='ACTIVE'`, fixture.organizationID, externalID).Scan(&activeSyncCount); err != nil {
		t.Fatal(err)
	}
	if accountCount != 0 || activeConnectionCount != 0 || activeSyncCount != 0 {
		t.Fatalf("archive race persisted account=%d active OAuth=%d active sync=%d", accountCount, activeConnectionCount, activeSyncCount)
	}
}

func TestPlatformSyncPersistenceDoesNotWriteAfterCompanyArchiveWinsIntegration(t *testing.T) {
	fixture := newLifecycleFixture(t, 1, true)
	job := platformSyncJob{
		TargetID:       fixture.syncTargetIDs[0],
		AccountID:      fixture.accountIDs[0],
		OrganizationID: fixture.organizationID,
		CompanyID:      fixture.companyID,
		CreatorID:      fixture.creatorID,
		Platform:       "YOUTUBE",
	}
	externalID := "sync-archive-race-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	video := youtubeVideo{ID: externalID}
	video.Snippet.Title = "Must not persist"
	video.Snippet.PublishedAt = time.Date(2033, time.April, 4, 5, 6, 7, 0, time.UTC).Format(time.RFC3339)

	persistenceConnection, err := fixture.pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer persistenceConnection.Release()
	archiveConnection, err := fixture.pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer archiveConnection.Release()
	persistenceTx, err := persistenceConnection.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer persistenceTx.Rollback(context.Background())
	archiveTx, err := archiveConnection.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer archiveTx.Rollback(context.Background())

	// Provider I/O has completed, but persistence has not started. Let archive
	// own the company row and pause the claimed target before the sync
	// transaction reaches its final active-state gate.
	if _, err = archiveTx.Exec(t.Context(), `SELECT id FROM companies WHERE id=$1 AND organization_id=$2 FOR UPDATE`, fixture.companyID, fixture.organizationID); err != nil {
		t.Fatal(err)
	}
	archivedAt := time.Date(2033, time.April, 4, 5, 6, 8, 0, time.UTC)
	if _, err = archiveTx.Exec(t.Context(), `UPDATE companies SET archived_at=$2,purge_at=$3,updated_at=$2 WHERE id=$1`, fixture.companyID, archivedAt, archivedAt.Add(90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = archiveTx.Exec(t.Context(), `
		UPDATE sync_targets SET status='PAUSED',suspended_for_company_archive=true
		WHERE id=$1 AND organization_id=$2 AND status='ACTIVE'
	`, job.TargetID, job.OrganizationID); err != nil {
		t.Fatal(err)
	}

	persistenceResult := make(chan error, 1)
	go func() {
		_, persistErr := fixture.server.upsertYouTubeVideoTx(context.Background(), persistenceTx, job, video)
		persistenceResult <- persistErr
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var blockerCount int
		if err = fixture.pool.QueryRow(t.Context(), `SELECT cardinality(pg_blocking_pids($1))`, int32(persistenceConnection.Conn().PgConn().PID())).Scan(&blockerCount); err != nil {
			t.Fatal(err)
		}
		if blockerCount > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sync persistence did not wait for the company archive lock")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err = archiveTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-persistenceResult:
		if !errors.Is(err, errOAuthTargetUnavailable) {
			t.Fatalf("sync persistence error=%v, want target unavailable", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sync persistence did not resume after archive commit")
	}
	if err = persistenceTx.Rollback(t.Context()); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		t.Fatal(err)
	}

	var publicationCount, snapshotCount int
	if err = fixture.pool.QueryRow(t.Context(), `SELECT count(*) FROM publications WHERE organization_id=$1 AND external_id=$2`, fixture.organizationID, externalID).Scan(&publicationCount); err != nil {
		t.Fatal(err)
	}
	if err = fixture.pool.QueryRow(t.Context(), `
		SELECT count(*) FROM publication_metric_snapshots snapshot
		JOIN publications publication ON publication.id=snapshot.publication_id
		WHERE publication.organization_id=$1 AND publication.external_id=$2
	`, fixture.organizationID, externalID).Scan(&snapshotCount); err != nil {
		t.Fatal(err)
	}
	var targetStatus string
	var suspended bool
	if err = fixture.pool.QueryRow(t.Context(), `SELECT status,suspended_for_company_archive FROM sync_targets WHERE id=$1`, job.TargetID).Scan(&targetStatus, &suspended); err != nil {
		t.Fatal(err)
	}
	if publicationCount != 0 || snapshotCount != 0 || targetStatus != "PAUSED" || !suspended {
		t.Fatalf("archive race publication=%d snapshots=%d target=(%s,%v), want zero writes and paused target", publicationCount, snapshotCount, targetStatus, suspended)
	}
}

func TestOAuthAuthorizeDoesNotCreateStateAfterCompanyArchiveWinsIntegration(t *testing.T) {
	fixture := newLifecycleFixture(t, 1, false)
	fixture.server.config.YouTubeClientID = "client"
	fixture.server.config.YouTubeClientSecret = "secret"
	fixture.server.config.YouTubeRedirectURL = "https://app.test/callback"
	archiveConnection, err := fixture.pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer archiveConnection.Release()
	archiveTx, err := archiveConnection.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer archiveTx.Rollback(context.Background())
	if _, err = archiveTx.Exec(t.Context(), `SELECT id FROM companies WHERE id=$1 AND organization_id=$2 FOR UPDATE`, fixture.companyID, fixture.organizationID); err != nil {
		t.Fatal(err)
	}
	archivedAt := time.Date(2033, time.February, 3, 4, 5, 6, 0, time.UTC)
	if _, err = archiveTx.Exec(t.Context(), `UPDATE companies SET archived_at=$2,purge_at=$3 WHERE id=$1`, fixture.companyID, archivedAt, archivedAt.Add(90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	responseResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		responseResult <- lifecycleRequest(fixture.server, http.MethodPost, "/api/v1/creators/"+fixture.creatorID+"/connections/youtube/authorize", "", fixture.cookie)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		if err = fixture.pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FOR KEY SHARE%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("OAuth authorization did not wait for the archive company lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = archiveTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	response := <-responseResult
	if response.Code != http.StatusNotFound {
		t.Fatalf("OAuth authorize after archive status=%d body=%s", response.Code, response.Body.String())
	}
	var stateCount int
	if err = fixture.pool.QueryRow(t.Context(), `SELECT count(*) FROM oauth_states WHERE creator_id=$1`, fixture.creatorID).Scan(&stateCount); err != nil || stateCount != 0 {
		t.Fatalf("archive race persisted OAuth state count=%d err=%v", stateCount, err)
	}
}

func TestArchiveWinsOverResumeAndManualSyncIntegration(t *testing.T) {
	fixture := newLifecycleFixture(t, 1, true)
	accountID := fixture.accountIDs[0]
	archiveConnection, err := fixture.pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer archiveConnection.Release()
	archiveTx, err := archiveConnection.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer archiveTx.Rollback(context.Background())
	if _, err = archiveTx.Exec(t.Context(), `SELECT id FROM companies WHERE id=$1 AND organization_id=$2 FOR UPDATE`, fixture.companyID, fixture.organizationID); err != nil {
		t.Fatal(err)
	}
	archivedAt := time.Date(2033, time.March, 3, 4, 5, 6, 0, time.UTC)
	if _, err = archiveTx.Exec(t.Context(), `UPDATE companies SET archived_at=$2,purge_at=$3 WHERE id=$1`, fixture.companyID, archivedAt, archivedAt.Add(90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = archiveTx.Exec(t.Context(), `UPDATE platform_accounts SET status='PAUSED' WHERE id=$1`, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err = archiveTx.Exec(t.Context(), `UPDATE sync_targets SET status='PAUSED' WHERE target_id=$1 AND organization_id=$2`, accountID, fixture.organizationID); err != nil {
		t.Fatal(err)
	}
	call := func(handler http.HandlerFunc, method string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, "/platform-accounts/"+accountID, nil)
		routeContext := chi.NewRouteContext()
		routeContext.URLParams.Add("id", accountID)
		owner := principal{ID: fixture.ownerID, OrganizationID: fixture.organizationID, Role: roleOwner}
		request = request.WithContext(context.WithValue(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext), principalKey, owner))
		response := httptest.NewRecorder()
		handler(response, request)
		return response
	}
	resumeResult := make(chan *httptest.ResponseRecorder, 1)
	go func() { resumeResult <- call(fixture.server.resumePlatformAccount, http.MethodPost) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		if err = fixture.pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FOR KEY SHARE%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("resume did not wait for the archive company lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = archiveTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if response := <-resumeResult; response.Code != http.StatusNotFound {
		t.Fatalf("resume after archive status=%d body=%s", response.Code, response.Body.String())
	}
	if response := call(fixture.server.requestAccountSync, http.MethodPost); response.Code != http.StatusNotFound {
		t.Fatalf("sync request after archive status=%d body=%s", response.Code, response.Body.String())
	}
	var accountStatus, targetStatus string
	if err = fixture.pool.QueryRow(t.Context(), `SELECT status FROM platform_accounts WHERE id=$1`, accountID).Scan(&accountStatus); err != nil {
		t.Fatal(err)
	}
	if err = fixture.pool.QueryRow(t.Context(), `SELECT status FROM sync_targets WHERE target_id=$1 AND organization_id=$2`, accountID, fixture.organizationID).Scan(&targetStatus); err != nil {
		t.Fatal(err)
	}
	if accountStatus != "PAUSED" || targetStatus != "PAUSED" {
		t.Fatalf("archive race reactivated account=%s target=%s", accountStatus, targetStatus)
	}
}

func assertLifecycleStatuses(t *testing.T, fixture lifecycleFixture, oauth0 string, oauthFlag0 bool, oauth1 string, oauthFlag1 bool, sync0 string, syncFlag0 bool, sync1 string, syncFlag1 bool) {
	t.Helper()
	var status string
	var flag bool
	checks := []struct {
		query      string
		id         string
		wantStatus string
		wantFlag   bool
	}{
		{`SELECT status,suspended_for_company_archive FROM oauth_connections WHERE id=$1`, fixture.oauthConnectionIDs[0], oauth0, oauthFlag0},
		{`SELECT status,suspended_for_company_archive FROM oauth_connections WHERE id=$1`, fixture.oauthConnectionIDs[1], oauth1, oauthFlag1},
		{`SELECT status,suspended_for_company_archive FROM sync_targets WHERE id=$1`, fixture.syncTargetIDs[0], sync0, syncFlag0},
		{`SELECT status,suspended_for_company_archive FROM sync_targets WHERE id=$1`, fixture.syncTargetIDs[1], sync1, syncFlag1},
	}
	for _, check := range checks {
		if err := fixture.pool.QueryRow(t.Context(), check.query, check.id).Scan(&status, &flag); err != nil || status != check.wantStatus || flag != check.wantFlag {
			t.Fatalf("status id=%s got=(%s,%v) want=(%s,%v) err=%v", check.id, status, flag, check.wantStatus, check.wantFlag, err)
		}
	}
}

func assertCompanyOrphans(t *testing.T, fixture lifecycleFixture) {
	t.Helper()
	queries := []string{
		`SELECT count(*) FROM companies WHERE id=$1`,
		`SELECT count(*) FROM creators WHERE company_id=$1`,
		`SELECT count(*) FROM platform_accounts WHERE company_id=$1`,
		`SELECT count(*) FROM sync_targets WHERE company_id=$1`,
		`SELECT count(*) FROM manager_company_assignments WHERE company_id=$1`,
		`SELECT count(*) FROM creator_content_approval_policies WHERE company_id=$1`,
		`SELECT count(*) FROM content_items WHERE company_id=$1`,
		`SELECT count(*) FROM content_publish_targets WHERE company_id=$1`,
		`SELECT count(*) FROM content_publish_jobs WHERE company_id=$1`,
		`SELECT count(*) FROM content_notifications WHERE company_id=$1`,
		`SELECT count(*) FROM media_assets WHERE company_id=$1`,
	}
	for _, query := range queries {
		var count int
		if err := fixture.pool.QueryRow(t.Context(), query, fixture.companyID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("orphan scan %q count=%d err=%v", query, count, err)
		}
	}
}

func TestLifecycleProviderTimeoutDoesNotBlockLocalCascadeIntegration(t *testing.T) {
	fixture := newLifecycleFixture(t, 1, true)
	base := time.Date(2033, time.January, 1, 0, 0, 0, 0, time.UTC)
	fixture.server.now = func() time.Time { return base.Add(90 * 24 * time.Hour) }
	for {
		processed, drainErr := fixture.server.RunLifecycle(t.Context(), "timeout-preflight-drain", 20)
		if drainErr != nil {
			t.Fatalf("drain unrelated lifecycle work: %v", drainErr)
		}
		if processed == 0 {
			break
		}
	}
	if _, err := fixture.pool.Exec(t.Context(), `UPDATE companies SET archived_at=$2,purge_at=$3 WHERE id=$1`, fixture.companyID, base, base.Add(90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	fixture.server.lifecycleRevokeTimeout = 30 * time.Millisecond
	fixture.server.revokeLifecycleToken = func(ctx context.Context, _, _ string, _ bool) error {
		<-ctx.Done()
		return ctx.Err()
	}
	started := time.Now()
	processed, err := fixture.server.RunLifecycle(t.Context(), "timeout-worker", 1)
	if err != nil || processed != 1 {
		t.Fatalf("timeout worker processed=%d err=%v", processed, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded revoke took %s", elapsed)
	}
	assertCompanyOrphans(t, fixture)
}

func TestLifecycleCleansExpiredAndConsumedOAuthStatesIntegration(t *testing.T) {
	fixture := newLifecycleFixture(t, 1, false)
	ciphertext, nonce, err := fixture.server.envelope.Encrypt([]byte("stale-pkce"))
	if err != nil {
		t.Fatal(err)
	}
	for index, suffix := range []string{"expired", "consumed"} {
		query := `INSERT INTO oauth_states(organization_id,creator_id,platform,state_hash,pkce_verifier_ciphertext,nonce,expires_at,initiated_by) VALUES($1,$2,'YOUTUBE',$3,$4,$5,$6,$7)`
		expiresAt := time.Now().Add(time.Hour)
		var consumedAt any
		if index == 0 {
			expiresAt = time.Now().Add(-time.Hour)
		} else {
			consumedAt = time.Now().Add(-time.Minute)
			query = `INSERT INTO oauth_states(organization_id,creator_id,platform,state_hash,pkce_verifier_ciphertext,nonce,expires_at,consumed_at,initiated_by) VALUES($1,$2,'YOUTUBE',$3,$4,$5,$6,$7,$8)`
		}
		if consumedAt == nil {
			if _, err = fixture.pool.Exec(t.Context(), query, fixture.organizationID, fixture.creatorID, []byte("stale-state-"+suffix), ciphertext, nonce, expiresAt, fixture.ownerID); err != nil {
				t.Fatal(err)
			}
		} else if _, err = fixture.pool.Exec(t.Context(), query, fixture.organizationID, fixture.creatorID, []byte("stale-state-"+suffix), ciphertext, nonce, expiresAt, consumedAt, fixture.ownerID); err != nil {
			t.Fatal(err)
		}
	}
	if processed, err := fixture.server.RunLifecycle(t.Context(), "oauth-state-cleanup", 1); err != nil || processed != 0 {
		t.Fatalf("cleanup processed=%d err=%v", processed, err)
	}
	var count int
	if err = fixture.pool.QueryRow(t.Context(), `SELECT count(*) FROM oauth_states WHERE creator_id=$1`, fixture.creatorID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale OAuth states retained=%d err=%v", count, err)
	}
}

func TestLifecyclePurgePreservesFacebookGrantUsedByAnotherWorkspaceIntegration(t *testing.T) {
	fixture := newLifecycleFixture(t, 1, false)
	base := time.Date(2034, time.January, 1, 0, 0, 0, 0, time.UTC)
	fixture.server.now = func() time.Time { return base.Add(90 * 24 * time.Hour) }
	sharedFacebookUserID := "shared-facebook-user"
	sharedUserToken := "shared-facebook-user-token"
	accessCipher, accessNonce, err := fixture.server.envelope.Encrypt([]byte("page-token-a"))
	if err != nil {
		t.Fatal(err)
	}
	refreshCipher, refreshNonce, err := fixture.server.envelope.Encrypt([]byte(sharedUserToken))
	if err != nil {
		t.Fatal(err)
	}
	metadata := fmt.Sprintf(`{"connectionMode":"FACEBOOK","facebookUserId":%q}`, sharedFacebookUserID)
	var sourceAccountID string
	if err = fixture.pool.QueryRow(t.Context(), `INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name,status,metadata) VALUES($1,$2,'INSTAGRAM','cross-workspace-source','source','Source','ACTIVE',$3::jsonb) RETURNING id::text`, fixture.organizationID, fixture.companyID, metadata).Scan(&sourceAccountID); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.pool.Exec(t.Context(), `INSERT INTO oauth_connections(organization_id,platform_account_id,access_token_ciphertext,refresh_token_ciphertext,nonce,access_token_nonce,refresh_token_nonce,status) VALUES($1,$2,$3,$4,$5,$5,$6,'ACTIVE')`, fixture.organizationID, sourceAccountID, accessCipher, refreshCipher, accessNonce, refreshNonce); err != nil {
		t.Fatal(err)
	}

	suffix := strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	var otherOrganizationID, otherCompanyID, otherAccountID string
	if err = fixture.pool.QueryRow(t.Context(), `INSERT INTO organizations(name,slug) VALUES('Other lifecycle workspace',$1) RETURNING id::text`, "other-lifecycle-"+suffix).Scan(&otherOrganizationID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, otherOrganizationID)
	})
	if err = fixture.pool.QueryRow(t.Context(), `INSERT INTO companies(organization_id,name) VALUES($1,'Other lifecycle company') RETURNING id::text`, otherOrganizationID).Scan(&otherCompanyID); err != nil {
		t.Fatal(err)
	}
	otherAccessCipher, otherAccessNonce, err := fixture.server.envelope.Encrypt([]byte("page-token-b"))
	if err != nil {
		t.Fatal(err)
	}
	if err = fixture.pool.QueryRow(t.Context(), `INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name,status,metadata) VALUES($1,$2,'INSTAGRAM','cross-workspace-other','other','Other','ACTIVE',$3::jsonb) RETURNING id::text`, otherOrganizationID, otherCompanyID, metadata).Scan(&otherAccountID); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.pool.Exec(t.Context(), `INSERT INTO oauth_connections(organization_id,platform_account_id,access_token_ciphertext,refresh_token_ciphertext,nonce,access_token_nonce,refresh_token_nonce,status) VALUES($1,$2,$3,$4,$5,$5,$6,'ACTIVE')`, otherOrganizationID, otherAccountID, otherAccessCipher, refreshCipher, otherAccessNonce, refreshNonce); err != nil {
		t.Fatal(err)
	}
	// Workspace deletion passes companyID=nil. A grant used by another active
	// workspace must still be excluded from this workspace's revoke batch.
	workspaceTokens, err := fixture.server.lifecycleTokens(t.Context(), fixture.organizationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(workspaceTokens) != 0 {
		t.Fatalf("workspace deletion would revoke a shared Facebook grant: %#v", workspaceTokens)
	}

	if _, err = fixture.pool.Exec(t.Context(), `UPDATE companies SET archived_at=$2,purge_at=$3 WHERE id=$1`, fixture.companyID, base, base.Add(90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var revokeCalls atomic.Int64
	fixture.server.revokeLifecycleToken = func(_ context.Context, platform, token string, facebookLogin bool) error {
		if platform == "INSTAGRAM" && facebookLogin && token == sharedUserToken {
			revokeCalls.Add(1)
		}
		return nil
	}
	processed, err := fixture.server.RunLifecycle(t.Context(), "cross-workspace-facebook", 1)
	if err != nil || processed != 1 {
		t.Fatalf("purge processed=%d err=%v", processed, err)
	}
	if revokeCalls.Load() != 0 {
		t.Fatalf("purge revoked a Facebook user grant still used by another workspace: calls=%d", revokeCalls.Load())
	}
	var remainingOtherAccount int
	if err = fixture.pool.QueryRow(t.Context(), `SELECT count(*) FROM platform_accounts WHERE id=$1 AND organization_id=$2 AND status='ACTIVE'`, otherAccountID, otherOrganizationID).Scan(&remainingOtherAccount); err != nil || remainingOtherAccount != 1 {
		t.Fatalf("other workspace account was not preserved: count=%d err=%v", remainingOtherAccount, err)
	}
}

func TestConcurrentDueCompanyPurgesChooseOneFacebookGrantRevokerIntegration(t *testing.T) {
	fixture := newLifecycleFixture(t, 1, false)
	base := time.Date(2035, time.January, 1, 0, 0, 0, 0, time.UTC)
	fixture.server.now = func() time.Time { return base }
	var secondCompanyID string
	if err := fixture.pool.QueryRow(t.Context(), `INSERT INTO companies(organization_id,name) VALUES($1,'Second due company') RETURNING id::text`, fixture.organizationID).Scan(&secondCompanyID); err != nil {
		t.Fatal(err)
	}
	accessCipher, accessNonce, err := fixture.server.envelope.Encrypt([]byte("due-page-token"))
	if err != nil {
		t.Fatal(err)
	}
	refreshCipher, refreshNonce, err := fixture.server.envelope.Encrypt([]byte("due-shared-facebook-user-token"))
	if err != nil {
		t.Fatal(err)
	}
	metadata := `{"connectionMode":"FACEBOOK","facebookUserId":"due-shared-facebook-user"}`
	for index, companyID := range []string{fixture.companyID, secondCompanyID} {
		var accountID string
		if err = fixture.pool.QueryRow(t.Context(), `INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name,status,metadata) VALUES($1,$2,'INSTAGRAM',$3,$3,$3,'ACTIVE',$4::jsonb) RETURNING id::text`, fixture.organizationID, companyID, fmt.Sprintf("due-facebook-%d", index), metadata).Scan(&accountID); err != nil {
			t.Fatal(err)
		}
		if _, err = fixture.pool.Exec(t.Context(), `INSERT INTO oauth_connections(organization_id,platform_account_id,access_token_ciphertext,refresh_token_ciphertext,nonce,access_token_nonce,refresh_token_nonce,status) VALUES($1,$2,$3,$4,$5,$5,$6,'ACTIVE')`, fixture.organizationID, accountID, accessCipher, refreshCipher, accessNonce, refreshNonce); err != nil {
			t.Fatal(err)
		}
		if _, err = fixture.pool.Exec(t.Context(), `UPDATE companies SET archived_at=$2,purge_at=$2 WHERE id=$1`, companyID, base); err != nil {
			t.Fatal(err)
		}
	}
	results := make(chan []lifecycleToken, 2)
	errors := make(chan error, 2)
	for _, companyID := range []string{fixture.companyID, secondCompanyID} {
		companyID := companyID
		go func() {
			tokens, tokenErr := fixture.server.lifecycleTokens(t.Context(), fixture.organizationID, &companyID)
			if tokenErr != nil {
				errors <- tokenErr
				return
			}
			results <- tokens
		}()
	}
	count := 0
	for range 2 {
		select {
		case tokenErr := <-errors:
			t.Fatal(tokenErr)
		case tokens := <-results:
			count += len(tokens)
		}
	}
	if count != 1 {
		t.Fatalf("two due company purges selected %d Facebook revocations, want exactly one", count)
	}
}

func TestPlatformDeletionReceiptIsAnonymizedWhenAccountIsPurgedIntegration(t *testing.T) {
	fixture := newLifecycleFixture(t, 1, true)
	rawCode := "legacy-platform-deletion-confirmation"
	if _, err := fixture.pool.Exec(t.Context(), `
		INSERT INTO platform_data_deletion_requests(
		  confirmation_code,organization_id,platform_account_id,platform,external_id,status,error_message
		) VALUES($1,$2,$3,'YOUTUBE','external-user-pii','FAILED','provider-pii-error')
	`, rawCode, fixture.organizationID, fixture.accountIDs[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(t.Context(), `
		INSERT INTO platform_data_deletion_request_workspaces(confirmation_code,organization_id)
		VALUES($1,$2)
	`, rawCode, fixture.organizationID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(t.Context(), `DELETE FROM platform_accounts WHERE id=$1 AND organization_id=$2`, fixture.accountIDs[0], fixture.organizationID); err != nil {
		t.Fatal(err)
	}
	var organizationID, confirmationCode, externalID string
	var platformAccountID, errorMessage *string
	if err := fixture.pool.QueryRow(t.Context(), `
		SELECT organization_id::text,platform_account_id::text,confirmation_code,external_id,error_message
		FROM platform_data_deletion_requests
		WHERE confirmation_code=$1
	`, legacyDeletionConfirmationDigest(rawCode)).Scan(&organizationID, &platformAccountID, &confirmationCode, &externalID, &errorMessage); err != nil {
		t.Fatal(err)
	}
	if organizationID != fixture.organizationID || platformAccountID != nil || confirmationCode != legacyDeletionConfirmationDigest(rawCode) || externalID != "" || errorMessage != nil {
		t.Fatalf("non-anonymous deletion receipt org=%s account=%v code=%s external=%q error=%v", organizationID, platformAccountID, confirmationCode, externalID, errorMessage)
	}
}

func TestConcurrentOwnerSelfDeleteAndWorkspaceCascadeIntegration(t *testing.T) {
	fixture := newLifecycleFixture(t, 2, false)
	immutableKey := "media/workspace-shared-" + fixture.organizationID + ".mp4"
	temporaryKey := "temporary/workspace-source-" + fixture.organizationID + ".mp4"
	activeKey := "temporary/workspace-active-" + fixture.organizationID + ".mp4"
	var firstMediaID, activeMediaID string
	mustScanID(t, fixture.pool, `INSERT INTO media_assets(organization_id,company_id,creator_id,status,object_key,content_sha256,original_filename,width,height,metadata,ready_at) VALUES($1,$2,$3,'READY',$4,decode('abcd','hex'),'workspace-one.mp4',9,16,jsonb_build_object('temporaryObjectKey',$5::text),now()) RETURNING id`, &firstMediaID, fixture.organizationID, fixture.companyID, fixture.creatorID, immutableKey, temporaryKey)
	if _, err := fixture.pool.Exec(t.Context(), `INSERT INTO media_assets(organization_id,company_id,creator_id,status,object_key,content_sha256,original_filename,width,height,ready_at) VALUES($1,$2,$3,'READY',$4,decode('abcd','hex'),'workspace-two.mp4',9,16,now())`, fixture.organizationID, fixture.companyID, fixture.creatorID, immutableKey); err != nil {
		t.Fatal(err)
	}
	mustScanID(t, fixture.pool, `INSERT INTO media_assets(organization_id,company_id,creator_id,status,object_key,original_filename,bytes) VALUES($1,$2,$3,'UPLOADING',$4,'workspace-active.mp4',100) RETURNING id`, &activeMediaID, fixture.organizationID, fixture.companyID, fixture.creatorID, activeKey)
	if _, err := fixture.pool.Exec(t.Context(), `INSERT INTO media_upload_sessions(media_asset_id,organization_id,actor_id,multipart_upload_id,expected_bytes,expected_mime,status) VALUES($1,$2,$3,$4,100,'video/mp4','ACTIVE')`, activeMediaID, fixture.organizationID, fixture.ownerID, "workspace-active-"+fixture.organizationID); err != nil {
		t.Fatal(err)
	}
	deletionCode := deletionConfirmationDigest("workspace-delete-pii-canary")
	if _, err := fixture.pool.Exec(t.Context(), `
		INSERT INTO platform_data_deletion_requests(confirmation_code,organization_id,platform,external_id,status,error_message)
		VALUES($1,$2,'INSTAGRAM','external-pii-canary','FAILED','pii-error-canary')
	`, deletionCode, fixture.organizationID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(t.Context(), `
		INSERT INTO platform_data_deletion_request_workspaces(confirmation_code,organization_id)
		VALUES($1,$2)
	`, deletionCode, fixture.organizationID); err != nil {
		t.Fatal(err)
	}
	var secondOwnerID string
	if err := fixture.pool.QueryRow(t.Context(), `SELECT user_id::text FROM organization_memberships WHERE organization_id=$1 AND user_id<>$2 ORDER BY user_id LIMIT 1`, fixture.organizationID, fixture.ownerID).Scan(&secondOwnerID); err != nil {
		t.Fatal(err)
	}
	secondCookie := insertLifecycleSession(t, fixture.pool, secondOwnerID, nil)
	emails := make(map[string]string)
	rows, err := fixture.pool.Query(t.Context(), `SELECT id::text,email FROM users WHERE id=ANY($1::uuid[])`, []string{fixture.ownerID, secondOwnerID})
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, email string
		if err = rows.Scan(&id, &email); err != nil {
			t.Fatal(err)
		}
		emails[id] = email
	}
	rows.Close()
	requests := []struct {
		userID string
		cookie *http.Cookie
	}{{fixture.ownerID, fixture.cookie}, {secondOwnerID, secondCookie}}
	responses := make([]*httptest.ResponseRecorder, 2)
	var wait sync.WaitGroup
	for index, request := range requests {
		wait.Add(1)
		go func(index int, request struct {
			userID string
			cookie *http.Cookie
		}) {
			defer wait.Done()
			body := fmt.Sprintf(`{"currentPassword":%q,"confirmEmail":%q}`, lifecycleTestPassword, emails[request.userID])
			responses[index] = lifecycleRequest(fixture.server, http.MethodDelete, "/api/v1/users/me", body, request.cookie)
		}(index, request)
	}
	wait.Wait()
	statuses := map[int]int{}
	var receiptID string
	for _, response := range responses {
		statuses[response.Code]++
		if response.Code == http.StatusAccepted {
			var payload map[string]any
			if err = json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			receiptID, _ = payload["receiptId"].(string)
		}
	}
	if statuses[http.StatusNoContent] != 1 || statuses[http.StatusAccepted] != 1 || receiptID == "" {
		t.Fatalf("concurrent self-delete statuses=%v bodies=%q/%q", statuses, responses[0].Body.String(), responses[1].Body.String())
	}
	var lifecycle string
	var activeSessions int
	if err = fixture.pool.QueryRow(t.Context(), `SELECT lifecycle_state::text,(SELECT count(*) FROM sessions session JOIN organization_memberships membership ON membership.user_id=session.user_id WHERE membership.organization_id=$1 AND session.revoked_at IS NULL) FROM organizations WHERE id=$1`, fixture.organizationID).Scan(&lifecycle, &activeSessions); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "DELETING" || activeSessions != 0 {
		t.Fatalf("workspace lifecycle=%s activeSessions=%d", lifecycle, activeSessions)
	}
	fixture.server.now = func() time.Time { return time.Date(2034, 1, 1, 0, 0, 0, 0, time.UTC) }
	processed, err := fixture.server.RunLifecycle(t.Context(), "workspace-worker", 1)
	if err != nil || processed != 1 {
		t.Fatalf("workspace worker processed=%d err=%v", processed, err)
	}
	for _, query := range []string{
		`SELECT count(*) FROM organizations WHERE id=$1`,
		`SELECT count(*) FROM audit_logs WHERE organization_id=$1`,
		`SELECT count(*) FROM creators WHERE organization_id=$1`,
		`SELECT count(*) FROM platform_accounts WHERE organization_id=$1`,
		`SELECT count(*) FROM sync_targets WHERE organization_id=$1`,
	} {
		var count int
		if err = fixture.pool.QueryRow(t.Context(), query, fixture.organizationID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("workspace orphan scan %q count=%d err=%v", query, count, err)
		}
	}
	var deletionRequestCount int
	if err = fixture.pool.QueryRow(t.Context(), `SELECT count(*) FROM platform_data_deletion_requests WHERE confirmation_code=$1 OR external_id='external-pii-canary' OR error_message='pii-error-canary'`, deletionCode).Scan(&deletionRequestCount); err != nil || deletionRequestCount != 0 {
		t.Fatalf("platform deletion request PII survived workspace deletion count=%d err=%v", deletionRequestCount, err)
	}
	var receiptStatus, receiptText string
	if err = fixture.pool.QueryRow(t.Context(), `SELECT status,metadata::text FROM operational_receipts WHERE id=$1`, receiptID).Scan(&receiptStatus, &receiptText); err != nil {
		t.Fatal(err)
	}
	if receiptStatus != "COMPLETE" || strings.Contains(receiptText, "@") || strings.Contains(receiptText, emails[fixture.ownerID]) || strings.Contains(receiptText, emails[secondOwnerID]) {
		t.Fatalf("non-anonymous receipt status=%s metadata=%s", receiptStatus, receiptText)
	}
	var pendingCleanup, immutableDeletes int
	if err = fixture.pool.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER (WHERE kind='DELETE_OBJECT' AND object_key=$2) FROM media_cleanup_tasks WHERE organization_id=$1 AND status='PENDING'`, fixture.organizationID, immutableKey).Scan(&pendingCleanup, &immutableDeletes); err != nil {
		t.Fatal(err)
	}
	if pendingCleanup != 4 || immutableDeletes != 1 {
		t.Fatalf("workspace durable cleanup tasks=%d immutable deletes=%d", pendingCleanup, immutableDeletes)
	}
	var aborts, deletes atomic.Int64
	seenDeletes := make(map[string]int)
	var seenMu sync.Mutex
	fakeS3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if request.URL.Query().Get("uploadId") != "" {
			aborts.Add(1)
		} else {
			deletes.Add(1)
			seenMu.Lock()
			seenDeletes[strings.TrimPrefix(request.URL.Path, "/test/")]++
			seenMu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer fakeS3.Close()
	fixture.server.config.ContentPublishingEnabled = true
	fixture.server.config.MediaS3Endpoint = fakeS3.URL
	fixture.server.config.MediaS3Region = "us-east-1"
	fixture.server.config.MediaS3Bucket = "test"
	fixture.server.config.MediaS3AccessKey = "key"
	fixture.server.config.MediaS3SecretKey = "secret"
	cleaned, cleanupErr := fixture.server.RunMediaCleanup(t.Context(), "workspace-media-cleanup", 4)
	if cleanupErr != nil || cleaned != 4 || aborts.Load() != 1 || deletes.Load() != 3 {
		t.Fatalf("workspace remote cleanup processed=%d err=%v aborts=%d deletes=%d", cleaned, cleanupErr, aborts.Load(), deletes.Load())
	}
	seenMu.Lock()
	immutableDeleteCalls := seenDeletes[immutableKey]
	seenMu.Unlock()
	if immutableDeleteCalls != 1 {
		t.Fatalf("shared immutable object delete calls=%d", immutableDeleteCalls)
	}
	var completedCleanup int
	if err = fixture.pool.QueryRow(t.Context(), `SELECT count(*) FROM media_cleanup_tasks WHERE organization_id=$1 AND status='COMPLETE'`, fixture.organizationID).Scan(&completedCleanup); err != nil || completedCleanup != 4 {
		t.Fatalf("workspace cleanup receipts=%d err=%v", completedCleanup, err)
	}
}

func TestAuditCanaryAndSensitiveFailClosedIntegration(t *testing.T) {
	fixture := newLifecycleFixture(t, 1, false)
	canary := "CANARY_SECRET_DO_NOT_AUDIT_73b9"
	response := lifecycleRequest(fixture.server, http.MethodPost, "/api/v1/auth/context", `{"companyId":null,"password":"`+canary+`"}`, fixture.cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("context status=%d body=%s", response.Code, response.Body.String())
	}
	var leaked bool
	if err := fixture.pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM audit_logs WHERE organization_id=$1 AND metadata::text LIKE '%'||$2||'%')`, fixture.organizationID, canary).Scan(&leaked); err != nil || leaked {
		t.Fatalf("audit canary leaked=%v err=%v", leaked, err)
	}
	activeCookie := insertLifecycleSession(t, fixture.pool, fixture.ownerID, &fixture.companyID)
	ciphertext, nonce, err := fixture.server.envelope.Encrypt([]byte(canary))
	if err != nil {
		t.Fatal(err)
	}
	var credentialID string
	if err = fixture.pool.QueryRow(t.Context(), `INSERT INTO creator_credentials(creator_id,section,field_key,is_secret,value_ciphertext,value_nonce,updated_by) VALUES($1,'AUTH','password',true,$2,$3,$4) RETURNING id::text`, fixture.creatorID, ciphertext, nonce, fixture.ownerID).Scan(&credentialID); err != nil {
		t.Fatal(err)
	}
	fixture.server.auditFailure = func(auditRecord) error { return errors.New("forced audit outage") }
	reveal := lifecycleRequest(fixture.server, http.MethodPost, "/api/v1/creators/"+fixture.creatorID+"/credentials/"+credentialID+"/reveal", "", activeCookie)
	if reveal.Code != http.StatusInternalServerError || strings.Contains(reveal.Body.String(), canary) {
		t.Fatalf("reveal did not fail closed status=%d body=%s", reveal.Code, reveal.Body.String())
	}
	export := lifecycleRequest(fixture.server, http.MethodGet, "/api/v1/exports?creatorId="+fixture.creatorID, "", activeCookie)
	if export.Code != http.StatusInternalServerError || strings.HasPrefix(export.Body.String(), "PK") {
		t.Fatalf("export did not fail closed status=%d contentType=%s", export.Code, export.Header().Get("Content-Type"))
	}
}
