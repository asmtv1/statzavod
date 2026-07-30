package httpserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

type instagramSignedRequest struct {
	Algorithm string `json:"algorithm"`
	UserID    string `json:"user_id"`
}

func (s *Server) instagramDeauthorize(w http.ResponseWriter, r *http.Request) {
	payload, err := s.verifyInstagramSignedRequest(r)
	if err != nil {
		problem(w, http.StatusBadRequest, "invalid signed request", "Meta signature verification failed")
		return
	}
	accountID, organizationID, lookupErr := s.findInstagramAccount(r.Context(), payload.UserID)
	if lookupErr == nil {
		if err = s.deletePlatformAccountData(r.Context(), accountID, organizationID); err != nil {
			problem(w, http.StatusInternalServerError, "deauthorization failed", "connected Instagram data could not be removed")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) instagramDataDeletion(w http.ResponseWriter, r *http.Request) {
	payload, err := s.verifyInstagramSignedRequest(r)
	if err != nil {
		problem(w, http.StatusBadRequest, "invalid signed request", "Meta signature verification failed")
		return
	}
	confirmationCode := makeToken()
	accountID, organizationID, lookupErr := s.findInstagramAccount(r.Context(), payload.UserID)
	var nullableAccount, nullableOrganization any
	if lookupErr == nil {
		nullableAccount, nullableOrganization = accountID, organizationID
	}
	_, err = s.pool.Exec(r.Context(), `
		INSERT INTO platform_data_deletion_requests(
			confirmation_code,organization_id,platform_account_id,platform,external_id,status
		) VALUES($1,$2,$3,'INSTAGRAM',$4,'PENDING')
	`, confirmationCode, nullableOrganization, nullableAccount, payload.UserID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "deletion request failed", "could not record the deletion request")
		return
	}
	if lookupErr == nil {
		err = s.deletePlatformAccountData(r.Context(), accountID, organizationID)
	}
	status, errorMessage := "COMPLETED", ""
	if err != nil {
		status, errorMessage = "FAILED", err.Error()
	}
	_, _ = s.pool.Exec(r.Context(), `
		UPDATE platform_data_deletion_requests
		SET status=$2,completed_at=CASE WHEN $2='COMPLETED' THEN now() ELSE NULL END,error_message=NULLIF($3,'')
		WHERE confirmation_code=$1
	`, confirmationCode, status, errorMessage)
	if err != nil {
		problem(w, http.StatusInternalServerError, "deletion request failed", "Instagram data could not be removed")
		return
	}
	statusURL := strings.TrimRight(s.config.PublicBaseURL, "/") + "/api/v1/oauth/instagram/data-deletion/status?code=" + confirmationCode
	writeJSON(w, http.StatusOK, map[string]string{"url": statusURL, "confirmation_code": confirmationCode})
}

func (s *Server) instagramDataDeletionStatus(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		problem(w, http.StatusBadRequest, "confirmation code required", "provide a deletion confirmation code")
		return
	}
	var status string
	if err := s.pool.QueryRow(r.Context(), `SELECT status FROM platform_data_deletion_requests WHERE confirmation_code=$1`, code).Scan(&status); err != nil {
		problem(w, http.StatusNotFound, "request not found", "the deletion request does not exist")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"confirmationCode": code, "status": status})
}

func (s *Server) verifyInstagramSignedRequest(r *http.Request) (instagramSignedRequest, error) {
	if err := r.ParseForm(); err != nil {
		return instagramSignedRequest{}, err
	}
	value := r.Form.Get("signed_request")
	parts := strings.Split(value, ".")
	if len(parts) != 2 || s.config.InstagramClientSecret == "" {
		return instagramSignedRequest{}, errors.New("invalid signed request")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return instagramSignedRequest{}, err
	}
	mac := hmac.New(sha256.New, []byte(s.config.InstagramClientSecret))
	_, _ = mac.Write([]byte(parts[1]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return instagramSignedRequest{}, errors.New("signature mismatch")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return instagramSignedRequest{}, err
	}
	var payload instagramSignedRequest
	if err = json.Unmarshal(body, &payload); err != nil {
		return instagramSignedRequest{}, err
	}
	if !strings.EqualFold(payload.Algorithm, "HMAC-SHA256") || payload.UserID == "" {
		return instagramSignedRequest{}, errors.New("unsupported signed request")
	}
	return payload, nil
}

func (s *Server) findInstagramAccount(ctx context.Context, externalID string) (accountID, organizationID string, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT id,organization_id FROM platform_accounts
		WHERE platform='INSTAGRAM' AND external_id=$1
		ORDER BY updated_at DESC LIMIT 1
	`, externalID).Scan(&accountID, &organizationID)
	return
}

func (s *Server) deletePlatformAccountData(ctx context.Context, accountID, organizationID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `DELETE FROM sync_targets WHERE target_id=$1 AND organization_id=$2`, accountID, organizationID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM creator_account_assignments WHERE platform_account_id=$1`, accountID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM publications WHERE platform_account_id=$1 AND organization_id=$2`, accountID, organizationID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM platform_accounts WHERE id=$1 AND organization_id=$2`, accountID, organizationID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	_, _ = tx.Exec(ctx, `
		INSERT INTO audit_logs(organization_id,action,entity_type,metadata)
		VALUES($1,'PLATFORM_DATA_DELETED','PLATFORM_ACCOUNT',jsonb_build_object('accountId',$2))
	`, organizationID, accountID)
	return tx.Commit(ctx)
}
