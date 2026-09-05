package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Content notifications are deliberately written by the same transaction as
// the state change. eventKey makes retries idempotent without exposing a
// second delivery queue; it is kept in the redacted payload for deep links.
func (s *Server) writeContentNotification(ctx context.Context, tx pgx.Tx, org, company, item, kind, eventKey string, recipientsSQL string, args ...any) error {
	// Serialize the small dedupe critical section. This keeps the expand-only
	// 00025 schema idempotent even when two workers finalize the same target.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, org+":"+item+":"+kind+":"+eventKey); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"eventKey": eventKey, "contentItemId": item, "href": "/app/publishing?contentItemId=" + item})
	_, err := tx.Exec(ctx, `INSERT INTO content_notifications(organization_id,company_id,recipient_id,content_item_id,kind,payload)
SELECT $1,$2,u.id,$3,$4,$5::jsonb FROM (`+recipientsSQL+`) u
WHERE NOT EXISTS (SELECT 1 FROM content_notifications n WHERE n.organization_id=$1 AND n.recipient_id=u.id AND n.content_item_id=$3 AND n.kind=$4 AND n.payload->>'eventKey'=$6)`, append([]any{org, company, item, kind, payload, eventKey}, args...)...)
	return err
}

func (s *Server) contentNotificationRecipients(ctx context.Context, tx pgx.Tx, org, company, item, kind, eventKey string) error {
	// The recipient query always checks active membership, user and company;
	// archived companies therefore suppress delivery at the source.
	var query string
	switch kind {
	case "APPROVAL_REQUESTED":
		query = `SELECT DISTINCT m.user_id AS id FROM organization_memberships m JOIN users u ON u.id=m.user_id JOIN creators cr ON cr.id=(SELECT creator_id FROM content_items WHERE id=$3 AND organization_id=$1) JOIN companies co ON co.id=cr.company_id AND co.organization_id=$1 LEFT JOIN manager_company_assignments ma ON ma.organization_id=m.organization_id AND ma.manager_user_id=m.user_id AND ma.company_id=$2 LEFT JOIN manager_company_permissions mp ON mp.assignment_id=ma.id AND mp.permission='CONTENT_APPROVE' WHERE m.organization_id=$1 AND u.status='ACTIVE' AND m.user_id <> COALESCE((SELECT requested_by FROM content_approval_requests WHERE content_item_id=$3 AND organization_id=$1 AND status='PENDING' ORDER BY created_at DESC LIMIT 1),'00000000-0000-0000-0000-000000000000') AND cr.company_id=$2 AND cr.archived_at IS NULL AND co.archived_at IS NULL AND (m.membership_role='OWNER' OR (m.membership_role='MANAGER' AND ma.id IS NOT NULL AND mp.assignment_id IS NOT NULL))`
	case "APPROVAL_DECIDED":
		query = `SELECT DISTINCT x.id FROM (SELECT requested_by AS id FROM content_approval_requests WHERE content_item_id=$3 AND organization_id=$1 UNION SELECT c.login_user_id FROM creators c JOIN content_items i ON i.creator_id=c.id JOIN companies co ON co.id=i.company_id AND co.organization_id=$1 WHERE i.id=$3 AND i.organization_id=$1 AND co.archived_at IS NULL) x JOIN users u ON u.id=x.id JOIN organization_memberships m ON m.user_id=x.id AND m.organization_id=$1 WHERE u.status='ACTIVE'`
	case "REAUTH_REQUIRED":
		query = `SELECT DISTINCT x.id FROM (SELECT c.login_user_id AS id FROM creators c JOIN content_items i ON i.creator_id=c.id JOIN companies co ON co.id=i.company_id AND co.organization_id=$1 WHERE i.id=$3 AND i.organization_id=$1 AND co.archived_at IS NULL AND c.login_user_id IS NOT NULL UNION SELECT i.created_by FROM content_items i JOIN companies co ON co.id=i.company_id AND co.organization_id=$1 WHERE i.id=$3 AND i.organization_id=$1 AND co.archived_at IS NULL AND i.created_by IS NOT NULL UNION SELECT m.user_id FROM organization_memberships m JOIN manager_company_assignments ma ON ma.organization_id=m.organization_id AND ma.manager_user_id=m.user_id AND ma.company_id=$2 JOIN manager_company_permissions mp ON mp.assignment_id=ma.id AND mp.permission='SOCIAL_CONNECT' WHERE m.organization_id=$1 AND m.membership_role='MANAGER' UNION SELECT m.user_id FROM organization_memberships m WHERE m.organization_id=$1 AND m.membership_role='OWNER') x JOIN users u ON u.id=x.id JOIN organization_memberships m ON m.user_id=x.id AND m.organization_id=$1 WHERE u.status='ACTIVE'`
	default:
		query = `SELECT DISTINCT x.id FROM (SELECT c.login_user_id AS id FROM creators c JOIN content_items i ON i.creator_id=c.id JOIN companies co ON co.id=i.company_id AND co.organization_id=$1 WHERE i.id=$3 AND i.organization_id=$1 AND co.archived_at IS NULL AND c.login_user_id IS NOT NULL UNION SELECT i.created_by FROM content_items i JOIN companies co ON co.id=i.company_id AND co.organization_id=$1 WHERE i.id=$3 AND i.organization_id=$1 AND co.archived_at IS NULL AND i.created_by IS NOT NULL) x JOIN users u ON u.id=x.id JOIN organization_memberships m ON m.user_id=x.id AND m.organization_id=$1 WHERE u.status='ACTIVE'`
	}
	return s.writeContentNotification(ctx, tx, org, company, item, kind, eventKey, query)
}

