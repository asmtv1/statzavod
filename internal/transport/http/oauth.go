package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
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
	Flow         string
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
		"instagram-facebook": {
			ID: "INSTAGRAM", Name: "Instagram через Facebook", ClientID: s.config.InstagramFacebookClientID, ClientSecret: s.config.InstagramFacebookClientSecret,
			RedirectURL: s.config.InstagramFacebookRedirectURL, AuthorizeURL: strings.TrimRight(s.config.InstagramFacebookOAuthBase, "/") + "/dialog/oauth",
			Scopes: []string{"instagram_basic", "instagram_manage_insights", "pages_show_list", "pages_read_engagement"}, Flow: "FACEBOOK",
		},
		"tiktok": {
			ID: "TIKTOK", Name: "TikTok", ClientID: s.config.TikTokClientKey, ClientSecret: s.config.TikTokClientSecret,
			RedirectURL: s.config.TikTokRedirectURL, AuthorizeURL: "https://www.tiktok.com/v2/auth/authorize/",
			Scopes: strings.Split(tiktokScopes, ","),
		},
		"vk": {
			ID: "VK", Name: "VK", ClientID: s.config.VKClientID, ClientSecret: s.config.VKClientSecret,
			RedirectURL: s.config.VKRedirectURL, AuthorizeURL: strings.TrimRight(s.config.VKOAuthBase, "/") + "/authorize",
			Scopes: []string{"video", "stats", "offline"}, UsePKCE: true,
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
	if provider.Flow == "FACEBOOK" && s.config.InstagramFacebookConfigID == "" {
		problem(w, http.StatusServiceUnavailable, provider.Name+" is not configured", "Facebook Login for Business configuration is missing")
		return
	}
	p := r.Context().Value(principalKey).(principal)
	creatorID := chi.URLParam(r, "id")
	if provider.ID == "VK" {
		problem(w, http.StatusBadRequest, "VK is connected at company level", "connect the shared VK account from the company page")
		return
	}
	var exists bool
	if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM creators WHERE id=$1 AND organization_id=$2 AND status='ACTIVE' AND archived_at IS NULL)`, creatorID, p.OrganizationID).Scan(&exists); err != nil || !exists {
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
		q.Set("scope", strings.Join(provider.Scopes, " "))
		q.Set("access_type", "offline")
		q.Set("include_granted_scopes", "true")
		q.Set("prompt", "consent")
	case "INSTAGRAM":
		if provider.Flow == "FACEBOOK" {
			q.Set("config_id", s.config.InstagramFacebookConfigID)
			q.Set("scope", strings.Join(provider.Scopes, ","))
			q.Set("override_default_response_type", "true")
			q.Set("auth_type", "rerequest")
		} else {
			q.Set("scope", strings.Join(provider.Scopes, ","))
		}
	case "VK":
		q.Set("scope", strings.Join(provider.Scopes, " "))
	}
	if provider.UsePKCE {
		q.Set("code_challenge", challenge)
		q.Set("code_challenge_method", "S256")
	}
	writeJSON(w, http.StatusOK, map[string]any{"authorizationUrl": provider.AuthorizeURL + "?" + q.Encode(), "expiresAt": time.Now().Add(10 * time.Minute)})
}

func (s *Server) companyVKOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	provider := s.oauthProviders()["vk"]
	if s.envelope == nil || provider.ClientID == "" || provider.ClientSecret == "" || provider.RedirectURL == "" {
		problem(w, http.StatusServiceUnavailable, "VK is not configured", "server OAuth credentials are missing")
		return
	}
	p := r.Context().Value(principalKey).(principal)
	companyID := chi.URLParam(r, "id")
	var companyVKAccountID string
	if err := s.pool.QueryRow(r.Context(), `INSERT INTO company_vk_accounts(organization_id,company_id,created_by,updated_by) SELECT $2,c.id,$3,$3 FROM companies c WHERE c.id=$1 AND c.organization_id=$2 AND c.archived_at IS NULL ON CONFLICT(company_id) DO UPDATE SET updated_by=excluded.updated_by,updated_at=now() RETURNING id`, companyID, p.OrganizationID, p.ID).Scan(&companyVKAccountID); err != nil {
		problem(w, http.StatusBadRequest, "company is unavailable", "could not prepare the shared VK account")
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
	_, err = s.pool.Exec(r.Context(), `INSERT INTO oauth_states(organization_id,creator_id,company_vk_account_id,platform,state_hash,pkce_verifier_ciphertext,nonce,expires_at,initiated_by) VALUES($1,NULL,$2,'VK',$3,$4,$5,now()+interval '10 minutes',$6)`, p.OrganizationID, companyVKAccountID, hash[:], encryptedVerifier, nonce, p.ID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "OAuth state creation failed", "could not start VK authorization")
		return
	}
	q := url.Values{
		"client_id":             {provider.ClientID},
		"redirect_uri":          {provider.RedirectURL},
		"response_type":         {"code"},
		"scope":                 {strings.Join(provider.Scopes, " ")},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
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
	var organizationID string
	var creatorID, companyVKAccountID *string
	var encryptedVerifier, nonce []byte
	err := s.pool.QueryRow(r.Context(), `UPDATE oauth_states SET consumed_at=now() WHERE state_hash=$1 AND platform=$2 AND consumed_at IS NULL AND expires_at>now() RETURNING organization_id,creator_id,company_vk_account_id,pkce_verifier_ciphertext,nonce`, hash[:], provider.ID).Scan(&organizationID, &creatorID, &companyVKAccountID, &encryptedVerifier, &nonce)
	if err != nil {
		s.redirectToApp(w, r, "/login?oauth="+platformKey+"-expired")
		return
	}
	redirect := func(result string) {
		if companyVKAccountID != nil {
			s.redirectToApp(w, r, "/app/companies?platform=vk&oauth="+url.QueryEscape(result))
			return
		}
		if creatorID == nil {
			s.redirectToApp(w, r, "/app/companies?platform=vk&oauth=state-error")
			return
		}
		s.redirectToApp(w, r, "/app/creators/"+*creatorID+"?platform="+url.QueryEscape(platformKey)+"&oauth="+url.QueryEscape(result))
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
	token, profile, err := s.completeOAuth(r.Context(), provider, code, string(verifier), state, r.URL.Query().Get("device_id"))
	if err != nil {
		// Keep provider details out of the browser, but retain them in the server
		// log so an OAuth failure can be diagnosed without replaying a one-time code.
		log.Printf("%s OAuth completion failed: %v", provider.Name, err)
		redirect("provider-error")
		return
	}
	if companyVKAccountID != nil {
		err = s.saveCompanyVKConnection(r.Context(), organizationID, *companyVKAccountID, provider, token, profile)
	} else if creatorID != nil {
		err = s.savePlatformConnection(r.Context(), organizationID, *creatorID, provider, token, profile)
	} else {
		err = fmt.Errorf("OAuth state has no owner")
	}
	if err != nil {
		redirect("save-error")
		return
	}
	redirect("connected")
}

func (s *Server) redirectToApp(w http.ResponseWriter, r *http.Request, path string) {
	http.Redirect(w, r, strings.TrimRight(s.config.PublicBaseURL, "/")+path, http.StatusFound)
}

func (s *Server) completeOAuth(ctx context.Context, provider oauthProvider, code, verifier, state, deviceID string) (oauthToken, platformProfile, error) {
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
			platformProfile{ExternalID: user.OpenID, Username: tiktokUsername(user), DisplayName: user.DisplayName, ProfileURL: tiktokProfileURL(user), AvatarURL: user.AvatarURL, AccountType: "CREATOR", Metadata: map[string]any{"followerCount": user.FollowerCount, "followingCount": user.FollowingCount, "likesCount": user.LikesCount, "videoCount": user.VideoCount, "bioDescription": user.BioDescription, "isVerified": user.IsVerified}}, nil
	case "YOUTUBE":
		return s.completeYouTubeOAuth(ctx, provider, code, verifier)
	case "INSTAGRAM":
		if provider.Flow == "FACEBOOK" {
			return s.completeInstagramFacebookOAuth(ctx, provider, code)
		}
		return s.completeInstagramOAuth(ctx, provider, code)
	case "VK":
		return s.completeVKOAuth(ctx, provider, code, verifier, state, deviceID)
	default:
		return oauthToken{}, platformProfile{}, fmt.Errorf("unsupported OAuth provider")
	}
}

func (s *Server) completeInstagramFacebookOAuth(ctx context.Context, provider oauthProvider, code string) (oauthToken, platformProfile, error) {
	endpoint := strings.TrimRight(s.config.InstagramFacebookGraphAPIBase, "/") + "/oauth/access_token?" + url.Values{
		"client_id": {provider.ClientID}, "client_secret": {provider.ClientSecret}, "redirect_uri": {provider.RedirectURL}, "code": {code},
	}.Encode()
	var token struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := doJSON(ctx, http.MethodGet, endpoint, "", &token); err != nil || token.AccessToken == "" {
		return oauthToken{}, platformProfile{}, fmt.Errorf("Facebook token exchange failed")
	}
	var longToken struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	longURL := strings.TrimRight(s.config.InstagramFacebookGraphAPIBase, "/") + "/oauth/access_token?" + url.Values{
		"grant_type": {"fb_exchange_token"}, "client_id": {provider.ClientID}, "client_secret": {provider.ClientSecret}, "fb_exchange_token": {token.AccessToken},
	}.Encode()
	if err := doJSON(ctx, http.MethodGet, longURL, "", &longToken); err == nil && longToken.AccessToken != "" {
		token = longToken
	}
	var permissions struct {
		Data []struct {
			Permission string `json:"permission"`
			Status     string `json:"status"`
		} `json:"data"`
	}
	permissionsURL := strings.TrimRight(s.config.InstagramFacebookGraphAPIBase, "/") + "/me/permissions?" + url.Values{
		"access_token": {token.AccessToken},
	}.Encode()
	granted := make(map[string]bool)
	if err := doJSON(ctx, http.MethodGet, permissionsURL, "", &permissions); err != nil {
		return oauthToken{}, platformProfile{}, fmt.Errorf("Facebook granted permissions are unavailable")
	}
	for _, permission := range permissions.Data {
		if permission.Status == "granted" {
			granted[permission.Permission] = true
		}
	}
	missing := make([]string, 0)
	for _, permission := range provider.Scopes {
		if !granted[permission] {
			missing = append(missing, permission)
		}
	}
	if len(missing) > 0 {
		return oauthToken{}, platformProfile{}, fmt.Errorf("Facebook permissions were not granted: %s", strings.Join(missing, ", "))
	}
	type facebookPage struct {
		ID                       string `json:"id"`
		Name                     string `json:"name"`
		InstagramBusinessAccount struct {
			ID                string `json:"id"`
			Username          string `json:"username"`
			Name              string `json:"name"`
			ProfilePictureURL string `json:"profile_picture_url"`
		} `json:"instagram_business_account"`
	}
	var pages struct {
		Data []facebookPage `json:"data"`
	}
	pageFields := "id,name,instagram_business_account{id,username,name,profile_picture_url}"
	pagesURL := strings.TrimRight(s.config.InstagramFacebookGraphAPIBase, "/") + "/me/accounts?" + url.Values{
		"fields": {pageFields}, "access_token": {token.AccessToken},
	}.Encode()
	if err := doJSON(ctx, http.MethodGet, pagesURL, "", &pages); err != nil {
		return oauthToken{}, platformProfile{}, fmt.Errorf("Facebook Pages are unavailable")
	}
	if len(pages.Data) == 0 {
		assignedPagesURL := strings.TrimRight(s.config.InstagramFacebookGraphAPIBase, "/") + "/me/assigned_pages?" + url.Values{
			"fields": {pageFields}, "access_token": {token.AccessToken},
		}.Encode()
		var assignedPages struct {
			Data []facebookPage `json:"data"`
		}
		if err := doJSON(ctx, http.MethodGet, assignedPagesURL, "", &assignedPages); err == nil {
			pages.Data = assignedPages.Data
		}
	}
	accounts := make([]struct{ ID, Username, Name, ProfilePictureURL string }, 0, len(pages.Data))
	for _, page := range pages.Data {
		if page.InstagramBusinessAccount.ID != "" {
			accounts = append(accounts, struct{ ID, Username, Name, ProfilePictureURL string }{page.InstagramBusinessAccount.ID, page.InstagramBusinessAccount.Username, page.InstagramBusinessAccount.Name, page.InstagramBusinessAccount.ProfilePictureURL})
		}
	}
	if len(accounts) == 0 {
		pageNames := make([]string, 0, len(pages.Data))
		for _, page := range pages.Data {
			pageNames = append(pageNames, page.Name+" ("+page.ID+")")
		}
		return oauthToken{}, platformProfile{}, fmt.Errorf("Facebook returned %d Pages but none included a professional Instagram account: %s", len(pages.Data), strings.Join(pageNames, ", "))
	}
	if len(accounts) > 1 {
		return oauthToken{}, platformProfile{}, fmt.Errorf("more than one Instagram account is available; select a single connected Page and try again")
	}
	account := accounts[0]
	if account.Username == "" {
		return oauthToken{}, platformProfile{}, fmt.Errorf("linked Instagram account did not include a username")
	}
	displayName := account.Name
	if displayName == "" {
		displayName = account.Username
	}
	return oauthToken{AccessToken: token.AccessToken, Scopes: provider.Scopes, ExpiresIn: token.ExpiresIn}, platformProfile{ExternalID: account.ID, Username: account.Username, DisplayName: displayName, ProfileURL: "https://www.instagram.com/" + account.Username + "/", AvatarURL: account.ProfilePictureURL, AccountType: "PROFESSIONAL", Metadata: map[string]any{"connectionMode": "FACEBOOK"}}, nil
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

func (s *Server) completeVKOAuth(ctx context.Context, provider oauthProvider, code, verifier, state, deviceID string) (oauthToken, platformProfile, error) {
	if deviceID == "" {
		return oauthToken{}, platformProfile{}, fmt.Errorf("VK ID callback did not include a device ID")
	}
	tokenURL := strings.TrimRight(s.config.VKOAuthBase, "/") + "/oauth2/auth?" + url.Values{
		"grant_type": {"authorization_code"}, "redirect_uri": {provider.RedirectURL}, "client_id": {provider.ClientID}, "code_verifier": {verifier}, "state": {state}, "device_id": {deviceID},
	}.Encode()
	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(url.Values{"code": {code}}.Encode()))
	if err != nil {
		return oauthToken{}, platformProfile{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := doRequestJSON(req, &raw); err != nil || raw.AccessToken == "" {
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
	return oauthToken{AccessToken: raw.AccessToken, RefreshToken: raw.RefreshToken, Scopes: provider.Scopes, ExpiresIn: raw.ExpiresIn},
		platformProfile{ExternalID: strconv.FormatInt(user.ID, 10), Username: username, DisplayName: strings.TrimSpace(user.FirstName + " " + user.LastName), ProfileURL: "https://vk.ru/" + username, AvatarURL: user.Photo, AccountType: "COMPANY_OPERATOR", Metadata: map[string]any{"deviceId": deviceID}}, nil
}

func (s *Server) saveCompanyVKConnection(ctx context.Context, organizationID, companyVKAccountID string, provider oauthProvider, token oauthToken, profile platformProfile) error {
	if profile.ExternalID == "" || token.AccessToken == "" || s.envelope == nil {
		return fmt.Errorf("incomplete VK connection")
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
	err = tx.QueryRow(ctx, `INSERT INTO platform_accounts(organization_id,platform,external_id,username,display_name,profile_url,avatar_url,account_type,status,metadata,last_synced_at) VALUES($1,'VK',$2,$3,$4,$5,$6,'COMPANY_OPERATOR','ACTIVE',$7::jsonb,NULL) ON CONFLICT(organization_id,platform,external_id) DO UPDATE SET username=excluded.username,display_name=excluded.display_name,profile_url=excluded.profile_url,avatar_url=excluded.avatar_url,account_type=excluded.account_type,status='ACTIVE',metadata=excluded.metadata,last_error=NULL,updated_at=now() RETURNING id`, organizationID, profile.ExternalID, profile.Username, profile.DisplayName, profile.ProfileURL, profile.AvatarURL, string(metadata)).Scan(&accountID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO oauth_connections(organization_id,platform_account_id,access_token_ciphertext,refresh_token_ciphertext,nonce,access_token_nonce,refresh_token_nonce,scopes,expires_at,last_refreshed_at,status) VALUES($1,$2,$3,$4,$5,$5,$6,$7,$8,now(),'ACTIVE') ON CONFLICT(platform_account_id) DO UPDATE SET access_token_ciphertext=excluded.access_token_ciphertext,refresh_token_ciphertext=excluded.refresh_token_ciphertext,nonce=excluded.nonce,access_token_nonce=excluded.access_token_nonce,refresh_token_nonce=excluded.refresh_token_nonce,scopes=excluded.scopes,expires_at=excluded.expires_at,last_refreshed_at=now(),status='ACTIVE',disconnect_requested_at=NULL,purge_after=NULL,updated_at=now()`, organizationID, accountID, access, refresh, accessNonce, refreshNonce, token.Scopes, expiresAt)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE company_vk_accounts SET platform_account_id=$2,updated_at=now() WHERE id=$1 AND organization_id=$3`, companyVKAccountID, accountID, organizationID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sync_targets(organization_id,target_type,target_id,operation,cadence,next_sync_at,status) VALUES($1,'PLATFORM_ACCOUNT',$2,'VK_IMPORT',interval '6 hours',now(),'ACTIVE') ON CONFLICT(organization_id,target_id,operation) WHERE status='ACTIVE' DO UPDATE SET next_sync_at=now(),status='ACTIVE',last_error=NULL`, organizationID, accountID); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
	rows, err := s.pool.Query(r.Context(), `SELECT a.id,a.platform,a.username,a.display_name,a.status,COALESCE(a.avatar_url,''),COALESCE(a.profile_url,''),COALESCE(c.scopes,'{}'),a.last_synced_at,COALESCE(c.status,''),a.metadata FROM platform_accounts a JOIN creator_account_assignments x ON x.platform_account_id=a.id AND x.valid_to IS NULL LEFT JOIN oauth_connections c ON c.platform_account_id=a.id WHERE x.creator_id=$1 AND a.organization_id=$2 AND a.status<>'DISCONNECTED' ORDER BY a.platform,a.created_at DESC`, creatorID, p.OrganizationID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "connections failed", "could not load platform connections")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, platform, username, display, status, avatar, profileURL, oauthStatus string
		var scopes []string
		var metadata []byte
		var synced *time.Time
		if err := rows.Scan(&id, &platform, &username, &display, &status, &avatar, &profileURL, &scopes, &synced, &oauthStatus, &metadata); err != nil {
			problem(w, http.StatusInternalServerError, "connections failed", "could not read platform connection")
			return
		}
		values := map[string]any{}
		_ = json.Unmarshal(metadata, &values)
		items = append(items, map[string]any{"id": id, "platform": platform, "username": username, "displayName": display, "status": status, "oauthStatus": oauthStatus, "avatarUrl": avatar, "profileUrl": profileURL, "scopes": scopes, "lastSyncedAt": synced, "bioDescription": values["bioDescription"], "isVerified": values["isVerified"]})
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

		health, message := "HEALTHY", localized(r, "Синхронизация работает", "Synchronization is working")
		now := time.Now()
		switch {
		case accountStatus != "ACTIVE":
			health, message = "ERROR", localized(r, "Аккаунт требует повторного подключения", "The account must be reconnected")
		case oauthStatus == "":
			health, message = "ERROR", localized(r, "Нет активного OAuth-подключения", "There is no active OAuth connection")
		case oauthStatus != "ACTIVE":
			health, message = "ERROR", localized(r, "Авторизация недействительна", "Authorization is invalid")
		case expiresAt != nil && !expiresAt.After(now):
			health, message = "ERROR", localized(r, "Токен истёк", "The token has expired")
		case lastError != "":
			message = lastError
			if englishRequest(r) && containsCyrillic(message) {
				message = "Synchronization failed"
			}
			health = "ERROR"
		case consecutiveFailures > 0:
			health, message = "ERROR", fmt.Sprintf(localized(r, "Ошибок синхронизации подряд: %d", "Consecutive synchronization failures: %d"), consecutiveFailures)
		case expiresAt != nil && expiresAt.Before(now.Add(7*24*time.Hour)):
			health, message = "WARNING", localized(r, "Срок действия токена скоро закончится", "The token will expire soon")
		case lastSyncedAt == nil:
			health, message = "PENDING", localized(r, "Ожидает первой синхронизации", "Waiting for the first synchronization")
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
	connection, found, err := s.platformConnectionForRevocation(r.Context(), accountID, p.OrganizationID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "disconnection failed", "could not load platform connection")
		return
	}
	if !found {
		// DELETE is idempotent and deliberately does not disclose whether an ID
		// belongs to a different organization.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if token := s.platformRevocationToken(connection); token != "" {
		// Provider revocation is best-effort. The local transaction below is the
		// source of truth and must still remove tokens if a provider is down or the
		// token was already revoked.
		_ = s.revokePlatform(r.Context(), connection.platform, token)
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "disconnection failed", "could not start transaction")
		return
	}
	defer tx.Rollback(r.Context())

	var platform, status string
	var hasOAuth bool
	err = tx.QueryRow(r.Context(), `
		SELECT a.platform,a.status,EXISTS(SELECT 1 FROM oauth_connections c WHERE c.platform_account_id=a.id AND c.organization_id=a.organization_id)
		FROM platform_accounts a
		WHERE a.id=$1 AND a.organization_id=$2
		FOR UPDATE
	`, accountID, p.OrganizationID).Scan(&platform, &status, &hasOAuth)
	if errors.Is(err, pgx.ErrNoRows) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "disconnection failed", "could not lock platform connection")
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM oauth_connections c USING platform_accounts a WHERE c.platform_account_id=a.id AND a.id=$1 AND a.organization_id=$2`, accountID, p.OrganizationID); err != nil {
		problem(w, http.StatusInternalServerError, "disconnection failed", "could not remove OAuth tokens")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE platform_accounts SET status='DISCONNECTED',last_error=NULL,updated_at=now() WHERE id=$1 AND organization_id=$2`, accountID, p.OrganizationID); err != nil {
		problem(w, http.StatusInternalServerError, "disconnection failed", "could not update platform connection")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE sync_targets SET status='PAUSED',last_error=NULL WHERE target_id=$1 AND organization_id=$2`, accountID, p.OrganizationID); err != nil {
		problem(w, http.StatusInternalServerError, "disconnection failed", "could not pause synchronization")
		return
	}
	if status != "DISCONNECTED" || hasOAuth {
		if _, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,entity_id) VALUES($1,$2,$3,'PLATFORM_ACCOUNT',$4)`, p.OrganizationID, p.ID, "DISCONNECT_"+platform, accountID); err != nil {
			problem(w, http.StatusInternalServerError, "disconnection failed", "could not record disconnection")
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "disconnection failed", "could not commit disconnection")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type platformRevocationConnection struct {
	platform                    string
	accessCipher, accessNonce   []byte
	refreshCipher, refreshNonce []byte
}

