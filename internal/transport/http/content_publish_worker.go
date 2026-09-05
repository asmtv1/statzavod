package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// Queue orchestration keeps all provider I/O outside database transactions.
type PublishRequest struct {
	JobID, TargetID, RevisionID, OrganizationID, CompanyID, CreatorID string
	Platform, AccountID, ProviderOperationID                          string
	Snapshot                                                          []byte
}
type PublishResult struct {
	ProviderOperationID, ExternalID, ExternalURL string
	Pending                                      bool
}

// contentPublishSnapshot is frozen atomically before a job is inserted. Provider
// adapters must use it for publish payloads rather than rereading revision or
// target fields that could otherwise change between scheduling and execution.
type contentPublishSnapshot struct {
	Revision          int             `json:"revision"`
	Caption           string          `json:"caption"`
	Hashtags          []string        `json:"hashtags"`
	PlatformOptions   json.RawMessage `json:"platformOptions"`
	MediaID           string          `json:"mediaId"`
	MediaObjectKey    string          `json:"mediaObjectKey"`
	MediaSHA256       string          `json:"mediaSha256"`
	MediaDurationMS   int64           `json:"mediaDurationMs"`
	AccountExternalID string          `json:"accountExternalId"`
	AccountType       string          `json:"accountType"`
	ConnectionMode    string          `json:"connectionMode"`
}

func decodeContentPublishSnapshot(raw []byte) (contentPublishSnapshot, error) {
	var snapshot contentPublishSnapshot
	if len(raw) == 0 || json.Unmarshal(raw, &snapshot) != nil || snapshot.Revision < 1 || snapshot.MediaID == "" || snapshot.MediaObjectKey == "" || len(snapshot.PlatformOptions) == 0 {
		return snapshot, &providerError{Kind: providerPermanent, Message: "publish snapshot is missing or invalid"}
	}
	return snapshot, nil
}

type PublishAdapter interface {
	Publish(context.Context, PublishRequest) (PublishResult, error)
	Poll(context.Context, PublishRequest) (PublishResult, error)
}
type contentPublishJob struct {
	ID, TargetID, RevisionID, OrganizationID, CompanyID, CreatorID string
	Platform, AccountID, ProviderOperationID, WorkerID             string
	Snapshot                                                       []byte
	Attempt, Execution                                             int
	RecoveryUncertain, CancellationRequested                       bool
}

var contentRetryBackoff = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 6 * time.Hour}

func (s *Server) SetPublishAdapter(platform string, a PublishAdapter) {
	if s.publishAdapters == nil {
		s.publishAdapters = map[string]PublishAdapter{}
	}
	s.publishAdapters[strings.ToUpper(platform)] = a
}

func (s *Server) RunContentPublish(ctx context.Context, workerID string, limit int) (int, error) {
	return s.runContentPublishForOrganization(ctx, workerID, limit, "")
}

func (s *Server) runContentPublishForOrganization(ctx context.Context, workerID string, limit int, organizationID string) (int, error) {
	if !s.config.ContentPublishingEnabled {
		return 0, nil
	}
	if limit <= 0 {
		limit = 10
	}
	processed := 0
	var first error
	for processed < limit {
		j, ok, err := s.claimContentPublishForOrganization(ctx, workerID, organizationID)
		if err != nil {
			return processed, err
		}
		if !ok {
			break
		}
		processed++
		// The lease is renewed while provider I/O is in flight. If ownership is
		// lost, cancellation stops cooperative adapters; finalization is also
		// fenced by worker id and execution number.
		executeCtx, cancelExecute := context.WithCancel(ctx)
		stopHeartbeat := s.startContentPublishHeartbeat(executeCtx, cancelExecute, j)
		r, runErr := s.executeContentPublish(executeCtx, j)
		stopHeartbeat()
		cancelExecute()
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		finishErr := s.finishContentPublish(finishCtx, j, r, runErr)
		cancel()
		providerBoundaryConfirmed := r.ExternalID != "" || (r.Pending && (r.ProviderOperationID != "" || j.ProviderOperationID != ""))
		if first == nil && runErr != nil && !providerBoundaryConfirmed {
			first = safeContentPublishWorkerError(runErr)
		}
		if first == nil && finishErr != nil {
			first = finishErr
		}
	}
	return processed, first
}

func safeContentPublishWorkerError(err error) error {
	var providerFailure *providerError
	if !errors.As(err, &providerFailure) || !strings.EqualFold(providerFailure.Platform, "TikTok") {
		return err
	}
	return &providerError{
		Platform:   "TikTok",
		Kind:       providerFailure.Kind,
		StatusCode: providerFailure.StatusCode,
		RetryAfter: providerFailure.RetryAfter,
		Message:    safeTikTokProviderMessage(providerFailure.Kind),
	}
}

func (s *Server) publishNow() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

func (s *Server) contentPublishLeaseDuration() time.Duration {
	if s.publishLeaseDuration <= 0 {
		return 10 * time.Minute
	}
	return s.publishLeaseDuration
}

