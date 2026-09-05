package httpserver

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

const (
	lifecycleLeaseDuration  = 2 * time.Minute
	lifecycleRevokeDeadline = 5 * time.Second
)

type lifecycleClaim struct {
	ID             string
	OrganizationID string
	LeaseOwner     string
	ReceiptID      string
}

type lifecycleToken struct {
	Platform      string
	Value         string
	FacebookLogin bool
}

// RunLifecycle processes immediate workspace deletions before retained company
// purges. Claims are short transactions; provider network calls never hold a
// database lock. Expired leases make a crash recoverable.
func (s *Server) RunLifecycle(ctx context.Context, workerID string, limit int) (int, error) {
	if err := s.cleanupOAuthStates(ctx, 100); err != nil {
		return 0, err
	}
	if strings.TrimSpace(workerID) == "" {
		workerID = makeToken()
	}
	if limit <= 0 {
		limit = 20
	}
	processed := 0
	var firstErr error
	for processed < limit {
		claim, ok, err := s.claimDeletingWorkspace(ctx, workerID)
		if err != nil {
			return processed, err
		}
		if ok {
			processed++
			if err = s.finishWorkspaceDeletion(ctx, claim); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		claim, ok, err = s.claimExpiredCompany(ctx, workerID)
		if err != nil {
			return processed, err
		}
		if !ok {
			break
		}
		processed++
		if err = s.finishCompanyPurge(ctx, claim); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return processed, firstErr
}

func (s *Server) claimExpiredCompany(ctx context.Context, workerID string) (lifecycleClaim, bool, error) {
	now := s.now().UTC()
	leaseUntil := now.Add(lifecycleLeaseDuration)
	var claim lifecycleClaim
	err := s.pool.QueryRow(ctx, `
		WITH candidate AS (
		  SELECT id FROM companies
		  WHERE lifecycle_state='ARCHIVED' AND purge_at<=$1
		    AND (purge_lease_until IS NULL OR purge_lease_until<=$1)
		  ORDER BY purge_at,id
		  FOR UPDATE SKIP LOCKED
		  LIMIT 1
		)
		UPDATE companies company
		SET purge_lease_owner=$2,purge_lease_until=$3
		FROM candidate
		WHERE company.id=candidate.id
		RETURNING company.id::text,company.organization_id::text
	`, now, workerID, leaseUntil).Scan(&claim.ID, &claim.OrganizationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return lifecycleClaim{}, false, nil
	}
	if err != nil {
		return lifecycleClaim{}, false, err
	}
	claim.LeaseOwner = workerID
	return claim, true, nil
}

func (s *Server) claimDeletingWorkspace(ctx context.Context, workerID string) (lifecycleClaim, bool, error) {
	now := s.now().UTC()
	leaseUntil := now.Add(lifecycleLeaseDuration)
	var claim lifecycleClaim
	err := s.pool.QueryRow(ctx, `
		WITH candidate AS (
		  SELECT id FROM organizations
		  WHERE lifecycle_state='DELETING'
		    AND (delete_lease_until IS NULL OR delete_lease_until<=$1)
		  ORDER BY deleting_at,id
		  FOR UPDATE SKIP LOCKED
		  LIMIT 1
		)
		UPDATE organizations workspace
		SET delete_lease_owner=$2,delete_lease_until=$3
		FROM candidate
		WHERE workspace.id=candidate.id
		RETURNING workspace.id::text,workspace.id::text,workspace.deletion_receipt_id::text
	`, now, workerID, leaseUntil).Scan(&claim.ID, &claim.OrganizationID, &claim.ReceiptID)
	if errors.Is(err, pgx.ErrNoRows) {
		return lifecycleClaim{}, false, nil
	}
	if err != nil {
		return lifecycleClaim{}, false, err
	}
	claim.LeaseOwner = workerID
	return claim, true, nil
}

func (s *Server) finishCompanyPurge(ctx context.Context, claim lifecycleClaim) error {
	tokens, _ := s.lifecycleTokens(ctx, claim.OrganizationID, &claim.ID)
	s.bestEffortRevoke(ctx, tokens)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var companyID string
	err = tx.QueryRow(ctx, `
		SELECT id::text FROM companies
		WHERE id=$1 AND organization_id=$2 AND lifecycle_state='ARCHIVED'
		  AND purge_at<=$3 AND purge_lease_owner=$4 AND purge_lease_until>$3
		FOR UPDATE`, claim.ID, claim.OrganizationID, s.now().UTC(), claim.LeaseOwner).Scan(&companyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if err = s.writeAudit(ctx, tx, auditRecord{
		OrganizationID: claim.OrganizationID, CompanyID: &companyID,
		Action: auditSystemPurgeCompany, EntityType: "COMPANY", EntityID: &companyID,
		Metadata: map[string]any{},
	}); err != nil {
		return err
	}
	if err = purgeCompanyContentDomain(ctx, tx, companyID, claim.OrganizationID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM companies WHERE id=$1 AND organization_id=$2`, companyID, claim.OrganizationID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO operational_receipts(operation,status,requested_at,completed_at,metadata)
		VALUES('COMPANY_PURGE','COMPLETE',$1,$1,jsonb_build_object('outcome','COMPLETE'))
	`, s.now().UTC()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// purgeCompanyContentDomain removes the content subsystem explicitly before
// the retained company row is deleted. These composite tenant FKs are
// deliberately restrictive during normal operation; the lifecycle purge is
// the one authorized path that tears the complete company graph down.
func purgeCompanyContentDomain(ctx context.Context, tx pgx.Tx, companyID, organizationID string) error {
	if err := enqueueCompanyMediaCleanup(ctx, tx, companyID, organizationID); err != nil {
		return fmt.Errorf("purge company content domain: %w", err)
	}
	statements := []string{
		`DELETE FROM content_notifications WHERE company_id=$1 AND organization_id=$2`,
		`DELETE FROM content_publish_attempts attempt USING content_publish_targets target WHERE attempt.target_id=target.id AND attempt.organization_id=target.organization_id AND target.company_id=$1 AND target.organization_id=$2`,
		`DELETE FROM content_publish_jobs WHERE company_id=$1 AND organization_id=$2`,
		`DELETE FROM content_approval_requests request USING content_items item WHERE request.content_item_id=item.id AND request.organization_id=item.organization_id AND item.company_id=$1 AND item.organization_id=$2`,
		`DELETE FROM content_publish_targets WHERE company_id=$1 AND organization_id=$2`,
		`DELETE FROM media_upload_sessions session USING media_assets media WHERE session.media_asset_id=media.id AND session.organization_id=media.organization_id AND media.company_id=$1 AND media.organization_id=$2`,
		`DELETE FROM media_assets WHERE company_id=$1 AND organization_id=$2`,
		`DELETE FROM content_items WHERE company_id=$1 AND organization_id=$2`,
		`DELETE FROM creator_content_approval_policies WHERE company_id=$1 AND organization_id=$2`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement, companyID, organizationID); err != nil {
			return fmt.Errorf("purge company content domain: %w", err)
		}
	}
	return nil
}

// enqueueCompanyMediaCleanup is the durable remote-delete boundary for a
// company purge. The task rows intentionally do not reference company,
// creator, media, or workspace rows, so the following local cascade cannot
// erase the only record of an unconfirmed S3 operation.
func enqueueCompanyMediaCleanup(ctx context.Context, tx pgx.Tx, companyID, organizationID string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO media_cleanup_tasks(task_key,organization_id,former_company_id,former_creator_id,former_media_asset_id,kind,object_key,multipart_upload_id)
		SELECT 'abort:'||session.id::text,media.organization_id,media.company_id,media.creator_id,media.id,
		       'ABORT_MULTIPART',media.object_key,session.multipart_upload_id
		FROM media_upload_sessions session
		JOIN media_assets media ON media.id=session.media_asset_id AND media.organization_id=session.organization_id
		WHERE media.company_id=$1 AND media.organization_id=$2 AND session.status='ACTIVE'
		ON CONFLICT(task_key) DO NOTHING`, companyID, organizationID); err != nil {
		return fmt.Errorf("enqueue multipart aborts: %w", err)
	}
	// Temporary validation sources are per-upload and must be removed even when
	// the immutable object is shared by another company.
	if _, err := tx.Exec(ctx, `
		INSERT INTO media_cleanup_tasks(task_key,organization_id,former_company_id,former_creator_id,former_media_asset_id,kind,object_key)
		SELECT 'delete-object:'||(media.metadata->>'temporaryObjectKey'),media.organization_id,media.company_id,media.creator_id,media.id,
		       'DELETE_OBJECT',media.metadata->>'temporaryObjectKey'
		FROM media_assets media
		WHERE media.company_id=$1 AND media.organization_id=$2
		  AND COALESCE(media.metadata->>'temporaryObjectKey','')<>''
		ON CONFLICT(task_key) DO NOTHING`, companyID, organizationID); err != nil {
		return fmt.Errorf("enqueue temporary object deletes: %w", err)
	}
	// Immutable content-addressed objects may be referenced by a different
	// company. Delete a key only when every local reference belongs to the graph
	// being purged. DISTINCT plus the key-derived task identity prevents double
	// deletion for deduplicated assets inside this company.
	if _, err := tx.Exec(ctx, `
		INSERT INTO media_cleanup_tasks(task_key,organization_id,former_company_id,former_media_asset_id,kind,object_key)
		SELECT 'delete-object:'||media.object_key,
		       media.organization_id,media.company_id,min(media.id::text)::uuid,'DELETE_OBJECT',media.object_key
		FROM media_assets media
		WHERE media.company_id=$1 AND media.organization_id=$2 AND media.object_key IS NOT NULL
		  AND NOT EXISTS (
		    SELECT 1 FROM media_assets other
		    WHERE other.object_key=media.object_key
		      AND (other.organization_id<>$2 OR other.company_id<>$1) AND other.status<>'DELETED'
		  )
		GROUP BY media.organization_id,media.company_id,media.object_key
		ON CONFLICT(task_key) DO NOTHING`, companyID, organizationID); err != nil {
		return fmt.Errorf("enqueue media object deletes: %w", err)
	}
	return nil
}

// enqueueWorkspaceMediaCleanup records every remote mutation before the
// workspace graph is removed. Immutable keys are global/content-addressed, so
// a key still referenced by another workspace is deliberately left for the
// last workspace that owns it. The object-key task identity also deduplicates
// multiple media rows and earlier creator/company cleanup requests.
func enqueueWorkspaceMediaCleanup(ctx context.Context, tx pgx.Tx, organizationID string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO media_cleanup_tasks(task_key,organization_id,former_company_id,former_creator_id,former_media_asset_id,kind,object_key,multipart_upload_id)
		SELECT 'abort:'||session.id::text,media.organization_id,media.company_id,media.creator_id,media.id,
		       'ABORT_MULTIPART',media.object_key,session.multipart_upload_id
		FROM media_upload_sessions session
		JOIN media_assets media ON media.id=session.media_asset_id AND media.organization_id=session.organization_id
		WHERE media.organization_id=$1 AND session.status='ACTIVE'
		ON CONFLICT(task_key) DO NOTHING`, organizationID); err != nil {
		return fmt.Errorf("enqueue workspace multipart aborts: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO media_cleanup_tasks(task_key,organization_id,former_company_id,former_creator_id,former_media_asset_id,kind,object_key)
		SELECT DISTINCT ON (media.metadata->>'temporaryObjectKey')
		       'delete-object:'||(media.metadata->>'temporaryObjectKey'),media.organization_id,media.company_id,media.creator_id,media.id,
		       'DELETE_OBJECT',media.metadata->>'temporaryObjectKey'
		FROM media_assets media
		WHERE media.organization_id=$1 AND COALESCE(media.metadata->>'temporaryObjectKey','')<>''
		ORDER BY media.metadata->>'temporaryObjectKey',media.id
		ON CONFLICT(task_key) DO NOTHING`, organizationID); err != nil {
		return fmt.Errorf("enqueue workspace temporary object deletes: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO media_cleanup_tasks(task_key,organization_id,former_company_id,former_media_asset_id,kind,object_key)
		SELECT 'delete-object:'||media.object_key,media.organization_id,min(media.company_id::text)::uuid,
		       min(media.id::text)::uuid,'DELETE_OBJECT',media.object_key
		FROM media_assets media
		WHERE media.organization_id=$1 AND media.object_key IS NOT NULL
		  AND NOT EXISTS (
		    SELECT 1 FROM media_assets other
		    WHERE other.organization_id<>$1 AND other.object_key=media.object_key AND other.status<>'DELETED'
		  )
		GROUP BY media.organization_id,media.object_key
		ON CONFLICT(task_key) DO NOTHING`, organizationID); err != nil {
		return fmt.Errorf("enqueue workspace media object deletes: %w", err)
	}
	return nil
}

func purgeWorkspaceContentDomain(ctx context.Context, tx pgx.Tx, organizationID string) error {
	if err := enqueueWorkspaceMediaCleanup(ctx, tx, organizationID); err != nil {
		return err
	}
	statements := []string{
		`DELETE FROM content_notifications WHERE organization_id=$1`,
		`DELETE FROM content_publish_attempts WHERE organization_id=$1`,
		`DELETE FROM content_publish_jobs WHERE organization_id=$1`,
		`DELETE FROM content_approval_requests WHERE organization_id=$1`,
		`DELETE FROM content_publish_targets WHERE organization_id=$1`,
		`DELETE FROM media_upload_sessions WHERE organization_id=$1`,
		`DELETE FROM media_assets WHERE organization_id=$1`,
		`DELETE FROM content_items WHERE organization_id=$1`,
		`DELETE FROM creator_content_approval_policies WHERE organization_id=$1`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement, organizationID); err != nil {
			return fmt.Errorf("purge workspace content domain: %w", err)
		}
	}
	return nil
}

// purgeActorMediaUploadSessions removes the user FK only after active remote
// uploads have a durable abort receipt. This is needed when one of several
// owners self-deletes while another owner concurrently transitions the whole
// workspace to DELETING.
func purgeActorMediaUploadSessions(ctx context.Context, tx pgx.Tx, organizationID, actorID string, now time.Time) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO media_cleanup_tasks(task_key,organization_id,former_company_id,former_creator_id,former_media_asset_id,kind,object_key,multipart_upload_id)
		SELECT 'abort:'||session.id::text,media.organization_id,media.company_id,media.creator_id,media.id,
		       'ABORT_MULTIPART',media.object_key,session.multipart_upload_id
		FROM media_upload_sessions session
		JOIN media_assets media ON media.id=session.media_asset_id AND media.organization_id=session.organization_id
		WHERE session.organization_id=$1 AND session.actor_id=$2 AND session.status='ACTIVE'
		ON CONFLICT(task_key) DO NOTHING`, organizationID, actorID); err != nil {
		return fmt.Errorf("enqueue actor multipart aborts: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE media_assets media SET status='DELETE_PENDING',delete_after=$3,cleanup_worker_id=NULL,cleanup_lease_until=NULL
		WHERE media.organization_id=$1 AND media.status<>'DELETED'
		  AND EXISTS (SELECT 1 FROM media_upload_sessions session WHERE session.media_asset_id=media.id AND session.organization_id=media.organization_id AND session.actor_id=$2 AND session.status='ACTIVE')
		  AND NOT EXISTS (SELECT 1 FROM content_publish_targets target WHERE target.media_asset_id=media.id AND target.organization_id=media.organization_id)`, organizationID, actorID, now); err != nil {
		return fmt.Errorf("mark actor upload media cleanup: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM media_upload_sessions WHERE organization_id=$1 AND actor_id=$2`, organizationID, actorID); err != nil {
		return fmt.Errorf("remove actor upload sessions: %w", err)
	}
	return nil
}

// markArchivedMediaCleanup keeps immutable media that is still referenced by
// publishing history. Incomplete/unreferenced assets and active multipart
// sessions remain as durable local work for the regular fenced cleanup worker,
// which only removes their rows' remote state after S3 confirms success.
func markArchivedMediaCleanup(ctx context.Context, tx pgx.Tx, organizationID string, companyID, creatorID *string, now time.Time) error {
	if _, err := tx.Exec(ctx, `
		UPDATE media_upload_sessions session SET expires_at=LEAST(session.expires_at,$4)
		FROM media_assets media
		WHERE session.media_asset_id=media.id AND session.organization_id=media.organization_id
		  AND media.organization_id=$1 AND ($2::uuid IS NULL OR media.company_id=$2)
		  AND ($3::uuid IS NULL OR media.creator_id=$3) AND session.status='ACTIVE'`, organizationID, companyID, creatorID, now); err != nil {
		return fmt.Errorf("expire archived multipart uploads: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE media_assets media
		SET status='DELETE_PENDING',delete_after=$4,cleanup_worker_id=NULL,cleanup_lease_until=NULL
		WHERE media.organization_id=$1 AND ($2::uuid IS NULL OR media.company_id=$2)
		  AND ($3::uuid IS NULL OR media.creator_id=$3)
		  AND media.status<>'DELETED'
		  AND (media.status<>'READY' OR NOT EXISTS (
		    SELECT 1 FROM content_publish_targets target
		    WHERE target.media_asset_id=media.id AND target.organization_id=media.organization_id
		  ))`, organizationID, companyID, creatorID, now); err != nil {
		return fmt.Errorf("mark archived media cleanup: %w", err)
	}
	return nil
}

// tombstoneCreatorContentDomain removes mutable publishing state while keeping
// successful publication analytics. Media objects are handed to the existing
// durable cleanup worker via DELETE_PENDING; object keys are never discarded
// here before S3 deletion is confirmed.
func tombstoneCreatorContentDomain(ctx context.Context, tx pgx.Tx, creatorID, organizationID string) error {
	statements := []string{
		`UPDATE media_upload_sessions session SET expires_at=LEAST(expires_at,now()) FROM media_assets media WHERE session.media_asset_id=media.id AND session.organization_id=media.organization_id AND media.creator_id=$1 AND media.organization_id=$2 AND session.status='ACTIVE'`,
		`UPDATE media_assets media SET status=CASE WHEN status='DELETED' OR (status='READY' AND EXISTS (SELECT 1 FROM content_publish_targets target WHERE target.media_asset_id=media.id AND target.organization_id=media.organization_id AND target.status='SUCCEEDED')) THEN status ELSE 'DELETE_PENDING' END,delete_after=CASE WHEN status='DELETED' OR (status='READY' AND EXISTS (SELECT 1 FROM content_publish_targets target WHERE target.media_asset_id=media.id AND target.organization_id=media.organization_id AND target.status='SUCCEEDED')) THEN delete_after ELSE now() END,original_filename='deleted-media',rejected_reason=NULL WHERE creator_id=$1 AND organization_id=$2`,
		`DELETE FROM content_notifications notification USING content_items item WHERE notification.content_item_id=item.id AND notification.organization_id=item.organization_id AND item.creator_id=$1 AND item.organization_id=$2`,
		`DELETE FROM content_publish_attempts attempt USING content_publish_targets target WHERE attempt.target_id=target.id AND attempt.organization_id=target.organization_id AND target.creator_id=$1 AND target.organization_id=$2`,
		`DELETE FROM content_publish_jobs job USING content_publish_targets target WHERE job.target_id=target.id AND job.organization_id=target.organization_id AND target.creator_id=$1 AND target.organization_id=$2`,
		`DELETE FROM content_approval_requests request USING content_items item WHERE request.content_item_id=item.id AND request.organization_id=item.organization_id AND item.creator_id=$1 AND item.organization_id=$2`,
		`DELETE FROM content_publish_targets WHERE creator_id=$1 AND organization_id=$2`,
		`DELETE FROM content_items WHERE creator_id=$1 AND organization_id=$2`,
		`DELETE FROM creator_content_approval_policies WHERE creator_id=$1 AND organization_id=$2`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement, creatorID, organizationID); err != nil {
			return fmt.Errorf("tombstone creator content domain: %w", err)
		}
	}
	return nil
}

func (s *Server) finishWorkspaceDeletion(ctx context.Context, claim lifecycleClaim) error {
	tokens, _ := s.lifecycleTokens(ctx, claim.OrganizationID, nil)
	s.bestEffortRevoke(ctx, tokens)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var receiptID string
	err = tx.QueryRow(ctx, `
		SELECT deletion_receipt_id::text FROM organizations
		WHERE id=$1 AND lifecycle_state='DELETING'
		  AND delete_lease_owner=$2 AND delete_lease_until>$3
		FOR UPDATE`, claim.OrganizationID, claim.LeaseOwner, s.now().UTC()).Scan(&receiptID)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	// This record intentionally disappears in the workspace cascade. The
	// anonymous operational receipt below is the only surviving evidence.
	if err = s.writeAudit(ctx, tx, auditRecord{
		OrganizationID: claim.OrganizationID, Action: auditSystemPurgeWorkspace,
		EntityType: "WORKSPACE", EntityID: &claim.OrganizationID, Metadata: map[string]any{},
	}); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE operational_receipts
		SET status='COMPLETE',completed_at=$2,metadata=jsonb_build_object('outcome','COMPLETE')
		WHERE id=$1 AND status='PENDING'`, receiptID, s.now().UTC()); err != nil {
		return err
	}
	if err = purgeWorkspaceContentDomain(ctx, tx, claim.OrganizationID); err != nil {
		return err
	}
	// users are global login rows rather than tenant-owned rows. The one-
	// workspace membership invariant makes this delete exact and safe.
	if _, err = tx.Exec(ctx, `
		DELETE FROM users user_row
		WHERE EXISTS(
		  SELECT 1 FROM organization_memberships membership
		  WHERE membership.organization_id=$1 AND membership.user_id=user_row.id
		)`, claim.OrganizationID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM organizations WHERE id=$1`, claim.OrganizationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Server) lifecycleTokens(ctx context.Context, organizationID string, companyID *string) ([]lifecycleToken, error) {
	if s.envelope == nil {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT account.platform::text,
		       connection.access_token_ciphertext,connection.access_token_nonce,
		       connection.refresh_token_ciphertext,connection.refresh_token_nonce,
		       COALESCE(account.metadata->>'connectionMode','')='FACEBOOK',
		       CASE
		         WHEN COALESCE(account.metadata->>'connectionMode','')<>'FACEBOOK'
		           OR COALESCE(account.metadata->>'facebookUserId','')='' THEN true
		         ELSE NOT EXISTS(
		           SELECT 1 FROM platform_accounts other
		   	   JOIN oauth_connections other_connection
		             ON other_connection.platform_account_id=other.id
		            AND other_connection.organization_id=other.organization_id
	           JOIN organizations other_workspace
	             ON other_workspace.id=other.organization_id
	            AND other_workspace.lifecycle_state='ACTIVE'
		           JOIN companies other_company
		             ON other_company.id=other.company_id
		            AND other_company.organization_id=other.organization_id
		           WHERE other.id<>account.id
		             AND other.platform='INSTAGRAM' AND other.status<>'DISCONNECTED'
		             AND COALESCE(other.metadata->>'connectionMode','')='FACEBOOK'
		             AND other.metadata->>'facebookUserId'=account.metadata->>'facebookUserId'
		             AND (other_company.lifecycle_state='ACTIVE' OR other_company.purge_at>$3)
		             AND (other.organization_id<>$1 OR ($2::uuid IS NOT NULL AND other.company_id<>$2))
		         )
		         AND NOT EXISTS(
		           SELECT 1 FROM platform_accounts due_other
		           JOIN oauth_connections due_connection
		             ON due_connection.platform_account_id=due_other.id
		            AND due_connection.organization_id=due_other.organization_id
		           JOIN companies due_company
		             ON due_company.id=due_other.company_id
		            AND due_company.organization_id=due_other.organization_id
		           JOIN organizations due_workspace
		             ON due_workspace.id=due_other.organization_id
		            AND due_workspace.lifecycle_state='ACTIVE'
		           WHERE due_other.id<account.id
		             AND due_other.platform='INSTAGRAM' AND due_other.status<>'DISCONNECTED'
		             AND COALESCE(due_other.metadata->>'connectionMode','')='FACEBOOK'
		             AND due_other.metadata->>'facebookUserId'=account.metadata->>'facebookUserId'
		             AND due_company.lifecycle_state='ARCHIVED' AND due_company.purge_at<=$3
		         )
		       END
		FROM oauth_connections connection
		JOIN platform_accounts account
		  ON account.id=connection.platform_account_id
		 AND account.organization_id=connection.organization_id
		WHERE connection.organization_id=$1
		  AND ($2::uuid IS NULL OR account.company_id=$2)
	`, organizationID, companyID, s.now().UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]lifecycleToken, 0)
	seen := make(map[[32]byte]struct{})
	for rows.Next() {
		var connection platformRevocationConnection
		if err = rows.Scan(&connection.platform, &connection.accessCipher, &connection.accessNonce,
			&connection.refreshCipher, &connection.refreshNonce, &connection.facebookLogin, &connection.revokeAllowed); err != nil {
			return nil, err
		}
		if !connection.revokeAllowed {
			continue
		}
		value := s.platformRevocationToken(connection)
		if value == "" {
			continue
		}
		digestInput := make([]byte, 0, len(connection.platform)+2+len(value))
		digestInput = append(digestInput, connection.platform...)
		digestInput = append(digestInput, 0)
		if connection.facebookLogin {
			digestInput = append(digestInput, 1)
		}
		digestInput = append(digestInput, value...)
		digest := sha256.Sum256(digestInput)
		if _, duplicate := seen[digest]; duplicate {
			continue
		}
		seen[digest] = struct{}{}
		result = append(result, lifecycleToken{Platform: connection.platform, Value: value, FacebookLogin: connection.facebookLogin})
	}
	return result, rows.Err()
}

// OAuth states contain encrypted PKCE material. They are short lived; clean a
// bounded batch on each lifecycle pass so callbacks and abandoned flows cannot
// retain credentials indefinitely.
func (s *Server) cleanupOAuthStates(ctx context.Context, limit int) error {
	if limit <= 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		DELETE FROM oauth_states
		WHERE id IN (
		  SELECT id FROM oauth_states
		  WHERE expires_at<=now() OR consumed_at IS NOT NULL
		  ORDER BY expires_at,id
		  FOR UPDATE SKIP LOCKED
		  LIMIT $1
		)`, limit)
	return err
}

func (s *Server) bestEffortRevoke(ctx context.Context, tokens []lifecycleToken) {
	timeout := s.lifecycleRevokeTimeout
	if timeout <= 0 {
		timeout = lifecycleRevokeDeadline
	}
	revokeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for _, token := range tokens {
		if revokeCtx.Err() != nil {
			return
		}
		_ = s.revokeLifecycleToken(revokeCtx, token.Platform, token.Value, token.FacebookLogin)
	}
}

func (s *Server) defaultLifecycleTokenRevoke(ctx context.Context, platform, token string, facebookLogin bool) error {
	if token == "" {
		return nil
	}
	if platform != "VK" {
		return s.revokePlatform(ctx, platform, token, facebookLogin)
	}
	values := url.Values{"token": {token}}
	endpoint := strings.TrimRight(s.config.VKOAuthBase, "/") + "/oauth2/revoke"
	values.Set("client_id", s.config.VKClientID)
	values.Set("client_secret", s.config.VKClientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := (&http.Client{
		Timeout:       lifecycleRevokeDeadline,
		CheckRedirect: rejectProviderRedirect,
	}).Do(req)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		// net/http wraps redirect-policy errors in url.Error, whose message
		// includes the request URL. Return only the sentinel so lifecycle
		// callers and logs cannot disclose even endpoint metadata.
		if errors.Is(err, errProviderRedirect) {
			return errProviderRedirect
		}
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("VK revoke returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (s *Server) deletionReceiptStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var operation, status string
	var requestedAt time.Time
	var completedAt *time.Time
	var metadata map[string]any
	err := s.pool.QueryRow(r.Context(), `
		SELECT operation,status,requested_at,completed_at,metadata
		FROM operational_receipts WHERE id=$1`, id).Scan(&operation, &status, &requestedAt, &completedAt, &metadata)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, http.StatusNotFound, "not found", "deletion receipt does not exist")
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "receipt failed", "could not load deletion receipt")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "operation": operation, "status": status,
		"requestedAt": requestedAt, "completedAt": completedAt, "metadata": metadata,
	})
}
