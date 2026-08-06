package httpserver

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (s *Server) listCompanies(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	rows, err := s.pool.Query(r.Context(), `
		SELECT x.id,x.name,count(c.id),v.id IS NOT NULL
		FROM companies x
		LEFT JOIN creators c ON c.company_id=x.id AND c.status<>'DISMISSED' AND c.archived_at IS NULL
		LEFT JOIN company_vk_accounts v ON v.company_id=x.id
		WHERE x.organization_id=$1 AND x.archived_at IS NULL
		GROUP BY x.id,v.id
		ORDER BY x.name`, p.OrganizationID)
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
	var id string
	err := s.pool.QueryRow(r.Context(), `INSERT INTO companies(organization_id,name) VALUES($1,$2) RETURNING id`, p.OrganizationID, name).Scan(&id)
	if err != nil {
		if pgErr, ok := err.(*pgconn.PgError); ok && pgErr.Code == "23505" {
			problem(w, http.StatusConflict, "company exists", "company with this name already exists")
			return
		}
		problem(w, http.StatusInternalServerError, "creation failed", "could not create company")
		return
	}
	_, _ = s.pool.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,entity_id) VALUES($1,$2,'CREATE','COMPANY',$3)`, p.OrganizationID, p.ID, id)
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
	tag, err := tx.Exec(r.Context(), `UPDATE companies SET archived_at=now(),updated_at=now() WHERE id=$1 AND organization_id=$2 AND archived_at IS NULL`, id, p.OrganizationID)
	if err != nil || tag.RowsAffected() == 0 {
		problem(w, http.StatusNotFound, "not found", "company does not exist")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE creators SET company_id=NULL,updated_at=now() WHERE company_id=$1 AND organization_id=$2`, id, p.OrganizationID); err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not detach creators")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,entity_id) VALUES($1,$2,'ARCHIVE','COMPANY',$3)`, p.OrganizationID, p.ID, id); err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not write audit log")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not archive company")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteCompany(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	id := chi.URLParam(r, "id")
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "deletion failed", "could not start deletion")
		return
	}
	defer tx.Rollback(r.Context())

	// Keep imported platform data and statistics intact, but remove sync jobs
	// owned by the company's shared VK account before the account is cascaded.
	if _, err = tx.Exec(r.Context(), `
		DELETE FROM sync_runs
		WHERE target_id IN (
			SELECT st.id FROM sync_targets st
			JOIN company_vk_accounts a ON a.platform_account_id=st.target_id
			WHERE a.company_id=$1 AND a.organization_id=$2
		)`, id, p.OrganizationID); err != nil {
		problem(w, http.StatusInternalServerError, "deletion failed", "could not remove sync history")
		return
	}
	if _, err = tx.Exec(r.Context(), `
		DELETE FROM sync_targets
		WHERE target_id IN (
			SELECT a.platform_account_id FROM company_vk_accounts a
			WHERE a.company_id=$1 AND a.organization_id=$2 AND a.platform_account_id IS NOT NULL
		)`, id, p.OrganizationID); err != nil {
		problem(w, http.StatusInternalServerError, "deletion failed", "could not remove sync jobs")
		return
	}
	tag, err := tx.Exec(r.Context(), `DELETE FROM companies WHERE id=$1 AND organization_id=$2`, id, p.OrganizationID)
	if err != nil || tag.RowsAffected() == 0 {
		problem(w, http.StatusNotFound, "not found", "company does not exist")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,entity_id) VALUES($1,$2,'DELETE','COMPANY',$3)`, p.OrganizationID, p.ID, id); err != nil {
		problem(w, http.StatusInternalServerError, "deletion failed", "could not write audit log")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "deletion failed", "could not delete company")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
