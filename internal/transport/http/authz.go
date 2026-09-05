package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

const (
	roleOwner   = "OWNER"
	roleManager = "MANAGER"
	roleCreator = "CREATOR"
)

var managerPermissions = []string{
	"STATS_VIEW", "STATS_EXPORT", "CREATOR_CREATE", "CREATOR_EDIT",
	"CREATOR_ARCHIVE", "CREATOR_DELETE", "CREATOR_ACCOUNT_MANAGE",
	"SOCIAL_CONNECT", "SYNC_MANAGE", "CREDENTIAL_EDIT", "SECRET_REVEAL",
	"CONTENT_VIEW", "CONTENT_CREATE", "CONTENT_EDIT", "CONTENT_APPROVE",
	"CONTENT_PUBLISH", "CONTENT_DELETE",
}

type companyAccess struct {
	ID          string
	Name        string
	Permissions map[string]struct{}
}

type creatorProfileAccess struct {
	ID          string `json:"id"`
	CompanyID   string `json:"companyId"`
	CompanyName string `json:"companyName"`
	DisplayName string `json:"displayName"`
}

type principal struct {
	ID, Email, OrganizationID, SessionID string
	Role                                 string
	ActiveCompanyID, ActiveCreatorID     *string
	Companies                            map[string]companyAccess
	CreatorProfiles                      map[string]creatorProfileAccess
}

var errStaleSessionContext = errors.New("session context is no longer available")

func (s *Server) principalForSession(ctx context.Context, digest []byte) (principal, error) {
	p := principal{Companies: map[string]companyAccess{}, CreatorProfiles: map[string]creatorProfileAccess{}}
	err := s.pool.QueryRow(ctx, `
		SELECT u.id,u.email,m.organization_id,m.membership_role,s.id,
		       s.active_company_id::text,s.active_creator_id::text
		FROM sessions s
		JOIN users u ON u.id=s.user_id
		JOIN organization_memberships m ON m.user_id=u.id
		JOIN organizations o ON o.id=m.organization_id AND o.lifecycle_state='ACTIVE'
		WHERE s.token_hash=$1 AND s.revoked_at IS NULL AND s.expires_at>now()
		  AND u.status='ACTIVE'`, digest).Scan(
		&p.ID, &p.Email, &p.OrganizationID, &p.Role, &p.SessionID,
		&p.ActiveCompanyID, &p.ActiveCreatorID,
	)
	if err != nil {
		return principal{}, err
	}

	var rows pgx.Rows
	switch p.Role {
	case roleOwner:
		rows, err = s.pool.Query(ctx, `
			SELECT id,name FROM companies
			WHERE organization_id=$1 AND archived_at IS NULL ORDER BY name,id`, p.OrganizationID)
	case roleManager:
		rows, err = s.pool.Query(ctx, `
			SELECT c.id,c.name,COALESCE(array_agg(mp.permission::text ORDER BY mp.permission)
				FILTER (WHERE mp.permission IS NOT NULL),'{}'::text[])
			FROM manager_company_assignments a
			JOIN companies c ON c.id=a.company_id AND c.organization_id=a.organization_id
			LEFT JOIN manager_company_permissions mp ON mp.assignment_id=a.id
			WHERE a.organization_id=$1 AND a.manager_user_id=$2 AND c.archived_at IS NULL
			GROUP BY c.id,c.name ORDER BY c.name,c.id`, p.OrganizationID, p.ID)
	case roleCreator:
		rows, err = s.pool.Query(ctx, `
			SELECT c.id,c.company_id,x.name,c.display_name
			FROM creators c JOIN companies x ON x.id=c.company_id AND x.organization_id=c.organization_id
			WHERE c.organization_id=$1 AND c.login_user_id=$2
			  AND c.archived_at IS NULL AND x.archived_at IS NULL
			ORDER BY x.name,c.display_name,c.id`, p.OrganizationID, p.ID)
	default:
		return principal{}, errStaleSessionContext
	}
	if err != nil {
		return principal{}, err
	}
	defer rows.Close()
	for rows.Next() {
		switch p.Role {
		case roleOwner:
			var id, name string
			if err = rows.Scan(&id, &name); err == nil {
				p.Companies[id] = companyAccess{ID: id, Name: name, Permissions: map[string]struct{}{}}
			}
		case roleManager:
			var id, name string
			var permissions []string
			if err = rows.Scan(&id, &name, &permissions); err == nil {
				grants := make(map[string]struct{}, len(permissions))
				for _, permission := range permissions {
					grants[permission] = struct{}{}
				}
				p.Companies[id] = companyAccess{ID: id, Name: name, Permissions: grants}
			}
		case roleCreator:
			var profile creatorProfileAccess
			if err = rows.Scan(&profile.ID, &profile.CompanyID, &profile.CompanyName, &profile.DisplayName); err == nil {
				p.CreatorProfiles[profile.ID] = profile
				p.Companies[profile.CompanyID] = companyAccess{ID: profile.CompanyID, Name: profile.CompanyName, Permissions: map[string]struct{}{}}
			}
		}
		if err != nil {
			return principal{}, err
		}
	}
	if err = rows.Err(); err != nil {
		return principal{}, err
	}
	if !p.contextValid() {
		return principal{}, errStaleSessionContext
	}
	return p, nil
}

