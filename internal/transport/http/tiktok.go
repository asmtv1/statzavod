package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

const tiktokScopes = "user.info.basic,user.info.profile,user.info.stats,video.list"

type tiktokTokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	OpenID       string `json:"open_id"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

type tiktokUser struct {
	OpenID          string `json:"open_id"`
	DisplayName     string `json:"display_name"`
	AvatarURL       string `json:"avatar_url"`
	Username        string `json:"username"`
	ProfileDeepLink string `json:"profile_deep_link"`
	BioDescription  string `json:"bio_description"`
	IsVerified      bool   `json:"is_verified"`
	FollowerCount   int64  `json:"follower_count"`
	FollowingCount  int64  `json:"following_count"`
	LikesCount      int64  `json:"likes_count"`
	VideoCount      int64  `json:"video_count"`
}

type tiktokVideo struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Description  string `json:"video_description"`
	Duration     int64  `json:"duration"`
	CoverURL     string `json:"cover_image_url"`
	ShareURL     string `json:"share_url"`
	CreateTime   int64  `json:"create_time"`
	LikeCount    int64  `json:"like_count"`
	CommentCount int64  `json:"comment_count"`
	ShareCount   int64  `json:"share_count"`
	ViewCount    int64  `json:"view_count"`
}

func (s *Server) tiktokAuthorize(w http.ResponseWriter, r *http.Request) {
	if !s.tiktokConfigured() {
		problem(w, 503, "TikTok is not configured", "server OAuth credentials are missing")
		return
	}
	p := r.Context().Value(principalKey).(principal)
	creatorID := chi.URLParam(r, "id")
	var exists bool
	if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM creators WHERE id=$1 AND organization_id=$2 AND status='ACTIVE' AND archived_at IS NULL)`, creatorID, p.OrganizationID).Scan(&exists); err != nil || !exists {
		problem(w, 404, "creator not found", "creator does not exist in this organization")
		return
	}
	state := makeToken()
	hash := sha256.Sum256([]byte(state))
	_, err := s.pool.Exec(r.Context(), `INSERT INTO oauth_states(organization_id,creator_id,platform,state_hash,expires_at,initiated_by) VALUES($1,$2,'TIKTOK',$3,now()+interval '10 minutes',$4)`, p.OrganizationID, creatorID, hash[:], p.ID)
	if err != nil {
		problem(w, 500, "OAuth state creation failed", "could not start TikTok authorization")
		return
	}
	q := url.Values{"client_key": {s.config.TikTokClientKey}, "response_type": {"code"}, "scope": {tiktokScopes}, "redirect_uri": {s.config.TikTokRedirectURL}, "state": {state}}
	writeJSON(w, 200, map[string]any{"authorizationUrl": "https://www.tiktok.com/v2/auth/authorize/?" + q.Encode(), "expiresAt": time.Now().Add(10 * time.Minute)})
}

