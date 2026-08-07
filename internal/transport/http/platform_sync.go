package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type platformSyncJob struct {
	RunID          string
	TargetID       string
	AccountID      string
	OrganizationID string
	CreatorID      string
	Platform       string
	ExternalID     string
}

type syncResult struct {
	RecordsRead    int
	RecordsWritten int
}

// RunPlatformSync claims and processes up to limit due platform accounts.
// Claims use SKIP LOCKED, so multiple workers can safely execute this method.
func (s *Server) RunPlatformSync(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 10
	}
	processed := 0
	var firstErr error
	for processed < limit {
		job, ok, err := s.claimPlatformSync(ctx)
		if err != nil {
			return processed, err
		}
		if !ok {
			break
		}
		processed++
		result, syncErr := s.syncPlatformAccount(ctx, job)
		// A provider can consume the whole job deadline. Always retain a short,
		// uncancelled window to persist the final outcome and avoid RUNNING rows.
		finishCtx, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		finishErr := s.finishPlatformSync(finishCtx, job, result, syncErr)
		cancelFinish()
		if finishErr != nil && firstErr == nil {
			firstErr = finishErr
		}
		if syncErr != nil && firstErr == nil {
			firstErr = syncErr
		}
	}
	return processed, firstErr
}

func (s *Server) claimPlatformSync(ctx context.Context) (platformSyncJob, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return platformSyncJob{}, false, err
	}
	defer tx.Rollback(ctx)
	var job platformSyncJob
	err = tx.QueryRow(ctx, `
		SELECT t.id,t.target_id,t.organization_id,COALESCE(x.creator_id::text,''),a.platform,a.external_id
		FROM sync_targets t
		JOIN platform_accounts a ON a.id=t.target_id AND a.organization_id=t.organization_id
		LEFT JOIN creator_account_assignments x ON x.platform_account_id=a.id AND x.valid_to IS NULL
		LEFT JOIN company_vk_accounts v ON v.platform_account_id=a.id
		WHERE t.target_type='PLATFORM_ACCOUNT'
		  AND t.status='ACTIVE'
		  AND t.next_sync_at<=now()
		  AND a.status='ACTIVE'
		  AND (x.creator_id IS NOT NULL OR (a.platform='VK' AND v.id IS NOT NULL))
		ORDER BY t.next_sync_at
		FOR UPDATE OF t SKIP LOCKED
		LIMIT 1
	`).Scan(&job.TargetID, &job.AccountID, &job.OrganizationID, &job.CreatorID, &job.Platform, &job.ExternalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return platformSyncJob{}, false, tx.Commit(ctx)
	}
	if err != nil {
		return platformSyncJob{}, false, err
	}
	if err = tx.QueryRow(ctx, `INSERT INTO sync_runs(target_id) VALUES($1) RETURNING id`, job.TargetID).Scan(&job.RunID); err != nil {
		return platformSyncJob{}, false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE sync_targets SET next_sync_at=now()+cadence WHERE id=$1`, job.TargetID); err != nil {
		return platformSyncJob{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return platformSyncJob{}, false, err
	}
	return job, true, nil
}

func (s *Server) syncPlatformAccount(ctx context.Context, job platformSyncJob) (syncResult, error) {
	accessToken, err := s.accessTokenForSync(ctx, job)
	if err != nil {
		return syncResult{}, err
	}
	switch job.Platform {
	case "YOUTUBE":
		return s.syncYouTubeAccount(ctx, job, accessToken)
	case "INSTAGRAM":
		return s.syncInstagramAccount(ctx, job, accessToken)
	case "TIKTOK":
		videos, fetchErr := s.fetchTikTokVideos(ctx, accessToken)
		if fetchErr != nil {
			return syncResult{}, fetchErr
		}
		result := syncResult{RecordsRead: len(videos)}
		for _, video := range videos {
			if err := s.upsertTikTokVideo(ctx, job.OrganizationID, job.AccountID, video); err != nil {
				return result, err
			}
			result.RecordsWritten++
		}
		return result, nil
	case "VK":
		return s.syncVKCommunities(ctx, job, accessToken)
	default:
		return syncResult{}, fmt.Errorf("unsupported platform %s", job.Platform)
	}
}

func (s *Server) finishPlatformSync(ctx context.Context, job platformSyncJob, result syncResult, syncErr error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if syncErr == nil {
		if _, err = tx.Exec(ctx, `UPDATE sync_runs SET finished_at=now(),outcome='SUCCESS',records_read=$2,records_written=$3 WHERE id=$1`, job.RunID, result.RecordsRead, result.RecordsWritten); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE sync_targets SET last_success_at=now(),last_error=NULL,consecutive_failures=0 WHERE id=$1 AND status='ACTIVE'`, job.TargetID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE platform_accounts SET last_synced_at=now(),last_error=NULL,status='ACTIVE',updated_at=now() WHERE id=$1 AND status<>'DISCONNECTED' AND EXISTS(SELECT 1 FROM sync_targets WHERE id=$2 AND status='ACTIVE') AND EXISTS(SELECT 1 FROM oauth_connections WHERE platform_account_id=$1 AND status='ACTIVE')`, job.AccountID, job.TargetID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}

	message := syncErr.Error()
	if len(message) > 500 {
		message = message[:500]
	}
	if _, err = tx.Exec(ctx, `UPDATE sync_runs SET finished_at=now(),outcome='FAILED',records_read=$2,records_written=$3,error_message=$4 WHERE id=$1`, job.RunID, result.RecordsRead, result.RecordsWritten, message); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE sync_targets SET last_error=$2,consecutive_failures=consecutive_failures+1 WHERE id=$1 AND status='ACTIVE'`, job.TargetID, message); err != nil {
		return err
	}
	accountStatus := "ACTIVE"
	oauthStatus := "ACTIVE"
	if isProviderKind(syncErr, providerAuth, providerPermission) {
		accountStatus = "REAUTH_REQUIRED"
		oauthStatus = "REAUTH_REQUIRED"
	}
	if _, err = tx.Exec(ctx, `UPDATE platform_accounts SET last_error=$2,status=$3,updated_at=now() WHERE id=$1 AND status<>'DISCONNECTED' AND EXISTS(SELECT 1 FROM sync_targets WHERE id=$4 AND status='ACTIVE') AND EXISTS(SELECT 1 FROM oauth_connections WHERE platform_account_id=$1 AND status='ACTIVE')`, job.AccountID, message, accountStatus, job.TargetID); err != nil {
		return err
	}
	if oauthStatus != "ACTIVE" {
		if _, err = tx.Exec(ctx, `UPDATE oauth_connections SET status=$2,updated_at=now() WHERE platform_account_id=$1 AND EXISTS(SELECT 1 FROM sync_targets WHERE id=$3 AND status='ACTIVE')`, job.AccountID, oauthStatus, job.TargetID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Server) accessTokenForSync(ctx context.Context, job platformSyncJob) (string, error) {
	if s.envelope == nil {
		return "", fmt.Errorf("token encryption is not configured")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var accessCipher, accessNonce, refreshCipher, refreshNonce []byte
	var expiresAt *time.Time
	var accountMetadata []byte
	err = tx.QueryRow(ctx, `
		SELECT access_token_ciphertext,access_token_nonce,
		       COALESCE(refresh_token_ciphertext,''::bytea),COALESCE(refresh_token_nonce,''::bytea),expires_at,a.metadata
		FROM oauth_connections c JOIN platform_accounts a ON a.id=c.platform_account_id
		WHERE c.platform_account_id=$1 AND c.organization_id=$2 AND c.status='ACTIVE'
		FOR UPDATE
	`, job.AccountID, job.OrganizationID).Scan(&accessCipher, &accessNonce, &refreshCipher, &refreshNonce, &expiresAt, &accountMetadata)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", &providerError{Platform: job.Platform, Kind: providerAuth, Message: "authorization is missing or inactive"}
		}
		return "", err
	}
	accessPlain, err := s.envelope.Decrypt(accessCipher, accessNonce)
	if err != nil {
		return "", fmt.Errorf("decrypt access token: %w", err)
	}

	refreshBefore := 10 * time.Minute
	if job.Platform == "INSTAGRAM" {
		refreshBefore = 7 * 24 * time.Hour
	}
	if expiresAt == nil || expiresAt.After(time.Now().Add(refreshBefore)) {
		if err = tx.Commit(ctx); err != nil {
			return "", err
		}
		return string(accessPlain), nil
	}

	var refreshed oauthToken
	var metadata map[string]any
	_ = json.Unmarshal(accountMetadata, &metadata)
	switch job.Platform {
	case "YOUTUBE":
		if len(refreshCipher) == 0 || len(refreshNonce) == 0 {
			return "", &providerError{Platform: "YouTube", Kind: providerAuth, Message: "refresh token is missing"}
		}
		refreshPlain, decryptErr := s.envelope.Decrypt(refreshCipher, refreshNonce)
		if decryptErr != nil {
			return "", decryptErr
		}
		refreshed, err = s.refreshYouTubeAccessToken(ctx, string(refreshPlain))
	case "INSTAGRAM":
		if metadata["connectionMode"] == "FACEBOOK" {
			if len(refreshCipher) == 0 || len(refreshNonce) == 0 {
				return "", &providerError{Platform: "Instagram", Kind: providerAuth, Message: "Facebook user authorization is missing; reconnect the account"}
			}
			userAccessToken, decryptErr := s.envelope.Decrypt(refreshCipher, refreshNonce)
			if decryptErr != nil {
				return "", decryptErr
			}
			pageID, _ := metadata["facebookPageId"].(string)
			if pageID == "" {
				return "", &providerError{Platform: "Instagram", Kind: providerAuth, Message: "Facebook Page identity is missing; reconnect the account"}
			}
			refreshed, err = s.refreshInstagramFacebookAccessToken(ctx, string(userAccessToken), pageID, job.ExternalID)
		} else {
			refreshed, err = s.refreshInstagramAccessToken(ctx, string(accessPlain))
		}
	case "TIKTOK":
		if len(refreshCipher) == 0 || len(refreshNonce) == 0 {
			return "", &providerError{Platform: "TikTok", Kind: providerAuth, Message: "refresh token is missing"}
		}
		refreshPlain, decryptErr := s.envelope.Decrypt(refreshCipher, refreshNonce)
		if decryptErr != nil {
			return "", decryptErr
		}
		var token tiktokTokenData
		token, err = s.refreshTikTokToken(ctx, string(refreshPlain))
		refreshed = oauthToken{AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, ExpiresIn: token.ExpiresIn, Scopes: splitScopes(token.Scope, nil)}
	case "VK":
		if len(refreshCipher) == 0 || len(refreshNonce) == 0 {
			return "", &providerError{Platform: "VK", Kind: providerAuth, Message: "refresh token is missing"}
		}
		refreshPlain, decryptErr := s.envelope.Decrypt(refreshCipher, refreshNonce)
		if decryptErr != nil {
			return "", decryptErr
		}
		deviceID, _ := metadata["deviceId"].(string)
		refreshed, err = s.refreshVKAccessToken(ctx, string(refreshPlain), deviceID)
	default:
		return "", &providerError{Platform: job.Platform, Kind: providerAuth, Message: "authorization expired"}
	}
	if err != nil {
		_, _ = tx.Exec(ctx, `UPDATE oauth_connections SET status='REAUTH_REQUIRED',updated_at=now() WHERE platform_account_id=$1`, job.AccountID)
		_ = tx.Commit(ctx)
		if isProviderKind(err, providerAuth, providerPermission) {
			return "", err
		}
		return "", &providerError{Platform: job.Platform, Kind: providerAuth, Message: "token refresh failed"}
	}
	if refreshed.AccessToken == "" {
		return "", &providerError{Platform: job.Platform, Kind: providerAuth, Message: "token refresh returned an empty token"}
	}
	newAccess, newAccessNonce, err := s.envelope.Encrypt([]byte(refreshed.AccessToken))
	if err != nil {
		return "", err
	}
	newRefresh, newRefreshNonce := refreshCipher, refreshNonce
	if refreshed.RefreshToken != "" {
		newRefresh, newRefreshNonce, err = s.envelope.Encrypt([]byte(refreshed.RefreshToken))
		if err != nil {
			return "", err
		}
	}
	var refreshedExpiry *time.Time
	if refreshed.ExpiresIn > 0 {
		value := time.Now().Add(time.Duration(refreshed.ExpiresIn) * time.Second)
		refreshedExpiry = &value
	}
	scopes := refreshed.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	_, err = tx.Exec(ctx, `
		UPDATE oauth_connections
		SET access_token_ciphertext=$2,access_token_nonce=$3,
		    refresh_token_ciphertext=$4,refresh_token_nonce=$5,
		    expires_at=$6,last_refreshed_at=now(),
		    scopes=CASE WHEN cardinality($7::text[])>0 THEN $7 ELSE scopes END,
		    status='ACTIVE',updated_at=now()
		WHERE platform_account_id=$1
	`, job.AccountID, newAccess, newAccessNonce, newRefresh, newRefreshNonce, refreshedExpiry, scopes)
	if err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return refreshed.AccessToken, nil
}