// startContentPublishHeartbeat extends only the exact execution owned by this
// worker. A reclaimed execution cannot be resurrected even if a delayed
// heartbeat or finalizer from the crashed worker eventually arrives.
func (s *Server) startContentPublishHeartbeat(ctx context.Context, cancel context.CancelFunc, j contentPublishJob) func() {
	done := make(chan struct{})
	stopped := make(chan struct{})
	var once sync.Once
	interval := s.contentPublishLeaseDuration() / 3
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := s.publishNow()
				tag, err := s.pool.Exec(ctx, `WITH lifecycle AS MATERIALIZED (SELECT target.id FROM content_publish_targets target JOIN creators creator ON creator.id=target.creator_id AND creator.organization_id=target.organization_id AND creator.company_id=target.company_id JOIN companies company ON company.id=target.company_id AND company.organization_id=target.organization_id JOIN organizations organization ON organization.id=target.organization_id JOIN platform_accounts account ON account.id=target.platform_account_id AND account.organization_id=target.organization_id WHERE target.id=$7 AND target.organization_id=$2 AND target.cancellation_requested_at IS NULL AND target.status<>'CANCELLED' AND creator.archived_at IS NULL AND creator.status<>'ARCHIVED' AND company.lifecycle_state='ACTIVE' AND company.archived_at IS NULL AND organization.lifecycle_state='ACTIVE' AND account.status='ACTIVE' AND (EXISTS (SELECT 1 FROM creator_account_assignments assignment WHERE assignment.creator_id=target.creator_id AND assignment.platform_account_id=target.platform_account_id AND assignment.organization_id=target.organization_id AND assignment.valid_to IS NULL) OR (target.platform='VK' AND EXISTS (SELECT 1 FROM company_vk_accounts vk WHERE vk.platform_account_id=target.platform_account_id AND vk.organization_id=target.organization_id AND vk.company_id=target.company_id))) FOR KEY SHARE OF creator,company,account) UPDATE content_publish_jobs job SET locked_at=$5,lease_expires_at=$6,updated_at=$5 FROM lifecycle WHERE job.id=$1 AND job.organization_id=$2 AND job.status='RUNNING' AND job.locked_by=$3 AND job.execution_count=$4`, j.ID, j.OrganizationID, j.WorkerID, j.Execution, now, now.Add(s.contentPublishLeaseDuration()), j.TargetID)
				if err != nil || tag.RowsAffected() != 1 {
					cancel()
					return
				}
			}
		}
	}()
	return func() {
		once.Do(func() { close(done) })
		<-stopped
	}
}

// Lock order: workspace -> company -> job -> target. The provider runs after
// commit; finalization reacquires this same order.
func (s *Server) claimContentPublish(ctx context.Context, workerID string) (contentPublishJob, bool, error) {
	return s.claimContentPublishForOrganization(ctx, workerID, "")
}

