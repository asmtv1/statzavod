package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// auditExecer is deliberately the intersection implemented by pgx.Tx and
// pgxpool.Pool. Business handlers and lifecycle workers therefore use one
// redacted writer, including when the record must share a transaction.
type auditExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

type auditRecord struct {
	OrganizationID string
	ActorID        *string
	CompanyID      *string
	Action         string
	EntityType     string
	EntityID       *string
	Endpoint       string
	HTTPMethod     string
	ResponseStatus *int
	Metadata       map[string]any
}

// auditEndpointCoverage is the executable endpoint -> action contract. Login,
// logout, anonymous provider callbacks/receipts, and technical health routes
// are intentionally absent. A successful authenticated route not listed here
// is failed closed by the middleware, making omissions visible in tests.
var auditEndpointCoverage = func() map[string]string {
	endpoints := []string{
		"GET /api/v1/auth/me", "POST /api/v1/auth/context",
		"GET /api/v1/companies", "GET /api/v1/companies/archive", "POST /api/v1/companies",
		"GET /api/v1/company-vk-accounts", "PUT /api/v1/companies/{id}/vk-account",
		"POST /api/v1/companies/{id}/vk-account/authorize", "POST /api/v1/company-vk-accounts/{id}/password/reveal",
		"DELETE /api/v1/companies/{id}", "POST /api/v1/companies/{id}/restore",
		"GET /api/v1/analytics/summary", "GET /api/v1/analytics/timeseries", "GET /api/v1/analytics/creators/{id}",
		"GET /api/v1/exports", "GET /api/v1/creators", "POST /api/v1/creators", "GET /api/v1/creators/{id}",
		"GET /api/v1/creators/{id}/history", "PATCH /api/v1/creators/{id}", "PATCH /api/v1/creators/{id}/work-status",
		"GET /api/v1/creators/{id}/credentials", "PUT /api/v1/creators/{id}/credentials",
		"POST /api/v1/creators/{id}/credentials/{credentialID}/reveal", "GET /api/v1/creators/{id}/vk-access",
		"PUT /api/v1/creators/{id}/vk-access", "POST /api/v1/creators/{id}/history/changes/{changeID}/reveal",
		"GET /api/v1/creators/{id}/accounts", "POST /api/v1/creators/{id}/accounts",
		"GET /api/v1/creators/{id}/login-account", "PUT /api/v1/creators/{id}/login-account",
		"POST /api/v1/creators/{id}/connections/{platform}/authorize",
		"GET /api/v1/creators/{id}/connections/instagram-facebook/selections/{selectionID}",
		"POST /api/v1/creators/{id}/connections/instagram-facebook/selections/{selectionID}",
		"GET /api/v1/creators/{id}/connections", "GET /api/v1/integrations",
		"DELETE /api/v1/platform-accounts/{id}/connection", "DELETE /api/v1/platform-accounts/{id}/data",
		"POST /api/v1/platform-accounts/{id}/sync", "POST /api/v1/platform-accounts/{id}/pause",
		"POST /api/v1/platform-accounts/{id}/resume", "POST /api/v1/creators/{id}/contacts",
		"POST /api/v1/creators/{id}/archive", "POST /api/v1/creators/{id}/restore", "DELETE /api/v1/creators/{id}",
		"GET /api/v1/publications", "GET /api/v1/notifications", "POST /api/v1/notifications/read-all", "POST /api/v1/notifications/{notificationID}/read", "GET /api/v1/content-groups", "POST /api/v1/content-groups", "GET /api/v1/sync/health",
		"GET /api/v1/content-items", "POST /api/v1/content-items", "POST /api/v1/content-preflight", "GET /api/v1/content-items/{id}", "GET /api/v1/content-items/{id}/attempts", "PATCH /api/v1/content-items/{id}", "POST /api/v1/content-items/{id}/copy", "POST /api/v1/content-items/{id}/preflight", "POST /api/v1/content-items/{id}/submit", "POST /api/v1/content-items/{id}/approve", "POST /api/v1/content-items/{id}/reject", "POST /api/v1/content-items/{id}/publish", "POST /api/v1/content-items/{id}/schedule", "POST /api/v1/content-items/{id}/retry", "POST /api/v1/content-items/{id}/cancel", "GET /api/v1/creators/{id}/content-approval-policy", "PUT /api/v1/creators/{id}/content-approval-policy",
		"GET /api/v1/creator-portal/profiles", "GET /api/v1/creator-portal/profile", "GET /api/v1/creator-portal/socials",
		"GET /api/v1/creator-portal/credentials", "POST /api/v1/creator-portal/credentials/{credentialID}/reveal",
		"GET /api/v1/creator-portal/publications", "GET /api/v1/creator-portal/stats", "GET /api/v1/creator-portal/export",
		"GET /api/v1/creator-portal/content-items", "POST /api/v1/creator-portal/content-items", "POST /api/v1/creator-portal/content-preflight", "GET /api/v1/creator-portal/content-items/{itemID}", "GET /api/v1/creator-portal/content-items/{itemID}/attempts", "PATCH /api/v1/creator-portal/content-items/{itemID}", "POST /api/v1/creator-portal/content-items/{itemID}/copy", "POST /api/v1/creator-portal/content-items/{itemID}/preflight", "POST /api/v1/creator-portal/content-items/{itemID}/submit", "POST /api/v1/creator-portal/content-items/{itemID}/publish", "POST /api/v1/creator-portal/content-items/{itemID}/schedule", "POST /api/v1/creator-portal/content-items/{itemID}/retry", "POST /api/v1/creator-portal/content-items/{itemID}/cancel",
		"POST /api/v1/media/uploads", "POST /api/v1/media/uploads/{id}/parts/{partNumber}", "POST /api/v1/media/uploads/{id}/complete", "DELETE /api/v1/media/uploads/{id}",
		"POST /api/v1/creator-portal/media/uploads", "POST /api/v1/creator-portal/media/uploads/{uploadID}/parts/{partNumber}", "POST /api/v1/creator-portal/media/uploads/{uploadID}/complete", "DELETE /api/v1/creator-portal/media/uploads/{uploadID}",
		"GET /api/v1/users", "GET /api/v1/users/me/deletion-impact", "DELETE /api/v1/users/me", "POST /api/v1/users",
		"PATCH /api/v1/users/{id}", "PUT /api/v1/users/{id}/password", "DELETE /api/v1/users/{id}",
		"PUT /api/v1/users/{id}/company-assignments", "GET /api/v1/audit", "POST /api/v1/users/invitations",
	}
	result := make(map[string]string, len(endpoints))
	for _, endpoint := range endpoints {
		parts := strings.SplitN(endpoint, " ", 2)
		result[endpoint] = auditAction(parts[0], parts[1])
	}
	return result
}()

