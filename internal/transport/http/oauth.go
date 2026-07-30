package httpserver

import (
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
	"github.com/statzavod/statzavod/internal/platforms"
)

type oauthProvider struct {
	ID           string
	Name         string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	AuthorizeURL string
	Scopes       []string
	UsePKCE      bool
}

type oauthToken struct {
	AccessToken  string
	RefreshToken string
	Scopes       []string
	ExpiresIn    int64
	ExternalID   string
}

type platformProfile struct {
	ExternalID  string
	Username    string
	DisplayName string
	ProfileURL  string
	AvatarURL   string
	AccountType string
	Metadata    map[string]any
}

func (s *Server) oauthProviders() map[string]oauthProvider {
	return map[string]oauthProvider{
		"youtube": {
			ID: "YOUTUBE", Name: "YouTube", ClientID: s.config.YouTubeClientID, ClientSecret: s.config.YouTubeClientSecret,
			RedirectURL: s.config.YouTubeRedirectURL, AuthorizeURL: "https://accounts.google.com/o/oauth2/v2/auth",
			Scopes:  []string{"https://www.googleapis.com/auth/youtube.readonly", "https://www.googleapis.com/auth/yt-analytics.readonly"},
			UsePKCE: true,
		},
		"instagram": {
			ID: "INSTAGRAM", Name: "Instagram", ClientID: s.config.InstagramClientID, ClientSecret: s.config.InstagramClientSecret,
			RedirectURL: s.config.InstagramRedirectURL, AuthorizeURL: strings.TrimRight(s.config.InstagramOAuthBase, "/") + "/oauth/authorize",
			Scopes: []string{"instagram_business_basic", "instagram_business_manage_insights"},
		},
		"tiktok": {
			ID: "TIKTOK", Name: "TikTok", ClientID: s.config.TikTokClientKey, ClientSecret: s.config.TikTokClientSecret,
			RedirectURL: s.config.TikTokRedirectURL, AuthorizeURL: "https://www.tiktok.com/v2/auth/authorize/",
			Scopes: strings.Split(tiktokScopes, ","),
		},
		"vk": {
			ID: "VK", Name: "VK", ClientID: s.config.VKClientID, ClientSecret: s.config.VKClientSecret,
			RedirectURL: s.config.VKRedirectURL, AuthorizeURL: strings.TrimRight(s.config.VKOAuthBase, "/") + "/authorize",
			Scopes: []string{"video", "stats", "offline"},
		},
	}
}