func (s *Server) refreshYouTubeAccessToken(ctx context.Context, refreshToken string) (oauthToken, error) {
	form := url.Values{
		"client_id":     {s.config.YouTubeClientID},
		"client_secret": {s.config.YouTubeClientSecret},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Scope       string `json:"scope"`
	}
	body := strings.NewReader(form.Encode())
	err := newProviderClient("YouTube").JSON(ctx, http.MethodPost, strings.TrimRight(s.config.YouTubeOAuthBase, "/")+"/token", "", "application/x-www-form-urlencoded", body, &out)
	if err != nil {
		return oauthToken{}, err
	}
	return oauthToken{AccessToken: out.AccessToken, ExpiresIn: out.ExpiresIn, Scopes: splitScopes(out.Scope, nil)}, nil
}

func (s *Server) refreshInstagramAccessToken(ctx context.Context, accessToken string) (oauthToken, error) {
	endpoint := strings.TrimRight(s.config.InstagramAPIBase, "/") + "/refresh_access_token?" + url.Values{
		"grant_type":   {"ig_refresh_token"},
		"access_token": {accessToken},
	}.Encode()
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	err := newProviderClient("Instagram").JSON(ctx, http.MethodGet, endpoint, "", "", nil, &out)
	if err != nil {
		return oauthToken{}, err
	}
	return oauthToken{AccessToken: out.AccessToken, ExpiresIn: out.ExpiresIn}, nil
}

