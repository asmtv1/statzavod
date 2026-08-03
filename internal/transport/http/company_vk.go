package httpserver

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func (s *Server) listCompanyVKAccounts(w http.ResponseWriter, r *http.Request) {
	if s.envelope == nil {
		problem(w, http.StatusServiceUnavailable, "encryption unavailable", "TOKEN_ENCRYPTION_KEY is not configured")
		return
	}
	p := r.Context().Value(principalKey).(principal)
	rows, err := s.pool.Query(r.Context(), `SELECT a.id,a.company_id,c.name,a.login_ciphertext,a.login_nonce,a.phone_ciphertext,a.phone_nonce,a.updated_at FROM company_vk_accounts a JOIN companies c ON c.id=a.company_id WHERE a.organization_id=$1 AND c.archived_at IS NULL ORDER BY c.name`, p.OrganizationID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "VK accounts failed", "could not load company VK accounts")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, companyID, companyName string
		var loginCiphertext, loginNonce, phoneCiphertext, phoneNonce []byte
		var updatedAt any
		if err := rows.Scan(&id, &companyID, &companyName, &loginCiphertext, &loginNonce, &phoneCiphertext, &phoneNonce, &updatedAt); err != nil {
			problem(w, http.StatusInternalServerError, "VK accounts failed", "could not read company VK account")
			return
		}
		login, err := s.envelope.Decrypt(loginCiphertext, loginNonce)
		if err != nil {
			problem(w, http.StatusInternalServerError, "VK accounts failed", "could not decrypt company VK login")
			return
		}
		phone := ""
		if len(phoneCiphertext) > 0 {
			plain, decryptErr := s.envelope.Decrypt(phoneCiphertext, phoneNonce)
			if decryptErr != nil {
				problem(w, http.StatusInternalServerError, "VK accounts failed", "could not decrypt company VK phone")
				return
			}
			phone = string(plain)
		}
		items = append(items, map[string]any{"id": id, "companyId": companyID, "companyName": companyName, "login": string(login), "phone": phone, "hasPassword": true, "updatedAt": updatedAt})
	}
	if err := rows.Err(); err != nil {
		problem(w, http.StatusInternalServerError, "VK accounts failed", "could not finish reading company VK accounts")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) saveCompanyVKAccount(w http.ResponseWriter, r *http.Request) {
	if s.envelope == nil {
		problem(w, http.StatusServiceUnavailable, "encryption unavailable", "TOKEN_ENCRYPTION_KEY is not configured")
		return
	}
	companyID := chi.URLParam(r, "id")
	p := r.Context().Value(principalKey).(principal)
	var in struct {
		Login    string `json:"login"`
		Password string `json:"password"`
		Phone    string `json:"phone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Login) == "" {
		problem(w, http.StatusBadRequest, "invalid VK account", "login is required")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "VK account save failed", "could not start update")
		return
	}
	defer tx.Rollback(r.Context())
	var companyName string
	if err = tx.QueryRow(r.Context(), `SELECT name FROM companies WHERE id=$1 AND organization_id=$2 AND archived_at IS NULL FOR UPDATE`, companyID, p.OrganizationID).Scan(&companyName); err == pgx.ErrNoRows {
		problem(w, http.StatusNotFound, "not found", "company does not exist")
		return
	} else if err != nil {
		problem(w, http.StatusInternalServerError, "VK account save failed", "could not load company")
		return
	}
	var existingAccountID string
	var existingLoginCiphertext, existingLoginNonce, existingPasswordCiphertext, existingPasswordNonce, existingPhoneCiphertext, existingPhoneNonce []byte
	loadErr := tx.QueryRow(r.Context(), `SELECT id,login_ciphertext,login_nonce,password_ciphertext,password_nonce,phone_ciphertext,phone_nonce FROM company_vk_accounts WHERE company_id=$1 FOR UPDATE`, companyID).Scan(&existingAccountID, &existingLoginCiphertext, &existingLoginNonce, &existingPasswordCiphertext, &existingPasswordNonce, &existingPhoneCiphertext, &existingPhoneNonce)
	if loadErr != nil && loadErr != pgx.ErrNoRows {
		problem(w, http.StatusInternalServerError, "VK account save failed", "could not load current VK account")
		return
	}
	password := strings.TrimSpace(in.Password)
	if loadErr == pgx.ErrNoRows && password == "" {
		problem(w, http.StatusBadRequest, "invalid VK account", "password is required for a new VK account")
		return
	}
	login := strings.TrimSpace(in.Login)
	phone := strings.TrimSpace(in.Phone)
	oldLogin, oldPassword, oldPhone := "", "", ""
	if loadErr == nil {
		if plain, decryptErr := s.envelope.Decrypt(existingLoginCiphertext, existingLoginNonce); decryptErr == nil {
			oldLogin = string(plain)
		} else {
			problem(w, http.StatusInternalServerError, "VK account save failed", "could not decrypt current login")
			return
		}
		if plain, decryptErr := s.envelope.Decrypt(existingPasswordCiphertext, existingPasswordNonce); decryptErr == nil {
			oldPassword = string(plain)
		} else {
			problem(w, http.StatusInternalServerError, "VK account save failed", "could not decrypt current password")
			return
		}
		if len(existingPhoneCiphertext) > 0 {
			if plain, decryptErr := s.envelope.Decrypt(existingPhoneCiphertext, existingPhoneNonce); decryptErr == nil {
				oldPhone = string(plain)
			} else {
				problem(w, http.StatusInternalServerError, "VK account save failed", "could not decrypt current phone")
				return
			}
		}
	}
	newPassword := password
	if newPassword == "" {
		newPassword = oldPassword
	}
	loginCiphertext, loginNonce, err := s.envelope.Encrypt([]byte(login))
	if err != nil {
		problem(w, http.StatusInternalServerError, "VK account save failed", "could not encrypt login")
		return
	}
	passwordCiphertext, passwordNonce := existingPasswordCiphertext, existingPasswordNonce
	if password != "" {
		passwordCiphertext, passwordNonce, err = s.envelope.Encrypt([]byte(password))
		if err != nil {
			problem(w, http.StatusInternalServerError, "VK account save failed", "could not encrypt password")
			return
		}
	}
	var phoneCiphertext, phoneNonce []byte
	if phone != "" {
		phoneCiphertext, phoneNonce, err = s.envelope.Encrypt([]byte(phone))
		if err != nil {
			problem(w, http.StatusInternalServerError, "VK account save failed", "could not encrypt phone")
			return
		}
	}
	var accountID string
	err = tx.QueryRow(r.Context(), `INSERT INTO company_vk_accounts(organization_id,company_id,login_ciphertext,login_nonce,password_ciphertext,password_nonce,phone_ciphertext,phone_nonce,created_by,updated_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$9) ON CONFLICT(company_id) DO UPDATE SET login_ciphertext=excluded.login_ciphertext,login_nonce=excluded.login_nonce,password_ciphertext=excluded.password_ciphertext,password_nonce=excluded.password_nonce,phone_ciphertext=excluded.phone_ciphertext,phone_nonce=excluded.phone_nonce,updated_by=excluded.updated_by,updated_at=now() RETURNING id`, p.OrganizationID, companyID, loginCiphertext, loginNonce, passwordCiphertext, passwordNonce, phoneCiphertext, phoneNonce, p.ID).Scan(&accountID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "VK account save failed", "could not save company VK account")
		return
	}
	changes := make([]creatorHistoryChange, 0, 3)
	if loadErr == nil && oldLogin != login {
		changes = append(changes, creatorHistoryChange{Section: "VK_COMPANY", FieldKey: "login", OldPresent: oldLogin != "", NewPresent: true, OldValue: oldLogin, NewValue: login})
	}
	if loadErr == nil && oldPassword != newPassword {
		changes = append(changes, creatorHistoryChange{Section: "VK_COMPANY", FieldKey: "password", IsSecret: true, OldPresent: true, NewPresent: true, OldCiphertext: existingPasswordCiphertext, OldNonce: existingPasswordNonce, NewCiphertext: passwordCiphertext, NewNonce: passwordNonce})
	}
	if loadErr == nil && oldPhone != phone {
		changes = append(changes, creatorHistoryChange{Section: "VK_COMPANY", FieldKey: "phone", OldPresent: oldPhone != "", NewPresent: phone != "", OldValue: oldPhone, NewValue: phone})
	}
	if len(changes) > 0 {
		rows, queryErr := tx.Query(r.Context(), `SELECT creator_id FROM creator_vk_assignments WHERE company_vk_account_id=$1`, accountID)
		if queryErr != nil {
			problem(w, http.StatusInternalServerError, "VK account save failed", "could not load linked creators")
			return
		}
		creatorIDs := make([]string, 0)
		for rows.Next() {
			var creatorID string
			if scanErr := rows.Scan(&creatorID); scanErr != nil {
				rows.Close()
				problem(w, http.StatusInternalServerError, "VK account save failed", "could not read linked creator")
				return
			}
			creatorIDs = append(creatorIDs, creatorID)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			rows.Close()
			problem(w, http.StatusInternalServerError, "VK account save failed", "could not finish reading linked creators")
			return
		}
		rows.Close()
		for _, creatorID := range creatorIDs {
			if err = insertCreatorHistory(r, tx, p, creatorID, "CREDENTIALS", changes); err != nil {
				problem(w, http.StatusInternalServerError, "VK account save failed", "could not save linked creator history")
				return
			}
		}
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,entity_id,metadata) VALUES($1,$2,'UPDATE_VK_ACCOUNT','COMPANY',$3,jsonb_build_object('companyName',$4::text,'accountId',$5::text))`, p.OrganizationID, p.ID, companyID, companyName, accountID); err != nil {
		problem(w, http.StatusInternalServerError, "VK account save failed", "could not save audit record")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "VK account save failed", "could not commit VK account update")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": accountID})
}

