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

type facebookPage struct {
	ID                       string   `json:"id"`
	Name                     string   `json:"name"`
	AccessToken              string   `json:"access_token"`
	Tasks                    []string `json:"tasks"`
	InstagramBusinessAccount struct {
		ID string `json:"id"`
	} `json:"instagram_business_account"`
}

type facebookBusiness struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type instagramFacebookCandidate struct {
	Token   oauthToken      `json:"token"`
	Profile platformProfile `json:"profile"`
}

var (
	errOAuthTargetUnavailable         = errors.New("OAuth target is no longer active")
	errOAuthReconnectIdentityMismatch = errors.New("OAuth reconnect identity does not match the original account")
	errOAuthAuthorizationSuperseded   = errors.New("OAuth authorization was superseded by a newer reconnect")
)

type oauthReconnectAuthorization struct {
	AccountID  string
	Generation int64
}

const (
	oauthReconnectAccountMetadataKey    = "_statzavodReconnectAccountId"
	oauthReconnectGenerationMetadataKey = "_statzavodReconnectGeneration"
)

// logOAuthFailure deliberately logs a fixed diagnostic envelope only. Provider
// errors may wrap request URLs, signed query strings, authorization codes, or
// client secrets, so neither err.Error() nor a provider-supplied message is
// permitted in the log record.
func logOAuthFailure(provider, phase string, err error) {
	class := "INTERNAL"
	status := 0
	var providerErr *providerError
	if errors.As(err, &providerErr) {
		class = string(providerErr.Kind)
		status = providerErr.StatusCode
	} else if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		class = "TIMEOUT"
	}
	log.Printf("oauth_failure provider=%q phase=%q class=%q status=%d correlation_id=%q", provider, phase, class, status, makeToken())
}

func (s *Server) oauthProviders() map[string]oauthProvider {
	return map[string]oauthProvider{
		"youtube": {
			ID: "YOUTUBE", Name: "YouTube", ClientID: s.config.YouTubeClientID, ClientSecret: s.config.YouTubeClientSecret,
			RedirectURL: s.config.YouTubeRedirectURL, AuthorizeURL: "https://accounts.google.com/o/oauth2/v2/auth",
			Scopes:  []string{"https://www.googleapis.com/auth/youtube.readonly", "https://www.googleapis.com/auth/yt-analytics.readonly", "https://www.googleapis.com/auth/youtube.upload"},
			UsePKCE: true,
		},
		"instagram": {
			ID: "INSTAGRAM", Name: "Instagram", ClientID: s.config.InstagramClientID, ClientSecret: s.config.InstagramClientSecret,
			RedirectURL: s.config.InstagramRedirectURL, AuthorizeURL: strings.TrimRight(s.config.InstagramOAuthBase, "/") + "/oauth/authorize",
			Scopes: []string{"instagram_business_basic", "instagram_business_manage_insights", "instagram_business_content_publish"},
		},
		"instagram-facebook": {
			ID: "INSTAGRAM", Name: "Instagram через Facebook", ClientID: s.config.InstagramFacebookClientID, ClientSecret: s.config.InstagramFacebookClientSecret,
			RedirectURL: s.config.InstagramFacebookRedirectURL, AuthorizeURL: strings.TrimRight(s.config.InstagramFacebookOAuthBase, "/") + "/dialog/oauth",
			Scopes: []string{"instagram_basic", "instagram_manage_insights", "instagram_content_publish", "pages_show_list", "pages_read_engagement", "business_management"}, Flow: "FACEBOOK",
		},
		"tiktok": {
			ID: "TIKTOK", Name: "TikTok", ClientID: s.config.TikTokClientKey, ClientSecret: s.config.TikTokClientSecret,
			RedirectURL: s.config.TikTokRedirectURL, AuthorizeURL: "https://www.tiktok.com/v2/auth/authorize/",
			Scopes: strings.Split(tiktokScopes, ","),
		},
		"vk": {
			ID: "VK", Name: "VK", ClientID: s.config.VKClientID, ClientSecret: s.config.VKClientSecret,
			RedirectURL: s.config.VKRedirectURL, AuthorizeURL: strings.TrimRight(s.config.VKOAuthBase, "/") + "/authorize",
			Scopes: []string{"video", "wall", "groups", "stats", "offline"}, UsePKCE: true,
		},
	}
}