// auditAtomicMutationCoverage is the route-level contract for authenticated
// business mutations. Each listed handler must mutate through a pgx.Tx, insert
// its audit record through writeAudit on that same transaction, commit, and
// only then call markResponseAuditCommitted. The middleware rejects a
// successful response that violates this contract. POST endpoints which only
// reveal already-existing data are deliberately absent; their response remains
// buffered and fail-closed until the ordinary mandatory audit insert succeeds.
var auditAtomicMutationCoverage = func() map[string]struct{} {
	endpoints := []string{
		"POST /api/v1/auth/context",
		"POST /api/v1/companies", "PUT /api/v1/companies/{id}/vk-account",
		"POST /api/v1/companies/{id}/vk-account/authorize", "DELETE /api/v1/companies/{id}",
		"POST /api/v1/companies/{id}/restore", "POST /api/v1/creators",
		"PATCH /api/v1/creators/{id}", "PATCH /api/v1/creators/{id}/work-status",
		"PUT /api/v1/creators/{id}/credentials", "PUT /api/v1/creators/{id}/vk-access",
		"POST /api/v1/creators/{id}/accounts", "PUT /api/v1/creators/{id}/login-account",
		"POST /api/v1/creators/{id}/connections/{platform}/authorize",
		"POST /api/v1/creators/{id}/connections/instagram-facebook/selections/{selectionID}",
		"DELETE /api/v1/platform-accounts/{id}/connection", "DELETE /api/v1/platform-accounts/{id}/data",
		"POST /api/v1/platform-accounts/{id}/sync", "POST /api/v1/platform-accounts/{id}/pause",
		"POST /api/v1/platform-accounts/{id}/resume", "POST /api/v1/creators/{id}/contacts",
		"POST /api/v1/creators/{id}/archive", "POST /api/v1/creators/{id}/restore",
		"DELETE /api/v1/creators/{id}", "POST /api/v1/content-groups",
		"POST /api/v1/notifications/read-all", "POST /api/v1/notifications/{notificationID}/read", "POST /api/v1/content-items", "POST /api/v1/content-preflight", "PATCH /api/v1/content-items/{id}", "POST /api/v1/content-items/{id}/copy", "POST /api/v1/content-items/{id}/preflight", "POST /api/v1/content-items/{id}/submit", "POST /api/v1/content-items/{id}/approve", "POST /api/v1/content-items/{id}/reject", "POST /api/v1/content-items/{id}/publish", "POST /api/v1/content-items/{id}/schedule", "POST /api/v1/content-items/{id}/retry", "POST /api/v1/content-items/{id}/cancel", "PUT /api/v1/creators/{id}/content-approval-policy",
		"POST /api/v1/creator-portal/content-items", "POST /api/v1/creator-portal/content-preflight", "PATCH /api/v1/creator-portal/content-items/{itemID}", "POST /api/v1/creator-portal/content-items/{itemID}/copy", "POST /api/v1/creator-portal/content-items/{itemID}/preflight", "POST /api/v1/creator-portal/content-items/{itemID}/submit", "POST /api/v1/creator-portal/content-items/{itemID}/publish", "POST /api/v1/creator-portal/content-items/{itemID}/schedule", "POST /api/v1/creator-portal/content-items/{itemID}/retry", "POST /api/v1/creator-portal/content-items/{itemID}/cancel",
		"POST /api/v1/media/uploads", "POST /api/v1/media/uploads/{id}/parts/{partNumber}", "POST /api/v1/media/uploads/{id}/complete", "DELETE /api/v1/media/uploads/{id}",
		"POST /api/v1/creator-portal/media/uploads", "POST /api/v1/creator-portal/media/uploads/{uploadID}/parts/{partNumber}", "POST /api/v1/creator-portal/media/uploads/{uploadID}/complete", "DELETE /api/v1/creator-portal/media/uploads/{uploadID}",
		"DELETE /api/v1/users/me", "POST /api/v1/users", "PATCH /api/v1/users/{id}",
		"PUT /api/v1/users/{id}/password", "DELETE /api/v1/users/{id}",
		"PUT /api/v1/users/{id}/company-assignments", "POST /api/v1/users/invitations",
	}
	result := make(map[string]struct{}, len(endpoints))
	for _, endpoint := range endpoints {
		result[endpoint] = struct{}{}
	}
	return result
}()

