package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/statzavod/statzavod/internal/config"
)

// This deliberately exercises the handlers against PostgreSQL. It is gated by
// TEST_DATABASE_URL because the transition/idempotency guarantees depend on
// row locks, partial indexes and jsonb equality, none of which mocks cover.
func TestContentCommandsPostgresIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to a disposable database migrated through 00026")
	}
	pool, err := pgxpool.New(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var mediaTable bool
	if err = pool.QueryRow(t.Context(), `SELECT to_regclass('public.media_upload_sessions') IS NOT NULL`).Scan(&mediaTable); err != nil || !mediaTable {
		t.Fatal("TEST_DATABASE_URL must be migrated through 00026")
	}
	suffix := time.Now().UTC().Format("20060102150405.000000000")
	var org, company, owner, manager, creatorUser, creator, alternateCreator, account, alternateAccount, media, alternateMedia, item, revision, target string
	mustScanID(t, pool, `INSERT INTO organizations(name,slug) VALUES('commands',$1) RETURNING id`, &org, "commands-"+suffix)
	mustScanID(t, pool, `INSERT INTO companies(organization_id,name) VALUES($1,'commands') RETURNING id`, &company, org)
	for _, row := range []struct {
		email string
		id    *string
	}{{"owner-" + suffix + "@test", &owner}, {"manager-" + suffix + "@test", &manager}, {"creator-" + suffix + "@test", &creatorUser}} {
		mustScanID(t, pool, `INSERT INTO users(email,password_hash,role,status) VALUES($1,'x','ADMIN','ACTIVE') RETURNING id`, row.id, row.email)
	}
	if _, err = pool.Exec(t.Context(), `INSERT INTO organization_memberships(organization_id,user_id,role,membership_role) VALUES ($1,$2,'ADMIN','OWNER'),($1,$3,'VIEWER','MANAGER'),($1,$4,'VIEWER','CREATOR')`, org, owner, manager, creatorUser); err != nil {
		t.Fatal(err)
	}
	mustScanID(t, pool, `INSERT INTO creators(organization_id,company_id,created_by,login_user_id,first_name,last_name,display_name) VALUES($1,$2,$3,$4,'C','Creator','Creator') RETURNING id`, &creator, org, company, owner, creatorUser)
	mustScanID(t, pool, `INSERT INTO creators(organization_id,company_id,created_by,first_name,last_name,display_name) VALUES($1,$2,$3,'Other','Creator','Other Creator') RETURNING id`, &alternateCreator, org, company, owner)
	mustScanID(t, pool, `INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name,status) VALUES($1,$2,'YOUTUBE',$3,'account','account','ACTIVE') RETURNING id`, &account, org, company, "account-"+suffix)
	if _, err = pool.Exec(t.Context(), `INSERT INTO creator_account_assignments(creator_id,platform_account_id) VALUES($1,$2)`, creator, account); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `INSERT INTO oauth_connections(organization_id,platform_account_id,access_token_ciphertext,nonce,access_token_nonce,status,scopes) VALUES($1,$2,decode('01','hex'),decode('02','hex'),decode('02','hex'),'ACTIVE',ARRAY['https://www.googleapis.com/auth/youtube.upload'])`, org, account); err != nil {
		t.Fatal(err)
	}
	mustScanID(t, pool, `INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name,status) VALUES($1,$2,'VK',$3,'other-account','other-account','ACTIVE') RETURNING id`, &alternateAccount, org, company, "other-account-"+suffix)
	if _, err = pool.Exec(t.Context(), `INSERT INTO creator_account_assignments(creator_id,platform_account_id) VALUES($1,$2)`, alternateCreator, alternateAccount); err != nil {
		t.Fatal(err)
	}
	mustScanID(t, pool, `INSERT INTO media_assets(organization_id,company_id,creator_id,status,object_key,content_sha256,original_filename,width,height) VALUES($1,$2,$3,'READY',$4,decode('aa','hex'),'x.mp4',9,16) RETURNING id`, &media, org, company, creator, "commands/"+suffix)
	mustScanID(t, pool, `INSERT INTO media_assets(organization_id,company_id,creator_id,status,object_key,content_sha256,original_filename,width,height) VALUES($1,$2,$3,'READY',$4,decode('bb','hex'),'other.mp4',9,16) RETURNING id`, &alternateMedia, org, company, alternateCreator, "commands/"+suffix+"-other")
	mustScanID(t, pool, `INSERT INTO content_items(organization_id,company_id,creator_id,created_by) VALUES($1,$2,$3,$4) RETURNING id`, &item, org, company, creator, owner)
	mustScanID(t, pool, `INSERT INTO content_revisions(content_item_id,organization_id,revision,description,status,created_by) VALUES($1,$2,1,'caption','DRAFT',$3) RETURNING id`, &revision, item, org, owner)
	mustScanID(t, pool, `INSERT INTO content_publish_targets(content_revision_id,organization_id,company_id,creator_id,platform_account_id,platform,media_asset_id,platform_options,status) VALUES($1,$2,$3,$4,$5,'YOUTUBE',$6,'{}','READY') RETURNING id`, &target, revision, org, company, creator, account, media)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, org) })
	if _, err = pool.Exec(t.Context(), `INSERT INTO creator_content_approval_policies(creator_id,organization_id,company_id,publish_requires_approval,updated_by) VALUES($1,$2,$3,true,$4)`, creator, org, company, owner); err != nil {
		t.Fatal(err)
	}
	s := &Server{pool: pool, now: time.Now}
	s.config.ContentPublishingEnabled = true
	s.config.ContentYouTubeEnabled = true
	creatorP := principal{ID: creatorUser, OrganizationID: org, Role: roleCreator, ActiveCompanyID: &company, ActiveCreatorID: &creator, Companies: map[string]companyAccess{company: testCompany(company)}, CreatorProfiles: map[string]creatorProfileAccess{creator: {ID: creator, CompanyID: company}}}
	managerP := principal{ID: manager, OrganizationID: org, Role: roleManager, ActiveCompanyID: &company, Companies: map[string]companyAccess{company: testCompany(company, "CONTENT_PUBLISH", "CONTENT_EDIT", "CONTENT_CREATE", "CONTENT_VIEW")}}
	assertStable := func(name string, primary, replay *httptest.ResponseRecorder) {
		t.Helper()
		if primary.Code != replay.Code || primary.Body.String() != replay.Body.String() || primary.Header().Get("Content-Type") != replay.Header().Get("Content-Type") {
			t.Fatalf("%s replay differs: primary=%d %q %q replay=%d %q %q", name, primary.Code, primary.Header().Get("Content-Type"), primary.Body.String(), replay.Code, replay.Header().Get("Content-Type"), replay.Body.String())
		}
	}
	// The create key is claimed before any content row or audit is inserted.
	// Two simultaneous requests therefore return one byte-identical 201 while
	// producing one item, one revision and one audit record.
	createBody := `{"creatorId":"` + creator + `","description":"concurrent create","targets":[{"platformAccountId":"` + account + `","platform":"YOUTUBE","mediaAssetId":"` + media + `","options":{}}]}`
	createCall := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/content-items", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "create-concurrent-"+suffix)
		r = r.WithContext(context.WithValue(r.Context(), principalKey, managerP))
		w := httptest.NewRecorder()
		s.createContentItem(w, r)
		return w
	}
	start := make(chan struct{})
	created := make(chan *httptest.ResponseRecorder, 2)
	var createWG sync.WaitGroup
	for range 2 {
		createWG.Add(1)
		go func() {
			defer createWG.Done()
			<-start
			created <- createCall(createBody)
		}()
	}
	close(start)
	createWG.Wait()
	close(created)
	createResponses := make([]*httptest.ResponseRecorder, 0, 2)
	for response := range created {
		createResponses = append(createResponses, response)
	}
	if createResponses[0].Code != http.StatusCreated {
		t.Fatalf("concurrent create=%d: %s", createResponses[0].Code, createResponses[0].Body.String())
	}
	assertStable("concurrent create", createResponses[0], createResponses[1])
	var createdResponse struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(createResponses[0].Body.Bytes(), &createdResponse); err != nil || createdResponse.ID == "" {
		t.Fatalf("decode concurrent create: id=%q err=%v", createdResponse.ID, err)
	}
	var createdItems, createdRevisions, createdAudits int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM content_items WHERE id=$1 AND organization_id=$2`, createdResponse.ID, org).Scan(&createdItems); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM content_revisions WHERE content_item_id=$1 AND organization_id=$2`, createdResponse.ID, org).Scan(&createdRevisions); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_logs WHERE organization_id=$1 AND entity_id=$2 AND action='CREATE_CONTENT_ITEM'`, org, createdResponse.ID).Scan(&createdAudits); err != nil {
		t.Fatal(err)
	}
	if createdItems != 1 || createdRevisions != 1 || createdAudits != 1 {
		t.Fatalf("concurrent create side effects items=%d revisions=%d audits=%d", createdItems, createdRevisions, createdAudits)
	}
	// Exercise the same replay behind the response type used by the mandatory
	// audit middleware. A replay is backed by the primary transaction's audit
	// and must not be failed closed merely because it does not write a duplicate
	// audit row.
	auditedReplayRequest := httptest.NewRequest(http.MethodPost, "/content-items", strings.NewReader(createBody))
	auditedReplayRequest.Header.Set("Content-Type", "application/json")
	auditedReplayRequest.Header.Set("Idempotency-Key", "create-concurrent-"+suffix)
	auditedReplayRequest = auditedReplayRequest.WithContext(context.WithValue(auditedReplayRequest.Context(), principalKey, managerP))
	auditedReplay := newBufferedAuditResponse()
	s.createContentItem(auditedReplay, auditedReplayRequest)
	if !auditedReplay.auditCommitted || auditedReplay.status != http.StatusCreated || auditedReplay.body.String() != createResponses[0].Body.String() || auditedReplay.header.Get("Content-Type") != createResponses[0].Header().Get("Content-Type") {
		t.Fatalf("audited create replay committed=%v status=%d content-type=%q body=%q", auditedReplay.auditCommitted, auditedReplay.status, auditedReplay.header.Get("Content-Type"), auditedReplay.body.String())
	}
	if conflictCreate := createCall(strings.Replace(createBody, "concurrent create", "different create", 1)); conflictCreate.Code != http.StatusConflict {
		t.Fatalf("concurrent create conflict=%d: %s", conflictCreate.Code, conflictCreate.Body.String())
	}
	call := func(p principal, action, body, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/content-items/"+item+"/"+action, strings.NewReader(body))
		r.Header.Set("Idempotency-Key", key)
		r.Header.Set("Content-Type", "application/json")
		route := chi.NewRouteContext()
		route.URLParams.Add("id", item)
		ctx := context.WithValue(r.Context(), chi.RouteCtxKey, route)
		r = r.WithContext(context.WithValue(ctx, principalKey, p))
		w := httptest.NewRecorder()
		s.transitionContent(w, r, action)
		return w
	}
	// A creator cannot use publish/schedule/retry as an approval-policy bypass.
	for _, tc := range []struct{ action, body string }{{"publish", "{}"}, {"schedule", `{"scheduledAt":"2030-01-01T12:00:00Z"}`}, {"retry", `{"targetIds":["` + target + `"]}`}} {
		if w := call(creatorP, tc.action, tc.body, "creator-"+tc.action+suffix); w.Code != http.StatusConflict {
			t.Fatalf("creator %s=%d: %s", tc.action, w.Code, w.Body.String())
		}
	}
	// A granted manager uses their own CONTENT_PUBLISH capability and is not
	// constrained by the creator-only policy.
	managerPublish := call(managerP, "publish", "{}", "manager-publish-"+suffix)
	if managerPublish.Code != http.StatusOK {
		t.Fatalf("manager publish=%d: %s", managerPublish.Code, managerPublish.Body.String())
	}
	// Same key replay is byte-for-byte stable; a different canonical body gets 409.
	replay := call(managerP, "publish", "{}", "manager-publish-"+suffix)
	if replay.Code != http.StatusOK {
		t.Fatalf("publish replay=%d %s", replay.Code, replay.Body.String())
	}
	assertStable("publish", managerPublish, replay)
	conflict := call(managerP, "publish", `{"ignored":true}`, "manager-publish-"+suffix)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("publish conflict=%d: %s", conflict.Code, conflict.Body.String())
	}
	// Editing while an old target/job is running keeps the worker's target lock
	// protocol intact by marking cancellation_requested_at on that old target.
	if _, err = pool.Exec(t.Context(), `UPDATE content_publish_targets SET status='PUBLISHING' WHERE id=$1`, target); err != nil {
		t.Fatal(err)
	}
	patchBody := `{"description":"edited","targets":[{"platformAccountId":"` + account + `","platform":"YOUTUBE","mediaAssetId":"` + media + `","options":{}}]}`
	patchReq := httptest.NewRequest(http.MethodPatch, "/content-items/"+item, strings.NewReader(patchBody))
	patchReq.Header.Set("If-Match", contentETag(1))
	patchRoute := chi.NewRouteContext()
	patchRoute.URLParams.Add("id", item)
	patchReq = patchReq.WithContext(context.WithValue(context.WithValue(patchReq.Context(), chi.RouteCtxKey, patchRoute), principalKey, managerP))
	patchResp := httptest.NewRecorder()
	s.patchContentItem(patchResp, patchReq)
	if patchResp.Code != http.StatusOK {
		t.Fatalf("patch=%d: %s", patchResp.Code, patchResp.Body.String())
	}
	// Same-company resources owned by another creator are still invisible to
	// this item. Both the media and account paths must fail before a revision
	// can be written, and the response must not disclose their UUIDs.
	for _, body := range []string{
		`{"targets":[{"platformAccountId":"` + account + `","platform":"YOUTUBE","mediaAssetId":"` + alternateMedia + `","options":{}}]}`,
		`{"targets":[{"platformAccountId":"` + alternateAccount + `","platform":"VK","mediaAssetId":"` + media + `","options":{}}]}`,
	} {
		foreignReq := httptest.NewRequest(http.MethodPatch, "/content-items/"+item, strings.NewReader(body))
		foreignReq.Header.Set("If-Match", contentETag(2))
		foreignRoute := chi.NewRouteContext()
		foreignRoute.URLParams.Add("id", item)
		foreignReq = foreignReq.WithContext(context.WithValue(context.WithValue(foreignReq.Context(), chi.RouteCtxKey, foreignRoute), principalKey, managerP))
		foreignResp := httptest.NewRecorder()
		s.patchContentItem(foreignResp, foreignReq)
		if foreignResp.Code != http.StatusBadRequest || strings.Contains(foreignResp.Body.String(), alternateMedia) || strings.Contains(foreignResp.Body.String(), alternateAccount) {
			t.Fatalf("foreign content resource status=%d body=%s", foreignResp.Code, foreignResp.Body.String())
		}
	}
	var cancelled bool
	if err = pool.QueryRow(t.Context(), `SELECT cancellation_requested_at IS NOT NULL FROM content_publish_targets WHERE id=$1`, target).Scan(&cancelled); err != nil || !cancelled {
		t.Fatalf("running target cancellation marker=%v err=%v", cancelled, err)
	}
	// A schedule command creates its queue entry once and then replays exactly.
	scheduleBody := `{"scheduledAt":"2030-01-01T12:00:00Z"}`
	schedule1 := call(managerP, "schedule", scheduleBody, "schedule-"+suffix)
	schedule2 := call(managerP, "schedule", scheduleBody, "schedule-"+suffix)
	if schedule1.Code != http.StatusOK || schedule2.Code != http.StatusOK {
		t.Fatalf("schedule replay=%d/%d: %s", schedule1.Code, schedule2.Code, schedule2.Body.String())
	}
	assertStable("schedule", schedule1, schedule2)
	// Copy is also a stateful command and must not create two copies on replay.
	copyReq := func(body, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/content-items/"+item+"/copy", strings.NewReader(body))
		r.Header.Set("Idempotency-Key", key)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", item)
		ctx := context.WithValue(r.Context(), chi.RouteCtxKey, route)
		r = r.WithContext(context.WithValue(ctx, principalKey, managerP))
		w := httptest.NewRecorder()
		s.copyContentItem(w, r)
		return w
	}
	copy1 := copyReq("{}", "copy-"+suffix)
	copy2 := copyReq("{}", "copy-"+suffix)
	if copy1.Code != http.StatusCreated || copy2.Code != http.StatusCreated || copy1.Body.String() != copy2.Body.String() {
		t.Fatalf("copy replay=%d/%d", copy1.Code, copy2.Code)
	}
	assertStable("copy", copy1, copy2)

	// Retry has a selected-target payload and is covered independently from the
	// empty publish body and scheduled timestamp payload.
	var retryTarget, retryRevision string
	if err = pool.QueryRow(t.Context(), `SELECT target.id::text,target.content_revision_id::text FROM content_publish_targets target JOIN content_revisions revision ON revision.id=target.content_revision_id WHERE revision.content_item_id=$1 AND revision.revision=2`, item).Scan(&retryTarget, &retryRevision); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE content_publish_targets SET status='FAILED' WHERE id=$1`, retryTarget); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE content_revisions SET status='FAILED' WHERE id=$1`, retryRevision); err != nil {
		t.Fatal(err)
	}
	retryBody := `{"targetIds":["` + retryTarget + `"]}`
	retry1 := call(managerP, "retry", retryBody, "retry-stable-"+suffix)
	retry2 := call(managerP, "retry", retryBody, "retry-stable-"+suffix)
	if retry1.Code != http.StatusOK || retry2.Code != http.StatusOK {
		t.Fatalf("retry replay=%d/%d: %s", retry1.Code, retry2.Code, retry2.Body.String())
	}
	assertStable("retry", retry1, retry2)

	// Media endpoints use a minimal S3-compatible fake so this test verifies the
	// actual HTTP handlers, not just the idempotency table.
	var creates, completes int32
	fakeS3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Query().Get("uploadId") != "" {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<ListPartsResult><IsTruncated>false</IsTruncated><Part><PartNumber>1</PartNumber><ETag>etag</ETag><Size>1024</Size></Part></ListPartsResult>`))
			return
		}
		if r.Method == http.MethodPost && strings.Contains(r.URL.RawQuery, "uploads") {
			atomic.AddInt32(&creates, 1)
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<InitiateMultipartUploadResult><UploadId>fake-upload</UploadId></InitiateMultipartUploadResult>`))
			return
		}
		if r.Method == http.MethodPost && r.URL.Query().Get("uploadId") != "" {
			atomic.AddInt32(&completes, 1)
			_, _ = w.Write([]byte(`<CompleteMultipartUploadResult/>`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer fakeS3.Close()
	s.config = config.Config{ContentPublishingEnabled: true, MediaS3Endpoint: fakeS3.URL, MediaS3Region: "us-east-1", MediaS3Bucket: "test", MediaS3AccessKey: "key", MediaS3SecretKey: "secret"}
	mediaCall := func(path, body, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		ctx := r.Context()
		if strings.HasSuffix(path, "/complete") {
			route := chi.NewRouteContext()
			route.URLParams.Add("uploadID", strings.TrimSuffix(strings.TrimPrefix(path, "/creator-portal/media/uploads/"), "/complete"))
			ctx = context.WithValue(ctx, chi.RouteCtxKey, route)
		}
		r = r.WithContext(context.WithValue(ctx, principalKey, creatorP))
		w := httptest.NewRecorder()
		if strings.HasSuffix(path, "/complete") {
			s.completeMediaUpload(w, r)
		} else {
			s.createMediaUpload(w, r)
		}
		return w
	}
	initBody := `{"filename":"clip.mp4","mime":"video/mp4","bytes":1024}`
	init1 := mediaCall("/creator-portal/media/uploads", initBody, "media-init-"+suffix)
	init2 := mediaCall("/creator-portal/media/uploads", initBody, "media-init-"+suffix)
	if init1.Code != http.StatusCreated || init2.Code != http.StatusCreated || atomic.LoadInt32(&creates) != 1 {
		t.Fatalf("media init=%d/%d creates=%d", init1.Code, init2.Code, creates)
	}
	assertStable("media create", init1, init2)
	var init struct {
		UploadID string `json:"uploadId"`
	}
	if err = json.NewDecoder(init1.Body).Decode(&init); err != nil || init.UploadID == "" {
		t.Fatalf("media init decode=%v %#v", err, init)
	}
	completePath := "/creator-portal/media/uploads/" + init.UploadID + "/complete"
	completeBody := `{"parts":[{"partNumber":1,"etag":"etag"}]}`
	done1 := mediaCall(completePath, completeBody, "media-complete-"+suffix)
	done2 := mediaCall(completePath, completeBody, "media-complete-"+suffix)
	if done1.Code != http.StatusOK || done2.Code != http.StatusOK || atomic.LoadInt32(&completes) != 1 {
		t.Fatalf("media complete=%d/%d completes=%d %s", done1.Code, done2.Code, completes, done1.Body.String())
	}
	assertStable("media complete", done1, done2)

	// Abort replay is deliberately a 204 with no JSON content type or body.
	abortInit := mediaCall("/creator-portal/media/uploads", initBody, "media-abort-init-"+suffix)
	var abortUpload struct {
		UploadID string `json:"uploadId"`
	}
	if err = json.Unmarshal(abortInit.Body.Bytes(), &abortUpload); err != nil || abortUpload.UploadID == "" {
		t.Fatalf("media abort init=%d id=%q err=%v", abortInit.Code, abortUpload.UploadID, err)
	}
	abortCall := func() *httptest.ResponseRecorder {
		path := "/creator-portal/media/uploads/" + abortUpload.UploadID
		r := httptest.NewRequest(http.MethodDelete, path, nil)
		r.Header.Set("Idempotency-Key", "media-abort-"+suffix)
		route := chi.NewRouteContext()
		route.URLParams.Add("uploadID", abortUpload.UploadID)
		r = r.WithContext(context.WithValue(context.WithValue(r.Context(), chi.RouteCtxKey, route), principalKey, creatorP))
		w := httptest.NewRecorder()
		s.abortMediaUpload(w, r)
		return w
	}
	abort1, abort2 := abortCall(), abortCall()
	if abort1.Code != http.StatusNoContent || abort2.Code != http.StatusNoContent || abort1.Body.Len() != 0 || abort1.Header().Get("Content-Type") != "" {
		t.Fatalf("media abort replay=%d/%d primary=%q replay=%q", abort1.Code, abort2.Code, abort1.Body.String(), abort2.Body.String())
	}
	assertStable("media abort", abort1, abort2)
}
