package httpserver

// The content API keeps publishing as a user-facing domain object rather than
// smuggling drafts into the analytics projection.  Provider execution lives in
// content_worker.go; handlers only make short, audited state transitions.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type contentTargetInput struct {
	PlatformAccountID string          `json:"platformAccountId"`
	Platform          string          `json:"platform"`
	MediaAssetID      *string         `json:"mediaAssetId"`
	Options           json.RawMessage `json:"options"`
}
type contentInput struct {
	CreatorID       string               `json:"creatorId"`
	Description     string               `json:"description"`
	Hashtags        []string             `json:"hashtags"`
	PlatformOptions json.RawMessage      `json:"platformOptions"`
	Targets         []contentTargetInput `json:"targets"`
}

func contentItemID(r *http.Request) string {
	if id := chi.URLParam(r, "itemID"); id != "" {
		return id
	}
	return chi.URLParam(r, "id")
}

func contentRouteCreator(r *http.Request, p principal, supplied string) (string, string, int) {
	if p.Role == roleCreator {
		profile, ok := activeCreatorContext(p)
		if !ok {
			return "", "", http.StatusForbidden
		}
		return profile.ID, profile.CompanyID, 0
	}
	if strings.TrimSpace(supplied) == "" {
		return "", "", http.StatusBadRequest
	}
	return supplied, "", 0
}
func contentPermission(p principal, permission string) bool {
	return p.Role == roleCreator || p.hasPermission(permission)
}
func normalizedHashtags(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, raw := range values {
		tag := strings.TrimSpace(strings.TrimPrefix(raw, "#"))
		if tag == "" {
			continue
		}
		tag = "#" + tag
		key := strings.ToLower(tag)
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			result = append(result, tag)
		}
	}
	sort.Strings(result)
	return result
}
func commandHash(r *http.Request, payload any) ([]byte, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(append([]byte(r.Method+" "+r.URL.Path+"\n"), b...))
	return sum[:], nil
}
func mustIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) < 8 || len(key) > 255 {
		problem(w, http.StatusBadRequest, "idempotency key required", "Idempotency-Key must be 8-255 characters")
		return "", false
	}
	return key, true
}

func contentETag(revision int) string { return `"revision-` + strconv.Itoa(revision) + `"` }

func matchesContentETag(header string, revision int) bool {
	expected := contentETag(revision)
	for _, candidate := range strings.Split(header, ",") {
		if strings.TrimSpace(candidate) == expected {
			return true
		}
	}
	return false
}

func (s *Server) assertContentItem(ctx context.Context, p principal, itemID, permission string) (creatorID, companyID string, revision int, code int) {
	err := s.pool.QueryRow(ctx, `SELECT creator_id::text,company_id::text,current_revision FROM content_items WHERE id=$1 AND organization_id=$2 AND cancelled_at IS NULL`, itemID, p.OrganizationID).Scan(&creatorID, &companyID, &revision)
	if err != nil {
		return "", "", 0, http.StatusNotFound
	}
	if p.Role == roleCreator {
		profile, ok := activeCreatorContext(p)
		if !ok || profile.ID != creatorID || profile.CompanyID != companyID {
			return "", "", 0, http.StatusNotFound
		}
	} else if status := authorizeCompanyAction(p, companyID, permission); status != 0 {
		return "", "", 0, status
	} else if !contentPermission(p, permission) {
		return "", "", 0, http.StatusForbidden
	}
	return creatorID, companyID, revision, 0
}

func (s *Server) createContentItem(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	var in contentInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		problem(w, 400, "invalid request", "expected content item JSON")
		return
	}
	creatorID, companyID, code := contentRouteCreator(r, p, in.CreatorID)
	if code != 0 {
		problem(w, code, "forbidden", "creator scope is required")
		return
	}
	if p.Role != roleCreator {
		status := s.authorizeCreatorAction(r.Context(), p, creatorID, "CONTENT_CREATE")
		if status != 0 {
			problem(w, status, "forbidden", "cannot create content for this creator")
			return
		}
	}
	key, ok := mustIdempotencyKey(w, r)
	if !ok {
		return
	}
	hash, err := commandHash(r, in)
	if err != nil {
		problem(w, 500, "request failed", "could not hash request")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, 500, "request failed", "could not start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	finishIdempotency, proceed := claimContentCommandIdempotency(w, r, tx, p, key, hash)
	if !proceed {
		_ = tx.Commit(r.Context())
		return
	}
	var itemID string
	err = tx.QueryRow(r.Context(), `INSERT INTO content_items(organization_id,company_id,creator_id,created_by) SELECT c.organization_id,c.company_id,c.id,$2 FROM creators c JOIN companies x ON x.id=c.company_id AND x.organization_id=c.organization_id WHERE c.id=$1 AND c.organization_id=$3 AND c.archived_at IS NULL AND x.archived_at IS NULL RETURNING id`, creatorID, p.ID, p.OrganizationID).Scan(&itemID)
	if err != nil {
		problem(w, 404, "not found", "creator is not available")
		return
	}
	if companyID == "" {
		_ = companyID
	}
	if err = s.insertContentRevision(r.Context(), tx, itemID, p.OrganizationID, 1, p.ID, in, "DRAFT"); err != nil {
		problem(w, 400, "invalid content", "could not create targets for this creator")
		return
	}
	response := map[string]any{"id": itemID, "revision": 1, "status": "DRAFT"}
	if err = finishIdempotency(http.StatusCreated, response); err != nil {
		problem(w, 500, "request failed", "could not persist idempotency state")
		return
	}
	company := companyID
	if company == "" {
		_ = tx.QueryRow(r.Context(), `SELECT company_id::text FROM content_items WHERE id=$1`, itemID).Scan(&company)
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &company, "CREATE_CONTENT_ITEM", "CONTENT_ITEM", &itemID, 201, map[string]any{"revision": 1})); err != nil {
		problem(w, 500, "audit failed", "could not record content creation")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, 500, "request failed", "could not commit content creation")
		return
	}
	markResponseAuditCommitted(w)
	writeContentCommandResponse(w, http.StatusCreated, response)
}

