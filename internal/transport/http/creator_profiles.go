package httpserver

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

var credentialFields = map[string]map[string]bool{
	"GMAIL":     {"login": false, "password": true, "phone": false},
	"YOUTUBE":   {"note": false, "login": false, "password": true, "phone": false, "email": false, "access_email": false},
	"INSTAGRAM": {"login": false, "password": true, "phone": false, "email": false},
	"TIKTOK":    {"login": false, "password": true, "phone": false, "email": false},
	"VK":        {"login": false, "password": true, "phone": false},
}

func normalizeTelegram(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "@")
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		host := strings.ToLower(parsed.Host)
		if host == "t.me" || host == "telegram.me" || host == "www.t.me" {
			value = strings.Trim(parsed.Path, "/")
		}
	}
	return strings.TrimPrefix(strings.TrimSpace(value), "@")
}

func (s *Server) updateCreator(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p := r.Context().Value(principalKey).(principal)
	var in struct {
		FirstName        string `json:"firstName"`
		LastName         string `json:"lastName"`
		MiddleName       string `json:"middleName"`
		DisplayName      string `json:"displayName"`
		InternalNote     string `json:"internalNote"`
		TelegramUsername string `json:"telegramUsername"`
		CompanyID        string `json:"companyId"`
		Status           string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.FirstName) == "" || strings.TrimSpace(in.LastName) == "" {
		problem(w, http.StatusBadRequest, "invalid creator", "firstName and lastName are required")
		return
	}
	if strings.TrimSpace(in.DisplayName) == "" {
		in.DisplayName = strings.TrimSpace(in.FirstName + " " + in.LastName)
	}
	status := strings.ToUpper(strings.TrimSpace(in.Status))
	if status == "" {
		status = "ACTIVE"
	}
	if status != "ACTIVE" && status != "ON_LEAVE" && status != "DISMISSED" {
		problem(w, http.StatusBadRequest, "invalid creator", "status must be ACTIVE, ON_LEAVE or DISMISSED")
		return
	}
	var companyID any
	if strings.TrimSpace(in.CompanyID) != "" {
		var exists bool
		if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM companies WHERE id=$1 AND organization_id=$2 AND archived_at IS NULL)`, in.CompanyID, p.OrganizationID).Scan(&exists); err != nil || !exists {
			problem(w, http.StatusBadRequest, "invalid company", "company does not exist")
			return
		}
		companyID = in.CompanyID
	}
	tag, err := s.pool.Exec(r.Context(), `UPDATE creators SET first_name=$1,last_name=$2,middle_name=$3,display_name=$4,internal_note=$5,telegram_username=$6,status=$7::creator_status,company_id=$8,archived_at=CASE WHEN $7::text='DISMISSED' THEN COALESCE(archived_at,now()) ELSE NULL END,updated_at=now() WHERE id=$9 AND organization_id=$10`, strings.TrimSpace(in.FirstName), strings.TrimSpace(in.LastName), strings.TrimSpace(in.MiddleName), strings.TrimSpace(in.DisplayName), strings.TrimSpace(in.InternalNote), normalizeTelegram(in.TelegramUsername), status, companyID, id, p.OrganizationID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "update failed", "could not update creator")
		return
	}
	if tag.RowsAffected() == 0 {
		problem(w, http.StatusNotFound, "not found", "creator does not exist")
		return
	}
	_, _ = s.pool.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,entity_id) VALUES($1,$2,'UPDATE','CREATOR',$3)`, p.OrganizationID, p.ID, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listCreatorCredentials(w http.ResponseWriter, r *http.Request) {
	if s.envelope == nil {
		problem(w, http.StatusServiceUnavailable, "encryption unavailable", "TOKEN_ENCRYPTION_KEY is not configured")
		return
	}
	id := chi.URLParam(r, "id")
	p := r.Context().Value(principalKey).(principal)
	rows, err := s.pool.Query(r.Context(), `SELECT x.id,x.section,x.field_key,x.is_secret,x.value_ciphertext,x.value_nonce,x.updated_at FROM creator_credentials x JOIN creators c ON c.id=x.creator_id WHERE x.creator_id=$1 AND c.organization_id=$2 ORDER BY x.section,x.field_key`, id, p.OrganizationID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "credentials failed", "could not load creator credentials")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var credentialID, section, fieldKey string
		var secret bool
		var ciphertext, nonce []byte
		var updatedAt any
		if err := rows.Scan(&credentialID, &section, &fieldKey, &secret, &ciphertext, &nonce, &updatedAt); err != nil {
			problem(w, http.StatusInternalServerError, "credentials failed", "could not read creator credentials")
			return
		}
		item := map[string]any{"id": credentialID, "section": section, "fieldKey": fieldKey, "isSecret": secret, "hasValue": true, "updatedAt": updatedAt}
		if !secret {
			plain, err := s.envelope.Decrypt(ciphertext, nonce)
			if err != nil {
				problem(w, http.StatusInternalServerError, "credentials failed", "could not decrypt creator credentials")
				return
			}
			item["value"] = string(plain)
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) saveCreatorCredentials(w http.ResponseWriter, r *http.Request) {
	if s.envelope == nil {
		problem(w, http.StatusServiceUnavailable, "encryption unavailable", "TOKEN_ENCRYPTION_KEY is not configured")
		return
	}
	id := chi.URLParam(r, "id")
	p := r.Context().Value(principalKey).(principal)
	var in struct {
		Items []struct {
			Section  string `json:"section"`
			FieldKey string `json:"fieldKey"`
			Value    string `json:"value"`
		} `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		problem(w, http.StatusBadRequest, "invalid credentials", "expected credentials array")
		return
	}
	var owned bool
	if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM creators WHERE id=$1 AND organization_id=$2)`, id, p.OrganizationID).Scan(&owned); err != nil || !owned {
		problem(w, http.StatusNotFound, "not found", "creator does not exist")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "save failed", "could not start credentials update")
		return
	}
	defer tx.Rollback(r.Context())
	for _, item := range in.Items {
		section := strings.ToUpper(strings.TrimSpace(item.Section))
		fields, ok := credentialFields[section]
		secret, valid := fields[item.FieldKey]
		if !ok || !valid {
			problem(w, http.StatusBadRequest, "invalid credentials", "unsupported credential field")
			return
		}
		value := strings.TrimSpace(item.Value)
		if value == "" {
			if _, err = tx.Exec(r.Context(), `DELETE FROM creator_credentials WHERE creator_id=$1 AND section=$2 AND field_key=$3`, id, section, item.FieldKey); err != nil {
				problem(w, http.StatusInternalServerError, "save failed", "could not clear credential")
				return
			}
			continue
		}
		ciphertext, nonce, encryptErr := s.envelope.Encrypt([]byte(value))
		if encryptErr != nil {
			problem(w, http.StatusInternalServerError, "save failed", "could not encrypt credential")
			return
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO creator_credentials(creator_id,section,field_key,is_secret,value_ciphertext,value_nonce,updated_by) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(creator_id,section,field_key) DO UPDATE SET is_secret=excluded.is_secret,value_ciphertext=excluded.value_ciphertext,value_nonce=excluded.value_nonce,updated_by=excluded.updated_by,updated_at=now()`, id, section, item.FieldKey, secret, ciphertext, nonce, p.ID); err != nil {
			problem(w, http.StatusInternalServerError, "save failed", "could not save credential")
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "save failed", "could not commit credentials update")
		return
	}
	_, _ = s.pool.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,entity_id,metadata) VALUES($1,$2,'UPDATE_CREDENTIALS','CREATOR',$3,jsonb_build_object('fields',$4))`, p.OrganizationID, p.ID, id, len(in.Items))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) revealCreatorCredential(w http.ResponseWriter, r *http.Request) {
	if s.envelope == nil {
		problem(w, http.StatusServiceUnavailable, "encryption unavailable", "TOKEN_ENCRYPTION_KEY is not configured")
		return
	}
	creatorID := chi.URLParam(r, "id")
	credentialID := chi.URLParam(r, "credentialID")
	p := r.Context().Value(principalKey).(principal)
	var ciphertext, nonce []byte
	err := s.pool.QueryRow(r.Context(), `SELECT x.value_ciphertext,x.value_nonce FROM creator_credentials x JOIN creators c ON c.id=x.creator_id WHERE x.id=$1 AND x.creator_id=$2 AND x.is_secret=true AND c.organization_id=$3`, credentialID, creatorID, p.OrganizationID).Scan(&ciphertext, &nonce)
	if err == pgx.ErrNoRows {
		problem(w, http.StatusNotFound, "not found", "credential does not exist")
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "reveal failed", "could not load credential")
		return
	}
	plain, err := s.envelope.Decrypt(ciphertext, nonce)
	if err != nil {
		problem(w, http.StatusInternalServerError, "reveal failed", "could not decrypt credential")
		return
	}
	_, _ = s.pool.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,entity_id,metadata) VALUES($1,$2,'REVEAL_CREDENTIAL','CREATOR',$3,jsonb_build_object('credentialId',$4))`, p.OrganizationID, p.ID, creatorID, credentialID)
	writeJSON(w, http.StatusOK, map[string]string{"value": string(plain)})
}