const (
	auditSystemOAuthRefresh                 = "SYSTEM_OAUTH_REFRESH"
	auditSystemOAuthReauthRequired          = "SYSTEM_OAUTH_REAUTH_REQUIRED"
	auditSystemSyncStarted                  = "SYSTEM_SYNC_STARTED"
	auditSystemSyncSuccess                  = "SYSTEM_SYNC_SUCCESS"
	auditSystemSyncFailed                   = "SYSTEM_SYNC_FAILED"
	auditSystemPurgeCompany                 = "SYSTEM_PURGE_COMPANY"
	auditSystemPurgeWorkspace               = "SYSTEM_PURGE_WORKSPACE"
	auditSystemContentPublishAttempt        = "SYSTEM_CONTENT_PUBLISH_ATTEMPT"
	auditSystemContentPublishPending        = "SYSTEM_CONTENT_PUBLISH_PENDING"
	auditSystemContentPublishSucceeded      = "SYSTEM_CONTENT_PUBLISH_SUCCEEDED"
	auditSystemContentPublishFailed         = "SYSTEM_CONTENT_PUBLISH_FAILED"
	auditSystemContentPublishCancelled      = "SYSTEM_CONTENT_PUBLISH_CANCELLED"
	auditSystemContentPublishReauthRequired = "SYSTEM_CONTENT_PUBLISH_REAUTH_REQUIRED"
	auditSystemMediaReady                   = "SYSTEM_MEDIA_READY"
	auditSystemMediaRejected                = "SYSTEM_MEDIA_REJECTED"
	auditSystemMediaUploadExpired           = "SYSTEM_MEDIA_UPLOAD_EXPIRED"
	auditSystemMediaDeletePending           = "SYSTEM_MEDIA_DELETE_PENDING"
	auditSystemMediaDeleted                 = "SYSTEM_MEDIA_DELETED"
)