func beginCreatorOAuthReconnectAuthorization(ctx context.Context, tx pgx.Tx, organizationID, creatorID, platform string) (*oauthReconnectAuthorization, error) {
	rows, err := tx.Query(ctx, `
		SELECT account.id::text
		FROM platform_accounts account
		JOIN creator_account_assignments assignment
		  ON assignment.platform_account_id=account.id
		 AND assignment.organization_id=account.organization_id
		 AND assignment.valid_to IS NULL
		JOIN oauth_connections connection
		  ON connection.platform_account_id=account.id
		 AND connection.organization_id=account.organization_id
		WHERE account.organization_id=$1 AND assignment.creator_id=$2 AND account.platform=$3
		  AND account.status='REAUTH_REQUIRED' AND connection.status='REAUTH_REQUIRED'
		ORDER BY account.id
		FOR UPDATE OF connection`, organizationID, creatorID, platform)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accountIDs := make([]string, 0, 2)
	for rows.Next() {
		var accountID string
		if err = rows.Scan(&accountID); err != nil {
			return nil, err
		}
		accountIDs = append(accountIDs, accountID)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(accountIDs) == 0 {
		return nil, nil
	}
	if len(accountIDs) != 1 {
		return nil, errors.New("multiple OAuth reconnect identities require explicit account selection")
	}
	var generation int64
	if err = tx.QueryRow(ctx, `UPDATE oauth_connections SET authorization_generation=authorization_generation+1,updated_at=now() WHERE organization_id=$1 AND platform_account_id=$2 RETURNING authorization_generation`, organizationID, accountIDs[0]).Scan(&generation); err != nil {
		return nil, err
	}
	return &oauthReconnectAuthorization{AccountID: accountIDs[0], Generation: generation}, nil
}

func beginCompanyVKOAuthReconnectAuthorization(ctx context.Context, tx pgx.Tx, organizationID, companyVKAccountID string) (*oauthReconnectAuthorization, error) {
	var accountID string
	err := tx.QueryRow(ctx, `
		SELECT account.id::text
		FROM company_vk_accounts company_account
		JOIN platform_accounts account
		  ON account.id=company_account.platform_account_id
		 AND account.organization_id=company_account.organization_id
		JOIN oauth_connections connection
		  ON connection.platform_account_id=account.id
		 AND connection.organization_id=account.organization_id
		WHERE company_account.id=$1 AND company_account.organization_id=$2
		  AND account.status='REAUTH_REQUIRED' AND connection.status='REAUTH_REQUIRED'
		FOR UPDATE OF connection`, companyVKAccountID, organizationID).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var generation int64
	if err = tx.QueryRow(ctx, `UPDATE oauth_connections SET authorization_generation=authorization_generation+1,updated_at=now() WHERE organization_id=$1 AND platform_account_id=$2 RETURNING authorization_generation`, organizationID, accountID).Scan(&generation); err != nil {
		return nil, err
	}
	return &oauthReconnectAuthorization{AccountID: accountID, Generation: generation}, nil
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
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "OAuth state creation failed", "could not start authorization")
		return
	}
	defer tx.Rollback(r.Context())
	companyID, err := lockActiveCreatorCompanyTarget(r.Context(), tx, p.OrganizationID, creatorID)
	if err != nil {
		problem(w, http.StatusNotFound, "creator not found", "creator does not exist in this organization")
		return
	}
	reconnect, err := beginCreatorOAuthReconnectAuthorization(r.Context(), tx, p.OrganizationID, creatorID, provider.ID)
	if err != nil {
		problem(w, http.StatusConflict, "OAuth reconnect failed", "could not establish a unique reconnect authorization")
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
	var reconnectAccountID any
	var authorizationGeneration any
	if reconnect != nil {
		reconnectAccountID = reconnect.AccountID
		authorizationGeneration = reconnect.Generation
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO oauth_states(organization_id,creator_id,platform,state_hash,pkce_verifier_ciphertext,nonce,expires_at,initiated_by,reconnect_platform_account_id,authorization_generation) VALUES($1,$2,$3,$4,$5,$6,now()+interval '10 minutes',$7,$8,$9)`, p.OrganizationID, creatorID, provider.ID, hash[:], encryptedVerifier, nonce, p.ID, reconnectAccountID, authorizationGeneration)
	if err != nil {
		problem(w, http.StatusInternalServerError, "OAuth state creation failed", "could not start "+provider.Name+" authorization")
		return
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &companyID, "START_"+provider.ID+"_OAUTH", "CREATOR", &creatorID, http.StatusOK, map[string]any{"platform": provider.ID})); err != nil {
		problem(w, http.StatusInternalServerError, "OAuth state creation failed", "could not write audit record")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "OAuth state creation failed", "could not commit authorization state")
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
		configureInstagramAuthorization(q, provider, s.config.InstagramFacebookConfigID)
	case "VK":
		q.Set("scope", strings.Join(provider.Scopes, " "))
	}
	if provider.UsePKCE {
		q.Set("code_challenge", challenge)
		q.Set("code_challenge_method", "S256")
	}
	markResponseAuditCommitted(w)
	writeJSON(w, http.StatusOK, map[string]any{"authorizationUrl": provider.AuthorizeURL + "?" + q.Encode(), "expiresAt": time.Now().Add(10 * time.Minute)})
}

func configureInstagramAuthorization(query url.Values, provider oauthProvider, facebookConfigID string) {
	if provider.Flow != "FACEBOOK" {
		query.Set("scope", strings.Join(provider.Scopes, ","))
		return
	}
	// Facebook Login for Business gets permissions and Page asset selection
	// from config_id. Meta documents config_id as the replacement for scope;
	// sending both can produce an ordinary user consent without Page assets. The
	// config must include business_management: we use it after OAuth to enumerate
	// Business Portfolios and their owned Pages.
	query.Del("scope")
	query.Set("config_id", facebookConfigID)
	query.Set("override_default_response_type", "true")
	query.Set("auth_type", "rerequest")
}

func (s *Server) companyVKOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	provider := s.oauthProviders()["vk"]
	if s.envelope == nil || provider.ClientID == "" || provider.ClientSecret == "" || provider.RedirectURL == "" {
		problem(w, http.StatusServiceUnavailable, "VK is not configured", "server OAuth credentials are missing")
		return
	}
	p := r.Context().Value(principalKey).(principal)
	companyID := chi.URLParam(r, "id")
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "OAuth state creation failed", "could not start authorization")
		return
	}
	defer tx.Rollback(r.Context())
	if err = lockActiveCompanyTarget(r.Context(), tx, p.OrganizationID, companyID); err != nil {
		problem(w, http.StatusNotFound, "company is unavailable", "company does not exist in an active state")
		return
	}
	var companyVKAccountID string
	if err = tx.QueryRow(r.Context(), `INSERT INTO company_vk_accounts(organization_id,company_id,created_by,updated_by) VALUES($1,$2,$3,$3) ON CONFLICT(company_id) DO UPDATE SET updated_by=excluded.updated_by,updated_at=now() RETURNING id`, p.OrganizationID, companyID, p.ID).Scan(&companyVKAccountID); err != nil {
		problem(w, http.StatusBadRequest, "company is unavailable", "could not prepare the shared VK account")
		return
	}
	reconnect, err := beginCompanyVKOAuthReconnectAuthorization(r.Context(), tx, p.OrganizationID, companyVKAccountID)
	if err != nil {
		problem(w, http.StatusConflict, "OAuth reconnect failed", "could not establish a unique reconnect authorization")
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
	var reconnectAccountID any
	var authorizationGeneration any
	if reconnect != nil {
		reconnectAccountID = reconnect.AccountID
		authorizationGeneration = reconnect.Generation
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO oauth_states(organization_id,creator_id,company_vk_account_id,platform,state_hash,pkce_verifier_ciphertext,nonce,expires_at,initiated_by,reconnect_platform_account_id,authorization_generation) VALUES($1,NULL,$2,'VK',$3,$4,$5,now()+interval '10 minutes',$6,$7,$8)`, p.OrganizationID, companyVKAccountID, hash[:], encryptedVerifier, nonce, p.ID, reconnectAccountID, authorizationGeneration)
	if err != nil {
		problem(w, http.StatusInternalServerError, "OAuth state creation failed", "could not start VK authorization")
		return
	}
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &companyID, "START_VK_OAUTH", "COMPANY_VK_ACCOUNT", &companyVKAccountID, http.StatusOK, map[string]any{"platform": "VK"})); err != nil {
		problem(w, http.StatusInternalServerError, "OAuth state creation failed", "could not write audit record")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "OAuth state creation failed", "could not commit authorization state")
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
	markResponseAuditCommitted(w)
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
	var initiatedBy string
	var creatorID, companyVKAccountID *string
	var reconnectAccountID *string
	var authorizationGeneration *int64
	var encryptedVerifier, nonce []byte
	err := s.pool.QueryRow(r.Context(), `UPDATE oauth_states SET consumed_at=now() WHERE state_hash=$1 AND platform=$2 AND consumed_at IS NULL AND expires_at>now() RETURNING organization_id,creator_id,company_vk_account_id,pkce_verifier_ciphertext,nonce,initiated_by,reconnect_platform_account_id,authorization_generation`, hash[:], provider.ID).Scan(&organizationID, &creatorID, &companyVKAccountID, &encryptedVerifier, &nonce, &initiatedBy, &reconnectAccountID, &authorizationGeneration)
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
	if !s.oauthInitiatorStillAuthorized(r.Context(), organizationID, initiatedBy, creatorID, companyVKAccountID) {
		redirect("permission-revoked")
		return
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
	if provider.Flow == "FACEBOOK" && creatorID != nil {
		candidates, discoverErr := s.discoverInstagramFacebookAccounts(r.Context(), provider, code)
		if discoverErr != nil {
			logOAuthFailure(provider.ID, "account_discovery", discoverErr)
			redirect("provider-error")
			return
		}
		if reconnectAccountID != nil && authorizationGeneration != nil {
			reconnect := oauthReconnectAuthorization{AccountID: *reconnectAccountID, Generation: *authorizationGeneration}
			for index := range candidates {
				setOAuthReconnectAuthorization(&candidates[index].Profile, &reconnect)
			}
		}
		selectionID, selectionErr := s.createInstagramAccountSelection(r.Context(), organizationID, *creatorID, initiatedBy, candidates)
		if selectionErr != nil {
			if errors.Is(selectionErr, errOAuthTargetUnavailable) {
				redirect("permission-revoked")
				return
			}
			logOAuthFailure(provider.ID, "selection_save", selectionErr)
			redirect("save-error")
			return
		}
		s.redirectToApp(w, r, "/app/creators/"+*creatorID+"?platform="+url.QueryEscape(platformKey)+"&oauth=select&selection="+url.QueryEscape(selectionID))
		return
	}
	token, profile, err := s.completeOAuth(r.Context(), provider, code, string(verifier), state, r.URL.Query().Get("device_id"))
	if err != nil {
		logOAuthFailure(provider.ID, "exchange_or_identity", err)
		redirect("provider-error")
		return
	}
	if reconnectAccountID != nil && authorizationGeneration != nil {
		setOAuthReconnectAuthorization(&profile, &oauthReconnectAuthorization{AccountID: *reconnectAccountID, Generation: *authorizationGeneration})
	}
	if companyVKAccountID != nil {
		err = s.saveCompanyVKConnection(r.Context(), organizationID, *companyVKAccountID, provider, token, profile)
	} else if creatorID != nil {
		err = s.savePlatformConnection(r.Context(), organizationID, *creatorID, provider, token, profile)
	} else {
		err = fmt.Errorf("OAuth state has no owner")
	}
	if err != nil {
		if errors.Is(err, errOAuthTargetUnavailable) {
			redirect("permission-revoked")
			return
		}
		if errors.Is(err, errOAuthAuthorizationSuperseded) || errors.Is(err, errOAuthReconnectIdentityMismatch) {
			redirect("superseded")
			return
		}
		logOAuthFailure(provider.ID, "credential_save", err)
		redirect("save-error")
		return
	}
	redirect("connected")
}