// claimContentPublishForOrganization keeps integration tests isolated from
// other tenants sharing the same disposable database. Production passes an
// empty organization and retains the global queue semantics.
func (s *Server) claimContentPublishForOrganization(ctx context.Context, workerID, organizationID string) (contentPublishJob, bool, error) {
	enabledPlatforms := s.enabledContentPublishingPlatforms()
	if len(enabledPlatforms) == 0 {
		return contentPublishJob{}, false, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contentPublishJob{}, false, err
	}
	defer tx.Rollback(ctx)
	var j contentPublishJob
	now := s.publishNow()
	var previousStatus string
	// Select and lock the lifecycle prefix first. The second query performs the
	// actual candidate selection with SKIP LOCKED inside that creator scope, so
	// a competing worker immediately advances to the next due job rather than
	// returning a false empty poll after losing an exact-id race.
	excludedCreators := []string{}
	for {
		var candidateOrganization, candidateCompany, candidateCreator string
		err = tx.QueryRow(ctx, `SELECT j.organization_id::text,j.company_id::text,target.creator_id::text FROM content_publish_jobs j JOIN content_publish_targets target ON target.id=j.target_id AND target.organization_id=j.organization_id JOIN creators creator ON creator.id=target.creator_id AND creator.organization_id=target.organization_id AND creator.company_id=target.company_id JOIN companies company ON company.id=j.company_id AND company.organization_id=j.organization_id JOIN organizations organization ON organization.id=j.organization_id WHERE (NULLIF($2,'')::uuid IS NULL OR j.organization_id=NULLIF($2,'')::uuid) AND NOT (target.creator_id=ANY($3::uuid[])) AND target.platform::text=ANY($4::text[]) AND ((j.status IN ('READY','RETRY_SCHEDULED') AND j.run_at<=$1 AND target.status IN ('READY','SCHEDULED','RETRY_SCHEDULED') AND target.cancellation_requested_at IS NULL) OR (j.status='RUNNING' AND j.lease_expires_at<=$1)) AND creator.archived_at IS NULL AND creator.status<>'ARCHIVED' AND company.lifecycle_state='ACTIVE' AND company.archived_at IS NULL AND organization.lifecycle_state='ACTIVE' ORDER BY CASE WHEN j.status='RUNNING' THEN 0 ELSE 1 END,COALESCE(j.lease_expires_at,j.run_at),j.id LIMIT 1`, now, organizationID, excludedCreators, enabledPlatforms).Scan(&candidateOrganization, &candidateCompany, &candidateCreator)
		if errors.Is(err, pgx.ErrNoRows) {
			return contentPublishJob{}, false, tx.Commit(ctx)
		}
		if err != nil {
			return contentPublishJob{}, false, err
		}
		if err = lockActiveCompanyTarget(ctx, tx, candidateOrganization, candidateCompany); err != nil {
			return contentPublishJob{}, false, err
		}
		var activeCreatorID string
		if err = tx.QueryRow(ctx, `SELECT id::text FROM creators WHERE id=$1 AND organization_id=$2 AND company_id=$3 AND archived_at IS NULL AND status<>'ARCHIVED' FOR KEY SHARE`, candidateCreator, candidateOrganization, candidateCompany).Scan(&activeCreatorID); err != nil {
			return contentPublishJob{}, false, err
		}
		err = tx.QueryRow(ctx, `SELECT j.id::text,j.target_id::text,j.content_revision_id::text,j.organization_id::text,j.company_id::text,target.platform_account_id::text,j.status,j.attempt_count,j.execution_count+1 FROM content_publish_jobs j JOIN content_publish_targets target ON target.id=j.target_id AND target.organization_id=j.organization_id WHERE j.organization_id=$2 AND j.company_id=$3 AND target.creator_id=$4 AND target.platform::text=ANY($5::text[]) AND ((j.status IN ('READY','RETRY_SCHEDULED') AND j.run_at<=$1 AND target.status IN ('READY','SCHEDULED','RETRY_SCHEDULED') AND target.cancellation_requested_at IS NULL) OR (j.status='RUNNING' AND j.lease_expires_at<=$1)) ORDER BY CASE WHEN j.status='RUNNING' THEN 0 ELSE 1 END,COALESCE(j.lease_expires_at,j.run_at),j.id LIMIT 1 FOR UPDATE OF j SKIP LOCKED`, now, candidateOrganization, candidateCompany, candidateCreator, enabledPlatforms).Scan(&j.ID, &j.TargetID, &j.RevisionID, &j.OrganizationID, &j.CompanyID, &j.AccountID, &previousStatus, &j.Attempt, &j.Execution)
		if errors.Is(err, pgx.ErrNoRows) {
			excludedCreators = append(excludedCreators, candidateCreator)
			continue
		}
		if err != nil {
			return contentPublishJob{}, false, err
		}
		break
	}
	j.WorkerID = workerID
	if err = tx.QueryRow(ctx, `SELECT creator_id::text,platform::text,platform_account_id::text,COALESCE(provider_operation_id,''),COALESCE(sent_snapshot,'{}'::jsonb),cancellation_requested_at IS NOT NULL OR status='CANCELLED' FROM content_publish_targets WHERE id=$1 AND organization_id=$2 AND ($3='RUNNING' OR status IN ('READY','SCHEDULED','RETRY_SCHEDULED','PROCESSING','PUBLISHING')) FOR UPDATE`, j.TargetID, j.OrganizationID, previousStatus).Scan(&j.CreatorID, &j.Platform, &j.AccountID, &j.ProviderOperationID, &j.Snapshot, &j.CancellationRequested); err != nil {
		return contentPublishJob{}, false, err
	}
	var accountActive bool
	if err = tx.QueryRow(ctx, `SELECT status='ACTIVE' FROM platform_accounts WHERE id=$1 AND organization_id=$2 FOR KEY SHARE`, j.AccountID, j.OrganizationID).Scan(&accountActive); err != nil {
		return contentPublishJob{}, false, err
	}
	if !j.CancellationRequested {
		var assignmentOK bool
		if err = tx.QueryRow(ctx, `SELECT $6 AND (EXISTS (SELECT 1 FROM creator_account_assignments assignment WHERE assignment.creator_id=$1 AND assignment.platform_account_id=$2 AND assignment.organization_id=$3 AND assignment.valid_to IS NULL FOR KEY SHARE) OR ($4='VK' AND EXISTS (SELECT 1 FROM company_vk_accounts vk WHERE vk.platform_account_id=$2 AND vk.organization_id=$3 AND vk.company_id=$5)))`, j.CreatorID, j.AccountID, j.OrganizationID, j.Platform, j.CompanyID, accountActive).Scan(&assignmentOK); err != nil {
			return contentPublishJob{}, false, err
		}
		if !assignmentOK {
			j.CancellationRequested = true
			if _, err = tx.Exec(ctx, `UPDATE content_publish_targets SET cancellation_requested_at=COALESCE(cancellation_requested_at,$3),status=CASE WHEN $4='RUNNING' THEN status ELSE 'CANCELLED' END,updated_at=$3 WHERE id=$1 AND organization_id=$2`, j.TargetID, j.OrganizationID, now, previousStatus); err != nil {
				return contentPublishJob{}, false, err
			}
		}
	}
	if previousStatus == "RUNNING" {
		oldStatus, oldCode, oldMessage := "FAILED", "lease_expired", "worker lease expired; execution reclaimed"
		if j.CancellationRequested {
			oldStatus, oldCode, oldMessage = "CANCELLED", "cancelled", "publish cancelled while prior worker lease was active"
		}
		if _, err = tx.Exec(ctx, `UPDATE content_publish_attempts SET status=$3,error_code=COALESCE(error_code,$4),error_message=COALESCE(error_message,$5),finished_at=$6 WHERE job_id=$1 AND attempt_number=$2 AND status='RUNNING'`, j.ID, j.Execution-1, oldStatus, oldCode, oldMessage, now); err != nil {
			return contentPublishJob{}, false, err
		}
		// An empty operation id cannot distinguish a crash before provider I/O
		// from a crash after provider acceptance. Repeating Publish would risk a
		// duplicate, so this reclaim execution is deliberately fail-closed.
		j.RecoveryUncertain = j.ProviderOperationID == "" && !j.CancellationRequested
	}
	leaseExpires := now.Add(s.contentPublishLeaseDuration())
	if _, err = tx.Exec(ctx, `UPDATE content_publish_jobs SET status='RUNNING',locked_by=$3,locked_at=$4,lease_expires_at=$5,execution_count=$6,updated_at=$4 WHERE id=$1 AND organization_id=$2`, j.ID, j.OrganizationID, workerID, now, leaseExpires, j.Execution); err != nil {
		return contentPublishJob{}, false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE content_publish_targets SET status=CASE WHEN cancellation_requested_at IS NOT NULL OR status='CANCELLED' THEN 'CANCELLED' WHEN provider_operation_id IS NULL THEN 'PUBLISHING' ELSE 'PROCESSING' END,updated_at=now() WHERE id=$1 AND organization_id=$2`, j.TargetID, j.OrganizationID); err != nil {
		return contentPublishJob{}, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO content_publish_attempts(job_id,target_id,organization_id,attempt_number,status,provider_operation_id) VALUES($1,$2,$3,$4,'RUNNING',NULLIF($5,''))`, j.ID, j.TargetID, j.OrganizationID, j.Execution, j.ProviderOperationID); err != nil {
		return contentPublishJob{}, false, err
	}
	company := j.CompanyID
	auditMetadata := map[string]any{"execution": j.Execution}
	if previousStatus == "RUNNING" {
		auditMetadata["reclaimed"] = true
		switch {
		case j.CancellationRequested:
			auditMetadata["recoveryMode"] = "cancel"
		case j.ProviderOperationID != "":
			auditMetadata["recoveryMode"] = "poll"
		default:
			auditMetadata["recoveryMode"] = "fail_closed"
		}
	}
	if err = s.writeAudit(ctx, tx, auditRecord{OrganizationID: j.OrganizationID, CompanyID: &company, Action: auditSystemContentPublishAttempt, EntityType: "CONTENT_PUBLISH_JOB", EntityID: &j.ID, Metadata: auditMetadata}); err != nil {
		return contentPublishJob{}, false, err
	}
	return j, true, tx.Commit(ctx)
}

