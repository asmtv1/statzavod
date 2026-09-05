package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type managerCompanyAssignmentInput struct {
	CompanyID   string   `json:"companyId"`
	Permissions []string `json:"permissions"`
}

type workspaceUserItem struct {
	ID                 string                          `json:"id"`
	Email              string                          `json:"email"`
	Role               string                          `json:"role"`
	Status             string                          `json:"status"`
	CompanyAssignments []managerCompanyAssignmentInput `json:"companyAssignments"`
}

var managerPermissionSet = func() map[string]struct{} {
	result := make(map[string]struct{}, len(managerPermissions))
	for _, permission := range managerPermissions {
		result[permission] = struct{}{}
	}
	return result
}()

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func validEmail(email string) bool {
	parsed, err := mail.ParseAddress(email)
	return err == nil && parsed.Address == email && strings.Contains(email, "@")
}

func isUniqueViolation(err error) bool {
	var databaseError *pgconn.PgError
	return errors.As(err, &databaseError) && databaseError.Code == "23505"
}

func validWorkspaceUserRole(role string) bool {
	return role == roleOwner || role == roleManager
}

func legacyRoleForMembership(role string) string {
	if role == roleOwner {
		return "ADMIN"
	}
	return "VIEWER"
}

func normalizeAssignmentInputs(items []managerCompanyAssignmentInput) ([]managerCompanyAssignmentInput, error) {
	seenCompanies := make(map[string]struct{}, len(items))
	result := make([]managerCompanyAssignmentInput, 0, len(items))
	for _, item := range items {
		item.CompanyID = strings.TrimSpace(item.CompanyID)
		if item.CompanyID == "" {
			return nil, errors.New("companyId is required")
		}
		if _, duplicate := seenCompanies[item.CompanyID]; duplicate {
			return nil, errors.New("each company may appear only once")
		}
		seenCompanies[item.CompanyID] = struct{}{}
		seenPermissions := make(map[string]struct{}, len(item.Permissions))
		permissions := make([]string, 0, len(item.Permissions))
		for _, permission := range item.Permissions {
			permission = strings.ToUpper(strings.TrimSpace(permission))
			if _, valid := managerPermissionSet[permission]; !valid {
				return nil, errors.New("unknown manager permission: " + permission)
			}
			if _, duplicate := seenPermissions[permission]; duplicate {
				continue
			}
			seenPermissions[permission] = struct{}{}
			permissions = append(permissions, permission)
		}
		sort.Strings(permissions)
		item.Permissions = permissions
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CompanyID < result[j].CompanyID })
	return result, nil
}