func (s *Server) insertContentRevision(ctx context.Context, tx pgx.Tx, itemID, org string, revision int, actor string, in contentInput, status string) error {
	if len(in.Targets) == 0 {
		return errors.New("at least one publish target is required")
	}
	var companyID, creatorID string
	if err := tx.QueryRow(ctx, `SELECT company_id::text,creator_id::text FROM content_items WHERE id=$1 AND organization_id=$2`, itemID, org).Scan(&companyID, &creatorID); err != nil {
		return err
	}
	// Resolve every supplied resource against the item's complete ownership
	// tuple before creating a revision. A false result deliberately covers both
	// an unknown id and a known foreign id, so this validation cannot disclose
	// another creator's media or account.
	for _, target := range in.Targets {
		var accountOK bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM platform_accounts account
				WHERE account.id=$1 AND account.organization_id=$2
				  AND account.company_id=$3 AND account.platform=$4::platform
				  AND (
					EXISTS (SELECT 1 FROM creator_account_assignments assignment
						WHERE assignment.creator_id=$5 AND assignment.platform_account_id=account.id
						  AND assignment.organization_id=$2 AND assignment.valid_to IS NULL)
					OR ($4::platform='VK' AND EXISTS (SELECT 1 FROM company_vk_accounts vk
						WHERE vk.platform_account_id=account.id AND vk.organization_id=$2 AND vk.company_id=$3))
				  )
			)`, target.PlatformAccountID, org, companyID, target.Platform, creatorID).Scan(&accountOK); err != nil || !accountOK {
			return errors.New("publish target account is not available for this creator")
		}
		if target.MediaAssetID != nil {
			var mediaOK bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM media_assets WHERE id=$1 AND organization_id=$2 AND company_id=$3 AND creator_id=$4)`, *target.MediaAssetID, org, companyID, creatorID).Scan(&mediaOK); err != nil || !mediaOK {
				return errors.New("publish target media is not available for this creator")
			}
		}
	}
	options := in.PlatformOptions
	if len(options) == 0 {
		options = []byte("{}")
	}
	var revisionID string
	description := strings.TrimSpace(in.Description)
	tags := normalizedHashtags(in.Hashtags)
	if len(tags) > 0 {
		description = strings.TrimSpace(description + "\n\n" + strings.Join(tags, " "))
	}
	if err := tx.QueryRow(ctx, `INSERT INTO content_revisions(content_item_id,organization_id,revision,description,hashtags,platform_options,status,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, itemID, org, revision, description, tags, options, status, actor).Scan(&revisionID); err != nil {
		return err
	}
	for _, target := range in.Targets {
		opts := target.Options
		if len(opts) == 0 {
			opts = []byte("{}")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO content_publish_targets(content_revision_id,organization_id,company_id,creator_id,platform_account_id,platform,media_asset_id,platform_options,status) SELECT $1,$2,i.company_id,i.creator_id,$3,$4,$5,$6,'READY' FROM content_items i WHERE i.id=$7 AND i.organization_id=$2`, revisionID, org, target.PlatformAccountID, target.Platform, target.MediaAssetID, opts, itemID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) listContentItems(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			problem(w, 400, "invalid limit", "limit must be between 1 and 100")
			return
		}
		limit = n
	}
	var beforeAt time.Time
	beforeID := ""
	if before := r.URL.Query().Get("before"); before != "" {
		parts := strings.SplitN(before, ",", 2)
		parsed, err := time.Parse(time.RFC3339Nano, parts[0])
		if err != nil || len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
			problem(w, 400, "invalid cursor", "before must be updatedAt,id")
			return
		}
		beforeAt, beforeID = parsed, parts[1]
	}
	fromRaw, untilRaw := r.URL.Query().Get("from"), r.URL.Query().Get("until")
	var from, until time.Time
	if fromRaw != "" || untilRaw != "" {
		var err error
		if fromRaw == "" || untilRaw == "" {
			problem(w, 400, "invalid calendar range", "from and until are required together")
			return
		}
		from, err = time.Parse(time.RFC3339, fromRaw)
		if err != nil {
			problem(w, 400, "invalid calendar range", "from must be RFC3339")
			return
		}
		until, err = time.Parse(time.RFC3339, untilRaw)
		if err != nil || !until.After(from) || until.Sub(from) > 32*24*time.Hour {
			problem(w, 400, "invalid calendar range", "until must be RFC3339 and span at most 32 days")
			return
		}
	}
	// A content item is a planning record, not an analytics publication.  Read
	// the schedule from its current targets so a partial multi-platform run is
	// represented once in both the list and calendar.
	q := `SELECT i.id::text AS id,i.creator_id::text AS creator_id,i.current_revision,i.updated_at,COALESCE(c.display_name,'') AS creator_name,COALESCE(min(t.scheduled_at),i.updated_at) AS scheduled_at,count(t.id) AS target_count,count(t.id) FILTER (WHERE t.status IN ('FAILED','WAITING_FOR_REAUTH','RETRY_SCHEDULED')) AS attention_count,array_agg(DISTINCT t.platform::text ORDER BY t.platform::text) AS platforms FROM content_items i JOIN creators c ON c.id=i.creator_id AND c.organization_id=i.organization_id JOIN content_revisions r ON r.content_item_id=i.id AND r.organization_id=i.organization_id AND r.revision=i.current_revision LEFT JOIN content_publish_targets t ON t.content_revision_id=r.id AND t.organization_id=i.organization_id WHERE i.organization_id=$1 AND i.cancelled_at IS NULL`
	args := []any{p.OrganizationID}
	if p.Role == roleCreator {
		profile, ok := activeCreatorContext(p)
		if !ok {
			problem(w, 403, "forbidden", "creator context required")
			return
		}
		q += ` AND i.creator_id=$2`
		args = append(args, profile.ID)
	} else if p.ActiveCompanyID != nil {
		q += ` AND i.company_id=$2`
		args = append(args, *p.ActiveCompanyID)
	}
	q += ` GROUP BY i.id,c.display_name`
	q = `SELECT * FROM (` + q + `) item WHERE true`
	if !from.IsZero() {
		q += ` AND item.scheduled_at >= $` + strconv.Itoa(len(args)+1) + ` AND item.scheduled_at < $` + strconv.Itoa(len(args)+2)
		args = append(args, from, until)
	}
	if !beforeAt.IsZero() {
		q += ` AND (item.updated_at,item.id) < ($` + strconv.Itoa(len(args)+1) + `,$` + strconv.Itoa(len(args)+2) + `)`
		args = append(args, beforeAt, beforeID)
	}
	q += ` ORDER BY item.updated_at DESC,item.id DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit+1)
	rows, err := s.pool.Query(r.Context(), q, args...)
	if err != nil {
		problem(w, 500, "content failed", "could not list content")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	nextBefore := ""
	for rows.Next() {
		var id, creator, creatorName string
		var rev int
		var updated, scheduled time.Time
		var targetCount, attentionCount int
		var platforms []string
		if err = rows.Scan(&id, &creator, &rev, &updated, &creatorName, &scheduled, &targetCount, &attentionCount, &platforms); err != nil {
			problem(w, 500, "content failed", "could not read content")
			return
		}
		if len(items) == limit {
			nextBefore = updated.UTC().Format(time.RFC3339Nano) + "," + id
			continue
		}
		items = append(items, map[string]any{"id": id, "creatorId": creator, "creatorName": creatorName, "revision": rev, "updatedAt": updated, "scheduledAt": scheduled, "targetCount": targetCount, "attentionCount": attentionCount, "platforms": platforms})
	}
	writeJSON(w, 200, map[string]any{"items": items, "nextBefore": nextBefore})
}
func (s *Server) getContentItem(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	id := contentItemID(r)
	creator, company, rev, code := s.assertContentItem(r.Context(), p, id, "CONTENT_VIEW")
	if code != 0 {
		problem(w, code, "not found", "content item is not available")
		return
	}
	var description, status string
	var tags []string
	var options []byte
	err := s.pool.QueryRow(r.Context(), `SELECT description,status,hashtags,platform_options FROM content_revisions WHERE content_item_id=$1 AND organization_id=$2 AND revision=$3`, id, p.OrganizationID, rev).Scan(&description, &status, &tags, &options)
	if err != nil {
		problem(w, 500, "content failed", "could not read current revision")
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT id::text,platform::text,platform_account_id::text,status,COALESCE(external_id,''),COALESCE(external_url,''),COALESCE(media_asset_id::text,''),COALESCE(error_code,''),COALESCE(error_message,''),scheduled_at,created_at,updated_at FROM content_publish_targets WHERE content_revision_id=(SELECT id FROM content_revisions WHERE content_item_id=$1 AND organization_id=$2 AND revision=$3) ORDER BY platform, id`, id, p.OrganizationID, rev)
	if err != nil {
		problem(w, 500, "content failed", "could not read targets")
		return
	}
	defer rows.Close()
	targets := []map[string]any{}
	for rows.Next() {
		var tid, platform, account, state, externalID, externalURL, media, ec, em string
		var scheduled *time.Time
		var created, updated time.Time
		if err = rows.Scan(&tid, &platform, &account, &state, &externalID, &externalURL, &media, &ec, &em, &scheduled, &created, &updated); err != nil {
			problem(w, 500, "content failed", "could not read target")
			return
		}
		targets = append(targets, publicContentTarget(tid, platform, account, state, externalID, externalURL, media, ec, em, scheduled, created, updated))
	}
	approval := map[string]any{}
	var approvalID, approvalStatus, requester, decider, note string
	var requested time.Time
	var decided *time.Time
	err = s.pool.QueryRow(r.Context(), `SELECT a.id::text,a.status,COALESCE(requester.email,''),COALESCE(decider.email,''),a.decision_note,a.created_at,a.decided_at FROM content_approval_requests a LEFT JOIN users requester ON requester.id=a.requested_by LEFT JOIN users decider ON decider.id=a.decided_by WHERE a.content_item_id=$1 AND a.organization_id=$2 AND a.content_revision_id=(SELECT id FROM content_revisions WHERE content_item_id=$1 AND organization_id=$2 AND revision=$3) ORDER BY a.created_at DESC LIMIT 1`, id, p.OrganizationID, rev).Scan(&approvalID, &approvalStatus, &requester, &decider, &note, &requested, &decided)
	if err == nil {
		approval = map[string]any{"id": approvalID, "status": approvalStatus, "requester": requester, "decider": decider, "note": note, "requestedAt": requested, "decidedAt": decided}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		problem(w, 500, "content failed", "could not read approval")
		return
	}
	w.Header().Set("ETag", contentETag(rev))
	writeJSON(w, 200, map[string]any{"id": id, "creatorId": creator, "companyId": company, "revision": rev, "description": description, "hashtags": tags, "platformOptions": json.RawMessage(options), "status": status, "approval": approval, "targets": targets, "availableActions": s.contentActions(p, creator, company, status)})
}