// auditSystemMutationCoverage keeps background mutations subject to the same
// canonical writer contract. Sync writes can span provider calls and many
// short database transactions, so their mandatory durable record is committed
// with the SKIP LOCKED claim before any import begins; completion records remain
// atomic with the final run/target state transition.
var auditSystemMutationCoverage = map[string]struct{}{
	auditSystemOAuthRefresh: {}, auditSystemOAuthReauthRequired: {},
	auditSystemSyncStarted: {}, auditSystemSyncSuccess: {}, auditSystemSyncFailed: {},
	auditSystemPurgeCompany: {}, auditSystemPurgeWorkspace: {},
	auditSystemContentPublishAttempt: {}, auditSystemContentPublishPending: {},
	auditSystemContentPublishSucceeded: {}, auditSystemContentPublishFailed: {},
	auditSystemContentPublishCancelled: {}, auditSystemContentPublishReauthRequired: {},
	auditSystemMediaReady: {}, auditSystemMediaRejected: {},
	auditSystemMediaUploadExpired: {}, auditSystemMediaDeletePending: {}, auditSystemMediaDeleted: {},
}

func (s *Server) writeAudit(ctx context.Context, execer auditExecer, record auditRecord) error {
	if record.OrganizationID == "" || record.Action == "" || record.EntityType == "" {
		return fmt.Errorf("incomplete audit record")
	}
	if record.Metadata == nil {
		record.Metadata = map[string]any{}
	}
	if record.ActorID != nil && strings.TrimSpace(*record.ActorID) == "" {
		record.ActorID = nil
	}
	if record.CompanyID != nil && strings.TrimSpace(*record.CompanyID) == "" {
		record.CompanyID = nil
	}
	if record.EntityID != nil && strings.TrimSpace(*record.EntityID) == "" {
		record.EntityID = nil
	}
	metadata, err := safeAuditMetadata(record.Metadata)
	if err != nil {
		return err
	}
	record.Metadata = metadata
	if s.auditFailure != nil {
		if err = s.auditFailure(record); err != nil {
			return err
		}
	}
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode audit metadata: %w", err)
	}
	// actor_id is resolved through the membership at insertion time. A
	// co-owner self-delete can therefore keep its audit evidence with a NULL
	// historical actor instead of failing on a deleted user FK.
	_, err = execer.Exec(ctx, `
		INSERT INTO audit_logs(
			organization_id,actor_id,company_id,action,entity_type,entity_id,
			endpoint,http_method,response_status,metadata
		)
		SELECT $1,
		       CASE WHEN NULLIF($2,'')::uuid IS NOT NULL AND EXISTS(
		         SELECT 1 FROM organization_memberships m
		         WHERE m.organization_id=$1 AND m.user_id=NULLIF($2,'')::uuid
		       ) THEN NULLIF($2,'')::uuid END,
		       CASE WHEN NULLIF($3,'')::uuid IS NOT NULL AND EXISTS(
		         SELECT 1 FROM companies c
		         WHERE c.organization_id=$1 AND c.id=NULLIF($3,'')::uuid
		       ) THEN NULLIF($3,'')::uuid END,
		       $4,$5,NULLIF($6,'')::uuid,NULLIF($7,''),NULLIF($8,''),$9,$10::jsonb
	`, record.OrganizationID, stringValue(record.ActorID), stringValue(record.CompanyID), record.Action,
		record.EntityType, stringValue(record.EntityID), record.Endpoint,
		record.HTTPMethod, record.ResponseStatus, string(encodedMetadata))
	return err
}

