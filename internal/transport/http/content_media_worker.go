package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type mediaValidationJob struct {
	ID, OrganizationID, CompanyID, ObjectKey, ExpectedMIME, WorkerID string
	ExpectedBytes                                                    int64
	Generation                                                       int64
}

var errMediaLeaseLost = errors.New("media worker lease lost")

func leaseValue(configured, fallback time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	return fallback
}

type ffprobeOutput struct {
	Streams []struct {
		CodecType, CodecName string
		Width, Height        int
		BitRate              string `json:"bit_rate"`
	} `json:"streams"`
	Format struct{ Duration, BitRate string } `json:"format"`
}

func (s *Server) RunMediaValidation(ctx context.Context, workerID string, limit int) (int, error) {
	if !s.config.ContentPublishingEnabled {
		return 0, nil
	}
	// Validation materializes a complete source file in scratch space. Keep one
	// file per worker process and cap every file at the upload contract limit.
	if limit <= 0 || limit > 1 {
		limit = 1
	}
	if slots := s.mediaValidationSlots; slots != nil {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	processed := 0
	for processed < limit {
		job, ok, err := s.claimMediaValidation(ctx, workerID)
		if err != nil {
			return processed, err
		}
		if !ok {
			break
		}
		processed++
		err = s.withMediaValidationHeartbeat(ctx, job, func(workCtx context.Context) error {
			probe, validationErr := s.validateMedia(workCtx, job)
			return s.finishMediaValidation(workCtx, job, validationErr == nil, probe, validationErr)
		})
		if err != nil && !errors.Is(err, errMediaLeaseLost) && !errors.Is(err, context.Canceled) {
			continue
		}
	}
	return processed, nil
}
func (s *Server) claimMediaValidation(ctx context.Context, workerID string) (mediaValidationJob, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return mediaValidationJob{}, false, err
	}
	defer tx.Rollback(ctx)
	var job mediaValidationJob
	lease := leaseValue(s.mediaValidationLease, 10*time.Minute)
	err = tx.QueryRow(ctx, `WITH candidate AS (SELECT id FROM media_assets WHERE status='VALIDATING' AND (validation_lease_until IS NULL OR validation_lease_until<now()) ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE media_assets asset SET validation_worker_id=$1,validation_lease_until=now()+$2::interval,validation_generation=validation_generation+1 FROM candidate WHERE asset.id=candidate.id RETURNING asset.id::text,asset.organization_id::text,asset.company_id::text,asset.object_key,asset.detected_mime,asset.bytes,asset.validation_generation`, workerID, lease.String()).Scan(&job.ID, &job.OrganizationID, &job.CompanyID, &job.ObjectKey, &job.ExpectedMIME, &job.ExpectedBytes, &job.Generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return mediaValidationJob{}, false, tx.Commit(ctx)
	}
	if err != nil {
		return mediaValidationJob{}, false, err
	}
	job.WorkerID = workerID
	return job, true, tx.Commit(ctx)
}

func (s *Server) heartbeatMediaValidation(ctx context.Context, job mediaValidationJob) error {
	lease := leaseValue(s.mediaValidationLease, 10*time.Minute)
	result, err := s.pool.Exec(ctx, `UPDATE media_assets SET validation_lease_until=now()+$4::interval WHERE id=$1 AND organization_id=$2 AND status='VALIDATING' AND validation_worker_id=$3 AND validation_generation=$5 AND validation_lease_until>now()`, job.ID, job.OrganizationID, job.WorkerID, lease.String(), job.Generation)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errMediaLeaseLost
	}
	return nil
}

func (s *Server) withMediaValidationHeartbeat(ctx context.Context, job mediaValidationJob, work func(context.Context) error) error {
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	interval := leaseValue(s.mediaHeartbeatEvery, time.Minute)
	done := make(chan struct{})
	heartbeatErr := make(chan error, 1)
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-workCtx.Done():
				return
			case <-ticker.C:
				if err := s.heartbeatMediaValidation(workCtx, job); err != nil {
					heartbeatErr <- err
					cancel()
					return
				}
			}
		}
	}()
	err := work(workCtx)
	cancel()
	<-done
	select {
	case heartbeat := <-heartbeatErr:
		return heartbeat
	default:
		return err
	}
}

type mediaProbe struct {
	SHA           []byte
	MIME          string
	Bytes         int64
	Width, Height int
	DurationMS    int64
	Metadata      map[string]any
	HashKey       string
}

