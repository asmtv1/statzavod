package httpserver

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type creatorHistoryChange struct {
	Section       string
	FieldKey      string
	IsSecret      bool
	OldPresent    bool
	NewPresent    bool
	OldValue      string
	NewValue      string
	OldCiphertext []byte
	OldNonce      []byte
	NewCiphertext []byte
	NewNonce      []byte
}

type creatorHistoryItem struct {
	ID         string `json:"id"`
	Section    string `json:"section"`
	FieldKey   string `json:"fieldKey"`
	IsSecret   bool   `json:"isSecret"`
	OldPresent bool   `json:"oldPresent"`
	NewPresent bool   `json:"newPresent"`
	OldValue   string `json:"oldValue,omitempty"`
	NewValue   string `json:"newValue,omitempty"`
}

type creatorHistoryEvent struct {
	ID        string               `json:"id"`
	ChangedAt time.Time            `json:"changedAt"`
	ChangedBy string               `json:"changedBy"`
	Changes   []creatorHistoryItem `json:"changes"`
}

func plainHistoryChange(fieldKey, oldValue, newValue string) creatorHistoryChange {
	return creatorHistoryChange{
		FieldKey:   fieldKey,
		OldPresent: oldValue != "",
		NewPresent: newValue != "",
		OldValue:   oldValue,
		NewValue:   newValue,
	}
}

