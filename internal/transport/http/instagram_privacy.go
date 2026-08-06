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
	"github.com/jackc/pgx/v5/pgconn"
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
	accounts, lookupErr := s.findInstagramAccounts(r.Context(), payload.UserID)
	if lookupErr != nil {
		problem(w, http.StatusInternalServerError, "deauthorization failed", "connected Instagram account could not be loaded")
		return
	}
	if err = deleteInstagramAccountCopies(r.Context(), accounts, s.deletePlatformAccountData); err != nil {
		problem(w, http.StatusInternalServerError, "deauthorization failed", "connected Instagram data could not be removed")
		return
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
	accounts, lookupErr := s.findInstagramAccounts(r.Context(), payload.UserID)
	if lookupErr != nil {
		problem(w, http.StatusInternalServerError, "deletion request failed", "connected Instagram account could not be loaded")
		return
	}
	nullableAccount, nullableOrganization := instagramDeletionRequestOwner(accounts)
	_, err = s.pool.Exec(r.Context(), `
		INSERT INTO platform_data_deletion_requests(
			confirmation_code,organization_id,platform_account_id,platform,external_id,status
		) VALUES($1,$2,$3,'INSTAGRAM',$4,'PENDING')
	`, confirmationCode, nullableOrganization, nullableAccount, payload.UserID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "deletion request failed", "could not record the deletion request")
		return
	}
	err = deleteInstagramAccountCopies(r.Context(), accounts, s.deletePlatformAccountData)
	status, errorMessage := "COMPLETED", ""
	if err != nil {
		status, errorMessage = "FAILED", err.Error()
	}
	if _, updateErr := s.pool.Exec(r.Context(), `
		UPDATE platform_data_deletion_requests
		SET status=$2,completed_at=CASE WHEN $2='COMPLETED' THEN now() ELSE NULL END,error_message=NULLIF($3,'')
		WHERE confirmation_code=$1
	`, confirmationCode, status, errorMessage); updateErr != nil {
		problem(w, http.StatusInternalServerError, "deletion request failed", "could not update the deletion request")
		return
	}
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

type instagramAccountCopy struct {
	AccountID      string
	OrganizationID string
}

func (s *Server) findInstagramAccounts(ctx context.Context, externalID string) ([]instagramAccountCopy, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,organization_id FROM platform_accounts
		WHERE platform='INSTAGRAM' AND external_id=$1
		ORDER BY organization_id,id
	`, externalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := make([]instagramAccountCopy, 0)
	for rows.Next() {
		var account instagramAccountCopy
		if err = rows.Scan(&account.AccountID, &account.OrganizationID); err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return accounts, nil
}

func instagramDeletionRequestOwner(accounts []instagramAccountCopy) (accountID, organizationID any) {
	if len(accounts) != 1 {
		return nil, nil
	}
	return accounts[0].AccountID, accounts[0].OrganizationID
}

func deleteInstagramAccountCopies(ctx context.Context, accounts []instagramAccountCopy, deleteAccount func(context.Context, string, string) error) error {
	for _, account := range accounts {
		if err := deleteAccount(ctx, account.AccountID, account.OrganizationID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	return nil
}

func (s *Server) deletePlatformAccountData(ctx context.Context, accountID, organizationID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = deleteInstagramAccountDataInTx(ctx, tx, accountID, organizationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type instagramAccountDeletionTx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func deleteInstagramAccountDataInTx(ctx context.Context, tx instagramAccountDeletionTx, accountID, organizationID string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM sync_runs WHERE target_id IN (SELECT id FROM sync_targets WHERE target_id=$1 AND organization_id=$2)`, accountID, organizationID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sync_targets WHERE target_id=$1 AND organization_id=$2`, accountID, organizationID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM creator_account_assignments assignment USING platform_accounts account WHERE assignment.platform_account_id=$1 AND account.id=assignment.platform_account_id AND account.organization_id=$2`, accountID, organizationID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM publications WHERE platform_account_id=$1 AND organization_id=$2`, accountID, organizationID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM platform_accounts WHERE id=$1 AND organization_id=$2`, accountID, organizationID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO audit_logs(organization_id,action,entity_type,metadata)
		VALUES($1,'PLATFORM_DATA_DELETED','PLATFORM_ACCOUNT',jsonb_build_object('accountId',$2))
	`, organizationID, accountID); err != nil {
		return err
	}
	return nil
}
