package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

const tiktokScopes = "user.info.basic,user.info.profile,user.info.stats,video.list,video.publish"

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
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "OAuth state creation failed", "could not start authorization")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = lockActiveCreatorCompanyTarget(r.Context(), tx, p.OrganizationID, creatorID); err != nil {
		problem(w, http.StatusNotFound, "creator not found", "creator does not exist in this organization")
		return
	}
	reconnect, err := beginCreatorOAuthReconnectAuthorization(r.Context(), tx, p.OrganizationID, creatorID, "TIKTOK")
	if err != nil {
		problem(w, http.StatusConflict, "OAuth reconnect failed", "could not establish a unique reconnect authorization")
		return
	}
	state := makeToken()
	hash := sha256.Sum256([]byte(state))
	var reconnectAccountID any
	var authorizationGeneration any
	if reconnect != nil {
		reconnectAccountID = reconnect.AccountID
		authorizationGeneration = reconnect.Generation
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO oauth_states(organization_id,creator_id,platform,state_hash,expires_at,initiated_by,reconnect_platform_account_id,authorization_generation) VALUES($1,$2,'TIKTOK',$3,now()+interval '10 minutes',$4,$5,$6)`, p.OrganizationID, creatorID, hash[:], p.ID, reconnectAccountID, authorizationGeneration)
	if err != nil {
		problem(w, 500, "OAuth state creation failed", "could not start TikTok authorization")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "OAuth state creation failed", "could not commit authorization state")
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
	var reconnectAccountID *string
	var authorizationGeneration *int64
	err := s.pool.QueryRow(r.Context(), `UPDATE oauth_states SET consumed_at=now() WHERE state_hash=$1 AND platform='TIKTOK' AND consumed_at IS NULL AND expires_at>now() RETURNING organization_id,creator_id,reconnect_platform_account_id,authorization_generation`, hash[:]).Scan(&organizationID, &creatorID, &reconnectAccountID, &authorizationGeneration)
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
	profileReconnect := (*oauthReconnectAuthorization)(nil)
	if reconnectAccountID != nil && authorizationGeneration != nil {
		profileReconnect = &oauthReconnectAuthorization{AccountID: *reconnectAccountID, Generation: *authorizationGeneration}
	}
	if err := s.saveTikTokConnectionAuthorized(r.Context(), organizationID, creatorID, token, user, profileReconnect); err != nil {
		if errors.Is(err, errOAuthTargetUnavailable) {
			http.Redirect(w, r, "/app/creators/"+creatorID+"?tiktok=permission-revoked", http.StatusFound)
			return
		}
		if errors.Is(err, errOAuthAuthorizationSuperseded) || errors.Is(err, errOAuthReconnectIdentityMismatch) {
			http.Redirect(w, r, "/app/creators/"+creatorID+"?tiktok=superseded", http.StatusFound)
			return
		}
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
	return s.saveTikTokConnectionAuthorized(ctx, organizationID, creatorID, token, user, nil)
}

func (s *Server) saveTikTokConnectionAuthorized(ctx context.Context, organizationID, creatorID string, token tiktokTokenData, user tiktokUser, reconnect *oauthReconnectAuthorization) error {
	// The requested authorization scopes are not evidence that TikTok granted
	// them. In particular, a token response without scope must remain an empty
	// grant set so publishing preflight requires a reconnect instead of falsely
	// reporting Direct Post readiness.
	scopes := splitScopes(token.Scope, nil)
	provider := oauthProvider{ID: "TIKTOK"}
	profile := platformProfile{
		ExternalID: user.OpenID, Username: tiktokUsername(user), DisplayName: user.DisplayName,
		ProfileURL: tiktokProfileURL(user), AvatarURL: user.AvatarURL, AccountType: "CREATOR",
		Metadata: map[string]any{
			"followerCount": user.FollowerCount, "followingCount": user.FollowingCount,
			"likesCount": user.LikesCount, "videoCount": user.VideoCount,
			"bioDescription": user.BioDescription, "isVerified": user.IsVerified,
		},
	}
	setOAuthReconnectAuthorization(&profile, reconnect)
	return s.savePlatformConnection(ctx, organizationID, creatorID, provider, oauthToken{
		AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, Scopes: scopes,
		ExpiresIn: token.ExpiresIn, ExternalID: token.OpenID,
	}, profile)
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
	if err := rows.Err(); err != nil {
		problem(w, http.StatusInternalServerError, "connections failed", "could not finish reading TikTok connections")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) disconnectTikTok(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	accountID := chi.URLParam(r, "id")
	var accessCipher, accessNonce []byte
	var companyID string
	err := s.pool.QueryRow(r.Context(), `SELECT c.access_token_ciphertext,c.access_token_nonce,a.company_id::text FROM oauth_connections c JOIN platform_accounts a ON a.id=c.platform_account_id WHERE a.id=$1 AND a.organization_id=$2`, accountID, p.OrganizationID).Scan(&accessCipher, &accessNonce, &companyID)
	if err != nil {
		problem(w, 404, "connection not found", "TikTok connection does not exist")
		return
	}
	if s.envelope != nil {
		if token, decryptErr := s.envelope.Decrypt(accessCipher, accessNonce); decryptErr == nil {
			_ = s.revokeTikTok(r.Context(), string(token))
		}
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "disconnection failed", "could not start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `SELECT id FROM platform_accounts WHERE id=$1 AND organization_id=$2 FOR UPDATE`, accountID, p.OrganizationID); err != nil {
		problem(w, http.StatusInternalServerError, "disconnection failed", "could not lock TikTok account")
		return
	}
	if err = cancelContentPublishesForAccountReassignmentTx(r.Context(), tx, p.OrganizationID, accountID, ""); err != nil {
		problem(w, http.StatusInternalServerError, "disconnection failed", "could not cancel queued publications")
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM oauth_connections connection USING platform_accounts account WHERE connection.platform_account_id=account.id AND account.id=$1 AND account.organization_id=$2`, accountID, p.OrganizationID); err != nil {
		problem(w, http.StatusInternalServerError, "disconnection failed", "could not delete TikTok authorization")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE platform_accounts SET status='DISCONNECTED',updated_at=now() WHERE id=$1 AND organization_id=$2`, accountID, p.OrganizationID); err != nil {
		problem(w, http.StatusInternalServerError, "disconnection failed", "could not update TikTok account")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE sync_targets SET status='PAUSED' WHERE target_id=$1 AND organization_id=$2`, accountID, p.OrganizationID); err != nil {
		problem(w, http.StatusInternalServerError, "disconnection failed", "could not pause TikTok synchronization")
		return
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &companyID, "DISCONNECT_TIKTOK", "PLATFORM_ACCOUNT", &accountID, http.StatusNoContent, map[string]any{})); err != nil {
		problem(w, http.StatusInternalServerError, "disconnection failed", "could not write audit record")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "disconnection failed", "could not commit TikTok disconnection")
		return
	}
	markResponseAuditCommitted(w)
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
	var companyID string
	if err = tx.QueryRow(r.Context(), `SELECT company_id::text FROM platform_accounts WHERE id=$1 AND organization_id=$2 AND platform='TIKTOK' FOR UPDATE`, accountID, p.OrganizationID).Scan(&companyID); err != nil {
		problem(w, 404, "connection not found", "TikTok connection does not exist")
		return
	}
	_, err = tx.Exec(r.Context(), `DELETE FROM sync_runs WHERE target_id IN (SELECT id FROM sync_targets WHERE target_id=$1 AND organization_id=$2)`, accountID, p.OrganizationID)
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM sync_targets WHERE target_id=$1 AND organization_id=$2`, accountID, p.OrganizationID)
	}
	if err == nil {
		err = cancelContentPublishesForAccountReassignmentTx(r.Context(), tx, p.OrganizationID, accountID, "")
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
		err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &companyID, "PURGE_TIKTOK_DATA", "PLATFORM_ACCOUNT", &accountID, http.StatusNoContent, map[string]any{"deletedAccount": accountID}))
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		problem(w, 500, "deletion failed", "TikTok data could not be removed")
		return
	}
	markResponseAuditCommitted(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) doTikTokJSON(req *http.Request, target any) error {
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: rejectProviderRedirect}
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
		if code, detail, _ := tikTokAPIError(envelope.Error, envelope.ErrorDescription, envelope.LogID); code != "" {
			_ = detail // Provider text is classification input only; it is never exposed.
			if classified := classifyTikTokError(code); classified != providerPermanent {
				kind = classified
			}
		}
		return &providerError{Platform: "TikTok", Kind: kind, StatusCode: resp.StatusCode, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")), Message: safeTikTokProviderMessage(kind)}
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
	if code, _, _ := tikTokAPIError(envelope.Error, envelope.ErrorDescription, envelope.LogID); code != "" {
		kind := classifyTikTokError(code)
		return &providerError{Platform: "TikTok", Kind: kind, Message: safeTikTokProviderMessage(kind)}
	}
	if target == nil {
		return nil
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode TikTok API payload: %w", err)
	}
	return nil
}

// safeTikTokProviderMessage is deliberately closed over an internal enum.
// TikTok error_description, fail_reason, log_id and arbitrary payload fields
// can contain URLs, bearer tokens, signed queries or opaque account IDs. They
// are useful for classification but must never cross the provider boundary.
func safeTikTokProviderMessage(kind providerErrorKind) string {
	switch kind {
	case providerAuth:
		return "TikTok authorization is invalid or expired"
	case providerPermission:
		return "TikTok permission is missing"
	case providerRateLimit:
		return "TikTok rate limit was reached"
	case providerRetryable:
		return "TikTok is temporarily unavailable"
	case providerSchema:
		return "TikTok returned an unexpected response"
	default:
		return "TikTok rejected the request"
	}
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

// RunTikTokSync processes one due TikTok account. New workers use
// RunPlatformSync; this compatibility entry point retains the same persistence
// lifecycle gate for callers that still invoke the provider-specific method.
func (s *Server) RunTikTokSync(ctx context.Context) error {
	if !s.tiktokConfigured() {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var job platformSyncJob
	err = tx.QueryRow(ctx, `
		SELECT target.id,target.target_id,target.organization_id,target.company_id,
		       assignment.creator_id::text,account.external_id
		FROM sync_targets target
		JOIN platform_accounts account ON account.id=target.target_id AND account.organization_id=target.organization_id
		JOIN creator_account_assignments assignment ON assignment.platform_account_id=account.id AND assignment.valid_to IS NULL
		JOIN companies company ON company.id=target.company_id AND company.organization_id=target.organization_id
		JOIN organizations workspace ON workspace.id=target.organization_id
		WHERE target.operation='TIKTOK_IMPORT' AND target.status='ACTIVE'
		  AND NOT target.suspended_for_company_archive AND target.next_sync_at<=now()
		  AND account.status='ACTIVE' AND company.archived_at IS NULL
		  AND workspace.lifecycle_state='ACTIVE'
		ORDER BY target.next_sync_at
		FOR UPDATE OF target SKIP LOCKED LIMIT 1
	`).Scan(&job.TargetID, &job.AccountID, &job.OrganizationID, &job.CompanyID, &job.CreatorID, &job.ExternalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	job.Platform = "TIKTOK"
	if _, err = tx.Exec(ctx, `UPDATE sync_targets SET next_sync_at=now()+cadence WHERE id=$1`, job.TargetID); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	plain, _, err := s.accessTokenForSync(ctx, job)
	if err != nil {
		return err
	}
	// TikTok users can change their username, display name, avatar, or profile
	// link without reconnecting OAuth. Refresh those fields on every import so
	// links shown in the application do not become stale.
	user, err := s.fetchTikTokUser(ctx, plain)
	if err != nil {
		return err
	}
	if err := s.refreshTikTokProfile(ctx, job, user); err != nil {
		return err
	}
	videos, err := s.fetchTikTokVideos(ctx, plain)
	if err != nil {
		return err
	}
	for _, video := range videos {
		if err := s.upsertTikTokVideo(ctx, job, video); err != nil {
			return err
		}
	}
	finishTx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer finishTx.Rollback(ctx)
	if err = lockActivePlatformSyncJob(ctx, finishTx, job); err != nil {
		return err
	}
	if _, err = finishTx.Exec(ctx, `
		UPDATE platform_accounts a
		SET last_synced_at=now(),last_error=NULL,status='ACTIVE'
		WHERE a.id=$1 AND a.organization_id=$2 AND a.status='ACTIVE'
		  AND EXISTS(SELECT 1 FROM oauth_connections c WHERE c.platform_account_id=a.id AND c.status='ACTIVE')
	`, job.AccountID, job.OrganizationID); err != nil {
		return err
	}
	return finishTx.Commit(ctx)
}

func (s *Server) refreshTikTokProfile(ctx context.Context, job platformSyncJob, user tiktokUser) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockActivePlatformSyncJob(ctx, tx, job); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		UPDATE platform_accounts a
		SET username=$3, display_name=$4, profile_url=$5, avatar_url=$6,
			metadata=$7::jsonb, updated_at=now()
		WHERE a.id=$1 AND a.organization_id=$2 AND a.platform='TIKTOK'
			AND a.status='ACTIVE'
			AND EXISTS(SELECT 1 FROM oauth_connections c WHERE c.platform_account_id=a.id AND c.status='ACTIVE')
	`, job.AccountID, job.OrganizationID, tiktokUsername(user), user.DisplayName, tiktokProfileURL(user), user.AvatarURL, tikTokMetadata(user))
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
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
func (s *Server) upsertTikTokVideo(ctx context.Context, job platformSyncJob, v tiktokVideo) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockActivePlatformSyncJob(ctx, tx, job); err != nil {
		return err
	}
	var creatorID string
	if err = tx.QueryRow(ctx, `
		SELECT assignment.creator_id::text
		FROM creator_account_assignments assignment
		JOIN creators creator ON creator.id=assignment.creator_id AND creator.organization_id=assignment.organization_id
		WHERE assignment.platform_account_id=$1 AND assignment.organization_id=$2
		  AND assignment.valid_to IS NULL AND creator.company_id=$3
	`, job.AccountID, job.OrganizationID, job.CompanyID).Scan(&creatorID); err != nil {
		return err
	}
	published := time.Unix(v.CreateTime, 0).UTC()
	var publicationID string
	err = tx.QueryRow(ctx, `INSERT INTO publications(organization_id,creator_id,platform_account_id,platform,external_id,publication_type,title,description,permalink,thumbnail_url,duration_ms,published_at) VALUES($1,$2,$3,'TIKTOK',$4,'VIDEO',$5,$6,$7,$8,$9,$10) ON CONFLICT(organization_id,platform,external_id) DO UPDATE SET creator_id=excluded.creator_id,platform_account_id=excluded.platform_account_id,title=excluded.title,description=excluded.description,permalink=excluded.permalink,thumbnail_url=excluded.thumbnail_url,published_at=excluded.published_at,status='ACTIVE',updated_at=now() RETURNING id`, job.OrganizationID, creatorID, job.AccountID, v.ID, v.Title, v.Description, v.ShareURL, v.CoverURL, v.Duration*1000, published).Scan(&publicationID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO publication_metric_snapshots(publication_id,views,likes,comments,shares,completeness_status) VALUES($1,$2,$3,$4,$5,'PARTIAL')`, publicationID, v.ViewCount, v.LikeCount, v.CommentCount, v.ShareCount); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