func (s *Server) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.oauthProviders()[strings.ToLower(chi.URLParam(r, "platform"))]
	if !ok {
		problem(w, http.StatusNotFound, "platform not found", "supported platforms are YouTube, Instagram, TikTok and VK")
		return
	}
	if s.envelope == nil || provider.ClientID == "" || provider.ClientSecret == "" || provider.RedirectURL == "" {
		problem(w, http.StatusServiceUnavailable, provider.Name+" is not configured", "server OAuth credentials are missing")
		return
	}
	p := r.Context().Value(principalKey).(principal)
	creatorID := chi.URLParam(r, "id")
	var exists bool
	if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM creators WHERE id=$1 AND organization_id=$2 AND status='ACTIVE')`, creatorID, p.OrganizationID).Scan(&exists); err != nil || !exists {
		problem(w, http.StatusNotFound, "creator not found", "creator does not exist in this organization")
		return
	}

	state, verifier, challenge, err := platforms.NewPKCE()
	if err != nil {
		problem(w, http.StatusInternalServerError, "OAuth state creation failed", "could not create a secure authorization state")
		return
	}
	encryptedVerifier, nonce, err := s.envelope.Encrypt([]byte(verifier))
	if err != nil {
		problem(w, http.StatusInternalServerError, "OAuth state creation failed", "could not protect the authorization state")
		return
	}
	hash := sha256.Sum256([]byte(state))
	_, err = s.pool.Exec(r.Context(), `INSERT INTO oauth_states(organization_id,creator_id,platform,state_hash,pkce_verifier_ciphertext,nonce,expires_at,initiated_by) VALUES($1,$2,$3,$4,$5,$6,now()+interval '10 minutes',$7)`, p.OrganizationID, creatorID, provider.ID, hash[:], encryptedVerifier, nonce, p.ID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "OAuth state creation failed", "could not start "+provider.Name+" authorization")
		return
	}

	q := url.Values{
		"redirect_uri":  {provider.RedirectURL},
		"response_type": {"code"},
		"scope":         {strings.Join(provider.Scopes, " ")},
		"state":         {state},
	}
	if provider.ID == "TIKTOK" {
		q.Set("client_key", provider.ClientID)
		q.Set("scope", strings.Join(provider.Scopes, ","))
	} else {
		q.Set("client_id", provider.ClientID)
	}
	switch provider.ID {
	case "YOUTUBE":
		q.Set("access_type", "offline")
		q.Set("include_granted_scopes", "true")
		q.Set("prompt", "consent")
	case "INSTAGRAM":
		q.Set("scope", strings.Join(provider.Scopes, ","))
	case "VK":
		q.Set("display", "page")
		q.Set("v", s.config.VKAPIVersion)
	}
	if provider.UsePKCE {
		q.Set("code_challenge", challenge)
		q.Set("code_challenge_method", "S256")
	}
	writeJSON(w, http.StatusOK, map[string]any{"authorizationUrl": provider.AuthorizeURL + "?" + q.Encode(), "expiresAt": time.Now().Add(10 * time.Minute)})
}

func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	platformKey := strings.ToLower(chi.URLParam(r, "platform"))
	provider, ok := s.oauthProviders()[platformKey]
	if !ok {
		s.redirectToApp(w, r, "/login?oauth=unknown-platform")
		return
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		s.redirectToApp(w, r, "/login?oauth="+platformKey+"-state")
		return
	}
	hash := sha256.Sum256([]byte(state))
	var organizationID, creatorID string
	var encryptedVerifier, nonce []byte
	err := s.pool.QueryRow(r.Context(), `UPDATE oauth_states SET consumed_at=now() WHERE state_hash=$1 AND platform=$2 AND consumed_at IS NULL AND expires_at>now() RETURNING organization_id,creator_id,pkce_verifier_ciphertext,nonce`, hash[:], provider.ID).Scan(&organizationID, &creatorID, &encryptedVerifier, &nonce)
	if err != nil {
		s.redirectToApp(w, r, "/login?oauth="+platformKey+"-expired")
		return
	}
	redirect := func(result string) {
		s.redirectToApp(w, r, "/app/creators/"+creatorID+"?platform="+url.QueryEscape(platformKey)+"&oauth="+url.QueryEscape(result))
	}
	if r.URL.Query().Get("error") != "" {
		redirect("denied")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		redirect("missing-code")
		return
	}
	if s.envelope == nil {
		redirect("server-error")
		return
	}
	verifier, err := s.envelope.Decrypt(encryptedVerifier, nonce)
	if err != nil {
		redirect("state-error")
		return
	}
	token, profile, err := s.completeOAuth(r.Context(), provider, code, string(verifier))
	if err != nil {
		redirect("provider-error")
		return
	}
	if err = s.savePlatformConnection(r.Context(), organizationID, creatorID, provider, token, profile); err != nil {
		redirect("save-error")
		return
	}
	redirect("connected")
}

func (s *Server) redirectToApp(w http.ResponseWriter, r *http.Request, path string) {
	http.Redirect(w, r, strings.TrimRight(s.config.PublicBaseURL, "/")+path, http.StatusFound)
}

func (s *Server) completeOAuth(ctx context.Context, provider oauthProvider, code, verifier string) (oauthToken, platformProfile, error) {
	switch provider.ID {
	case "TIKTOK":
		token, err := s.exchangeTikTokCode(ctx, code)
		if err != nil {
			return oauthToken{}, platformProfile{}, err
		}
		user, err := s.fetchTikTokUser(ctx, token.AccessToken)
		if err != nil || user.OpenID == "" {
			return oauthToken{}, platformProfile{}, fmt.Errorf("TikTok profile is unavailable")
		}
		return oauthToken{AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, Scopes: splitScopes(token.Scope, provider.Scopes), ExpiresIn: token.ExpiresIn, ExternalID: token.OpenID},
			platformProfile{ExternalID: user.OpenID, Username: user.DisplayName, DisplayName: user.DisplayName, ProfileURL: "https://www.tiktok.com/@" + user.DisplayName, AvatarURL: user.AvatarURL, AccountType: "CREATOR", Metadata: map[string]any{"followerCount": user.FollowerCount, "followingCount": user.FollowingCount, "likesCount": user.LikesCount, "videoCount": user.VideoCount}}, nil
	case "YOUTUBE":
		return s.completeYouTubeOAuth(ctx, provider, code, verifier)
	case "INSTAGRAM":
		return s.completeInstagramOAuth(ctx, provider, code)
	case "VK":
		return s.completeVKOAuth(ctx, provider, code)
	default:
		return oauthToken{}, platformProfile{}, fmt.Errorf("unsupported OAuth provider")
	}
}

func (s *Server) completeYouTubeOAuth(ctx context.Context, provider oauthProvider, code, verifier string) (oauthToken, platformProfile, error) {
	form := url.Values{"client_id": {provider.ClientID}, "client_secret": {provider.ClientSecret}, "code": {code}, "grant_type": {"authorization_code"}, "redirect_uri": {provider.RedirectURL}, "code_verifier": {verifier}}
	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := doOAuthForm(ctx, strings.TrimRight(s.config.YouTubeOAuthBase, "/")+"/token", form, &raw); err != nil || raw.AccessToken == "" {
		return oauthToken{}, platformProfile{}, fmt.Errorf("YouTube token exchange failed")
	}
	var channels struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title       string `json:"title"`
				CustomURL   string `json:"customUrl"`
				Description string `json:"description"`
				Thumbnails  map[string]struct {
					URL string `json:"url"`
				} `json:"thumbnails"`
			} `json:"snippet"`
		} `json:"items"`
	}
	endpoint := strings.TrimRight(s.config.YouTubeAPIBase, "/") + "/channels?part=snippet&mine=true"
	if err := doBearerJSON(ctx, endpoint, raw.AccessToken, &channels); err != nil || len(channels.Items) == 0 {
		return oauthToken{}, platformProfile{}, fmt.Errorf("YouTube channel is unavailable")
	}
	channel := channels.Items[0]
	username := strings.TrimPrefix(channel.Snippet.CustomURL, "@")
	if username == "" {
		username = channel.ID
	}
	avatar := ""
	if thumbnail, ok := channel.Snippet.Thumbnails["high"]; ok {
		avatar = thumbnail.URL
	} else if thumbnail, ok := channel.Snippet.Thumbnails["default"]; ok {
		avatar = thumbnail.URL
	}
	token := oauthToken{AccessToken: raw.AccessToken, RefreshToken: raw.RefreshToken, Scopes: splitScopes(raw.Scope, provider.Scopes), ExpiresIn: raw.ExpiresIn}
	profile := platformProfile{ExternalID: channel.ID, Username: username, DisplayName: channel.Snippet.Title, ProfileURL: "https://www.youtube.com/channel/" + channel.ID, AvatarURL: avatar, AccountType: "CHANNEL", Metadata: map[string]any{"description": channel.Snippet.Description}}
	return token, profile, nil
}

func (s *Server) completeInstagramOAuth(ctx context.Context, provider oauthProvider, code string) (oauthToken, platformProfile, error) {
	form := url.Values{"client_id": {provider.ClientID}, "client_secret": {provider.ClientSecret}, "code": {code}, "grant_type": {"authorization_code"}, "redirect_uri": {provider.RedirectURL}}
	var short struct {
		AccessToken string `json:"access_token"`
		UserID      int64  `json:"user_id"`
	}
	if err := doOAuthForm(ctx, strings.TrimRight(s.config.InstagramTokenBase, "/")+"/oauth/access_token", form, &short); err != nil || short.AccessToken == "" {
		return oauthToken{}, platformProfile{}, fmt.Errorf("Instagram token exchange failed")
	}
	accessToken, expiresIn := short.AccessToken, int64(3600)
	longURL := strings.TrimRight(s.config.InstagramAPIBase, "/") + "/access_token?" + url.Values{"grant_type": {"ig_exchange_token"}, "client_secret": {provider.ClientSecret}, "access_token": {short.AccessToken}}.Encode()
	var long struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := doJSON(ctx, http.MethodGet, longURL, "", &long); err == nil && long.AccessToken != "" {
		accessToken, expiresIn = long.AccessToken, long.ExpiresIn
	}
	var user struct {
		UserID            string `json:"user_id"`
		ID                string `json:"id"`
		Username          string `json:"username"`
		Name              string `json:"name"`
		ProfilePictureURL string `json:"profile_picture_url"`
		AccountType       string `json:"account_type"`
	}
	profileURL := strings.TrimRight(s.config.InstagramAPIBase, "/") + "/me?" + url.Values{"fields": {"user_id,username,name,profile_picture_url,account_type"}, "access_token": {accessToken}}.Encode()
	if err := doJSON(ctx, http.MethodGet, profileURL, "", &user); err != nil {
		return oauthToken{}, platformProfile{}, fmt.Errorf("Instagram profile is unavailable")
	}
	externalID := user.UserID
	if externalID == "" {
		externalID = user.ID
	}
	if externalID == "" && short.UserID != 0 {
		externalID = strconv.FormatInt(short.UserID, 10)
	}
	if externalID == "" || user.Username == "" {
		return oauthToken{}, platformProfile{}, fmt.Errorf("Instagram profile did not include an account id")
	}
	displayName := user.Name
	if displayName == "" {
		displayName = user.Username
	}
	return oauthToken{AccessToken: accessToken, Scopes: provider.Scopes, ExpiresIn: expiresIn},
		platformProfile{ExternalID: externalID, Username: user.Username, DisplayName: displayName, ProfileURL: "https://www.instagram.com/" + user.Username + "/", AvatarURL: user.ProfilePictureURL, AccountType: user.AccountType}, nil
}

func (s *Server) completeVKOAuth(ctx context.Context, provider oauthProvider, code string) (oauthToken, platformProfile, error) {
	tokenURL := strings.TrimRight(s.config.VKOAuthBase, "/") + "/access_token?" + url.Values{"client_id": {provider.ClientID}, "client_secret": {provider.ClientSecret}, "redirect_uri": {provider.RedirectURL}, "code": {code}}.Encode()
	var raw struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		UserID      int64  `json:"user_id"`
	}
	if err := doJSON(ctx, http.MethodGet, tokenURL, "", &raw); err != nil || raw.AccessToken == "" || raw.UserID == 0 {
		return oauthToken{}, platformProfile{}, fmt.Errorf("VK token exchange failed")
	}
	var users struct {
		Response []struct {
			ID         int64  `json:"id"`
			FirstName  string `json:"first_name"`
			LastName   string `json:"last_name"`
			ScreenName string `json:"screen_name"`
			Photo      string `json:"photo_200"`
		} `json:"response"`
	}
	profileURL := strings.TrimRight(s.config.VKAPIBase, "/") + "/method/users.get?" + url.Values{"access_token": {raw.AccessToken}, "fields": {"screen_name,photo_200"}, "v": {s.config.VKAPIVersion}}.Encode()
	if err := doJSON(ctx, http.MethodGet, profileURL, "", &users); err != nil || len(users.Response) == 0 {
		return oauthToken{}, platformProfile{}, fmt.Errorf("VK profile is unavailable")
	}
	user := users.Response[0]
	username := user.ScreenName
	if username == "" {
		username = "id" + strconv.FormatInt(user.ID, 10)
	}
	return oauthToken{AccessToken: raw.AccessToken, Scopes: provider.Scopes, ExpiresIn: raw.ExpiresIn},
		platformProfile{ExternalID: strconv.FormatInt(user.ID, 10), Username: username, DisplayName: strings.TrimSpace(user.FirstName + " " + user.LastName), ProfileURL: "https://vk.ru/" + username, AvatarURL: user.Photo, AccountType: "CREATOR"}, nil
}

func (s *Server) savePlatformConnection(ctx context.Context, organizationID, creatorID string, provider oauthProvider, token oauthToken, profile platformProfile) error {
	if profile.ExternalID == "" || token.AccessToken == "" || s.envelope == nil {
		return fmt.Errorf("incomplete platform connection")
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
	metadata, _ := json.Marshal(profile.Metadata)
	if len(metadata) == 0 || string(metadata) == "null" {
		metadata = []byte("{}")
	}
	var expiresAt *time.Time
	if token.ExpiresIn > 0 {
		value := time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
		expiresAt = &value
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var accountID string
	err = tx.QueryRow(ctx, `INSERT INTO platform_accounts(organization_id,platform,external_id,username,display_name,profile_url,avatar_url,account_type,status,metadata,last_synced_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'ACTIVE',$9::jsonb,NULL) ON CONFLICT(organization_id,platform,external_id) DO UPDATE SET username=excluded.username,display_name=excluded.display_name,profile_url=excluded.profile_url,avatar_url=excluded.avatar_url,account_type=excluded.account_type,status='ACTIVE',metadata=excluded.metadata,last_error=NULL,updated_at=now() RETURNING id`, organizationID, provider.ID, profile.ExternalID, profile.Username, profile.DisplayName, profile.ProfileURL, profile.AvatarURL, profile.AccountType, string(metadata)).Scan(&accountID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE creator_account_assignments SET valid_to=now() WHERE platform_account_id=$1 AND valid_to IS NULL AND creator_id<>$2`, accountID, creatorID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO creator_account_assignments(creator_id,platform_account_id) SELECT $1,$2 WHERE NOT EXISTS(SELECT 1 FROM creator_account_assignments WHERE creator_id=$1 AND platform_account_id=$2 AND valid_to IS NULL)`, creatorID, accountID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO oauth_connections(organization_id,platform_account_id,access_token_ciphertext,refresh_token_ciphertext,nonce,access_token_nonce,refresh_token_nonce,scopes,expires_at,last_refreshed_at,status) VALUES($1,$2,$3,$4,$5,$5,$6,$7,$8,now(),'ACTIVE') ON CONFLICT(platform_account_id) DO UPDATE SET access_token_ciphertext=excluded.access_token_ciphertext,refresh_token_ciphertext=excluded.refresh_token_ciphertext,nonce=excluded.nonce,access_token_nonce=excluded.access_token_nonce,refresh_token_nonce=excluded.refresh_token_nonce,scopes=excluded.scopes,expires_at=excluded.expires_at,last_refreshed_at=now(),status='ACTIVE',disconnect_requested_at=NULL,purge_after=NULL,updated_at=now()`, organizationID, accountID, access, refresh, accessNonce, refreshNonce, token.Scopes, expiresAt)
	if err != nil {
		return err
	}
	if provider.ID == "YOUTUBE" || provider.ID == "INSTAGRAM" || provider.ID == "TIKTOK" {
		if _, err = tx.Exec(ctx, `INSERT INTO sync_targets(organization_id,target_type,target_id,operation,cadence,next_sync_at,status) VALUES($1,'PLATFORM_ACCOUNT',$2,$3,interval '6 hours',now(),'ACTIVE') ON CONFLICT(organization_id,target_id,operation) WHERE status='ACTIVE' DO UPDATE SET next_sync_at=now(),status='ACTIVE',last_error=NULL`, organizationID, accountID, provider.ID+"_IMPORT"); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_logs(organization_id,action,entity_type,entity_id,metadata) VALUES($1,$2,'PLATFORM_ACCOUNT',$3,jsonb_build_object('scopes',$4::text[]))`, organizationID, "CONNECT_"+provider.ID, accountID, token.Scopes)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Server) platformConnections(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	creatorID := chi.URLParam(r, "id")
	rows, err := s.pool.Query(r.Context(), `SELECT a.id,a.platform,a.username,a.display_name,a.status,COALESCE(a.avatar_url,''),COALESCE(a.profile_url,''),COALESCE(c.scopes,'{}'),a.last_synced_at,COALESCE(c.status,'') FROM platform_accounts a JOIN creator_account_assignments x ON x.platform_account_id=a.id AND x.valid_to IS NULL LEFT JOIN oauth_connections c ON c.platform_account_id=a.id WHERE x.creator_id=$1 AND a.organization_id=$2 ORDER BY a.platform,a.created_at DESC`, creatorID, p.OrganizationID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "connections failed", "could not load platform connections")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, platform, username, display, status, avatar, profileURL, oauthStatus string
		var scopes []string
		var synced *time.Time
		if err := rows.Scan(&id, &platform, &username, &display, &status, &avatar, &profileURL, &scopes, &synced, &oauthStatus); err != nil {
			problem(w, http.StatusInternalServerError, "connections failed", "could not read platform connection")
			return
		}
		items = append(items, map[string]any{"id": id, "platform": platform, "username": username, "displayName": display, "status": status, "oauthStatus": oauthStatus, "avatarUrl": avatar, "profileUrl": profileURL, "scopes": scopes, "lastSyncedAt": synced})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) integrationStatus(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	counts := map[string]int64{}
	rows, err := s.pool.Query(r.Context(), `SELECT platform,count(*) FROM platform_accounts WHERE organization_id=$1 AND status='ACTIVE' GROUP BY platform`, p.OrganizationID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var platform string
			var count int64
			if rows.Scan(&platform, &count) == nil {
				counts[platform] = count
			}
		}
	}
	items := make([]map[string]any, 0, 4)
	for _, key := range []string{"youtube", "instagram", "tiktok", "vk"} {
		provider := s.oauthProviders()[key]
		items = append(items, map[string]any{"id": provider.ID, "name": provider.Name, "configured": s.envelope != nil && provider.ClientID != "" && provider.ClientSecret != "" && provider.RedirectURL != "", "connectedAccounts": counts[provider.ID]})
	}

	connectionRows, err := s.pool.Query(r.Context(), `
		SELECT
			a.id,
			a.platform,
			a.username,
			a.display_name,
			a.status,
			COALESCE(a.profile_url, ''),
			a.last_synced_at,
			COALESCE(a.last_error, ''),
			cr.id,
			cr.display_name,
			COALESCE(o.status, ''),
			o.expires_at,
			COALESCE(st.consecutive_failures, 0),
			st.last_success_at
		FROM platform_accounts a
		JOIN creator_account_assignments ca
			ON ca.platform_account_id = a.id
			AND ca.valid_to IS NULL
		JOIN creators cr ON cr.id = ca.creator_id
		LEFT JOIN oauth_connections o ON o.platform_account_id = a.id
		LEFT JOIN LATERAL (
			SELECT consecutive_failures, last_success_at
			FROM sync_targets
			WHERE target_id = a.id
				AND organization_id = a.organization_id
			ORDER BY next_sync_at DESC
			LIMIT 1
		) st ON true
		WHERE a.organization_id = $1
		ORDER BY
			CASE
				WHEN a.status <> 'ACTIVE' OR COALESCE(o.status, '') <> 'ACTIVE' OR a.last_error <> '' THEN 0
				WHEN o.expires_at IS NOT NULL AND o.expires_at <= now() + interval '7 days' THEN 1
				ELSE 2
			END,
			cr.display_name,
			a.platform
	`, p.OrganizationID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "sync dashboard failed", "could not load connection health")
		return
	}
	defer connectionRows.Close()

	accounts := make([]map[string]any, 0)
	for connectionRows.Next() {
		var id, platform, username, displayName, accountStatus, profileURL string
		var lastSyncedAt, expiresAt, lastSuccessAt *time.Time
		var lastError, creatorID, creatorName, oauthStatus string
		var consecutiveFailures int
		if err := connectionRows.Scan(
			&id,
			&platform,
			&username,
			&displayName,
			&accountStatus,
			&profileURL,
			&lastSyncedAt,
			&lastError,
			&creatorID,
			&creatorName,
			&oauthStatus,
			&expiresAt,
			&consecutiveFailures,
			&lastSuccessAt,
		); err != nil {
			problem(w, http.StatusInternalServerError, "sync dashboard failed", "could not read connection health")
			return
		}

		health, message := "HEALTHY", "Синхронизация работает"
		now := time.Now()
		switch {
		case accountStatus != "ACTIVE":
			health, message = "ERROR", "Аккаунт требует повторного подключения"
		case oauthStatus == "":
			health, message = "ERROR", "Нет активного OAuth-подключения"
		case oauthStatus != "ACTIVE":
			health, message = "ERROR", "Авторизация недействительна"
		case expiresAt != nil && !expiresAt.After(now):
			health, message = "ERROR", "Токен истёк"
		case lastError != "":
			health, message = "ERROR", lastError
		case consecutiveFailures > 0:
			health, message = "ERROR", fmt.Sprintf("Ошибок синхронизации подряд: %d", consecutiveFailures)
		case expiresAt != nil && expiresAt.Before(now.Add(7*24*time.Hour)):
			health, message = "WARNING", "Срок действия токена скоро закончится"
		case lastSyncedAt == nil:
			health, message = "PENDING", "Ожидает первой синхронизации"
		}

		accounts = append(accounts, map[string]any{
			"id":                  id,
			"platform":            platform,
			"username":            username,
			"displayName":         displayName,
			"profileUrl":          profileURL,
			"creatorId":           creatorID,
			"creatorName":         creatorName,
			"accountStatus":       accountStatus,
			"oauthStatus":         oauthStatus,
			"health":              health,
			"message":             message,
			"lastSyncedAt":        lastSyncedAt,
			"tokenExpiresAt":      expiresAt,
			"consecutiveFailures": consecutiveFailures,
			"lastSuccessAt":       lastSuccessAt,
		})
	}
	if err := connectionRows.Err(); err != nil {
		problem(w, http.StatusInternalServerError, "sync dashboard failed", "could not finish reading connection health")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items, "accounts": accounts})
}

func (s *Server) disconnectPlatform(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	accountID := chi.URLParam(r, "id")
	var platform string
	var accessCipher, accessNonce []byte
	err := s.pool.QueryRow(r.Context(), `SELECT a.platform,c.access_token_ciphertext,c.access_token_nonce FROM oauth_connections c JOIN platform_accounts a ON a.id=c.platform_account_id WHERE a.id=$1 AND a.organization_id=$2`, accountID, p.OrganizationID).Scan(&platform, &accessCipher, &accessNonce)
	if err != nil {
		problem(w, http.StatusNotFound, "connection not found", "platform connection does not exist")
		return
	}
	if s.envelope != nil {
		if token, decryptErr := s.envelope.Decrypt(accessCipher, accessNonce); decryptErr == nil {
			_ = s.revokePlatform(r.Context(), platform, string(token))
		}
	}
	_, _ = s.pool.Exec(r.Context(), `DELETE FROM oauth_connections WHERE platform_account_id=$1`, accountID)
	_, _ = s.pool.Exec(r.Context(), `UPDATE platform_accounts SET status='DISCONNECTED',updated_at=now() WHERE id=$1 AND organization_id=$2`, accountID, p.OrganizationID)
	_, _ = s.pool.Exec(r.Context(), `UPDATE sync_targets SET status='PAUSED' WHERE target_id=$1 AND organization_id=$2`, accountID, p.OrganizationID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) revokePlatform(ctx context.Context, platform, accessToken string) error {
	switch platform {
	case "TIKTOK":
		return s.revokeTikTok(ctx, accessToken)
	case "YOUTUBE":
		return doJSON(ctx, http.MethodPost, strings.TrimRight(s.config.YouTubeOAuthBase, "/")+"/revoke?"+url.Values{"token": {accessToken}}.Encode(), "", &map[string]any{})
	case "INSTAGRAM":
		return doJSON(ctx, http.MethodDelete, strings.TrimRight(s.config.InstagramAPIBase, "/")+"/me/permissions?"+url.Values{"access_token": {accessToken}}.Encode(), "", &map[string]any{})
	default:
		return nil
	}
}

func (s *Server) purgePlatformData(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	accountID := chi.URLParam(r, "id")
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "deletion failed", "could not start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	var platform string
	if err = tx.QueryRow(r.Context(), `SELECT platform FROM platform_accounts WHERE id=$1 AND organization_id=$2`, accountID, p.OrganizationID).Scan(&platform); err != nil {
		problem(w, http.StatusNotFound, "connection not found", "platform connection does not exist")
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM sync_targets WHERE target_id=$1 AND organization_id=$2`, accountID, p.OrganizationID); err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM creator_account_assignments WHERE platform_account_id=$1`, accountID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM publications WHERE platform_account_id=$1 AND organization_id=$2`, accountID, p.OrganizationID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM platform_accounts WHERE id=$1 AND organization_id=$2`, accountID, p.OrganizationID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,metadata) VALUES($1,$2,$3,'PLATFORM_ACCOUNT',jsonb_build_object('deletedAccount',$4))`, p.OrganizationID, p.ID, "PURGE_"+platform+"_DATA", accountID)
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		problem(w, http.StatusInternalServerError, "deletion failed", "platform data could not be removed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func splitScopes(value string, fallback []string) []string {
	scopes := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' })
	if len(scopes) == 0 {
		return fallback
	}
	return scopes
}

func doOAuthForm(ctx context.Context, endpoint string, form url.Values, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return doRequestJSON(req, target)
}

func doBearerJSON(ctx context.Context, endpoint, token string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return doRequestJSON(req, target)
}

func doJSON(ctx context.Context, method, endpoint, bearer string, target any) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return doRequestJSON(req, target)
}

func doRequestJSON(req *http.Request, target any) error {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("OAuth provider returned %s", resp.Status)
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil && err != io.EOF {
		return err
	}
	return nil
}
