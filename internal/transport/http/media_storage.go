package httpserver

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/go-chi/chi/v5"
)

type mediaStorage struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
}

const (
	mediaUploadPartSize         int64 = 8 << 20
	mediaUploadMaxBytes         int64 = 4 << 30
	mediaUploadMaxOrgSessions         = 25
	mediaUploadMaxActorSessions       = 5
	mediaUploadMaxOrgBytes      int64 = 20 << 30
	mediaUploadMaxActorBytes    int64 = 8 << 30
	mediaDeliveryMaxLifetime          = 2 * time.Hour
)

var mediaDeliveryRange = regexp.MustCompile(`^bytes=(?:[0-9]+-[0-9]*|-[0-9]+)$`)

func mediaUploadPartCount(expectedBytes int64) int32 {
	return int32((expectedBytes + mediaUploadPartSize - 1) / mediaUploadPartSize)
}

func mediaUploadPartBytes(expectedBytes int64, partNumber int32) int64 {
	parts := mediaUploadPartCount(expectedBytes)
	if partNumber < 1 || partNumber > parts {
		return 0
	}
	if partNumber < parts {
		return mediaUploadPartSize
	}
	return expectedBytes - int64(parts-1)*mediaUploadPartSize
}

func mediaUploadQuotaExceeded(orgSessions int, orgBytes int64, actorSessions int, actorBytes, requested int64) bool {
	return orgSessions >= mediaUploadMaxOrgSessions || orgBytes+requested > mediaUploadMaxOrgBytes || actorSessions >= mediaUploadMaxActorSessions || actorBytes+requested > mediaUploadMaxActorBytes
}

type mediaDeliveryCapability struct {
	ObjectKey string `json:"k"`
	ExpiresAt int64  `json:"e"`
}

// providerSafeMediaURL creates an application capability, not an S3 URL. The
// authenticated ciphertext hides the bucket and immutable key; the public
// endpoint resolves it server-side and streams from the private bucket.
func (s *Server) providerSafeMediaURL(ctx context.Context, objectKey string, expires time.Duration) (string, error) {
	_ = ctx
	if strings.TrimSpace(objectKey) == "" {
		return "", fmt.Errorf("media object key is missing")
	}
	if expires <= 0 || expires > mediaDeliveryMaxLifetime {
		return "", fmt.Errorf("media delivery lifetime must be between 1ns and %s", mediaDeliveryMaxLifetime)
	}
	public, err := mediaDeliveryOrigin(s.config.MediaPublicBaseURL)
	if err != nil {
		return "", err
	}
	storageURL, storageErr := url.Parse(strings.TrimSpace(s.config.MediaS3Endpoint))
	if storageErr == nil && storageURL.Host != "" && strings.EqualFold(storageURL.Host, public.Host) {
		return "", fmt.Errorf("MEDIA_PUBLIC_BASE_URL must not be the storage API host")
	}
	key, err := mediaDeliveryKey(s.config.MediaDeliveryTokenKey)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	payload, err := json.Marshal(mediaDeliveryCapability{ObjectKey: objectKey, ExpiresAt: s.now().Add(expires).Unix()})
	if err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, payload, []byte("statzavod-media-delivery-v1"))
	token := base64.RawURLEncoding.EncodeToString(append([]byte{1}, sealed...))
	public.Path = "/api/v1/media/delivery/" + token
	return public.String(), nil
}

func mediaDeliveryOrigin(value string) (*url.URL, error) {
	public, err := url.Parse(strings.TrimSpace(value))
	if err != nil || public.Scheme != "https" || public.Host == "" || public.User != nil || public.RawQuery != "" || public.Fragment != "" || (public.Path != "" && public.Path != "/") {
		return nil, fmt.Errorf("MEDIA_PUBLIC_BASE_URL must be a clean HTTPS delivery origin")
	}
	public.Path, public.RawPath = "", ""
	return public, nil
}

func mediaDeliveryKey(value string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("MEDIA_DELIVERY_TOKEN_KEY must be exactly 32 bytes encoded as base64")
	}
	return key, nil
}