func (s *Server) listWorkspaceUsers(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	rows, err := s.pool.Query(r.Context(), `
		SELECT u.id::text,u.email,m.membership_role::text,u.status::text
		FROM organization_memberships m
		JOIN users u ON u.id=m.user_id
		WHERE m.organization_id=$1
		ORDER BY CASE m.membership_role WHEN 'OWNER' THEN 0 WHEN 'MANAGER' THEN 1 ELSE 2 END,u.email,u.id`, p.OrganizationID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "users failed", "could not load workspace users")
		return
	}
	defer rows.Close()
	items := make([]workspaceUserItem, 0)
	byID := make(map[string]int)
	for rows.Next() {
		var item workspaceUserItem
		item.CompanyAssignments = make([]managerCompanyAssignmentInput, 0)
		if err = rows.Scan(&item.ID, &item.Email, &item.Role, &item.Status); err != nil {
			problem(w, http.StatusInternalServerError, "users failed", "could not read workspace users")
			return
		}
		items = append(items, item)
		byID[item.ID] = len(items) - 1
	}
	if err = rows.Err(); err != nil {
		problem(w, http.StatusInternalServerError, "users failed", "could not read workspace users")
		return
	}
	assignmentRows, err := s.pool.Query(r.Context(), `
		SELECT a.manager_user_id::text,a.company_id::text,
		       COALESCE(array_agg(mp.permission::text ORDER BY mp.permission)
		         FILTER (WHERE mp.permission IS NOT NULL),'{}'::text[])
		FROM manager_company_assignments a
		LEFT JOIN manager_company_permissions mp ON mp.assignment_id=a.id
		WHERE a.organization_id=$1
		GROUP BY a.id,a.manager_user_id,a.company_id
		ORDER BY a.manager_user_id,a.company_id`, p.OrganizationID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "users failed", "could not load company assignments")
		return
	}
	defer assignmentRows.Close()
	for assignmentRows.Next() {
		var userID, companyID string
		var permissions []string
		if err = assignmentRows.Scan(&userID, &companyID, &permissions); err != nil {
			problem(w, http.StatusInternalServerError, "users failed", "could not read company assignments")
			return
		}
		if index, ok := byID[userID]; ok {
			items[index].CompanyAssignments = append(items[index].CompanyAssignments, managerCompanyAssignmentInput{CompanyID: companyID, Permissions: permissions})
		}
	}
	if err = assignmentRows.Err(); err != nil {
		problem(w, http.StatusInternalServerError, "users failed", "could not read company assignments")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createWorkspaceUser(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	var in struct {
		Email              string                          `json:"email"`
		Password           string                          `json:"password"`
		Role               string                          `json:"role"`
		CompanyAssignments []managerCompanyAssignmentInput `json:"companyAssignments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		problem(w, http.StatusBadRequest, "invalid user", "expected JSON body")
		return
	}
	in.Email = normalizeEmail(in.Email)
	in.Role = strings.ToUpper(strings.TrimSpace(in.Role))
	if !validEmail(in.Email) || !validWorkspaceUserRole(in.Role) || !validPassword(in.Password) {
		problem(w, http.StatusBadRequest, "invalid user", "email, role OWNER or MANAGER, and a password of at least 12 characters are required")
		return
	}
	assignments, err := normalizeAssignmentInputs(in.CompanyAssignments)
	if err != nil || (in.Role == roleOwner && len(assignments) != 0) {
		problem(w, http.StatusBadRequest, "invalid assignments", "company assignments are supported only for MANAGER and must contain valid permissions")
		return
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		problem(w, http.StatusInternalServerError, "user creation failed", "could not secure password")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "user creation failed", "could not start user creation")
		return
	}
	defer tx.Rollback(r.Context())
	var userID string
	err = tx.QueryRow(r.Context(), `
		INSERT INTO users(email,password_hash,role,status)
		VALUES($1,$2,$3::user_role,'ACTIVE') RETURNING id`, in.Email, hash, legacyRoleForMembership(in.Role)).Scan(&userID)
	if isUniqueViolation(err) {
		problem(w, http.StatusConflict, "email already in use", "email is already assigned to an account")
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "user creation failed", "could not create user")
		return
	}
	if _, err = tx.Exec(r.Context(), `
		INSERT INTO organization_memberships(organization_id,user_id,role,membership_role)
		VALUES($1,$2,$3::user_role,$4::membership_role)`, p.OrganizationID, userID, legacyRoleForMembership(in.Role), in.Role); err != nil {
		problem(w, http.StatusInternalServerError, "user creation failed", "could not create workspace membership")
		return
	}
	if in.Role == roleManager {
		if err = replaceManagerAssignmentsTx(r, tx, p, userID, assignments); err != nil {
			writeAssignmentError(w, err)
			return
		}
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, nil, "CREATE", "USER", &userID, http.StatusCreated, map[string]any{"role": in.Role})); err != nil {
		problem(w, http.StatusInternalServerError, "user creation failed", "could not save audit record")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "user creation failed", "could not commit user creation")
		return
	}
	markResponseAuditCommitted(w)
	writeJSON(w, http.StatusCreated, workspaceUserItem{ID: userID, Email: in.Email, Role: in.Role, Status: "ACTIVE", CompanyAssignments: assignments})
}

func (s *Server) updateWorkspaceUser(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	userID := chi.URLParam(r, "id")
	var in struct {
		Email  *string `json:"email"`
		Status *string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || (in.Email == nil && in.Status == nil) {
		problem(w, http.StatusBadRequest, "invalid user", "email or status is required")
		return
	}
	if in.Email != nil {
		normalized := normalizeEmail(*in.Email)
		if !validEmail(normalized) {
			problem(w, http.StatusBadRequest, "invalid email", "email is required")
			return
		}
		in.Email = &normalized
	}
	if in.Status != nil {
		normalized := strings.ToUpper(strings.TrimSpace(*in.Status))
		if normalized != "ACTIVE" && normalized != "SUSPENDED" {
			problem(w, http.StatusBadRequest, "invalid status", "status must be ACTIVE or SUSPENDED")
			return
		}
		in.Status = &normalized
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "user update failed", "could not start user update")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, p.OrganizationID); err != nil {
		problem(w, http.StatusInternalServerError, "user update failed", "could not lock workspace")
		return
	}
	var role, currentStatus string
	if err = tx.QueryRow(r.Context(), `SELECT m.membership_role::text,u.status::text FROM organization_memberships m JOIN users u ON u.id=m.user_id WHERE m.organization_id=$1 AND m.user_id=$2 FOR UPDATE OF u,m`, p.OrganizationID, userID).Scan(&role, &currentStatus); errors.Is(err, pgx.ErrNoRows) {
		problem(w, http.StatusNotFound, "not found", "workspace user does not exist")
		return
	} else if err != nil {
		problem(w, http.StatusInternalServerError, "user update failed", "could not load workspace user")
		return
	}
	if role == roleOwner && currentStatus == "ACTIVE" && in.Status != nil && *in.Status == "SUSPENDED" {
		if ok, countErr := activeOwnerRemains(r, tx, p.OrganizationID, userID); countErr != nil {
			problem(w, http.StatusInternalServerError, "user update failed", "could not validate workspace owners")
			return
		} else if !ok {
			problem(w, http.StatusConflict, "last owner", "the last active owner cannot be suspended")
			return
		}
	}
	_, err = tx.Exec(r.Context(), `UPDATE users SET email=COALESCE($2,email),status=COALESCE($3::user_status,status),updated_at=now() WHERE id=$1`, userID, in.Email, in.Status)
	if isUniqueViolation(err) {
		problem(w, http.StatusConflict, "email already in use", "email is already assigned to an account")
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "user update failed", "could not update workspace user")
		return
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, nil, "UPDATE_USER", "USER", &userID, http.StatusNoContent, map[string]any{"emailChanged": in.Email != nil, "statusChanged": in.Status != nil})); err != nil {
		problem(w, http.StatusInternalServerError, "user update failed", "could not write audit record")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "user update failed", "could not commit user update")
		return
	}
	markResponseAuditCommitted(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) resetWorkspaceUserPassword(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	userID := chi.URLParam(r, "id")
	var in struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || !validPassword(in.Password) {
		problem(w, http.StatusBadRequest, "invalid password", "a password of at least 12 characters is required")
		return
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		problem(w, http.StatusInternalServerError, "password reset failed", "could not secure password")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "password reset failed", "could not start password reset")
		return
	}
	defer tx.Rollback(r.Context())
	tag, err := tx.Exec(r.Context(), `UPDATE users u SET password_hash=$3,updated_at=now() FROM organization_memberships m WHERE u.id=$2 AND m.user_id=u.id AND m.organization_id=$1`, p.OrganizationID, userID, hash)
	if err != nil {
		problem(w, http.StatusInternalServerError, "password reset failed", "could not reset password")
		return
	}
	if tag.RowsAffected() == 0 {
		problem(w, http.StatusNotFound, "not found", "workspace user does not exist")
		return
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, nil, "RESET_USER_PASSWORD", "USER", &userID, http.StatusNoContent, map[string]any{})); err != nil {
		problem(w, http.StatusInternalServerError, "password reset failed", "could not write audit record")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "password reset failed", "could not commit password reset")
		return
	}
	markResponseAuditCommitted(w)
	w.WriteHeader(http.StatusNoContent)
}

func activeOwnerRemains(r *http.Request, tx pgx.Tx, organizationID, excludedUserID string) (bool, error) {
	var count int
	err := tx.QueryRow(r.Context(), `SELECT count(*) FROM organization_memberships m JOIN users u ON u.id=m.user_id WHERE m.organization_id=$1 AND m.membership_role='OWNER' AND u.status='ACTIVE' AND m.user_id<>$2`, organizationID, excludedUserID).Scan(&count)
	return count > 0, err
}

func (s *Server) deleteWorkspaceUser(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	userID := chi.URLParam(r, "id")
	if userID == p.ID {
		problem(w, http.StatusConflict, "self deletion requires confirmation", "use workspace self-delete to remove your own account")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "user deletion failed", "could not start user deletion")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, p.OrganizationID); err != nil {
		problem(w, http.StatusInternalServerError, "user deletion failed", "could not lock workspace")
		return
	}
	var role string
	if err = tx.QueryRow(r.Context(), `SELECT membership_role::text FROM organization_memberships WHERE organization_id=$1 AND user_id=$2 FOR UPDATE`, p.OrganizationID, userID).Scan(&role); errors.Is(err, pgx.ErrNoRows) {
		problem(w, http.StatusNotFound, "not found", "workspace user does not exist")
		return
	} else if err != nil {
		problem(w, http.StatusInternalServerError, "user deletion failed", "could not load workspace user")
		return
	}
	if role == roleOwner {
		if ok, countErr := activeOwnerRemains(r, tx, p.OrganizationID, userID); countErr != nil {
			problem(w, http.StatusInternalServerError, "user deletion failed", "could not validate workspace owners")
			return
		} else if !ok {
			detail := "the last owner cannot be deleted through this endpoint"
			if userID == p.ID {
				detail = "use workspace self-delete to remove the last owner and workspace"
			}
			problem(w, http.StatusConflict, "last owner", detail)
			return
		}
	}
	if _, err = tx.Exec(r.Context(), `UPDATE sessions SET revoked_at=COALESCE(revoked_at,now()),active_company_id=NULL,active_creator_id=NULL WHERE user_id=$1`, userID); err != nil {
		problem(w, http.StatusInternalServerError, "user deletion failed", "could not revoke sessions")
		return
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, nil, "DELETE_USER", "USER", &userID, http.StatusNoContent, map[string]any{"role": role})); err != nil {
		problem(w, http.StatusInternalServerError, "user deletion failed", "could not write audit record")
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM users u USING organization_memberships m WHERE u.id=$2 AND m.user_id=u.id AND m.organization_id=$1`, p.OrganizationID, userID); err != nil {
		problem(w, http.StatusInternalServerError, "user deletion failed", "could not delete workspace user")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "user deletion failed", "could not commit user deletion")
		return
	}
	markResponseAuditCommitted(w)
	w.WriteHeader(http.StatusNoContent)
}

