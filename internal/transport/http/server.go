package httpserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
	pool                   *pgxpool.Pool
	config                 config.Config
	envelope               *crypt.Envelope
	now                    func() time.Time
	revokeLifecycleToken   func(context.Context, string, string, bool) error
	lifecycleRevokeTimeout time.Duration
	auditFailure           func(auditRecord) error
	publishAdapters        map[string]PublishAdapter
	publishLeaseDuration   time.Duration
	mediaValidationSlots   chan struct{}
	mediaValidationLease   time.Duration
	mediaCleanupLease      time.Duration
	mediaHeartbeatEvery    time.Duration
}
type contextKey string

const principalKey contextKey = "principal"

func New(pool *pgxpool.Pool, c config.Config) *Server {
	var envelope *crypt.Envelope
	if c.TokenEncryptionKey != "" {
		envelope, _ = crypt.NewFromBase64(c.TokenEncryptionKey)
	}
	s := &Server{pool: pool, config: c, envelope: envelope, now: time.Now, lifecycleRevokeTimeout: lifecycleRevokeDeadline, publishAdapters: map[string]PublishAdapter{}, publishLeaseDuration: 10 * time.Minute, mediaValidationSlots: make(chan struct{}, 1), mediaValidationLease: 10 * time.Minute, mediaCleanupLease: 10 * time.Minute, mediaHeartbeatEvery: time.Minute}
	s.revokeLifecycleToken = s.defaultLifecycleTokenRevoke
	// Direct Post is deliberately registered even while disabled: the adapter
	// returns the feature-gate error and never falls back to a generic retry.
	s.SetPublishAdapter("TIKTOK", newTikTokPublishAdapter(s))
	s.SetPublishAdapter("INSTAGRAM", newInstagramPublishAdapter(s))
	s.SetPublishAdapter("YOUTUBE", newYouTubePublishAdapter(s))
	s.SetPublishAdapter("VK", newVKPublishAdapter(s))
	return s
}

func englishRequest(r *http.Request) bool {
	locale := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("locale")))
	if locale == "" {
		locale = strings.ToLower(strings.TrimSpace(r.Header.Get("Accept-Language")))
	}
	return locale == "en" || strings.HasPrefix(locale, "en-") || strings.HasPrefix(locale, "en,") || strings.HasPrefix(locale, "en;")
}

func localized(r *http.Request, russian, english string) string {
	if englishRequest(r) {
		return english
	}
	return russian
}