func (s *Server) revealCompanyVKPassword(w http.ResponseWriter, r *http.Request) {
	if s.envelope == nil {
		problem(w, http.StatusServiceUnavailable, "encryption unavailable", "TOKEN_ENCRYPTION_KEY is not configured")
		return
	}
	accountID := chi.URLParam(r, "id")
	p := r.Context().Value(principalKey).(principal)
	var companyID string
	var ciphertext, nonce []byte
	err := s.pool.QueryRow(r.Context(), `SELECT company_id,password_ciphertext,password_nonce FROM company_vk_accounts WHERE id=$1 AND organization_id=$2`, accountID, p.OrganizationID).Scan(&companyID, &ciphertext, &nonce)
	if err == pgx.ErrNoRows {
		problem(w, http.StatusNotFound, "not found", "company VK account does not exist")
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "reveal failed", "could not load company VK password")
		return
	}
	plain, err := s.envelope.Decrypt(ciphertext, nonce)
	if err != nil {
		problem(w, http.StatusInternalServerError, "reveal failed", "could not decrypt company VK password")
		return
	}
	_, _ = s.pool.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,entity_id,metadata) VALUES($1,$2,'REVEAL_COMPANY_VK_PASSWORD','COMPANY',$3,jsonb_build_object('accountId',$4::text))`, p.OrganizationID, p.ID, companyID, accountID)
	writeJSON(w, http.StatusOK, map[string]string{"value": string(plain)})
}

func normalizeVKCommunityURL(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" {
		return "", false
	}
	host := strings.ToLower(strings.TrimPrefix(parsed.Hostname(), "www."))
	path := strings.Trim(parsed.EscapedPath(), "/")
	if (host != "vk.ru" && host != "vk.com") || path == "" || strings.Contains(path, "/") {
		return "", false
	}
	return "https://" + host + "/" + path, true
}

func (s *Server) getCreatorVKAccess(w http.ResponseWriter, r *http.Request) {
	if s.envelope == nil {
		problem(w, http.StatusServiceUnavailable, "encryption unavailable", "TOKEN_ENCRYPTION_KEY is not configured")
		return
	}
	creatorID := chi.URLParam(r, "id")
	p := r.Context().Value(principalKey).(principal)
	var owned bool
	if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM creators WHERE id=$1 AND organization_id=$2)`, creatorID, p.OrganizationID).Scan(&owned); err != nil || !owned {
		problem(w, http.StatusNotFound, "not found", "creator does not exist")
		return
	}
	var accountID, companyID, companyName, communityURL string
	var loginCiphertext, loginNonce, phoneCiphertext, phoneNonce []byte
	err := s.pool.QueryRow(r.Context(), `SELECT a.id,a.company_id,c.name,v.community_url,a.login_ciphertext,a.login_nonce,a.phone_ciphertext,a.phone_nonce FROM creator_vk_assignments v JOIN company_vk_accounts a ON a.id=v.company_vk_account_id JOIN companies c ON c.id=a.company_id WHERE v.creator_id=$1 AND a.organization_id=$2`, creatorID, p.OrganizationID).Scan(&accountID, &companyID, &companyName, &communityURL, &loginCiphertext, &loginNonce, &phoneCiphertext, &phoneNonce)
	if err == pgx.ErrNoRows {
		writeJSON(w, http.StatusOK, map[string]any{"accountId": "", "companyId": "", "companyName": "", "login": "", "phone": "", "hasPassword": false, "communityUrl": ""})
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "VK access failed", "could not load creator VK access")
		return
	}
	login, err := s.envelope.Decrypt(loginCiphertext, loginNonce)
	if err != nil {
		problem(w, http.StatusInternalServerError, "VK access failed", "could not decrypt company VK login")
		return
	}
	phone := ""
	if len(phoneCiphertext) > 0 {
		plain, decryptErr := s.envelope.Decrypt(phoneCiphertext, phoneNonce)
		if decryptErr != nil {
			problem(w, http.StatusInternalServerError, "VK access failed", "could not decrypt company VK phone")
			return
		}
		phone = string(plain)
	}
	writeJSON(w, http.StatusOK, map[string]any{"accountId": accountID, "companyId": companyID, "companyName": companyName, "login": string(login), "phone": phone, "hasPassword": true, "communityUrl": communityURL})
}

