package httpserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/xuri/excelize/v2"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/statzavod/statzavod/internal/config"
	crypt "github.com/statzavod/statzavod/internal/crypto"
)

type Server struct {
	pool     *pgxpool.Pool
	config   config.Config
	envelope *crypt.Envelope
}
type principal struct{ ID, Role, Email, OrganizationID string }
type contextKey string

const principalKey contextKey = "principal"

func New(pool *pgxpool.Pool, c config.Config) *Server {
	var envelope *crypt.Envelope
	if c.TokenEncryptionKey != "" {
		envelope, _ = crypt.NewFromBase64(c.TokenEncryptionKey)
	}
	return &Server{pool: pool, config: c, envelope: envelope}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(s.requestID, s.cors, s.recoverer)
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/readyz", s.ready)
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/oauth/{platform}/callback", s.oauthCallback)
		r.Post("/oauth/instagram/deauthorize", s.instagramDeauthorize)
		r.Post("/oauth/instagram/data-deletion", s.instagramDataDeletion)
		r.Get("/oauth/instagram/data-deletion/status", s.instagramDataDeletionStatus)
		r.Post("/auth/login", s.login)
		r.Post("/auth/accept-invitation", s.acceptInvitation)
		r.With(s.auth).Post("/auth/logout", s.logout)
		r.With(s.auth).Get("/auth/me", s.me)
		r.Group(func(r chi.Router) {
			r.Use(s.auth)
			r.Get("/companies", s.listCompanies)
			r.With(s.require("ADMIN", "ANALYST")).Post("/companies", s.createCompany)
			r.Get("/company-vk-accounts", s.listCompanyVKAccounts)
			r.With(s.require("ADMIN", "ANALYST")).Put("/companies/{id}/vk-account", s.saveCompanyVKAccount)
			r.With(s.require("ADMIN", "ANALYST")).Post("/companies/{id}/vk-account/authorize", s.companyVKOAuthAuthorize)
			r.With(s.require("ADMIN")).Post("/company-vk-accounts/{id}/password/reveal", s.revealCompanyVKPassword)
			r.With(s.require("ADMIN", "ANALYST")).Delete("/companies/{id}", s.archiveCompany)
			r.Get("/analytics/summary", s.summary)
			r.Get("/analytics/timeseries", s.timeseries)
			r.Get("/analytics/creators/{id}", s.creatorAnalytics)
			r.Get("/exports", s.exportCreator)
			r.Get("/creators", s.listCreators)
			r.With(s.require("ADMIN", "ANALYST")).Post("/creators", s.createCreator)
			r.Get("/creators/{id}", s.getCreator)
			r.Get("/creators/{id}/history", s.listCreatorHistory)
			r.With(s.require("ADMIN", "ANALYST")).Patch("/creators/{id}", s.updateCreator)
			r.With(s.require("ADMIN", "ANALYST")).Patch("/creators/{id}/work-status", s.updateCreatorWorkStatus)
			r.Get("/creators/{id}/credentials", s.listCreatorCredentials)
			r.With(s.require("ADMIN", "ANALYST")).Put("/creators/{id}/credentials", s.saveCreatorCredentials)
			r.With(s.require("ADMIN")).Post("/creators/{id}/credentials/{credentialID}/reveal", s.revealCreatorCredential)
			r.Get("/creators/{id}/vk-access", s.getCreatorVKAccess)
			r.With(s.require("ADMIN", "ANALYST")).Put("/creators/{id}/vk-access", s.saveCreatorVKAccess)
			r.With(s.require("ADMIN")).Post("/creators/{id}/history/changes/{changeID}/reveal", s.revealCreatorHistoryCredential)
			r.Get("/creators/{id}/accounts", s.listCreatorAccounts)
			r.With(s.require("ADMIN", "ANALYST")).Post("/creators/{id}/accounts", s.createCreatorAccount)
			r.With(s.require("ADMIN", "ANALYST")).Post("/creators/{id}/connections/{platform}/authorize", s.oauthAuthorize)
			r.Get("/creators/{id}/connections", s.platformConnections)
			r.Get("/integrations", s.integrationStatus)
			r.With(s.require("ADMIN")).Delete("/platform-accounts/{id}/connection", s.disconnectPlatform)
			r.With(s.require("ADMIN")).Delete("/platform-accounts/{id}/data", s.purgePlatformData)
			r.With(s.require("ADMIN", "ANALYST")).Post("/platform-accounts/{id}/sync", s.requestAccountSync)
			r.With(s.require("ADMIN", "ANALYST")).Post("/platform-accounts/{id}/pause", s.pausePlatformAccount)
			r.With(s.require("ADMIN", "ANALYST")).Post("/platform-accounts/{id}/resume", s.resumePlatformAccount)
			r.With(s.require("ADMIN", "ANALYST")).Post("/creators/{id}/contacts", s.createContact)
			r.With(s.require("ADMIN", "ANALYST")).Post("/creators/{id}/archive", s.archiveCreator)
			r.With(s.require("ADMIN", "ANALYST")).Post("/creators/{id}/restore", s.restoreCreator)
			r.Get("/publications", s.listPublications)
			r.Get("/content-groups", s.listContentGroups)
			r.With(s.require("ADMIN", "ANALYST")).Post("/content-groups", s.createContentGroup)
			r.Get("/sync/health", s.syncHealth)
			r.With(s.require("ADMIN")).Post("/users/invitations", s.createInvitation)
		})
	})
	return r
}