func (s *Server) executeContentPublish(ctx context.Context, j contentPublishJob) (PublishResult, error) {
	if !s.contentPlatformPublishingEnabled(strings.ToUpper(j.Platform)) {
		return PublishResult{}, &providerError{Platform: j.Platform, Kind: providerPermanent, Message: "publishing is disabled for this platform"}
	}
	if j.CancellationRequested {
		return PublishResult{}, &providerError{Platform: j.Platform, Kind: providerPermanent, Message: "publish was cancelled before lease recovery"}
	}
	if j.RecoveryUncertain {
		return PublishResult{}, &providerError{Platform: j.Platform, Kind: providerPermanent, Message: "previous worker expired without a durable provider operation; manual reconciliation is required"}
	}
	a := s.publishAdapters[strings.ToUpper(j.Platform)]
	if a == nil {
		return PublishResult{}, &providerError{Platform: j.Platform, Kind: providerRetryable, Message: "publish adapter is not configured"}
	}
	r := PublishRequest{JobID: j.ID, TargetID: j.TargetID, RevisionID: j.RevisionID, OrganizationID: j.OrganizationID, CompanyID: j.CompanyID, CreatorID: j.CreatorID, Platform: j.Platform, AccountID: j.AccountID, ProviderOperationID: j.ProviderOperationID, Snapshot: j.Snapshot}
	if r.ProviderOperationID != "" {
		result, err := a.Poll(ctx, r)
		if err == nil && (result.ExternalID != "" || result.Pending) {
			return result, nil
		}
		// A timeout/no-result after a known operation is uncertain. Never create
		// another provider object until Poll establishes that it is absent.
		if err != nil {
			return result, err
		}
		return PublishResult{}, &providerError{Platform: j.Platform, Kind: providerRetryable, Message: "provider operation has no terminal result"}
	}
	return a.Publish(ctx, r)
}