func (s *Server) tiktokCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state == "" {
		http.Redirect(w, r, "/login?oauth=tiktok-state", http.StatusFound)
		return
	}
	hash := sha256.Sum256([]byte(state))
	var organizationID, creatorID string
	err := s.pool.QueryRow(r.Context(), `UPDATE oauth_states SET consumed_at=now() WHERE state_hash=$1 AND platform='TIKTOK' AND consumed_at IS NULL AND expires_at>now() RETURNING organization_id,creator_id`, hash[:]).Scan(&organizationID, &creatorID)
	if err != nil {
		http.Redirect(w, r, "/login?oauth=tiktok-expired", http.StatusFound)
		return
	}
	if denied := r.URL.Query().Get("error"); denied != "" {
		http.Redirect(w, r, "/app/creators/"+creatorID+"?tiktok=denied", http.StatusFound)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Redirect(w, r, "/app/creators/"+creatorID+"?tiktok=missing-code", http.StatusFound)
		return
	}
	token, err := s.exchangeTikTokCode(r.Context(), code)
	if err != nil {
		http.Redirect(w, r, "/app/creators/"+creatorID+"?tiktok=token-error", http.StatusFound)
		return
	}
	user, err := s.fetchTikTokUser(r.Context(), token.AccessToken)
	if err != nil || user.OpenID == "" {
		http.Redirect(w, r, "/app/creators/"+creatorID+"?tiktok=profile-error", http.StatusFound)
		return
	}
	if err := s.saveTikTokConnection(r.Context(), organizationID, creatorID, token, user); err != nil {
		http.Redirect(w, r, "/app/creators/"+creatorID+"?tiktok=save-error", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/app/creators/"+creatorID+"?tiktok=connected", http.StatusFound)
}

func (s *Server) exchangeTikTokCode(ctx context.Context, code string) (tiktokTokenData, error) {
	form := url.Values{"client_key": {s.config.TikTokClientKey}, "client_secret": {s.config.TikTokClientSecret}, "code": {code}, "grant_type": {"authorization_code"}, "redirect_uri": {s.config.TikTokRedirectURL}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.config.TikTokAPIBase, "/")+"/v2/oauth/token/", strings.NewReader(form.Encode()))
	if err != nil {
		return tiktokTokenData{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out tiktokTokenData
	if err := s.doTikTokJSON(req, &out); err != nil {
		return tiktokTokenData{}, err
	}
	if out.AccessToken == "" {
		return tiktokTokenData{}, fmt.Errorf("TikTok token response did not include an access token")
	}
	return out, nil
}

func (s *Server) refreshTikTokToken(ctx context.Context, refreshToken string) (tiktokTokenData, error) {
	form := url.Values{"client_key": {s.config.TikTokClientKey}, "client_secret": {s.config.TikTokClientSecret}, "refresh_token": {refreshToken}, "grant_type": {"refresh_token"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.config.TikTokAPIBase, "/")+"/v2/oauth/token/", strings.NewReader(form.Encode()))
	if err != nil {
		return tiktokTokenData{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out tiktokTokenData
	if err := s.doTikTokJSON(req, &out); err != nil {
		return tiktokTokenData{}, err
	}
	if out.AccessToken == "" {
		return tiktokTokenData{}, fmt.Errorf("TikTok refresh response did not include an access token")
	}
	return out, nil
}

func (s *Server) fetchTikTokUser(ctx context.Context, accessToken string) (tiktokUser, error) {
	endpoint := strings.TrimRight(s.config.TikTokAPIBase, "/") + "/v2/user/info/?fields=open_id,display_name,avatar_url,username,profile_deep_link,bio_description,is_verified,follower_count,following_count,likes_count,video_count"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return tiktokUser{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	var out struct {
		Data struct {
			User tiktokUser `json:"user"`
		} `json:"data"`
	}
	if err := s.doTikTokJSON(req, &out); err != nil {
		return tiktokUser{}, err
	}
	return out.Data.User, nil
}

func (s *Server) saveTikTokConnection(ctx context.Context, organizationID, creatorID string, token tiktokTokenData, user tiktokUser) error {
	if s.envelope == nil {
		return fmt.Errorf("token encryption is not configured")
	}
	access, accessNonce, err := s.envelope.Encrypt([]byte(token.AccessToken))
	if err != nil {
		return err
	}
	var refresh, refreshNonce []byte
	if token.RefreshToken != "" {
		refresh, refreshNonce, err = s.envelope.Encrypt([]byte(token.RefreshToken))
		if err != nil {
			return err
		}
	}
	scopes := strings.FieldsFunc(token.Scope, func(r rune) bool { return r == ',' || r == ' ' })
	if len(scopes) == 0 {
		scopes = strings.Split(tiktokScopes, ",")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var accountID string
	err = tx.QueryRow(ctx, `INSERT INTO platform_accounts(organization_id,platform,external_id,username,display_name,profile_url,avatar_url,account_type,status,metadata,last_synced_at) VALUES($1,'TIKTOK',$2,$3,$4,$5,$6,'CREATOR','ACTIVE',$7::jsonb,now()) ON CONFLICT(organization_id,platform,external_id) DO UPDATE SET username=excluded.username,display_name=excluded.display_name,profile_url=excluded.profile_url,avatar_url=excluded.avatar_url,status='ACTIVE',metadata=excluded.metadata,updated_at=now() RETURNING id`, organizationID, user.OpenID, tiktokUsername(user), user.DisplayName, tiktokProfileURL(user), user.AvatarURL, tikTokMetadata(user)).Scan(&accountID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE creator_account_assignments SET valid_to=now() WHERE platform_account_id=$1 AND valid_to IS NULL AND creator_id<>$2`, accountID, creatorID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO creator_account_assignments(creator_id,platform_account_id) SELECT $1,$2 WHERE NOT EXISTS(SELECT 1 FROM creator_account_assignments WHERE creator_id=$1 AND platform_account_id=$2 AND valid_to IS NULL)`, creatorID, accountID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO oauth_connections(organization_id,platform_account_id,access_token_ciphertext,refresh_token_ciphertext,nonce,access_token_nonce,refresh_token_nonce,scopes,expires_at,last_refreshed_at,status) VALUES($1,$2,$3,$4,$5,$5,$6,$7,now()+($8::text||' seconds')::interval,now(),'ACTIVE') ON CONFLICT(platform_account_id) DO UPDATE SET access_token_ciphertext=excluded.access_token_ciphertext,refresh_token_ciphertext=excluded.refresh_token_ciphertext,access_token_nonce=excluded.access_token_nonce,refresh_token_nonce=excluded.refresh_token_nonce,scopes=excluded.scopes,expires_at=excluded.expires_at,last_refreshed_at=now(),status='ACTIVE',disconnect_requested_at=NULL,purge_after=NULL,updated_at=now()`, organizationID, accountID, access, refresh, accessNonce, refreshNonce, scopes, strconv.FormatInt(token.ExpiresIn, 10))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO sync_targets(organization_id,target_type,target_id,operation,cadence,next_sync_at,status) VALUES($1,'PLATFORM_ACCOUNT',$2,'TIKTOK_IMPORT',interval '6 hours',now(),'ACTIVE') ON CONFLICT DO NOTHING`, organizationID, accountID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_logs(organization_id,action,entity_type,entity_id,metadata) VALUES($1,'CONNECT_TIKTOK','PLATFORM_ACCOUNT',$2,jsonb_build_object('scopes',$3::text[]))`, organizationID, accountID, scopes)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func tikTokMetadata(user tiktokUser) string {
	b, _ := json.Marshal(map[string]any{"followerCount": user.FollowerCount, "followingCount": user.FollowingCount, "likesCount": user.LikesCount, "videoCount": user.VideoCount, "bioDescription": user.BioDescription, "isVerified": user.IsVerified})
	return string(b)
}

func tiktokUsername(user tiktokUser) string {
	if user.Username != "" {
		return user.Username
	}
	return user.DisplayName
}

func tiktokProfileURL(user tiktokUser) string {
	if user.ProfileDeepLink != "" {
		return user.ProfileDeepLink
	}
	return "https://www.tiktok.com/@" + tiktokUsername(user)
}

func (s *Server) tiktokConnections(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	creatorID := chi.URLParam(r, "id")
	rows, err := s.pool.Query(r.Context(), `SELECT a.id,a.username,a.display_name,a.status,COALESCE(a.avatar_url,''),COALESCE(c.scopes,'{}'),c.last_refreshed_at,COALESCE(c.status,'') FROM platform_accounts a JOIN creator_account_assignments x ON x.platform_account_id=a.id AND x.valid_to IS NULL LEFT JOIN oauth_connections c ON c.platform_account_id=a.id WHERE x.creator_id=$1 AND a.organization_id=$2 AND a.platform='TIKTOK' AND a.status<>'DISCONNECTED' ORDER BY a.created_at DESC`, creatorID, p.OrganizationID)
	if err != nil {
		problem(w, 500, "connections failed", "could not load TikTok connections")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, username, display, status, avatar, oauthStatus string
		var scopes []string
		var refreshed *time.Time
		if err := rows.Scan(&id, &username, &display, &status, &avatar, &scopes, &refreshed, &oauthStatus); err != nil {
			problem(w, 500, "connections failed", "could not read TikTok connection")
			return
		}
		items = append(items, map[string]any{"id": id, "platform": "TIKTOK", "username": username, "displayName": display, "status": status, "oauthStatus": oauthStatus, "avatarUrl": avatar, "scopes": scopes, "lastSyncedAt": refreshed})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) disconnectTikTok(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	accountID := chi.URLParam(r, "id")
	var accessCipher, accessNonce []byte
	err := s.pool.QueryRow(r.Context(), `SELECT c.access_token_ciphertext,c.access_token_nonce FROM oauth_connections c JOIN platform_accounts a ON a.id=c.platform_account_id WHERE a.id=$1 AND a.organization_id=$2`, accountID, p.OrganizationID).Scan(&accessCipher, &accessNonce)
	if err != nil {
		problem(w, 404, "connection not found", "TikTok connection does not exist")
		return
	}
	if s.envelope != nil {
		if token, decryptErr := s.envelope.Decrypt(accessCipher, accessNonce); decryptErr == nil {
			_ = s.revokeTikTok(r.Context(), string(token))
		}
	}
	_, _ = s.pool.Exec(r.Context(), `DELETE FROM oauth_connections WHERE platform_account_id=$1`, accountID)
	_, _ = s.pool.Exec(r.Context(), `UPDATE platform_accounts SET status='DISCONNECTED',updated_at=now() WHERE id=$1 AND organization_id=$2`, accountID, p.OrganizationID)
	_, _ = s.pool.Exec(r.Context(), `UPDATE sync_targets SET status='PAUSED' WHERE target_id=$1 AND organization_id=$2`, accountID, p.OrganizationID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) purgeTikTokData(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	accountID := chi.URLParam(r, "id")
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, 500, "deletion failed", "could not start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	var found bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM platform_accounts WHERE id=$1 AND organization_id=$2 AND platform='TIKTOK')`, accountID, p.OrganizationID).Scan(&found); err != nil || !found {
		problem(w, 404, "connection not found", "TikTok connection does not exist")
		return
	}
	_, err = tx.Exec(r.Context(), `DELETE FROM sync_runs WHERE target_id IN (SELECT id FROM sync_targets WHERE target_id=$1 AND organization_id=$2)`, accountID, p.OrganizationID)
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM sync_targets WHERE target_id=$1 AND organization_id=$2`, accountID, p.OrganizationID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM creator_account_assignments WHERE platform_account_id=$1`, accountID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM publications WHERE platform_account_id=$1 AND organization_id=$2`, accountID, p.OrganizationID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM platform_accounts WHERE id=$1 AND organization_id=$2`, accountID, p.OrganizationID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,metadata) VALUES($1,$2,'PURGE_TIKTOK_DATA','PLATFORM_ACCOUNT',jsonb_build_object('deletedAccount',$3::text))`, p.OrganizationID, p.ID, accountID)
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		problem(w, 500, "deletion failed", "TikTok data could not be removed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) doTikTokJSON(req *http.Request, target any) error {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return &providerError{Platform: "TikTok", Kind: providerRetryable, Message: "network request failed"}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return &providerError{Platform: "TikTok", Kind: providerRetryable, Message: "could not read API response"}
	}
	var envelope struct {
		Error            json.RawMessage `json:"error"`
		ErrorDescription string          `json:"error_description"`
		LogID            string          `json:"log_id"`
	}
	envelopeErr := json.Unmarshal(body, &envelope)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		kind := providerPermanent
		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			kind = providerAuth
		case resp.StatusCode == http.StatusForbidden:
			kind = providerPermission
		case resp.StatusCode == http.StatusTooManyRequests:
			kind = providerRateLimit
		case resp.StatusCode >= 500:
			kind = providerRetryable
		}
		message := resp.Status
		if code, detail, _ := tikTokAPIError(envelope.Error, envelope.ErrorDescription, envelope.LogID); code != "" {
			message = code + ": " + detail
			if classified := classifyTikTokError(code); classified != providerPermanent {
				kind = classified
			}
		}
		return &providerError{Platform: "TikTok", Kind: kind, StatusCode: resp.StatusCode, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")), Message: message}
	}
	if len(bytes.TrimSpace(body)) == 0 {
		if target == nil {
			return nil
		}
		return fmt.Errorf("decode TikTok API response: empty response body")
	}
	if envelopeErr != nil {
		return fmt.Errorf("decode TikTok API response: %w", envelopeErr)
	}
	if code, message, logID := tikTokAPIError(envelope.Error, envelope.ErrorDescription, envelope.LogID); code != "" {
		if logID != "" {
			message = fmt.Sprintf("%s (log_id %s)", message, logID)
		}
		return &providerError{Platform: "TikTok", Kind: classifyTikTokError(code), Message: code + ": " + message}
	}
	if target == nil {
		return nil
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode TikTok API payload: %w", err)
	}
	return nil
}

func classifyTikTokError(code string) providerErrorKind {
	value := strings.ToLower(strings.TrimSpace(code))
	switch {
	case value == "invalid_grant",
		strings.Contains(value, "token") && (strings.Contains(value, "invalid") || strings.Contains(value, "expired") || strings.Contains(value, "revoked")):
		return providerAuth
	case strings.Contains(value, "scope") || strings.Contains(value, "permission"):
		return providerPermission
	case strings.Contains(value, "rate") || strings.Contains(value, "too_many"):
		return providerRateLimit
	case strings.Contains(value, "server") || strings.Contains(value, "internal") || strings.Contains(value, "temporar"):
		return providerRetryable
	default:
		return providerPermanent
	}
}

func tikTokAPIError(raw json.RawMessage, description, logID string) (string, string, string) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", "", ""
	}
	var code string
	if err := json.Unmarshal(raw, &code); err == nil {
		code = strings.TrimSpace(code)
		if code == "" || strings.EqualFold(code, "ok") || code == "0" {
			return "", "", ""
		}
		message := strings.TrimSpace(description)
		if message == "" {
			message = "unspecified provider error"
		}
		return code, message, strings.TrimSpace(logID)
	}
	var detail struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		LogID   string `json:"log_id"`
	}
	if err := json.Unmarshal(raw, &detail); err != nil {
		return "invalid_response", "TikTok returned an unrecognized error response", strings.TrimSpace(logID)
	}
	code = strings.TrimSpace(detail.Code)
	if code == "" || strings.EqualFold(code, "ok") || code == "0" {
		return "", "", ""
	}
	message := strings.TrimSpace(detail.Message)
	if message == "" {
		message = strings.TrimSpace(description)
	}
	if message == "" {
		message = "unspecified provider error"
	}
	if strings.TrimSpace(detail.LogID) != "" {
		logID = detail.LogID
	}
	return code, message, strings.TrimSpace(logID)
}
func (s *Server) revokeTikTok(ctx context.Context, accessToken string) error {
	form := url.Values{"client_key": {s.config.TikTokClientKey}, "client_secret": {s.config.TikTokClientSecret}, "token": {accessToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.config.TikTokAPIBase, "/")+"/v2/oauth/revoke/", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// TikTok documents a successful revocation as an empty response body. A nil
	// target enables that response while retaining TikTok's JSON error parsing.
	return s.doTikTokJSON(req, nil)
}
func (s *Server) tiktokConfigured() bool {
	return s.envelope != nil && s.config.TikTokClientKey != "" && s.config.TikTokClientSecret != ""
}

// RunTikTokSync processes one due TikTok account. It is called by the worker and is intentionally idempotent.
func (s *Server) RunTikTokSync(ctx context.Context) error {
	if !s.tiktokConfigured() {
		return nil
	}
	var targetID, accountID, organizationID string
	err := s.pool.QueryRow(ctx, `SELECT id,target_id,organization_id FROM sync_targets WHERE operation='TIKTOK_IMPORT' AND status='ACTIVE' AND next_sync_at<=now() ORDER BY next_sync_at FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&targetID, &accountID, &organizationID)
	if err != nil {
		return nil
	}
	_, _ = s.pool.Exec(ctx, `UPDATE sync_targets SET next_sync_at=now()+cadence WHERE id=$1`, targetID)
	var ciphertext, nonce, refreshCiphertext, refreshNonce []byte
	var expiresAt *time.Time
	err = s.pool.QueryRow(ctx, `SELECT access_token_ciphertext,access_token_nonce,COALESCE(refresh_token_ciphertext,''::bytea),COALESCE(refresh_token_nonce,''::bytea),expires_at FROM oauth_connections WHERE platform_account_id=$1 AND organization_id=$2 AND status='ACTIVE'`, accountID, organizationID).Scan(&ciphertext, &nonce, &refreshCiphertext, &refreshNonce, &expiresAt)
	if err != nil {
		return err
	}
	plain, err := s.envelope.Decrypt(ciphertext, nonce)
	if err != nil {
		return err
	}
	if expiresAt != nil && expiresAt.Before(time.Now().Add(5*time.Minute)) {
		if len(refreshCiphertext) == 0 || len(refreshNonce) == 0 {
			return fmt.Errorf("TikTok authorization expired and has no refresh token")
		}
		refreshPlain, decryptErr := s.envelope.Decrypt(refreshCiphertext, refreshNonce)
		if decryptErr != nil {
			return decryptErr
		}
		refreshed, refreshErr := s.refreshTikTokToken(ctx, string(refreshPlain))
		if refreshErr != nil {
			return refreshErr
		}
		newAccess, newAccessNonce, encryptErr := s.envelope.Encrypt([]byte(refreshed.AccessToken))
		if encryptErr != nil {
			return encryptErr
		}
		newRefresh, newRefreshNonce := refreshCiphertext, refreshNonce
		if refreshed.RefreshToken != "" {
			newRefresh, newRefreshNonce, encryptErr = s.envelope.Encrypt([]byte(refreshed.RefreshToken))
			if encryptErr != nil {
				return encryptErr
			}
		}
		_, updateErr := s.pool.Exec(ctx, `UPDATE oauth_connections SET access_token_ciphertext=$2,access_token_nonce=$3,refresh_token_ciphertext=$4,refresh_token_nonce=$5,expires_at=now()+($6::text||' seconds')::interval,last_refreshed_at=now(),updated_at=now() WHERE platform_account_id=$1`, accountID, newAccess, newAccessNonce, newRefresh, newRefreshNonce, strconv.FormatInt(refreshed.ExpiresIn, 10))
		if updateErr != nil {
			return updateErr
		}
		plain = []byte(refreshed.AccessToken)
	}
	// TikTok users can change their username, display name, avatar, or profile
	// link without reconnecting OAuth. Refresh those fields on every import so
	// links shown in the application do not become stale.
	user, err := s.fetchTikTokUser(ctx, string(plain))
	if err != nil {
		return err
	}
	if err := s.refreshTikTokProfile(ctx, organizationID, accountID, user); err != nil {
		return err
	}
	videos, err := s.fetchTikTokVideos(ctx, string(plain))
	if err != nil {
		return err
	}
	for _, video := range videos {
		if err := s.upsertTikTokVideo(ctx, organizationID, accountID, video); err != nil {
			return err
		}
	}
	// A disconnect can race with a sync that already decrypted its token. Never
	// let that in-flight run reactivate the local account after OAuth cleanup.
	_, _ = s.pool.Exec(ctx, `
		UPDATE platform_accounts a
		SET last_synced_at=now(),last_error=NULL,status='ACTIVE'
		WHERE a.id=$1 AND a.status<>'DISCONNECTED'
		  AND EXISTS(SELECT 1 FROM oauth_connections c WHERE c.platform_account_id=a.id AND c.status='ACTIVE')
	`, accountID)
	return nil
}

func (s *Server) refreshTikTokProfile(ctx context.Context, organizationID, accountID string, user tiktokUser) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE platform_accounts a
		SET username=$3, display_name=$4, profile_url=$5, avatar_url=$6,
			metadata=$7::jsonb, updated_at=now()
		WHERE a.id=$1 AND a.organization_id=$2 AND a.platform='TIKTOK'
			AND a.status<>'DISCONNECTED'
			AND EXISTS(SELECT 1 FROM oauth_connections c WHERE c.platform_account_id=a.id AND c.status='ACTIVE')
	`, accountID, organizationID, tiktokUsername(user), user.DisplayName, tiktokProfileURL(user), user.AvatarURL, tikTokMetadata(user))
	return err
}

func (s *Server) fetchTikTokVideos(ctx context.Context, accessToken string) ([]tiktokVideo, error) {
	endpoint := strings.TrimRight(s.config.TikTokAPIBase, "/") + "/v2/video/list/?fields=id,title,video_description,duration,cover_image_url,share_url,create_time,like_count,comment_count,share_count,view_count"
	body, _ := json.Marshal(map[string]any{"max_count": 20})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	var out struct {
		Data struct {
			Videos []tiktokVideo `json:"videos"`
		} `json:"data"`
	}
	if err = s.doTikTokJSON(req, &out); err != nil {
		return nil, err
	}
	return out.Data.Videos, nil
}
func (s *Server) upsertTikTokVideo(ctx context.Context, orgID, accountID string, v tiktokVideo) error {
	var creatorID string
	if err := s.pool.QueryRow(ctx, `SELECT creator_id FROM creator_account_assignments WHERE platform_account_id=$1 AND valid_to IS NULL`, accountID).Scan(&creatorID); err != nil {
		return err
	}
	published := time.Unix(v.CreateTime, 0).UTC()
	var publicationID string
	err := s.pool.QueryRow(ctx, `INSERT INTO publications(organization_id,creator_id,platform_account_id,platform,external_id,publication_type,title,description,permalink,thumbnail_url,duration_ms,published_at) VALUES($1,$2,$3,'TIKTOK',$4,'VIDEO',$5,$6,$7,$8,$9,$10) ON CONFLICT(organization_id,platform,external_id) DO UPDATE SET title=excluded.title,description=excluded.description,permalink=excluded.permalink,thumbnail_url=excluded.thumbnail_url,updated_at=now() RETURNING id`, orgID, creatorID, accountID, v.ID, v.Title, v.Description, v.ShareURL, v.CoverURL, v.Duration*1000, published).Scan(&publicationID)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO publication_metric_snapshots(publication_id,views,likes,comments,shares,completeness_status) VALUES($1,$2,$3,$4,$5,'PARTIAL')`, publicationID, v.ViewCount, v.LikeCount, v.CommentCount, v.ShareCount)
	return err
}