func containsCyrillic(value string) bool {
	for _, character := range value {
		if (character >= '\u0400' && character <= '\u052f') || (character >= '\u2de0' && character <= '\u2dff') || (character >= '\ua640' && character <= '\ua69f') {
			return true
		}
	}
	return false
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(s.requestID, s.cors, s.recoverer)
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/readyz", s.ready)
	// Scraped only over the private compose network. The production reverse
	// proxy explicitly denies this path on the public site.
	r.Get("/metrics", s.metrics)
	// Provider pulls cannot carry a StatZavod user session. The opaque,
	// authenticated path is the only capability and is short lived.
	r.Get("/api/v1/media/delivery/{token}", s.serveProviderMedia)
	r.Head("/api/v1/media/delivery/{token}", s.serveProviderMedia)
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/oauth/{platform}/callback", s.oauthCallback)
		r.Post("/oauth/instagram/deauthorize", s.instagramDeauthorize)
		r.Post("/oauth/instagram/data-deletion", s.instagramDataDeletion)
		r.Get("/oauth/instagram/data-deletion/status", s.instagramDataDeletionStatus)
		r.Get("/deletions/{id}", s.deletionReceiptStatus)
		r.Post("/auth/login", s.login)
		r.Post("/auth/accept-invitation", s.acceptInvitation)
		r.With(s.auth).Post("/auth/logout", s.logout)
		r.With(s.auth, s.auditAuthenticatedBusiness).Get("/auth/me", s.me)
		r.With(s.auth, s.auditAuthenticatedBusiness).Post("/auth/context", s.setAuthContext)
		r.Group(func(r chi.Router) {
			r.Use(s.auth)
			r.Use(s.auditAuthenticatedBusiness)
			r.With(s.requireManagement).Get("/companies", s.listCompanies)
			r.With(s.requireOwner).Get("/companies/archive", s.listArchivedCompanies)
			r.With(s.requireOwner).Post("/companies", s.createCompany)
			r.With(s.requireManagement).Get("/company-vk-accounts", s.listCompanyVKAccounts)
			r.With(s.requireCompanyPermission("CREDENTIAL_EDIT", "company")).Put("/companies/{id}/vk-account", s.saveCompanyVKAccount)
			r.With(s.requireCompanyPermission("SOCIAL_CONNECT", "company")).Post("/companies/{id}/vk-account/authorize", s.companyVKOAuthAuthorize)
			r.With(s.requireCompanyPermission("SECRET_REVEAL", "company-vk-account")).Post("/company-vk-accounts/{id}/password/reveal", s.revealCompanyVKPassword)
			r.With(s.requireCompanyAccess("company"), s.requireOwner).Delete("/companies/{id}", s.archiveCompany)
			r.With(s.requireOwner).Post("/companies/{id}/restore", s.restoreCompany)
			r.With(s.requireActiveCompanyPermission("STATS_VIEW")).Get("/analytics/summary", s.summary)
			r.With(s.requireActiveCompanyPermission("STATS_VIEW")).Get("/analytics/timeseries", s.timeseries)
			r.With(s.requireCompanyPermission("STATS_VIEW", "creator")).Get("/analytics/creators/{id}", s.creatorAnalytics)
			r.With(s.requireActiveCompanyPermission("STATS_EXPORT")).Get("/exports", s.exportCreator)
			r.With(s.requireManagement).Get("/creators", s.listCreators)
			r.With(s.requireManagement).Post("/creators", s.createCreator)
			r.With(s.requireCompanyAccess("creator")).Get("/creators/{id}", s.getCreator)
			r.With(s.requireCompanyAccess("creator")).Get("/creators/{id}/history", s.listCreatorHistory)
			r.With(s.requireCompanyPermission("CREATOR_EDIT", "creator")).Patch("/creators/{id}", s.updateCreator)
			r.With(s.requireCompanyPermission("CREATOR_EDIT", "creator")).Patch("/creators/{id}/work-status", s.updateCreatorWorkStatus)
			r.With(s.requireCompanyAccess("creator")).Get("/creators/{id}/credentials", s.listCreatorCredentials)
			r.With(s.requireCompanyPermission("CREDENTIAL_EDIT", "creator")).Put("/creators/{id}/credentials", s.saveCreatorCredentials)
			r.With(s.requireCompanyPermission("SECRET_REVEAL", "creator")).Post("/creators/{id}/credentials/{credentialID}/reveal", s.revealCreatorCredential)
			r.With(s.requireCompanyAccess("creator")).Get("/creators/{id}/vk-access", s.getCreatorVKAccess)
			r.With(s.requireCompanyPermission("SOCIAL_CONNECT", "creator")).Put("/creators/{id}/vk-access", s.saveCreatorVKAccess)
			r.With(s.requireCompanyPermission("SECRET_REVEAL", "creator")).Post("/creators/{id}/history/changes/{changeID}/reveal", s.revealCreatorHistoryCredential)
			r.With(s.requireCompanyAccess("creator")).Get("/creators/{id}/accounts", s.listCreatorAccounts)
			r.With(s.requireCompanyPermission("SOCIAL_CONNECT", "creator")).Post("/creators/{id}/accounts", s.createCreatorAccount)
			r.With(s.requireCompanyPermission("CREATOR_ACCOUNT_MANAGE", "creator")).Get("/creators/{id}/login-account", s.getCreatorLoginAccount)
			r.With(s.requireCompanyPermission("CREATOR_ACCOUNT_MANAGE", "creator")).Put("/creators/{id}/login-account", s.putCreatorLoginAccount)
			r.With(s.requireCompanyPermission("SOCIAL_CONNECT", "creator")).Post("/creators/{id}/connections/{platform}/authorize", s.oauthAuthorize)
			r.With(s.requireCompanyPermission("SOCIAL_CONNECT", "creator")).Get("/creators/{id}/connections/instagram-facebook/selections/{selectionID}", s.getInstagramAccountSelection)
			r.With(s.requireCompanyPermission("SOCIAL_CONNECT", "creator")).Post("/creators/{id}/connections/instagram-facebook/selections/{selectionID}", s.completeInstagramAccountSelection)
			r.With(s.requireCompanyAccess("creator")).Get("/creators/{id}/connections", s.platformConnections)
			r.With(s.requireManagement).Get("/integrations", s.integrationStatus)
			r.With(s.requireCompanyPermission("SOCIAL_CONNECT", "platform-account")).Delete("/platform-accounts/{id}/connection", s.disconnectPlatform)
			r.With(s.requireCompanyPermission("SOCIAL_CONNECT", "platform-account")).Delete("/platform-accounts/{id}/data", s.purgePlatformData)
			r.With(s.requireCompanyPermission("SYNC_MANAGE", "platform-account")).Post("/platform-accounts/{id}/sync", s.requestAccountSync)
			r.With(s.requireCompanyPermission("SYNC_MANAGE", "platform-account")).Post("/platform-accounts/{id}/pause", s.pausePlatformAccount)
			r.With(s.requireCompanyPermission("SYNC_MANAGE", "platform-account")).Post("/platform-accounts/{id}/resume", s.resumePlatformAccount)
			r.With(s.requireCompanyPermission("CREATOR_EDIT", "creator")).Post("/creators/{id}/contacts", s.createContact)
			r.With(s.requireCompanyPermission("CREATOR_ARCHIVE", "creator")).Post("/creators/{id}/archive", s.archiveCreator)
			r.With(s.requireCompanyPermission("CREATOR_ARCHIVE", "creator")).Post("/creators/{id}/restore", s.restoreCreator)
			r.With(s.requireCompanyPermission("CREATOR_DELETE", "creator")).Delete("/creators/{id}", s.deleteCreator)
			r.With(s.requireActiveCompanyPermission("STATS_VIEW")).Get("/publications", s.listPublications)
			r.Get("/notifications", s.listContentNotifications)
			r.Post("/notifications/read-all", s.markAllContentNotificationsRead)
			r.Post("/notifications/{notificationID}/read", s.markContentNotificationRead)
			r.With(s.requireManagement).Get("/content-items", s.listContentItems)
			r.With(s.requireManagement).Post("/content-items", s.createContentItem)
			r.With(s.requireManagement).Post("/content-preflight", s.composerPreflight)
			r.With(s.requireManagement).Get("/content-items/{id}", s.getContentItem)
			r.With(s.requireManagement).Get("/content-items/{id}/attempts", s.listContentAttempts)
			r.With(s.requireManagement).Patch("/content-items/{id}", s.patchContentItem)
			r.With(s.requireManagement).Post("/content-items/{id}/copy", s.copyContentItem)
			r.With(s.requireManagement).Post("/content-items/{id}/preflight", s.preflightContentItem)
			r.With(s.requireManagement).Post("/content-items/{id}/submit", s.submitContentItem)
			r.With(s.requireManagement).Post("/content-items/{id}/approve", s.approveContentItem)
			r.With(s.requireManagement).Post("/content-items/{id}/reject", s.rejectContentItem)
			r.With(s.requireManagement).Post("/content-items/{id}/publish", s.publishContentItem)
			r.With(s.requireManagement).Post("/content-items/{id}/schedule", s.scheduleContentItem)
			r.With(s.requireManagement).Post("/content-items/{id}/retry", s.retryContentTargets)
			r.With(s.requireManagement).Post("/content-items/{id}/cancel", s.cancelContentItem)
			r.With(s.requireManagement).Get("/creators/{id}/content-approval-policy", s.getContentApprovalPolicy)
			r.With(s.requireManagement).Put("/creators/{id}/content-approval-policy", s.putContentApprovalPolicy)
			r.With(s.requireManagement).Post("/media/uploads", s.createMediaUpload)
			r.With(s.requireManagement).Post("/media/uploads/{id}/parts/{partNumber}", s.signMediaUploadPart)
			r.With(s.requireManagement).Post("/media/uploads/{id}/complete", s.completeMediaUpload)
			r.With(s.requireManagement).Delete("/media/uploads/{id}", s.abortMediaUpload)
			r.With(s.requireManagement).Get("/content-groups", s.listContentGroups)
			r.With(s.requireManagement).Post("/content-groups", s.createContentGroup)
			r.With(s.requireManagement).Get("/sync/health", s.syncHealth)

			// Creator portal routes never accept a creator ID. Every resource is
			// derived from the validated session context on each request.
			r.With(s.requireCreatorPortal).Get("/creator-portal/profiles", s.listOwnCreatorProfiles)
			r.With(s.requireActiveOwnCreator).Get("/creator-portal/profile", s.getOwnCreatorProfile)
			r.With(s.requireActiveOwnCreator).Get("/creator-portal/socials", s.listOwnSocials)
			r.With(s.requireActiveOwnCreator).Get("/creator-portal/credentials", s.listOwnCredentials)
			r.With(s.requireActiveOwnCreator).Post("/creator-portal/credentials/{credentialID}/reveal", s.revealOwnCredential)
			r.With(s.requireActiveOwnCreator).Get("/creator-portal/publications", s.listOwnPublications)
			r.With(s.requireActiveOwnCreator).Get("/creator-portal/content-items", s.listContentItems)
			r.With(s.requireActiveOwnCreator).Post("/creator-portal/content-items", s.createContentItem)
			r.With(s.requireActiveOwnCreator).Post("/creator-portal/content-preflight", s.composerPreflight)
			r.With(s.requireActiveOwnCreator).Get("/creator-portal/content-items/{itemID}", s.getContentItem)
			r.With(s.requireActiveOwnCreator).Get("/creator-portal/content-items/{itemID}/attempts", s.listContentAttempts)
			r.With(s.requireActiveOwnCreator).Patch("/creator-portal/content-items/{itemID}", s.patchContentItem)
			r.With(s.requireActiveOwnCreator).Post("/creator-portal/content-items/{itemID}/copy", s.copyContentItem)
			r.With(s.requireActiveOwnCreator).Post("/creator-portal/content-items/{itemID}/preflight", s.preflightContentItem)
			r.With(s.requireActiveOwnCreator).Post("/creator-portal/content-items/{itemID}/submit", s.submitContentItem)
			r.With(s.requireActiveOwnCreator).Post("/creator-portal/content-items/{itemID}/publish", s.publishContentItem)
			r.With(s.requireActiveOwnCreator).Post("/creator-portal/content-items/{itemID}/schedule", s.scheduleContentItem)
			r.With(s.requireActiveOwnCreator).Post("/creator-portal/content-items/{itemID}/retry", s.retryContentTargets)
			r.With(s.requireActiveOwnCreator).Post("/creator-portal/content-items/{itemID}/cancel", s.cancelContentItem)
			r.With(s.requireActiveOwnCreator).Post("/creator-portal/media/uploads", s.createMediaUpload)
			r.With(s.requireActiveOwnCreator).Post("/creator-portal/media/uploads/{uploadID}/parts/{partNumber}", s.signMediaUploadPart)
			r.With(s.requireActiveOwnCreator).Post("/creator-portal/media/uploads/{uploadID}/complete", s.completeMediaUpload)
			r.With(s.requireActiveOwnCreator).Delete("/creator-portal/media/uploads/{uploadID}", s.abortMediaUpload)
			r.With(s.requireActiveOwnCreator).Get("/creator-portal/stats", s.getOwnStats)
			r.With(s.requireActiveOwnCreator).Get("/creator-portal/export", s.exportOwnCreator)
			r.With(s.requireOwner).Get("/users", s.listWorkspaceUsers)
			r.With(s.requireOwner).Get("/users/me/deletion-impact", s.selfDeletionImpact)
			r.With(s.requireOwner).Delete("/users/me", s.selfDelete)
			r.With(s.requireOwner).Post("/users", s.createWorkspaceUser)
			r.With(s.requireOwner).Patch("/users/{id}", s.updateWorkspaceUser)
			r.With(s.requireOwner).Put("/users/{id}/password", s.resetWorkspaceUserPassword)
			r.With(s.requireOwner).Delete("/users/{id}", s.deleteWorkspaceUser)
			r.With(s.requireOwner).Put("/users/{id}/company-assignments", s.replaceManagerCompanyAssignments)
			r.With(s.requireOwner).Get("/audit", s.listAuditLogs)
			// Kept for one expand-compatible release. New clients create an
			// OWNER or MANAGER directly through POST /users.
			r.With(s.requireOwner).Post("/users/invitations", s.createInvitation)
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
	if _, err := s.pool.Exec(ctx, `INSERT INTO organizations(name,slug) VALUES('Statzavod','statzavod') ON CONFLICT(slug) DO NOTHING`); err != nil {
		return err
	}
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
	err := s.pool.QueryRow(r.Context(), `SELECT u.id,u.email,u.password_hash,m.membership_role FROM users u JOIN organization_memberships m ON m.user_id=u.id WHERE u.email=$1 AND u.status='ACTIVE'`, strings.ToLower(strings.TrimSpace(in.Email))).Scan(&id, &email, &hash, &role)
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
	if _, err = s.pool.Exec(r.Context(), `UPDATE users SET last_login_at=now(),updated_at=now() WHERE id=$1`, id); err != nil {
		problem(w, http.StatusInternalServerError, "login failed", "could not update login state")
		return
	}
	http.SetCookie(w, sessionCookie(s.config.CookieName, token, s.config.Environment == "production"))
	writeJSON(w, 200, map[string]any{"id": id, "email": email, "role": role})
}

func sessionCookie(name, token string, secure bool) *http.Cookie {
	return &http.Cookie{Name: name, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure, MaxAge: 86400}
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.config.CookieName); err == nil {
		d := sha256.Sum256([]byte(c.Value))
		if _, err = s.pool.Exec(r.Context(), `UPDATE sessions SET revoked_at=now() WHERE token_hash=$1`, d[:]); err != nil {
			problem(w, http.StatusInternalServerError, "logout failed", "could not revoke session")
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: s.config.CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		digest, err := sessionDigest(r, s.config.CookieName)
		if err != nil {
			problem(w, 401, "authentication required", "sign in first")
			return
		}
		p, err := s.principalForSession(r.Context(), digest)
		if err != nil {
			if errors.Is(err, errStaleSessionContext) {
				if _, revokeErr := s.pool.Exec(r.Context(), `UPDATE sessions SET revoked_at=now() WHERE token_hash=$1 AND revoked_at IS NULL`, digest); revokeErr != nil {
					problem(w, http.StatusInternalServerError, "authentication failed", "could not invalidate stale session")
					return
				}
			}
			problem(w, 401, "authentication required", "session expired")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, p)))
	})
}