// safeAuditMetadata normalizes metadata through JSON and recursively replaces
// values whose keys can carry authentication material. This applies equally to
// typed maps/structs and nested arrays, so a future caller cannot accidentally
// bypass the guard by changing the Go representation.
func safeAuditMetadata(metadata map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("encode audit metadata: %w", err)
	}
	var normalized map[string]any
	if err = json.Unmarshal(encoded, &normalized); err != nil {
		return nil, fmt.Errorf("normalize audit metadata: %w", err)
	}
	redactAuditValue(normalized, "")
	return normalized, nil
}

func redactAuditValue(value any, path string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			keyPath := path + compactAuditKey(key)
			if sensitiveAuditKey(key) || strings.Contains(keyPath, "requestbody") {
				typed[key] = "[REDACTED]"
				continue
			}
			redactAuditValue(child, keyPath)
		}
	case []any:
		for _, child := range typed {
			redactAuditValue(child, path)
		}
	}
}

func compactAuditKey(key string) string {
	var normalized strings.Builder
	for _, character := range strings.ToLower(key) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			normalized.WriteRune(character)
		}
	}
	return normalized.String()
}

func sensitiveAuditKey(key string) bool {
	compact := compactAuditKey(key)
	for _, forbidden := range []string{
		"password", "hash", "cookie", "token", "ciphertext", "secret",
		"requestbody", "revealedcredential", "authorization", "apikey",
		"accesskey", "privatekey", "session", "nonce",
	} {
		if strings.Contains(compact, forbidden) {
			return true
		}
	}
	return false
}

