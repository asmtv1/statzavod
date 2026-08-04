package httpserver

import (
	"crypto/sha256"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

const connectionInvitationLifetime = 24 * time.Hour

func (s *Server) currentInstagramConnectionInvitation(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	creatorID := chi.URLParam(r, "id")
	var id string
	var expiresAt, createdAt time.Time
	err := s.pool.QueryRow(r.Context(), `
		SELECT id, expires_at, created_at
		FROM oauth_connection_invitations
		WHERE organization_id=$1 AND creator_id=$2 AND provider_key='instagram'
		  AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at>now()
		ORDER BY created_at DESC
		LIMIT 1`, p.OrganizationID, creatorID).Scan(&id, &expiresAt, &createdAt)
	if err != nil && err != pgx.ErrNoRows {
		problem(w, http.StatusInternalServerError, "invitation failed", "could not load the Instagram connection invitation")
		return
	}
	if err == pgx.ErrNoRows {
		writeJSON(w, http.StatusOK, map[string]any{"invitation": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invitation": map[string]any{"id": id, "expiresAt": expiresAt, "createdAt": createdAt}})
}

func (s *Server) createInstagramConnectionInvitation(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	creatorID := chi.URLParam(r, "id")
	provider := s.oauthProviders()["instagram"]
	if s.envelope == nil || provider.ClientID == "" || provider.ClientSecret == "" || provider.RedirectURL == "" {
		problem(w, http.StatusServiceUnavailable, "Instagram is not configured", "server OAuth credentials are missing")
		return
	}

	var exists bool
	if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM creators WHERE id=$1 AND organization_id=$2 AND status='ACTIVE' AND archived_at IS NULL)`, creatorID, p.OrganizationID).Scan(&exists); err != nil || !exists {
		problem(w, http.StatusNotFound, "creator not found", "creator does not exist in this organization")
		return
	}

	token := makeToken()
	hash := sha256.Sum256([]byte(token))
	expiresAt := time.Now().Add(connectionInvitationLifetime)
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "invitation failed", "could not create the Instagram connection invitation")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `UPDATE oauth_connection_invitations SET revoked_at=now() WHERE organization_id=$1 AND creator_id=$2 AND provider_key='instagram' AND consumed_at IS NULL AND revoked_at IS NULL`, p.OrganizationID, creatorID); err != nil {
		problem(w, http.StatusInternalServerError, "invitation failed", "could not replace the Instagram connection invitation")
		return
	}
	var invitationID string
	if err = tx.QueryRow(r.Context(), `INSERT INTO oauth_connection_invitations(organization_id,creator_id,provider_key,token_hash,expires_at,created_by) VALUES($1,$2,'instagram',$3,$4,$5) RETURNING id`, p.OrganizationID, creatorID, hash[:], expiresAt, p.ID).Scan(&invitationID); err != nil {
		problem(w, http.StatusInternalServerError, "invitation failed", "could not create the Instagram connection invitation")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,entity_id,metadata) VALUES($1,$2,'CREATE_INSTAGRAM_CONNECTION_INVITATION','CREATOR',$3,jsonb_build_object('invitationId',$4::text,'expiresAt',$5::timestamptz))`, p.OrganizationID, p.ID, creatorID, invitationID, expiresAt); err != nil {
		problem(w, http.StatusInternalServerError, "invitation failed", "could not record the Instagram connection invitation")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "invitation failed", "could not save the Instagram connection invitation")
		return
	}

	connectionURL := strings.TrimRight(s.config.PublicBaseURL, "/") + "/connect/instagram/" + url.PathEscape(token)
	writeJSON(w, http.StatusCreated, map[string]any{"id": invitationID, "connectionUrl": connectionURL, "expiresAt": expiresAt})
}

func (s *Server) revokeInstagramConnectionInvitation(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	creatorID := chi.URLParam(r, "id")
	invitationID := chi.URLParam(r, "invitationID")
	result, err := s.pool.Exec(r.Context(), `UPDATE oauth_connection_invitations SET revoked_at=now() WHERE id=$1 AND organization_id=$2 AND creator_id=$3 AND provider_key='instagram' AND consumed_at IS NULL AND revoked_at IS NULL`, invitationID, p.OrganizationID, creatorID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "invitation failed", "could not revoke the Instagram connection invitation")
		return
	}
	if result.RowsAffected() == 0 {
		problem(w, http.StatusNotFound, "invitation not found", "active Instagram connection invitation does not exist")
		return
	}
	_, _ = s.pool.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,entity_id,metadata) VALUES($1,$2,'REVOKE_INSTAGRAM_CONNECTION_INVITATION','CREATOR',$3,jsonb_build_object('invitationId',$4::text))`, p.OrganizationID, p.ID, creatorID, invitationID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) instagramConnectionInvitationInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token := chi.URLParam(r, "token")
	hash := sha256.Sum256([]byte(token))
	var creatorName string
	var expiresAt time.Time
	err := s.pool.QueryRow(r.Context(), `
		SELECT c.display_name, i.expires_at
		FROM oauth_connection_invitations i
		JOIN creators c ON c.id=i.creator_id AND c.organization_id=i.organization_id
		WHERE i.token_hash=$1 AND i.provider_key='instagram'
		  AND i.consumed_at IS NULL AND i.revoked_at IS NULL AND i.expires_at>now()
		  AND c.status='ACTIVE' AND c.archived_at IS NULL`, hash[:]).Scan(&creatorName, &expiresAt)
	if err != nil {
		problem(w, http.StatusNotFound, "invitation unavailable", "this Instagram connection link is invalid, expired, or already used")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"creatorName": creatorName, "expiresAt": expiresAt})
}

func (s *Server) authorizeInstagramConnectionInvitation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token := chi.URLParam(r, "token")
	hash := sha256.Sum256([]byte(token))
	var invitationID, organizationID, creatorID string
	err := s.pool.QueryRow(r.Context(), `
		SELECT i.id, i.organization_id, i.creator_id
		FROM oauth_connection_invitations i
		JOIN creators c ON c.id=i.creator_id AND c.organization_id=i.organization_id
		WHERE i.token_hash=$1 AND i.provider_key='instagram'
		  AND i.consumed_at IS NULL AND i.revoked_at IS NULL AND i.expires_at>now()
		  AND c.status='ACTIVE' AND c.archived_at IS NULL`, hash[:]).Scan(&invitationID, &organizationID, &creatorID)
	if err != nil {
		s.redirectToApp(w, r, "/connect/instagram/result?oauth=expired")
		return
	}
	provider := s.oauthProviders()["instagram"]
	if s.envelope == nil || provider.ClientID == "" || provider.ClientSecret == "" || provider.RedirectURL == "" {
		s.redirectToApp(w, r, "/connect/instagram/result?oauth=unavailable")
		return
	}
	state, challenge, err := s.createCreatorOAuthState(r.Context(), organizationID, creatorID, provider, nil, &invitationID)
	if err != nil {
		s.redirectToApp(w, r, "/connect/instagram/result?oauth=server-error")
		return
	}
	http.Redirect(w, r, s.oauthAuthorizationURL(provider, state, challenge), http.StatusFound)
}
