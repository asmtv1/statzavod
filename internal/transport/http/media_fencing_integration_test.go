package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/statzavod/statzavod/internal/config"
)

func TestMediaWorkerGenerationFencingPostgresIntegration(t *testing.T) {
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
	mustScanID(t, pool, `INSERT INTO organizations(name,slug) VALUES('media-fencing',$1) RETURNING id`, &org, "media-fencing-"+suffix)
	mustScanID(t, pool, `INSERT INTO companies(organization_id,name) VALUES($1,'media-fencing') RETURNING id`, &company, org)
	mustScanID(t, pool, `INSERT INTO users(email,password_hash,role,status) VALUES($1,'x','ADMIN','ACTIVE') RETURNING id`, &owner, "fencing-owner-"+suffix+"@test")
	mustScanID(t, pool, `INSERT INTO users(email,password_hash,role,status) VALUES($1,'x','VIEWER','ACTIVE') RETURNING id`, &creatorUser, "fencing-creator-"+suffix+"@test")
	if _, err = pool.Exec(t.Context(), `INSERT INTO organization_memberships(organization_id,user_id,role,membership_role) VALUES ($1,$2,'ADMIN','OWNER'),($1,$3,'VIEWER','CREATOR')`, org, owner, creatorUser); err != nil {
		t.Fatal(err)
	}
	mustScanID(t, pool, `INSERT INTO creators(organization_id,company_id,created_by,login_user_id,first_name,last_name,display_name) VALUES($1,$2,$3,$4,'Fence','Creator','Fence Creator') RETURNING id`, &creator, org, company, owner, creatorUser)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_cleanup_tasks WHERE organization_id=$1`, org)
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, org)
	})

	var copies, deletes, aborts int32
	fakeS3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.Header.Get("X-Amz-Copy-Source") != "":
			atomic.AddInt32(&copies, 1)
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<CopyObjectResult><ETag>copy</ETag></CopyObjectResult>`))
		case r.Method == http.MethodDelete && r.URL.Query().Get("uploadId") != "":
			atomic.AddInt32(&aborts, 1)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete:
			atomic.AddInt32(&deletes, 1)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer fakeS3.Close()
	s := &Server{pool: pool, now: time.Now, config: config.Config{ContentPublishingEnabled: true, MediaS3Endpoint: fakeS3.URL, MediaS3Region: "us-east-1", MediaS3Bucket: "test", MediaS3AccessKey: "key", MediaS3SecretKey: "secret"}}

	var validationAsset string
	mustScanID(t, pool, `INSERT INTO media_assets(organization_id,company_id,creator_id,status,object_key,original_filename,detected_mime,bytes,created_at) VALUES($1,$2,$3,'VALIDATING',$4,'validation.mp4','video/mp4',10,'2000-01-01') RETURNING id`, &validationAsset, org, company, creator, "temporary/validation-"+suffix)
	oldValidation, ok, claimErr := s.claimMediaValidation(t.Context(), "validation-old-"+suffix)
	if claimErr != nil || !ok || oldValidation.ID != validationAsset {
		t.Fatalf("old validation claim=%#v ok=%v err=%v", oldValidation, ok, claimErr)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE media_assets SET validation_lease_until=now()-interval '1 second' WHERE id=$1`, validationAsset); err != nil {
		t.Fatal(err)
	}
	newValidation, ok, claimErr := s.claimMediaValidation(t.Context(), "validation-new-"+suffix)
	if claimErr != nil || !ok || newValidation.Generation != oldValidation.Generation+1 {
		t.Fatalf("new validation claim=%#v ok=%v err=%v", newValidation, ok, claimErr)
	}
	probe := mediaProbe{SHA: []byte{0x91, 0x92}, MIME: "video/mp4", Bytes: 10, Width: 9, Height: 16, DurationMS: 1000, Metadata: map[string]any{}, HashKey: "media/fenced-" + suffix + ".mp4"}
	if staleErr := s.finishMediaValidation(t.Context(), oldValidation, true, probe, nil); !errors.Is(staleErr, errMediaLeaseLost) || atomic.LoadInt32(&copies) != 0 {
		t.Fatalf("stale validation err=%v copies=%d", staleErr, copies)
	}
	if err = s.finishMediaValidation(t.Context(), newValidation, true, probe, nil); err != nil || atomic.LoadInt32(&copies) != 1 {
		t.Fatalf("current validation err=%v copies=%d", err, copies)
	}
	var readyAudits int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_logs WHERE organization_id=$1 AND entity_id=$2 AND action='SYSTEM_MEDIA_READY'`, org, validationAsset).Scan(&readyAudits); err != nil || readyAudits != 1 {
		t.Fatalf("ready audits=%d err=%v", readyAudits, err)
	}
	// The historical timestamp above only makes this fixture's validation claim
	// deterministic in a shared integration database. Once READY, restore a
	// current timestamp so the independent seven-day orphan collector cannot
	// select it during the lease-fencing assertions below.
	if _, err = pool.Exec(t.Context(), `UPDATE media_assets SET created_at=now() WHERE id=$1`, validationAsset); err != nil {
		t.Fatal(err)
	}

	oldTemp, ok, claimErr := s.claimMediaCleanup(t.Context(), "temp-old-"+suffix)
	if claimErr != nil || !ok || oldTemp.Kind != "TEMP_OBJECT" {
		t.Fatalf("old temp claim=%#v ok=%v err=%v", oldTemp, ok, claimErr)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE media_assets SET cleanup_lease_until=now()-interval '1 second' WHERE id=$1`, validationAsset); err != nil {
		t.Fatal(err)
	}
	newTemp, ok, claimErr := s.claimMediaCleanup(t.Context(), "temp-new-"+suffix)
	if claimErr != nil || !ok || newTemp.Generation != oldTemp.Generation+1 {
		t.Fatalf("new temp claim=%#v ok=%v err=%v", newTemp, ok, claimErr)
	}
	if staleErr := s.executeMediaCleanup(t.Context(), oldTemp); !errors.Is(staleErr, errMediaLeaseLost) || atomic.LoadInt32(&deletes) != 0 {
		t.Fatalf("stale temp cleanup err=%v deletes=%d", staleErr, deletes)
	}
	if err = s.executeMediaCleanup(t.Context(), newTemp); err != nil || atomic.LoadInt32(&deletes) != 1 {
		t.Fatalf("current temp cleanup err=%v deletes=%d", err, deletes)
	}

	var heartbeatAsset string
	mustScanID(t, pool, `INSERT INTO media_assets(organization_id,company_id,creator_id,status,object_key,original_filename,detected_mime,bytes,created_at) VALUES($1,$2,$3,'VALIDATING',$4,'heartbeat.mp4','video/mp4',10,'2000-01-02') RETURNING id`, &heartbeatAsset, org, company, creator, "temporary/heartbeat-"+suffix)
	live, ok, claimErr := s.claimMediaValidation(t.Context(), "validation-live-"+suffix)
	if claimErr != nil || !ok || live.ID != heartbeatAsset {
		t.Fatalf("heartbeat claim=%#v ok=%v err=%v", live, ok, claimErr)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE media_assets SET validation_lease_until=now()+interval '1 second' WHERE id=$1`, heartbeatAsset); err != nil {
		t.Fatal(err)
	}
	if err = s.heartbeatMediaValidation(t.Context(), live); err != nil {
		t.Fatal(err)
	}
	if contender, contenderOK, contenderErr := s.claimMediaValidation(t.Context(), "validation-contender-"+suffix); contenderErr != nil || (contenderOK && contender.ID == heartbeatAsset) {
		t.Fatalf("heartbeat allowed reclaim of live job=%#v ok=%v err=%v", contender, contenderOK, contenderErr)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE media_assets SET status='REJECTED',validation_worker_id=NULL,validation_lease_until=NULL,delete_after=now()+interval '7 days' WHERE id=$1`, heartbeatAsset); err != nil {
		t.Fatal(err)
	}

	var expiredAsset, expiredSession string
	mustScanID(t, pool, `INSERT INTO media_assets(organization_id,company_id,creator_id,status,object_key,original_filename,bytes) VALUES($1,$2,$3,'UPLOADING',$4,'expired.mp4',10) RETURNING id`, &expiredAsset, org, company, creator, "temporary/expired-"+suffix)
	mustScanID(t, pool, `INSERT INTO media_upload_sessions(media_asset_id,organization_id,actor_id,multipart_upload_id,expected_bytes,expected_mime,expires_at) VALUES($1,$2,$3,$4,10,'video/mp4',now()-interval '1 hour') RETURNING id`, &expiredSession, expiredAsset, org, creatorUser, "expired-"+suffix)
	oldExpired, ok, claimErr := s.claimMediaCleanup(t.Context(), "expired-old-"+suffix)
	if claimErr != nil || !ok || oldExpired.Kind != "MULTIPART" {
		t.Fatalf("old expired claim=%#v ok=%v err=%v", oldExpired, ok, claimErr)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE media_upload_sessions SET cleanup_lease_until=now()+interval '1 second' WHERE id=$1`, expiredSession); err != nil {
		t.Fatal(err)
	}
	if err = s.heartbeatMediaCleanup(t.Context(), oldExpired); err != nil {
		t.Fatal(err)
	}
	if contender, contenderOK, contenderErr := s.claimMediaCleanup(t.Context(), "cleanup-contender-"+suffix); contenderErr != nil || contenderOK {
		t.Fatalf("cleanup heartbeat allowed reclaim job=%#v ok=%v err=%v", contender, contenderOK, contenderErr)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE media_upload_sessions SET cleanup_lease_until=now()-interval '1 second' WHERE id=$1`, expiredSession); err != nil {
		t.Fatal(err)
	}
	newExpired, ok, claimErr := s.claimMediaCleanup(t.Context(), "expired-new-"+suffix)
	if claimErr != nil || !ok || newExpired.Generation != oldExpired.Generation+1 {
		t.Fatalf("new expired claim=%#v ok=%v err=%v", newExpired, ok, claimErr)
	}
	if staleErr := s.executeMediaCleanup(t.Context(), oldExpired); !errors.Is(staleErr, errMediaLeaseLost) || atomic.LoadInt32(&aborts) != 0 {
		t.Fatalf("stale expired cleanup err=%v aborts=%d", staleErr, aborts)
	}
	if err = s.executeMediaCleanup(t.Context(), newExpired); err != nil || atomic.LoadInt32(&aborts) != 1 {
		t.Fatalf("current expired cleanup err=%v aborts=%d", err, aborts)
	}
	var expiredAudits int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_logs WHERE organization_id=$1 AND entity_id=$2 AND action='SYSTEM_MEDIA_UPLOAD_EXPIRED'`, org, expiredAsset).Scan(&expiredAudits); err != nil || expiredAudits != 1 {
		t.Fatalf("expired audits=%d err=%v", expiredAudits, err)
	}

	// Equal bytes uploaded by different creators share only the immutable S3
	// object. Authorization identity remains represented by two scoped READY
	// media rows, and concurrent validation cannot strand either in VALIDATING.
	var otherCreator, duplicateA, duplicateB string
	mustScanID(t, pool, `INSERT INTO creators(organization_id,company_id,created_by,first_name,last_name,display_name) VALUES($1,$2,$3,'Other','Creator','Other Creator') RETURNING id`, &otherCreator, org, company, owner)
	mustScanID(t, pool, `INSERT INTO media_assets(organization_id,company_id,creator_id,status,object_key,original_filename,detected_mime,bytes,created_at) VALUES($1,$2,$3,'VALIDATING',$4,'duplicate-a.mp4','video/mp4',10,'2000-01-03') RETURNING id`, &duplicateA, org, company, creator, "temporary/duplicate-a-"+suffix)
	mustScanID(t, pool, `INSERT INTO media_assets(organization_id,company_id,creator_id,status,object_key,original_filename,detected_mime,bytes,created_at) VALUES($1,$2,$3,'VALIDATING',$4,'duplicate-b.mp4','video/mp4',10,'2000-01-03') RETURNING id`, &duplicateB, org, company, otherCreator, "temporary/duplicate-b-"+suffix)
	firstDuplicate, ok, claimErr := s.claimMediaValidation(t.Context(), "duplicate-first-"+suffix)
	if claimErr != nil || !ok {
		t.Fatalf("first duplicate claim=%#v ok=%v err=%v", firstDuplicate, ok, claimErr)
	}
	secondDuplicate, ok, claimErr := s.claimMediaValidation(t.Context(), "duplicate-second-"+suffix)
	if claimErr != nil || !ok || firstDuplicate.ID == secondDuplicate.ID {
		t.Fatalf("second duplicate claim=%#v ok=%v err=%v", secondDuplicate, ok, claimErr)
	}
	duplicateProbe := mediaProbe{SHA: []byte{0xde, 0xad, 0xbe, 0xef}, MIME: "video/mp4", Bytes: 10, Width: 9, Height: 16, DurationMS: 1000, Metadata: map[string]any{}, HashKey: "media/duplicate-" + suffix + ".mp4"}
	copiesBefore := atomic.LoadInt32(&copies)
	finishErrors := make(chan error, 2)
	startDuplicates := make(chan struct{})
	for _, claimed := range []mediaValidationJob{firstDuplicate, secondDuplicate} {
		claimed := claimed
		go func() {
			<-startDuplicates
			probeForCreator := duplicateProbe
			probeForCreator.Metadata = map[string]any{}
			finishErrors <- s.finishMediaValidation(t.Context(), claimed, true, probeForCreator, nil)
		}()
	}
	close(startDuplicates)
	for range 2 {
		if finishErr := <-finishErrors; finishErr != nil {
			t.Fatalf("finish duplicate validation: %v", finishErr)
		}
	}
	if got := atomic.LoadInt32(&copies) - copiesBefore; got != 1 {
		t.Fatalf("duplicate immutable copies=%d, want 1", got)
	}
	var duplicateReady, duplicateObjects, duplicateCreators int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE status='READY'),count(DISTINCT object_key),count(DISTINCT creator_id) FROM media_assets WHERE id=ANY($1::uuid[])`, []string{duplicateA, duplicateB}).Scan(&duplicateReady, &duplicateObjects, &duplicateCreators); err != nil {
		t.Fatal(err)
	}
	if duplicateReady != 2 || duplicateObjects != 1 || duplicateCreators != 2 {
		t.Fatalf("duplicate media ready=%d objects=%d creators=%d", duplicateReady, duplicateObjects, duplicateCreators)
	}
}
