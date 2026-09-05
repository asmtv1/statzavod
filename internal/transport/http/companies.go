package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (s *Server) listCompanies(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	var scopeSQL string
	var scopeArgs []any
	if p.Role == roleOwner && p.ActiveCompanyID == nil {
		// OWNER all-company context intentionally lists the whole organization.
	} else if (p.Role == roleOwner || p.Role == roleManager) && p.ActiveCompanyID != nil && p.hasCompany(*p.ActiveCompanyID) {
		// companies is the scoped resource itself, so scope by its primary key;
		// managementCompanyScope is only for tables that expose company_id.
		scopeSQL = " AND x.id=$2"
		scopeArgs = []any{*p.ActiveCompanyID}
	} else {
		problem(w, http.StatusForbidden, "forbidden", "an assigned active company is required")
		return
	}
	args := append([]any{p.OrganizationID}, scopeArgs...)
	rows, err := s.pool.Query(r.Context(), `
		SELECT x.id,x.name,count(c.id),v.id IS NOT NULL
		FROM companies x
		LEFT JOIN creators c ON c.company_id=x.id AND c.status<>'DISMISSED' AND c.archived_at IS NULL
		LEFT JOIN company_vk_accounts v ON v.company_id=x.id
		WHERE x.organization_id=$1 AND x.archived_at IS NULL`+scopeSQL+`
		GROUP BY x.id,v.id
		ORDER BY x.name`, args...)
	if err != nil {
		problem(w, http.StatusInternalServerError, "companies failed", "could not load companies")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, name string
		var creatorCount int64
		var hasVKAccount bool
		if err := rows.Scan(&id, &name, &creatorCount, &hasVKAccount); err != nil {
			problem(w, http.StatusInternalServerError, "companies failed", "could not read companies")
			return
		}
		items = append(items, map[string]any{"id": id, "name": name, "creatorCount": creatorCount, "hasVkAccount": hasVKAccount})
	}
	if err := rows.Err(); err != nil {
		problem(w, http.StatusInternalServerError, "companies failed", "could not finish reading companies")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) listArchivedCompanies(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	rows, err := s.pool.Query(r.Context(), `
		SELECT x.id::text,x.name,x.archived_at,x.purge_at,count(c.id)
		FROM companies x
		LEFT JOIN creators c ON c.company_id=x.id AND c.organization_id=x.organization_id
		WHERE x.organization_id=$1 AND x.lifecycle_state='ARCHIVED'
		GROUP BY x.id
		ORDER BY x.purge_at,x.id`, p.OrganizationID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not load archived companies")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, name string
		var archivedAt, purgeAt time.Time
		var creatorCount int64
		if err = rows.Scan(&id, &name, &archivedAt, &purgeAt, &creatorCount); err != nil {
			problem(w, http.StatusInternalServerError, "archive failed", "could not read archived companies")
			return
		}
		remaining := purgeAt.Sub(s.now())
		if remaining < 0 {
			remaining = 0
		}
		items = append(items, map[string]any{
			"id": id, "name": name, "archivedAt": archivedAt, "purgeAt": purgeAt,
			"remainingSeconds": int64(remaining / time.Second), "creatorCount": creatorCount,
		})
	}
	if err = rows.Err(); err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not finish reading archived companies")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createCompany(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Name) == "" {
		problem(w, http.StatusBadRequest, "invalid company", "name is required")
		return
	}
	name := strings.TrimSpace(in.Name)
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "creation failed", "could not start company creation")
		return
	}
	defer tx.Rollback(r.Context())
	var id string
	err = tx.QueryRow(r.Context(), `INSERT INTO companies(organization_id,name) VALUES($1,$2) RETURNING id`, p.OrganizationID, name).Scan(&id)
	if err != nil {
		if pgErr, ok := err.(*pgconn.PgError); ok && pgErr.Code == "23505" {
			problem(w, http.StatusConflict, "company exists", "company with this name already exists")
			return
		}
		problem(w, http.StatusInternalServerError, "creation failed", "could not create company")
		return
	}
	companyID := id
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &companyID, "CREATE", "COMPANY", &companyID, http.StatusCreated, map[string]any{})); err != nil {
		problem(w, http.StatusInternalServerError, "creation failed", "could not write audit record")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "creation failed", "could not commit company creation")
		return
	}
	markResponseAuditCommitted(w)
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "name": name})
}

