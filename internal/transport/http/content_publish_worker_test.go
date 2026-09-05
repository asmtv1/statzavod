package httpserver

import (
	"context"
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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/statzavod/statzavod/internal/config"
)

type fakePublishAdapter struct {
	pollResult PublishResult
	pollErr    error
	publish    PublishResult
	publishErr error
	polls      int
	publishes  int
}

type blockingPublishAdapter struct {
	started chan struct{}
	release chan struct{}
}

type boundaryConfirmedAdapter struct {
	started    chan struct{}
	externalID string
}

func (a *boundaryConfirmedAdapter) Publish(ctx context.Context, _ PublishRequest) (PublishResult, error) {
	close(a.started)
	<-ctx.Done()
	return PublishResult{ExternalID: a.externalID, ExternalURL: "https://provider.example/" + a.externalID}, ctx.Err()
}
func (*boundaryConfirmedAdapter) Poll(context.Context, PublishRequest) (PublishResult, error) {
	return PublishResult{}, nil
}

type leasePublishAdapter struct {
	publishes atomic.Int32
	polls     atomic.Int32
}

func (f *leasePublishAdapter) Publish(context.Context, PublishRequest) (PublishResult, error) {
	f.publishes.Add(1)
	return PublishResult{ProviderOperationID: "unexpected-create", Pending: true}, nil
}
func (f *leasePublishAdapter) Poll(_ context.Context, r PublishRequest) (PublishResult, error) {
	f.polls.Add(1)
	return PublishResult{ProviderOperationID: r.ProviderOperationID, ExternalID: "7234567890123456789", ExternalURL: "javascript:alert('poison-session')?X-Amz-Signature=secret"}, nil
}

func (f *blockingPublishAdapter) Publish(ctx context.Context, _ PublishRequest) (PublishResult, error) {
	close(f.started)
	select {
	case <-f.release:
		return PublishResult{ExternalID: "outside-tx"}, nil
	case <-ctx.Done():
		return PublishResult{}, ctx.Err()
	}
}
func (f *blockingPublishAdapter) Poll(context.Context, PublishRequest) (PublishResult, error) {
	return PublishResult{}, nil
}

func (f *fakePublishAdapter) Publish(_ context.Context, _ PublishRequest) (PublishResult, error) {
	f.publishes++
	return f.publish, f.publishErr
}
func (f *fakePublishAdapter) Poll(_ context.Context, _ PublishRequest) (PublishResult, error) {
	f.polls++
	return f.pollResult, f.pollErr
}

func TestContentPublishBackoffContract(t *testing.T) {
	want := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 6 * time.Hour}
	if len(contentRetryBackoff) != len(want) {
		t.Fatalf("backoff entries = %d, want %d", len(contentRetryBackoff), len(want))
	}
	for i := range want {
		if contentRetryBackoff[i] != want[i] {
			t.Fatalf("backoff[%d] = %s, want %s", i, contentRetryBackoff[i], want[i])
		}
	}
}

func TestSanitizeSucceededPublicationUsesOnlyOfficialCanonicalLinks(t *testing.T) {
	tests := []struct{ platform, id, rawURL, wantURL string }{
		{"YOUTUBE", "AbCdEf_1234", "javascript:alert(1)?X-Amz-Signature=secret", "https://www.youtube.com/shorts/AbCdEf_1234"},
		{"INSTAGRAM", "1234567890", "https://www.instagram.com/reel/C0de_safe-1/", "https://www.instagram.com/reel/C0de_safe-1/"},
		{"VK", "-123_456", "https://evil.example/upload/session", "https://vk.ru/video-123_456"},
		{"TIKTOK", "7234567890123456789", "https://www.tiktok.com/@safe.creator/video/7234567890123456789", "https://www.tiktok.com/@safe.creator/video/7234567890123456789"},
	}
	for _, test := range tests {
		id, permalink := sanitizeSucceededPublication(test.platform, "SUCCEEDED", test.id, test.rawURL)
		if id != test.id || permalink != test.wantURL {
			t.Fatalf("%s sanitized=(%q,%q), want=(%q,%q)", test.platform, id, permalink, test.id, test.wantURL)
		}
	}
	if id, permalink := sanitizeSucceededPublication("YOUTUBE", "FAILED", "AbCdEf_1234", "https://www.youtube.com/shorts/AbCdEf_1234"); id != "" || permalink != "" {
		t.Fatalf("non-success identity leaked: (%q,%q)", id, permalink)
	}
	if id, permalink := sanitizeSucceededPublication("TIKTOK", "SUCCEEDED", "opaque-session", "https://www.tiktok.com/@safe.creator/video/7234567890123456789"); id != "" || permalink != "" {
		t.Fatalf("invalid provider identity leaked: (%q,%q)", id, permalink)
	}
}