// OAuth state is not an authorization grant. A manager assignment or
// SOCIAL_CONNECT permission may be revoked while the user is at the provider.
func (s *Server) oauthInitiatorStillAuthorized(ctx context.Context, organizationID, initiatedBy string, creatorID, companyVKAccountID *string) bool {
	var allowed bool
	err := s.pool.QueryRow(ctx, `
		WITH target AS (
			SELECT company_id FROM creators
			WHERE $3::uuid IS NOT NULL AND id=$3 AND organization_id=$1
			  AND status='ACTIVE' AND archived_at IS NULL
			UNION ALL
			SELECT company_id FROM company_vk_accounts
			WHERE $4::uuid IS NOT NULL AND id=$4 AND organization_id=$1
		), actor AS (
			SELECT m.membership_role
			FROM organization_memberships m JOIN users u ON u.id=m.user_id
			WHERE m.organization_id=$1 AND m.user_id=$2 AND u.status='ACTIVE'
		)
		SELECT EXISTS(
			SELECT 1 FROM actor CROSS JOIN target t
			JOIN organizations o ON o.id=$1 AND o.lifecycle_state='ACTIVE'
			JOIN companies c ON c.id=t.company_id AND c.organization_id=$1 AND c.archived_at IS NULL
			WHERE membership_role='OWNER'
			   OR (membership_role='MANAGER' AND EXISTS (
				SELECT 1 FROM manager_company_assignments a
				JOIN manager_company_permissions p
				  ON p.assignment_id=a.id AND p.permission='SOCIAL_CONNECT'
				WHERE a.organization_id=$1 AND a.manager_user_id=$2 AND a.company_id=t.company_id
			))
		)`, organizationID, initiatedBy, creatorID, companyVKAccountID).Scan(&allowed)
	return err == nil && allowed
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
		return oauthToken{AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, Scopes: splitScopes(token.Scope, nil), ExpiresIn: token.ExpiresIn, ExternalID: token.OpenID},
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
	candidates, err := s.discoverInstagramFacebookAccounts(ctx, provider, code)
	if err != nil {
		return oauthToken{}, platformProfile{}, err
	}
	if len(candidates) != 1 {
		return oauthToken{}, platformProfile{}, fmt.Errorf("Facebook returned %d linked Instagram accounts", len(candidates))
	}
	return candidates[0].Token, candidates[0].Profile, nil
}

func (s *Server) discoverInstagramFacebookAccounts(ctx context.Context, provider oauthProvider, code string) ([]instagramFacebookCandidate, error) {
	endpoint := strings.TrimRight(s.config.InstagramFacebookGraphAPIBase, "/") + "/oauth/access_token?" + url.Values{
		"client_id": {provider.ClientID}, "client_secret": {provider.ClientSecret}, "redirect_uri": {provider.RedirectURL}, "code": {code},
	}.Encode()
	var token struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := doJSON(ctx, http.MethodGet, endpoint, "", &token); err != nil || token.AccessToken == "" {
		return nil, fmt.Errorf("Facebook token exchange failed")
	}
	var longToken struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	longURL := strings.TrimRight(s.config.InstagramFacebookGraphAPIBase, "/") + "/oauth/access_token?" + url.Values{
		"grant_type": {"fb_exchange_token"}, "client_id": {provider.ClientID}, "client_secret": {provider.ClientSecret}, "fb_exchange_token": {token.AccessToken},
	}.Encode()
	if err := doJSON(ctx, http.MethodGet, longURL, "", &longToken); err != nil || longToken.AccessToken == "" {
		return nil, fmt.Errorf("Facebook long-lived token exchange failed")
	}
	token = longToken
	var facebookUser struct {
		ID string `json:"id"`
	}
	facebookUserURL := strings.TrimRight(s.config.InstagramFacebookGraphAPIBase, "/") + "/me?" + url.Values{
		"fields":       {"id"},
		"access_token": {token.AccessToken},
	}.Encode()
	if err := doJSON(ctx, http.MethodGet, facebookUserURL, "", &facebookUser); err != nil || facebookUser.ID == "" {
		return nil, fmt.Errorf("Facebook user identity is unavailable")
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
		return nil, fmt.Errorf("Facebook granted permissions are unavailable")
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
		return nil, fmt.Errorf("Facebook permissions were not granted: %s", strings.Join(missing, ", "))
	}
	businessPages, businessErr := s.fetchFacebookBusinessPortfolioPages(ctx, token.AccessToken)
	directPages, directErr := s.fetchFacebookPages(ctx, token.AccessToken)
	if businessErr != nil && directErr != nil {
		return nil, fmt.Errorf("Facebook Pages are unavailable")
	}
	pages := mergeFacebookPages(businessPages, directPages)
	linkedPages := make([]facebookPage, 0, len(pages))
	for _, page := range pages {
		if page.InstagramBusinessAccount.ID != "" {
			linkedPages = append(linkedPages, page)
		}
	}
	if len(linkedPages) == 0 {
		if len(pages) == 0 {
			return nil, fmt.Errorf("Facebook granted the requested permissions but returned 0 Page assets; check the Facebook Login for Business configuration and selected Pages")
		}
		pageNames := make([]string, 0, len(pages))
		for _, page := range pages {
			pageNames = append(pageNames, page.Name+" ("+page.ID+")")
		}
		return nil, fmt.Errorf("Facebook returned %d Pages but none included a professional Instagram account: %s", len(pages), strings.Join(pageNames, ", "))
	}
	candidates := make([]instagramFacebookCandidate, 0, len(linkedPages))
	seenAccounts := make(map[string]struct{}, len(linkedPages))
	for _, page := range linkedPages {
		if page.AccessToken == "" {
			continue
		}
		account, accountErr := s.fetchFacebookInstagramAccount(ctx, page.InstagramBusinessAccount.ID, page.AccessToken)
		if accountErr != nil {
			continue
		}
		if _, seen := seenAccounts[account.ID]; seen {
			continue
		}
		seenAccounts[account.ID] = struct{}{}
		displayName := account.Name
		if displayName == "" {
			displayName = account.Username
		}
		candidates = append(candidates, instagramFacebookCandidate{
			Token:   oauthToken{AccessToken: page.AccessToken, RefreshToken: token.AccessToken, Scopes: provider.Scopes, ExpiresIn: token.ExpiresIn},
			Profile: platformProfile{ExternalID: account.ID, Username: account.Username, DisplayName: displayName, ProfileURL: "https://www.instagram.com/" + account.Username + "/", AvatarURL: account.ProfilePictureURL, AccountType: "PROFESSIONAL", Metadata: map[string]any{"connectionMode": "FACEBOOK", "facebookUserId": facebookUser.ID, "facebookPageId": page.ID, "facebookPageName": page.Name}},
		})
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("Facebook Pages did not provide a usable linked Instagram account")
	}
	return candidates, nil
}

func (s *Server) fetchFacebookPages(ctx context.Context, userAccessToken string) ([]facebookPage, error) {
	base := strings.TrimRight(s.config.InstagramFacebookGraphAPIBase, "/") + "/me/accounts"
	query := url.Values{
		"fields":       {"id,name,access_token,tasks,instagram_business_account"},
		"limit":        {"100"},
		"access_token": {userAccessToken},
	}
	pages := make([]facebookPage, 0)
	seenCursors := make(map[string]struct{})
	for {
		var response struct {
			Data   []facebookPage `json:"data"`
			Paging struct {
				Next    string `json:"next"`
				Cursors struct {
					After string `json:"after"`
				} `json:"cursors"`
			} `json:"paging"`
		}
		if err := doJSON(ctx, http.MethodGet, base+"?"+query.Encode(), "", &response); err != nil {
			return nil, err
		}
		pages = append(pages, response.Data...)
		after := response.Paging.Cursors.After
		if response.Paging.Next == "" || after == "" || len(response.Data) == 0 {
			return pages, nil
		}
		if _, seen := seenCursors[after]; seen {
			return nil, fmt.Errorf("Facebook Pages pagination repeated a cursor")
		}
		seenCursors[after] = struct{}{}
		query.Set("after", after)
	}
}