// publicContentTarget is shared by management and creator detail responses.
// Provider operation IDs and raw provider evidence never enter ordinary API
// JSON. A completed publication may expose only a bounded public identifier and
// a canonical, platform-owned permalink that has passed the strict allowlist.
func publicContentTarget(id, platform, account, status, externalID, externalURL, media, errorCode, errorMessage string, scheduled *time.Time, created, updated time.Time) map[string]any {
	target := map[string]any{
		"id": id, "platform": platform, "platformAccountId": account,
		"status": status, "mediaAssetId": media, "errorCode": publicContentErrorCode(errorCode),
		"errorMessage": publicContentAttemptMessage(errorMessage),
		"scheduledAt":  scheduled, "createdAt": created, "updatedAt": updated,
	}
	if safeID := publicExternalID(platform, status, externalID); safeID != "" {
		target["externalId"] = safeID
		if permalink := canonicalPublicationURL(platform, safeID, externalURL); permalink != "" {
			target["externalUrl"] = permalink
		}
	}
	return target
}

var publicExternalIDPatterns = map[string]*regexp.Regexp{
	"YOUTUBE":   regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`),
	"INSTAGRAM": regexp.MustCompile(`^[0-9]{1,32}$`),
	"VK":        regexp.MustCompile(`^-?[1-9][0-9]{0,18}_[1-9][0-9]{0,18}$`),
	"TIKTOK":    regexp.MustCompile(`^[0-9]{6,32}$`),
}

var canonicalPublicationURLPatterns = map[string]*regexp.Regexp{
	"YOUTUBE":   regexp.MustCompile(`^/shorts/([A-Za-z0-9_-]{11})/?$`),
	"INSTAGRAM": regexp.MustCompile(`^/reel/([A-Za-z0-9_-]{5,64})/?$`),
	"VK":        regexp.MustCompile(`^/video(-?[1-9][0-9]{0,18}_[1-9][0-9]{0,18})/?$`),
	"TIKTOK":    regexp.MustCompile(`^/@([A-Za-z0-9._]{2,24})/video/([0-9]{6,32})/?$`),
}

func publicExternalID(platform, status, value string) string {
	if status != "SUCCEEDED" {
		return ""
	}
	value = strings.TrimSpace(value)
	pattern := publicExternalIDPatterns[strings.ToUpper(platform)]
	if pattern == nil || !pattern.MatchString(value) {
		return ""
	}
	return value
}

func canonicalPublicationURL(platform, externalID, value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 || containsOpaqueCapability(value) {
		return ""
	}
	lower := strings.ToLower(value)
	for _, poison := range []string{"x-amz", "upload", "session", "opaque", "base64"} {
		if strings.Contains(lower, poison) {
			return ""
		}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return ""
	}
	platform = strings.ToUpper(platform)
	hosts := map[string]string{
		"YOUTUBE": "www.youtube.com", "INSTAGRAM": "www.instagram.com",
		"VK": "vk.ru", "TIKTOK": "www.tiktok.com",
	}
	host := hosts[platform]
	pattern := canonicalPublicationURLPatterns[platform]
	if host == "" || pattern == nil || strings.ToLower(parsed.Hostname()) != host {
		return ""
	}
	matches := pattern.FindStringSubmatch(parsed.Path)
	if matches == nil {
		return ""
	}
	switch platform {
	case "YOUTUBE", "VK":
		if matches[1] != externalID {
			return ""
		}
	case "TIKTOK":
		if matches[2] != externalID {
			return ""
		}
	}
	switch platform {
	case "YOUTUBE":
		return "https://www.youtube.com/shorts/" + externalID
	case "INSTAGRAM":
		return "https://www.instagram.com/reel/" + matches[1] + "/"
	case "VK":
		return "https://vk.ru/video" + externalID
	case "TIKTOK":
		return "https://www.tiktok.com/@" + matches[1] + "/video/" + externalID
	}
	return ""
}

func publicContentErrorCode(value string) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) > 64 || strings.ContainsAny(trimmed, "/?=&:+") || containsOpaqueCapability(trimmed) {
		return "PROVIDER_ERROR"
	}
	return trimmed
}

// listContentAttempts is deliberately a separate, lazy detail read: the
// planning list stays compact even when a provider has retried many times.
func (s *Server) listContentAttempts(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	id := contentItemID(r)
	_, _, rev, code := s.assertContentItem(r.Context(), p, id, "CONTENT_VIEW")
	if code != 0 {
		problem(w, code, "not found", "content item is not available")
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 100 {
			problem(w, 400, "invalid limit", "limit must be between 1 and 100")
			return
		}
		limit = n
	}
	args := []any{p.OrganizationID, id, rev}
	where := ""
	if before := r.URL.Query().Get("before"); before != "" {
		parts := strings.SplitN(before, ",", 2)
		at, e := time.Parse(time.RFC3339Nano, parts[0])
		if e != nil || len(parts) != 2 {
			problem(w, 400, "invalid cursor", "before must be startedAt,id")
			return
		}
		where = ` AND (a.started_at,a.id)<($4,$5)`
		args = append(args, at, parts[1])
	}
	args = append(args, limit+1)
	rows, err := s.pool.Query(r.Context(), `SELECT a.id::text,a.target_id::text,a.status,COALESCE(a.error_code,''),COALESCE(a.error_message,''),a.started_at,a.finished_at FROM content_publish_attempts a JOIN content_publish_targets t ON t.id=a.target_id AND t.organization_id=a.organization_id WHERE a.organization_id=$1 AND t.content_revision_id=(SELECT id FROM content_revisions WHERE content_item_id=$2 AND organization_id=$1 AND revision=$3)`+where+` ORDER BY a.started_at DESC,a.id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		problem(w, 500, "content failed", "could not load publish attempts")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	nextBefore := ""
	for rows.Next() {
		var aid, tid, status, ec, em string
		var started time.Time
		var finished *time.Time
		if err = rows.Scan(&aid, &tid, &status, &ec, &em, &started, &finished); err != nil {
			problem(w, 500, "content failed", "could not read publish attempts")
			return
		}
		if len(items) == limit {
			nextBefore = started.UTC().Format(time.RFC3339Nano) + "," + aid
			continue
		}
		items = append(items, map[string]any{"id": aid, "targetId": tid, "status": status, "errorCode": ec, "errorMessage": publicContentAttemptMessage(em), "startedAt": started, "finishedAt": finished})
	}
	if err = rows.Err(); err != nil {
		problem(w, 500, "content failed", "could not finish reading publish attempts")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "nextBefore": nextBefore})
}