func (s *Server) finishContentPublish(ctx context.Context, j contentPublishJob, r PublishResult, runErr error) error {
	if r.ExternalID != "" {
		safeID, safeURL := sanitizeSucceededPublication(j.Platform, "SUCCEEDED", r.ExternalID, r.ExternalURL)
		if safeID == "" {
			r.ExternalID, r.ExternalURL = "", ""
			runErr = &providerError{Platform: j.Platform, Kind: providerSchema, Message: "provider returned an invalid public publication identifier"}
		} else {
			r.ExternalID, r.ExternalURL = safeID, safeURL
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	providerSucceeded := r.ExternalID != ""
	providerPending := r.Pending && (r.ProviderOperationID != "" || j.ProviderOperationID != "")
	if providerSucceeded || providerPending {
		err = lockRetainedCompanyForPublishResult(ctx, tx, j.OrganizationID, j.CompanyID)
	} else {
		err = lockActiveCompanyTarget(ctx, tx, j.OrganizationID, j.CompanyID)
	}
	if err != nil {
		return s.finishCancelledPublish(ctx, tx, j, err)
	}
	var id string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM content_publish_jobs WHERE id=$1 AND organization_id=$2 AND status='RUNNING' AND locked_by=$3 AND execution_count=$4 FOR UPDATE`, j.ID, j.OrganizationID, j.WorkerID, j.Execution).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return tx.Commit(ctx)
		}
		return err
	}
	var cancelled bool
	if err = tx.QueryRow(ctx, `SELECT cancellation_requested_at IS NOT NULL OR status='CANCELLED' FROM content_publish_targets WHERE id=$1 AND organization_id=$2 FOR UPDATE`, j.TargetID, j.OrganizationID).Scan(&cancelled); err != nil {
		return err
	}
	if cancelled && !providerSucceeded && !providerPending {
		return s.finishCancelledPublish(ctx, tx, j, nil)
	}
	if providerSucceeded {
		return s.finishSuccessfulPublish(ctx, tx, j, r)
	}
	if providerPending {
		return s.finishPendingPublish(ctx, tx, j, r)
	}
	// A transient Poll failure has not created a new publish attempt. Keep the
	// persisted operation and reschedule polling without touching retry budget.
	if j.ProviderOperationID != "" && isProviderKind(runErr, providerRetryable, providerRateLimit) {
		return s.finishPendingPublish(ctx, tx, j, r)
	}
	return s.finishFailedPublish(ctx, tx, j, r, runErr)
}

func lockRetainedCompanyForPublishResult(ctx context.Context, tx pgx.Tx, organizationID, companyID string) error {
	var locked string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM organizations WHERE id=$1 FOR KEY SHARE`, organizationID).Scan(&locked); err != nil {
		return err
	}
	return tx.QueryRow(ctx, `SELECT id::text FROM companies WHERE id=$1 AND organization_id=$2 FOR KEY SHARE`, companyID, organizationID).Scan(&locked)
}

// An accepted operation is persisted before releasing ownership. Polling does
// not consume the five network retry slots.
func (s *Server) finishPendingPublish(ctx context.Context, tx pgx.Tx, j contentPublishJob, r PublishResult) error {
	if _, err := tx.Exec(ctx, `UPDATE content_publish_jobs SET status='RETRY_SCHEDULED',run_at=$3,locked_by=NULL,locked_at=NULL,lease_expires_at=NULL,last_error=NULL,updated_at=now() WHERE id=$1 AND organization_id=$2`, j.ID, j.OrganizationID, s.now().UTC().Add(time.Minute)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE content_publish_targets SET status='PROCESSING',provider_operation_id=COALESCE(NULLIF($3,''),provider_operation_id),updated_at=now() WHERE id=$1 AND organization_id=$2`, j.TargetID, j.OrganizationID, r.ProviderOperationID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE content_publish_attempts SET status='PROCESSING',provider_operation_id=COALESCE(NULLIF($3,''),provider_operation_id),finished_at=now() WHERE job_id=$1 AND attempt_number=$2 AND status='RUNNING'`, j.ID, j.Execution, r.ProviderOperationID); err != nil {
		return err
	}
	if err := s.recomputeContentAggregate(ctx, tx, j.RevisionID, j.OrganizationID); err != nil {
		return err
	}
	return s.auditContentPublishAndCommit(ctx, tx, j, auditSystemContentPublishPending, map[string]any{"execution": j.Execution})
}
func (s *Server) finishCancelledPublish(ctx context.Context, tx pgx.Tx, j contentPublishJob, cause error) error {
	if _, err := tx.Exec(ctx, `UPDATE content_publish_jobs SET status='CANCELLED',locked_by=NULL,locked_at=NULL,lease_expires_at=NULL,updated_at=now() WHERE id=$1 AND organization_id=$2 AND status='RUNNING' AND locked_by=$3 AND execution_count=$4`, j.ID, j.OrganizationID, j.WorkerID, j.Execution); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE content_publish_targets SET status=CASE WHEN status='SUCCEEDED' THEN status ELSE 'CANCELLED' END,updated_at=now() WHERE id=$1 AND organization_id=$2`, j.TargetID, j.OrganizationID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE content_publish_attempts SET status='CANCELLED',finished_at=now(),error_message=COALESCE($3,error_message) WHERE job_id=$1 AND attempt_number=$2 AND status='RUNNING'`, j.ID, j.Execution, errorMessage(cause)); err != nil {
		return err
	}
	if err := s.recomputeContentAggregate(ctx, tx, j.RevisionID, j.OrganizationID); err != nil {
		return err
	}
	return s.auditContentPublishAndCommit(ctx, tx, j, auditSystemContentPublishCancelled, map[string]any{"execution": j.Execution})
}

