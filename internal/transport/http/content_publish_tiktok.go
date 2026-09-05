package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const tiktokProviderMediaURLLifetime = 2 * time.Hour

type tiktokCreatorInfo struct {
	CreatorUsername             string   `json:"creator_username"`
	PrivacyLevelOptions         []string `json:"privacy_level_options"`
	CommentDisabled             bool     `json:"comment_disabled"`
	DuetDisabled                bool     `json:"duet_disabled"`
	StitchDisabled              bool     `json:"stitch_disabled"`
	MaxVideoPostDurationSeconds int64    `json:"max_video_post_duration_sec"`
}

type tiktokTargetOptions struct {
	PrivacyLevel         string `json:"privacyLevel"`
	CommentsEnabled      *bool  `json:"commentsEnabled"`
	DuetEnabled          *bool  `json:"duetEnabled"`
	StitchEnabled        *bool  `json:"stitchEnabled"`
	ExplicitConsent      bool   `json:"explicitConsent"`
	MusicUsageConfirmed  bool   `json:"musicUsageConfirmed"`
	CommercialDisclosure *struct {
		BrandContent bool `json:"brandContent"`
		BrandOrganic bool `json:"brandOrganic"`
	} `json:"commercialDisclosure"`
	AIGC bool `json:"isAIGC"`
}

type tiktokPublishAdapter struct {
	s                *Server
	accessToken      func(context.Context, PublishRequest) (string, error)
	creatorInfo      func(context.Context, string) (tiktokCreatorInfo, error)
	loadTarget       func(context.Context, PublishRequest) (string, tiktokTargetOptions, int64, error)
	providerMediaURL func(context.Context, string) (string, error)
	loadMedia        func(context.Context, PublishRequest) (string, int64, string, error)
	persistOperation func(context.Context, PublishRequest, string) error
}

func newTikTokPublishAdapter(s *Server) *tiktokPublishAdapter {
	a := &tiktokPublishAdapter{s: s}
	a.accessToken = a.defaultAccessToken
	a.creatorInfo = a.queryCreatorInfo
	a.loadTarget = a.defaultLoadTarget
	a.loadMedia = a.loadTargetMedia
	a.persistOperation = a.defaultPersistOperation
	a.providerMediaURL = func(ctx context.Context, objectKey string) (string, error) {
		return s.providerSafeMediaURL(ctx, objectKey, tiktokProviderMediaURLLifetime)
	}
	return a
}

func (a *tiktokPublishAdapter) Publish(ctx context.Context, request PublishRequest) (PublishResult, error) {
	if !a.s.config.ContentPublishingEnabled || !a.s.config.ContentTikTokEnabled {
		return PublishResult{}, &providerError{Platform: "TikTok", Kind: providerPermanent, Message: "TikTok content publishing is disabled"}
	}
	token, err := a.accessToken(ctx, request)
	if err != nil {
		return PublishResult{}, err
	}
	// This is intentionally repeated after composer preflight. TikTok requires
	// current creator permissions at the moment the Direct Post is initialized.
	creator, err := a.creatorInfo(ctx, token)
	if err != nil {
		return PublishResult{}, err
	}
	title, options, durationMS, err := a.loadTarget(ctx, request)
	if err != nil {
		return PublishResult{}, err
	}
	if err = validateTikTokOptions(options, creator, durationMS); err != nil {
		return PublishResult{}, err
	}
	_, _, objectKey, err := a.loadMedia(ctx, request)
	if err != nil {
		return PublishResult{}, err
	}
	videoURL, err := a.providerMediaURL(ctx, objectKey)
	if err != nil {
		return PublishResult{}, &providerError{Platform: "TikTok", Kind: providerRetryable, Message: "could not create provider-safe media URL"}
	}
	if err = validateTikTokPullURLForDomain(videoURL, a.s.config.MediaPublicBaseURL); err != nil {
		return PublishResult{}, err
	}
	// Direct Post init is not idempotent. Persist an intent before provider I/O
	// so a broken connection after acceptance can never trigger a blind replay.
	intent := "tiktok:init:" + request.TargetID
	if err = a.persistOperation(ctx, request, intent); err != nil {
		return PublishResult{}, err
	}
	request.ProviderOperationID = intent
	body := map[string]any{
		"post_info": map[string]any{
			"title": title, "privacy_level": options.PrivacyLevel,
			"disable_comment":      !*options.CommentsEnabled,
			"disable_duet":         !*options.DuetEnabled,
			"disable_stitch":       !*options.StitchEnabled,
			"brand_content_toggle": options.CommercialDisclosure.BrandContent,
			"brand_organic_toggle": options.CommercialDisclosure.BrandOrganic,
			"is_aigc":              options.AIGC,
		},
		"source_info": map[string]any{"source": "PULL_FROM_URL", "video_url": videoURL},
	}
	var out struct {
		Data struct {
			PublishID string `json:"publish_id"`
		} `json:"data"`
		Error            json.RawMessage `json:"error"`
		ErrorDescription string          `json:"error_description"`
		LogID            string          `json:"log_id"`
	}
	if err = a.doJSONWithMode(ctx, http.MethodPost, "/v2/post/publish/video/init/", token, body, &out, providerOneShot); err != nil {
		return PublishResult{}, &providerError{Platform: "TikTok", Kind: providerPermanent, Message: "TikTok init outcome is uncertain; manual reconciliation is required"}
	}
	if code, _, _ := tikTokAPIError(out.Error, out.ErrorDescription, out.LogID); code != "" {
		kind := classifyTikTokError(code)
		return PublishResult{}, &providerError{Platform: "TikTok", Kind: kind, Message: safeTikTokProviderMessage(kind)}
	}
	if strings.TrimSpace(out.Data.PublishID) == "" {
		return PublishResult{}, &providerError{Platform: "TikTok", Kind: providerSchema, Message: "TikTok init response did not include publish_id"}
	}
	if err = a.persistOperation(ctx, request, out.Data.PublishID); err != nil {
		return PublishResult{}, err
	}
	return PublishResult{ProviderOperationID: out.Data.PublishID, Pending: true}, nil
}