func publicContentAttemptMessage(message string) string {
	trimmed := strings.TrimSpace(message)
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "http://") || strings.Contains(lower, "https://") || strings.Contains(lower, "provider_operation") || containsOpaqueCapability(trimmed) {
		return "Publishing failed; manual reconciliation may be required"
	}
	return trimmed
}

func containsOpaqueCapability(value string) bool {
	for _, field := range strings.FieldsFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_' && r != '-'
	}) {
		if len(field) >= 32 {
			return true
		}
	}
	return false
}
func (s *Server) contentActions(p principal, creator, company, status string) []string {
	if p.Role == roleCreator {
		return []string{"edit", "submit", "publish", "schedule", "copy", "cancel"}
	}
	if !p.canAccessCompany(company) {
		return []string{}
	}
	actions := []string{"view", "copy"}
	for _, x := range []string{"CONTENT_EDIT", "CONTENT_APPROVE", "CONTENT_PUBLISH", "CONTENT_DELETE"} {
		if p.hasPermission(x) {
			actions = append(actions, strings.ToLower(strings.TrimPrefix(x, "CONTENT_")))
		}
	}
	return actions
}

func (s *Server) patchContentItem(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	id := contentItemID(r)
	_, company, _, code := s.assertContentItem(r.Context(), p, id, "CONTENT_EDIT")
	if code != 0 {
		problem(w, code, "not found", "content item is not available")
		return
	}
	if p.Role != roleCreator && !p.hasPermission("CONTENT_EDIT") {
		problem(w, 403, "forbidden", "content edit permission is required")
		return
	}
	if strings.TrimSpace(r.Header.Get("If-Match")) == "" {
		problem(w, 428, "revision required", "If-Match is required")
		return
	}
	var in contentInput
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in) != nil {
		problem(w, 400, "invalid request", "expected content item JSON")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, 500, "content failed", "could not start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	var locked int
	if err = tx.QueryRow(r.Context(), `SELECT current_revision FROM content_items WHERE id=$1 AND organization_id=$2 FOR UPDATE`, id, p.OrganizationID).Scan(&locked); err != nil {
		problem(w, 404, "not found", "content item is not available")
		return
	}
	if !matchesContentETag(r.Header.Get("If-Match"), locked) {
		problem(w, 409, "revision conflict", "the content item was changed by another user")
		return
	}
	next := locked + 1
	if err = s.insertContentRevision(r.Context(), tx, id, p.OrganizationID, next, p.ID, in, "DRAFT"); err != nil {
		problem(w, 400, "invalid content", "could not save content targets")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE content_items SET current_revision=$3,updated_at=now() WHERE id=$1 AND organization_id=$2`, id, p.OrganizationID, next); err != nil {
		problem(w, 500, "content failed", "could not update revision")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE content_approval_requests SET status='SUPERSEDED',decided_at=now() WHERE content_item_id=$1 AND organization_id=$2 AND status='PENDING'`, id, p.OrganizationID); err != nil {
		return
	}
	// Superseding a revision invalidates every outstanding target, including a
	// job already handed to a worker.  Running work is not force-killed (the
	// provider call is outside our transaction); the target cancellation marker
	// is observed under that worker's existing finalization lock.
	if _, err = tx.Exec(r.Context(), `UPDATE content_publish_targets SET cancellation_requested_at=now(),status=CASE WHEN status IN ('READY','WAITING_APPROVAL','SCHEDULED','RETRY_SCHEDULED','FAILED') THEN 'CANCELLED' ELSE status END,updated_at=now() WHERE organization_id=$1 AND content_revision_id IN (SELECT id FROM content_revisions WHERE content_item_id=$2 AND revision<$3) AND status NOT IN ('SUCCEEDED','CANCELLED')`, p.OrganizationID, id, next); err != nil {
		problem(w, 500, "content failed", "could not cancel superseded targets")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE content_publish_jobs SET status='CANCELLED',updated_at=now() WHERE organization_id=$1 AND content_revision_id IN (SELECT id FROM content_revisions WHERE content_item_id=$2 AND revision<$3) AND status IN ('READY','RETRY_SCHEDULED')`, p.OrganizationID, id, next); err != nil {
		problem(w, 500, "content failed", "could not cancel superseded jobs")
		return
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &company, "UPDATE_CONTENT_ITEM", "CONTENT_ITEM", &id, 200, map[string]any{"revision": next})); err != nil {
		problem(w, 500, "audit failed", "could not record update")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, 500, "content failed", "could not commit update")
		return
	}
	markResponseAuditCommitted(w)
	writeJSON(w, 200, map[string]any{"id": id, "revision": next, "status": "DRAFT"})
}

func (s *Server) copyContentItem(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	source := contentItemID(r)
	creator, company, rev, code := s.assertContentItem(r.Context(), p, source, "CONTENT_VIEW")
	if code != 0 {
		problem(w, code, "not found", "content item is not available")
		return
	}
	if p.Role != roleCreator && !p.hasPermission("CONTENT_CREATE") {
		problem(w, 403, "forbidden", "content create permission is required")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, 500, "content failed", "could not start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	finishIdempotency, proceed := beginContentCommandIdempotency(w, r, tx, p)
	if !proceed {
		_ = tx.Commit(r.Context())
		return
	}
	var id string
	err = tx.QueryRow(r.Context(), `INSERT INTO content_items(organization_id,company_id,creator_id,created_by) VALUES($1,$2,$3,$4) RETURNING id`, p.OrganizationID, company, creator, p.ID).Scan(&id)
	if err != nil {
		problem(w, 500, "content failed", "could not create copy")
		return
	}
	var desc string
	var tags []string
	var options []byte
	_ = tx.QueryRow(r.Context(), `SELECT description,hashtags,platform_options FROM content_revisions WHERE content_item_id=$1 AND organization_id=$2 AND revision=$3`, source, p.OrganizationID, rev).Scan(&desc, &tags, &options)
	var revisionID string
	err = tx.QueryRow(r.Context(), `INSERT INTO content_revisions(content_item_id,organization_id,revision,description,hashtags,platform_options,status,created_by) VALUES($1,$2,1,$3,$4,$5,'DRAFT',$6) RETURNING id`, id, p.OrganizationID, desc, tags, options, p.ID).Scan(&revisionID)
	if err != nil {
		problem(w, 500, "content failed", "could not copy revision")
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO content_publish_targets(content_revision_id,organization_id,company_id,creator_id,platform_account_id,platform,media_asset_id,platform_options,status) SELECT $1,organization_id,company_id,creator_id,platform_account_id,platform,media_asset_id,platform_options,'READY' FROM content_publish_targets WHERE content_revision_id=(SELECT id FROM content_revisions WHERE content_item_id=$2 AND organization_id=$3 AND revision=$4)`, revisionID, source, p.OrganizationID, rev)
	if err != nil {
		problem(w, 500, "content failed", "could not copy targets")
		return
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &company, "COPY_CONTENT_ITEM", "CONTENT_ITEM", &id, 201, map[string]any{"sourceId": source})); err != nil {
		problem(w, 500, "audit failed", "could not record copy")
		return
	}
	response := map[string]any{"id": id, "revision": 1, "status": "DRAFT"}
	if err = finishIdempotency(http.StatusCreated, response); err != nil {
		problem(w, 500, "content failed", "could not persist idempotency state")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, 500, "content failed", "could not commit copy")
		return
	}
	markResponseAuditCommitted(w)
	writeContentCommandResponse(w, http.StatusCreated, response)
}