func requestAuditRecord(r *http.Request, p principal, companyID *string, action, entityType string, entityID *string, status int, metadata map[string]any) auditRecord {
	actorID := p.ID
	return auditRecord{
		OrganizationID: p.OrganizationID,
		ActorID:        &actorID,
		CompanyID:      companyID,
		Action:         action,
		EntityType:     entityType,
		EntityID:       entityID,
		Endpoint:       chi.RouteContext(r.Context()).RoutePattern(),
		HTTPMethod:     r.Method,
		ResponseStatus: &status,
		Metadata:       metadata,
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

type bufferedAuditResponse struct {
	header         http.Header
	body           bytes.Buffer
	status         int
	auditCommitted bool
}

func newBufferedAuditResponse() *bufferedAuditResponse {
	return &bufferedAuditResponse{header: make(http.Header)}
}

func (w *bufferedAuditResponse) Header() http.Header { return w.header }

func (w *bufferedAuditResponse) markAuditCommitted() { w.auditCommitted = true }

func markResponseAuditCommitted(w http.ResponseWriter) {
	if marker, ok := w.(interface{ markAuditCommitted() }); ok {
		marker.markAuditCommitted()
	}
}

// commitAuditOnly is used by successful idempotent mutation attempts whose
// target disappeared before the handler acquired its lock. There is no
// business row to roll back, but the mutation route still satisfies the same
// exactly-once transactional audit protocol.
func (s *Server) commitAuditOnly(ctx context.Context, w http.ResponseWriter, record auditRecord) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = s.writeAudit(ctx, tx, record); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	markResponseAuditCommitted(w)
	return nil
}

// resolveRequestAuditTarget enriches centrally audited operations with stable,
// filtering-ready resource and company identifiers. Every lookup is tenant
// scoped; a successful handler followed by an unresolvable target fails closed
// instead of emitting a misleading workspace-wide record.
func (s *Server) resolveRequestAuditTarget(ctx context.Context, r *http.Request, p principal, record *auditRecord) error {
	route := chi.RouteContext(r.Context()).RoutePattern()
	id := chi.URLParam(r, "id")
	creatorID := ""
	switch {
	case strings.Contains(route, "/creators/{id}") || strings.Contains(route, "/analytics/creators/{id}"):
		creatorID = id
	case route == "/api/v1/exports":
		creatorID = strings.TrimSpace(r.URL.Query().Get("creatorId"))
	case strings.HasPrefix(route, "/api/v1/creator-portal/") && p.ActiveCreatorID != nil:
		creatorID = *p.ActiveCreatorID
	}
	if creatorID != "" {
		var companyID string
		if err := s.pool.QueryRow(ctx, `SELECT company_id::text FROM creators WHERE id=$1 AND organization_id=$2`, creatorID, p.OrganizationID).Scan(&companyID); err != nil {
			return fmt.Errorf("resolve creator audit target: %w", err)
		}
		record.CompanyID = &companyID
		record.EntityType = "CREATOR"
		record.EntityID = &creatorID
		return nil
	}
	if strings.Contains(route, "/companies/{id}") && id != "" {
		companyID := id
		record.CompanyID = &companyID
		record.EntityType = "COMPANY"
		record.EntityID = &companyID
		return nil
	}
	if strings.Contains(route, "/company-vk-accounts/{id}") && id != "" {
		var companyID string
		if err := s.pool.QueryRow(ctx, `SELECT company_id::text FROM company_vk_accounts WHERE id=$1 AND organization_id=$2`, id, p.OrganizationID).Scan(&companyID); err != nil {
			return fmt.Errorf("resolve company VK audit target: %w", err)
		}
		record.CompanyID = &companyID
		record.EntityType = "COMPANY_VK_ACCOUNT"
		record.EntityID = &id
		return nil
	}
	if strings.Contains(route, "/platform-accounts/{id}") && id != "" {
		var companyID string
		if err := s.pool.QueryRow(ctx, `SELECT company_id::text FROM platform_accounts WHERE id=$1 AND organization_id=$2`, id, p.OrganizationID).Scan(&companyID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("resolve platform account audit target: %w", err)
		} else if err == nil {
			record.CompanyID = &companyID
		}
		record.EntityType = "PLATFORM_ACCOUNT"
		record.EntityID = &id
		return nil
	}
	if strings.Contains(route, "/users/{id}") && id != "" {
		record.EntityType = "USER"
		record.EntityID = &id
	}
	return nil
}

func (w *bufferedAuditResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *bufferedAuditResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(data)
}

func (w *bufferedAuditResponse) flush(target http.ResponseWriter) {
	for key, values := range w.header {
		for _, value := range values {
			target.Header().Add(key, value)
		}
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	target.WriteHeader(status)
	_, _ = target.Write(w.body.Bytes())
}

// auditAuthenticatedBusiness is installed only after authentication. It
// buffers the result so reveal/export bytes are never released unless their
// mandatory audit record was durably inserted. We apply the same invariant to
// every successful business response, making endpoint coverage fail closed.
func (s *Server) auditAuthenticatedBusiness(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture := newBufferedAuditResponse()
		next.ServeHTTP(capture, r)
		status := capture.status
		if status == 0 {
			status = http.StatusOK
		}
		if status < 200 || status >= 300 {
			capture.flush(w)
			return
		}

		p := r.Context().Value(principalKey).(principal)
		route := chi.RouteContext(r.Context()).RoutePattern()
		action, covered := auditEndpointCoverage[r.Method+" "+route]
		if !covered {
			problem(w, http.StatusInternalServerError, "audit coverage missing", "the endpoint has no audit action")
			return
		}
		endpoint := r.Method + " " + route
		if _, mutation := auditAtomicMutationCoverage[endpoint]; mutation && !capture.auditCommitted {
			problem(w, http.StatusInternalServerError, "transactional audit missing", "the mutation did not commit its audit record atomically")
			return
		}
		if capture.auditCommitted {
			capture.flush(w)
			return
		}
		record := requestAuditRecord(r, p, p.ActiveCompanyID, action, "ENDPOINT", nil, status, map[string]any{})
		if err := s.resolveRequestAuditTarget(r.Context(), r, p, &record); err != nil {
			problem(w, http.StatusInternalServerError, "audit unavailable", "the operation audit target could not be resolved")
			return
		}
		if err := s.writeAudit(r.Context(), s.pool, record); err != nil {
			for key := range w.Header() {
				w.Header().Del(key)
			}
			problem(w, http.StatusInternalServerError, "audit unavailable", "the operation could not be audited")
			return
		}
		capture.flush(w)
	})
}