func (s *Server) validateMedia(ctx context.Context, job mediaValidationJob) (mediaProbe, error) {
	if job.ExpectedBytes <= 0 || job.ExpectedBytes > mediaUploadMaxBytes {
		return mediaProbe{}, fmt.Errorf("media exceeds validation scratch limit")
	}
	storage, err := s.mediaStorage(ctx)
	if err != nil {
		return mediaProbe{}, err
	}
	object, err := storage.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &storage.bucket, Key: &job.ObjectKey})
	if err != nil {
		return mediaProbe{}, fmt.Errorf("download media: %w", err)
	}
	defer object.Body.Close()
	dir, err := os.MkdirTemp("", "statzavod-media-")
	if err != nil {
		return mediaProbe{}, err
	}
	defer os.RemoveAll(dir)
	var disk syscall.Statfs_t
	if err = syscall.Statfs(dir, &disk); err != nil {
		return mediaProbe{}, fmt.Errorf("inspect validation scratch space: %w", err)
	}
	available := int64(disk.Bavail) * int64(disk.Bsize)
	if available < job.ExpectedBytes+(256<<20) {
		return mediaProbe{}, fmt.Errorf("insufficient validation scratch space")
	}
	filePath := filepath.Join(dir, "source")
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		return mediaProbe{}, err
	}
	hash := sha256.New()
	header := make([]byte, 512)
	n, readErr := io.ReadFull(object.Body, header)
	if readErr != nil && readErr != io.ErrUnexpectedEOF {
		file.Close()
		return mediaProbe{}, readErr
	}
	writer := io.MultiWriter(file, hash)
	if _, err = writer.Write(header[:n]); err == nil {
		_, err = io.Copy(writer, io.LimitReader(object.Body, job.ExpectedBytes+1))
	}
	closeErr := file.Close()
	if err != nil {
		return mediaProbe{}, err
	}
	if closeErr != nil {
		return mediaProbe{}, closeErr
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return mediaProbe{}, err
	}
	if info.Size() != job.ExpectedBytes {
		return mediaProbe{}, fmt.Errorf("uploaded size mismatch")
	}
	mime := http.DetectContentType(header[:n])
	if !strings.HasPrefix(mime, "video/") && mime != "application/octet-stream" {
		return mediaProbe{}, fmt.Errorf("file signature is not a supported video")
	}
	probe, err := runFFProbe(ctx, filePath)
	if err != nil {
		return mediaProbe{}, err
	}
	if err = validateVerticalVideo(probe); err == nil {
		probe.SHA = hash.Sum(nil)
		probe.MIME = mime
		probe.Bytes = info.Size()
		probe.HashKey = "media/" + hex.EncodeToString(probe.SHA) + ".mp4"
	} else {
		return mediaProbe{}, fmt.Errorf("video must be vertical (height greater than width)")
	}
	return probe, nil
}

func validateVerticalVideo(probe mediaProbe) error {
	if probe.Width <= 0 || probe.Height <= probe.Width {
		return fmt.Errorf("video must be vertical (height greater than width)")
	}
	return nil
}

type mediaCleanupJob struct {
	Kind, ID, OrganizationID, CompanyID, AssetID, ObjectKey, MultipartUploadID, OriginalStatus, WorkerID string
	Generation                                                                                           int64
}

// RunMediaCleanup aborts expired multipart uploads and removes objects whose
// seven-day retention elapsed. Each claim is leased, so a process crash is
// retried without letting two workers mutate the same S3 upload/object.
func (s *Server) RunMediaCleanup(ctx context.Context, workerID string, limit int) (int, error) {
	if !s.config.ContentPublishingEnabled {
		return 0, nil
	}
	if limit <= 0 {
		limit = 20
	}
	processed := 0
	for processed < limit {
		job, ok, err := s.claimMediaCleanup(ctx, workerID)
		if err != nil || !ok {
			return processed, err
		}
		processed++
		if err = s.withMediaCleanupHeartbeat(ctx, job, func(workCtx context.Context) error { return s.executeMediaCleanup(workCtx, job) }); err != nil {
			_ = s.releaseMediaCleanup(context.WithoutCancel(ctx), job)
		}
	}
	return processed, nil
}