func (s *Server) contentNotificationForRevision(ctx context.Context, tx pgx.Tx, org, company, revision, kind, eventKey string) error {
	var item string
	if err := tx.QueryRow(ctx, `SELECT content_item_id::text FROM content_revisions WHERE id=$1 AND organization_id=$2`, revision, org).Scan(&item); err != nil {
		return err
	}
	return s.contentNotificationRecipients(ctx, tx, org, company, item, kind, eventKey)
}

type contentNotification struct {
	ID, Kind, ContentItemID, CreatedAt string
	Payload                            map[string]any
	ReadAt                             *string
}

func (s *Server) listContentNotifications(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 50
	}
	args := []any{p.OrganizationID, p.ID, limit}
	cursor := strings.TrimSpace(r.URL.Query().Get("before"))
	clause := ""
	if cursor != "" {
		parts := strings.SplitN(cursor, ",", 2)
		if len(parts) != 2 {
			problem(w, 400, "invalid cursor", "before must be createdAt,id")
			return
		}
		clause = " AND (n.created_at,n.id) < ($4::timestamptz,$5::uuid)"
		args = []any{p.OrganizationID, p.ID, limit, parts[0], parts[1]}
	}
	rows, err := s.pool.Query(r.Context(), `SELECT id::text,kind,COALESCE(content_item_id::text,''),payload,read_at::text,created_at::text FROM content_notifications n WHERE n.organization_id=$1 AND n.recipient_id=$2 AND EXISTS (SELECT 1 FROM companies c WHERE c.id=n.company_id AND c.organization_id=n.organization_id AND c.archived_at IS NULL)`+clause+` ORDER BY created_at DESC,id DESC LIMIT $3`, args...)
	if err != nil {
		problem(w, 500, "notifications failed", "could not list notifications")
		return
	}
	defer rows.Close()
	items := make([]contentNotification, 0)
	for rows.Next() {
		var n contentNotification
		var raw []byte
		if err = rows.Scan(&n.ID, &n.Kind, &n.ContentItemID, &raw, &n.ReadAt, &n.CreatedAt); err != nil {
			problem(w, 500, "notifications failed", "could not read notifications")
			return
		}
		_ = json.Unmarshal(raw, &n.Payload)
		items = append(items, n)
	}
	var unread int
	_ = s.pool.QueryRow(r.Context(), `SELECT count(*) FROM content_notifications n WHERE n.organization_id=$1 AND n.recipient_id=$2 AND n.read_at IS NULL AND EXISTS (SELECT 1 FROM companies c WHERE c.id=n.company_id AND c.organization_id=n.organization_id AND c.archived_at IS NULL)`, p.OrganizationID, p.ID).Scan(&unread)
	var next string
	if len(items) == limit {
		last := items[len(items)-1]
		next = last.CreatedAt + "," + last.ID
	}
	writeJSON(w, 200, map[string]any{"items": items, "unreadCount": unread, "nextBefore": next})
}
func (s *Server) markContentNotificationRead(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	id := chi.URLParam(r, "notificationID")
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, 500, "notifications failed", "could not start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	tag, err := tx.Exec(r.Context(), `UPDATE content_notifications SET read_at=COALESCE(read_at,now()) WHERE id=$1 AND organization_id=$2 AND recipient_id=$3`, id, p.OrganizationID, p.ID)
	if err != nil {
		problem(w, 500, "notifications failed", "could not mark notification")
		return
	}
	if tag.RowsAffected() == 0 {
		problem(w, 404, "not found", "notification is not available")
		return
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, nil, "MARK_NOTIFICATION_READ", "CONTENT_NOTIFICATION", &id, http.StatusOK, nil)); err != nil {
		problem(w, 500, "audit failed", "could not record notification read")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, 500, "notifications failed", "could not commit notification read")
		return
	}
	markResponseAuditCommitted(w)
	writeJSON(w, 200, map[string]any{"status": "read"})
}
func (s *Server) markAllContentNotificationsRead(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, 500, "notifications failed", "could not start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `UPDATE content_notifications SET read_at=COALESCE(read_at,now()) WHERE organization_id=$1 AND recipient_id=$2 AND read_at IS NULL`, p.OrganizationID, p.ID); err != nil {
		problem(w, 500, "notifications failed", "could not mark notifications")
		return
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, nil, "MARK_ALL_NOTIFICATIONS_READ", "CONTENT_NOTIFICATION", nil, http.StatusOK, nil)); err != nil {
		problem(w, 500, "audit failed", "could not record notification read")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, 500, "notifications failed", "could not commit notification read")
		return
	}
	markResponseAuditCommitted(w)
	writeJSON(w, 200, map[string]any{"status": "read"})
}