func (s *Server) platformConnectionForRevocation(ctx context.Context, accountID, organizationID string) (platformRevocationConnection, bool, error) {
	var connection platformRevocationConnection
	err := s.pool.QueryRow(ctx, `
		SELECT a.platform,
			COALESCE(c.access_token_ciphertext,''::bytea),COALESCE(c.access_token_nonce,''::bytea),
			COALESCE(c.refresh_token_ciphertext,''::bytea),COALESCE(c.refresh_token_nonce,''::bytea)
		FROM platform_accounts a
		LEFT JOIN oauth_connections c ON c.platform_account_id=a.id AND c.organization_id=a.organization_id
		WHERE a.id=$1 AND a.organization_id=$2
	`, accountID, organizationID).Scan(&connection.platform, &connection.accessCipher, &connection.accessNonce, &connection.refreshCipher, &connection.refreshNonce)
	if errors.Is(err, pgx.ErrNoRows) {
		return platformRevocationConnection{}, false, nil
	}
	return connection, err == nil, err
}

func (s *Server) platformRevocationToken(connection platformRevocationConnection) string {
	if s.envelope == nil {
		return ""
	}
	decrypt := func(ciphertext, nonce []byte) string {
		if len(ciphertext) == 0 || len(nonce) == 0 {
			return ""
		}
		plain, err := s.envelope.Decrypt(ciphertext, nonce)
		if err != nil {
			return ""
		}
		return string(plain)
	}
	// Google recommends revoking the refresh token when one is available, so
	// an expired access token cannot leave a reusable long-lived credential.
	if connection.platform == "YOUTUBE" {
		if token := decrypt(connection.refreshCipher, connection.refreshNonce); token != "" {
			return token
		}
	}
	return decrypt(connection.accessCipher, connection.accessNonce)
}