func (s *Server) refreshInstagramFacebookAccessToken(ctx context.Context, userAccessToken, pageID, instagramAccountID string) (oauthToken, error) {
	endpoint := strings.TrimRight(s.config.InstagramFacebookGraphAPIBase, "/") + "/oauth/access_token?" + url.Values{
		"grant_type": {"fb_exchange_token"}, "client_id": {s.config.InstagramFacebookClientID}, "client_secret": {s.config.InstagramFacebookClientSecret}, "fb_exchange_token": {userAccessToken},
	}.Encode()
	var userToken struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := doJSON(ctx, http.MethodGet, endpoint, "", &userToken); err != nil || userToken.AccessToken == "" {
		return oauthToken{}, fmt.Errorf("Facebook token refresh failed")
	}
	pageURL := strings.TrimRight(s.config.InstagramFacebookGraphAPIBase, "/") + "/" + url.PathEscape(pageID) + "?" + url.Values{
		"fields":       {"access_token,instagram_business_account"},
		"access_token": {userToken.AccessToken},
	}.Encode()
	var page facebookPage
	if err := doJSON(ctx, http.MethodGet, pageURL, "", &page); err != nil || page.AccessToken == "" {
		return oauthToken{}, fmt.Errorf("Facebook Page token refresh failed")
	}
	if page.InstagramBusinessAccount.ID == "" || page.InstagramBusinessAccount.ID != instagramAccountID {
		return oauthToken{}, fmt.Errorf("Facebook Page is no longer linked to the connected Instagram account")
	}
	return oauthToken{AccessToken: page.AccessToken, RefreshToken: userToken.AccessToken, ExpiresIn: userToken.ExpiresIn}, nil
}