var errAssignmentTargetNotManager = errors.New("assignment target is not a manager")
var errAssignmentCompanyNotFound = errors.New("assignment company does not exist")

func replaceManagerAssignmentsTx(r *http.Request, tx pgx.Tx, actor principal, managerUserID string, items []managerCompanyAssignmentInput) error {
	var role string
	if err := tx.QueryRow(r.Context(), `SELECT membership_role::text FROM organization_memberships WHERE organization_id=$1 AND user_id=$2 FOR UPDATE`, actor.OrganizationID, managerUserID).Scan(&role); err != nil || role != roleManager {
		return errAssignmentTargetNotManager
	}
	for _, item := range items {
		var exists bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM companies WHERE id=$1 AND organization_id=$2 AND archived_at IS NULL)`, item.CompanyID, actor.OrganizationID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errAssignmentCompanyNotFound
		}
	}
	if _, err := tx.Exec(r.Context(), `DELETE FROM manager_company_assignments WHERE organization_id=$1 AND manager_user_id=$2`, actor.OrganizationID, managerUserID); err != nil {
		return err
	}
	for _, item := range items {
		var assignmentID string
		if err := tx.QueryRow(r.Context(), `INSERT INTO manager_company_assignments(organization_id,manager_user_id,company_id,created_by) VALUES($1,$2,$3,$4) RETURNING id`, actor.OrganizationID, managerUserID, item.CompanyID, actor.ID).Scan(&assignmentID); err != nil {
			return err
		}
		for _, permission := range item.Permissions {
			if _, err := tx.Exec(r.Context(), `INSERT INTO manager_company_permissions(assignment_id,permission,granted_by) VALUES($1,$2::manager_permission,$3)`, assignmentID, permission, actor.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeAssignmentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errAssignmentTargetNotManager):
		problem(w, http.StatusNotFound, "not found", "workspace manager does not exist")
	case errors.Is(err, errAssignmentCompanyNotFound):
		problem(w, http.StatusNotFound, "not found", "company does not exist")
	default:
		problem(w, http.StatusInternalServerError, "assignments failed", "could not replace company assignments")
	}
}

func (s *Server) replaceManagerCompanyAssignments(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	managerUserID := chi.URLParam(r, "id")
	var in struct {
		Items *[]managerCompanyAssignmentInput `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Items == nil {
		problem(w, http.StatusBadRequest, "invalid assignments", "expected an items array")
		return
	}
	items, err := normalizeAssignmentInputs(*in.Items)
	if err != nil {
		problem(w, http.StatusBadRequest, "invalid assignments", err.Error())
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "assignments failed", "could not start assignment update")
		return
	}
	defer tx.Rollback(r.Context())
	if err = replaceManagerAssignmentsTx(r, tx, p, managerUserID, items); err != nil {
		writeAssignmentError(w, err)
		return
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, nil, "REPLACE_MANAGER_COMPANY_ASSIGNMENTS", "USER", &managerUserID, http.StatusOK, map[string]any{"companyCount": len(items)})); err != nil {
		problem(w, http.StatusInternalServerError, "assignments failed", "could not write audit record")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "assignments failed", "could not commit company assignments")
		return
	}
	markResponseAuditCommitted(w)
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getCreatorLoginAccount(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	creatorID := chi.URLParam(r, "id")
	var userID, email, status string
	err := s.pool.QueryRow(r.Context(), `SELECT u.id::text,u.email,u.status::text FROM creators c JOIN users u ON u.id=c.login_user_id WHERE c.id=$1 AND c.organization_id=$2`, creatorID, p.OrganizationID).Scan(&userID, &email, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"account": nil})
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "creator account failed", "could not load creator account")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": map[string]string{"id": userID, "email": email, "status": status}})
}