func (p principal) contextValid() bool {
	switch p.Role {
	case roleOwner:
		if p.ActiveCreatorID != nil {
			return false
		}
		return p.ActiveCompanyID == nil || p.hasCompany(*p.ActiveCompanyID)
	case roleManager:
		if p.ActiveCreatorID != nil {
			return false
		}
		return p.ActiveCompanyID == nil || p.hasCompany(*p.ActiveCompanyID)
	case roleCreator:
		if p.ActiveCompanyID == nil && p.ActiveCreatorID == nil {
			return true
		}
		if p.ActiveCompanyID == nil || p.ActiveCreatorID == nil {
			return false
		}
		profile, ok := p.CreatorProfiles[*p.ActiveCreatorID]
		return ok && profile.CompanyID == *p.ActiveCompanyID
	default:
		return false
	}
}

func (p principal) hasCompany(companyID string) bool {
	_, ok := p.Companies[companyID]
	return ok
}

func (p principal) legacyRole() string {
	switch p.Role {
	case roleOwner:
		return "ADMIN"
	case roleManager:
		return "ANALYST"
	case roleCreator:
		return "VIEWER"
	default:
		return p.Role
	}
}

func (p principal) hasPermission(permission string) bool {
	if p.Role == roleOwner {
		return true
	}
	if p.Role != roleManager || p.ActiveCompanyID == nil {
		return false
	}
	company, ok := p.Companies[*p.ActiveCompanyID]
	if !ok {
		return false
	}
	_, ok = company.Permissions[permission]
	return ok
}

func (p principal) capabilities() []string {
	capabilities := []string{}
	switch p.Role {
	case roleOwner:
		capabilities = append(capabilities, "WORKSPACE_MANAGE", "COMPANY_VIEW")
		capabilities = append(capabilities, managerPermissions...)
	case roleManager:
		if p.ActiveCompanyID != nil {
			capabilities = append(capabilities, "COMPANY_VIEW")
			if company, ok := p.Companies[*p.ActiveCompanyID]; ok {
				for permission := range company.Permissions {
					capabilities = append(capabilities, permission)
				}
			}
		}
	case roleCreator:
		if p.ActiveCreatorID != nil {
			capabilities = append(capabilities, "OWN_PROFILE_VIEW", "OWN_STATS_VIEW", "OWN_STATS_EXPORT", "OWN_SECRET_REVEAL", "CONTENT_VIEW", "CONTENT_CREATE", "CONTENT_EDIT", "CONTENT_PUBLISH")
		}
	}
	sort.Strings(capabilities)
	return capabilities
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	companies := make([]map[string]any, 0, len(p.Companies))
	for _, company := range p.Companies {
		permissions := make([]string, 0, len(company.Permissions))
		for permission := range company.Permissions {
			permissions = append(permissions, permission)
		}
		sort.Strings(permissions)
		companies = append(companies, map[string]any{"id": company.ID, "name": company.Name, "permissions": permissions})
	}
	sort.Slice(companies, func(i, j int) bool { return companies[i]["name"].(string) < companies[j]["name"].(string) })
	profiles := make([]creatorProfileAccess, 0, len(p.CreatorProfiles))
	for _, profile := range p.CreatorProfiles {
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].DisplayName < profiles[j].DisplayName })
	writeJSON(w, http.StatusOK, map[string]any{
		"id": p.ID, "email": p.Email, "role": p.Role, "capabilities": p.capabilities(),
		"companies": companies, "creatorProfiles": profiles,
		"activeContext": map[string]any{"companyId": p.ActiveCompanyID, "creatorId": p.ActiveCreatorID, "allCompanies": p.Role == roleOwner && p.ActiveCompanyID == nil},
	})
}