func insertCreatorHistory(r *http.Request, tx pgx.Tx, p principal, creatorID, block string, changes []creatorHistoryChange) error {
	if len(changes) == 0 {
		return nil
	}
	var eventID string
	if err := tx.QueryRow(r.Context(), `INSERT INTO creator_history_events(organization_id,creator_id,actor_id,block) VALUES($1,$2,$3,$4) RETURNING id`, p.OrganizationID, creatorID, p.ID, block).Scan(&eventID); err != nil {
		return err
	}
	for _, change := range changes {
		var oldValue, newValue any = change.OldValue, change.NewValue
		if change.IsSecret {
			oldValue, newValue = nil, nil
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO creator_history_changes(event_id,section,field_key,is_secret,old_present,new_present,old_value,new_value,old_value_ciphertext,old_value_nonce,new_value_ciphertext,new_value_nonce) VALUES($1,NULLIF($2,''),$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, eventID, change.Section, change.FieldKey, change.IsSecret, change.OldPresent, change.NewPresent, oldValue, newValue, change.OldCiphertext, change.OldNonce, change.NewCiphertext, change.NewNonce); err != nil {
			return err
		}
	}
	return nil
}

var credentialFields = map[string]map[string]bool{
	"GMAIL":     {"login": false, "password": true, "phone": false},
	"YOUTUBE":   {"note": false, "login": false, "password": true, "phone": false, "email": false, "access_email": false, "channel_url": false},
	"INSTAGRAM": {"login": false, "password": true, "phone": false, "email": false, "channel_url": false},
	"TIKTOK":    {"login": false, "password": true, "phone": false, "email": false, "channel_url": false},
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
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "update failed", "could not start creator update")
		return
	}
	defer tx.Rollback(r.Context())

	var old struct {
		FirstName, LastName, MiddleName, DisplayName, InternalNote, TelegramUsername string
		Status, CompanyID, CompanyName                                               string
	}
	err = tx.QueryRow(r.Context(), `SELECT c.first_name,c.last_name,COALESCE(c.middle_name,''),c.display_name,c.internal_note,c.telegram_username,c.status::text,COALESCE(c.company_id::text,''),COALESCE(x.name,'') FROM creators c LEFT JOIN companies x ON x.id=c.company_id WHERE c.id=$1 AND c.organization_id=$2 FOR UPDATE OF c`, id, p.OrganizationID).Scan(&old.FirstName, &old.LastName, &old.MiddleName, &old.DisplayName, &old.InternalNote, &old.TelegramUsername, &old.Status, &old.CompanyID, &old.CompanyName)
	if err == pgx.ErrNoRows {
		problem(w, http.StatusNotFound, "not found", "creator does not exist")
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "update failed", "could not load creator")
		return
	}

	newCompanyID := strings.TrimSpace(in.CompanyID)
	newCompanyName := ""
	var companyID any
	if newCompanyID != "" {
		if err := tx.QueryRow(r.Context(), `SELECT name FROM companies WHERE id=$1 AND organization_id=$2 AND archived_at IS NULL`, newCompanyID, p.OrganizationID).Scan(&newCompanyName); err == pgx.ErrNoRows {
			problem(w, http.StatusBadRequest, "invalid company", "company does not exist")
			return
		} else if err != nil {
			problem(w, http.StatusInternalServerError, "update failed", "could not validate company")
			return
		}
		companyID = newCompanyID
	}

	firstName := strings.TrimSpace(in.FirstName)
	lastName := strings.TrimSpace(in.LastName)
	middleName := strings.TrimSpace(in.MiddleName)
	displayName := strings.TrimSpace(in.DisplayName)
	internalNote := strings.TrimSpace(in.InternalNote)
	telegramUsername := normalizeTelegram(in.TelegramUsername)
	changes := make([]creatorHistoryChange, 0, 8)
	for _, values := range []struct{ key, before, after string }{
		{"firstName", old.FirstName, firstName},
		{"lastName", old.LastName, lastName},
		{"middleName", old.MiddleName, middleName},
		{"displayName", old.DisplayName, displayName},
		{"status", old.Status, status},
		{"company", old.CompanyName, newCompanyName},
		{"telegramUsername", old.TelegramUsername, telegramUsername},
		{"internalNote", old.InternalNote, internalNote},
	} {
		if values.before != values.after {
			changes = append(changes, plainHistoryChange(values.key, values.before, values.after))
		}
	}
	if len(changes) == 0 {
		if err := tx.Commit(r.Context()); err != nil {
			problem(w, http.StatusInternalServerError, "update failed", "could not finish creator update")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if _, err = tx.Exec(r.Context(), `UPDATE creators SET first_name=$1,last_name=$2,middle_name=$3,display_name=$4,internal_note=$5,telegram_username=$6,status=$7::creator_status,company_id=$8,updated_at=now() WHERE id=$9 AND organization_id=$10`, firstName, lastName, middleName, displayName, internalNote, telegramUsername, status, companyID, id, p.OrganizationID); err != nil {
		problem(w, http.StatusInternalServerError, "update failed", "could not update creator")
		return
	}
	if err = insertCreatorHistory(r, tx, p, id, "PROFILE", changes); err != nil {
		problem(w, http.StatusInternalServerError, "update failed", "could not save creator history")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,entity_id) VALUES($1,$2,'UPDATE','CREATOR',$3)`, p.OrganizationID, p.ID, id); err != nil {
		problem(w, http.StatusInternalServerError, "update failed", "could not save audit record")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "update failed", "could not commit creator update")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) updateCreatorWorkStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p := r.Context().Value(principalKey).(principal)
	var in struct {
		Status  string `json:"status"`
		Comment string `json:"comment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		problem(w, http.StatusBadRequest, "invalid work status", "expected JSON body")
		return
	}
	status := strings.ToUpper(strings.TrimSpace(in.Status))
	comment := strings.TrimSpace(in.Comment)
	if status != "OK" && status != "NEEDS_ATTENTION" && status != "IN_PROGRESS" {
		problem(w, http.StatusBadRequest, "invalid work status", "status must be OK, NEEDS_ATTENTION or IN_PROGRESS")
		return
	}
	if status != "OK" && comment == "" {
		problem(w, http.StatusBadRequest, "invalid work status", "comment is required when work is needed or in progress")
		return
	}
	if status == "OK" {
		comment = ""
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "update failed", "could not start creator work update")
		return
	}
	defer tx.Rollback(r.Context())
	var oldStatus, oldComment string
	err = tx.QueryRow(r.Context(), `SELECT work_status,work_comment FROM creators WHERE id=$1 AND organization_id=$2 FOR UPDATE`, id, p.OrganizationID).Scan(&oldStatus, &oldComment)
	if err == pgx.ErrNoRows {
		problem(w, http.StatusNotFound, "not found", "creator does not exist")
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "update failed", "could not load creator work status")
		return
	}
	changes := make([]creatorHistoryChange, 0, 2)
	if oldStatus != status {
		changes = append(changes, plainHistoryChange("status", oldStatus, status))
	}
	if oldComment != comment {
		changes = append(changes, plainHistoryChange("comment", oldComment, comment))
	}
	if len(changes) > 0 {
		if _, err = tx.Exec(r.Context(), `UPDATE creators SET work_status=$1,work_comment=$2,updated_at=now() WHERE id=$3 AND organization_id=$4`, status, comment, id, p.OrganizationID); err != nil {
			problem(w, http.StatusInternalServerError, "update failed", "could not update creator work status")
			return
		}
		if err = insertCreatorHistory(r, tx, p, id, "WORK", changes); err != nil {
			problem(w, http.StatusInternalServerError, "update failed", "could not save creator work history")
			return
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,entity_id,metadata) VALUES($1,$2,'UPDATE_WORK_STATUS','CREATOR',$3,jsonb_build_object('status',$4::text))`, p.OrganizationID, p.ID, id, status); err != nil {
			problem(w, http.StatusInternalServerError, "update failed", "could not save work status history")
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "update failed", "could not commit creator work update")
		return
	}
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
	changes := make([]creatorHistoryChange, 0, len(in.Items))
	seen := make(map[string]struct{}, len(in.Items))
	for _, item := range in.Items {
		section := strings.ToUpper(strings.TrimSpace(item.Section))
		fields, ok := credentialFields[section]
		secret, valid := fields[item.FieldKey]
		if !ok || !valid {
			problem(w, http.StatusBadRequest, "invalid credentials", "unsupported credential field")
			return
		}
		key := section + ":" + item.FieldKey
		if _, duplicate := seen[key]; duplicate {
			problem(w, http.StatusBadRequest, "invalid credentials", "credential field is duplicated")
			return
		}
		seen[key] = struct{}{}

		var oldSecret bool
		var oldCiphertext, oldNonce []byte
		loadErr := tx.QueryRow(r.Context(), `SELECT is_secret,value_ciphertext,value_nonce FROM creator_credentials WHERE creator_id=$1 AND section=$2 AND field_key=$3 FOR UPDATE`, id, section, item.FieldKey).Scan(&oldSecret, &oldCiphertext, &oldNonce)
		oldPresent := loadErr == nil
		if loadErr != nil && loadErr != pgx.ErrNoRows {
			problem(w, http.StatusInternalServerError, "save failed", "could not load current credential")
			return
		}
		oldValue := ""
		if oldPresent {
			plain, decryptErr := s.envelope.Decrypt(oldCiphertext, oldNonce)
			if decryptErr != nil {
				problem(w, http.StatusInternalServerError, "save failed", "could not decrypt current credential")
				return
			}
			oldValue = string(plain)
		}
		value := strings.TrimSpace(item.Value)
		if oldPresent && oldValue == value && oldSecret == secret {
			continue
		}
		if value == "" {
			if !oldPresent {
				continue
			}
			change := creatorHistoryChange{Section: section, FieldKey: item.FieldKey, IsSecret: secret, OldPresent: true}
			if secret {
				change.OldCiphertext, change.OldNonce = oldCiphertext, oldNonce
			} else {
				change.OldValue = oldValue
			}
			changes = append(changes, change)
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
		change := creatorHistoryChange{Section: section, FieldKey: item.FieldKey, IsSecret: secret, OldPresent: oldPresent, NewPresent: true}
		if secret {
			change.OldCiphertext, change.OldNonce = oldCiphertext, oldNonce
			change.NewCiphertext, change.NewNonce = ciphertext, nonce
		} else {
			change.OldValue, change.NewValue = oldValue, value
		}
		changes = append(changes, change)
		if _, err = tx.Exec(r.Context(), `INSERT INTO creator_credentials(creator_id,section,field_key,is_secret,value_ciphertext,value_nonce,updated_by) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(creator_id,section,field_key) DO UPDATE SET is_secret=excluded.is_secret,value_ciphertext=excluded.value_ciphertext,value_nonce=excluded.value_nonce,updated_by=excluded.updated_by,updated_at=now()`, id, section, item.FieldKey, secret, ciphertext, nonce, p.ID); err != nil {
			problem(w, http.StatusInternalServerError, "save failed", "could not save credential")
			return
		}
	}
	if err = insertCreatorHistory(r, tx, p, id, "CREDENTIALS", changes); err != nil {
		problem(w, http.StatusInternalServerError, "save failed", "could not save credentials history")
		return
	}
	if len(changes) > 0 {
		if _, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,entity_id,metadata) VALUES($1,$2,'UPDATE_CREDENTIALS','CREATOR',$3,jsonb_build_object('fields',$4::integer))`, p.OrganizationID, p.ID, id, len(changes)); err != nil {
			problem(w, http.StatusInternalServerError, "save failed", "could not save credentials history")
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "save failed", "could not commit credentials update")
		return
	}
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
	_, _ = s.pool.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,entity_id,metadata) VALUES($1,$2,'REVEAL_CREDENTIAL','CREATOR',$3,jsonb_build_object('credentialId',$4::text))`, p.OrganizationID, p.ID, creatorID, credentialID)
	writeJSON(w, http.StatusOK, map[string]string{"value": string(plain)})
}

func (s *Server) listCreatorHistory(w http.ResponseWriter, r *http.Request) {
	creatorID := chi.URLParam(r, "id")
	block := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("block")))
	if block != "PROFILE" && block != "WORK" && block != "CREDENTIALS" {
		problem(w, http.StatusBadRequest, "invalid history block", "block must be PROFILE, WORK or CREDENTIALS")
		return
	}
	p := r.Context().Value(principalKey).(principal)
	var owned bool
	if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM creators WHERE id=$1 AND organization_id=$2)`, creatorID, p.OrganizationID).Scan(&owned); err != nil {
		problem(w, http.StatusInternalServerError, "history failed", "could not validate creator")
		return
	}
	if !owned {
		problem(w, http.StatusNotFound, "not found", "creator does not exist")
		return
	}

	rows, err := s.pool.Query(r.Context(), `WITH recent_events AS (SELECT id,actor_id,created_at FROM creator_history_events WHERE creator_id=$1 AND organization_id=$2 AND block=$3 ORDER BY created_at DESC LIMIT 100) SELECT e.id,e.created_at,COALESCE(u.email,''),c.id,COALESCE(c.section,''),c.field_key,c.is_secret,c.old_present,c.new_present,COALESCE(c.old_value,''),COALESCE(c.new_value,'') FROM recent_events e JOIN creator_history_changes c ON c.event_id=e.id LEFT JOIN users u ON u.id=e.actor_id ORDER BY e.created_at DESC,c.id`, creatorID, p.OrganizationID, block)
	if err != nil {
		problem(w, http.StatusInternalServerError, "history failed", "could not load creator history")
		return
	}
	defer rows.Close()
	events := make([]creatorHistoryEvent, 0)
	eventIndexes := make(map[string]int)
	for rows.Next() {
		var eventID, changedBy, changeID, section, fieldKey, oldValue, newValue string
		var changedAt time.Time
		var secret, oldPresent, newPresent bool
		if err := rows.Scan(&eventID, &changedAt, &changedBy, &changeID, &section, &fieldKey, &secret, &oldPresent, &newPresent, &oldValue, &newValue); err != nil {
			problem(w, http.StatusInternalServerError, "history failed", "could not read creator history")
			return
		}
		index, exists := eventIndexes[eventID]
		if !exists {
			index = len(events)
			eventIndexes[eventID] = index
			if changedBy == "" {
				changedBy = "Система"
			}
			events = append(events, creatorHistoryEvent{ID: eventID, ChangedAt: changedAt, ChangedBy: changedBy, Changes: make([]creatorHistoryItem, 0, 2)})
		}
		item := creatorHistoryItem{ID: changeID, Section: section, FieldKey: fieldKey, IsSecret: secret, OldPresent: oldPresent, NewPresent: newPresent}
		if !secret {
			item.OldValue, item.NewValue = oldValue, newValue
		}
		events[index].Changes = append(events[index].Changes, item)
	}
	if err := rows.Err(); err != nil {
		problem(w, http.StatusInternalServerError, "history failed", "could not finish reading creator history")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": events})
}