func (s *Server) submitContentItem(w http.ResponseWriter, r *http.Request) {
	s.transitionContent(w, r, "submit")
}
func (s *Server) approveContentItem(w http.ResponseWriter, r *http.Request) {
	s.transitionContent(w, r, "approve")
}
func (s *Server) rejectContentItem(w http.ResponseWriter, r *http.Request) {
	s.transitionContent(w, r, "reject")
}
func (s *Server) publishContentItem(w http.ResponseWriter, r *http.Request) {
	s.transitionContent(w, r, "publish")
}
func (s *Server) scheduleContentItem(w http.ResponseWriter, r *http.Request) {
	s.transitionContent(w, r, "schedule")
}
func (s *Server) retryContentTargets(w http.ResponseWriter, r *http.Request) {
	s.transitionContent(w, r, "retry")
}
func (s *Server) cancelContentItem(w http.ResponseWriter, r *http.Request) {
	s.transitionContent(w, r, "cancel")
}

func (s *Server) transitionContent(w http.ResponseWriter, r *http.Request, action string) {
	p := r.Context().Value(principalKey).(principal)
	id := contentItemID(r)
	permission := "CONTENT_PUBLISH"
	if action == "approve" || action == "reject" {
		permission = "CONTENT_APPROVE"
	}
	if action == "cancel" {
		permission = "CONTENT_DELETE"
	}
	creator, company, _, code := s.assertContentItem(r.Context(), p, id, permission)
	if code != 0 {
		problem(w, code, "not found", "content item is not available")
		return
	}
	if p.Role != roleCreator && !p.hasPermission(permission) {
		problem(w, 403, "forbidden", "content permission is required")
		return
	}
	if (action == "publish" || action == "schedule" || action == "retry") && !s.config.ContentPublishingEnabled {
		problem(w, http.StatusServiceUnavailable, "publishing is disabled", "content publishing is not enabled for this environment")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, 500, "content failed", "could not start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	finishIdempotency, proceed := beginContentCommandIdempotency(w, r, tx, p)
	if !proceed {
		// A replay is already written; commit releases the key lock before return.
		_ = tx.Commit(r.Context())
		return
	}
	// Lock the item before resolving its revision.  The previous implementation
	// read current_revision before this transaction, allowing an approval or a
	// publish command to apply to a revision that an edit had just superseded.
	var rev int
	if err = tx.QueryRow(r.Context(), `SELECT current_revision FROM content_items WHERE id=$1 AND organization_id=$2 AND cancelled_at IS NULL FOR UPDATE`, id, p.OrganizationID).Scan(&rev); err != nil {
		problem(w, 404, "not found", "content item is not available")
		return
	}
	revisionID := ""
	var currentState string
	if err = tx.QueryRow(r.Context(), `SELECT id::text,status FROM content_revisions WHERE content_item_id=$1 AND organization_id=$2 AND revision=$3 FOR UPDATE`, id, p.OrganizationID, rev).Scan(&revisionID, &currentState); err != nil {
		problem(w, 404, "not found", "content revision is not available")
		return
	}
	var approvalRequired bool
	if p.Role == roleCreator {
		_ = tx.QueryRow(r.Context(), `SELECT publish_requires_approval FROM creator_content_approval_policies WHERE creator_id=$1 AND organization_id=$2`, creator, p.OrganizationID).Scan(&approvalRequired)
	}
	if action == "approve" || action == "reject" {
		var requester string
		if err = tx.QueryRow(r.Context(), `SELECT requested_by::text FROM content_approval_requests WHERE content_item_id=$1 AND content_revision_id=$2 AND organization_id=$3 AND status='PENDING' FOR UPDATE`, id, revisionID, p.OrganizationID).Scan(&requester); err != nil {
			problem(w, 409, "approval unavailable", "there is no pending approval for the current revision")
			return
		}
		if requester == p.ID {
			problem(w, 403, "forbidden", "the author cannot approve their own request")
			return
		}
	}
	// A creator policy applies only to creator-initiated commands. Managers and
	// owners retain their explicitly granted capability, but cannot borrow a
	// creator's implicit permission.
	if p.Role == roleCreator && approvalRequired && (action == "publish" || action == "schedule" || action == "retry") && currentState != "APPROVED" {
		problem(w, 409, "approval required", "submit the current revision and wait for approval before publishing")
		return
	}
	valid := map[string]map[string]bool{
		"submit":  {"DRAFT": true, "REJECTED": true},
		"approve": {"PENDING_APPROVAL": true}, "reject": {"PENDING_APPROVAL": true},
		"publish":  {"DRAFT": true, "APPROVED": true, "FAILED": true, "PARTIALLY_PUBLISHED": true},
		"schedule": {"DRAFT": true, "APPROVED": true, "FAILED": true, "PARTIALLY_PUBLISHED": true},
		"retry":    {"FAILED": true, "PARTIALLY_PUBLISHED": true, "PUBLISHING": true},
		"cancel":   {"DRAFT": true, "PENDING_APPROVAL": true, "APPROVED": true, "SCHEDULED": true, "PUBLISHING": true, "FAILED": true, "PARTIALLY_PUBLISHED": true},
	}
	if !valid[action][currentState] {
		problem(w, 409, "transition unavailable", "action is not available for the current revision state")
		return
	}
	state := ""
	switch action {
	case "submit":
		state = "PENDING_APPROVAL"
		_, err = tx.Exec(r.Context(), `INSERT INTO content_approval_requests(content_item_id,content_revision_id,organization_id,requested_by) VALUES($1,$2,$3,$4)`, id, revisionID, p.OrganizationID, p.ID)
	case "approve":
		state = "APPROVED"
		_, err = tx.Exec(r.Context(), `UPDATE content_approval_requests SET status='APPROVED',decided_by=$4,decided_at=now() WHERE content_item_id=$1 AND content_revision_id=$2 AND organization_id=$3 AND status='PENDING'`, id, revisionID, p.OrganizationID, p.ID)
	case "reject":
		state = "DRAFT"
		_, err = tx.Exec(r.Context(), `UPDATE content_approval_requests SET status='REJECTED',decided_by=$4,decided_at=now() WHERE content_item_id=$1 AND content_revision_id=$2 AND organization_id=$3 AND status='PENDING'`, id, revisionID, p.OrganizationID, p.ID)
	case "cancel":
		state = "CANCELLED"
		_, err = tx.Exec(r.Context(), `UPDATE content_approval_requests SET status='CANCELLED',decided_at=now() WHERE content_item_id=$1 AND content_revision_id=$2 AND organization_id=$3 AND status='PENDING'`, id, revisionID, p.OrganizationID)
		if err != nil {
			break
		}
		_, err = tx.Exec(r.Context(), `UPDATE content_publish_targets SET status='CANCELLED',cancellation_requested_at=now(),updated_at=now() WHERE content_revision_id=$1 AND status NOT IN ('SUCCEEDED','CANCELLED')`, revisionID)
		if err != nil {
			break
		}
		// READY/RETRY jobs have not crossed the provider-I/O fence and may be
		// terminally cancelled in this transaction. RUNNING remains fenced by
		// the target cancellation marker and the worker's finalization lock.
		_, err = tx.Exec(r.Context(), `UPDATE content_publish_jobs SET status='CANCELLED',locked_by=NULL,locked_at=NULL,lease_expires_at=NULL,updated_at=now() WHERE content_revision_id=$1 AND organization_id=$2 AND status IN ('READY','RETRY_SCHEDULED')`, revisionID, p.OrganizationID)
	case "publish", "schedule", "retry":
		state = map[string]string{"publish": "PUBLISHING", "schedule": "SCHEDULED", "retry": "PUBLISHING"}[action]
		runAt := s.now()
		var selectedTargetIDs []string
		var scheduledAt *time.Time
		if action == "schedule" {
			var in struct {
				ScheduledAt time.Time `json:"scheduledAt"`
			}
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.ScheduledAt.IsZero() || !in.ScheduledAt.After(s.now()) {
				problem(w, 400, "invalid schedule", "scheduledAt must be a future RFC3339 timestamp")
				return
			}
			runAt = in.ScheduledAt
			scheduledAt = &runAt
		} else if action == "retry" {
			var in struct {
				TargetIDs []string `json:"targetIds"`
			}
			if r.Body != nil && json.NewDecoder(r.Body).Decode(&in) != nil {
				problem(w, 400, "invalid retry", "targetIds must be a JSON array")
				return
			}
			selectedTargetIDs = in.TargetIDs
			if len(selectedTargetIDs) == 0 {
				problem(w, 400, "invalid retry", "targetIds must contain at least one failed target")
				return
			}
		}
		var invalidTargets int
		mediaQuery := `SELECT count(*) FROM content_publish_targets target LEFT JOIN media_assets media ON media.id=target.media_asset_id AND media.organization_id=target.organization_id WHERE target.content_revision_id=$1 AND (target.media_asset_id IS NULL OR media.status<>'READY')`
		mediaArgs := []any{revisionID}
		if action == "retry" {
			mediaQuery += ` AND target.id=ANY($2::uuid[])`
			mediaArgs = append(mediaArgs, selectedTargetIDs)
		}
		if err = tx.QueryRow(r.Context(), mediaQuery, mediaArgs...).Scan(&invalidTargets); err != nil {
			problem(w, 500, "preflight failed", "could not validate media readiness")
			return
		}
		if invalidTargets > 0 {
			problem(w, 409, "media is not ready", "every publish target requires a validated media asset")
			return
		}
		// Freeze the exact provider payload inputs before any job exists. Retries
		// preserve the first immutable snapshot instead of rebuilding it from
		// mutable target/revision records.
		if action == "retry" && len(selectedTargetIDs) > 0 {
			var valid int
			if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM content_publish_targets WHERE content_revision_id=$1 AND organization_id=$2 AND id=ANY($3::uuid[]) AND status IN ('FAILED','WAITING_FOR_REAUTH','RETRY_SCHEDULED')`, revisionID, p.OrganizationID, selectedTargetIDs).Scan(&valid); err != nil || valid != len(selectedTargetIDs) {
				problem(w, 409, "retry unavailable", "only failed or reauthorization targets can be retried")
				return
			}
		}
		eligible := `target.status IN ('READY','FAILED','RETRY_SCHEDULED')`
		if action == "retry" {
			eligible = `target.status IN ('FAILED','WAITING_FOR_REAUTH','RETRY_SCHEDULED')`
		}
		selection := ""
		scheduleParam := "$3"
		if len(selectedTargetIDs) > 0 {
			selection = ` AND target.id=ANY($3::uuid[])`
			scheduleParam = "$4"
		}
		params := []any{revisionID, p.OrganizationID}
		if len(selectedTargetIDs) > 0 {
			params = append(params, selectedTargetIDs)
		}
		_, err = tx.Exec(r.Context(), `UPDATE content_publish_targets target SET sent_snapshot=jsonb_build_object('revision',revision.revision,'caption',revision.description,'hashtags',revision.hashtags,'platformOptions',target.platform_options,'mediaId',media.id::text,'mediaObjectKey',media.object_key,'mediaSha256',encode(media.content_sha256,'hex'),'mediaDurationMs',media.duration_ms,'accountExternalId',account.external_id,'accountType',COALESCE(account.account_type,''),'connectionMode',COALESCE(account.metadata->>'connectionMode','')),scheduled_at=`+scheduleParam+`,updated_at=now() FROM content_revisions revision,media_assets media,platform_accounts account WHERE target.content_revision_id=$1 AND target.organization_id=$2 AND `+eligible+selection+` AND target.sent_snapshot IS NULL AND revision.id=target.content_revision_id AND revision.organization_id=target.organization_id AND media.id=target.media_asset_id AND media.organization_id=target.organization_id AND media.status='READY' AND account.id=target.platform_account_id AND account.organization_id=target.organization_id`, append(params, scheduledAt)...)
		if err == nil {
			eligibleStatuses := []string{"READY", "FAILED", "RETRY_SCHEDULED"}
			if action == "retry" {
				eligibleStatuses = []string{"FAILED", "WAITING_FOR_REAUTH", "RETRY_SCHEDULED"}
			}
			if err = s.transactionalPublishPreflight(r.Context(), tx, revisionID, p.OrganizationID, selectedTargetIDs, eligibleStatuses); err != nil {
				if errors.Is(err, errTransactionalPublishPreflight) {
					problem(w, http.StatusConflict, "publishing unavailable", "a selected publishing target is no longer authorized or ready")
					return
				}
				problem(w, http.StatusInternalServerError, "preflight failed", "could not validate publishing readiness")
				return
			}
			jobSelection := ""
			jobArgs := []any{revisionID, p.OrganizationID, runAt}
			if len(selectedTargetIDs) > 0 {
				jobSelection = ` AND id=ANY($4::uuid[])`
				jobArgs = append(jobArgs, selectedTargetIDs)
			}
			_, err = tx.Exec(r.Context(), `INSERT INTO content_publish_jobs(target_id,content_revision_id,organization_id,company_id,run_at,status) SELECT id,$1,$2,company_id,$3,'READY' FROM content_publish_targets WHERE content_revision_id=$1 AND organization_id=$2 AND `+strings.TrimPrefix(eligible, "target.")+jobSelection+` AND sent_snapshot IS NOT NULL ON CONFLICT DO NOTHING`, jobArgs...)
		}
	}
	if err != nil {
		problem(w, 409, "transition failed", "content state changed concurrently")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE content_revisions SET status=$1 WHERE id=$2 AND organization_id=$3`, state, revisionID, p.OrganizationID); err != nil {
		return
	}
	// Notification insertion is part of the transition transaction. The event
	// key makes duplicate command retries harmless while recipient queries keep
	// archived companies and inactive users out of the delivery set.
	if action == "submit" {
		if err = s.contentNotificationRecipients(r.Context(), tx, p.OrganizationID, company, id, "APPROVAL_REQUESTED", "approval-request:"+revisionID); err != nil {
			problem(w, 500, "notification failed", "could not create approval notification")
			return
		}
	} else if action == "approve" || action == "reject" {
		if err = s.contentNotificationRecipients(r.Context(), tx, p.OrganizationID, company, id, "APPROVAL_DECIDED", "approval-decision:"+revisionID+":"+action); err != nil {
			problem(w, 500, "notification failed", "could not create decision notification")
			return
		}
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &company, strings.ToUpper(action)+"_CONTENT_ITEM", "CONTENT_ITEM", &id, 200, map[string]any{"revision": rev})); err != nil {
		problem(w, 500, "audit failed", "could not record transition")
		return
	}
	response := map[string]any{"id": id, "revision": rev, "status": state}
	if err = finishIdempotency(http.StatusOK, response); err != nil {
		problem(w, 500, "request failed", "could not persist idempotency state")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, 500, "content failed", "could not commit transition")
		return
	}
	markResponseAuditCommitted(w)
	writeContentCommandResponse(w, http.StatusOK, response)
}