func (s *Server) setAuthContext(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	var in struct {
		CompanyID *string `json:"companyId"`
		CreatorID *string `json:"creatorId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		problem(w, http.StatusBadRequest, "invalid context", "expected a JSON body")
		return
	}
	var companyID, creatorID *string
	switch p.Role {
	case roleOwner:
		if in.CreatorID != nil || (in.CompanyID != nil && !p.hasCompany(*in.CompanyID)) {
			problem(w, http.StatusNotFound, "context not found", "company is not available")
			return
		}
		companyID = in.CompanyID
	case roleManager:
		if in.CompanyID == nil || in.CreatorID != nil || !p.hasCompany(*in.CompanyID) {
			problem(w, http.StatusNotFound, "context not found", "assigned company is required")
			return
		}
		companyID = in.CompanyID
	case roleCreator:
		if in.CreatorID == nil {
			problem(w, http.StatusNotFound, "context not found", "own creator profile is required")
			return
		}
		profile, ok := p.CreatorProfiles[*in.CreatorID]
		if !ok || (in.CompanyID != nil && *in.CompanyID != profile.CompanyID) {
			problem(w, http.StatusNotFound, "context not found", "creator profile is not available")
			return
		}
		companyID, creatorID = &profile.CompanyID, in.CreatorID
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "context update failed", "could not start context update")
		return
	}
	defer tx.Rollback(r.Context())
	tag, err := tx.Exec(r.Context(), `
		UPDATE sessions SET active_company_id=$1,active_creator_id=$2,last_seen_at=now()
		WHERE id=$3 AND user_id=$4 AND revoked_at IS NULL AND expires_at>now()`, companyID, creatorID, p.SessionID, p.ID)
	if err != nil || tag.RowsAffected() != 1 {
		problem(w, http.StatusUnauthorized, "authentication required", "session expired")
		return
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, companyID, "SET_AUTH_CONTEXT", "SESSION", &p.SessionID, http.StatusOK, map[string]any{"creatorId": creatorID, "allCompanies": p.Role == roleOwner && companyID == nil})); err != nil {
		problem(w, http.StatusInternalServerError, "context update failed", "could not write audit record")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "context update failed", "could not commit context update")
		return
	}
	markResponseAuditCommitted(w)
	writeJSON(w, http.StatusOK, map[string]any{"activeContext": map[string]any{
		"companyId": companyID, "creatorId": creatorID, "allCompanies": p.Role == roleOwner && companyID == nil,
	}})
}

func (s *Server) requireOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Value(principalKey).(principal).Role != roleOwner {
			problem(w, http.StatusForbidden, "forbidden", "workspace owner role is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireActiveCompanyPermission(permission string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := r.Context().Value(principalKey).(principal)
			if p.Role == roleOwner {
				next.ServeHTTP(w, r)
				return
			}
			if p.Role != roleManager || p.ActiveCompanyID == nil || !p.hasPermission(permission) {
				problem(w, http.StatusForbidden, "forbidden", "an active company with permission "+permission+" is required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// requireManagement keeps the creator portal separate from every legacy
// management endpoint. OWNER may operate in all-company mode; MANAGER must
// always have a currently assigned active company selected.
func (s *Server) requireManagement(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.Context().Value(principalKey).(principal)
		switch p.Role {
		case roleOwner:
			next.ServeHTTP(w, r)
		case roleManager:
			if p.ActiveCompanyID == nil || !p.hasCompany(*p.ActiveCompanyID) {
				problem(w, http.StatusForbidden, "forbidden", "an assigned active company is required")
				return
			}
			next.ServeHTTP(w, r)
		default:
			problem(w, http.StatusForbidden, "forbidden", "management access is not available to creator accounts")
		}
	})
}

func (s *Server) requireCreatorPortal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.Context().Value(principalKey).(principal)
		if p.Role != roleCreator {
			problem(w, http.StatusForbidden, "forbidden", "creator account is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireActiveOwnCreator(next http.Handler) http.Handler {
	return s.requireCreatorPortal(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.Context().Value(principalKey).(principal)
		if p.ActiveCreatorID == nil || p.ActiveCompanyID == nil {
			problem(w, http.StatusForbidden, "creator context required", "select one of your creator profiles")
			return
		}
		profile, ok := p.CreatorProfiles[*p.ActiveCreatorID]
		if !ok || profile.CompanyID != *p.ActiveCompanyID {
			problem(w, http.StatusUnauthorized, "authentication required", "creator context is no longer available")
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// managementCompanyScope returns a parameterized company predicate for a
// table alias that has company_id. OWNER all-company mode intentionally has no
// predicate. CREATOR is rejected by the caller before any query runs.
func managementCompanyScope(p principal, alias string, parameter int) (string, []any, bool) {
	if p.Role == roleOwner && p.ActiveCompanyID == nil {
		return "", nil, true
	}
	if (p.Role == roleOwner || p.Role == roleManager) && p.ActiveCompanyID != nil && p.hasCompany(*p.ActiveCompanyID) {
		return fmt.Sprintf(" AND %s.company_id=$%d", alias, parameter), []any{*p.ActiveCompanyID}, true
	}
	return "", nil, false
}

func activeCreatorContext(p principal) (creatorProfileAccess, bool) {
	if p.Role != roleCreator || p.ActiveCreatorID == nil || p.ActiveCompanyID == nil {
		return creatorProfileAccess{}, false
	}
	profile, ok := p.CreatorProfiles[*p.ActiveCreatorID]
	return profile, ok && profile.CompanyID == *p.ActiveCompanyID
}

// resourceKind is company, creator, platform-account, or company-vk-account.
// Missing and cross-tenant resources deliberately share the same 404 response.
func (s *Server) resourceCompanyID(ctx context.Context, p principal, resourceKind, resourceID string) (string, error) {
	var companyID string
	var query string
	switch resourceKind {
	case "company":
		query = `SELECT id FROM companies WHERE id=$1 AND organization_id=$2 AND archived_at IS NULL`
	case "creator":
		query = `SELECT c.company_id FROM creators c JOIN companies x ON x.id=c.company_id WHERE c.id=$1 AND c.organization_id=$2 AND x.archived_at IS NULL`
	case "platform-account":
		query = `SELECT a.company_id FROM platform_accounts a JOIN companies x ON x.id=a.company_id WHERE a.id=$1 AND a.organization_id=$2 AND x.archived_at IS NULL`
	case "company-vk-account":
		query = `SELECT a.company_id FROM company_vk_accounts a JOIN companies x ON x.id=a.company_id WHERE a.id=$1 AND a.organization_id=$2 AND x.archived_at IS NULL`
	default:
		return "", pgx.ErrNoRows
	}
	err := s.pool.QueryRow(ctx, query, resourceID, p.OrganizationID).Scan(&companyID)
	return companyID, err
}

func (p principal) canAccessCompany(companyID string) bool {
	switch p.Role {
	case roleOwner:
		return p.hasCompany(companyID) && (p.ActiveCompanyID == nil || *p.ActiveCompanyID == companyID)
	case roleManager:
		return p.ActiveCompanyID != nil && *p.ActiveCompanyID == companyID && p.hasCompany(companyID)
	case roleCreator:
		return p.ActiveCompanyID != nil && *p.ActiveCompanyID == companyID
	default:
		return false
	}
}

func authorizeCompanyAction(p principal, companyID, permission string) int {
	if !p.canAccessCompany(companyID) {
		return http.StatusNotFound
	}
	if permission != "" && !p.hasPermission(permission) {
		return http.StatusForbidden
	}
	return 0
}

// Exports are scoped by the selected context. OWNER all-company mode is the
// only context that may intentionally combine creators from multiple companies.
func (p principal) canExportCompany(companyID string) bool {
	if p.Role == roleOwner {
		if !p.hasCompany(companyID) {
			return false
		}
		return p.ActiveCompanyID == nil || *p.ActiveCompanyID == companyID
	}
	return p.canAccessCompany(companyID)
}

func (p principal) oauthSelectionCompanyIDs(targetCompanyID string) []string {
	if p.Role == roleOwner && p.ActiveCompanyID == nil {
		ids := make([]string, 0, len(p.Companies))
		for companyID := range p.Companies {
			ids = append(ids, companyID)
		}
		sort.Strings(ids)
		return ids
	}
	if p.Role == roleOwner && p.ActiveCompanyID != nil {
		return []string{*p.ActiveCompanyID}
	}
	if p.canAccessCompany(targetCompanyID) {
		return []string{targetCompanyID}
	}
	return nil
}

func (s *Server) validateCreatorExportScope(ctx context.Context, p principal, creatorIDs []string) int {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id::text,c.company_id::text
		FROM creators c JOIN companies x ON x.id=c.company_id AND x.organization_id=c.organization_id
		WHERE c.id::text=ANY($1) AND c.organization_id=$2
		  AND c.archived_at IS NULL AND x.archived_at IS NULL`, creatorIDs, p.OrganizationID)
	if err != nil {
		return http.StatusInternalServerError
	}
	defer rows.Close()
	found := make(map[string]struct{}, len(creatorIDs))
	for rows.Next() {
		var creatorID, companyID string
		if err = rows.Scan(&creatorID, &companyID); err != nil {
			return http.StatusInternalServerError
		}
		if !p.canExportCompany(companyID) {
			return http.StatusNotFound
		}
		found[creatorID] = struct{}{}
	}
	if err = rows.Err(); err != nil {
		return http.StatusInternalServerError
	}
	if len(found) != len(creatorIDs) {
		return http.StatusNotFound
	}
	return 0
}