func (s *Server) fetchFacebookBusinessPortfolioPages(ctx context.Context, userAccessToken string) ([]facebookPage, error) {
	base := strings.TrimRight(s.config.InstagramFacebookGraphAPIBase, "/")
	query := url.Values{
		"fields":       {"id,name"},
		"limit":        {"100"},
		"access_token": {userAccessToken},
	}
	businesses := make([]facebookBusiness, 0)
	seenCursors := make(map[string]struct{})
	for {
		var response struct {
			Data   []facebookBusiness `json:"data"`
			Paging struct {
				Next    string `json:"next"`
				Cursors struct {
					After string `json:"after"`
				} `json:"cursors"`
			} `json:"paging"`
		}
		if err := doJSON(ctx, http.MethodGet, base+"/me/businesses?"+query.Encode(), "", &response); err != nil {
			return nil, err
		}
		businesses = append(businesses, response.Data...)
		after := response.Paging.Cursors.After
		if response.Paging.Next == "" || after == "" || len(response.Data) == 0 {
			break
		}
		if _, seen := seenCursors[after]; seen {
			return nil, fmt.Errorf("Facebook Business pagination repeated a cursor")
		}
		seenCursors[after] = struct{}{}
		query.Set("after", after)
	}

	pages := make([]facebookPage, 0)
	seenPages := make(map[string]struct{})
	var lastErr error
	for _, business := range businesses {
		if business.ID == "" {
			continue
		}
		businessPages, err := s.fetchFacebookBusinessOwnedPages(ctx, business.ID, userAccessToken)
		if err != nil {
			lastErr = err
			continue
		}
		for _, businessPage := range businessPages {
			if businessPage.ID == "" {
				continue
			}
			if _, seen := seenPages[businessPage.ID]; seen {
				continue
			}
			page, pageErr := s.fetchFacebookPage(ctx, businessPage.ID, userAccessToken)
			if pageErr != nil {
				lastErr = pageErr
				continue
			}
			seenPages[page.ID] = struct{}{}
			pages = append(pages, page)
		}
	}
	if len(pages) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return pages, nil
}

func (s *Server) fetchFacebookBusinessOwnedPages(ctx context.Context, businessID, userAccessToken string) ([]facebookPage, error) {
	base := strings.TrimRight(s.config.InstagramFacebookGraphAPIBase, "/") + "/" + url.PathEscape(businessID) + "/owned_pages"
	query := url.Values{
		"fields":       {"id,name"},
		"limit":        {"100"},
		"access_token": {userAccessToken},
	}
	pages := make([]facebookPage, 0)
	seenCursors := make(map[string]struct{})
	for {
		var response struct {
			Data   []facebookPage `json:"data"`
			Paging struct {
				Next    string `json:"next"`
				Cursors struct {
					After string `json:"after"`
				} `json:"cursors"`
			} `json:"paging"`
		}
		if err := doJSON(ctx, http.MethodGet, base+"?"+query.Encode(), "", &response); err != nil {
			return nil, err
		}
		pages = append(pages, response.Data...)
		after := response.Paging.Cursors.After
		if response.Paging.Next == "" || after == "" || len(response.Data) == 0 {
			return pages, nil
		}
		if _, seen := seenCursors[after]; seen {
			return nil, fmt.Errorf("Facebook Business Page pagination repeated a cursor")
		}
		seenCursors[after] = struct{}{}
		query.Set("after", after)
	}
}

func (s *Server) fetchFacebookPage(ctx context.Context, pageID, userAccessToken string) (facebookPage, error) {
	endpoint := strings.TrimRight(s.config.InstagramFacebookGraphAPIBase, "/") + "/" + url.PathEscape(pageID) + "?" + url.Values{
		"fields":       {"id,name,access_token,tasks,instagram_business_account"},
		"access_token": {userAccessToken},
	}.Encode()
	var page facebookPage
	if err := doJSON(ctx, http.MethodGet, endpoint, "", &page); err != nil {
		return facebookPage{}, err
	}
	if page.ID == "" || page.AccessToken == "" {
		return facebookPage{}, fmt.Errorf("Facebook Business Page access token is unavailable")
	}
	return page, nil
}

func mergeFacebookPages(pageSets ...[]facebookPage) []facebookPage {
	pages := make([]facebookPage, 0)
	seen := make(map[string]int)
	for _, pageSet := range pageSets {
		for _, page := range pageSet {
			if page.ID == "" {
				continue
			}
			if index, ok := seen[page.ID]; ok {
				if pages[index].AccessToken == "" && page.AccessToken != "" {
					pages[index] = page
				}
				continue
			}
			seen[page.ID] = len(pages)
			pages = append(pages, page)
		}
	}
	return pages
}

func (s *Server) fetchFacebookInstagramAccount(ctx context.Context, accountID, pageAccessToken string) (instagramAccount, error) {
	endpoint := strings.TrimRight(s.config.InstagramFacebookGraphAPIBase, "/") + "/" + url.PathEscape(accountID) + "?" + url.Values{
		"fields":       {"id,username,name,profile_picture_url"},
		"access_token": {pageAccessToken},
	}.Encode()
	var account instagramAccount
	if err := doJSON(ctx, http.MethodGet, endpoint, "", &account); err != nil {
		return instagramAccount{}, err
	}
	if account.ID == "" || account.Username == "" {
		return instagramAccount{}, fmt.Errorf("Instagram account identity is incomplete")
	}
	return account, nil
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
	token := oauthToken{AccessToken: raw.AccessToken, RefreshToken: raw.RefreshToken, Scopes: splitScopes(raw.Scope, nil), ExpiresIn: raw.ExpiresIn}
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
	// Instagram Login does not return granted scopes from either token exchange
	// used by this flow, and this integration has no permissions introspection
	// call. Requested scopes are intent, not evidence of a grant: persist an
	// explicit empty set so publishing readiness remains fail-closed.
	return oauthToken{AccessToken: accessToken, Scopes: []string{}, ExpiresIn: expiresIn},
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
		Scope        string `json:"scope"`
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
	return oauthToken{AccessToken: raw.AccessToken, RefreshToken: raw.RefreshToken, Scopes: splitScopes(raw.Scope, nil), ExpiresIn: raw.ExpiresIn},
		platformProfile{ExternalID: strconv.FormatInt(user.ID, 10), Username: username, DisplayName: strings.TrimSpace(user.FirstName + " " + user.LastName), ProfileURL: "https://vk.ru/" + username, AvatarURL: user.Photo, AccountType: "COMPANY_OPERATOR", Metadata: map[string]any{"deviceId": deviceID}}, nil
}