func TestContentPublishPollsBeforeCreatingAgain(t *testing.T) {
	s := &Server{config: config.Config{ContentPublishingEnabled: true, ContentTikTokEnabled: true}, publishAdapters: map[string]PublishAdapter{}}
	fake := &fakePublishAdapter{pollResult: PublishResult{ExternalID: "already-created", ExternalURL: "https://provider.example/post"}}
	s.SetPublishAdapter("TIKTOK", fake)
	result, err := s.executeContentPublish(context.Background(), contentPublishJob{Platform: "TIKTOK", ProviderOperationID: "operation-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExternalID != "already-created" || fake.polls != 1 || fake.publishes != 0 {
		t.Fatalf("result=%+v polls=%d publishes=%d", result, fake.polls, fake.publishes)
	}
}

func TestContentPublishUsesPublishWithoutKnownOperation(t *testing.T) {
	s := &Server{config: config.Config{ContentPublishingEnabled: true, ContentYouTubeEnabled: true}, publishAdapters: map[string]PublishAdapter{}}
	fake := &fakePublishAdapter{publish: PublishResult{ProviderOperationID: "operation-2", Pending: true}}
	s.SetPublishAdapter("YOUTUBE", fake)
	result, err := s.executeContentPublish(context.Background(), contentPublishJob{Platform: "YOUTUBE"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Pending || fake.polls != 0 || fake.publishes != 1 {
		t.Fatalf("result=%+v polls=%d publishes=%d", result, fake.polls, fake.publishes)
	}
}

func TestContentPublishKnownOperationNeverBlindlyPublishes(t *testing.T) {
	s := &Server{config: config.Config{ContentPublishingEnabled: true, ContentTikTokEnabled: true}, publishAdapters: map[string]PublishAdapter{}}
	fake := &fakePublishAdapter{pollErr: &providerError{Platform: "TIKTOK", Kind: providerRetryable, Message: "timeout"}}
	s.SetPublishAdapter("TIKTOK", fake)
	if _, err := s.executeContentPublish(context.Background(), contentPublishJob{Platform: "TIKTOK", ProviderOperationID: "operation-1"}); err == nil {
		t.Fatal("known operation timeout must be retried by Poll, not a second Publish")
	}
	if fake.polls != 1 || fake.publishes != 0 {
		t.Fatalf("polls=%d publishes=%d", fake.polls, fake.publishes)
	}
}

func TestContentPublishRetryAfterContract(t *testing.T) {
	clock := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	err := &providerError{Kind: providerRateLimit, RetryAfter: 2 * time.Hour}
	if got := contentRetryDelay(4, err); got != 2*time.Hour {
		t.Fatalf("Retry-After delay=%s, want 2h", got)
	}
	if got := contentRetryDelay(6, err); got != 0 {
		t.Fatalf("sixth retry delay=%s, want 0", got)
	}
	if got := nextContentRetryAt(clock, 4, err); !got.Equal(clock.Add(2 * time.Hour)) {
		t.Fatalf("fake-clock Retry-After time=%s", got)
	}
}

// Queue concurrency uses an actual migrated disposable PostgreSQL database
// when TEST_DATABASE_URL is supplied. It must never silently skip in that mode.
func TestContentPublishQueueIntegration(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to a disposable database migrated through 00025")
	}
	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatalf("connect TEST_DATABASE_URL: %v", err)
	}
	t.Cleanup(pool.Close)
	var ready bool
	if err = pool.QueryRow(t.Context(), `SELECT to_regclass('public.content_publish_jobs') IS NOT NULL`).Scan(&ready); err != nil || !ready {
		t.Fatalf("TEST_DATABASE_URL must be migrated through 00025: ready=%v err=%v", ready, err)
	}
	suffix := fmt.Sprintf("publish-queue-%d", time.Now().UnixNano())
	var org, company, user, creator, account, item, revision, target, job string
	mustScanID(t, pool, `INSERT INTO organizations(name,slug) VALUES($1,$2) RETURNING id`, &org, "Queue test", suffix)
	mustScanID(t, pool, `INSERT INTO companies(organization_id,name) VALUES($1,'Queue company') RETURNING id`, &company, org)
	mustScanID(t, pool, `INSERT INTO users(email,password_hash,role,status) VALUES($1,'test','ADMIN','ACTIVE') RETURNING id`, &user, suffix+"@example.com")
	if _, err := pool.Exec(t.Context(), `INSERT INTO organization_memberships(organization_id,user_id,membership_role) VALUES($1,$2,'OWNER')`, org, user); err != nil {
		t.Fatal(err)
	}
	mustScanID(t, pool, `INSERT INTO creators(organization_id,company_id,created_by,first_name,last_name,display_name) VALUES($1,$2,$3,'Queue','Worker','Queue Worker') RETURNING id`, &creator, org, company, user)
	mustScanID(t, pool, `INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name,status) VALUES($1,$2,'TIKTOK',$3,$3,'Queue account','ACTIVE') RETURNING id`, &account, org, company, suffix)
	if _, err := pool.Exec(t.Context(), `INSERT INTO creator_account_assignments(creator_id,platform_account_id) VALUES($1,$2)`, creator, account); err != nil {
		t.Fatal(err)
	}
	mustScanID(t, pool, `INSERT INTO content_items(organization_id,company_id,creator_id,created_by) VALUES($1,$2,$3,$4) RETURNING id`, &item, org, company, creator, user)
	mustScanID(t, pool, `INSERT INTO content_revisions(content_item_id,organization_id,revision,created_by) VALUES($1,$2,1,$3) RETURNING id`, &revision, item, org, user)
	mustScanID(t, pool, `INSERT INTO content_publish_targets(content_revision_id,organization_id,company_id,creator_id,platform_account_id,platform) VALUES($1,$2,$3,$4,$5,'TIKTOK') RETURNING id`, &target, revision, org, company, creator, account)
	mustScanID(t, pool, `INSERT INTO content_publish_jobs(target_id,content_revision_id,organization_id,company_id,run_at) VALUES($1,$2,$3,$4,now()-interval '1 minute') RETURNING id`, &job, target, revision, org, company)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_logs WHERE organization_id=$1`, org)
		_, _ = pool.Exec(context.Background(), `DELETE FROM content_publish_attempts WHERE organization_id=$1`, org)
		_, _ = pool.Exec(context.Background(), `DELETE FROM content_publish_jobs WHERE organization_id=$1`, org)
		_, _ = pool.Exec(context.Background(), `DELETE FROM content_publish_targets WHERE organization_id=$1`, org)
		_, _ = pool.Exec(context.Background(), `DELETE FROM content_revisions WHERE organization_id=$1`, org)
		_, _ = pool.Exec(context.Background(), `DELETE FROM content_items WHERE organization_id=$1`, org)
		_, _ = pool.Exec(context.Background(), `DELETE FROM creator_account_assignments WHERE creator_id=$1`, creator)
		_, _ = pool.Exec(context.Background(), `DELETE FROM platform_accounts WHERE organization_id=$1`, org)
		_, _ = pool.Exec(context.Background(), `DELETE FROM creators WHERE id=$1`, creator)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, user)
		_, _ = pool.Exec(context.Background(), `DELETE FROM companies WHERE id=$1`, company)
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, org)
	})
	s := &Server{pool: pool, config: config.Config{ContentPublishingEnabled: true, ContentTikTokEnabled: true}, now: time.Now, publishAdapters: map[string]PublishAdapter{}}
	type claimResult struct {
		worker string
		job    contentPublishJob
		ok     bool
		err    error
	}
	var wg sync.WaitGroup
	got := make(chan claimResult, 2)
	for _, id := range []string{"worker-a", "worker-b"} {
		wg.Add(1)
		go func(worker string) {
			defer wg.Done()
			claimedJob, ok, claimErr := s.claimContentPublishForOrganization(t.Context(), worker, org)
			got <- claimResult{worker: worker, job: claimedJob, ok: ok, err: claimErr}
		}(id)
	}
	wg.Wait()
	close(got)
	var winner claimResult
	claims, misses := 0, 0
	for result := range got {
		if result.err != nil {
			t.Fatalf("claim %s: %v", result.worker, result.err)
		}
		if result.ok {
			claims++
			winner = result
		} else {
			misses++
			if result.job.ID != "" || result.job.WorkerID != "" {
				t.Fatalf("losing worker %s received a job: %+v", result.worker, result.job)
			}
		}
	}
	if claims != 1 || misses != 1 {
		t.Fatalf("two workers produced claims=%d misses=%d, want exactly one of each", claims, misses)
	}
	if winner.job.ID != job || winner.job.OrganizationID != org || winner.job.WorkerID != winner.worker || (winner.worker != "worker-a" && winner.worker != "worker-b") {
		t.Fatalf("winner claimed wrong queue row or owner: worker=%s job=%+v want_job=%s", winner.worker, winner.job, job)
	}

	var lockedBy *string
	var execution int
	if err := pool.QueryRow(t.Context(), `SELECT locked_by,execution_count FROM content_publish_jobs WHERE id=$1`, job).Scan(&lockedBy, &execution); err != nil {
		t.Fatal(err)
	}
	if lockedBy == nil || *lockedBy != winner.worker || execution != 1 || winner.job.Execution != execution {
		t.Fatalf("claimed lease owner=%v execution=%d winner=%+v", lockedBy, execution, winner)
	}
	var totalAttempts, runningAttempts int
	if err = pool.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER (WHERE attempt_number=$2 AND status='RUNNING') FROM content_publish_attempts WHERE job_id=$1`, job, execution).Scan(&totalAttempts, &runningAttempts); err != nil || totalAttempts != 1 || runningAttempts != 1 {
		t.Fatalf("claimed execution attempts total=%d running=%d err=%v, want exactly one RUNNING attempt", totalAttempts, runningAttempts, err)
	}
	stale := contentPublishJob{ID: job, TargetID: target, RevisionID: revision, OrganizationID: org, CompanyID: company, WorkerID: "not-" + *lockedBy, Execution: execution}
	if err := s.finishContentPublish(t.Context(), stale, PublishResult{ExternalID: "must-not-write"}, nil); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := pool.QueryRow(t.Context(), `SELECT status FROM content_publish_jobs WHERE id=$1`, job).Scan(&state); err != nil || state != "RUNNING" {
		t.Fatalf("stale worker finalized job: status=%s err=%v", state, err)
	}

	// With two independently due rows under the same lifecycle prefix, SKIP
	// LOCKED must advance the losing worker to the second row. Neither worker is
	// allowed to report an empty poll or share ownership of one execution.
	if _, err = pool.Exec(t.Context(), `UPDATE content_publish_jobs SET run_at=now()+interval '1 hour' WHERE id=$1`, job); err != nil {
		t.Fatal(err)
	}
	var twoTargets, twoJobs []string
	for index := 0; index < 2; index++ {
		var twoItem, twoRevision, targetID, jobID string
		mustScanID(t, pool, `INSERT INTO content_items(organization_id,company_id,creator_id,created_by) VALUES($1,$2,$3,$4) RETURNING id`, &twoItem, org, company, creator, user)
		mustScanID(t, pool, `INSERT INTO content_revisions(content_item_id,organization_id,revision,created_by) VALUES($1,$2,1,$3) RETURNING id`, &twoRevision, twoItem, org, user)
		mustScanID(t, pool, `INSERT INTO content_publish_targets(content_revision_id,organization_id,company_id,creator_id,platform_account_id,platform) VALUES($1,$2,$3,$4,$5,'TIKTOK') RETURNING id`, &targetID, twoRevision, org, company, creator, account)
		mustScanID(t, pool, `INSERT INTO content_publish_jobs(target_id,content_revision_id,organization_id,company_id,run_at) VALUES($1,$2,$3,$4,now()-interval '1 minute') RETURNING id`, &jobID, targetID, twoRevision, org, company)
		twoTargets = append(twoTargets, targetID)
		twoJobs = append(twoJobs, jobID)
	}
	startTwoClaims := make(chan struct{})
	twoResults := make(chan claimResult, 2)
	for _, worker := range []string{"two-job-worker-a", "two-job-worker-b"} {
		go func(workerID string) {
			<-startTwoClaims
			claimedJob, ok, claimErr := s.claimContentPublishForOrganization(t.Context(), workerID, org)
			twoResults <- claimResult{worker: workerID, job: claimedJob, ok: ok, err: claimErr}
		}(worker)
	}
	close(startTwoClaims)
	owners := map[string]string{}
	for index := 0; index < 2; index++ {
		result := <-twoResults
		if result.err != nil || !result.ok {
			t.Fatalf("two-job claim worker=%s ok=%v err=%v", result.worker, result.ok, result.err)
		}
		if _, duplicate := owners[result.job.ID]; duplicate {
			t.Fatalf("two workers claimed the same job %s", result.job.ID)
		}
		owners[result.job.ID] = result.worker
	}
	for _, jobID := range twoJobs {
		owner, claimed := owners[jobID]
		if !claimed {
			t.Fatalf("due job %s was not claimed; owners=%v", jobID, owners)
		}
		var databaseOwner *string
		var executions, attempts int
		if err = pool.QueryRow(t.Context(), `SELECT job.locked_by,job.execution_count,(SELECT count(*) FROM content_publish_attempts attempt WHERE attempt.job_id=job.id AND attempt.status='RUNNING') FROM content_publish_jobs job WHERE job.id=$1`, jobID).Scan(&databaseOwner, &executions, &attempts); err != nil || databaseOwner == nil || *databaseOwner != owner || executions != 1 || attempts != 1 {
			t.Fatalf("two-job lease job=%s owner=%v want=%s executions=%d attempts=%d err=%v", jobID, databaseOwner, owner, executions, attempts, err)
		}
	}
	if _, err = pool.Exec(t.Context(), `UPDATE content_publish_attempts SET status='CANCELLED',finished_at=now() WHERE job_id=ANY($1::uuid[]) AND status='RUNNING'`, twoJobs); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE content_publish_jobs SET status='CANCELLED',locked_by=NULL,locked_at=NULL,lease_expires_at=NULL WHERE id=ANY($1::uuid[])`, twoJobs); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE content_publish_targets SET status='CANCELLED',cancellation_requested_at=now() WHERE id=ANY($1::uuid[])`, twoTargets); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(t.Context(), `UPDATE content_publish_jobs SET status='RETRY_SCHEDULED',locked_by=NULL,reauth_deadline_at=now()+interval '1 hour',run_at=now()+interval '1 hour' WHERE id=$1`, job); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE content_publish_targets SET status='WAITING_FOR_REAUTH' WHERE id=$1`, target); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ResumeContentPublishAfterReconnect(t.Context()); err != nil || n != 0 {
		t.Fatalf("unscoped resume n=%d err=%v", n, err)
	}
	var targetState string
	if err := pool.QueryRow(t.Context(), `SELECT j.status,t.status FROM content_publish_jobs j JOIN content_publish_targets t ON t.id=j.target_id WHERE j.id=$1`, job).Scan(&state, &targetState); err != nil || state != "RETRY_SCHEDULED" || targetState != "WAITING_FOR_REAUTH" {
		t.Fatalf("unscoped resume states job=%s target=%s err=%v", state, targetState, err)
	}

	// A callback for account A must not revive account B just because both are
	// under the same creator and company.
	var otherAccount, otherTarget, otherJob string
	mustScanID(t, pool, `INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name,status) VALUES($1,$2,'TIKTOK',$3,$3,'Other queue account','ACTIVE') RETURNING id`, &otherAccount, org, company, suffix+"-other")
	if _, err := pool.Exec(t.Context(), `INSERT INTO creator_account_assignments(creator_id,platform_account_id) VALUES($1,$2)`, creator, otherAccount); err != nil {
		t.Fatal(err)
	}
	mustScanID(t, pool, `INSERT INTO content_publish_targets(content_revision_id,organization_id,company_id,creator_id,platform_account_id,platform,status) VALUES($1,$2,$3,$4,$5,'TIKTOK','WAITING_FOR_REAUTH') RETURNING id`, &otherTarget, revision, org, company, creator, otherAccount)
	mustScanID(t, pool, `INSERT INTO content_publish_jobs(target_id,content_revision_id,organization_id,company_id,run_at,status,reauth_deadline_at) VALUES($1,$2,$3,$4,now()+interval '1 hour','RETRY_SCHEDULED',now()+interval '1 hour') RETURNING id`, &otherJob, otherTarget, revision, org, company)
	if _, err := pool.Exec(t.Context(), `UPDATE content_publish_jobs SET status='RETRY_SCHEDULED',reauth_deadline_at=now()+interval '1 hour' WHERE id=$1`, job); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE content_publish_targets SET status='WAITING_FOR_REAUTH' WHERE id=$1`, target); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ResumeContentPublishAfterReconnectForAccount(t.Context(), org, account); err != nil || n != 1 {
		t.Fatalf("scoped resume n=%d err=%v", n, err)
	}
	var otherState string
	if err := pool.QueryRow(t.Context(), `SELECT status FROM content_publish_targets WHERE id=$1`, otherTarget).Scan(&otherState); err != nil || otherState != "WAITING_FOR_REAUTH" {
		t.Fatalf("account A reconnect resumed account B: status=%s err=%v", otherState, err)
	}

	blocking := &blockingPublishAdapter{started: make(chan struct{}), release: make(chan struct{})}
	s.SetPublishAdapter("TIKTOK", blocking)
	done := make(chan error, 1)
	go func() { _, e := s.runContentPublishForOrganization(t.Context(), "io-worker", 1, org); done <- e }()
	select {
	case <-blocking.started:
	case <-time.After(time.Second):
		t.Fatal("Publish did not start")
	}
	readCtx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	if _, err := pool.Exec(readCtx, `UPDATE companies SET name=name WHERE id=$1`, company); err != nil {
		t.Fatalf("provider I/O retained DB lock: %v", err)
	}
	close(blocking.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	// Provider-controlled TikTok text must not survive persistence, the public
	// attempt projection, or the worker error returned to cmd/worker logging.
	const providerCanary = "https://evil.example/cb?access_token=db-canary&sig=signed-query-canary b3BhcXVlLWRiLWNhbmFyeQ== opaque-db-id-canary"
	var redactionItem, redactionRevision, redactionTarget, redactionJob string
	mustScanID(t, pool, `INSERT INTO content_items(organization_id,company_id,creator_id,created_by) VALUES($1,$2,$3,$4) RETURNING id`, &redactionItem, org, company, creator, user)
	mustScanID(t, pool, `INSERT INTO content_revisions(content_item_id,organization_id,revision,created_by) VALUES($1,$2,1,$3) RETURNING id`, &redactionRevision, redactionItem, org, user)
	mustScanID(t, pool, `INSERT INTO content_publish_targets(content_revision_id,organization_id,company_id,creator_id,platform_account_id,platform,status) VALUES($1,$2,$3,$4,$5,'TIKTOK','READY') RETURNING id`, &redactionTarget, redactionRevision, org, company, creator, account)
	mustScanID(t, pool, `INSERT INTO content_publish_jobs(target_id,content_revision_id,organization_id,company_id,run_at,status) VALUES($1,$2,$3,$4,now()-interval '1 minute','READY') RETURNING id`, &redactionJob, redactionTarget, redactionRevision, org, company)
	s.SetPublishAdapter("TIKTOK", &fakePublishAdapter{publishErr: &providerError{Platform: "TikTok", Kind: providerPermanent, Message: providerCanary}})
	_, redactionErr := s.runContentPublishForOrganization(t.Context(), "redaction-worker", 1, org)
	if redactionErr == nil || strings.Contains(redactionErr.Error(), providerCanary) {
		t.Fatalf("worker error was not redacted: %v", redactionErr)
	}
	var targetMessage, attemptMessage, jobMessage string
	if err = pool.QueryRow(t.Context(), `SELECT target.error_message,attempt.error_message,job.last_error FROM content_publish_targets target JOIN content_publish_jobs job ON job.target_id=target.id JOIN content_publish_attempts attempt ON attempt.job_id=job.id WHERE target.id=$1`, redactionTarget).Scan(&targetMessage, &attemptMessage, &jobMessage); err != nil {
		t.Fatal(err)
	}
	for name, message := range map[string]string{"target": targetMessage, "attempt": attemptMessage, "job": jobMessage, "public": publicContentAttemptMessage(attemptMessage)} {
		if strings.Contains(message, providerCanary) || message != safeTikTokProviderMessage(providerPermanent) {
			t.Fatalf("%s message was not safely normalized: %q", name, message)
		}
	}
}