func (s *Server) claimMediaCleanup(ctx context.Context, workerID string) (mediaCleanupJob, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return mediaCleanupJob{}, false, err
	}
	defer tx.Rollback(ctx)
	var job mediaCleanupJob
	lease := leaseValue(s.mediaCleanupLease, 10*time.Minute)
	err = tx.QueryRow(ctx, `WITH candidate AS (
  SELECT task.id FROM media_cleanup_tasks task
  WHERE task.status IN ('PENDING','RUNNING')
    AND (task.lease_until IS NULL OR task.lease_until<now())
  ORDER BY task.created_at,task.id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE media_cleanup_tasks task SET status='RUNNING',lease_worker_id=$1,lease_until=now()+$2::interval,
  generation=task.generation+1,attempts=task.attempts+1,last_error=NULL
FROM candidate WHERE task.id=candidate.id
RETURNING CASE task.kind WHEN 'ABORT_MULTIPART' THEN 'OUTBOX_MULTIPART' ELSE 'OUTBOX_OBJECT' END,
  task.id::text,task.organization_id::text,COALESCE(task.former_company_id::text,''),
  COALESCE(task.former_media_asset_id::text,''),task.object_key,COALESCE(task.multipart_upload_id,''),'',task.generation`, workerID, lease.String()).Scan(&job.Kind, &job.ID, &job.OrganizationID, &job.CompanyID, &job.AssetID, &job.ObjectKey, &job.MultipartUploadID, &job.OriginalStatus, &job.Generation)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `WITH candidate AS (
  SELECT session.id FROM media_upload_sessions session
  WHERE session.status='ACTIVE' AND session.expires_at<=now()
    AND (session.cleanup_lease_until IS NULL OR session.cleanup_lease_until<now())
  ORDER BY session.expires_at,session.id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE media_upload_sessions session SET cleanup_worker_id=$1,cleanup_lease_until=now()+$2::interval,cleanup_generation=session.cleanup_generation+1
FROM candidate,media_assets asset
WHERE session.id=candidate.id AND asset.id=session.media_asset_id AND asset.organization_id=session.organization_id
RETURNING 'MULTIPART',session.id::text,session.organization_id::text,asset.company_id::text,asset.id::text,asset.object_key,session.multipart_upload_id,'',session.cleanup_generation`, workerID, lease.String()).Scan(&job.Kind, &job.ID, &job.OrganizationID, &job.CompanyID, &job.AssetID, &job.ObjectKey, &job.MultipartUploadID, &job.OriginalStatus, &job.Generation)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `WITH candidate AS (
  SELECT asset.id FROM media_assets asset
  WHERE asset.status='READY' AND asset.metadata ? 'temporaryObjectKey'
    AND (asset.cleanup_lease_until IS NULL OR asset.cleanup_lease_until<now())
  ORDER BY asset.ready_at,asset.id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE media_assets asset SET cleanup_worker_id=$1,cleanup_lease_until=now()+$2::interval,cleanup_generation=asset.cleanup_generation+1
FROM candidate WHERE asset.id=candidate.id
RETURNING 'TEMP_OBJECT',asset.id::text,asset.organization_id::text,asset.company_id::text,asset.id::text,asset.metadata->>'temporaryObjectKey','','READY',asset.cleanup_generation`, workerID, lease.String()).Scan(&job.Kind, &job.ID, &job.OrganizationID, &job.CompanyID, &job.AssetID, &job.ObjectKey, &job.MultipartUploadID, &job.OriginalStatus, &job.Generation)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `WITH candidate AS (
  SELECT asset.id,asset.status FROM media_assets asset
  WHERE (
      (asset.status IN ('REJECTED','DELETE_PENDING') AND asset.delete_after<=now())
      OR (asset.status='READY' AND asset.created_at<=now()-interval '7 days')
    )
    AND asset.object_key IS NOT NULL
    AND NOT EXISTS (SELECT 1 FROM content_publish_targets target WHERE target.media_asset_id=asset.id)
    AND (asset.cleanup_lease_until IS NULL OR asset.cleanup_lease_until<now())
  ORDER BY COALESCE(asset.delete_after,asset.created_at),asset.id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE media_assets asset SET
  status='DELETE_PENDING',
  delete_after=COALESCE(asset.delete_after,now()),cleanup_worker_id=$1,cleanup_lease_until=now()+$2::interval,cleanup_generation=asset.cleanup_generation+1
FROM candidate WHERE asset.id=candidate.id
RETURNING 'OBJECT',asset.id::text,asset.organization_id::text,asset.company_id::text,asset.id::text,asset.object_key,'',candidate.status,asset.cleanup_generation`, workerID, lease.String()).Scan(&job.Kind, &job.ID, &job.OrganizationID, &job.CompanyID, &job.AssetID, &job.ObjectKey, &job.MultipartUploadID, &job.OriginalStatus, &job.Generation)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return mediaCleanupJob{}, false, tx.Commit(ctx)
	}
	if err != nil {
		return mediaCleanupJob{}, false, err
	}
	job.WorkerID = workerID
	if job.Kind == "OBJECT" {
		// The target trigger takes the same transaction lock before attaching
		// media. This closes the claim-vs-new-reference race.
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('media-asset:'||$1::text,0))`, job.AssetID); err != nil {
			return mediaCleanupJob{}, false, err
		}
		var referenced bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM content_publish_targets WHERE media_asset_id=$1)`, job.AssetID).Scan(&referenced); err != nil {
			return mediaCleanupJob{}, false, err
		}
		if referenced {
			return mediaCleanupJob{}, false, tx.Rollback(ctx)
		}
		if job.OriginalStatus != "DELETE_PENDING" {
			company, asset := job.CompanyID, job.AssetID
			reason := "retention elapsed"
			if job.OriginalStatus == "READY" {
				reason = "unreferenced for 7 days"
			}
			if err = s.writeAudit(ctx, tx, auditRecord{OrganizationID: job.OrganizationID, CompanyID: &company, Action: "SYSTEM_MEDIA_DELETE_PENDING", EntityType: "MEDIA_ASSET", EntityID: &asset, Metadata: map[string]any{"reason": reason, "previousStatus": job.OriginalStatus}}); err != nil {
				return mediaCleanupJob{}, false, err
			}
		}
	}
	return job, true, tx.Commit(ctx)
}