// cancelContentPublishesForAssignmentTx is called before an active creator ↔
// platform assignment is revoked or moved. The assignment row and matching
// queue rows are changed in one transaction, so a future claim can never
// observe the old authorization. A currently owned execution is fenced with a
// cancellation marker; its heartbeat cancels provider context promptly.
func cancelContentPublishesForAssignmentTx(ctx context.Context, tx pgx.Tx, organizationID, creatorID, accountID string) error {
	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `UPDATE content_publish_jobs job SET status='CANCELLED',locked_by=NULL,locked_at=NULL,lease_expires_at=NULL,updated_at=$4 FROM content_publish_targets target WHERE job.target_id=target.id AND job.organization_id=$1 AND target.organization_id=$1 AND target.creator_id=$2 AND target.platform_account_id=$3 AND job.status IN ('READY','RETRY_SCHEDULED')`, organizationID, creatorID, accountID, now); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE content_publish_targets target SET cancellation_requested_at=COALESCE(cancellation_requested_at,$4),status=CASE WHEN EXISTS (SELECT 1 FROM content_publish_jobs job WHERE job.target_id=target.id AND job.organization_id=target.organization_id AND job.status='RUNNING') THEN target.status ELSE 'CANCELLED' END,updated_at=$4 WHERE target.organization_id=$1 AND target.creator_id=$2 AND target.platform_account_id=$3 AND target.status NOT IN ('SUCCEEDED','CANCELLED')`, organizationID, creatorID, accountID, now)
	return err
}

func cancelContentPublishesForAccountReassignmentTx(ctx context.Context, tx pgx.Tx, organizationID, accountID, keepCreatorID string) error {
	// Discovery is intentionally unlocked. Queue rows are fenced before callers
	// update the assignment, matching claim's company -> job -> target ->
	// assignment lock order and avoiding a revoke-vs-claim deadlock.
	rows, err := tx.Query(ctx, `SELECT creator_id::text FROM creator_account_assignments WHERE organization_id=$1 AND platform_account_id=$2 AND valid_to IS NULL AND (NULLIF($3,'')::uuid IS NULL OR creator_id<>NULLIF($3,'')::uuid)`, organizationID, accountID, keepCreatorID)
	if err != nil {
		return err
	}
	var creatorIDs []string
	for rows.Next() {
		var creatorID string
		if err = rows.Scan(&creatorID); err != nil {
			rows.Close()
			return err
		}
		creatorIDs = append(creatorIDs, creatorID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, creatorID := range creatorIDs {
		if err = cancelContentPublishesForAssignmentTx(ctx, tx, organizationID, creatorID, accountID); err != nil {
			return err
		}
	}
	return nil
}
func (s *Server) finishSuccessfulPublish(ctx context.Context, tx pgx.Tx, j contentPublishJob, r PublishResult) error {
	if _, err := tx.Exec(ctx, `UPDATE content_publish_jobs SET status='SUCCEEDED',locked_by=NULL,locked_at=NULL,lease_expires_at=NULL,last_error=NULL,updated_at=now() WHERE id=$1 AND organization_id=$2 AND status='RUNNING'`, j.ID, j.OrganizationID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE content_publish_targets SET status='SUCCEEDED',provider_operation_id=COALESCE(NULLIF($3,''),provider_operation_id),external_id=$4,external_url=$5,error_code=NULL,error_message=NULL,updated_at=now() WHERE id=$1 AND organization_id=$2`, j.TargetID, j.OrganizationID, r.ProviderOperationID, r.ExternalID, r.ExternalURL); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE content_publish_attempts SET status='SUCCEEDED',provider_operation_id=COALESCE(NULLIF($3,''),provider_operation_id),finished_at=now() WHERE job_id=$1 AND attempt_number=$2 AND status='RUNNING'`, j.ID, j.Execution, r.ProviderOperationID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO publications(organization_id,creator_id,platform_account_id,platform,external_id,publication_type,permalink,published_at,content_item_id,content_publish_target_id) SELECT $1,t.creator_id,t.platform_account_id,t.platform,$2,'VIDEO',$3,now(),r.content_item_id,t.id FROM content_publish_targets t JOIN content_revisions r ON r.id=t.content_revision_id WHERE t.id=$4 AND t.organization_id=$1 ON CONFLICT (content_publish_target_id) WHERE content_publish_target_id IS NOT NULL DO UPDATE SET external_id=excluded.external_id,permalink=excluded.permalink,updated_at=now()`, j.OrganizationID, r.ExternalID, r.ExternalURL, j.TargetID); err != nil {
		return err
	}
	if err := s.recomputeContentAggregate(ctx, tx, j.RevisionID, j.OrganizationID); err != nil {
		return err
	}
	if err := s.contentNotificationForRevision(ctx, tx, j.OrganizationID, j.CompanyID, j.RevisionID, "PUBLISH_SUCCEEDED", "publish-success:"+j.TargetID); err != nil {
		return err
	}
	return s.auditContentPublishAndCommit(ctx, tx, j, auditSystemContentPublishSucceeded, map[string]any{"execution": j.Execution})
}
func (s *Server) finishFailedPublish(ctx context.Context, tx pgx.Tx, j contentPublishJob, r PublishResult, runErr error) error {
	msg := errorMessage(runErr)
	auth := isProviderKind(runErr, providerAuth, providerPermission)
	retryable := isProviderKind(runErr, providerRetryable, providerRateLimit)
	status, targetStatus := "FAILED", "FAILED"
	count, runAt := j.Attempt+1, s.now().UTC()
	var deadline any
	if auth {
		status, targetStatus, count = "RETRY_SCHEDULED", "WAITING_FOR_REAUTH", j.Attempt
		deadline = runAt.Add(24 * time.Hour)
		runAt = deadline.(time.Time)
	} else if retryable && count <= len(contentRetryBackoff) {
		status, targetStatus = "RETRY_SCHEDULED", "RETRY_SCHEDULED"
		runAt = nextContentRetryAt(runAt, count, runErr)
	}
	if _, err := tx.Exec(ctx, `UPDATE content_publish_jobs SET status=$3,attempt_count=$4,run_at=$5,reauth_deadline_at=$6,locked_by=NULL,locked_at=NULL,lease_expires_at=NULL,last_error=$7,updated_at=now() WHERE id=$1 AND organization_id=$2`, j.ID, j.OrganizationID, status, count, runAt, deadline, msg); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE content_publish_targets SET status=$3,provider_operation_id=COALESCE(NULLIF($4,''),provider_operation_id),error_code=$5,error_message=$6,updated_at=now() WHERE id=$1 AND organization_id=$2`, j.TargetID, j.OrganizationID, targetStatus, r.ProviderOperationID, providerCode(runErr), msg); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE content_publish_attempts SET status=CASE WHEN $3 THEN 'WAITING_FOR_REAUTH' ELSE 'FAILED' END,provider_operation_id=COALESCE(NULLIF($4,''),provider_operation_id),error_code=$5,error_message=$6,finished_at=now() WHERE job_id=$1 AND attempt_number=$2 AND status='RUNNING'`, j.ID, j.Execution, auth, r.ProviderOperationID, providerCode(runErr), msg); err != nil {
		return err
	}
	if err := s.recomputeContentAggregate(ctx, tx, j.RevisionID, j.OrganizationID); err != nil {
		return err
	}
	kind, key := "PUBLISH_FAILED", "publish-failure:"+j.TargetID+":"+strconv.Itoa(j.Execution)
	if auth {
		kind, key = "REAUTH_REQUIRED", "reauth-required:"+j.TargetID
	}
	if err := s.contentNotificationForRevision(ctx, tx, j.OrganizationID, j.CompanyID, j.RevisionID, kind, key); err != nil {
		return err
	}
	action := auditSystemContentPublishFailed
	if auth {
		action = auditSystemContentPublishReauthRequired
	}
	return s.auditContentPublishAndCommit(ctx, tx, j, action, map[string]any{"execution": j.Execution, "retryCount": count})
}