func (s *Server) openMediaDeliveryToken(token string) (mediaDeliveryCapability, error) {
	var capability mediaDeliveryCapability
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != token || len(raw) < 2 || raw[0] != 1 {
		return capability, fmt.Errorf("invalid media delivery token")
	}
	key, err := mediaDeliveryKey(s.config.MediaDeliveryTokenKey)
	if err != nil {
		return capability, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return capability, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil || len(raw[1:]) < aead.NonceSize() {
		return capability, fmt.Errorf("invalid media delivery token")
	}
	nonce, ciphertext := raw[1:1+aead.NonceSize()], raw[1+aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, ciphertext, []byte("statzavod-media-delivery-v1"))
	if err != nil || json.Unmarshal(plain, &capability) != nil || capability.ObjectKey == "" || capability.ExpiresAt <= s.now().Unix() || capability.ExpiresAt > s.now().Add(mediaDeliveryMaxLifetime).Unix() {
		return mediaDeliveryCapability{}, fmt.Errorf("invalid or expired media delivery token")
	}
	return capability, nil
}

func (s *Server) serveProviderMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		problem(w, http.StatusMethodNotAllowed, "method not allowed", "only GET and HEAD are supported")
		return
	}
	public, originErr := mediaDeliveryOrigin(s.config.MediaPublicBaseURL)
	if originErr != nil || !strings.EqualFold(strings.TrimSpace(r.Host), public.Host) {
		http.NotFound(w, r)
		return
	}
	capability, err := s.openMediaDeliveryToken(chi.URLParam(r, "token"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rangeHeader := strings.TrimSpace(r.Header.Get("Range"))
	if rangeHeader != "" && !mediaDeliveryRange.MatchString(rangeHeader) {
		w.Header().Set("Content-Range", "bytes */*")
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	storage, err := s.mediaStorage(r.Context())
	if err != nil {
		problem(w, http.StatusServiceUnavailable, "media unavailable", "media storage is unavailable")
		return
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodHead {
		input := &s3.HeadObjectInput{Bucket: &storage.bucket, Key: &capability.ObjectKey}
		if rangeHeader != "" {
			input.Range = &rangeHeader
		}
		out, headErr := storage.client.HeadObject(r.Context(), input)
		if headErr != nil {
			http.NotFound(w, r)
			return
		}
		copyMediaDeliveryHeaders(w.Header(), out.ContentType, out.ContentLength, out.ContentRange)
		if rangeHeader != "" {
			w.WriteHeader(http.StatusPartialContent)
		}
		return
	}
	input := &s3.GetObjectInput{Bucket: &storage.bucket, Key: &capability.ObjectKey}
	if rangeHeader != "" {
		input.Range = &rangeHeader
	}
	out, getErr := storage.client.GetObject(r.Context(), input)
	if getErr != nil {
		http.NotFound(w, r)
		return
	}
	defer out.Body.Close()
	copyMediaDeliveryHeaders(w.Header(), out.ContentType, out.ContentLength, out.ContentRange)
	if rangeHeader != "" {
		w.WriteHeader(http.StatusPartialContent)
	}
	_, _ = io.Copy(w, out.Body)
}

func copyMediaDeliveryHeaders(header http.Header, contentType *string, contentLength *int64, contentRange *string) {
	if contentType != nil && strings.TrimSpace(*contentType) != "" {
		header.Set("Content-Type", *contentType)
	}
	if contentLength != nil && *contentLength >= 0 {
		header.Set("Content-Length", strconv.FormatInt(*contentLength, 10))
	}
	if contentRange != nil && strings.TrimSpace(*contentRange) != "" {
		header.Set("Content-Range", *contentRange)
	}
}

func (s *Server) mediaStorage(ctx context.Context) (*mediaStorage, error) {
	if s.config.MediaS3Endpoint == "" || s.config.MediaS3Bucket == "" || s.config.MediaS3AccessKey == "" || s.config.MediaS3SecretKey == "" {
		return nil, fmt.Errorf("media storage is not configured")
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(s.config.MediaS3Region), awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(s.config.MediaS3AccessKey, s.config.MediaS3SecretKey, "")), awsconfig.WithBaseEndpoint(s.config.MediaS3Endpoint))
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true })
	return &mediaStorage{client: client, presign: s3.NewPresignClient(client), bucket: s.config.MediaS3Bucket}, nil
}
func uploadID(r *http.Request) string {
	if id := chi.URLParam(r, "uploadID"); id != "" {
		return id
	}
	return chi.URLParam(r, "id")
}
func (s *Server) createMediaUpload(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, 500, "upload failed", "could not start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	finishIdempotency, proceed := beginContentCommandIdempotency(w, r, tx, p)
	if !proceed {
		_ = tx.Commit(r.Context())
		return
	}
	var in struct {
		CreatorID, Filename, Mime string
		Bytes                     int64
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil || in.Bytes <= 0 || in.Bytes > mediaUploadMaxBytes || strings.TrimSpace(in.Filename) == "" {
		problem(w, 400, "invalid upload", "filename and a size up to 4 GiB are required")
		return
	}
	creator, company, code := contentRouteCreator(r, p, in.CreatorID)
	if code != 0 {
		problem(w, code, "forbidden", "creator scope is required")
		return
	}
	if p.Role != roleCreator {
		if status := s.authorizeCreatorAction(r.Context(), p, creator, "CONTENT_CREATE"); status != 0 {
			problem(w, status, "not found", "creator is not available")
			return
		}
	}
	storage, err := s.mediaStorage(r.Context())
	if err != nil {
		problem(w, 503, "media unavailable", err.Error())
		return
	}
	// Quota checks and the following insert are serialized for both dimensions.
	// Without the advisory locks, concurrent transactions could all observe the
	// same pre-insert count and exceed the declared-byte budget.
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('media-upload-org:'||$1::text,0)),pg_advisory_xact_lock(hashtextextended('media-upload-actor:'||$2::text,0))`, p.OrganizationID, p.ID); err != nil {
		problem(w, 500, "upload failed", "could not reserve upload quota")
		return
	}
	var orgSessions, actorSessions int
	var orgBytes, actorBytes int64
	if err = tx.QueryRow(r.Context(), `SELECT count(*),COALESCE(sum(expected_bytes),0),count(*) FILTER (WHERE actor_id=$2),COALESCE(sum(expected_bytes) FILTER (WHERE actor_id=$2),0) FROM media_upload_sessions WHERE organization_id=$1 AND status='ACTIVE' AND expires_at>now()`, p.OrganizationID, p.ID).Scan(&orgSessions, &orgBytes, &actorSessions, &actorBytes); err != nil {
		problem(w, 500, "upload failed", "could not check upload quota")
		return
	}
	if mediaUploadQuotaExceeded(orgSessions, orgBytes, actorSessions, actorBytes, in.Bytes) {
		problem(w, http.StatusTooManyRequests, "upload quota exceeded", "finish or abort active uploads before starting another")
		return
	}
	var assetID string
	err = tx.QueryRow(r.Context(), `INSERT INTO media_assets(organization_id,company_id,creator_id,original_filename) VALUES($1,$2,$3,$4) RETURNING id`, p.OrganizationID, company, creator, path.Base(in.Filename)).Scan(&assetID)
	if err != nil {
		problem(w, 500, "upload failed", "could not create media asset")
		return
	}
	key := "temporary/" + p.OrganizationID + "/" + assetID
	result, err := storage.client.CreateMultipartUpload(r.Context(), &s3.CreateMultipartUploadInput{Bucket: &storage.bucket, Key: &key, ContentType: &in.Mime, CacheControl: ptr("private, no-store")})
	if err != nil {
		problem(w, 502, "media unavailable", "could not create multipart upload")
		return
	}
	// S3 is outside PostgreSQL's transaction.  Until the DB commit is known to
	// have succeeded, compensate the remote multipart upload on every failure.
	// This prevents a failed idempotency/database write from leaking an upload.
	multipartCreated := true
	committed := false
	defer func() {
		if multipartCreated && !committed {
			_, _ = storage.client.AbortMultipartUpload(context.Background(), &s3.AbortMultipartUploadInput{Bucket: &storage.bucket, Key: &key, UploadId: result.UploadId})
		}
	}()
	var sessionID string
	err = tx.QueryRow(r.Context(), `INSERT INTO media_upload_sessions(media_asset_id,organization_id,actor_id,multipart_upload_id,expected_bytes,expected_mime) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, assetID, p.OrganizationID, p.ID, *result.UploadId, in.Bytes, in.Mime).Scan(&sessionID)
	if err != nil {
		problem(w, 500, "upload failed", "could not create upload session")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE media_assets SET object_key=$2,bytes=$3,detected_mime=$4 WHERE id=$1`, assetID, key, in.Bytes, in.Mime); err != nil {
		return
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &company, "CREATE_MEDIA_UPLOAD", "MEDIA_ASSET", &assetID, 201, map[string]any{})); err != nil {
		problem(w, 500, "audit failed", "could not record upload")
		return
	}
	response := map[string]any{"uploadId": sessionID, "mediaAssetId": assetID, "partSize": mediaUploadPartSize, "partCount": mediaUploadPartCount(in.Bytes), "expiresInSeconds": 86400}
	if err = finishIdempotency(http.StatusCreated, response); err != nil {
		problem(w, 500, "upload failed", "could not persist idempotency state")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, 500, "upload failed", "could not commit upload")
		return
	}
	committed = true
	markResponseAuditCommitted(w)
	writeContentCommandResponse(w, http.StatusCreated, response)
}
func (s *Server) signMediaUploadPart(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	id := uploadID(r)
	part, err := strconv.Atoi(chi.URLParam(r, "partNumber"))
	if err != nil || part < 1 {
		problem(w, 400, "invalid upload part", "part number must be positive")
		return
	}
	var multi, key, creator, company string
	var expectedBytes int64
	err = s.pool.QueryRow(r.Context(), `SELECT session.multipart_upload_id,asset.object_key,asset.creator_id::text,asset.company_id::text,session.expected_bytes FROM media_upload_sessions session JOIN media_assets asset ON asset.id=session.media_asset_id AND asset.organization_id=session.organization_id WHERE session.id=$1 AND session.organization_id=$2 AND session.status='ACTIVE' AND session.expires_at>now()`, id, p.OrganizationID).Scan(&multi, &key, &creator, &company, &expectedBytes)
	if err != nil || !canUseContentCreator(p, creator, company, "CONTENT_CREATE") {
		problem(w, 404, "not found", "upload session is not available")
		return
	}
	if int32(part) > mediaUploadPartCount(expectedBytes) {
		problem(w, 400, "invalid upload part", "part number exceeds the declared file size")
		return
	}
	storage, err := s.mediaStorage(r.Context())
	if err != nil {
		problem(w, 503, "media unavailable", err.Error())
		return
	}
	part32 := int32(part)
	out, err := storage.presign.PresignUploadPart(r.Context(), &s3.UploadPartInput{Bucket: &storage.bucket, Key: &key, UploadId: &multi, PartNumber: &part32}, func(o *s3.PresignOptions) { o.Expires = 15 * time.Minute })
	if err != nil {
		problem(w, 502, "media unavailable", "could not sign upload part")
		return
	}
	if err = s.commitAuditOnly(r.Context(), w, requestAuditRecord(r, p, &company, "SIGN_MEDIA_UPLOAD_PART", "MEDIA_UPLOAD_SESSION", &id, http.StatusOK, map[string]any{"partNumber": part})); err != nil {
		problem(w, http.StatusInternalServerError, "audit failed", "could not record upload part")
		return
	}
	writeJSON(w, 200, map[string]any{"url": out.URL, "headers": out.SignedHeader})
}
func canUseContentCreator(p principal, creator, company, permission string) bool {
	if p.Role == roleCreator {
		profile, ok := activeCreatorContext(p)
		return ok && profile.ID == creator && profile.CompanyID == company
	}
	return authorizeCompanyAction(p, company, permission) == 0 && p.hasPermission(permission)
}
func ptr(v string) *string { return &v }

func (s *Server) completeMediaUpload(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	id := uploadID(r)
	r.Body = http.MaxBytesReader(w, r.Body, contentCommandBodyLimit)
	rawBody, readErr := ioReadAndRestoreBounded(r, contentCommandBodyLimit)
	if readErr != nil {
		if errors.Is(readErr, errContentCommandBodyTooLarge) || isMaxBytesError(readErr) {
			problem(w, http.StatusRequestEntityTooLarge, "request too large", "completed parts body exceeds 1 MiB")
			return
		}
		problem(w, 400, "invalid upload", "could not read completed parts")
		return
	}
	var in struct {
		Parts []struct {
			PartNumber int32  `json:"partNumber"`
			ETag       string `json:"etag"`
		} `json:"parts"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil || len(in.Parts) == 0 {
		problem(w, 400, "invalid upload", "at least one completed part is required")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(rawBody))
	var multi, key, assetID, creator, company string
	var expectedBytes int64
	parts := make([]types.CompletedPart, 0, len(in.Parts))
	for _, part := range in.Parts {
		if part.PartNumber < 1 || part.ETag == "" {
			problem(w, 400, "invalid upload", "each part requires number and ETag")
			return
		}
		etag := part.ETag
		number := part.PartNumber
		parts = append(parts, types.CompletedPart{ETag: &etag, PartNumber: &number})
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, 500, "upload failed", "could not start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	finishIdempotency, proceed := beginContentCommandIdempotency(w, r, tx, p)
	if !proceed {
		_ = tx.Commit(r.Context())
		return
	}
	// Lock the session before completing the external upload. This serializes
	// distinct command keys for the same session, while the idempotency row
	// serializes duplicate keys. A failed DB transaction leaves the multipart
	// active for a safe retry instead of marking the asset complete locally.
	if err = tx.QueryRow(r.Context(), `SELECT session.multipart_upload_id,asset.object_key,asset.id::text,asset.creator_id::text,asset.company_id::text,session.expected_bytes FROM media_upload_sessions session JOIN media_assets asset ON asset.id=session.media_asset_id AND asset.organization_id=session.organization_id WHERE session.id=$1 AND session.organization_id=$2 AND session.status='ACTIVE' AND session.expires_at>now() FOR UPDATE OF session`, id, p.OrganizationID).Scan(&multi, &key, &assetID, &creator, &company, &expectedBytes); err != nil || !canUseContentCreator(p, creator, company, "CONTENT_CREATE") {
		problem(w, 404, "not found", "upload session is not available")
		return
	}
	storage, err := s.mediaStorage(r.Context())
	if err != nil {
		problem(w, 503, "media unavailable", err.Error())
		return
	}
	if err = validateUploadedMediaParts(r.Context(), storage, key, multi, expectedBytes, parts); err != nil {
		problem(w, 400, "invalid upload", err.Error())
		return
	}
	_, err = storage.client.CompleteMultipartUpload(r.Context(), &s3.CompleteMultipartUploadInput{Bucket: &storage.bucket, Key: &key, UploadId: &multi, MultipartUpload: &types.CompletedMultipartUpload{Parts: parts}})
	if err != nil {
		problem(w, 502, "media unavailable", "could not complete multipart upload")
		return
	}
	remoteCompleted := true
	committed := false
	defer func() {
		if !remoteCompleted || committed {
			return
		}
		// CompleteMultipartUpload cannot be rolled back. If the transaction did
		// not commit, retain a durable DELETE_PENDING tombstone for the cleanup
		// worker instead of leaving an untracked object. A commit may return an
		// uncertain error, so first look for the persisted replay response.
		var status int
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key != "" && s.pool.QueryRow(context.Background(), `SELECT response_status FROM content_command_idempotency WHERE organization_id=$1 AND actor_id=$2 AND idempotency_key=$3`, p.OrganizationID, p.ID, key).Scan(&status) == nil && status == http.StatusOK {
			return
		}
		_, _ = s.pool.Exec(context.Background(), `UPDATE media_assets SET status='DELETE_PENDING',delete_after=now()+interval '7 days' WHERE id=$1 AND organization_id=$2 AND status IN ('UPLOADING','VALIDATING')`, assetID, p.OrganizationID)
	}()
	if _, err = tx.Exec(r.Context(), `UPDATE media_upload_sessions SET status='COMPLETED',completed_at=now() WHERE id=$1 AND status='ACTIVE'`, id); err != nil {
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE media_assets SET status='VALIDATING' WHERE id=$1 AND organization_id=$2 AND status='UPLOADING'`, assetID, p.OrganizationID); err != nil {
		return
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &company, "COMPLETE_MEDIA_UPLOAD", "MEDIA_ASSET", &assetID, 200, map[string]any{})); err != nil {
		problem(w, 500, "audit failed", "could not record upload")
		return
	}
	response := map[string]any{"mediaAssetId": assetID, "status": "VALIDATING"}
	if err = finishIdempotency(http.StatusOK, response); err != nil {
		problem(w, 500, "upload failed", "could not persist idempotency state")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, 500, "upload failed", "could not commit upload")
		return
	}
	committed = true
	markResponseAuditCommitted(w)
	writeContentCommandResponse(w, http.StatusOK, response)
}

// validateUploadedMediaParts is the enforceable size boundary for presigned
// multipart uploads. SigV4 UploadPart URLs do not constrain Content-Length, so
// we inspect S3's persisted part sizes immediately before completing.
func validateUploadedMediaParts(ctx context.Context, storage *mediaStorage, key, uploadID string, expectedBytes int64, requested []types.CompletedPart) error {
	expectedCount := mediaUploadPartCount(expectedBytes)
	if int32(len(requested)) != expectedCount {
		return fmt.Errorf("exactly %d upload parts are required", expectedCount)
	}
	byNumber := make(map[int32]types.Part, expectedCount)
	var marker *string
	for {
		out, err := storage.client.ListParts(ctx, &s3.ListPartsInput{Bucket: &storage.bucket, Key: &key, UploadId: &uploadID, PartNumberMarker: marker})
		if err != nil {
			return fmt.Errorf("could not verify uploaded parts")
		}
		for _, uploaded := range out.Parts {
			if uploaded.PartNumber != nil {
				byNumber[*uploaded.PartNumber] = uploaded
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			break
		}
		marker = out.NextPartNumberMarker
		if marker == nil {
			return fmt.Errorf("storage returned an invalid parts page")
		}
	}
	seen := make(map[int32]struct{}, expectedCount)
	for _, requestedPart := range requested {
		if requestedPart.PartNumber == nil || requestedPart.ETag == nil || *requestedPart.PartNumber < 1 || *requestedPart.PartNumber > expectedCount {
			return fmt.Errorf("completed part number is outside the declared file size")
		}
		number := *requestedPart.PartNumber
		if _, duplicate := seen[number]; duplicate {
			return fmt.Errorf("completed part numbers must be unique")
		}
		seen[number] = struct{}{}
		uploaded, ok := byNumber[number]
		if !ok || uploaded.Size == nil || uploaded.ETag == nil {
			return fmt.Errorf("upload part %d is missing from storage", number)
		}
		if *uploaded.Size != mediaUploadPartBytes(expectedBytes, number) {
			return fmt.Errorf("upload part %d has size %d, expected %d", number, *uploaded.Size, mediaUploadPartBytes(expectedBytes, number))
		}
		if strings.Trim(*uploaded.ETag, `"`) != strings.Trim(*requestedPart.ETag, `"`) {
			return fmt.Errorf("upload part %d ETag does not match storage", number)
		}
	}
	return nil
}
func (s *Server) abortMediaUpload(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	id := uploadID(r)
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, 500, "upload failed", "could not start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	finishIdempotency, proceed := beginContentCommandIdempotency(w, r, tx, p)
	if !proceed {
		_ = tx.Commit(r.Context())
		return
	}
	var multi, key, assetID, creator, company string
	err = tx.QueryRow(r.Context(), `SELECT session.multipart_upload_id,asset.object_key,asset.id::text,asset.creator_id::text,asset.company_id::text FROM media_upload_sessions session JOIN media_assets asset ON asset.id=session.media_asset_id AND asset.organization_id=session.organization_id WHERE session.id=$1 AND session.organization_id=$2 AND session.status='ACTIVE' FOR UPDATE OF session`, id, p.OrganizationID).Scan(&multi, &key, &assetID, &creator, &company)
	if err != nil || !canUseContentCreator(p, creator, company, "CONTENT_CREATE") {
		problem(w, 404, "not found", "upload session is not available")
		return
	}
	storage, err := s.mediaStorage(r.Context())
	if err != nil {
		problem(w, 503, "media unavailable", err.Error())
		return
	}
	_, err = storage.client.AbortMultipartUpload(r.Context(), &s3.AbortMultipartUploadInput{Bucket: &storage.bucket, Key: &key, UploadId: &multi})
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "nosuchupload") {
		problem(w, 502, "media unavailable", "could not abort multipart upload")
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE media_upload_sessions SET status='ABORTED' WHERE id=$1`, id)
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE media_assets SET status='DELETE_PENDING',delete_after=now()+interval '7 days' WHERE id=$1`, assetID)
	}
	if err == nil {
		err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &company, "ABORT_MEDIA_UPLOAD", "MEDIA_ASSET", &assetID, 204, map[string]any{}))
	}
	if err != nil {
		problem(w, 500, "upload failed", "could not abort upload")
		return
	}
	if err = finishIdempotency(http.StatusNoContent, nil); err != nil {
		problem(w, 500, "upload failed", "could not persist idempotency state")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, 500, "upload failed", "could not commit upload abort")
		return
	}
	markResponseAuditCommitted(w)
	w.WriteHeader(http.StatusNoContent)
}