func (s *Server) heartbeatMediaCleanup(ctx context.Context, job mediaCleanupJob) error {
	lease := leaseValue(s.mediaCleanupLease, 10*time.Minute)
	var result pgconn.CommandTag
	var err error
	if strings.HasPrefix(job.Kind, "OUTBOX_") {
		result, err = s.pool.Exec(ctx, `UPDATE media_cleanup_tasks SET lease_until=now()+$4::interval WHERE id=$1 AND status='RUNNING' AND lease_worker_id=$2 AND generation=$3 AND lease_until>now()`, job.ID, job.WorkerID, job.Generation, lease.String())
	} else if job.Kind == "MULTIPART" {
		result, err = s.pool.Exec(ctx, `UPDATE media_upload_sessions SET cleanup_lease_until=now()+$5::interval WHERE id=$1 AND organization_id=$2 AND status='ACTIVE' AND cleanup_worker_id=$3 AND cleanup_generation=$4 AND cleanup_lease_until>now()`, job.ID, job.OrganizationID, job.WorkerID, job.Generation, lease.String())
	} else {
		result, err = s.pool.Exec(ctx, `UPDATE media_assets SET cleanup_lease_until=now()+$5::interval WHERE id=$1 AND organization_id=$2 AND cleanup_worker_id=$3 AND cleanup_generation=$4 AND cleanup_lease_until>now()`, job.AssetID, job.OrganizationID, job.WorkerID, job.Generation, lease.String())
	}
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errMediaLeaseLost
	}
	return nil
}

func (s *Server) withMediaCleanupHeartbeat(ctx context.Context, job mediaCleanupJob, work func(context.Context) error) error {
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	interval := leaseValue(s.mediaHeartbeatEvery, time.Minute)
	done := make(chan struct{})
	heartbeatErr := make(chan error, 1)
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-workCtx.Done():
				return
			case <-ticker.C:
				if err := s.heartbeatMediaCleanup(workCtx, job); err != nil {
					heartbeatErr <- err
					cancel()
					return
				}
			}
		}
	}()
	err := work(workCtx)
	cancel()
	<-done
	select {
	case heartbeat := <-heartbeatErr:
		return heartbeat
	default:
		return err
	}
}