func contentRetryDelay(attempt int, err error) time.Duration {
	if attempt < 1 || attempt > len(contentRetryBackoff) {
		return 0
	}
	delay := contentRetryBackoff[attempt-1]
	var pe *providerError
	if errors.As(err, &pe) && pe.RetryAfter > delay {
		return pe.RetryAfter
	}
	return delay
}

func nextContentRetryAt(now time.Time, attempt int, err error) time.Time {
	return now.Add(contentRetryDelay(attempt, err))
}
func (s *Server) auditContentPublishAndCommit(ctx context.Context, tx pgx.Tx, j contentPublishJob, action string, meta map[string]any) error {
	company := j.CompanyID
	if err := s.writeAudit(ctx, tx, auditRecord{OrganizationID: j.OrganizationID, CompanyID: &company, Action: action, EntityType: "CONTENT_PUBLISH_JOB", EntityID: &j.ID, Metadata: meta}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Server) recomputeContentAggregate(ctx context.Context, tx pgx.Tx, revisionID, org string) error {
	var state string
	if err := tx.QueryRow(ctx, `SELECT CASE WHEN count(*) FILTER (WHERE status='SUCCEEDED')=count(*) THEN 'PUBLISHED' WHEN count(*) FILTER (WHERE status='SUCCEEDED')>0 THEN 'PARTIALLY_PUBLISHED' WHEN count(*) FILTER (WHERE status IN ('PUBLISHING','PROCESSING','CLAIMED','READY','RETRY_SCHEDULED','WAITING_FOR_REAUTH','SCHEDULED'))>0 THEN 'PUBLISHING' WHEN count(*) FILTER (WHERE status='CANCELLED')=count(*) THEN 'CANCELLED' ELSE 'FAILED' END FROM content_publish_targets WHERE content_revision_id=$1 AND organization_id=$2`, revisionID, org).Scan(&state); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE content_revisions SET status=$3 WHERE id=$1 AND organization_id=$2`, revisionID, org, state)
	return err
}
func errorMessage(err error) any {
	if err == nil {
		return nil
	}
	var providerFailure *providerError
	if errors.As(err, &providerFailure) && strings.EqualFold(providerFailure.Platform, "TikTok") {
		// Defense in depth for custom/test adapters: provider-controlled TikTok
		// text is never persisted even if an adapter forgot to sanitize it.
		return safeTikTokProviderMessage(providerFailure.Kind)
	}
	s := err.Error()
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}
func providerCode(err error) any {
	var e *providerError
	if errors.As(err, &e) {
		return string(e.Kind)
	}
	return nil
}

func (s *Server) ExpireContentReauth(ctx context.Context) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `UPDATE content_publish_jobs j SET status='FAILED',locked_by=NULL,locked_at=NULL,lease_expires_at=NULL,last_error='reauth deadline expired',updated_at=now() FROM content_publish_targets t WHERE j.target_id=t.id AND j.status='RETRY_SCHEDULED' AND t.status='WAITING_FOR_REAUTH' AND j.reauth_deadline_at<=now() RETURNING j.id::text,j.target_id::text,j.content_revision_id::text,j.organization_id::text,j.company_id::text`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var jobs []contentPublishJob
	for rows.Next() {
		var j contentPublishJob
		if err := rows.Scan(&j.ID, &j.TargetID, &j.RevisionID, &j.OrganizationID, &j.CompanyID); err != nil {
			return 0, err
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, j := range jobs {
		if _, err = tx.Exec(ctx, `UPDATE content_publish_targets SET status='FAILED',error_code='reauth_expired',error_message='reauth deadline expired',updated_at=now() WHERE id=$1 AND organization_id=$2`, j.TargetID, j.OrganizationID); err != nil {
			return 0, err
		}
		if err = s.recomputeContentAggregate(ctx, tx, j.RevisionID, j.OrganizationID); err != nil {
			return 0, err
		}
		company := j.CompanyID
		if err = s.writeAudit(ctx, tx, auditRecord{OrganizationID: j.OrganizationID, CompanyID: &company, Action: auditSystemContentPublishFailed, EntityType: "CONTENT_PUBLISH_JOB", EntityID: &j.ID, Metadata: map[string]any{"reason": "reauth_expired"}}); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(jobs), nil
}

// The worker invokes this before claims; callbacks may call it immediately after a verified reconnect.
func (s *Server) ResumeContentPublishAfterReconnect(ctx context.Context) (int, error) {
	// Deliberately no global wake-up: an ACTIVE account alone is not evidence
	// that this waiting target was reconnected. OAuth invokes the account-scoped
	// variant only after verified identity and credential persistence.
	return 0, nil
}

// ResumeContentPublishAfterReconnectForAccount is safe for OAuth callbacks:
// it wakes only jobs whose target points to the verified provider account.
func (s *Server) ResumeContentPublishAfterReconnectForAccount(ctx context.Context, organizationID, accountID string) (int, error) {
	if organizationID == "" || accountID == "" {
		return 0, nil
	}
	return s.resumeContentPublishAfterReconnect(ctx, organizationID, accountID)
}

func (s *Server) resumeContentPublishAfterReconnect(ctx context.Context, organizationID, accountID string) (int, error) {
	tag, err := s.pool.Exec(ctx, `WITH eligible AS (SELECT j.id AS job_id,t.id AS target_id,t.organization_id FROM content_publish_jobs j JOIN content_publish_targets t ON t.id=j.target_id AND t.organization_id=j.organization_id JOIN platform_accounts a ON a.id=t.platform_account_id AND a.organization_id=t.organization_id JOIN companies c ON c.id=j.company_id AND c.organization_id=j.organization_id JOIN organizations o ON o.id=j.organization_id WHERE j.status='RETRY_SCHEDULED' AND t.status='WAITING_FOR_REAUTH' AND j.reauth_deadline_at>now() AND a.status='ACTIVE' AND c.lifecycle_state='ACTIVE' AND c.archived_at IS NULL AND o.lifecycle_state='ACTIVE' AND (NULLIF($1,'')::uuid IS NULL OR (t.organization_id=NULLIF($1,'')::uuid AND t.platform_account_id=NULLIF($2,'')::uuid)) FOR UPDATE OF j,t SKIP LOCKED), resumed_targets AS (UPDATE content_publish_targets t SET status='READY',error_code=NULL,error_message=NULL,updated_at=now() FROM eligible e WHERE t.id=e.target_id AND t.organization_id=e.organization_id RETURNING e.job_id) UPDATE content_publish_jobs j SET status='READY',run_at=now(),locked_by=NULL,locked_at=NULL,lease_expires_at=NULL,last_error=NULL,updated_at=now() FROM resumed_targets r WHERE j.id=r.job_id`, organizationID, accountID)
	return int(tag.RowsAffected()), err
}