// require remains temporarily for expand compatibility with downstream code
// and tests while all route registration uses membership-based middleware.
func (s *Server) require(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := r.Context().Value(principalKey).(principal)
			legacyRole := map[string]string{roleOwner: "ADMIN", roleManager: "ANALYST", roleCreator: "VIEWER"}[p.Role]
			if legacyRole == "" {
				legacyRole = p.Role
			}
			for _, role := range roles {
				if legacyRole == role {
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
	scopeSQL, scopeArgs, ok := managementCompanyScope(p, "c", 2)
	if !ok {
		problem(w, http.StatusForbidden, "forbidden", "an assigned active company is required")
		return
	}
	scope := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("scope")))
	archiveFilter := "c.archived_at IS NULL"
	if scope == "archived" {
		archiveFilter = "c.archived_at IS NOT NULL"
	} else if scope != "" && scope != "active" {
		problem(w, http.StatusBadRequest, "invalid scope", "scope must be active or archived")
		return
	}
	args := append([]any{p.OrganizationID}, scopeArgs...)
	rows, err := s.pool.Query(r.Context(), `SELECT c.id,c.first_name,c.last_name,COALESCE(c.middle_name,''),c.display_name,c.status,c.created_at,c.telegram_username,c.company_id::text,x.name,c.work_status,c.work_comment,c.archived_at,ARRAY(
		SELECT connected.platform FROM (
			SELECT a.platform::text AS platform
			FROM creator_account_assignments assignment
			JOIN platform_accounts a ON a.id=assignment.platform_account_id AND a.organization_id=assignment.organization_id
			WHERE assignment.creator_id=c.id AND assignment.organization_id=c.organization_id AND assignment.valid_to IS NULL AND a.company_id=c.company_id AND a.status<>'DISCONNECTED'
			UNION
			SELECT 'VK' AS platform
			FROM creator_vk_assignments vk
			JOIN company_vk_accounts company_account ON company_account.id=vk.company_vk_account_id AND company_account.organization_id=vk.organization_id
			WHERE vk.creator_id=c.id AND vk.organization_id=c.organization_id AND company_account.company_id=c.company_id
		) connected ORDER BY connected.platform
	) FROM creators c JOIN companies x ON x.id=c.company_id AND x.organization_id=c.organization_id WHERE c.organization_id=$1 AND x.archived_at IS NULL AND `+archiveFilter+scopeSQL+` ORDER BY CASE c.status WHEN 'ACTIVE' THEN 0 WHEN 'ON_LEAVE' THEN 1 ELSE 2 END,c.display_name`, args...)
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
		var connectedPlatforms []string
		if err := rows.Scan(&id, &first, &last, &middle, &display, &status, &created, &telegram, &companyID, &companyName, &workStatus, &workComment, &archivedAt, &connectedPlatforms); err != nil {
			problem(w, 500, "scan failed", err.Error())
			return
		}
		items = append(items, map[string]any{"id": id, "firstName": first, "lastName": last, "middleName": middle, "displayName": display, "status": status, "createdAt": created, "archivedAt": archivedAt, "telegramUsername": telegram, "companyId": companyID, "companyName": companyName, "workStatus": workStatus, "workComment": workComment, "connectedPlatforms": connectedPlatforms})
	}
	if err := rows.Err(); err != nil {
		problem(w, http.StatusInternalServerError, "query failed", "could not finish reading creators")
		return
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
	p := r.Context().Value(principalKey).(principal)
	if p.ActiveCompanyID == nil {
		problem(w, http.StatusBadRequest, "invalid company", "an active company is required")
		return
	}
	companyID := *p.ActiveCompanyID
	if requestedCompanyID := strings.TrimSpace(in.CompanyID); requestedCompanyID != "" && requestedCompanyID != companyID {
		problem(w, http.StatusBadRequest, "invalid company", "companyId must match the active company")
		return
	}
	if !p.canAccessCompany(companyID) {
		problem(w, http.StatusNotFound, "not found", "company does not exist")
		return
	}
	if !p.hasPermission("CREATOR_CREATE") {
		problem(w, http.StatusForbidden, "forbidden", "company permission CREATOR_CREATE is required")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "creation failed", "could not start creator creation")
		return
	}
	defer tx.Rollback(r.Context())
	// archiveCompany takes FOR UPDATE before changing archived_at. Holding a
	// key-share lock through this insert therefore makes validation + creation
	// atomic with respect to archive without blocking unrelated companies.
	var lockedCompanyID string
	err = tx.QueryRow(r.Context(), `SELECT id::text FROM companies WHERE id=$1 AND organization_id=$2 AND archived_at IS NULL FOR KEY SHARE`, companyID, p.OrganizationID).Scan(&lockedCompanyID)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, http.StatusNotFound, "not found", "company does not exist")
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "creation failed", "could not validate active company")
		return
	}
	var id string
	err = tx.QueryRow(r.Context(), `INSERT INTO creators(organization_id,company_id,first_name,last_name,middle_name,display_name,internal_note,telegram_username,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, p.OrganizationID, lockedCompanyID, in.FirstName, in.LastName, in.MiddleName, in.DisplayName, in.InternalNote, normalizeTelegram(in.TelegramUsername), p.ID).Scan(&id)
	if err != nil {
		problem(w, 500, "creation failed", err.Error())
		return
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &companyID, "CREATE", "CREATOR", &id, http.StatusCreated, map[string]any{})); err != nil {
		problem(w, http.StatusInternalServerError, "creation failed", "could not write audit log")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "creation failed", "could not commit creator creation")
		return
	}
	markResponseAuditCommitted(w)
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
	rows, err := s.pool.Query(r.Context(), `SELECT contact.id,contact.kind,contact.value,COALESCE(contact.label,''),contact.is_primary FROM creator_contacts contact JOIN creators c ON c.id=contact.creator_id JOIN companies x ON x.id=c.company_id AND x.organization_id=c.organization_id WHERE contact.creator_id=$1 AND c.organization_id=$2 AND x.archived_at IS NULL ORDER BY contact.is_primary DESC,contact.created_at`, id, p.OrganizationID)
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
	if err := rows.Err(); err != nil {
		problem(w, http.StatusInternalServerError, "contacts failed", "could not finish reading contacts")
		return
	}
	canDelete := p.hasPermission("CREATOR_DELETE") || p.legacyRole() == "ADMIN"
	writeJSON(w, 200, map[string]any{"id": id, "firstName": first, "lastName": last, "middleName": middle, "displayName": display, "status": status, "internalNote": note, "archivedAt": archivedAt, "telegramUsername": telegram, "companyId": companyID, "companyName": companyName, "workStatus": workStatus, "workComment": workComment, "contacts": contacts, "canDelete": canDelete})
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
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "contact creation failed", "could not start contact creation")
		return
	}
	defer tx.Rollback(r.Context())
	var companyID string
	if err = tx.QueryRow(r.Context(), `SELECT c.company_id::text FROM creators c JOIN companies x ON x.id=c.company_id AND x.organization_id=c.organization_id WHERE c.id=$1 AND c.organization_id=$2 AND x.archived_at IS NULL FOR KEY SHARE OF c,x`, id, p.OrganizationID).Scan(&companyID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		problem(w, http.StatusInternalServerError, "contact creation failed", "could not validate creator")
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 404, "not found", "creator does not exist")
		return
	}
	if in.IsPrimary {
		if _, err = tx.Exec(r.Context(), `UPDATE creator_contacts SET is_primary=false WHERE creator_id=$1`, id); err != nil {
			problem(w, http.StatusInternalServerError, "contact creation failed", "could not update primary contact")
			return
		}
	}
	var cid string
	err = tx.QueryRow(r.Context(), `INSERT INTO creator_contacts(creator_id,kind,value,label,is_primary) VALUES($1,$2,$3,$4,$5) RETURNING id`, id, in.Kind, in.Value, in.Label, in.IsPrimary).Scan(&cid)
	if err != nil {
		problem(w, 500, "contact creation failed", err.Error())
		return
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &companyID, "CREATE_CONTACT", "CREATOR_CONTACT", &cid, http.StatusCreated, map[string]any{"creatorId": id, "kind": in.Kind, "isPrimary": in.IsPrimary})); err != nil {
		problem(w, http.StatusInternalServerError, "contact creation failed", "could not write audit record")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "contact creation failed", "could not commit contact creation")
		return
	}
	markResponseAuditCommitted(w)
	writeJSON(w, 201, map[string]string{"id": cid})
}
func (s *Server) listCreatorAccounts(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p := r.Context().Value(principalKey).(principal)
	rows, err := s.pool.Query(r.Context(), `SELECT a.id,a.platform,a.username,a.display_name,a.status,COALESCE(a.profile_url,'') FROM platform_accounts a JOIN creator_account_assignments assignment ON assignment.platform_account_id=a.id AND assignment.organization_id=a.organization_id JOIN creators c ON c.id=assignment.creator_id AND c.organization_id=assignment.organization_id WHERE assignment.creator_id=$1 AND a.organization_id=$2 AND assignment.valid_to IS NULL AND a.company_id=c.company_id AND a.status<>'DISCONNECTED' ORDER BY a.platform,a.username`, id, p.OrganizationID)
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
	if err := rows.Err(); err != nil {
		problem(w, http.StatusInternalServerError, "accounts failed", "could not finish reading creator accounts")
		return
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
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, 500, "assignment failed", err.Error())
		return
	}
	defer tx.Rollback(r.Context())
	var companyID string
	if err = tx.QueryRow(r.Context(), `
		SELECT c.company_id FROM creators c JOIN companies x ON x.id=c.company_id
		WHERE c.id=$1 AND c.organization_id=$2 AND c.archived_at IS NULL AND x.archived_at IS NULL
		FOR KEY SHARE OF c,x`, creatorID, p.OrganizationID).Scan(&companyID); err != nil || !p.canAccessCompany(companyID) {
		problem(w, http.StatusNotFound, "not found", "creator does not exist")
		return
	}
	if !p.hasPermission("SOCIAL_CONNECT") {
		problem(w, http.StatusForbidden, "forbidden", "company permission SOCIAL_CONNECT is required")
		return
	}
	var accountID string
	err = tx.QueryRow(r.Context(), `
		INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name,profile_url,status)
		VALUES($1,$2,$3,$4,$5,$6,$7,'REAUTH_REQUIRED')
		ON CONFLICT(organization_id,platform,external_id) DO UPDATE
		SET username=excluded.username,display_name=excluded.display_name,profile_url=excluded.profile_url
		WHERE platform_accounts.company_id=excluded.company_id
		RETURNING id`, p.OrganizationID, companyID, in.Platform, in.ExternalID, in.Username, in.DisplayName, in.ProfileURL).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		var existingCompanyID string
		lookupErr := tx.QueryRow(r.Context(), `SELECT company_id FROM platform_accounts WHERE organization_id=$1 AND platform=$2 AND external_id=$3`, p.OrganizationID, in.Platform, in.ExternalID).Scan(&existingCompanyID)
		if lookupErr == nil && p.canAccessCompany(existingCompanyID) {
			problem(w, http.StatusConflict, "account already belongs to another company", "platform account cannot be moved between companies")
		} else {
			problem(w, http.StatusNotFound, "not found", "platform account is not available")
		}
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "account creation failed", "could not save account")
		return
	}
	if err = cancelContentPublishesForAccountReassignmentTx(r.Context(), tx, p.OrganizationID, accountID, creatorID); err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE creator_account_assignments SET valid_to=now() WHERE platform_account_id=$1 AND valid_to IS NULL`, accountID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO creator_account_assignments(creator_id,platform_account_id) VALUES($1,$2)`, creatorID, accountID)
	}
	if err == nil {
		err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &companyID, "CREATE_PLATFORM_ACCOUNT", "PLATFORM_ACCOUNT", &accountID, http.StatusCreated, map[string]any{"creatorId": creatorID, "platform": in.Platform}))
	}
	if err != nil {
		problem(w, 500, "assignment failed", "could not assign account")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, 500, "assignment failed", "could not commit account assignment")
		return
	}
	markResponseAuditCommitted(w)
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
	var companyID string
	if err = tx.QueryRow(r.Context(), `SELECT company_id::text FROM creators WHERE id=$1 AND organization_id=$2 AND archived_at IS NULL FOR UPDATE`, id, p.OrganizationID).Scan(&companyID); errors.Is(err, pgx.ErrNoRows) {
		problem(w, http.StatusNotFound, "not found", "active creator does not exist")
		return
	} else if err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not load creator")
		return
	}
	tag, err := tx.Exec(r.Context(), `UPDATE creators SET archived_at=now(),updated_at=now() WHERE id=$1 AND organization_id=$2 AND archived_at IS NULL`, id, p.OrganizationID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not archive creator")
		return
	}
	if tag.RowsAffected() == 0 {
		problem(w, http.StatusNotFound, "not found", "active creator does not exist")
		return
	}
	now := time.Now().UTC()
	if s.now != nil {
		now = s.now().UTC()
	}
	if _, err = tx.Exec(r.Context(), `UPDATE content_publish_jobs job SET status='CANCELLED',locked_by=NULL,locked_at=NULL,lease_expires_at=NULL,updated_at=$3 FROM content_publish_targets target WHERE job.target_id=target.id AND job.organization_id=$2 AND target.organization_id=$2 AND target.creator_id=$1 AND job.status IN ('READY','RETRY_SCHEDULED')`, id, p.OrganizationID, now); err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not cancel creator publications")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE content_publish_targets target SET cancellation_requested_at=COALESCE(cancellation_requested_at,$3),status=CASE WHEN EXISTS (SELECT 1 FROM content_publish_jobs job WHERE job.target_id=target.id AND job.organization_id=target.organization_id AND job.status='RUNNING') THEN target.status ELSE 'CANCELLED' END,updated_at=$3 WHERE target.creator_id=$1 AND target.organization_id=$2 AND target.status NOT IN ('SUCCEEDED','CANCELLED')`, id, p.OrganizationID, now); err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not fence creator publications")
		return
	}
	creatorIDForCleanup := id
	if err = markArchivedMediaCleanup(r.Context(), tx, p.OrganizationID, nil, &creatorIDForCleanup, now); err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not schedule creator media cleanup")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE creator_account_assignments SET valid_to=GREATEST(valid_from+interval '1 microsecond',$3) WHERE creator_id=$1 AND organization_id=$2 AND valid_to IS NULL`, id, p.OrganizationID, now); err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not deactivate creator assignments")
		return
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &companyID, "ARCHIVE", "CREATOR", &id, http.StatusNoContent, map[string]any{})); err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not save audit record")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "archive failed", "could not commit archive")
		return
	}
	markResponseAuditCommitted(w)
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
	var companyID string
	if err = tx.QueryRow(r.Context(), `SELECT company_id::text FROM creators WHERE id=$1 AND organization_id=$2 AND archived_at IS NOT NULL`, id, p.OrganizationID).Scan(&companyID); errors.Is(err, pgx.ErrNoRows) {
		problem(w, http.StatusNotFound, "not found", "archived creator does not exist")
		return
	} else if err != nil {
		problem(w, http.StatusInternalServerError, "restore failed", "could not load creator")
		return
	}
	tag, err := tx.Exec(r.Context(), `UPDATE creators SET status=CASE WHEN status='ARCHIVED' THEN 'ACTIVE'::creator_status ELSE status END,archived_at=NULL,updated_at=now() WHERE id=$1 AND organization_id=$2 AND archived_at IS NOT NULL`, id, p.OrganizationID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "restore failed", "could not restore creator")
		return
	}
	if tag.RowsAffected() == 0 {
		problem(w, http.StatusNotFound, "not found", "archived creator does not exist")
		return
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &companyID, "RESTORE", "CREATOR", &id, http.StatusNoContent, map[string]any{})); err != nil {
		problem(w, http.StatusInternalServerError, "restore failed", "could not save audit record")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "restore failed", "could not commit restore")
		return
	}
	markResponseAuditCommitted(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteCreator(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p := r.Context().Value(principalKey).(principal)
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "deletion failed", "could not start deletion")
		return
	}
	defer tx.Rollback(r.Context())

	// Mutable creator-owned rows are explicitly purged below so the retained
	// tombstone can preserve publication and audit evidence. Platform accounts
	// are intentionally not creator-owned and survive; assignments do not.
	// The login user is deleted only after its last profile is removed.
	var loginUserID *string
	var companyID string
	err = tx.QueryRow(r.Context(), `SELECT login_user_id::text,company_id::text FROM creators WHERE id=$1 AND organization_id=$2 AND archived_at IS NULL FOR UPDATE`, id, p.OrganizationID).Scan(&loginUserID, &companyID)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, http.StatusNotFound, "not found", "creator does not exist")
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "deletion failed", "could not load creator")
		return
	}
	if loginUserID != nil {
		// Profiles sharing one login can be deleted in different transactions.
		// Serialize their last-profile check by login identity so exactly one
		// transaction removes the now-unused account.
		if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,1))`, *loginUserID); err != nil {
			problem(w, http.StatusInternalServerError, "deletion failed", "could not lock creator account lifecycle")
			return
		}
	}
	if err = tombstoneCreatorContentDomain(r.Context(), tx, id, p.OrganizationID); err != nil {
		problem(w, http.StatusInternalServerError, "deletion failed", "could not purge creator content")
		return
	}
	for _, statement := range []string{
		`DELETE FROM creator_contacts WHERE creator_id=$1 AND EXISTS(SELECT 1 FROM creators WHERE id=$1 AND organization_id=$2)`,
		`DELETE FROM creator_credentials WHERE creator_id=$1 AND EXISTS(SELECT 1 FROM creators WHERE id=$1 AND organization_id=$2)`,
		`DELETE FROM creator_history_events WHERE creator_id=$1 AND organization_id=$2`,
		`DELETE FROM content_groups WHERE creator_id=$1 AND EXISTS(SELECT 1 FROM creators WHERE id=$1 AND organization_id=$2)`,
		`DELETE FROM content_match_suggestions WHERE creator_id=$1 AND EXISTS(SELECT 1 FROM creators WHERE id=$1 AND organization_id=$2)`,
		`DELETE FROM oauth_states WHERE creator_id=$1 AND organization_id=$2`,
		`DELETE FROM oauth_account_selections WHERE creator_id=$1 AND organization_id=$2`,
		`DELETE FROM creator_account_assignments WHERE creator_id=$1 AND organization_id=$2`,
	} {
		if _, err = tx.Exec(r.Context(), statement, id, p.OrganizationID); err != nil {
			problem(w, http.StatusInternalServerError, "deletion failed", "could not remove creator private data")
			return
		}
	}
	if _, err = tx.Exec(r.Context(), `UPDATE creators SET first_name='Deleted',last_name='Creator',middle_name=NULL,display_name='Deleted creator',internal_note='',telegram_username='',work_status='OK',work_comment='',login_user_id=NULL,status='ARCHIVED',archived_at=now(),updated_at=now() WHERE id=$1 AND organization_id=$2`, id, p.OrganizationID); err != nil {
		problem(w, http.StatusInternalServerError, "deletion failed", "could not anonymize creator")
		return
	}
	if loginUserID != nil {
		var profilesRemain bool
		if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM creators WHERE organization_id=$1 AND login_user_id=$2)`, p.OrganizationID, *loginUserID).Scan(&profilesRemain); err != nil {
			problem(w, http.StatusInternalServerError, "deletion failed", "could not validate creator account lifecycle")
			return
		}
		if !profilesRemain {
			if _, err = tx.Exec(r.Context(), `UPDATE sessions SET revoked_at=COALESCE(revoked_at,now()),active_company_id=NULL,active_creator_id=NULL WHERE user_id=$1`, *loginUserID); err == nil {
				_, err = tx.Exec(r.Context(), `DELETE FROM users u USING organization_memberships m WHERE u.id=$2 AND m.user_id=u.id AND m.organization_id=$1 AND m.membership_role='CREATOR'`, p.OrganizationID, *loginUserID)
			}
			if err != nil {
				problem(w, http.StatusInternalServerError, "deletion failed", "could not remove unused creator account")
				return
			}
		}
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &companyID, "DELETE", "CREATOR", &id, http.StatusNoContent, map[string]any{"anonymized": true, "mediaCleanupPending": true})); err != nil {
		problem(w, http.StatusInternalServerError, "deletion failed", "could not write audit log")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "deletion failed", "could not commit deletion")
		return
	}
	markResponseAuditCommitted(w)
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) listContentGroups(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	scopeSQL, scopeArgs, ok := managementCompanyScope(p, "c", 2)
	if !ok {
		problem(w, http.StatusForbidden, "forbidden", "an assigned active company is required")
		return
	}
	args := append([]any{p.OrganizationID}, scopeArgs...)
	rows, err := s.pool.Query(r.Context(), `SELECT g.id,g.name,g.status,c.display_name,count(m.publication_id) FROM content_groups g JOIN creators c ON c.id=g.creator_id AND c.organization_id=$1 JOIN companies x ON x.id=c.company_id AND x.organization_id=c.organization_id LEFT JOIN content_group_members m ON m.content_group_id=g.id WHERE x.archived_at IS NULL`+scopeSQL+` GROUP BY g.id,c.display_name ORDER BY g.created_at DESC`, args...)
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
	if err := rows.Err(); err != nil {
		problem(w, http.StatusInternalServerError, "groups failed", "could not finish reading content groups")
		return
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
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "group creation failed", "could not start group creation")
		return
	}
	defer tx.Rollback(r.Context())
	var companyID string
	if err = tx.QueryRow(r.Context(), `SELECT c.company_id::text FROM creators c JOIN companies x ON x.id=c.company_id AND x.organization_id=c.organization_id WHERE c.id=$1 AND c.organization_id=$2 AND c.archived_at IS NULL AND x.archived_at IS NULL FOR KEY SHARE OF c,x`, in.CreatorID, p.OrganizationID).Scan(&companyID); errors.Is(err, pgx.ErrNoRows) {
		problem(w, http.StatusNotFound, "creator not found", "creator does not exist")
		return
	} else if err != nil {
		problem(w, http.StatusInternalServerError, "group creation failed", "could not authorize creator")
		return
	}
	if status := authorizeCompanyAction(p, companyID, "CREATOR_EDIT"); status != 0 {
		if status == http.StatusForbidden {
			problem(w, http.StatusForbidden, "forbidden", "company permission CREATOR_EDIT is required")
		} else {
			problem(w, http.StatusNotFound, "creator not found", "creator does not exist")
		}
		return
	}
	var id string
	err = tx.QueryRow(r.Context(), `INSERT INTO content_groups(creator_id,name,created_by) SELECT id,$2,$3 FROM creators WHERE id=$1 AND organization_id=$4 RETURNING id`, in.CreatorID, in.Name, p.ID, p.OrganizationID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, http.StatusNotFound, "creator not found", "creator does not exist in this organization")
		return
	}
	if err != nil {
		problem(w, 500, "group creation failed", err.Error())
		return
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &companyID, "CREATE_CONTENT_GROUP", "CONTENT_GROUP", &id, http.StatusCreated, map[string]any{"creatorId": in.CreatorID})); err != nil {
		problem(w, http.StatusInternalServerError, "group creation failed", "could not write audit record")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "group creation failed", "could not commit group creation")
		return
	}
	markResponseAuditCommitted(w)
	writeJSON(w, 201, map[string]string{"id": id})
}
func (s *Server) listPublications(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	scopeSQL, scopeArgs, ok := managementCompanyScope(p, "c", 2)
	if !ok {
		problem(w, http.StatusForbidden, "forbidden", "an assigned active company is required")
		return
	}
	args := append([]any{p.OrganizationID}, scopeArgs...)
	rows, err := s.pool.Query(r.Context(), `SELECT p.id,p.title,p.platform,p.published_at,p.thumbnail_url,p.external_id,COALESCE(p.permalink,''),CASE WHEN p.content_publish_target_id IS NULL AND p.status='ACTIVE' THEN 'SUCCEEDED' ELSE COALESCE(target.status,'') END,c.display_name,x.id::text,x.name,COALESCE(s.views,0),COALESCE(s.likes,0),COALESCE(s.comments,0),COALESCE(s.shares,0) FROM publications p LEFT JOIN content_publish_targets target ON target.id=p.content_publish_target_id AND target.organization_id=p.organization_id JOIN creators c ON c.id=p.creator_id AND c.organization_id=p.organization_id JOIN companies x ON x.id=c.company_id AND x.organization_id=c.organization_id LEFT JOIN LATERAL (SELECT views,likes,comments,shares FROM publication_metric_snapshots s WHERE s.publication_id=p.id ORDER BY observed_at DESC LIMIT 1) s ON true WHERE p.organization_id=$1 AND x.archived_at IS NULL`+scopeSQL+` ORDER BY p.published_at DESC LIMIT 100`, args...)
	if err != nil {
		problem(w, 500, "query failed", err.Error())
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, platform, externalID, rawPermalink, targetStatus, creator, companyID, companyName string
		var title, thumbnailURL *string
		var published time.Time
		var views, likes, comments, shares int64
		if err := rows.Scan(&id, &title, &platform, &published, &thumbnailURL, &externalID, &rawPermalink, &targetStatus, &creator, &companyID, &companyName, &views, &likes, &comments, &shares); err != nil {
			problem(w, http.StatusInternalServerError, "scan failed", err.Error())
			return
		}
		_, permalink := sanitizeSucceededPublication(platform, targetStatus, externalID, rawPermalink)
		var publicPermalink any
		if permalink != "" {
			publicPermalink = permalink
		}
		items = append(items, map[string]any{"id": id, "title": title, "platform": platform, "publishedAt": published, "thumbnailUrl": thumbnailURL, "permalink": publicPermalink, "creatorName": creator, "companyId": companyID, "companyName": companyName, "views": views, "likes": likes, "comments": comments, "shares": shares})
	}
	if err := rows.Err(); err != nil {
		problem(w, http.StatusInternalServerError, "query failed", "could not finish reading publications")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

// sanitizeSucceededPublication is the single persistence/read boundary for
// cross-posting identities. It never returns provider-controlled URLs; URLs are
// canonicalized against the strict platform allowlist or derived from an ID
// whose platform grammar is sufficient to do so safely.
func sanitizeSucceededPublication(platform, status, externalID, rawURL string) (string, string) {
	safeID := publicExternalID(platform, status, externalID)
	if safeID == "" {
		return "", ""
	}
	if permalink := canonicalPublicationURL(platform, safeID, rawURL); permalink != "" {
		return safeID, permalink
	}
	switch strings.ToUpper(platform) {
	case "YOUTUBE":
		return safeID, "https://www.youtube.com/shorts/" + safeID
	case "VK":
		return safeID, "https://vk.ru/video" + safeID
	default:
		return safeID, ""
	}
}
func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	scopeSQL, scopeArgs, ok := managementCompanyScope(p, "c", 2)
	if !ok {
		problem(w, http.StatusForbidden, "forbidden", "an assigned active company is required")
		return
	}
	args := append([]any{p.OrganizationID}, scopeArgs...)
	var creators, publications int64
	var views, likes int64
	err := s.pool.QueryRow(r.Context(), `
		WITH scoped_creators AS (
			SELECT c.id,c.status,c.archived_at FROM creators c
			JOIN companies company ON company.id=c.company_id AND company.organization_id=c.organization_id
			WHERE c.organization_id=$1 AND company.archived_at IS NULL`+scopeSQL+`
		), scoped_publications AS (
			SELECT publication.id FROM publications publication
			JOIN scoped_creators creator ON creator.id=publication.creator_id
			WHERE publication.organization_id=$1
		), latest AS (
			SELECT DISTINCT ON (metric.publication_id) metric.publication_id,COALESCE(metric.views,0) views,COALESCE(metric.likes,0) likes
			FROM publication_metric_snapshots metric JOIN scoped_publications publication ON publication.id=metric.publication_id
			ORDER BY metric.publication_id,metric.observed_at DESC
		)
		SELECT
			(SELECT count(*) FROM scoped_creators WHERE status='ACTIVE' AND archived_at IS NULL),
			(SELECT count(*) FROM scoped_publications),
			COALESCE((SELECT sum(views) FROM latest),0),
			COALESCE((SELECT sum(likes) FROM latest),0)`, args...).Scan(&creators, &publications, &views, &likes)
	if err != nil {
		problem(w, 500, "summary failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"kpis": []map[string]any{{"key": "views", "label": localized(r, "Просмотры", "Views"), "value": views}, {"key": "likes", "label": localized(r, "Реакции", "Reactions"), "value": likes}, {"key": "publications", "label": localized(r, "Публикации", "Publications"), "value": publications}, {"key": "creators", "label": localized(r, "Креаторы", "Creators"), "value": creators}}, "freshness": map[string]string{"status": "partial", "message": localized(r, "Подключите платформенные аккаунты для сбора данных.", "Connect platform accounts to start collecting data.")}})
}
func (s *Server) timeseries(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	scopeSQL, scopeArgs, ok := managementCompanyScope(p, "c", 2)
	if !ok {
		problem(w, http.StatusForbidden, "forbidden", "an assigned active company is required")
		return
	}
	args := append([]any{p.OrganizationID}, scopeArgs...)
	rows, err := s.pool.Query(r.Context(), `SELECT metric.observed_at::date,COALESCE(sum(metric.views),0) FROM publication_metric_snapshots metric JOIN publications publication ON publication.id=metric.publication_id JOIN creators c ON c.id=publication.creator_id AND c.organization_id=publication.organization_id JOIN companies company ON company.id=c.company_id AND company.organization_id=c.organization_id WHERE publication.organization_id=$1 AND company.archived_at IS NULL`+scopeSQL+` GROUP BY metric.observed_at::date ORDER BY metric.observed_at::date`, args...)
	if err != nil {
		problem(w, 500, "timeseries failed", err.Error())
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var d time.Time
		var v int64
		if err := rows.Scan(&d, &v); err != nil {
			problem(w, http.StatusInternalServerError, "timeseries failed", "could not read timeseries")
			return
		}
		items = append(items, map[string]any{"date": d.Format("2006-01-02"), "views": v})
	}
	if err := rows.Err(); err != nil {
		problem(w, http.StatusInternalServerError, "timeseries failed", "could not finish reading timeseries")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) creatorAnalytics(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p := r.Context().Value(principalKey).(principal)
	from, to := r.URL.Query().Get("activityFrom"), r.URL.Query().Get("activityTo")
	var name string
	if err := s.pool.QueryRow(r.Context(), `SELECT c.display_name FROM creators c JOIN companies x ON x.id=c.company_id AND x.organization_id=c.organization_id WHERE c.id=$1 AND c.organization_id=$2 AND x.archived_at IS NULL`, id, p.OrganizationID).Scan(&name); err == pgx.ErrNoRows {
		problem(w, 404, "not found", "creator does not exist")
		return
	} else if err != nil {
		problem(w, 500, "query failed", err.Error())
		return
	}
	query := `SELECT count(DISTINCT publication.id), COALESCE(sum(metric.views),0), COALESCE(sum(metric.likes),0), COALESCE(sum(metric.comments),0), COALESCE(sum(metric.shares),0) FROM publications publication JOIN creators creator ON creator.id=publication.creator_id AND creator.organization_id=publication.organization_id JOIN companies company ON company.id=creator.company_id AND company.organization_id=creator.organization_id LEFT JOIN LATERAL (SELECT views,likes,comments,shares FROM publication_metric_snapshots WHERE publication_id=publication.id ORDER BY observed_at DESC LIMIT 1) metric ON true WHERE publication.creator_id=$1 AND publication.organization_id=$2 AND company.archived_at IS NULL`
	args := []any{id, p.OrganizationID}
	if from != "" {
		query += " AND publication.published_at >= $" + fmt.Sprint(len(args)+1)
		args = append(args, from)
	}
	if to != "" {
		query += " AND publication.published_at < ($" + fmt.Sprint(len(args)+1) + "::date + interval '1 day')"
		args = append(args, to)
	}
	var publications, views, likes, comments, shares int64
	if err := s.pool.QueryRow(r.Context(), query, args...).Scan(&publications, &views, &likes, &comments, &shares); err != nil {
		problem(w, 500, "analytics failed", err.Error())
		return
	}
	publicationQuery := `SELECT publication.id,COALESCE(publication.title,''),publication.platform,publication.published_at,COALESCE(metric.views,0),COALESCE(metric.likes,0) FROM publications publication JOIN creators creator ON creator.id=publication.creator_id AND creator.organization_id=publication.organization_id JOIN companies company ON company.id=creator.company_id AND company.organization_id=creator.organization_id LEFT JOIN LATERAL (SELECT views,likes FROM publication_metric_snapshots WHERE publication_id=publication.id ORDER BY observed_at DESC LIMIT 1) metric ON true WHERE publication.creator_id=$1 AND publication.organization_id=$2 AND company.archived_at IS NULL`
	publicationArgs := []any{id, p.OrganizationID}
	if from != "" {
		publicationQuery += " AND publication.published_at >= $" + fmt.Sprint(len(publicationArgs)+1)
		publicationArgs = append(publicationArgs, from)
	}
	if to != "" {
		publicationQuery += " AND publication.published_at < ($" + fmt.Sprint(len(publicationArgs)+1) + "::date + interval '1 day')"
		publicationArgs = append(publicationArgs, to)
	}
	publicationQuery += " ORDER BY publication.published_at DESC LIMIT 100"
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
	if err := rows.Err(); err != nil {
		problem(w, http.StatusInternalServerError, "analytics failed", "could not finish reading publications")
		return
	}
	writeJSON(w, 200, map[string]any{"creatorId": id, "creatorName": name, "period": map[string]string{"from": from, "to": to}, "kpis": []map[string]any{{"key": "views", "label": localized(r, "Просмотры", "Views"), "value": views}, {"key": "likes", "label": localized(r, "Реакции", "Reactions"), "value": likes}, {"key": "comments", "label": localized(r, "Комментарии", "Comments"), "value": comments}, {"key": "shares", "label": localized(r, "Репосты", "Shares"), "value": shares}, {"key": "publications", "label": localized(r, "Публикации", "Publications"), "value": publications}}, "publications": items})
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
	if status := s.validateCreatorExportScope(r.Context(), p, ids); status != 0 {
		if status == http.StatusNotFound {
			problem(w, http.StatusNotFound, "not found", "one or more creators do not exist in the active context")
		} else {
			problem(w, http.StatusInternalServerError, "export failed", "could not authorize creators")
		}
		return
	}
	s.writeCreatorExport(w, r, p, ids, "creator-report.xlsx")
}
func (s *Server) syncHealth(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	scopeSQL, scopeArgs, ok := managementCompanyScope(p, "target", 2)
	if !ok {
		problem(w, http.StatusForbidden, "forbidden", "an assigned active company is required")
		return
	}
	args := append([]any{p.OrganizationID}, scopeArgs...)
	var due int64
	if err := s.pool.QueryRow(r.Context(), `SELECT count(*) FROM sync_targets target WHERE target.organization_id=$1 AND target.next_sync_at<=now() AND target.status='ACTIVE'`+scopeSQL, args...).Scan(&due); err != nil {
		problem(w, http.StatusInternalServerError, "sync health failed", "could not load synchronization health")
		return
	}
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
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept-Language, If-Match, Idempotency-Key")
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