func auditAction(method, route string) string {
	var action strings.Builder
	action.WriteString("HTTP_")
	action.WriteString(method)
	for _, character := range route {
		switch {
		case unicode.IsLetter(character) || unicode.IsDigit(character):
			action.WriteRune(unicode.ToUpper(character))
		case action.Len() > 0 && !strings.HasSuffix(action.String(), "_"):
			action.WriteByte('_')
		}
	}
	return strings.TrimRight(action.String(), "_")
}

func (s *Server) listAuditLogs(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			problem(w, http.StatusBadRequest, "invalid limit", "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			problem(w, http.StatusBadRequest, "invalid offset", "offset must be non-negative")
			return
		}
		offset = parsed
	}
	action := strings.TrimSpace(r.URL.Query().Get("action"))
	companyID := strings.TrimSpace(r.URL.Query().Get("companyId"))
	actorID := strings.TrimSpace(r.URL.Query().Get("actorId"))
	entityType := strings.TrimSpace(r.URL.Query().Get("entityType"))
	dateFrom, err := auditDateBoundary(r.URL.Query().Get("dateFrom"), false)
	if err != nil {
		problem(w, http.StatusBadRequest, "invalid dateFrom", "dateFrom must use YYYY-MM-DD")
		return
	}
	dateTo, err := auditDateBoundary(r.URL.Query().Get("dateTo"), true)
	if err != nil {
		problem(w, http.StatusBadRequest, "invalid dateTo", "dateTo must use YYYY-MM-DD")
		return
	}
	if dateFrom != nil && dateTo != nil && !dateFrom.Before(*dateTo) {
		problem(w, http.StatusBadRequest, "invalid date range", "dateFrom must not be after dateTo")
		return
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT id::text,actor_id::text,company_id::text,action,entity_type,
		       entity_id::text,COALESCE(endpoint,''),COALESCE(http_method,''),
		       response_status,metadata,created_at
		FROM audit_logs
		WHERE organization_id=$1
		  AND ($2='' OR action=$2)
		  AND ($3='' OR company_id=$3::uuid)
		  AND ($4='' OR actor_id=$4::uuid)
		  AND ($5='' OR entity_type=$5)
		  AND ($6::timestamptz IS NULL OR created_at >= $6)
		  AND ($7::timestamptz IS NULL OR created_at < $7)
		ORDER BY created_at DESC,id DESC
		LIMIT $8 OFFSET $9
	`, p.OrganizationID, action, companyID, actorID, entityType, dateFrom, dateTo, limit, offset)
	if err != nil {
		problem(w, http.StatusInternalServerError, "audit failed", "could not load audit records")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0, limit)
	for rows.Next() {
		var id, auditActionValue, auditEntityType, endpoint, method string
		var actor, company, entity *string
		var responseStatus *int
		var metadata map[string]any
		var createdAt time.Time
		if err = rows.Scan(&id, &actor, &company, &auditActionValue, &auditEntityType,
			&entity, &endpoint, &method, &responseStatus, &metadata, &createdAt); err != nil {
			problem(w, http.StatusInternalServerError, "audit failed", "could not read audit records")
			return
		}
		items = append(items, map[string]any{
			"id": id, "actorId": actor, "companyId": company, "action": auditActionValue,
			"entityType": auditEntityType, "entityId": entity, "endpoint": endpoint,
			"httpMethod": method, "responseStatus": responseStatus,
			"metadata": metadata, "createdAt": createdAt,
		})
	}
	if err = rows.Err(); err != nil {
		problem(w, http.StatusInternalServerError, "audit failed", "could not finish reading audit records")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
}

// auditDateBoundary converts a calendar date to a UTC boundary. The upper
// boundary is exclusive and advanced by one day, so dateTo includes the whole
// selected calendar day while pagination remains server-side and stable.
func auditDateBoundary(raw string, upper bool) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	value, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return nil, err
	}
	if upper {
		value = value.AddDate(0, 0, 1)
	}
	return &value, nil
}