func (s *Server) saveCompanyVKConnection(ctx context.Context, organizationID, companyVKAccountID string, provider oauthProvider, token oauthToken, profile platformProfile) error {
	reconnect, err := takeOAuthReconnectAuthorization(&profile)
	if err != nil {
		return err
	}
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
	metadata, err := json.Marshal(profile.Metadata)
	if err != nil {
		return err
	}
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
	companyID, err := lockActiveCompanyVKTarget(ctx, tx, organizationID, companyVKAccountID)
	if err != nil {
		return err
	}
	if reconnect != nil {
		var currentExternalID string
		err = tx.QueryRow(ctx, `SELECT account.external_id FROM company_vk_accounts company_account JOIN platform_accounts account ON account.id=company_account.platform_account_id AND account.organization_id=company_account.organization_id JOIN oauth_connections connection ON connection.platform_account_id=account.id AND connection.organization_id=account.organization_id WHERE company_account.id=$1 AND company_account.organization_id=$2 AND account.id=$3 AND account.platform='VK' AND connection.authorization_generation=$4 FOR UPDATE OF connection`, companyVKAccountID, organizationID, reconnect.AccountID, reconnect.Generation).Scan(&currentExternalID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errOAuthAuthorizationSuperseded
		}
		if err != nil {
			return err
		}
		if currentExternalID != profile.ExternalID {
			return errOAuthReconnectIdentityMismatch
		}
	}
	var accountID string
	err = tx.QueryRow(ctx, `INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name,profile_url,avatar_url,account_type,status,metadata,last_synced_at) VALUES($1,$2,'VK',$3,$4,$5,$6,$7,'COMPANY_OPERATOR','ACTIVE',$8::jsonb,NULL) ON CONFLICT(organization_id,platform,external_id) DO UPDATE SET username=excluded.username,display_name=excluded.display_name,profile_url=excluded.profile_url,avatar_url=excluded.avatar_url,account_type=excluded.account_type,status='ACTIVE',metadata=excluded.metadata,last_error=NULL,updated_at=now() WHERE platform_accounts.company_id=excluded.company_id RETURNING id`, organizationID, companyID, profile.ExternalID, profile.Username, profile.DisplayName, profile.ProfileURL, profile.AvatarURL, string(metadata)).Scan(&accountID)
	if err != nil {
		return err
	}
	if reconnect != nil {
		result, updateErr := tx.Exec(ctx, `UPDATE oauth_connections SET access_token_ciphertext=$3,refresh_token_ciphertext=$4,nonce=$5,access_token_nonce=$5,refresh_token_nonce=$6,scopes=$7,expires_at=$8,last_refreshed_at=now(),status='ACTIVE',disconnect_requested_at=NULL,purge_after=NULL,updated_at=now() WHERE organization_id=$1 AND platform_account_id=$2 AND authorization_generation=$9`, organizationID, accountID, access, refresh, accessNonce, refreshNonce, token.Scopes, expiresAt, reconnect.Generation)
		if updateErr != nil {
			return updateErr
		}
		if result.RowsAffected() != 1 {
			return errOAuthAuthorizationSuperseded
		}
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO oauth_connections(organization_id,platform_account_id,access_token_ciphertext,refresh_token_ciphertext,nonce,access_token_nonce,refresh_token_nonce,scopes,expires_at,last_refreshed_at,status) VALUES($1,$2,$3,$4,$5,$5,$6,$7,$8,now(),'ACTIVE') ON CONFLICT(platform_account_id) DO UPDATE SET access_token_ciphertext=excluded.access_token_ciphertext,refresh_token_ciphertext=excluded.refresh_token_ciphertext,nonce=excluded.nonce,access_token_nonce=excluded.access_token_nonce,refresh_token_nonce=excluded.refresh_token_nonce,scopes=excluded.scopes,expires_at=excluded.expires_at,last_refreshed_at=now(),status='ACTIVE',disconnect_requested_at=NULL,purge_after=NULL,updated_at=now()`, organizationID, accountID, access, refresh, accessNonce, refreshNonce, token.Scopes, expiresAt)
		if err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE company_vk_accounts SET platform_account_id=$2,updated_at=now() WHERE id=$1 AND organization_id=$3`, companyVKAccountID, accountID, organizationID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sync_targets(organization_id,target_type,target_id,operation,cadence,next_sync_at,status) VALUES($1,'PLATFORM_ACCOUNT',$2,'VK_IMPORT',interval '6 hours',now(),'ACTIVE') ON CONFLICT(organization_id,target_id,operation) WHERE status='ACTIVE' DO UPDATE SET next_sync_at=now(),status='ACTIVE',last_error=NULL`, organizationID, accountID); err != nil {
		return err
	}
	if err = s.writeAudit(ctx, tx, auditRecord{
		OrganizationID: organizationID,
		CompanyID:      &companyID,
		Action:         "CONNECT_VK",
		EntityType:     "PLATFORM_ACCOUNT",
		EntityID:       &accountID,
		Metadata:       map[string]any{"scopes": token.Scopes},
	}); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if reconnect != nil {
		return s.resumeVerifiedOAuthReconnectGeneration(ctx, organizationID, provider.ID, profile.ExternalID, *reconnect)
	}
	return nil
}

func (s *Server) savePlatformConnection(ctx context.Context, organizationID, creatorID string, provider oauthProvider, token oauthToken, profile platformProfile) error {
	reconnect, err := takeOAuthReconnectAuthorization(&profile)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = s.savePlatformConnectionTxAuthorized(ctx, tx, organizationID, creatorID, provider, token, profile, reconnect); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if reconnect != nil {
		return s.resumeVerifiedOAuthReconnectGeneration(ctx, organizationID, provider.ID, profile.ExternalID, *reconnect)
	}
	// Compatibility for internal imports/tests that do not originate in an OAuth
	// callback. Real reconnect callbacks always carry a generation.
	return s.resumeVerifiedOAuthReconnect(ctx, organizationID, provider.ID, profile.ExternalID)
}

func (s *Server) savePlatformConnectionTx(ctx context.Context, tx pgx.Tx, organizationID, creatorID string, provider oauthProvider, token oauthToken, profile platformProfile) error {
	reconnect, err := takeOAuthReconnectAuthorization(&profile)
	if err != nil {
		return err
	}
	return s.savePlatformConnectionTxAuthorized(ctx, tx, organizationID, creatorID, provider, token, profile, reconnect)
}

func (s *Server) savePlatformConnectionTxAuthorized(ctx context.Context, tx pgx.Tx, organizationID, creatorID string, provider oauthProvider, token oauthToken, profile platformProfile, reconnect *oauthReconnectAuthorization) error {
	companyID, err := lockActiveCreatorCompanyTarget(ctx, tx, organizationID, creatorID)
	if err != nil {
		return err
	}
	if profile.ExternalID == "" || token.AccessToken == "" || s.envelope == nil {
		return fmt.Errorf("incomplete platform connection")
	}
	if reconnect != nil {
		if err = verifyOAuthReconnectAuthorization(ctx, tx, organizationID, creatorID, provider.ID, profile.ExternalID, *reconnect); err != nil {
			return err
		}
	} else if err = verifyOAuthReconnectIdentity(ctx, tx, organizationID, creatorID, provider.ID, profile.ExternalID); err != nil {
		return err
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
	metadata, err := json.Marshal(profile.Metadata)
	if err != nil {
		return err
	}
	if len(metadata) == 0 || string(metadata) == "null" {
		metadata = []byte("{}")
	}
	var expiresAt *time.Time
	if token.ExpiresIn > 0 {
		value := time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
		expiresAt = &value
	}
	var accountID string
	err = tx.QueryRow(ctx, `INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name,profile_url,avatar_url,account_type,status,metadata,last_synced_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'ACTIVE',$10::jsonb,NULL) ON CONFLICT(organization_id,platform,external_id) DO UPDATE SET username=excluded.username,display_name=excluded.display_name,profile_url=excluded.profile_url,avatar_url=excluded.avatar_url,account_type=excluded.account_type,status='ACTIVE',metadata=excluded.metadata,last_error=NULL,updated_at=now() WHERE platform_accounts.company_id=excluded.company_id RETURNING id`, organizationID, companyID, provider.ID, profile.ExternalID, profile.Username, profile.DisplayName, profile.ProfileURL, profile.AvatarURL, profile.AccountType, string(metadata)).Scan(&accountID)
	if err != nil {
		return err
	}
	if err = cancelContentPublishesForAccountReassignmentTx(ctx, tx, organizationID, accountID, creatorID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE creator_account_assignments SET valid_to=now() WHERE platform_account_id=$1 AND valid_to IS NULL AND creator_id<>$2`, accountID, creatorID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO creator_account_assignments(creator_id,platform_account_id) SELECT $1,$2 WHERE NOT EXISTS(SELECT 1 FROM creator_account_assignments WHERE creator_id=$1 AND platform_account_id=$2 AND valid_to IS NULL)`, creatorID, accountID); err != nil {
		return err
	}
	if reconnect != nil {
		result, updateErr := tx.Exec(ctx, `UPDATE oauth_connections SET access_token_ciphertext=$3,refresh_token_ciphertext=$4,nonce=$5,access_token_nonce=$5,refresh_token_nonce=$6,scopes=$7,expires_at=$8,last_refreshed_at=now(),status='ACTIVE',disconnect_requested_at=NULL,purge_after=NULL,updated_at=now() WHERE organization_id=$1 AND platform_account_id=$2 AND authorization_generation=$9`, organizationID, accountID, access, refresh, accessNonce, refreshNonce, token.Scopes, expiresAt, reconnect.Generation)
		if updateErr != nil {
			return updateErr
		}
		if result.RowsAffected() != 1 {
			return errOAuthAuthorizationSuperseded
		}
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO oauth_connections(organization_id,platform_account_id,access_token_ciphertext,refresh_token_ciphertext,nonce,access_token_nonce,refresh_token_nonce,scopes,expires_at,last_refreshed_at,status) VALUES($1,$2,$3,$4,$5,$5,$6,$7,$8,now(),'ACTIVE') ON CONFLICT(platform_account_id) DO UPDATE SET access_token_ciphertext=excluded.access_token_ciphertext,refresh_token_ciphertext=excluded.refresh_token_ciphertext,nonce=excluded.nonce,access_token_nonce=excluded.access_token_nonce,refresh_token_nonce=excluded.refresh_token_nonce,scopes=excluded.scopes,expires_at=excluded.expires_at,last_refreshed_at=now(),status='ACTIVE',disconnect_requested_at=NULL,purge_after=NULL,updated_at=now()`, organizationID, accountID, access, refresh, accessNonce, refreshNonce, token.Scopes, expiresAt)
		if err != nil {
			return err
		}
	}
	if provider.ID == "YOUTUBE" || provider.ID == "INSTAGRAM" || provider.ID == "TIKTOK" {
		if _, err = tx.Exec(ctx, `INSERT INTO sync_targets(organization_id,target_type,target_id,operation,cadence,next_sync_at,status) VALUES($1,'PLATFORM_ACCOUNT',$2,$3,interval '6 hours',now(),'ACTIVE') ON CONFLICT(organization_id,target_id,operation) WHERE status='ACTIVE' DO UPDATE SET next_sync_at=now(),status='ACTIVE',last_error=NULL`, organizationID, accountID, provider.ID+"_IMPORT"); err != nil {
			return err
		}
	}
	if err = s.writeAudit(ctx, tx, auditRecord{
		OrganizationID: organizationID,
		CompanyID:      &companyID,
		Action:         "CONNECT_" + provider.ID,
		EntityType:     "PLATFORM_ACCOUNT",
		EntityID:       &accountID,
		Metadata:       map[string]any{"scopes": token.Scopes},
	}); err != nil {
		return err
	}
	return nil
}