func (s *Server) putCreatorLoginAccount(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	creatorID := chi.URLParam(r, "id")
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || !validEmail(normalizeEmail(in.Email)) {
		problem(w, http.StatusBadRequest, "invalid creator account", "email is required")
		return
	}
	in.Email = normalizeEmail(in.Email)
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "creator account failed", "could not start creator account update")
		return
	}
	defer tx.Rollback(r.Context())
	var companyID string
	var currentUserID *string
	if err = tx.QueryRow(r.Context(), `SELECT c.company_id::text,c.login_user_id::text FROM creators c JOIN companies x ON x.id=c.company_id AND x.organization_id=c.organization_id WHERE c.id=$1 AND c.organization_id=$2 AND c.archived_at IS NULL AND x.archived_at IS NULL FOR UPDATE OF c`, creatorID, p.OrganizationID).Scan(&companyID, &currentUserID); errors.Is(err, pgx.ErrNoRows) {
		problem(w, http.StatusNotFound, "not found", "creator does not exist")
		return
	} else if err != nil {
		problem(w, http.StatusInternalServerError, "creator account failed", "could not load creator")
		return
	}
	if status := authorizeCompanyAction(p, companyID, "CREATOR_ACCOUNT_MANAGE"); status != 0 {
		if status == http.StatusForbidden {
			problem(w, http.StatusForbidden, "forbidden", "company permission CREATOR_ACCOUNT_MANAGE is required")
		} else {
			problem(w, http.StatusNotFound, "not found", "creator does not exist")
		}
		return
	}
	var userID string
	var membershipOrganizationID, membershipRole *string
	err = tx.QueryRow(r.Context(), `SELECT u.id::text,m.organization_id::text,m.membership_role::text FROM users u LEFT JOIN organization_memberships m ON m.user_id=u.id WHERE u.email=$1`, in.Email).Scan(&userID, &membershipOrganizationID, &membershipRole)
	created := false
	if errors.Is(err, pgx.ErrNoRows) {
		if !validPassword(in.Password) {
			problem(w, http.StatusBadRequest, "password required", "a new creator account requires a password of at least 12 characters")
			return
		}
		hash, hashErr := hashPassword(in.Password)
		if hashErr != nil {
			problem(w, http.StatusInternalServerError, "creator account failed", "could not secure password")
			return
		}
		if err = tx.QueryRow(r.Context(), `INSERT INTO users(email,password_hash,role,status) VALUES($1,$2,'VIEWER','ACTIVE') RETURNING id`, in.Email, hash).Scan(&userID); err != nil {
			if isUniqueViolation(err) {
				problem(w, http.StatusConflict, "email unavailable", "email cannot be used for this creator account")
			} else {
				problem(w, http.StatusInternalServerError, "creator account failed", "could not create account")
			}
			return
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO organization_memberships(organization_id,user_id,role,membership_role) VALUES($1,$2,'VIEWER','CREATOR')`, p.OrganizationID, userID); err != nil {
			problem(w, http.StatusInternalServerError, "creator account failed", "could not create creator membership")
			return
		}
		created = true
	} else if err != nil {
		problem(w, http.StatusInternalServerError, "creator account failed", "could not look up account")
		return
	} else {
		if membershipOrganizationID == nil || membershipRole == nil || *membershipOrganizationID != p.OrganizationID || *membershipRole != roleCreator {
			problem(w, http.StatusConflict, "email unavailable", "email belongs to a different account or workspace")
			return
		}
		if in.Password != "" {
			if p.Role != roleOwner {
				problem(w, http.StatusForbidden, "forbidden", "only an owner may reset an existing creator account password")
				return
			}
			if !validPassword(in.Password) {
				problem(w, http.StatusBadRequest, "invalid password", "a password of at least 12 characters is required")
				return
			}
			hash, hashErr := hashPassword(in.Password)
			if hashErr != nil {
				problem(w, http.StatusInternalServerError, "creator account failed", "could not secure password")
				return
			}
			if _, err = tx.Exec(r.Context(), `UPDATE users SET password_hash=$2,updated_at=now() WHERE id=$1`, userID, hash); err != nil {
				problem(w, http.StatusInternalServerError, "creator account failed", "could not reset creator password")
				return
			}
		}
	}
	if currentUserID != nil && *currentUserID != userID {
		problem(w, http.StatusConflict, "creator account already linked", "the creator profile is already linked to another account")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE creators SET login_user_id=$2,updated_at=now() WHERE id=$1`, creatorID, userID); isUniqueViolation(err) {
		problem(w, http.StatusConflict, "account already linked", "this creator account already has a profile in the company")
		return
	} else if err != nil {
		problem(w, http.StatusInternalServerError, "creator account failed", "could not link creator account")
		return
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &companyID, "LINK_CREATOR_LOGIN_ACCOUNT", "CREATOR", &creatorID, http.StatusOK, map[string]any{"loginUserId": userID, "created": created})); err != nil {
		problem(w, http.StatusInternalServerError, "creator account failed", "could not write audit record")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "creator account failed", "could not commit creator account update")
		return
	}
	markResponseAuditCommitted(w)
	writeJSON(w, http.StatusOK, map[string]any{"id": userID, "email": in.Email, "created": created})
}