func (s *Server) requestAccountSync(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	accountID := chi.URLParam(r, "id")
	tag, err := s.pool.Exec(r.Context(), `
		UPDATE sync_targets target SET next_sync_at=now(),status='ACTIVE',last_error=NULL
		WHERE target.target_id=$1 AND target.organization_id=$2
		  AND EXISTS(
			SELECT 1 FROM platform_accounts account
			JOIN oauth_connections oauth ON oauth.platform_account_id=account.id AND oauth.organization_id=account.organization_id
			WHERE account.id=target.target_id AND account.organization_id=target.organization_id
			  AND account.status='ACTIVE' AND oauth.status='ACTIVE'
		  )
	`, accountID, p.OrganizationID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "sync request failed", "could not queue synchronization")
		return
	}
	if tag.RowsAffected() == 0 {
		problem(w, http.StatusNotFound, "connection not found", "platform account has no synchronization target")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accountId": accountID, "status": "QUEUED"})
}

func (s *Server) pausePlatformAccount(w http.ResponseWriter, r *http.Request) {
	s.setPlatformAccountPaused(w, r, true)
}

func (s *Server) resumePlatformAccount(w http.ResponseWriter, r *http.Request) {
	s.setPlatformAccountPaused(w, r, false)
}

func (s *Server) setPlatformAccountPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	p := r.Context().Value(principalKey).(principal)
	accountID := chi.URLParam(r, "id")
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "account update failed", "could not start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	accountStatus, targetStatus := "ACTIVE", "ACTIVE"
	if paused {
		accountStatus, targetStatus = "PAUSED", "PAUSED"
	}
	tag, err := tx.Exec(r.Context(), `UPDATE platform_accounts SET status=$3,updated_at=now() WHERE id=$1 AND organization_id=$2 AND status<>'DISCONNECTED'`, accountID, p.OrganizationID, accountStatus)
	if err != nil || tag.RowsAffected() == 0 {
		problem(w, http.StatusNotFound, "connection not found", "platform account does not exist")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE sync_targets SET status=$3,next_sync_at=CASE WHEN $3='ACTIVE' THEN now() ELSE next_sync_at END WHERE target_id=$1 AND organization_id=$2`, accountID, p.OrganizationID, targetStatus); err != nil {
		problem(w, http.StatusInternalServerError, "account update failed", "could not update synchronization state")
		return
	}
	action := "RESUME_PLATFORM_ACCOUNT"
	if paused {
		action = "PAUSE_PLATFORM_ACCOUNT"
	}
	metadata, _ := json.Marshal(map[string]string{"accountId": accountID})
	_, _ = tx.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,entity_id,metadata) VALUES($1,$2,$3,'PLATFORM_ACCOUNT',$4,$5::jsonb)`, p.OrganizationID, p.ID, action, accountID, string(metadata))
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "account update failed", "could not commit account state")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func parseInt64(value string) *int64 {
	if value == "" {
		return nil
	}
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil
	}
	return &number
}