func setOAuthReconnectAuthorization(profile *platformProfile, reconnect *oauthReconnectAuthorization) {
	if reconnect == nil {
		return
	}
	if profile.Metadata == nil {
		profile.Metadata = make(map[string]any)
	}
	profile.Metadata[oauthReconnectAccountMetadataKey] = reconnect.AccountID
	profile.Metadata[oauthReconnectGenerationMetadataKey] = reconnect.Generation
}

func takeOAuthReconnectAuthorization(profile *platformProfile) (*oauthReconnectAuthorization, error) {
	if profile.Metadata == nil {
		return nil, nil
	}
	accountValue, hasAccount := profile.Metadata[oauthReconnectAccountMetadataKey]
	generationValue, hasGeneration := profile.Metadata[oauthReconnectGenerationMetadataKey]
	delete(profile.Metadata, oauthReconnectAccountMetadataKey)
	delete(profile.Metadata, oauthReconnectGenerationMetadataKey)
	if !hasAccount && !hasGeneration {
		return nil, nil
	}
	accountID, ok := accountValue.(string)
	if !ok || accountID == "" || !hasGeneration {
		return nil, errOAuthAuthorizationSuperseded
	}
	var generation int64
	switch value := generationValue.(type) {
	case int64:
		generation = value
	case float64:
		generation = int64(value)
	case json.Number:
		generation, _ = value.Int64()
	}
	if generation <= 0 {
		return nil, errOAuthAuthorizationSuperseded
	}
	return &oauthReconnectAuthorization{AccountID: accountID, Generation: generation}, nil
}

// verifyOAuthReconnectIdentity prevents a newly authorized provider account
// from replacing a REAUTH_REQUIRED account. A fresh connection is still
// allowed when no reconnect is pending for this creator/platform.
func verifyOAuthReconnectIdentity(ctx context.Context, tx pgx.Tx, organizationID, creatorID, platform, externalID string) error {
	rows, err := tx.Query(ctx, `SELECT account.external_id FROM platform_accounts account
		JOIN creator_account_assignments assignment ON assignment.platform_account_id=account.id AND assignment.valid_to IS NULL
		JOIN oauth_connections connection ON connection.platform_account_id=account.id AND connection.organization_id=account.organization_id
		WHERE account.organization_id=$1 AND assignment.creator_id=$2 AND account.platform=$3
		  AND account.status='REAUTH_REQUIRED' AND connection.status='REAUTH_REQUIRED'
		FOR UPDATE OF account`, organizationID, creatorID, platform)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var original string
		if err = rows.Scan(&original); err != nil {
			return err
		}
		if original != externalID {
			return errOAuthReconnectIdentityMismatch
		}
	}
	return rows.Err()
}

func verifyOAuthReconnectAuthorization(ctx context.Context, tx pgx.Tx, organizationID, creatorID, platform, externalID string, reconnect oauthReconnectAuthorization) error {
	var currentExternalID string
	err := tx.QueryRow(ctx, `
		SELECT account.external_id
		FROM platform_accounts account
		JOIN creator_account_assignments assignment
		  ON assignment.platform_account_id=account.id
		 AND assignment.organization_id=account.organization_id
		 AND assignment.valid_to IS NULL
		JOIN oauth_connections connection
		  ON connection.platform_account_id=account.id
		 AND connection.organization_id=account.organization_id
		WHERE account.id=$1 AND account.organization_id=$2 AND assignment.creator_id=$3
		  AND account.platform=$4 AND connection.authorization_generation=$5
		FOR UPDATE OF connection`, reconnect.AccountID, organizationID, creatorID, platform, reconnect.Generation).Scan(&currentExternalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errOAuthAuthorizationSuperseded
	}
	if err != nil {
		return err
	}
	if currentExternalID != externalID {
		return errOAuthReconnectIdentityMismatch
	}
	return nil
}

func (s *Server) resumeVerifiedOAuthReconnect(ctx context.Context, organizationID, platform, externalID string) error {
	var accountID string
	err := s.pool.QueryRow(ctx, `SELECT account.id::text FROM platform_accounts account
		JOIN oauth_connections connection ON connection.platform_account_id=account.id AND connection.organization_id=account.organization_id
		WHERE account.organization_id=$1 AND account.platform=$2 AND account.external_id=$3
		  AND account.status='ACTIVE' AND connection.status='ACTIVE'`, organizationID, platform, externalID).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.ResumeContentPublishAfterReconnectForAccount(ctx, organizationID, accountID)
	return err
}

func (s *Server) resumeVerifiedOAuthReconnectGeneration(ctx context.Context, organizationID, platform, externalID string, reconnect oauthReconnectAuthorization) error {
	var accountID string
	err := s.pool.QueryRow(ctx, `SELECT account.id::text FROM platform_accounts account
		JOIN oauth_connections connection ON connection.platform_account_id=account.id AND connection.organization_id=account.organization_id
		WHERE account.id=$1 AND account.organization_id=$2 AND account.platform=$3 AND account.external_id=$4
		  AND connection.authorization_generation=$5
		  AND account.status='ACTIVE' AND connection.status='ACTIVE'`, reconnect.AccountID, organizationID, platform, externalID, reconnect.Generation).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errOAuthAuthorizationSuperseded
	}
	if err != nil {
		return err
	}
	_, err = s.ResumeContentPublishAfterReconnectForAccount(ctx, organizationID, accountID)
	return err
}

