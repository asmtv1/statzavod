package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (s *Server) selfDeletionImpact(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	var owners, users, companies, creators int64
	err := s.pool.QueryRow(r.Context(), `
		SELECT
		  (SELECT count(*) FROM organization_memberships membership
		   JOIN users user_row ON user_row.id=membership.user_id
		   WHERE membership.organization_id=$1 AND membership.membership_role='OWNER' AND user_row.status='ACTIVE'),
		  (SELECT count(*) FROM organization_memberships WHERE organization_id=$1),
		  (SELECT count(*) FROM companies WHERE organization_id=$1),
		  (SELECT count(*) FROM creators WHERE organization_id=$1)
	`, p.OrganizationID).Scan(&owners, &users, &companies, &creators)
	if err != nil {
		problem(w, http.StatusInternalServerError, "deletion impact failed", "could not calculate deletion impact")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":                   map[bool]string{true: "WORKSPACE", false: "ACCOUNT"}[owners == 1],
		"workspaceWillBeDeleted": owners == 1,
		"counts":                 map[string]int64{"users": users, "companies": companies, "creators": creators},
	})
}

func (s *Server) selfDelete(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	var input struct {
		CurrentPassword string `json:"currentPassword"`
		ConfirmEmail    string `json:"confirmEmail"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.CurrentPassword == "" || strings.TrimSpace(input.ConfirmEmail) == "" {
		problem(w, http.StatusBadRequest, "invalid confirmation", "currentPassword and confirmEmail are required")
		return
	}
	if normalizeEmail(input.ConfirmEmail) != p.Email {
		problem(w, http.StatusBadRequest, "invalid confirmation", "confirmEmail must match the current email")
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "self-delete failed", "could not start deletion")
		return
	}
	defer tx.Rollback(r.Context())
	// All owner-removal paths use the same workspace advisory lock. Concurrent
	// self-deletes therefore observe a deterministic remaining-owner count.
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, p.OrganizationID); err != nil {
		problem(w, http.StatusInternalServerError, "self-delete failed", "could not lock workspace")
		return
	}
	var hash, role string
	err = tx.QueryRow(r.Context(), `
		SELECT user_row.password_hash,membership.membership_role::text
		FROM users user_row
		JOIN organization_memberships membership ON membership.user_id=user_row.id
		JOIN organizations workspace ON workspace.id=membership.organization_id AND workspace.lifecycle_state='ACTIVE'
		WHERE user_row.id=$1 AND membership.organization_id=$2
		FOR UPDATE OF user_row,membership,workspace
	`, p.ID, p.OrganizationID).Scan(&hash, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, http.StatusUnauthorized, "self-delete failed", "account is no longer active")
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "self-delete failed", "could not validate account")
		return
	}
	if role != roleOwner {
		problem(w, http.StatusForbidden, "forbidden", "workspace owner role is required")
		return
	}
	if !verifyPassword(hash, input.CurrentPassword) {
		problem(w, http.StatusUnauthorized, "invalid password", "current password is incorrect")
		return
	}
	var ownerCount int64
	if err = tx.QueryRow(r.Context(), `
		SELECT count(*) FROM organization_memberships membership
		JOIN users user_row ON user_row.id=membership.user_id
		WHERE membership.organization_id=$1 AND membership.membership_role='OWNER' AND user_row.status='ACTIVE'
	`, p.OrganizationID).Scan(&ownerCount); err != nil {
		problem(w, http.StatusInternalServerError, "self-delete failed", "could not validate workspace owners")
		return
	}
	actorID := p.ID
	if ownerCount > 1 {
		if err = s.writeAudit(r.Context(), tx, auditRecord{
			OrganizationID: p.OrganizationID, ActorID: &actorID,
			Action: "SELF_DELETE_ACCOUNT", EntityType: "USER", EntityID: &actorID,
			Metadata: map[string]any{},
		}); err != nil {
			problem(w, http.StatusInternalServerError, "self-delete failed", "could not write audit record")
			return
		}
		if err = purgeActorMediaUploadSessions(r.Context(), tx, p.OrganizationID, p.ID, s.now().UTC()); err != nil {
			problem(w, http.StatusInternalServerError, "self-delete failed", "could not schedule media cleanup")
			return
		}
		if _, err = tx.Exec(r.Context(), `DELETE FROM users WHERE id=$1`, p.ID); err != nil {
			problem(w, http.StatusInternalServerError, "self-delete failed", "could not delete account")
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			problem(w, http.StatusInternalServerError, "self-delete failed", "could not commit account deletion")
			return
		}
		markResponseAuditCommitted(w)
		http.SetCookie(w, &http.Cookie{Name: s.config.CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var receiptID string
	if err = tx.QueryRow(r.Context(), `
		INSERT INTO operational_receipts(operation,status,requested_at)
		VALUES('WORKSPACE_DELETE','PENDING',$1) RETURNING id::text
	`, s.now().UTC()).Scan(&receiptID); err != nil {
		problem(w, http.StatusInternalServerError, "self-delete failed", "could not create deletion receipt")
		return
	}
	if _, err = tx.Exec(r.Context(), `
		UPDATE organizations
		SET lifecycle_state='DELETING',deleting_at=$2,deletion_receipt_id=$3,
		    delete_lease_owner=NULL,delete_lease_until=NULL,updated_at=$2
		WHERE id=$1 AND lifecycle_state='ACTIVE'
	`, p.OrganizationID, s.now().UTC(), receiptID); err != nil {
		problem(w, http.StatusInternalServerError, "self-delete failed", "could not schedule workspace deletion")
		return
	}
	if _, err = tx.Exec(r.Context(), `
		UPDATE sessions SET revoked_at=COALESCE(revoked_at,$2),active_company_id=NULL,active_creator_id=NULL
		WHERE user_id IN (SELECT user_id FROM organization_memberships WHERE organization_id=$1)
	`, p.OrganizationID, s.now().UTC()); err != nil {
		problem(w, http.StatusInternalServerError, "self-delete failed", "could not block workspace access")
		return
	}
	if err = s.writeAudit(r.Context(), tx, auditRecord{
		OrganizationID: p.OrganizationID, ActorID: &actorID,
		Action: "SELF_DELETE_WORKSPACE_REQUESTED", EntityType: "WORKSPACE", EntityID: &p.OrganizationID,
		Metadata: map[string]any{},
	}); err != nil {
		problem(w, http.StatusInternalServerError, "self-delete failed", "could not write audit record")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "self-delete failed", "could not commit workspace deletion")
		return
	}
	markResponseAuditCommitted(w)
	// Access is already blocked. This bounded attempt reduces provider exposure;
	// the leased worker retries best-effort and completes local deletion.
	tokens, _ := s.lifecycleTokens(r.Context(), p.OrganizationID, nil)
	s.bestEffortRevoke(r.Context(), tokens)
	http.SetCookie(w, &http.Cookie{Name: s.config.CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	writeJSON(w, http.StatusAccepted, map[string]any{"receiptId": receiptID, "status": "DELETING"})
}