func (s *Server) revokePlatform(ctx context.Context, platform, accessToken string) error {
	switch platform {
	case "TIKTOK":
		return s.revokeTikTok(ctx, accessToken)
	case "YOUTUBE":
		return doOAuthForm(ctx, strings.TrimRight(s.config.YouTubeOAuthBase, "/")+"/revoke", url.Values{"token": {accessToken}}, &map[string]any{})
	case "INSTAGRAM":
		return doJSON(ctx, http.MethodDelete, strings.TrimRight(s.config.InstagramAPIBase, "/")+"/me/permissions?"+url.Values{"access_token": {accessToken}}.Encode(), "", &map[string]any{})
	default:
		return nil
	}
}

func (s *Server) purgePlatformData(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	accountID := chi.URLParam(r, "id")

	// Revocation is best-effort: local deletion must still succeed when the
	// provider is unavailable or the token has already been revoked.
	connection, found, err := s.platformConnectionForRevocation(r.Context(), accountID, p.OrganizationID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "deletion failed", "could not load platform connection")
		return
	}
	if !found {
		// DELETE is idempotent. A retry after a lost 204 response is successful.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if token := s.platformRevocationToken(connection); token != "" {
		_ = s.revokePlatform(r.Context(), connection.platform, token)
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "deletion failed", "could not start transaction")
		return
	}
	defer tx.Rollback(r.Context())

	// Serialize deletion with updates to this account. Related account,
	// publication and OAuth rows use cascading foreign keys (migration 00009).
	platform := connection.platform
	if err = tx.QueryRow(r.Context(), `SELECT platform FROM platform_accounts WHERE id=$1 AND organization_id=$2 FOR UPDATE`, accountID, p.OrganizationID).Scan(&platform); errors.Is(err, pgx.ErrNoRows) {
		w.WriteHeader(http.StatusNoContent)
		return
	} else if err != nil {
		problem(w, http.StatusInternalServerError, "deletion failed", "could not lock platform connection")
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM sync_runs WHERE target_id IN (SELECT id FROM sync_targets WHERE target_id=$1 AND organization_id=$2)`, accountID, p.OrganizationID); err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM sync_targets WHERE target_id=$1 AND organization_id=$2`, accountID, p.OrganizationID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM platform_accounts WHERE id=$1 AND organization_id=$2`, accountID, p.OrganizationID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(organization_id,actor_id,action,entity_type,metadata) VALUES($1,$2,$3,'PLATFORM_ACCOUNT',jsonb_build_object('deletedAccount',$4::text))`, p.OrganizationID, p.ID, "PURGE_"+platform+"_DATA", accountID)
	}
	if err != nil {
		log.Printf("platform data deletion failed for account %s: %v", accountID, err)
		problem(w, http.StatusInternalServerError, "deletion failed", "platform data could not be removed")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		log.Printf("platform data deletion commit failed for account %s: %v", accountID, err)
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