func (s *Server) EnsureBootstrap(ctx context.Context) error {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE email=$1)`, s.config.BootstrapEmail).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		hash, err := hashPassword(s.config.BootstrapPassword)
		if err != nil {
			return err
		}
		if _, err = s.pool.Exec(ctx, `INSERT INTO users(email,password_hash,role,status) VALUES($1,$2,'ADMIN','ACTIVE')`, s.config.BootstrapEmail, hash); err != nil {
			return err
		}
	}
	_, _ = s.pool.Exec(ctx, `INSERT INTO organizations(name,slug) VALUES('Statzavod','statzavod') ON CONFLICT(slug) DO NOTHING`)
	_, err := s.pool.Exec(ctx, `INSERT INTO organization_memberships(organization_id,user_id,role) SELECT o.id,u.id,u.role FROM organizations o JOIN users u ON u.email=$1 WHERE o.slug='statzavod' ON CONFLICT DO NOTHING`, s.config.BootstrapEmail)
	return err
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.pool.Ping(r.Context()); err != nil {
		problem(w, http.StatusServiceUnavailable, "database unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Password string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		problem(w, 400, "invalid request", "expected JSON body")
		return
	}
	var id, email, hash, role string
	err := s.pool.QueryRow(r.Context(), `SELECT id,email,password_hash,role FROM users WHERE email=$1 AND status='ACTIVE'`, strings.ToLower(strings.TrimSpace(in.Email))).Scan(&id, &email, &hash, &role)
	if err != nil || !verifyPassword(hash, in.Password) {
		problem(w, http.StatusUnauthorized, "invalid credentials", "email or password is incorrect")
		return
	}
	token := makeToken()
	digest := sha256.Sum256([]byte(token))
	_, err = s.pool.Exec(r.Context(), `INSERT INTO sessions(user_id,token_hash,expires_at,user_agent) VALUES($1,$2,$3,$4)`, id, digest[:], time.Now().Add(24*time.Hour), r.UserAgent())
	if err != nil {
		problem(w, 500, "session creation failed", err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{Name: s.config.CookieName, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.config.Environment == "production", MaxAge: 86400})
	_, _ = s.pool.Exec(r.Context(), `UPDATE users SET last_login_at=now(),updated_at=now() WHERE id=$1`, id)
	writeJSON(w, 200, map[string]any{"id": id, "email": email, "role": role})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.config.CookieName); err == nil {
		d := sha256.Sum256([]byte(c.Value))
		_, _ = s.pool.Exec(r.Context(), `UPDATE sessions SET revoked_at=now() WHERE token_hash=$1`, d[:])
	}
	http.SetCookie(w, &http.Cookie{Name: s.config.CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	writeJSON(w, 200, map[string]string{"id": p.ID, "email": p.Email, "role": p.Role})
}
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(s.config.CookieName)
		if err != nil {
			problem(w, 401, "authentication required", "sign in first")
			return
		}
		d := sha256.Sum256([]byte(c.Value))
		var p principal
		err = s.pool.QueryRow(r.Context(), `SELECT u.id,u.role,u.email,m.organization_id FROM sessions s JOIN users u ON u.id=s.user_id JOIN organization_memberships m ON m.user_id=u.id WHERE s.token_hash=$1 AND s.revoked_at IS NULL AND s.expires_at>now() AND u.status='ACTIVE' ORDER BY m.created_at LIMIT 1`, d[:]).Scan(&p.ID, &p.Role, &p.Email, &p.OrganizationID)
		if err != nil {
			problem(w, 401, "authentication required", "session expired")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, p)))
	})
}
func (s *Server) require(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := r.Context().Value(principalKey).(principal)
			for _, role := range roles {
				if p.Role == role {
					next.ServeHTTP(w, r)
					return
				}
			}
			problem(w, 403, "forbidden", "insufficient role")
		})
	}
}
func (s *Server) listCreators(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	scope := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("scope")))
	archiveFilter := "c.archived_at IS NULL"
	if scope == "archived" {
		archiveFilter = "c.archived_at IS NOT NULL"
	} else if scope != "" && scope != "active" {
		problem(w, http.StatusBadRequest, "invalid scope", "scope must be active or archived")
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT c.id,c.first_name,c.last_name,COALESCE(c.middle_name,''),c.display_name,c.status,c.created_at,c.telegram_username,COALESCE(c.company_id::text,''),COALESCE(x.name,''),c.work_status,c.work_comment,c.archived_at FROM creators c LEFT JOIN companies x ON x.id=c.company_id WHERE c.organization_id=$1 AND `+archiveFilter+` ORDER BY CASE c.status WHEN 'ACTIVE' THEN 0 WHEN 'ON_LEAVE' THEN 1 ELSE 2 END,c.display_name`, p.OrganizationID)
	if err != nil {
		problem(w, 500, "query failed", err.Error())
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, first, last, middle, display, status, telegram, companyID, companyName, workStatus, workComment string
		var created time.Time
		var archivedAt *time.Time
		if err := rows.Scan(&id, &first, &last, &middle, &display, &status, &created, &telegram, &companyID, &companyName, &workStatus, &workComment, &archivedAt); err != nil {
			problem(w, 500, "scan failed", err.Error())
			return
		}
		items = append(items, map[string]any{"id": id, "firstName": first, "lastName": last, "middleName": middle, "displayName": display, "status": status, "createdAt": created, "archivedAt": archivedAt, "telegramUsername": telegram, "companyId": companyID, "companyName": companyName, "workStatus": workStatus, "workComment": workComment})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) createCreator(w http.ResponseWriter, r *http.Request) {
	var in struct {
		FirstName        string `json:"firstName"`
		LastName         string `json:"lastName"`
		MiddleName       string `json:"middleName"`
		DisplayName      string `json:"displayName"`
		InternalNote     string `json:"internalNote"`
		TelegramUsername string `json:"telegramUsername"`
		CompanyID        string `json:"companyId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.FirstName) == "" || strings.TrimSpace(in.LastName) == "" {
		problem(w, 400, "invalid creator", "firstName and lastName are required")
		return
	}
	if in.DisplayName == "" {
		in.DisplayName = strings.TrimSpace(in.FirstName + " " + in.LastName)
	}
	var id string
	p := r.Context().Value(principalKey).(principal)
	var companyID any
	if strings.TrimSpace(in.CompanyID) != "" {
		var exists bool
		if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM companies WHERE id=$1 AND organization_id=$2 AND archived_at IS NULL)`, in.CompanyID, p.OrganizationID).Scan(&exists); err != nil || !exists {
			problem(w, http.StatusBadRequest, "invalid company", "company does not exist")
			return
		}
		companyID = in.CompanyID
	}
	err := s.pool.QueryRow(r.Context(), `INSERT INTO creators(organization_id,company_id,first_name,last_name,middle_name,display_name,internal_note,telegram_username) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, p.OrganizationID, companyID, in.FirstName, in.LastName, in.MiddleName, in.DisplayName, in.InternalNote, normalizeTelegram(in.TelegramUsername)).Scan(&id)
	if err != nil {
		problem(w, 500, "creation failed", err.Error())
		return
	}
	_, _ = s.pool.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,entity_id) VALUES($1,$2,'CREATE','CREATOR',$3)`, p.OrganizationID, p.ID, id)
	writeJSON(w, 201, map[string]string{"id": id})
}
func (s *Server) getCreator(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p := r.Context().Value(principalKey).(principal)
	var first, last, middle, display, status, note, telegram, companyID, companyName, workStatus, workComment string
	var archivedAt *time.Time
	err := s.pool.QueryRow(r.Context(), `SELECT c.first_name,c.last_name,COALESCE(c.middle_name,''),c.display_name,c.status,c.internal_note,c.telegram_username,COALESCE(c.company_id::text,''),COALESCE(x.name,''),c.work_status,c.work_comment,c.archived_at FROM creators c LEFT JOIN companies x ON x.id=c.company_id WHERE c.id=$1 AND c.organization_id=$2`, id, p.OrganizationID).Scan(&first, &last, &middle, &display, &status, &note, &telegram, &companyID, &companyName, &workStatus, &workComment, &archivedAt)
	if err == pgx.ErrNoRows {
		problem(w, 404, "not found", "creator does not exist")
		return
	}
	if err != nil {
		problem(w, 500, "query failed", err.Error())
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT id,kind,value,COALESCE(label,''),is_primary FROM creator_contacts WHERE creator_id=$1 ORDER BY is_primary DESC,created_at`, id)
	if err != nil {
		problem(w, 500, "contacts failed", err.Error())
		return
	}
	defer rows.Close()
	contacts := make([]map[string]any, 0)
	for rows.Next() {
		var cid, kind, value, label string
		var primary bool
		if err := rows.Scan(&cid, &kind, &value, &label, &primary); err != nil {
			problem(w, 500, "contacts failed", err.Error())
			return
		}
		contacts = append(contacts, map[string]any{"id": cid, "kind": kind, "value": value, "label": label, "isPrimary": primary})
	}
	writeJSON(w, 200, map[string]any{"id": id, "firstName": first, "lastName": last, "middleName": middle, "displayName": display, "status": status, "internalNote": note, "archivedAt": archivedAt, "telegramUsername": telegram, "companyId": companyID, "companyName": companyName, "workStatus": workStatus, "workComment": workComment, "contacts": contacts})
}
func (s *Server) createContact(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p := r.Context().Value(principalKey).(principal)
	var in struct {
		Kind      string `json:"kind"`
		Value     string `json:"value"`
		Label     string `json:"label"`
		IsPrimary bool   `json:"isPrimary"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Kind) == "" || strings.TrimSpace(in.Value) == "" {
		problem(w, 400, "invalid contact", "kind and value are required")
		return
	}
	var owned bool
	_ = s.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM creators WHERE id=$1 AND organization_id=$2)`, id, p.OrganizationID).Scan(&owned)
	if !owned {
		problem(w, 404, "not found", "creator does not exist")
		return
	}
	if in.IsPrimary {
		_, _ = s.pool.Exec(r.Context(), `UPDATE creator_contacts SET is_primary=false WHERE creator_id=$1`, id)
	}
	var cid string
	err := s.pool.QueryRow(r.Context(), `INSERT INTO creator_contacts(creator_id,kind,value,label,is_primary) VALUES($1,$2,$3,$4,$5) RETURNING id`, id, in.Kind, in.Value, in.Label, in.IsPrimary).Scan(&cid)
	if err != nil {
		problem(w, 500, "contact creation failed", err.Error())
		return
	}
	writeJSON(w, 201, map[string]string{"id": cid})
}
func (s *Server) listCreatorAccounts(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p := r.Context().Value(principalKey).(principal)
	rows, err := s.pool.Query(r.Context(), `SELECT a.id,a.platform,a.username,a.display_name,a.status,COALESCE(a.profile_url,'') FROM platform_accounts a JOIN creator_account_assignments x ON x.platform_account_id=a.id WHERE x.creator_id=$1 AND a.organization_id=$2 AND x.valid_to IS NULL ORDER BY a.platform,a.username`, id, p.OrganizationID)
	if err != nil {
		problem(w, 500, "accounts failed", err.Error())
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var aid, platform, username, display, status, url string
		if err := rows.Scan(&aid, &platform, &username, &display, &status, &url); err != nil {
			problem(w, 500, "accounts failed", err.Error())
			return
		}
		items = append(items, map[string]any{"id": aid, "platform": platform, "username": username, "displayName": display, "status": status, "profileUrl": url})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) createCreatorAccount(w http.ResponseWriter, r *http.Request) {
	creatorID := chi.URLParam(r, "id")
	p := r.Context().Value(principalKey).(principal)
	var in struct {
		Platform    string `json:"platform"`
		ExternalID  string `json:"externalId"`
		Username    string `json:"username"`
		DisplayName string `json:"displayName"`
		ProfileURL  string `json:"profileUrl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Platform == "" || in.ExternalID == "" || in.Username == "" {
		problem(w, 400, "invalid account", "platform, externalId and username are required")
		return
	}
	var accountID string
	err := s.pool.QueryRow(r.Context(), `INSERT INTO platform_accounts(organization_id,platform,external_id,username,display_name,profile_url,status) VALUES($1,$2,$3,$4,$5,$6,'REAUTH_REQUIRED') ON CONFLICT(organization_id,platform,external_id) DO UPDATE SET username=excluded.username,display_name=excluded.display_name,profile_url=excluded.profile_url RETURNING id`, p.OrganizationID, in.Platform, in.ExternalID, in.Username, in.DisplayName, in.ProfileURL).Scan(&accountID)
	if err != nil {
		problem(w, 500, "account creation failed", err.Error())
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, 500, "assignment failed", err.Error())
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `UPDATE creator_account_assignments SET valid_to=now() WHERE platform_account_id=$1 AND valid_to IS NULL`, accountID); err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO creator_account_assignments(creator_id,platform_account_id) VALUES($1,$2)`, creatorID, accountID)
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		problem(w, 500, "assignment failed", "could not assign account")
		return
	}
	writeJSON(w, 201, map[string]string{"id": accountID})
}
func (s *Server) archiveCreator(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p := r.Context().Value(principalKey).(principal)
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not start archive")
		return
	}
	defer tx.Rollback(r.Context())
	tag, err := tx.Exec(r.Context(), `UPDATE creators SET archived_at=now(),updated_at=now() WHERE id=$1 AND organization_id=$2 AND archived_at IS NULL`, id, p.OrganizationID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not archive creator")
		return
	}
	if tag.RowsAffected() == 0 {
		problem(w, http.StatusNotFound, "not found", "active creator does not exist")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,entity_id) VALUES($1,$2,'ARCHIVE','CREATOR',$3)`, p.OrganizationID, p.ID, id); err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not save audit record")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not commit archive")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) restoreCreator(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p := r.Context().Value(principalKey).(principal)
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "restore failed", "could not start restore")
		return
	}
	defer tx.Rollback(r.Context())
	tag, err := tx.Exec(r.Context(), `UPDATE creators SET status=CASE WHEN status='ARCHIVED' THEN 'ACTIVE'::creator_status ELSE status END,archived_at=NULL,updated_at=now() WHERE id=$1 AND organization_id=$2 AND archived_at IS NOT NULL`, id, p.OrganizationID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "restore failed", "could not restore creator")
		return
	}
	if tag.RowsAffected() == 0 {
		problem(w, http.StatusNotFound, "not found", "archived creator does not exist")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,entity_id) VALUES($1,$2,'RESTORE','CREATOR',$3)`, p.OrganizationID, p.ID, id); err != nil {
		problem(w, http.StatusInternalServerError, "restore failed", "could not save audit record")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "restore failed", "could not commit restore")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) listContentGroups(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pool.Query(r.Context(), `SELECT g.id,g.name,g.status,c.display_name,count(m.publication_id) FROM content_groups g JOIN creators c ON c.id=g.creator_id LEFT JOIN content_group_members m ON m.content_group_id=g.id GROUP BY g.id,c.display_name ORDER BY g.created_at DESC`)
	if err != nil {
		problem(w, 500, "groups failed", err.Error())
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, name, status, creator string
		var count int64
		if err := rows.Scan(&id, &name, &status, &creator, &count); err != nil {
			problem(w, 500, "groups failed", err.Error())
			return
		}
		items = append(items, map[string]any{"id": id, "name": name, "status": status, "creatorName": creator, "publicationCount": count})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) createContentGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CreatorID string `json:"creatorId"`
		Name      string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.CreatorID == "" || strings.TrimSpace(in.Name) == "" {
		problem(w, 400, "invalid group", "creatorId and name are required")
		return
	}
	p := r.Context().Value(principalKey).(principal)
	var id string
	err := s.pool.QueryRow(r.Context(), `INSERT INTO content_groups(creator_id,name,created_by) VALUES($1,$2,$3) RETURNING id`, in.CreatorID, in.Name, p.ID).Scan(&id)
	if err != nil {
		problem(w, 500, "group creation failed", err.Error())
		return
	}
	writeJSON(w, 201, map[string]string{"id": id})
}
func (s *Server) listPublications(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	rows, err := s.pool.Query(r.Context(), `SELECT p.id,p.title,p.platform,p.published_at,c.display_name,COALESCE(x.id::text,''),COALESCE(x.name,''),COALESCE((SELECT views FROM publication_metric_snapshots s WHERE s.publication_id=p.id ORDER BY observed_at DESC LIMIT 1),0) FROM publications p JOIN creators c ON c.id=p.creator_id LEFT JOIN companies x ON x.id=c.company_id WHERE p.organization_id=$1 ORDER BY p.published_at DESC LIMIT 100`, p.OrganizationID)
	if err != nil {
		problem(w, 500, "query failed", err.Error())
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, platform, creator, companyID, companyName string
		var title *string
		var published time.Time
		var views int64
		if err := rows.Scan(&id, &title, &platform, &published, &creator, &companyID, &companyName, &views); err != nil {
			problem(w, http.StatusInternalServerError, "scan failed", err.Error())
			return
		}
		items = append(items, map[string]any{"id": id, "title": title, "platform": platform, "publishedAt": published, "creatorName": creator, "companyId": companyID, "companyName": companyName, "views": views})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	var creators, publications int64
	var views, likes int64
	err := s.pool.QueryRow(r.Context(), `SELECT (SELECT count(*) FROM creators WHERE status='ACTIVE' AND archived_at IS NULL AND organization_id=$1),(SELECT count(*) FROM publications WHERE organization_id=$1),(SELECT COALESCE(sum(x.views),0) FROM (SELECT DISTINCT ON (s.publication_id) s.views FROM publication_metric_snapshots s JOIN publications p ON p.id=s.publication_id WHERE p.organization_id=$1 ORDER BY s.publication_id,s.observed_at DESC) x),(SELECT COALESCE(sum(x.likes),0) FROM (SELECT DISTINCT ON (s.publication_id) s.likes FROM publication_metric_snapshots s JOIN publications p ON p.id=s.publication_id WHERE p.organization_id=$1 ORDER BY s.publication_id,s.observed_at DESC) x)`, p.OrganizationID).Scan(&creators, &publications, &views, &likes)
	if err != nil {
		problem(w, 500, "summary failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"kpis": []map[string]any{{"key": "views", "label": "Просмотры", "value": views}, {"key": "likes", "label": "Реакции", "value": likes}, {"key": "publications", "label": "Публикации", "value": publications}, {"key": "creators", "label": "Креаторы", "value": creators}}, "freshness": map[string]string{"status": "partial", "message": "Подключите платформенные аккаунты для сбора данных."}})
}
func (s *Server) timeseries(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pool.Query(r.Context(), `SELECT observed_at::date,COALESCE(sum(views),0) FROM publication_metric_snapshots GROUP BY observed_at::date ORDER BY observed_at::date`)
	if err != nil {
		problem(w, 500, "timeseries failed", err.Error())
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var d time.Time
		var v int64
		_ = rows.Scan(&d, &v)
		items = append(items, map[string]any{"date": d.Format("2006-01-02"), "views": v})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) creatorAnalytics(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p := r.Context().Value(principalKey).(principal)
	from, to := r.URL.Query().Get("activityFrom"), r.URL.Query().Get("activityTo")
	var name string
	if err := s.pool.QueryRow(r.Context(), `SELECT display_name FROM creators WHERE id=$1 AND organization_id=$2`, id, p.OrganizationID).Scan(&name); err == pgx.ErrNoRows {
		problem(w, 404, "not found", "creator does not exist")
		return
	} else if err != nil {
		problem(w, 500, "query failed", err.Error())
		return
	}
	query := `SELECT count(DISTINCT p.id), COALESCE(sum(s.views),0), COALESCE(sum(s.likes),0), COALESCE(sum(s.comments),0), COALESCE(sum(s.shares),0) FROM publications p LEFT JOIN LATERAL (SELECT views,likes,comments,shares FROM publication_metric_snapshots WHERE publication_id=p.id ORDER BY observed_at DESC LIMIT 1) s ON true WHERE p.creator_id=$1 AND p.organization_id=$2`
	args := []any{id, p.OrganizationID}
	if from != "" {
		query += " AND p.published_at >= $" + fmt.Sprint(len(args)+1)
		args = append(args, from)
	}
	if to != "" {
		query += " AND p.published_at < ($" + fmt.Sprint(len(args)+1) + "::date + interval '1 day')"
		args = append(args, to)
	}
	var publications, views, likes, comments, shares int64
	if err := s.pool.QueryRow(r.Context(), query, args...).Scan(&publications, &views, &likes, &comments, &shares); err != nil {
		problem(w, 500, "analytics failed", err.Error())
		return
	}
	publicationQuery := `SELECT p.id,COALESCE(p.title,''),p.platform,p.published_at,COALESCE(s.views,0),COALESCE(s.likes,0) FROM publications p LEFT JOIN LATERAL (SELECT views,likes FROM publication_metric_snapshots WHERE publication_id=p.id ORDER BY observed_at DESC LIMIT 1) s ON true WHERE p.creator_id=$1 AND p.organization_id=$2`
	publicationArgs := []any{id, p.OrganizationID}
	if from != "" {
		publicationQuery += " AND p.published_at >= $" + fmt.Sprint(len(publicationArgs)+1)
		publicationArgs = append(publicationArgs, from)
	}
	if to != "" {
		publicationQuery += " AND p.published_at < ($" + fmt.Sprint(len(publicationArgs)+1) + "::date + interval '1 day')"
		publicationArgs = append(publicationArgs, to)
	}
	publicationQuery += " ORDER BY p.published_at DESC LIMIT 100"
	rows, err := s.pool.Query(r.Context(), publicationQuery, publicationArgs...)
	if err != nil {
		problem(w, 500, "analytics failed", err.Error())
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var pid, title, platform string
		var published time.Time
		var pv, pl int64
		if err := rows.Scan(&pid, &title, &platform, &published, &pv, &pl); err != nil {
			problem(w, 500, "scan failed", err.Error())
			return
		}
		items = append(items, map[string]any{"id": pid, "title": title, "platform": platform, "publishedAt": published, "views": pv, "likes": pl})
	}
	writeJSON(w, 200, map[string]any{"creatorId": id, "creatorName": name, "period": map[string]string{"from": from, "to": to}, "kpis": []map[string]any{{"key": "views", "label": "Просмотры", "value": views}, {"key": "likes", "label": "Реакции", "value": likes}, {"key": "comments", "label": "Комментарии", "value": comments}, {"key": "shares", "label": "Репосты", "value": shares}, {"key": "publications", "label": "Публикации", "value": publications}}, "publications": items})
}

func (s *Server) exportCreator(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	rawIDs := r.URL.Query().Get("creatorIds")
	if rawIDs == "" {
		rawIDs = r.URL.Query().Get("creatorId")
	}
	ids := make([]string, 0)
	for _, id := range strings.Split(rawIDs, ",") {
		if value := strings.TrimSpace(id); value != "" {
			ids = append(ids, value)
		}
	}
	if len(ids) == 0 {
		problem(w, 400, "invalid export", "at least one creatorId is required")
		return
	}

	creatorRows, err := s.pool.Query(r.Context(), `SELECT id,display_name FROM creators WHERE id::text=ANY($1) AND organization_id=$2 ORDER BY display_name`, ids, p.OrganizationID)
	if err != nil {
		problem(w, 500, "export failed", "could not load creators")
		return
	}
	defer creatorRows.Close()
	names := map[string]string{}
	for creatorRows.Next() {
		var id, name string
		if creatorRows.Scan(&id, &name) == nil {
			names[id] = name
		}
	}
	if len(names) != len(ids) {
		problem(w, 404, "not found", "one or more creators do not exist")
		return
	}

	from, to := r.URL.Query().Get("activityFrom"), r.URL.Query().Get("activityTo")
	where := ` WHERE p.creator_id::text=ANY($1) AND p.organization_id=$2`
	args := []any{ids, p.OrganizationID}
	if from != "" {
		where += " AND p.published_at >= $" + fmt.Sprint(len(args)+1)
		args = append(args, from)
	}
	if to != "" {
		where += " AND p.published_at < ($" + fmt.Sprint(len(args)+1) + "::date + interval '1 day')"
		args = append(args, to)
	}

	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	sheet := "Сводка"
	f.SetSheetName("Sheet1", sheet)
	_ = f.SetCellValue(sheet, "A1", "Отчёт по креаторам")
	_ = f.SetCellValue(sheet, "A2", "Период")
	_ = f.SetCellValue(sheet, "B2", from+" — "+to)
	for column, value := range []string{"Креатор", "Просмотры", "Реакции", "Комментарии", "Репосты", "Публикации"} {
		_ = f.SetCellValue(sheet, string(rune('A'+column))+"4", value)
	}

	summaryQuery := `SELECT p.creator_id,COALESCE(sum(s.views),0),COALESCE(sum(s.likes),0),COALESCE(sum(s.comments),0),COALESCE(sum(s.shares),0),count(DISTINCT p.id) FROM publications p LEFT JOIN LATERAL (SELECT views,likes,comments,shares FROM publication_metric_snapshots WHERE publication_id=p.id ORDER BY observed_at DESC LIMIT 1) s ON true` + where + ` GROUP BY p.creator_id`
	summaryRows, queryErr := s.pool.Query(r.Context(), summaryQuery, args...)
	summary := map[string][5]int64{}
	if queryErr == nil {
		defer summaryRows.Close()
		for summaryRows.Next() {
			var id string
			var values [5]int64
			if summaryRows.Scan(&id, &values[0], &values[1], &values[2], &values[3], &values[4]) == nil {
				summary[id] = values
			}
		}
	}
	rowNumber := 5
	for _, id := range ids {
		values := summary[id]
		_ = f.SetCellValue(sheet, "A"+fmt.Sprint(rowNumber), names[id])
		for index, value := range values {
			_ = f.SetCellValue(sheet, string(rune('B'+index))+fmt.Sprint(rowNumber), value)
		}
		rowNumber++
	}

	pubs, _ := f.NewSheet("Публикации")
	_ = f.SetCellValue("Публикации", "A1", "Название")
	_ = f.SetCellValue("Публикации", "B1", "Креатор")
	_ = f.SetCellValue("Публикации", "C1", "Платформа")
	_ = f.SetCellValue("Публикации", "D1", "Дата публикации")
	_ = f.SetCellValue("Публикации", "E1", "Просмотры")
	_ = f.SetCellValue("Публикации", "F1", "Реакции")
	rows, err := s.pool.Query(r.Context(), `SELECT COALESCE(p.title,''),c.display_name,p.platform,p.published_at,COALESCE(s.views,0),COALESCE(s.likes,0) FROM publications p JOIN creators c ON c.id=p.creator_id LEFT JOIN LATERAL (SELECT views,likes FROM publication_metric_snapshots WHERE publication_id=p.id ORDER BY observed_at DESC LIMIT 1) s ON true`+where+` ORDER BY p.published_at DESC`, args...)
	if err == nil {
		defer rows.Close()
		row := 2
		for rows.Next() {
			var title, creator, platform string
			var published time.Time
			var pviews, plikes int64
			if rows.Scan(&title, &creator, &platform, &published, &pviews, &plikes) == nil {
				_ = f.SetCellValue("Публикации", "A"+fmt.Sprint(row), title)
				_ = f.SetCellValue("Публикации", "B"+fmt.Sprint(row), creator)
				_ = f.SetCellValue("Публикации", "C"+fmt.Sprint(row), platform)
				_ = f.SetCellValue("Публикации", "D"+fmt.Sprint(row), published)
				_ = f.SetCellValue("Публикации", "E"+fmt.Sprint(row), pviews)
				_ = f.SetCellValue("Публикации", "F"+fmt.Sprint(row), plikes)
				row++
			}
		}
	}
	_ = f.SetColWidth(sheet, "A", "F", 22)
	_ = f.SetColWidth("Публикации", "A", "F", 22)
	_ = f.SetPanes(sheet, &excelize.Panes{Freeze: true, YSplit: 4, TopLeftCell: "A5", ActivePane: "bottomLeft"})
	_ = f.SetPanes("Публикации", &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
	f.SetActiveSheet(pubs)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="creator-report.xlsx"`)
	if err := f.Write(w); err != nil {
		problem(w, 500, "export failed", err.Error())
	}
}
func (s *Server) syncHealth(w http.ResponseWriter, r *http.Request) {
	var due int64
	_ = s.pool.QueryRow(r.Context(), `SELECT count(*) FROM sync_targets WHERE next_sync_at<=now() AND status='ACTIVE'`).Scan(&due)
	writeJSON(w, 200, map[string]any{"dueTargets": due, "status": "healthy"})
}
func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := makeToken()[:16]
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r)
	})
}
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", s.config.CORSOrigin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				problem(w, 500, "internal server error", "unexpected failure")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": title, "status": status, "detail": detail})
}
func makeToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("token entropy: %w", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
