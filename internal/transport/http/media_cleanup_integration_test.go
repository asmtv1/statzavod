package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/statzavod/statzavod/internal/config"
)

func TestMediaQuotaAndCleanupPostgresIntegration(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to a disposable database migrated through 00026")
	}
	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	suffix := time.Now().UTC().Format("20060102150405.000000000")
	var org, company, owner, creatorUser, creator string
	mustScanID(t, pool, `INSERT INTO organizations(name,slug) VALUES('media-cleanup',$1) RETURNING id`, &org, "media-cleanup-"+suffix)
	mustScanID(t, pool, `INSERT INTO companies(organization_id,name) VALUES($1,'media-cleanup') RETURNING id`, &company, org)
	mustScanID(t, pool, `INSERT INTO users(email,password_hash,role,status) VALUES($1,'x','ADMIN','ACTIVE') RETURNING id`, &owner, "media-owner-"+suffix+"@test")
	mustScanID(t, pool, `INSERT INTO users(email,password_hash,role,status) VALUES($1,'x','VIEWER','ACTIVE') RETURNING id`, &creatorUser, "media-creator-"+suffix+"@test")
	if _, err = pool.Exec(t.Context(), `INSERT INTO organization_memberships(organization_id,user_id,role,membership_role) VALUES ($1,$2,'ADMIN','OWNER'),($1,$3,'VIEWER','CREATOR')`, org, owner, creatorUser); err != nil {
		t.Fatal(err)
	}
	mustScanID(t, pool, `INSERT INTO creators(organization_id,company_id,created_by,login_user_id,first_name,last_name,display_name) VALUES($1,$2,$3,$4,'Media','Creator','Media Creator') RETURNING id`, &creator, org, company, owner, creatorUser)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_cleanup_tasks WHERE organization_id=$1`, org)
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, org)
	})

	var aborts, deletes, creates, failDelete, abortMissing int32
	fakeS3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.RawQuery, "uploads"):
			atomic.AddInt32(&creates, 1)
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<InitiateMultipartUploadResult><UploadId>unexpected</UploadId></InitiateMultipartUploadResult>`))
		case r.Method == http.MethodDelete && r.URL.Query().Get("uploadId") != "":
			atomic.AddInt32(&aborts, 1)
			if atomic.LoadInt32(&abortMissing) != 0 {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`<Error><Code>NoSuchUpload</Code><Message>already aborted</Message></Error>`))
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete:
			atomic.AddInt32(&deletes, 1)
			if atomic.LoadInt32(&failDelete) != 0 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer fakeS3.Close()
	s := &Server{pool: pool, now: time.Now, config: config.Config{ContentPublishingEnabled: true, MediaS3Endpoint: fakeS3.URL, MediaS3Region: "us-east-1", MediaS3Bucket: "test", MediaS3AccessKey: "key", MediaS3SecretKey: "secret"}}
	// The integration package deliberately shares one disposable database. Drain
	// durable cleanup work left by lifecycle tests before asserting this test's
	// exact worker counts; those jobs are valid global work, not part of this
	// fixture.
	for {
		processed, cleanupErr := s.RunMediaCleanup(t.Context(), "cleanup-drain-"+suffix, 100)
		if cleanupErr != nil {
			t.Fatal(cleanupErr)
		}
		if processed == 0 {
			break
		}
	}
	atomic.StoreInt32(&aborts, 0)
	atomic.StoreInt32(&deletes, 0)

	var quotaAsset string
	mustScanID(t, pool, `INSERT INTO media_assets(organization_id,company_id,creator_id,status,object_key,original_filename,bytes) VALUES($1,$2,$3,'UPLOADING',$4,'quota.mp4',1) RETURNING id`, &quotaAsset, org, company, creator, "temporary/quota-"+suffix)
	for i := 0; i < mediaUploadMaxActorSessions; i++ {
		if _, err = pool.Exec(t.Context(), `INSERT INTO media_upload_sessions(media_asset_id,organization_id,actor_id,multipart_upload_id,expected_bytes,expected_mime) VALUES($1,$2,$3,$4,1,'video/mp4')`, quotaAsset, org, creatorUser, fmt.Sprintf("quota-%d-%s", i, suffix)); err != nil {
			t.Fatal(err)
		}
	}
	p := principal{ID: creatorUser, OrganizationID: org, Role: roleCreator, ActiveCompanyID: &company, ActiveCreatorID: &creator, Companies: map[string]companyAccess{company: testCompany(company)}, CreatorProfiles: map[string]creatorProfileAccess{creator: {ID: creator, CompanyID: company}}}
	r := httptest.NewRequest(http.MethodPost, "/creator-portal/media/uploads", strings.NewReader(`{"filename":"sixth.mp4","mime":"video/mp4","bytes":1}`))
	r.Header.Set("Idempotency-Key", "media-quota-"+suffix)
	r = r.WithContext(context.WithValue(r.Context(), principalKey, p))
	w := httptest.NewRecorder()
	s.createMediaUpload(w, r)
	if w.Code != http.StatusTooManyRequests || atomic.LoadInt32(&creates) != 0 {
		t.Fatalf("quota response=%d creates=%d body=%s", w.Code, creates, w.Body.String())
	}

	var expiredAsset, expiredSession, orphanAsset, referencedAsset, account, item, revision string
	mustScanID(t, pool, `INSERT INTO media_assets(organization_id,company_id,creator_id,status,object_key,original_filename,bytes) VALUES($1,$2,$3,'UPLOADING',$4,'expired.mp4',10) RETURNING id`, &expiredAsset, org, company, creator, "temporary/expired-"+suffix)
	mustScanID(t, pool, `INSERT INTO media_upload_sessions(media_asset_id,organization_id,actor_id,multipart_upload_id,expected_bytes,expected_mime,expires_at) VALUES($1,$2,$3,$4,10,'video/mp4',now()-interval '1 hour') RETURNING id`, &expiredSession, expiredAsset, org, creatorUser, "expired-"+suffix)
	mustScanID(t, pool, `INSERT INTO media_assets(organization_id,company_id,creator_id,status,object_key,content_sha256,original_filename,width,height,created_at) VALUES($1,$2,$3,'READY',$4,decode(md5($4),'hex'),'orphan.mp4',9,16,now()-interval '8 days') RETURNING id`, &orphanAsset, org, company, creator, "media/orphan-"+suffix)
	mustScanID(t, pool, `INSERT INTO media_assets(organization_id,company_id,creator_id,status,object_key,content_sha256,original_filename,width,height,created_at) VALUES($1,$2,$3,'READY',$4,decode(md5($4),'hex'),'referenced.mp4',9,16,now()-interval '8 days') RETURNING id`, &referencedAsset, org, company, creator, "media/referenced-"+suffix)
	mustScanID(t, pool, `INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name,status) VALUES($1,$2,'YOUTUBE',$3,'media','media','ACTIVE') RETURNING id`, &account, org, company, "media-account-"+suffix)
	if _, err = pool.Exec(t.Context(), `INSERT INTO creator_account_assignments(creator_id,platform_account_id) VALUES($1,$2)`, creator, account); err != nil {
		t.Fatal(err)
	}
	mustScanID(t, pool, `INSERT INTO content_items(organization_id,company_id,creator_id,created_by) VALUES($1,$2,$3,$4) RETURNING id`, &item, org, company, creator, owner)
	mustScanID(t, pool, `INSERT INTO content_revisions(content_item_id,organization_id,revision,description,status,created_by) VALUES($1,$2,1,'media','DRAFT',$3) RETURNING id`, &revision, item, org, owner)
	if _, err = pool.Exec(t.Context(), `INSERT INTO content_publish_targets(content_revision_id,organization_id,company_id,creator_id,platform_account_id,platform,media_asset_id,platform_options,status) VALUES($1,$2,$3,$4,$5,'YOUTUBE',$6,'{}','READY')`, revision, org, company, creator, account, referencedAsset); err != nil {
		t.Fatal(err)
	}

	if processed, cleanupErr := s.RunMediaCleanup(t.Context(), "cleanup-"+suffix, 3); cleanupErr != nil || processed != 2 {
		t.Fatalf("cleanup processed=%d err=%v", processed, cleanupErr)
	}
	var expiredStatus, orphanStatus, referencedStatus string
	if err = pool.QueryRow(t.Context(), `SELECT status FROM media_upload_sessions WHERE id=$1`, expiredSession).Scan(&expiredStatus); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), `SELECT status FROM media_assets WHERE id=$1`, orphanAsset).Scan(&orphanStatus); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), `SELECT status FROM media_assets WHERE id=$1`, referencedAsset).Scan(&referencedStatus); err != nil {
		t.Fatal(err)
	}
	if expiredStatus != "EXPIRED" || orphanStatus != "DELETED" || referencedStatus != "READY" || atomic.LoadInt32(&aborts) != 1 || atomic.LoadInt32(&deletes) != 1 {
		t.Fatalf("expired=%s orphan=%s referenced=%s aborts=%d deletes=%d", expiredStatus, orphanStatus, referencedStatus, aborts, deletes)
	}
	var cleanupAudits int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_logs WHERE organization_id=$1 AND action IN ('SYSTEM_MEDIA_UPLOAD_EXPIRED','SYSTEM_MEDIA_DELETE_PENDING','SYSTEM_MEDIA_DELETED')`, org).Scan(&cleanupAudits); err != nil || cleanupAudits != 3 {
		t.Fatalf("cleanup audits=%d err=%v", cleanupAudits, err)
	}

	var failingAsset string
	mustScanID(t, pool, `INSERT INTO media_assets(organization_id,company_id,creator_id,status,object_key,original_filename,delete_after) VALUES($1,$2,$3,'DELETE_PENDING',$4,'failed.mp4',now()-interval '1 hour') RETURNING id`, &failingAsset, org, company, creator, "temporary/failing-"+suffix)
	atomic.StoreInt32(&failDelete, 1)
	if processed, cleanupErr := s.RunMediaCleanup(t.Context(), "cleanup-fail-"+suffix, 1); cleanupErr != nil || processed != 1 {
		t.Fatalf("failed cleanup processed=%d err=%v", processed, cleanupErr)
	}
	var failingStatus string
	if err = pool.QueryRow(t.Context(), `SELECT status FROM media_assets WHERE id=$1`, failingAsset).Scan(&failingStatus); err != nil || failingStatus != "DELETE_PENDING" {
		t.Fatalf("failed status=%s err=%v", failingStatus, err)
	}
	atomic.StoreInt32(&failDelete, 0)
	if processed, cleanupErr := s.RunMediaCleanup(t.Context(), "cleanup-retry-"+suffix, 1); cleanupErr != nil || processed != 1 {
		t.Fatalf("retry cleanup processed=%d err=%v", processed, cleanupErr)
	}
	if err = pool.QueryRow(t.Context(), `SELECT status FROM media_assets WHERE id=$1`, failingAsset).Scan(&failingStatus); err != nil || failingStatus != "DELETED" {
		t.Fatalf("retry status=%s err=%v", failingStatus, err)
	}

	// A process may crash after S3 applied an abort but before the database
	// commit. Reclaiming the stale durable task must treat NoSuchUpload as the
	// terminal desired state rather than poisoning the cleanup queue.
	var crashedAbortTask string
	mustScanID(t, pool, `INSERT INTO media_cleanup_tasks(task_key,organization_id,former_company_id,former_creator_id,former_media_asset_id,kind,object_key,multipart_upload_id,status,lease_worker_id,lease_until,generation,attempts,created_at) VALUES($1,$2,$3,$4,$5,'ABORT_MULTIPART',$6,$7,'RUNNING','crashed-worker',now()-interval '1 minute',1,1,now()-interval '2 minutes') RETURNING id`, &crashedAbortTask, "crashed-abort-"+suffix, org, company, creator, expiredAsset, "temporary/crashed-"+suffix, "already-aborted-"+suffix)
	atomic.StoreInt32(&abortMissing, 1)
	if processed, cleanupErr := s.RunMediaCleanup(t.Context(), "cleanup-crash-retry-"+suffix, 1); cleanupErr != nil || processed != 1 {
		t.Fatalf("crash retry processed=%d err=%v", processed, cleanupErr)
	}
	var taskStatus string
	if err = pool.QueryRow(t.Context(), `SELECT status FROM media_cleanup_tasks WHERE id=$1`, crashedAbortTask).Scan(&taskStatus); err != nil || taskStatus != "COMPLETE" {
		t.Fatalf("crashed abort task status=%s err=%v", taskStatus, err)
	}
	atomic.StoreInt32(&abortMissing, 0)

	// Remote failure retains the outbox row and a later pass completes it. No
	// company/media FK is needed: these identifiers are diagnostic tombstones.
	var retryDeleteTask string
	mustScanID(t, pool, `INSERT INTO media_cleanup_tasks(task_key,organization_id,former_company_id,former_creator_id,former_media_asset_id,kind,object_key) VALUES($1,$2,$3,$4,$5,'DELETE_OBJECT',$6) RETURNING id`, &retryDeleteTask, "retry-delete-"+suffix, org, company, creator, orphanAsset, "immutable/retry-"+suffix)
	atomic.StoreInt32(&failDelete, 1)
	if processed, cleanupErr := s.RunMediaCleanup(t.Context(), "cleanup-outbox-fail-"+suffix, 1); cleanupErr != nil || processed != 1 {
		t.Fatalf("outbox failure processed=%d err=%v", processed, cleanupErr)
	}
	var attempts int
	if err = pool.QueryRow(t.Context(), `SELECT status,attempts FROM media_cleanup_tasks WHERE id=$1`, retryDeleteTask).Scan(&taskStatus, &attempts); err != nil || taskStatus != "PENDING" || attempts != 1 {
		t.Fatalf("outbox retained status=%s attempts=%d err=%v", taskStatus, attempts, err)
	}
	atomic.StoreInt32(&failDelete, 0)
	if processed, cleanupErr := s.RunMediaCleanup(t.Context(), "cleanup-outbox-retry-"+suffix, 1); cleanupErr != nil || processed != 1 {
		t.Fatalf("outbox retry processed=%d err=%v", processed, cleanupErr)
	}
	if err = pool.QueryRow(t.Context(), `SELECT status,attempts FROM media_cleanup_tasks WHERE id=$1`, retryDeleteTask).Scan(&taskStatus, &attempts); err != nil || taskStatus != "COMPLETE" || attempts != 2 {
		t.Fatalf("outbox completed status=%s attempts=%d err=%v", taskStatus, attempts, err)
	}
}