func (s *Server) saveCreatorVKAccess(w http.ResponseWriter, r *http.Request) {
	creatorID := chi.URLParam(r, "id")
	p := r.Context().Value(principalKey).(principal)
	var in struct {
		AccountID    string `json:"accountId"`
		CommunityURL string `json:"communityUrl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		problem(w, http.StatusBadRequest, "invalid VK access", "expected JSON body")
		return
	}
	accountID := strings.TrimSpace(in.AccountID)
	communityURL := ""
	if accountID != "" {
		var valid bool
		communityURL, valid = normalizeVKCommunityURL(in.CommunityURL)
		if !valid {
			problem(w, http.StatusBadRequest, "invalid VK access", "a vk.ru or vk.com community link is required")
			return
		}
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "VK access save failed", "could not start update")
		return
	}
	defer tx.Rollback(r.Context())
	var oldAccountID, oldCompanyName, oldCommunityURL string
	err = tx.QueryRow(r.Context(), `SELECT COALESCE(v.company_vk_account_id::text,''),COALESCE(c.name,''),COALESCE(v.community_url,'') FROM creators cr LEFT JOIN creator_vk_assignments v ON v.creator_id=cr.id LEFT JOIN company_vk_accounts a ON a.id=v.company_vk_account_id LEFT JOIN companies c ON c.id=a.company_id WHERE cr.id=$1 AND cr.organization_id=$2 FOR UPDATE OF cr`, creatorID, p.OrganizationID).Scan(&oldAccountID, &oldCompanyName, &oldCommunityURL)
	if err == pgx.ErrNoRows {
		problem(w, http.StatusNotFound, "not found", "creator does not exist")
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "VK access save failed", "could not load creator")
		return
	}
	newCompanyName := ""
	if accountID != "" {
		if err = tx.QueryRow(r.Context(), `SELECT c.name FROM company_vk_accounts a JOIN companies c ON c.id=a.company_id WHERE a.id=$1 AND a.organization_id=$2 AND c.archived_at IS NULL`, accountID, p.OrganizationID).Scan(&newCompanyName); err == pgx.ErrNoRows {
			problem(w, http.StatusBadRequest, "invalid VK access", "company VK account does not exist")
			return
		} else if err != nil {
			problem(w, http.StatusInternalServerError, "VK access save failed", "could not validate company VK account")
			return
		}
	}
	if oldAccountID == accountID && oldCommunityURL == communityURL {
		if err = tx.Commit(r.Context()); err != nil {
			problem(w, http.StatusInternalServerError, "VK access save failed", "could not finish update")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if accountID == "" {
		if _, err = tx.Exec(r.Context(), `DELETE FROM creator_vk_assignments WHERE creator_id=$1`, creatorID); err != nil {
			problem(w, http.StatusInternalServerError, "VK access save failed", "could not clear creator VK access")
			return
		}
	} else if _, err = tx.Exec(r.Context(), `INSERT INTO creator_vk_assignments(creator_id,company_vk_account_id,community_url,updated_by) VALUES($1,$2,$3,$4) ON CONFLICT(creator_id) DO UPDATE SET company_vk_account_id=excluded.company_vk_account_id,community_url=excluded.community_url,updated_by=excluded.updated_by,updated_at=now()`, creatorID, accountID, communityURL, p.ID); err != nil {
		problem(w, http.StatusInternalServerError, "VK access save failed", "could not save creator VK access")
		return
	}
	changes := make([]creatorHistoryChange, 0, 2)
	if oldAccountID != accountID {
		changes = append(changes, creatorHistoryChange{Section: "VK_SHARED", FieldKey: "account", OldPresent: oldAccountID != "", NewPresent: accountID != "", OldValue: oldCompanyName, NewValue: newCompanyName})
	}
	if oldCommunityURL != communityURL {
		changes = append(changes, creatorHistoryChange{Section: "VK_SHARED", FieldKey: "communityUrl", OldPresent: oldCommunityURL != "", NewPresent: communityURL != "", OldValue: oldCommunityURL, NewValue: communityURL})
	}
	if err = insertCreatorHistory(r, tx, p, creatorID, "CREDENTIALS", changes); err != nil {
		problem(w, http.StatusInternalServerError, "VK access save failed", "could not save creator history")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,entity_id,metadata) VALUES($1,$2,'UPDATE_VK_ACCESS','CREATOR',$3,jsonb_build_object('companyVkAccountId',NULLIF($4::text,''),'communityUrl',NULLIF($5::text,'')))`, p.OrganizationID, p.ID, creatorID, accountID, communityURL); err != nil {
		problem(w, http.StatusInternalServerError, "VK access save failed", "could not save audit record")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "VK access save failed", "could not commit update")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