// OAuth provider work happens outside a database transaction. The final save
// must therefore serialize with company archival and repeat the active-state
// decision after any concurrent archive transaction finishes. archiveCompany
// takes FOR UPDATE on the same company row; FOR KEY SHARE lets concurrent
// connections for an active company proceed while still conflicting with that
// lifecycle transition.
func lockActiveCompanyTarget(ctx context.Context, tx pgx.Tx, organizationID, companyID string) error {
	// Workspace deletion locks the organization before cascading into companies.
	// Take the same lock first, so a callback/authorize request that was already
	// in flight cannot create credentials after the workspace became DELETING.
	var lockedOrganizationID string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM organizations
		WHERE id=$1 AND lifecycle_state='ACTIVE'
		FOR KEY SHARE`, organizationID).Scan(&lockedOrganizationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errOAuthTargetUnavailable
	}
	if err != nil {
		return err
	}
	var lockedCompanyID string
	err = tx.QueryRow(ctx, `
		SELECT id::text FROM companies
		WHERE id=$1 AND organization_id=$2
		  AND lifecycle_state='ACTIVE' AND archived_at IS NULL
		FOR KEY SHARE`, companyID, organizationID).Scan(&lockedCompanyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errOAuthTargetUnavailable
	}
	return err
}

func lockActiveCreatorCompanyTarget(ctx context.Context, tx pgx.Tx, organizationID, creatorID string) (string, error) {
	var companyID string
	err := tx.QueryRow(ctx, `
		SELECT company_id::text FROM creators
		WHERE id=$1 AND organization_id=$2
		  AND status='ACTIVE' AND archived_at IS NULL`, creatorID, organizationID).Scan(&companyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errOAuthTargetUnavailable
	}
	if err != nil {
		return "", err
	}
	// Lifecycle workers lock the company before cascading into creator-owned
	// rows. Follow that same order: the first creator read only resolves the
	// company, then the locked read below repeats the creator decision.
	if err = lockActiveCompanyTarget(ctx, tx, organizationID, companyID); err != nil {
		return "", err
	}
	var recheckedCompanyID string
	err = tx.QueryRow(ctx, `
		SELECT company_id::text FROM creators
		WHERE id=$1 AND organization_id=$2 AND company_id=$3
		  AND status='ACTIVE' AND archived_at IS NULL
		FOR KEY SHARE`, creatorID, organizationID, companyID).Scan(&recheckedCompanyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errOAuthTargetUnavailable
	}
	if err != nil {
		return "", err
	}
	return companyID, nil
}

func lockActiveCompanyVKTarget(ctx context.Context, tx pgx.Tx, organizationID, companyVKAccountID string) (string, error) {
	var companyID string
	err := tx.QueryRow(ctx, `
		SELECT company_id::text FROM company_vk_accounts
		WHERE id=$1 AND organization_id=$2`, companyVKAccountID, organizationID).Scan(&companyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errOAuthTargetUnavailable
	}
	if err != nil {
		return "", err
	}
	if err = lockActiveCompanyTarget(ctx, tx, organizationID, companyID); err != nil {
		return "", err
	}
	var recheckedCompanyID string
	err = tx.QueryRow(ctx, `
		SELECT company_id::text FROM company_vk_accounts
		WHERE id=$1 AND organization_id=$2 AND company_id=$3
		FOR KEY SHARE`, companyVKAccountID, organizationID, companyID).Scan(&recheckedCompanyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errOAuthTargetUnavailable
	}
	if err != nil {
		return "", err
	}
	return companyID, nil
}

func (s *Server) platformConnections(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	creatorID := chi.URLParam(r, "id")
	rows, err := s.pool.Query(r.Context(), `SELECT a.id,a.platform,a.username,a.display_name,a.status,COALESCE(a.avatar_url,''),COALESCE(a.profile_url,''),COALESCE(connection.scopes,'{}'),a.last_synced_at,COALESCE(connection.status,''),a.metadata,COALESCE(st.last_error,a.last_error,''),COALESCE(st.consecutive_failures,0),st.last_success_at FROM platform_accounts a JOIN creator_account_assignments assignment ON assignment.platform_account_id=a.id AND assignment.organization_id=a.organization_id AND assignment.valid_to IS NULL JOIN creators creator ON creator.id=assignment.creator_id AND creator.organization_id=assignment.organization_id LEFT JOIN oauth_connections connection ON connection.platform_account_id=a.id AND connection.organization_id=a.organization_id LEFT JOIN LATERAL (SELECT last_error,consecutive_failures,last_success_at FROM sync_targets WHERE target_id=a.id AND organization_id=a.organization_id AND company_id=a.company_id AND status='ACTIVE' ORDER BY next_sync_at DESC LIMIT 1) st ON true WHERE assignment.creator_id=$1 AND a.organization_id=$2 AND a.company_id=creator.company_id AND a.status<>'DISCONNECTED' ORDER BY a.platform,a.created_at DESC`, creatorID, p.OrganizationID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "connections failed", "could not load platform connections")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, platform, username, display, status, avatar, profileURL, oauthStatus, syncError string
		var scopes []string
		var metadata []byte
		var synced, lastSuccessAt *time.Time
		var consecutiveFailures int
		if err := rows.Scan(&id, &platform, &username, &display, &status, &avatar, &profileURL, &scopes, &synced, &oauthStatus, &metadata, &syncError, &consecutiveFailures, &lastSuccessAt); err != nil {
			problem(w, http.StatusInternalServerError, "connections failed", "could not read platform connection")
			return
		}
		values := map[string]any{}
		if err := json.Unmarshal(metadata, &values); err != nil {
			problem(w, http.StatusInternalServerError, "connections failed", "could not read platform connection metadata")
			return
		}
		items = append(items, map[string]any{"id": id, "platform": platform, "username": username, "displayName": display, "status": status, "oauthStatus": oauthStatus, "avatarUrl": avatar, "profileUrl": profileURL, "scopes": scopes, "lastSyncedAt": synced, "lastSuccessAt": lastSuccessAt, "syncError": syncError, "consecutiveFailures": consecutiveFailures, "bioDescription": values["bioDescription"], "isVerified": values["isVerified"]})
	}
	if err := rows.Err(); err != nil {
		problem(w, http.StatusInternalServerError, "connections failed", "could not finish reading platform connections")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) integrationStatus(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	counts := map[string]int64{}
	countScopeSQL, countScopeArgs, ok := managementCompanyScope(p, "account", 2)
	if !ok {
		problem(w, http.StatusForbidden, "forbidden", "an assigned active company is required")
		return
	}
	countArgs := append([]any{p.OrganizationID}, countScopeArgs...)
	rows, err := s.pool.Query(r.Context(), `SELECT account.platform,count(*) FROM platform_accounts account WHERE account.organization_id=$1 AND account.status='ACTIVE'`+countScopeSQL+` GROUP BY account.platform`, countArgs...)
	if err != nil {
		problem(w, http.StatusInternalServerError, "integrations failed", "could not load integration counts")
		return
	}
	for rows.Next() {
		var platform string
		var count int64
		if err = rows.Scan(&platform, &count); err != nil {
			rows.Close()
			problem(w, http.StatusInternalServerError, "integrations failed", "could not read integration counts")
			return
		}
		counts[platform] = count
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		problem(w, http.StatusInternalServerError, "integrations failed", "could not finish reading integration counts")
		return
	}
	rows.Close()
	items := make([]map[string]any, 0, 4)
	for _, key := range []string{"youtube", "instagram", "tiktok", "vk"} {
		provider := s.oauthProviders()[key]
		items = append(items, map[string]any{"id": provider.ID, "name": provider.Name, "configured": s.envelope != nil && provider.ClientID != "" && provider.ClientSecret != "" && provider.RedirectURL != "", "connectedAccounts": counts[provider.ID]})
	}

	connectionScopeSQL, connectionScopeArgs, ok := managementCompanyScope(p, "a", 2)
	if !ok {
		problem(w, http.StatusForbidden, "forbidden", "an assigned active company is required")
		return
	}
	connectionArgs := append([]any{p.OrganizationID}, connectionScopeArgs...)
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
				AND cr.organization_id = ca.organization_id
				AND cr.company_id = a.company_id
			LEFT JOIN oauth_connections o ON o.platform_account_id = a.id AND o.organization_id = a.organization_id
		LEFT JOIN LATERAL (
			SELECT consecutive_failures, last_success_at
			FROM sync_targets
				WHERE target_id = a.id
					AND organization_id = a.organization_id
					AND company_id = a.company_id
			ORDER BY next_sync_at DESC
			LIMIT 1
		) st ON true
			WHERE a.organization_id = $1`+connectionScopeSQL+`
		ORDER BY
			CASE
				WHEN a.status <> 'ACTIVE' OR COALESCE(o.status, '') <> 'ACTIVE' OR a.last_error <> '' THEN 0
				WHEN COALESCE(st.consecutive_failures, 0) > 0 THEN 1
				ELSE 2
			END,
			cr.display_name,
			a.platform
	`, connectionArgs...)
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
		case consecutiveFailures > 0:
			health, message = "WARNING", fmt.Sprintf(localized(r, "Ошибок синхронизации подряд: %d", "Consecutive synchronization failures: %d"), consecutiveFailures)
		case lastError != "":
			message = lastError
			if englishRequest(r) && containsCyrillic(message) {
				message = "Synchronization failed"
			}
			health = "ERROR"
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
		if err = s.commitAuditOnly(r.Context(), w, requestAuditRecord(r, p, nil, "DISCONNECT_PLATFORM_NOOP", "PLATFORM_ACCOUNT", &accountID, http.StatusNoContent, map[string]any{"changed": false})); err != nil {
			problem(w, http.StatusInternalServerError, "disconnection failed", "could not record disconnection")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "disconnection failed", "could not start transaction")
		return
	}
	defer tx.Rollback(r.Context())

	var platform, status, companyID string
	var hasOAuth bool
	err = tx.QueryRow(r.Context(), `
		SELECT a.platform,a.status,a.company_id::text,EXISTS(SELECT 1 FROM oauth_connections c WHERE c.platform_account_id=a.id AND c.organization_id=a.organization_id)
		FROM platform_accounts a
		WHERE a.id=$1 AND a.organization_id=$2
		FOR UPDATE
	`, accountID, p.OrganizationID).Scan(&platform, &status, &companyID, &hasOAuth)
	if errors.Is(err, pgx.ErrNoRows) {
		if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, nil, "DISCONNECT_PLATFORM_NOOP", "PLATFORM_ACCOUNT", &accountID, http.StatusNoContent, map[string]any{"changed": false})); err != nil {
			problem(w, http.StatusInternalServerError, "disconnection failed", "could not record disconnection")
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			problem(w, http.StatusInternalServerError, "disconnection failed", "could not commit disconnection")
			return
		}
		markResponseAuditCommitted(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "disconnection failed", "could not lock platform connection")
		return
	}
	if err = cancelContentPublishesForAccountReassignmentTx(r.Context(), tx, p.OrganizationID, accountID, ""); err != nil {
		problem(w, http.StatusInternalServerError, "disconnect failed", "could not cancel queued publications")
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
	changed := status != "DISCONNECTED" || hasOAuth
	if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &companyID, "DISCONNECT_"+platform, "PLATFORM_ACCOUNT", &accountID, http.StatusNoContent, map[string]any{"changed": changed})); err != nil {
		problem(w, http.StatusInternalServerError, "disconnection failed", "could not record disconnection")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "disconnection failed", "could not commit disconnection")
		return
	}
	// Provider revocation is best-effort and deliberately happens only after the
	// local state commits. For a shared Facebook user grant, the helper elects
	// the last disconnected copy under a grant-scoped advisory lock.
	_ = s.revokeDisconnectedPlatform(r.Context(), connection)
	markResponseAuditCommitted(w)
	w.WriteHeader(http.StatusNoContent)
}