func (s *Server) executeMediaCleanup(ctx context.Context, job mediaCleanupJob) error {
	storage, err := s.mediaStorage(ctx)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if strings.HasPrefix(job.Kind, "OUTBOX_") {
		var owned bool
		if err = tx.QueryRow(ctx, `SELECT true FROM media_cleanup_tasks WHERE id=$1 AND status='RUNNING' AND lease_worker_id=$2 AND generation=$3 AND lease_until>now() FOR UPDATE`, job.ID, job.WorkerID, job.Generation).Scan(&owned); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errMediaLeaseLost
			}
			return err
		}
		if job.Kind == "OUTBOX_MULTIPART" {
			if _, err = storage.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: &storage.bucket, Key: &job.ObjectKey, UploadId: &job.MultipartUploadID}); err != nil {
				var apiErr smithy.APIError
				if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "NoSuchUpload" {
					return fmt.Errorf("abort lifecycle multipart upload: %w", err)
				}
			}
		} else if _, err = storage.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &storage.bucket, Key: &job.ObjectKey}); err != nil {
			var apiErr smithy.APIError
			if !errors.As(err, &apiErr) || (apiErr.ErrorCode() != "NoSuchKey" && apiErr.ErrorCode() != "NotFound") {
				return fmt.Errorf("delete lifecycle media object: %w", err)
			}
		}
		result, updateErr := tx.Exec(ctx, `UPDATE media_cleanup_tasks SET status='COMPLETE',completed_at=now(),lease_worker_id=NULL,lease_until=NULL,last_error=NULL WHERE id=$1 AND status='RUNNING' AND lease_worker_id=$2 AND generation=$3 AND lease_until>now()`, job.ID, job.WorkerID, job.Generation)
		if updateErr != nil {
			return updateErr
		}
		if result.RowsAffected() != 1 {
			return errMediaLeaseLost
		}
		return tx.Commit(ctx)
	}
	// Keep the claimed row locked across the irreversible S3 request. A reclaim
	// must update this same row, so it cannot install a newer generation between
	// the ownership check and Abort/Delete.
	if job.Kind == "MULTIPART" {
		var owned bool
		if err = tx.QueryRow(ctx, `SELECT true FROM media_upload_sessions WHERE id=$1 AND organization_id=$2 AND status='ACTIVE' AND cleanup_worker_id=$3 AND cleanup_generation=$4 AND cleanup_lease_until>now() FOR UPDATE`, job.ID, job.OrganizationID, job.WorkerID, job.Generation).Scan(&owned); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errMediaLeaseLost
			}
			return err
		}
		if _, err = storage.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: &storage.bucket, Key: &job.ObjectKey, UploadId: &job.MultipartUploadID}); err != nil {
			var apiErr smithy.APIError
			// Abort may have succeeded just before a worker crash. S3 then returns
			// NoSuchUpload on the leased retry; that is the desired remote state.
			if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "NoSuchUpload" {
				return fmt.Errorf("abort expired multipart upload: %w", err)
			}
		}
	} else {
		var owned bool
		if err = tx.QueryRow(ctx, `SELECT true FROM media_assets WHERE id=$1 AND organization_id=$2 AND cleanup_worker_id=$3 AND cleanup_generation=$4 AND cleanup_lease_until>now() FOR UPDATE`, job.AssetID, job.OrganizationID, job.WorkerID, job.Generation).Scan(&owned); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errMediaLeaseLost
			}
			return err
		}
		if _, err = storage.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &storage.bucket, Key: &job.ObjectKey}); err != nil {
			return fmt.Errorf("delete expired media object: %w", err)
		}
	}
	action := "SYSTEM_MEDIA_DELETED"
	if job.Kind == "MULTIPART" {
		action = "SYSTEM_MEDIA_UPLOAD_EXPIRED"
		var result pgconn.CommandTag
		if result, err = tx.Exec(ctx, `UPDATE media_upload_sessions SET status='EXPIRED',cleanup_worker_id=NULL,cleanup_lease_until=NULL WHERE id=$1 AND organization_id=$2 AND status='ACTIVE' AND cleanup_worker_id=$3 AND cleanup_generation=$4 AND cleanup_lease_until>now()`, job.ID, job.OrganizationID, job.WorkerID, job.Generation); err == nil && result.RowsAffected() != 1 {
			err = errMediaLeaseLost
		}
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE media_assets SET status='DELETE_PENDING',delete_after=now()+interval '7 days' WHERE id=$1 AND organization_id=$2 AND status='UPLOADING'`, job.AssetID, job.OrganizationID)
		}
	} else if job.Kind == "TEMP_OBJECT" {
		action = "SYSTEM_MEDIA_DELETED"
		result, updateErr := tx.Exec(ctx, `UPDATE media_assets SET metadata=metadata-'temporaryObjectKey',cleanup_worker_id=NULL,cleanup_lease_until=NULL WHERE id=$1 AND organization_id=$2 AND status='READY' AND cleanup_worker_id=$3 AND cleanup_generation=$4 AND cleanup_lease_until>now()`, job.AssetID, job.OrganizationID, job.WorkerID, job.Generation)
		err = updateErr
		if err == nil && result.RowsAffected() != 1 {
			err = errMediaLeaseLost
		}
	} else {
		// Recheck references under the final mutation. If a legacy/direct DB
		// writer attached the asset after claim, never declare it deleted.
		var referenced bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM content_publish_targets WHERE media_asset_id=$1)`, job.AssetID).Scan(&referenced); err == nil && referenced {
			return fmt.Errorf("media asset became referenced during cleanup")
		}
		if err == nil {
			result, updateErr := tx.Exec(ctx, `UPDATE media_assets SET status='DELETED',object_key=NULL,cleanup_worker_id=NULL,cleanup_lease_until=NULL WHERE id=$1 AND organization_id=$2 AND status='DELETE_PENDING' AND cleanup_worker_id=$3 AND cleanup_generation=$4 AND cleanup_lease_until>now()`, job.AssetID, job.OrganizationID, job.WorkerID, job.Generation)
			err = updateErr
			if err == nil && result.RowsAffected() != 1 {
				err = errMediaLeaseLost
			}
		}
	}
	if err != nil {
		return err
	}
	company, asset := job.CompanyID, job.AssetID
	metadata := map[string]any{}
	if job.Kind == "TEMP_OBJECT" {
		metadata["objectKind"] = "temporary-source"
	}
	if err = s.writeAudit(ctx, tx, auditRecord{OrganizationID: job.OrganizationID, CompanyID: &company, Action: action, EntityType: "MEDIA_ASSET", EntityID: &asset, Metadata: metadata}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Server) releaseMediaCleanup(ctx context.Context, job mediaCleanupJob) error {
	if strings.HasPrefix(job.Kind, "OUTBOX_") {
		_, err := s.pool.Exec(ctx, `UPDATE media_cleanup_tasks SET status='PENDING',lease_worker_id=NULL,lease_until=NULL,last_error='remote cleanup attempt failed' WHERE id=$1 AND lease_worker_id=$2 AND generation=$3`, job.ID, job.WorkerID, job.Generation)
		return err
	}
	if job.Kind == "MULTIPART" {
		_, err := s.pool.Exec(ctx, `UPDATE media_upload_sessions SET cleanup_worker_id=NULL,cleanup_lease_until=NULL WHERE id=$1 AND organization_id=$2 AND cleanup_worker_id=$3 AND cleanup_generation=$4`, job.ID, job.OrganizationID, job.WorkerID, job.Generation)
		return err
	}
	_, err := s.pool.Exec(ctx, `UPDATE media_assets SET cleanup_worker_id=NULL,cleanup_lease_until=NULL WHERE id=$1 AND organization_id=$2 AND cleanup_worker_id=$3 AND cleanup_generation=$4`, job.AssetID, job.OrganizationID, job.WorkerID, job.Generation)
	return err
}
func runFFProbe(ctx context.Context, filePath string) (mediaProbe, error) {
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_streams", "-show_format", "-of", "json", filePath)
	output, err := cmd.Output()
	if err != nil {
		return mediaProbe{}, fmt.Errorf("ffprobe rejected media")
	}
	var parsed ffprobeOutput
	if json.Unmarshal(output, &parsed) != nil {
		return mediaProbe{}, fmt.Errorf("ffprobe response is invalid")
	}
	probe := mediaProbe{Metadata: map[string]any{}}
	for _, stream := range parsed.Streams {
		if stream.CodecType == "video" && probe.Width == 0 {
			probe.Width = stream.Width
			probe.Height = stream.Height
			probe.Metadata["videoCodec"] = stream.CodecName
		}
		if stream.CodecType == "audio" {
			probe.Metadata["audioCodec"] = stream.CodecName
		}
	}
	duration, _ := strconv.ParseFloat(parsed.Format.Duration, 64)
	probe.DurationMS = int64(duration * 1000)
	probe.Metadata["bitrate"], _ = strconv.ParseInt(parsed.Format.BitRate, 10, 64)
	return probe, nil
}
func (s *Server) finishMediaValidation(ctx context.Context, job mediaValidationJob, ready bool, probe mediaProbe, validationErr error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var owned bool
	if err = tx.QueryRow(ctx, `SELECT true FROM media_assets WHERE id=$1 AND organization_id=$2 AND status='VALIDATING' AND validation_worker_id=$3 AND validation_generation=$4 AND validation_lease_until>now() FOR UPDATE`, job.ID, job.OrganizationID, job.WorkerID, job.Generation).Scan(&owned); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errMediaLeaseLost
		}
		return err
	}
	var result pgconn.CommandTag
	if ready {
		// Serialize installation of the organization-scoped immutable object, but
		// keep a distinct media_assets row for each creator. A digest is storage
		// identity, not authorization identity.
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('media-sha:'||$1::text||':'||encode($2::bytea,'hex'),0))`, job.OrganizationID, probe.SHA); err != nil {
			return err
		}
		var immutableExists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM media_assets WHERE organization_id=$1 AND content_sha256=$2 AND status='READY' AND object_key=$3)`, job.OrganizationID, probe.SHA, probe.HashKey).Scan(&immutableExists); err != nil {
			return err
		}
		storage, storageErr := s.mediaStorage(ctx)
		if storageErr != nil {
			return storageErr
		}
		// The media row stays locked through CopyObject and the fenced DB update.
		// Reclaim therefore cannot install a new generation in the check/copy gap.
		// The destination is content-addressed, so a retry after uncertain commit
		// copies identical bytes to the same immutable key.
		if !immutableExists {
			if _, storageErr = storage.client.CopyObject(ctx, &s3.CopyObjectInput{Bucket: &storage.bucket, Key: &probe.HashKey, CopySource: ptr(storage.bucket + "/" + job.ObjectKey), CacheControl: ptr("public, max-age=31536000, immutable"), ContentType: &probe.MIME, MetadataDirective: types.MetadataDirectiveReplace}); storageErr != nil {
				return fmt.Errorf("store immutable media: %w", storageErr)
			}
		}
		if probe.Metadata == nil {
			probe.Metadata = map[string]any{}
		}
		// Temporary source deletion is a separate fenced cleanup job represented
		// durably on the READY asset. Validation never deletes the shared source.
		probe.Metadata["temporaryObjectKey"] = job.ObjectKey
		metadata, _ := json.Marshal(probe.Metadata)
		result, err = tx.Exec(ctx, `UPDATE media_assets SET status='READY',object_key=$2,content_sha256=$3,detected_mime=$4,bytes=$5,width=$6,height=$7,duration_ms=$8,metadata=$9,ready_at=now(),validation_worker_id=NULL,validation_lease_until=NULL,rejected_reason=NULL WHERE id=$1 AND organization_id=$10 AND status='VALIDATING' AND validation_worker_id=$11 AND validation_generation=$12 AND validation_lease_until>now()`, job.ID, probe.HashKey, probe.SHA, probe.MIME, probe.Bytes, probe.Width, probe.Height, probe.DurationMS, metadata, job.OrganizationID, job.WorkerID, job.Generation)
	} else {
		message := "media validation failed"
		if validationErr != nil {
			message = validationErr.Error()
		}
		if len(message) > 500 {
			message = message[:500]
		}
		result, err = tx.Exec(ctx, `UPDATE media_assets SET status='REJECTED',rejected_reason=$2,delete_after=now()+interval '7 days',validation_worker_id=NULL,validation_lease_until=NULL WHERE id=$1 AND organization_id=$3 AND status='VALIDATING' AND validation_worker_id=$4 AND validation_generation=$5 AND validation_lease_until>now()`, job.ID, message, job.OrganizationID, job.WorkerID, job.Generation)
	}
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errMediaLeaseLost
	}
	company := job.CompanyID
	action := "SYSTEM_MEDIA_REJECTED"
	if ready {
		action = "SYSTEM_MEDIA_READY"
	}
	if err = s.writeAudit(ctx, tx, auditRecord{OrganizationID: job.OrganizationID, CompanyID: &company, Action: action, EntityType: "MEDIA_ASSET", EntityID: &job.ID, Metadata: map[string]any{}}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

var _ = time.Second