func TestContentPublishLeaseReclaimIntegration(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to a disposable database migrated through 00026")
	}
	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var leaseSchema bool
	if err = pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='content_publish_jobs' AND column_name='lease_expires_at') AND to_regclass('content_publish_jobs_expired_lease_idx') IS NOT NULL`).Scan(&leaseSchema); err != nil || !leaseSchema {
		t.Fatalf("TEST_DATABASE_URL must include publishing lease schema and index: ready=%v err=%v", leaseSchema, err)
	}

	suffix := fmt.Sprintf("publish-lease-%d", time.Now().UnixNano())
	var org, company, user, creator, account, item, revision string
	mustScanID(t, pool, `INSERT INTO organizations(name,slug) VALUES('Lease test',$1) RETURNING id`, &org, suffix)
	mustScanID(t, pool, `INSERT INTO companies(organization_id,name) VALUES($1,'Lease company') RETURNING id`, &company, org)
	mustScanID(t, pool, `INSERT INTO users(email,password_hash,role,status) VALUES($1,'test','ADMIN','ACTIVE') RETURNING id`, &user, suffix+"@example.com")
	if _, err = pool.Exec(t.Context(), `INSERT INTO organization_memberships(organization_id,user_id,membership_role) VALUES($1,$2,'OWNER')`, org, user); err != nil {
		t.Fatal(err)
	}
	mustScanID(t, pool, `INSERT INTO creators(organization_id,company_id,created_by,first_name,last_name,display_name) VALUES($1,$2,$3,'Lease','Worker','Lease Worker') RETURNING id`, &creator, org, company, user)
	mustScanID(t, pool, `INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name,status) VALUES($1,$2,'TIKTOK',$3,$3,'Lease account','ACTIVE') RETURNING id`, &account, org, company, suffix)
	if _, err = pool.Exec(t.Context(), `INSERT INTO creator_account_assignments(creator_id,platform_account_id) VALUES($1,$2)`, creator, account); err != nil {
		t.Fatal(err)
	}
	mustScanID(t, pool, `INSERT INTO content_items(organization_id,company_id,creator_id,created_by) VALUES($1,$2,$3,$4) RETURNING id`, &item, org, company, creator, user)
	mustScanID(t, pool, `INSERT INTO content_revisions(content_item_id,organization_id,revision,created_by) VALUES($1,$2,1,$3) RETURNING id`, &revision, item, org, user)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM audit_logs WHERE organization_id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM publications WHERE organization_id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM content_notifications WHERE organization_id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM content_publish_attempts WHERE organization_id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM content_publish_jobs WHERE organization_id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM content_publish_targets WHERE organization_id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM content_revisions WHERE organization_id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM content_items WHERE organization_id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM creator_account_assignments WHERE creator_id=$1`, creator)
		_, _ = pool.Exec(ctx, `DELETE FROM platform_accounts WHERE organization_id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM creators WHERE id=$1`, creator)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, user)
		_, _ = pool.Exec(ctx, `DELETE FROM companies WHERE id=$1`, company)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id=$1`, org)
	})

	var unixNano atomic.Int64
	unixNano.Store(time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC).UnixNano())
	adapter := &leasePublishAdapter{}
	s := &Server{
		pool: pool, config: config.Config{ContentPublishingEnabled: true, ContentTikTokEnabled: true},
		now:                  func() time.Time { return time.Unix(0, unixNano.Load()).UTC() },
		publishLeaseDuration: time.Minute, publishAdapters: map[string]PublishAdapter{"TIKTOK": adapter},
	}
	seedJob := func(label string) (target, job string) {
		t.Helper()
		var seededItem, seededRevision string
		mustScanID(t, pool, `INSERT INTO content_items(organization_id,company_id,creator_id,created_by) VALUES($1,$2,$3,$4) RETURNING id`, &seededItem, org, company, creator, user)
		mustScanID(t, pool, `INSERT INTO content_revisions(content_item_id,organization_id,revision,description,created_by) VALUES($1,$2,1,$3,$4) RETURNING id`, &seededRevision, seededItem, org, label, user)
		mustScanID(t, pool, `INSERT INTO content_publish_targets(content_revision_id,organization_id,company_id,creator_id,platform_account_id,platform,status) VALUES($1,$2,$3,$4,$5,'TIKTOK','READY') RETURNING id`, &target, seededRevision, org, company, creator, account)
		mustScanID(t, pool, `INSERT INTO content_publish_jobs(target_id,content_revision_id,organization_id,company_id,run_at) VALUES($1,$2,$3,$4,$5) RETURNING id`, &job, target, seededRevision, org, company, s.publishNow().Add(-time.Minute))
		return target, job
	}
	claimOnce := func(worker string) contentPublishJob {
		t.Helper()
		j, ok, claimErr := s.claimContentPublishForOrganization(t.Context(), worker, org)
		if claimErr != nil || !ok {
			t.Fatalf("claim %s: ok=%v err=%v", worker, ok, claimErr)
		}
		return j
	}
	reclaimWithTwoWorkers := func() contentPublishJob {
		t.Helper()
		jobs := make(chan contentPublishJob, 2)
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for _, worker := range []string{"reclaimer-a", "reclaimer-b"} {
			wg.Add(1)
			go func(workerID string) {
				defer wg.Done()
				j, ok, claimErr := s.claimContentPublishForOrganization(t.Context(), workerID, org)
				if claimErr != nil {
					errs <- claimErr
					return
				}
				if ok {
					jobs <- j
				}
			}(worker)
		}
		wg.Wait()
		close(jobs)
		close(errs)
		for claimErr := range errs {
			t.Fatal(claimErr)
		}
		var claimed []contentPublishJob
		for j := range jobs {
			claimed = append(claimed, j)
		}
		if len(claimed) != 1 {
			t.Fatalf("reclaim winners=%d, want exactly one", len(claimed))
		}
		return claimed[0]
	}

	// A crash with no durable provider operation is ambiguous. Reclaim records
	// a second execution but fails closed without a provider create call.
	_, noOperationJob := seedJob("no-operation")
	first := claimOnce("crashed-no-operation")
	unixNano.Add(int64(2 * time.Minute))
	reclaimed := reclaimWithTwoWorkers()
	if reclaimed.ID != noOperationJob || reclaimed.Execution != first.Execution+1 || !reclaimed.RecoveryUncertain {
		t.Fatalf("unexpected no-operation reclaim: %+v first=%+v", reclaimed, first)
	}
	result, runErr := s.executeContentPublish(t.Context(), reclaimed)
	if runErr == nil {
		t.Fatal("ambiguous no-operation recovery must fail closed")
	}
	if err = s.finishContentPublish(t.Context(), reclaimed, result, runErr); err != nil {
		t.Fatal(err)
	}
	if adapter.publishes.Load() != 0 || adapter.polls.Load() != 0 {
		t.Fatalf("ambiguous recovery called provider: publishes=%d polls=%d", adapter.publishes.Load(), adapter.polls.Load())
	}
	assertPublishLeaseStates(t, pool, noOperationJob, "FAILED", []string{"FAILED", "FAILED"})

	// If an adapter persisted its operation before the crash, exactly one
	// reclaimer resumes by Poll; Publish is never called again.
	targetWithOperation, jobWithOperation := seedJob("known-operation")
	first = claimOnce("crashed-known-operation")
	if _, err = pool.Exec(t.Context(), `UPDATE content_publish_targets SET provider_operation_id='provider-op-1',status='PROCESSING' WHERE id=$1`, targetWithOperation); err != nil {
		t.Fatal(err)
	}
	unixNano.Add(int64(2 * time.Minute))
	reclaimed = reclaimWithTwoWorkers()
	if reclaimed.ID != jobWithOperation || reclaimed.ProviderOperationID != "provider-op-1" || reclaimed.Execution != first.Execution+1 {
		t.Fatalf("unexpected known-operation reclaim: %+v", reclaimed)
	}
	result, runErr = s.executeContentPublish(t.Context(), reclaimed)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if err = s.finishContentPublish(t.Context(), reclaimed, result, nil); err != nil {
		t.Fatal(err)
	}
	if adapter.publishes.Load() != 0 || adapter.polls.Load() != 1 {
		t.Fatalf("known operation recovery: publishes=%d polls=%d", adapter.publishes.Load(), adapter.polls.Load())
	}
	assertPublishLeaseStates(t, pool, jobWithOperation, "SUCCEEDED", []string{"FAILED", "SUCCEEDED"})
	var persistedTargetID, persistedTargetURL, persistedPublicationID, persistedPermalink string
	if err = pool.QueryRow(t.Context(), `SELECT target.external_id,COALESCE(target.external_url,''),publication.external_id,COALESCE(publication.permalink,'') FROM content_publish_targets target JOIN publications publication ON publication.content_publish_target_id=target.id WHERE target.id=$1`, targetWithOperation).Scan(&persistedTargetID, &persistedTargetURL, &persistedPublicationID, &persistedPermalink); err != nil {
		t.Fatal(err)
	}
	if persistedTargetID != "7234567890123456789" || persistedPublicationID != persistedTargetID || persistedTargetURL != "" || persistedPermalink != "" {
		t.Fatalf("poisoned provider result persisted: target=(%q,%q) publication=(%q,%q)", persistedTargetID, persistedTargetURL, persistedPublicationID, persistedPermalink)
	}

	// Cancellation wins over reclaim and suppresses all provider I/O.
	targetCancelled, jobCancelled := seedJob("cancelled")
	first = claimOnce("crashed-cancelled")
	if _, err = pool.Exec(t.Context(), `UPDATE content_publish_targets SET cancellation_requested_at=$2 WHERE id=$1`, targetCancelled, s.publishNow()); err != nil {
		t.Fatal(err)
	}
	unixNano.Add(int64(2 * time.Minute))
	reclaimed = reclaimWithTwoWorkers()
	if reclaimed.ID != jobCancelled || !reclaimed.CancellationRequested || reclaimed.Execution != first.Execution+1 {
		t.Fatalf("unexpected cancellation reclaim: %+v", reclaimed)
	}
	result, runErr = s.executeContentPublish(t.Context(), reclaimed)
	if runErr == nil {
		t.Fatal("cancelled reclaim must not enter provider adapter")
	}
	if err = s.finishContentPublish(t.Context(), reclaimed, result, runErr); err != nil {
		t.Fatal(err)
	}
	if adapter.publishes.Load() != 0 || adapter.polls.Load() != 1 {
		t.Fatalf("cancellation called provider: publishes=%d polls=%d", adapter.publishes.Load(), adapter.polls.Load())
	}
	assertPublishLeaseStates(t, pool, jobCancelled, "CANCELLED", []string{"CANCELLED", "CANCELLED"})

	// A live execution renews its durable deadline. Advancing the fake clock
	// beyond the original deadline must not make it reclaimable after heartbeat.
	s.publishLeaseDuration = 300 * time.Millisecond
	targetHeartbeat, jobHeartbeat := seedJob("heartbeat")
	live := claimOnce("live-heartbeat")
	var originalLease time.Time
	if err = pool.QueryRow(t.Context(), `SELECT lease_expires_at FROM content_publish_jobs WHERE id=$1`, jobHeartbeat).Scan(&originalLease); err != nil {
		t.Fatal(err)
	}
	unixNano.Add(int64(250 * time.Millisecond))
	heartbeatCtx, cancelHeartbeat := context.WithCancel(t.Context())
	stopHeartbeat := s.startContentPublishHeartbeat(heartbeatCtx, cancelHeartbeat, live)
	deadline := time.Now().Add(time.Second)
	var renewedLease time.Time
	for time.Now().Before(deadline) {
		if err = pool.QueryRow(t.Context(), `SELECT lease_expires_at FROM content_publish_jobs WHERE id=$1`, jobHeartbeat).Scan(&renewedLease); err == nil && renewedLease.After(originalLease) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	stopHeartbeat()
	cancelHeartbeat()
	if !renewedLease.After(originalLease) {
		t.Fatalf("heartbeat did not renew lease: original=%s renewed=%s err=%v", originalLease, renewedLease, err)
	}
	unixNano.Add(int64(100 * time.Millisecond)) // original lease has expired; renewed lease has not.
	if _, ok, claimErr := s.claimContentPublishForOrganization(t.Context(), "must-not-reclaim-live", org); claimErr != nil || ok {
		t.Fatalf("live heartbeat execution was reclaimed: ok=%v err=%v", ok, claimErr)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE content_publish_targets SET cancellation_requested_at=$2 WHERE id=$1`, targetHeartbeat, s.publishNow()); err != nil {
		t.Fatal(err)
	}
	if err = s.finishContentPublish(t.Context(), live, PublishResult{}, context.Canceled); err != nil {
		t.Fatal(err)
	}
	assertPublishLeaseStates(t, pool, jobHeartbeat, "CANCELLED", []string{"CANCELLED"})

	// Revoking the active creator/account assignment fences queued work in the
	// same transaction. A subsequent claimant cannot enter provider I/O.
	targetRevoked, jobRevoked := seedJob("revoked-assignment")
	revokeTx, txErr := pool.Begin(t.Context())
	if txErr != nil {
		t.Fatal(txErr)
	}
	if txErr = cancelContentPublishesForAccountReassignmentTx(t.Context(), revokeTx, org, account, ""); txErr == nil {
		_, txErr = revokeTx.Exec(t.Context(), `UPDATE creator_account_assignments SET valid_to=GREATEST(valid_from+interval '1 microsecond',$3) WHERE organization_id=$1 AND platform_account_id=$2 AND valid_to IS NULL`, org, account, s.publishNow())
	}
	if txErr == nil {
		type claimResult struct {
			ok  bool
			err error
		}
		claimDone := make(chan claimResult, 1)
		go func() {
			_, ok, claimErr := s.claimContentPublishForOrganization(t.Context(), "revoke-race-claimant", org)
			claimDone <- claimResult{ok: ok, err: claimErr}
		}()
		claimReturned := false
		select {
		case result := <-claimDone:
			claimReturned = true
			if result.err != nil || result.ok {
				t.Fatalf("claim crossed uncommitted revoke fence: %+v", result)
			}
		case <-time.After(100 * time.Millisecond):
		}
		txErr = revokeTx.Commit(t.Context())
		if txErr == nil && !claimReturned {
			select {
			case result := <-claimDone:
				if result.err != nil || result.ok {
					t.Fatalf("claim after committed revoke: %+v", result)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("claim did not resume after revoke commit")
			}
		}
	} else {
		_ = revokeTx.Rollback(t.Context())
	}
	if txErr != nil {
		t.Fatal(txErr)
	}
	assertPublishLeaseStates(t, pool, jobRevoked, "CANCELLED", nil)
	var revokedTargetStatus string
	if err = pool.QueryRow(t.Context(), `SELECT status FROM content_publish_targets WHERE id=$1`, targetRevoked).Scan(&revokedTargetStatus); err != nil || revokedTargetStatus != "CANCELLED" {
		t.Fatalf("revoked target status=%s err=%v", revokedTargetStatus, err)
	}
	if _, err = pool.Exec(t.Context(), `INSERT INTO creator_account_assignments(creator_id,platform_account_id) VALUES($1,$2)`, creator, account); err != nil {
		t.Fatal(err)
	}

	// A cancellation marker may stop further provider work, but it cannot erase
	// a provider result that crossed the external side-effect boundary before
	// the heartbeat observed the marker.
	cancelledTarget, cancelledJob := seedJob("cancel-in-flight-confirmed")
	cancelAdapter := &boundaryConfirmedAdapter{started: make(chan struct{}), externalID: "7234567890123456790"}
	s.SetPublishAdapter("TIKTOK", cancelAdapter)
	s.publishLeaseDuration = 300 * time.Millisecond
	cancelRunDone := make(chan error, 1)
	go func() {
		_, runErr := s.runContentPublishForOrganization(t.Context(), "cancel-boundary-worker", 1, org)
		cancelRunDone <- runErr
	}()
	select {
	case <-cancelAdapter.started:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel boundary provider did not start")
	}
	if _, err = pool.Exec(t.Context(), `UPDATE content_publish_targets SET cancellation_requested_at=$2 WHERE id=$1`, cancelledTarget, s.publishNow()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelRunDone:
	case <-time.After(2 * time.Second):
		t.Fatal("target cancellation did not cancel in-flight provider context")
	}
	assertPublishLeaseStates(t, pool, cancelledJob, "SUCCEEDED", []string{"SUCCEEDED"})
	var cancellationPublication int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM publications WHERE content_publish_target_id=$1 AND external_id='7234567890123456790'`, cancelledTarget).Scan(&cancellationPublication); err != nil || cancellationPublication != 1 {
		t.Fatalf("confirmed provider result was lost after target cancellation: count=%d err=%v", cancellationPublication, err)
	}

	// Archive racing with in-flight provider work marks the target while holding
	// the company lifecycle lock. The authorization-aware heartbeat cancels the
	// provider context, and finalization leaves a terminal attempt.
	_, archivedJob := seedJob("archive-in-flight")
	archiveAdapter := &boundaryConfirmedAdapter{started: make(chan struct{}), externalID: "7234567890123456791"}
	s.SetPublishAdapter("TIKTOK", archiveAdapter)
	s.publishLeaseDuration = 300 * time.Millisecond
	runDone := make(chan error, 1)
	go func() {
		_, runErr := s.runContentPublishForOrganization(t.Context(), "archive-worker", 1, org)
		runDone <- runErr
	}()
	select {
	case <-archiveAdapter.started:
	case <-time.After(2 * time.Second):
		t.Fatal("archive race provider did not start")
	}
	route := chi.NewRouteContext()
	route.URLParams.Add("id", company)
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/companies/"+company, nil)
	request = request.WithContext(context.WithValue(context.WithValue(request.Context(), chi.RouteCtxKey, route), principalKey, principal{ID: user, OrganizationID: org, Role: roleOwner}))
	response := httptest.NewRecorder()
	s.archiveCompany(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("archive status=%d body=%s", response.Code, response.Body.String())
	}
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("archive did not cancel in-flight provider context")
	}
	assertPublishLeaseStates(t, pool, archivedJob, "SUCCEEDED", []string{"SUCCEEDED"})
	var preservedPublication int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM publications WHERE content_publish_target_id=(SELECT target_id FROM content_publish_jobs WHERE id=$1) AND external_id='7234567890123456791'`, archivedJob).Scan(&preservedPublication); err != nil || preservedPublication != 1 {
		t.Fatalf("confirmed provider result was lost after archive: count=%d err=%v", preservedPublication, err)
	}
}

func assertPublishLeaseStates(t *testing.T, pool *pgxpool.Pool, jobID, wantJob string, wantAttempts []string) {
	t.Helper()
	var gotJob string
	if err := pool.QueryRow(t.Context(), `SELECT status FROM content_publish_jobs WHERE id=$1`, jobID).Scan(&gotJob); err != nil || gotJob != wantJob {
		t.Fatalf("job %s status=%s want=%s err=%v", jobID, gotJob, wantJob, err)
	}
	rows, err := pool.Query(t.Context(), `SELECT status FROM content_publish_attempts WHERE job_id=$1 ORDER BY attempt_number`, jobID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var status string
		if err = rows.Scan(&status); err != nil {
			t.Fatal(err)
		}
		got = append(got, status)
	}
	if fmt.Sprint(got) != fmt.Sprint(wantAttempts) {
		t.Fatalf("job %s attempts=%v want=%v", jobID, got, wantAttempts)
	}
}
