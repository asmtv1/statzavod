package httpserver

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	storedConfirmationCode := deletionConfirmationDigest(confirmationCode)
	accounts, lookupErr := s.findInstagramAccounts(r.Context(), payload.UserID)
	if lookupErr != nil {
		problem(w, http.StatusInternalServerError, "deletion request failed", "connected Instagram account could not be loaded")
		return
	}
	nullableAccount, nullableOrganization := instagramDeletionRequestOwner(accounts)
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "deletion request failed", "could not start the deletion request")
		return
	}
	defer tx.Rollback(r.Context())
	externalID := ""
	if len(accounts) > 0 {
		externalID = payload.UserID
	}
	_, err = tx.Exec(r.Context(), `
		INSERT INTO platform_data_deletion_requests(
			confirmation_code,organization_id,platform_account_id,platform,external_id,status
		) VALUES($1,$2,$3,'INSTAGRAM',$4,'PENDING')
	`, storedConfirmationCode, nullableOrganization, nullableAccount, externalID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "deletion request failed", "could not record the deletion request")
		return
	}
	seenOrganizations := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		if _, duplicate := seenOrganizations[account.OrganizationID]; duplicate {
			continue
		}
		seenOrganizations[account.OrganizationID] = struct{}{}
		if _, err = tx.Exec(r.Context(), `
			INSERT INTO platform_data_deletion_request_workspaces(confirmation_code,organization_id)
			VALUES($1,$2) ON CONFLICT DO NOTHING
		`, storedConfirmationCode, account.OrganizationID); err != nil {
			problem(w, http.StatusInternalServerError, "deletion request failed", "could not scope the deletion request")
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "deletion request failed", "could not commit the deletion request")
		return
	}
	err = deleteInstagramAccountCopies(r.Context(), accounts, s.deletePlatformAccountData)
	status, errorMessage := "COMPLETED", ""
	if err != nil {
		status, errorMessage = "FAILED", "provider data deletion failed"
	}
	if _, updateErr := s.pool.Exec(r.Context(), `
		UPDATE platform_data_deletion_requests
		SET status=$2,completed_at=CASE WHEN $2='COMPLETED' THEN now() ELSE NULL END,
		    external_id='',error_message=NULLIF($3,'')
		WHERE confirmation_code=$1
	`, storedConfirmationCode, status, errorMessage); updateErr != nil {
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
	// The raw comparison keeps pre-00023 receipts pollable for their short
	// remaining lifetime; new receipts store only the digest.
	if err := s.pool.QueryRow(r.Context(), `SELECT status FROM platform_data_deletion_requests WHERE confirmation_code=$1 OR confirmation_code=$2 OR confirmation_code=$3`, deletionConfirmationDigest(code), legacyDeletionConfirmationDigest(code), code).Scan(&status); err != nil {
		problem(w, http.StatusNotFound, "request not found", "the deletion request does not exist")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"confirmationCode": code, "status": status})
}

func deletionConfirmationDigest(code string) string {
	digest := sha256.Sum256([]byte(code))
	return "sha256:" + base64.RawURLEncoding.EncodeToString(digest[:])
}

func legacyDeletionConfirmationDigest(code string) string {
	digest := md5.Sum([]byte(code)) // #nosec G401 -- migration lookup compatibility, not authentication.
	return "md5:" + hex.EncodeToString(digest[:])
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
	var companyID string
	if err = tx.QueryRow(ctx, `SELECT company_id::text FROM platform_accounts WHERE id=$1 AND organization_id=$2 FOR UPDATE`, accountID, organizationID).Scan(&companyID); err != nil {
		return err
	}
	if err = deleteInstagramAccountDataInTx(ctx, tx, accountID, organizationID); err != nil {
		return err
	}
	if err = s.writeAudit(ctx, tx, auditRecord{
		OrganizationID: organizationID,
		CompanyID:      &companyID,
		Action:         "PLATFORM_DATA_DELETED",
		EntityType:     "PLATFORM_ACCOUNT",
		EntityID:       &accountID,
		Metadata:       map[string]any{"accountId": accountID},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type instagramAccountDeletionTx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func deleteInstagramAccountDataInTx(ctx context.Context, tx instagramAccountDeletionTx, accountID, organizationID string) error {
	if _, err := tx.Exec(ctx, `SELECT id FROM platform_accounts WHERE id=$1 AND organization_id=$2 FOR UPDATE`, accountID, organizationID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sync_runs WHERE target_id IN (SELECT id FROM sync_targets WHERE target_id=$1 AND organization_id=$2)`, accountID, organizationID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sync_targets WHERE target_id=$1 AND organization_id=$2`, accountID, organizationID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE content_publish_jobs job SET status='CANCELLED',locked_by=NULL,locked_at=NULL,lease_expires_at=NULL,updated_at=now() FROM content_publish_targets target WHERE job.target_id=target.id AND job.organization_id=$1 AND target.organization_id=$1 AND target.platform_account_id=$2 AND job.status IN ('READY','RETRY_SCHEDULED')`, organizationID, accountID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE content_publish_targets target SET cancellation_requested_at=COALESCE(cancellation_requested_at,now()),status=CASE WHEN EXISTS (SELECT 1 FROM content_publish_jobs job WHERE job.target_id=target.id AND job.organization_id=target.organization_id AND job.status='RUNNING') THEN target.status ELSE 'CANCELLED' END,updated_at=now() WHERE target.organization_id=$1 AND target.platform_account_id=$2 AND target.status NOT IN ('SUCCEEDED','CANCELLED')`, organizationID, accountID); err != nil {
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
	return nil
}