type platformRevocationConnection struct {
	platform                    string
	accessCipher, accessNonce   []byte
	refreshCipher, refreshNonce []byte
	facebookLogin               bool
	facebookUserID              string
	revokeAllowed               bool
}

func (s *Server) platformConnectionForRevocation(ctx context.Context, accountID, organizationID string) (platformRevocationConnection, bool, error) {
	var connection platformRevocationConnection
	err := s.pool.QueryRow(ctx, `
		SELECT a.platform,
			COALESCE(c.access_token_ciphertext,''::bytea),COALESCE(c.access_token_nonce,''::bytea),
			COALESCE(c.refresh_token_ciphertext,''::bytea),COALESCE(c.refresh_token_nonce,''::bytea),
			COALESCE(a.metadata->>'connectionMode','')='FACEBOOK',
			COALESCE(a.metadata->>'facebookUserId',''),
			CASE
				WHEN COALESCE(a.metadata->>'connectionMode','')<>'FACEBOOK' OR COALESCE(a.metadata->>'facebookUserId','')='' THEN true
				ELSE NOT EXISTS(
					SELECT 1
					FROM platform_accounts other
					JOIN oauth_connections other_connection ON other_connection.platform_account_id=other.id AND other_connection.organization_id=other.organization_id
					WHERE other.id<>a.id AND other.platform='INSTAGRAM'
						AND other.status<>'DISCONNECTED' AND COALESCE(other.metadata->>'connectionMode','')='FACEBOOK'
						AND other.metadata->>'facebookUserId'=a.metadata->>'facebookUserId'
				)
			END
		FROM platform_accounts a
		LEFT JOIN oauth_connections c ON c.platform_account_id=a.id AND c.organization_id=a.organization_id
		WHERE a.id=$1 AND a.organization_id=$2
	`, accountID, organizationID).Scan(&connection.platform, &connection.accessCipher, &connection.accessNonce, &connection.refreshCipher, &connection.refreshNonce, &connection.facebookLogin, &connection.facebookUserID, &connection.revokeAllowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return platformRevocationConnection{}, false, nil
	}
	return connection, err == nil, err
}

// revokeDisconnectedPlatform serializes the "last copy" decision for a
// Facebook user grant. It is called after a successful local disconnect/purge:
// two concurrent requests can therefore never both decide that the other copy
// will revoke the provider credential.
func (s *Server) revokeDisconnectedPlatform(ctx context.Context, connection platformRevocationConnection) error {
	token := s.platformRevocationToken(connection)
	if token == "" {
		return nil
	}
	if !connection.facebookLogin || connection.facebookUserID == "" {
		return s.revokePlatform(ctx, connection.platform, token, connection.facebookLogin)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "facebook-user-grant:"+connection.facebookUserID); err != nil {
		return err
	}
	var stillUsed bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM platform_accounts a
			JOIN oauth_connections c ON c.platform_account_id=a.id AND c.organization_id=a.organization_id
			WHERE a.platform='INSTAGRAM' AND a.status<>'DISCONNECTED'
			  AND COALESCE(a.metadata->>'connectionMode','')='FACEBOOK'
			  AND COALESCE(a.metadata->>'facebookUserId','')=$1
		)`, connection.facebookUserID).Scan(&stillUsed)
	if err != nil {
		return err
	}
	if !stillUsed {
		err = s.revokePlatform(ctx, connection.platform, token, true)
	}
	if commitErr := tx.Commit(ctx); err == nil {
		err = commitErr
	}
	return err
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
	if connection.platform == "YOUTUBE" || (connection.platform == "INSTAGRAM" && connection.facebookLogin) {
		if token := decrypt(connection.refreshCipher, connection.refreshNonce); token != "" {
			return token
		}
	}
	return decrypt(connection.accessCipher, connection.accessNonce)
}

func (s *Server) revokePlatform(ctx context.Context, platform, accessToken string, facebookLogin bool) error {
	switch platform {
	case "TIKTOK":
		return s.revokeTikTok(ctx, accessToken)
	case "YOUTUBE":
		return doOAuthForm(ctx, strings.TrimRight(s.config.YouTubeOAuthBase, "/")+"/revoke", url.Values{"token": {accessToken}}, &map[string]any{})
	case "INSTAGRAM":
		apiBase := s.config.InstagramAPIBase
		if facebookLogin {
			apiBase = s.config.InstagramFacebookGraphAPIBase
		}
		return doJSON(ctx, http.MethodDelete, strings.TrimRight(apiBase, "/")+"/me/permissions?"+url.Values{"access_token": {accessToken}}.Encode(), "", &map[string]any{})
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
		if err = s.commitAuditOnly(r.Context(), w, requestAuditRecord(r, p, nil, "PURGE_PLATFORM_DATA_NOOP", "PLATFORM_ACCOUNT", &accountID, http.StatusNoContent, map[string]any{"changed": false})); err != nil {
			problem(w, http.StatusInternalServerError, "deletion failed", "could not record deletion")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
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
	var companyID string
	if err = tx.QueryRow(r.Context(), `SELECT platform,company_id::text FROM platform_accounts WHERE id=$1 AND organization_id=$2 FOR UPDATE`, accountID, p.OrganizationID).Scan(&platform, &companyID); errors.Is(err, pgx.ErrNoRows) {
		if err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, nil, "PURGE_PLATFORM_DATA_NOOP", "PLATFORM_ACCOUNT", &accountID, http.StatusNoContent, map[string]any{"changed": false})); err != nil {
			problem(w, http.StatusInternalServerError, "deletion failed", "could not record deletion")
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			problem(w, http.StatusInternalServerError, "deletion failed", "could not commit deletion")
			return
		}
		markResponseAuditCommitted(w)
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
		err = s.writeAudit(r.Context(), tx, requestAuditRecord(r, p, &companyID, "PURGE_"+platform+"_DATA", "PLATFORM_ACCOUNT", &accountID, http.StatusNoContent, map[string]any{"deletedAccount": accountID}))
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
	_ = s.revokeDisconnectedPlatform(r.Context(), connection)
	markResponseAuditCommitted(w)
	w.WriteHeader(http.StatusNoContent)
}

func splitScopes(value string, _ []string) []string {
	scopes := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' })
	if len(scopes) == 0 {
		// The authorization request is not proof that the provider granted every
		// requested permission. Keep unknown grants as a non-nil empty set so DB
		// writes remain valid and publishing preflight fails closed.
		return []string{}
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
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: rejectProviderRedirect}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, errProviderRedirect) {
			return errProviderRedirect
		}
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