func (s *Server) revealCreatorHistoryCredential(w http.ResponseWriter, r *http.Request) {
	if s.envelope == nil {
		problem(w, http.StatusServiceUnavailable, "encryption unavailable", "TOKEN_ENCRYPTION_KEY is not configured")
		return
	}
	creatorID := chi.URLParam(r, "id")
	changeID := chi.URLParam(r, "changeID")
	var in struct {
		Side string `json:"side"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		problem(w, http.StatusBadRequest, "invalid history reveal", "expected JSON body")
		return
	}
	in.Side = strings.ToLower(strings.TrimSpace(in.Side))
	if in.Side != "old" && in.Side != "new" {
		problem(w, http.StatusBadRequest, "invalid history reveal", "side must be old or new")
		return
	}
	p := r.Context().Value(principalKey).(principal)
	var oldCiphertext, oldNonce, newCiphertext, newNonce []byte
	var oldPresent, newPresent bool
	err := s.pool.QueryRow(r.Context(), `SELECT c.old_value_ciphertext,c.old_value_nonce,c.new_value_ciphertext,c.new_value_nonce,c.old_present,c.new_present FROM creator_history_changes c JOIN creator_history_events e ON e.id=c.event_id WHERE c.id=$1 AND e.creator_id=$2 AND e.organization_id=$3 AND e.block='CREDENTIALS' AND c.is_secret=true`, changeID, creatorID, p.OrganizationID).Scan(&oldCiphertext, &oldNonce, &newCiphertext, &newNonce, &oldPresent, &newPresent)
	if err == pgx.ErrNoRows {
		problem(w, http.StatusNotFound, "not found", "credential history value does not exist")
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "reveal failed", "could not load credential history")
		return
	}
	ciphertext, nonce, present := oldCiphertext, oldNonce, oldPresent
	if in.Side == "new" {
		ciphertext, nonce, present = newCiphertext, newNonce, newPresent
	}
	if !present || len(ciphertext) == 0 || len(nonce) == 0 {
		problem(w, http.StatusNotFound, "not found", "credential history value is empty")
		return
	}
	plain, err := s.envelope.Decrypt(ciphertext, nonce)
	if err != nil {
		problem(w, http.StatusInternalServerError, "reveal failed", "could not decrypt credential history")
		return
	}
	_, _ = s.pool.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,entity_id,metadata) VALUES($1,$2,'REVEAL_CREDENTIAL_HISTORY','CREATOR',$3,jsonb_build_object('changeId',$4::text,'side',$5::text))`, p.OrganizationID, p.ID, creatorID, changeID, in.Side)
	writeJSON(w, http.StatusOK, map[string]string{"value": string(plain)})
}