func (s *Server) getContentApprovalPolicy(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	id := chi.URLParam(r, "id")
	if status := s.authorizeCreatorAction(r.Context(), p, id, "CONTENT_VIEW"); status != 0 {
		problem(w, status, "not found", "creator is not available")
		return
	}
	var a, b, c bool
	err := s.pool.QueryRow(r.Context(), `SELECT publish_requires_approval,edit_requires_approval,delete_requires_approval FROM creator_content_approval_policies WHERE creator_id=$1 AND organization_id=$2`, id, p.OrganizationID).Scan(&a, &b, &c)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, 200, map[string]any{"publishRequiresApproval": false, "editRequiresApproval": false, "deleteRequiresApproval": false})
		return
	}
	if err != nil {
		problem(w, 500, "policy failed", "could not load policy")
		return
	}
	writeJSON(w, 200, map[string]any{"publishRequiresApproval": a, "editRequiresApproval": b, "deleteRequiresApproval": c})
}
func (s *Server) putContentApprovalPolicy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PublishRequiresApproval bool `json:"publishRequiresApproval"`
		EditRequiresApproval    bool `json:"editRequiresApproval"`
		DeleteRequiresApproval  bool `json:"deleteRequiresApproval"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, mediaCreateBodyLimit)
	if _, readErr := ioReadAndRestoreBounded(r, mediaCreateBodyLimit); readErr != nil {
		if errors.Is(readErr, errContentCommandBodyTooLarge) || isMaxBytesError(readErr) {
			problem(w, http.StatusRequestEntityTooLarge, "request too large", "approval policy body exceeds 64 KiB")
			return
		}
		problem(w, http.StatusBadRequest, "invalid request", "could not read policy JSON")
		return
	}
	if decodeErr := json.NewDecoder(r.Body).Decode(&in); decodeErr != nil {
		problem(w, 400, "invalid request", "expected policy JSON")
		return
	}
	p := r.Context().Value(principalKey).(principal)
	id := chi.URLParam(r, "id")
	if status := s.authorizeCreatorAction(r.Context(), p, id, "CONTENT_EDIT"); status != 0 {
		problem(w, status, "not found", "creator is not available")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, 500, "policy failed", "could not start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	var company string
	err = tx.QueryRow(r.Context(), `SELECT company_id::text FROM creators WHERE id=$1 AND organization_id=$2`, id, p.OrganizationID).Scan(&company)
	if err != nil {
		problem(w, 404, "not found", "creator is not available")
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO creator_content_approval_policies(creator_id,organization_id,company_id,publish_requires_approval,edit_requires_approval,delete_requires_approval,updated_by) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(creator_id) DO UPDATE SET publish_requires_approval=EXCLUDED.publish_requires_approval,edit_requires_approval=EXCLUDED.edit_requires_approval,delete_requires_approval=EXCLUDED.delete_requires_approval,updated_by=EXCLUDED.updated_by,updated_at=now()`, id, p.OrganizationID, company, in.PublishRequiresApproval, in.EditRequiresApproval, in.DeleteRequiresApproval, p.ID)
	if err != nil {
		problem(w, 500, "policy failed", "could not save policy")
		return
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &company, "UPDATE_CONTENT_APPROVAL_POLICY", "CREATOR", &id, 200, map[string]any{})); err != nil {
		problem(w, 500, "audit failed", "could not record policy")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, 500, "policy failed", "could not commit policy")
		return
	}
	markResponseAuditCommitted(w)
	writeJSON(w, 200, in)
}