func (a *tiktokPublishAdapter) Poll(ctx context.Context, request PublishRequest) (PublishResult, error) {
	if strings.TrimSpace(request.ProviderOperationID) == "" {
		return PublishResult{}, &providerError{Platform: "TikTok", Kind: providerPermanent, Message: "TikTok publish_id is missing"}
	}
	if strings.HasPrefix(request.ProviderOperationID, "tiktok:init:") {
		return PublishResult{}, &providerError{Platform: "TikTok", Kind: providerPermanent, Message: "TikTok init outcome is uncertain; manual reconciliation is required"}
	}
	token, err := a.accessToken(ctx, request)
	if err != nil {
		return PublishResult{}, err
	}
	var out struct {
		Data struct {
			Status          string `json:"status"`
			PublicID        string `json:"publicaly_available_post_id"`
			PublicIDCorrect string `json:"publicly_available_post_id"`
			FailReason      string `json:"fail_reason"`
		} `json:"data"`
	}
	if err = a.doJSON(ctx, http.MethodPost, "/v2/post/publish/status/fetch/", token, map[string]string{"publish_id": request.ProviderOperationID}, &out); err != nil {
		return PublishResult{}, err
	}
	publicID := out.Data.PublicID
	if publicID == "" {
		publicID = out.Data.PublicIDCorrect
	}
	switch strings.ToUpper(out.Data.Status) {
	case "PUBLISH_COMPLETE":
		if publicID == "" {
			return PublishResult{}, &providerError{Platform: "TikTok", Kind: providerSchema, Message: "TikTok completed without a post ID"}
		}
		return PublishResult{ProviderOperationID: request.ProviderOperationID, ExternalID: publicID}, nil
	case "FAILED":
		// fail_reason is provider-controlled and may contain capabilities or
		// opaque identifiers. Keep it out of errors, persistence and logs.
		return PublishResult{}, &providerError{Platform: "TikTok", Kind: providerPermanent, Message: "TikTok rejected the publish operation"}
	default:
		return PublishResult{ProviderOperationID: request.ProviderOperationID, Pending: true}, nil
	}
}

func (a *tiktokPublishAdapter) defaultAccessToken(ctx context.Context, request PublishRequest) (string, error) {
	token, _, err := a.s.accessTokenForSync(ctx, platformSyncJob{OrganizationID: request.OrganizationID, CompanyID: request.CompanyID, AccountID: request.AccountID, Platform: "TIKTOK"})
	return token, err
}
func (a *tiktokPublishAdapter) defaultLoadTarget(ctx context.Context, request PublishRequest) (string, tiktokTargetOptions, int64, error) {
	snapshot, err := decodeContentPublishSnapshot(request.Snapshot)
	if err != nil {
		return "", tiktokTargetOptions{}, 0, &providerError{Platform: "TikTok", Kind: providerPermanent, Message: "TikTok publish snapshot is unavailable"}
	}
	var options tiktokTargetOptions
	if err := json.Unmarshal(snapshot.PlatformOptions, &options); err != nil {
		return "", options, 0, &providerError{Platform: "TikTok", Kind: providerPermanent, Message: "TikTok options are invalid"}
	}
	return snapshot.Caption, options, snapshot.MediaDurationMS, nil
}
func (a *tiktokPublishAdapter) loadTargetMedia(ctx context.Context, request PublishRequest) (string, int64, string, error) {
	snapshot, err := decodeContentPublishSnapshot(request.Snapshot)
	if err != nil {
		return "", 0, "", &providerError{Platform: "TikTok", Kind: providerPermanent, Message: "TikTok target media is unavailable"}
	}
	return snapshot.MediaID, snapshot.MediaDurationMS, snapshot.MediaObjectKey, nil
}
func (a *tiktokPublishAdapter) queryCreatorInfo(ctx context.Context, token string) (tiktokCreatorInfo, error) {
	var out struct {
		Data tiktokCreatorInfo `json:"data"`
	}
	if err := a.doJSON(ctx, http.MethodPost, "/v2/post/publish/creator_info/query/", token, map[string]any{}, &out); err != nil {
		return tiktokCreatorInfo{}, err
	}
	return out.Data, nil
}
func (a *tiktokPublishAdapter) doJSON(ctx context.Context, method, endpoint, token string, body any, out any) error {
	return a.doJSONWithMode(ctx, method, endpoint, token, body, out, providerSafeRetry)
}
func (a *tiktokPublishAdapter) doJSONWithMode(ctx context.Context, method, endpoint, token string, body any, out any, mode providerRequestMode) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.s.config.TikTokAPIBase, "/")+endpoint, strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	if mode == providerOneShot {
		return newProviderClient("TikTok").JSONWithMode(ctx, method, req.URL.String(), token, "application/json; charset=UTF-8", strings.NewReader(string(b)), out, mode)
	}
	return a.s.doTikTokJSON(req, out)
}