func (s *Server) authorizeCreatorAction(ctx context.Context, p principal, creatorID, permission string) int {
	companyID, err := s.resourceCompanyID(ctx, p, "creator", creatorID)
	if err != nil || !p.canAccessCompany(companyID) {
		return http.StatusNotFound
	}
	if !p.hasPermission(permission) {
		return http.StatusForbidden
	}
	return 0
}

func (s *Server) requireCompanyAccess(resourceKind string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := r.Context().Value(principalKey).(principal)
			if p.Role == roleCreator {
				problem(w, http.StatusForbidden, "forbidden", "management access is not available to creator accounts")
				return
			}
			companyID, err := s.resourceCompanyID(r.Context(), p, resourceKind, chi.URLParam(r, "id"))
			if err != nil || authorizeCompanyAction(p, companyID, "") == http.StatusNotFound {
				problem(w, http.StatusNotFound, "not found", "resource does not exist")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *Server) requireCompanyPermission(permission, resourceKind string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return s.requireCompanyAccess(resourceKind)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := r.Context().Value(principalKey).(principal)
			if !p.hasPermission(permission) {
				problem(w, http.StatusForbidden, "forbidden", "company permission "+permission+" is required")
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}

func (s *Server) requireOwnCreatorProfile(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.Context().Value(principalKey).(principal)
		id := chi.URLParam(r, "id")
		if p.ActiveCreatorID == nil || *p.ActiveCreatorID != id {
			problem(w, http.StatusNotFound, "not found", "creator profile does not exist")
			return
		}
		if _, ok := p.CreatorProfiles[id]; !ok {
			problem(w, http.StatusNotFound, "not found", "creator profile does not exist")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireCreatorProfileAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.Context().Value(principalKey).(principal)
		if p.Role == roleCreator {
			s.requireOwnCreatorProfile(next).ServeHTTP(w, r)
			return
		}
		s.requireCompanyAccess("creator")(next).ServeHTTP(w, r)
	})
}

func sessionDigest(r *http.Request, cookieName string) ([]byte, error) {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(cookie.Value))
	return digest[:], nil
}