func (s *Server) archiveCompany(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	id := chi.URLParam(r, "id")
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not archive company")
		return
	}
	defer tx.Rollback(r.Context())
	// Creator creation and final OAuth saves hold FOR KEY SHARE from their
	// active-company validation through commit. Take the conflicting row lock
	// before archiving so neither profiles nor ACTIVE provider credentials can
	// be inserted after the lifecycle transition wins.
	var lockedCompanyID string
	if err = tx.QueryRow(r.Context(), `SELECT id::text FROM companies WHERE id=$1 AND organization_id=$2 AND archived_at IS NULL FOR UPDATE`, id, p.OrganizationID).Scan(&lockedCompanyID); errors.Is(err, pgx.ErrNoRows) {
		problem(w, http.StatusNotFound, "not found", "company does not exist")
		return
	} else if err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not lock company")
		return
	}
	now := s.now().UTC()
	purgeAt := now.Add(90 * 24 * time.Hour)
	tag, err := tx.Exec(r.Context(), `UPDATE companies SET archived_at=$3,purge_at=$4,updated_at=$3 WHERE id=$1 AND organization_id=$2 AND archived_at IS NULL`, id, p.OrganizationID, now, purgeAt)
	if err != nil || tag.RowsAffected() == 0 {
		problem(w, http.StatusNotFound, "not found", "company does not exist")
		return
	}
	// Archive is a publishing lifecycle fence. Jobs that have not crossed the
	// provider-I/O boundary become terminal in this transaction; owned work is
	// marked on its target so the authorization-aware heartbeat cancels its
	// context and fenced finalization records a terminal attempt. Successful
	// publications and their referenced media are intentionally untouched.
	if _, err = tx.Exec(r.Context(), `UPDATE content_publish_jobs job SET status='CANCELLED',locked_by=NULL,locked_at=NULL,lease_expires_at=NULL,updated_at=$3 FROM content_publish_targets target WHERE job.target_id=target.id AND job.organization_id=$2 AND job.company_id=$1 AND target.organization_id=$2 AND job.status IN ('READY','RETRY_SCHEDULED')`, id, p.OrganizationID, now); err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not cancel queued publications")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE content_publish_targets target SET cancellation_requested_at=COALESCE(cancellation_requested_at,$3),status=CASE WHEN EXISTS (SELECT 1 FROM content_publish_jobs job WHERE job.target_id=target.id AND job.organization_id=target.organization_id AND job.status='RUNNING') THEN target.status ELSE 'CANCELLED' END,updated_at=$3 WHERE target.company_id=$1 AND target.organization_id=$2 AND target.status NOT IN ('SUCCEEDED','CANCELLED')`, id, p.OrganizationID, now); err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not fence active publications")
		return
	}
	companyIDForCleanup := id
	if err = markArchivedMediaCleanup(r.Context(), tx, p.OrganizationID, &companyIDForCleanup, nil, now); err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not schedule media cleanup")
		return
	}
	// Preserve prior operator intent. Restore only reactivates rows marked by
	// this archive transition; manually paused and reauth-required rows stay so.
	if _, err = tx.Exec(r.Context(), `
		UPDATE oauth_connections connection
		SET status='PAUSED',suspended_for_company_archive=true,updated_at=$3
		FROM platform_accounts account
		WHERE connection.platform_account_id=account.id
		  AND connection.organization_id=account.organization_id
		  AND account.company_id=$1 AND account.organization_id=$2
		  AND connection.status='ACTIVE'`, id, p.OrganizationID, now); err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not pause OAuth connections")
		return
	}
	if _, err = tx.Exec(r.Context(), `
		UPDATE sync_targets
		SET status='PAUSED',suspended_for_company_archive=true
		WHERE company_id=$1 AND organization_id=$2 AND status='ACTIVE'`, id, p.OrganizationID); err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not pause synchronization")
		return
	}
	if _, err = tx.Exec(r.Context(), `
		DELETE FROM oauth_states state
		WHERE state.organization_id=$2 AND (
		  EXISTS(SELECT 1 FROM creators creator WHERE creator.id=state.creator_id AND creator.company_id=$1)
		  OR EXISTS(SELECT 1 FROM company_vk_accounts account WHERE account.id=state.company_vk_account_id AND account.company_id=$1)
		)`, id, p.OrganizationID); err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not stop pending OAuth flows")
		return
	}
	// Instagram/Facebook selections contain encrypted provider credentials, not
	// just a nonce. The company lock makes this delete exhaustive with respect
	// to selection creation/completion transactions that started before archive.
	if _, err = tx.Exec(r.Context(), `
		DELETE FROM oauth_account_selections selection
		USING creators creator
		WHERE selection.creator_id=creator.id
		  AND selection.organization_id=creator.organization_id
		  AND creator.company_id=$1 AND creator.organization_id=$2`, id, p.OrganizationID); err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not remove pending OAuth credentials")
		return
	}
	companyID := id
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &companyID, "ARCHIVE_COMPANY", "COMPANY", &companyID, http.StatusNoContent, map[string]any{"purgeAt": purgeAt})); err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not write audit log")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not archive company")
		return
	}
	markResponseAuditCommitted(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) restoreCompany(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	id := chi.URLParam(r, "id")
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "restore failed", "could not start restore")
		return
	}
	defer tx.Rollback(r.Context())
	now := s.now().UTC()
	var companyID string
	err = tx.QueryRow(r.Context(), `
		SELECT id::text FROM companies
		WHERE id=$1 AND organization_id=$2 AND lifecycle_state='ARCHIVED' AND purge_at>$3
		FOR UPDATE`, id, p.OrganizationID, now).Scan(&companyID)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, http.StatusConflict, "restore unavailable", "company is not archived or its retention period has elapsed")
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "restore failed", "could not lock archived company")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE companies SET archived_at=NULL,purge_at=NULL,purge_lease_owner=NULL,purge_lease_until=NULL,updated_at=$3 WHERE id=$1 AND organization_id=$2`, id, p.OrganizationID, now); err != nil {
		problem(w, http.StatusInternalServerError, "restore failed", "could not restore company")
		return
	}
	if _, err = tx.Exec(r.Context(), `
		UPDATE oauth_connections connection
		SET status='ACTIVE',suspended_for_company_archive=false,updated_at=$3
		FROM platform_accounts account
		WHERE connection.platform_account_id=account.id
		  AND connection.organization_id=account.organization_id
		  AND account.company_id=$1 AND account.organization_id=$2
		  AND connection.suspended_for_company_archive`, id, p.OrganizationID, now); err != nil {
		problem(w, http.StatusInternalServerError, "restore failed", "could not resume OAuth connections")
		return
	}
	if _, err = tx.Exec(r.Context(), `
		UPDATE sync_targets SET status='ACTIVE',suspended_for_company_archive=false,next_sync_at=$3
		WHERE company_id=$1 AND organization_id=$2 AND suspended_for_company_archive`, id, p.OrganizationID, now); err != nil {
		problem(w, http.StatusInternalServerError, "restore failed", "could not resume synchronization")
		return
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &companyID, "RESTORE_COMPANY", "COMPANY", &companyID, http.StatusNoContent, map[string]any{})); err != nil {
		problem(w, http.StatusInternalServerError, "restore failed", "could not write audit log")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "restore failed", "could not restore company")
		return
	}
	markResponseAuditCommitted(w)
	w.WriteHeader(http.StatusNoContent)
}