func (a *tiktokPublishAdapter) defaultPersistOperation(ctx context.Context, r PublishRequest, operation string) error {
	q, err := a.s.pool.Exec(ctx, `UPDATE content_publish_targets SET provider_operation_id=$3,updated_at=now() WHERE id=$1 AND organization_id=$2 AND COALESCE(provider_operation_id,'')=$4`, r.TargetID, r.OrganizationID, operation, r.ProviderOperationID)
	if err != nil || q.RowsAffected() != 1 {
		return &providerError{Platform: "TikTok", Kind: providerRetryable, Message: "could not persist TikTok publish operation"}
	}
	return nil
}
func (s *Server) tiktokCreatorPreflight(ctx context.Context, organizationID, companyID, accountID string) (tiktokCreatorInfo, error) {
	a := newTikTokPublishAdapter(s)
	token, err := a.defaultAccessToken(ctx, PublishRequest{OrganizationID: organizationID, CompanyID: companyID, AccountID: accountID})
	if err != nil {
		return tiktokCreatorInfo{}, err
	}
	return a.queryCreatorInfo(ctx, token)
}
func validateTikTokOptions(o tiktokTargetOptions, c tiktokCreatorInfo, durationMS int64) error {
	if !o.ExplicitConsent || !o.MusicUsageConfirmed || o.CommercialDisclosure == nil {
		return &providerError{Platform: "TikTok", Kind: providerPermanent, Message: "TikTok explicit consent, music confirmation and commercial disclosure are required"}
	}
	if o.PrivacyLevel == "" || !containsString(c.PrivacyLevelOptions, o.PrivacyLevel) {
		return &providerError{Platform: "TikTok", Kind: providerPermanent, Message: "TikTok privacy level must be explicitly selected from creator options"}
	}
	if o.CommentsEnabled == nil || o.DuetEnabled == nil || o.StitchEnabled == nil {
		return &providerError{Platform: "TikTok", Kind: providerPermanent, Message: "TikTok comments, Duet and Stitch settings must be explicitly selected"}
	}
	if c.CommentDisabled && *o.CommentsEnabled || c.DuetDisabled && *o.DuetEnabled || c.StitchDisabled && *o.StitchEnabled {
		return &providerError{Platform: "TikTok", Kind: providerPermanent, Message: "TikTok account does not allow one of the selected interactions"}
	}
	if c.MaxVideoPostDurationSeconds > 0 && durationMS > c.MaxVideoPostDurationSeconds*1000 {
		return &providerError{Platform: "TikTok", Kind: providerPermanent, Message: "video exceeds the creator's TikTok duration limit"}
	}
	return nil
}
func validateTikTokPullURL(value string) error {
	// TikTok pulls directly; a URL with a non-HTTPS scheme, credentials, or a
	// missing host would either leak or require a browser-like redirect flow.
	u, err := http.NewRequest(http.MethodGet, value, nil)
	if err != nil || u.URL.Scheme != "https" || u.URL.Host == "" || u.URL.User != nil {
		return &providerError{Platform: "TikTok", Kind: providerPermanent, Message: "TikTok media URL must be a direct HTTPS URL"}
	}
	return nil
}

func validateTikTokPullURLForDomain(value, mediaBase string) error {
	if err := validateTikTokPullURL(value); err != nil {
		return err
	}
	u, _ := url.Parse(value)
	base, err := url.Parse(strings.TrimSpace(mediaBase))
	if err != nil || base.Scheme != "https" || base.Host == "" || !strings.EqualFold(u.Host, base.Host) {
		return &providerError{Platform: "TikTok", Kind: providerPermanent, Message: "TikTok media URL must use the configured HTTPS media domain"}
	}
	return nil
}
func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

var _ PublishAdapter = (*tiktokPublishAdapter)(nil)
