package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const instagramProviderMediaURLLifetime = 2 * time.Hour

// instagramPublishAdapter persists a phase-qualified operation ID.  A container
// is never created once ig:container is durable; after media_publish succeeds
// we first persist ig:media and only then fetch its permalink on the next poll.
type instagramPublishAdapter struct {
	s                *Server
	accessToken      func(context.Context, PublishRequest) (string, error)
	loadTarget       func(context.Context, PublishRequest) (instagramPublishTarget, error)
	providerMediaURL func(context.Context, string) (string, error)
	persistDispatch  func(context.Context, PublishRequest, string, string) error
}

type instagramPublishTarget struct {
	AccountExternalID string
	AccountType       string
	ConnectionMode    string
	Caption           string
	ObjectKey         string
}

func newInstagramPublishAdapter(s *Server) *instagramPublishAdapter {
	a := &instagramPublishAdapter{s: s}
	a.accessToken = a.defaultAccessToken
	a.loadTarget = a.defaultLoadTarget
	a.providerMediaURL = func(ctx context.Context, key string) (string, error) {
		return s.providerSafeMediaURL(ctx, key, instagramProviderMediaURLLifetime)
	}
	a.persistDispatch = a.defaultPersistDispatch
	return a
}

func (a *instagramPublishAdapter) Publish(ctx context.Context, request PublishRequest) (PublishResult, error) {
	if strings.TrimSpace(request.ProviderOperationID) != "" {
		return a.Poll(ctx, request)
	}
	if !a.s.config.ContentPublishingEnabled || !a.s.config.ContentInstagramEnabled {
		return PublishResult{}, instagramPublishError(providerPermanent, "Instagram content publishing is disabled")
	}
	token, err := a.accessToken(ctx, request)
	if err != nil {
		return PublishResult{}, normalizeInstagramError(err)
	}
	target, err := a.loadTarget(ctx, request)
	if err != nil {
		return PublishResult{}, normalizeInstagramError(err)
	}
	if !instagramProfessional(target.AccountType) {
		return PublishResult{}, instagramPublishError(providerPermanent, "Instagram publishing requires a Professional Business or Creator account")
	}
	videoURL, err := a.providerMediaURL(ctx, target.ObjectKey)
	if err != nil {
		return PublishResult{}, instagramPublishError(providerRetryable, "could not create provider-safe Instagram media URL")
	}
	if err = validateInstagramMediaURL(videoURL, a.s.config.MediaPublicBaseURL); err != nil {
		return PublishResult{}, err
	}
	// Container creation is not idempotent.  Checkpoint the intent before the
	// POST so a lost response can never make a later execution create a second
	// container blindly.
	intent := instagramCreateIntent(request.TargetID)
	if err = a.persistDispatch(ctx, request, request.ProviderOperationID, intent); err != nil {
		return PublishResult{}, err
	}
	request.ProviderOperationID = intent
	var out struct {
		ID string `json:"id"`
	}
	if err = a.doJSONOneShot(ctx, target.ConnectionMode, http.MethodPost, "/"+url.PathEscape(target.AccountExternalID)+"/media", token, url.Values{
		"media_type": {"REELS"}, "video_url": {videoURL}, "caption": {target.Caption},
	}, &out); err != nil {
		return PublishResult{ProviderOperationID: intent}, err
	}
	if strings.TrimSpace(out.ID) == "" {
		return PublishResult{ProviderOperationID: intent}, instagramPublishError(providerSchema, "Instagram did not return a Reels container ID; manual reconciliation is required")
	}
	container := instagramContainerOperation(out.ID)
	if err = a.persistDispatch(ctx, request, intent, container); err != nil {
		return PublishResult{ProviderOperationID: intent}, err
	}
	return PublishResult{ProviderOperationID: container, Pending: true}, nil
}

func (a *instagramPublishAdapter) Poll(ctx context.Context, request PublishRequest) (PublishResult, error) {
	phase, id := parseInstagramOperation(request.ProviderOperationID)
	if phase == "create" {
		return PublishResult{ProviderOperationID: request.ProviderOperationID}, instagramPublishError(providerPermanent, "Instagram container creation outcome is uncertain; manual reconciliation is required")
	}
	token, err := a.accessToken(ctx, request)
	if err != nil {
		return PublishResult{}, normalizeInstagramError(err)
	}
	target, err := a.loadTarget(ctx, request)
	if err != nil {
		return PublishResult{}, normalizeInstagramError(err)
	}
	switch phase {
	case "container":
		var state struct {
			StatusCode string `json:"status_code"`
			Status     string `json:"status"`
		}
		if err = a.doJSON(ctx, target.ConnectionMode, http.MethodGet, "/"+url.PathEscape(id)+"?fields=status_code,status", token, nil, &state); err != nil {
			return PublishResult{}, err
		}
		switch strings.ToUpper(strings.TrimSpace(firstNonEmpty(state.StatusCode, state.Status))) {
		case "FINISHED", "READY", "PUBLISHED":
			intent := instagramPublishDispatchOperation(id)
			if err = a.persistDispatch(ctx, request, request.ProviderOperationID, intent); err != nil {
				return PublishResult{}, err
			}
			var published struct {
				ID string `json:"id"`
			}
			if err = a.doJSONOnce(ctx, target.ConnectionMode, http.MethodPost, "/"+url.PathEscape(target.AccountExternalID)+"/media_publish", token, url.Values{"creation_id": {id}}, &published); err != nil {
				return PublishResult{ProviderOperationID: intent}, err
			}
			if published.ID == "" {
				return PublishResult{}, instagramPublishError(providerSchema, "Instagram did not return a published media ID")
			}
			return PublishResult{ProviderOperationID: instagramMediaOperation(published.ID), Pending: true}, nil
		case "ERROR", "EXPIRED", "FAILED":
			return PublishResult{}, instagramPublishError(providerPermanent, "Instagram rejected the Reels container")
		default:
			return PublishResult{ProviderOperationID: request.ProviderOperationID, Pending: true}, nil
		}
	case "publish-dispatched":
		return PublishResult{ProviderOperationID: request.ProviderOperationID}, instagramPublishError(providerPermanent, "Instagram publish outcome is uncertain; investigate the existing container before retrying")
	case "media":
		var media struct {
			ID        string `json:"id"`
			Permalink string `json:"permalink"`
		}
		if err = a.doJSON(ctx, target.ConnectionMode, http.MethodGet, "/"+url.PathEscape(id)+"?fields=id,permalink", token, nil, &media); err != nil {
			return PublishResult{}, err
		}
		if media.ID == "" {
			return PublishResult{}, instagramPublishError(providerSchema, "Instagram published media ID is unavailable")
		}
		return PublishResult{ProviderOperationID: request.ProviderOperationID, ExternalID: media.ID, ExternalURL: media.Permalink}, nil
	default:
		return PublishResult{}, instagramPublishError(providerPermanent, "Instagram publishing operation is invalid")
	}
}

func (a *instagramPublishAdapter) defaultAccessToken(ctx context.Context, r PublishRequest) (string, error) {
	token, _, err := a.s.accessTokenForSync(ctx, platformSyncJob{OrganizationID: r.OrganizationID, CompanyID: r.CompanyID, AccountID: r.AccountID, Platform: "INSTAGRAM"})
	return token, err
}
func (a *instagramPublishAdapter) defaultLoadTarget(_ context.Context, r PublishRequest) (instagramPublishTarget, error) {
	snapshot, err := decodeContentPublishSnapshot(r.Snapshot)
	if err != nil {
		return instagramPublishTarget{}, instagramPublishError(providerPermanent, "Instagram publish snapshot is unavailable")
	}
	return instagramPublishTarget{AccountExternalID: snapshot.AccountExternalID, AccountType: snapshot.AccountType, ConnectionMode: snapshot.ConnectionMode, Caption: snapshot.Caption, ObjectKey: snapshot.MediaObjectKey}, nil
}
func (a *instagramPublishAdapter) defaultPersistDispatch(ctx context.Context, request PublishRequest, expected, intent string) error {
	command, err := a.s.pool.Exec(ctx, `UPDATE content_publish_targets SET provider_operation_id=$3,updated_at=now() WHERE id=$1 AND organization_id=$2 AND COALESCE(provider_operation_id,'')=$4`, request.TargetID, request.OrganizationID, intent, expected)
	if err != nil {
		return instagramPublishError(providerRetryable, "could not persist Instagram publish intent")
	}
	if command.RowsAffected() != 1 {
		return instagramPublishError(providerPermanent, "Instagram publish operation changed concurrently")
	}
	return nil
}
func (a *instagramPublishAdapter) doJSON(ctx context.Context, connectionMode, method, path, token string, form url.Values, out any) error {
	return a.doJSONWithRetries(ctx, connectionMode, method, path, token, form, out, 3)
}
func (a *instagramPublishAdapter) doJSONOnce(ctx context.Context, connectionMode, method, path, token string, form url.Values, out any) error {
	return a.doJSONWithRetries(ctx, connectionMode, method, path, token, form, out, 1)
}
func (a *instagramPublishAdapter) doJSONOneShot(ctx context.Context, connectionMode, method, path, token string, form url.Values, out any) error {
	base := a.s.config.InstagramAPIBase
	if strings.EqualFold(connectionMode, "FACEBOOK") {
		base = a.s.config.InstagramFacebookGraphAPIBase
	}
	endpoint := strings.TrimRight(base, "/") + path
	err := newProviderClient("Instagram").JSONWithMode(ctx, method, endpoint, token, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()), out, providerOneShot)
	if err != nil {
		var providerFailure *providerError
		if !errors.As(err, &providerFailure) {
			return instagramPublishError(providerPermanent, "Instagram provider outcome is uncertain; manual reconciliation is required")
		}
	}
	return normalizeInstagramError(err)
}
func (a *instagramPublishAdapter) doJSONWithRetries(ctx context.Context, connectionMode, method, path, token string, form url.Values, out any, retries int) error {
	base := a.s.config.InstagramAPIBase
	if strings.EqualFold(connectionMode, "FACEBOOK") {
		base = a.s.config.InstagramFacebookGraphAPIBase
	}
	endpoint := strings.TrimRight(base, "/") + path
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	client := newProviderClient("Instagram")
	client.retries = retries
	err := client.JSON(ctx, method, endpoint, token, "application/x-www-form-urlencoded", body, out)
	return normalizeInstagramError(err)
}
func instagramProfessional(accountType string) bool {
	v := strings.ToUpper(strings.TrimSpace(accountType))
	return v == "PROFESSIONAL" || v == "BUSINESS" || v == "CREATOR"
}
func instagramContainerOperation(id string) string { return "ig:container:" + strings.TrimSpace(id) }
func instagramMediaOperation(id string) string     { return "ig:media:" + strings.TrimSpace(id) }
func instagramCreateIntent(targetID string) string { return "ig:create:" + strings.TrimSpace(targetID) }
func instagramPublishDispatchOperation(id string) string {
	return "ig:publish-dispatched:" + strings.TrimSpace(id)
}
func parseInstagramOperation(value string) (string, string) {
	p := strings.SplitN(value, ":", 3)
	if len(p) != 3 || p[0] != "ig" || strings.TrimSpace(p[2]) == "" {
		return "", ""
	}
	return p[1], p[2]
}
func instagramPublishError(kind providerErrorKind, message string) error {
	return &providerError{Platform: "Instagram", Kind: kind, Message: message}
}
func normalizeInstagramError(err error) error {
	if err == nil {
		return nil
	}
	var pe *providerError
	if !errors.As(err, &pe) {
		return instagramPublishError(providerRetryable, "Instagram request failed")
	}
	message := "Instagram rejected the publishing request"
	switch pe.Kind {
	case providerAuth:
		message = "Instagram authorization expired; reconnect the account"
	case providerPermission:
		message = "Instagram publishing permission is missing; reconnect the account"
	case providerRateLimit:
		message = "Instagram rate limit reached"
	case providerRetryable:
		message = "Instagram is temporarily unavailable"
	case providerSchema:
		message = "Instagram returned an invalid publishing response"
	case providerPermanent:
		if strings.Contains(strings.ToLower(pe.Message), "uncertain") {
			message = "Instagram provider outcome is uncertain; manual reconciliation is required"
		}
	}
	return &providerError{Platform: "Instagram", Kind: pe.Kind, StatusCode: pe.StatusCode, RetryAfter: pe.RetryAfter, Message: message}
}
func validateInstagramMediaURL(value, mediaBase string) error {
	u, err := url.Parse(value)
	base, baseErr := url.Parse(strings.TrimSpace(mediaBase))
	if err != nil || baseErr != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || base.Scheme != "https" || !strings.EqualFold(u.Host, base.Host) {
		return instagramPublishError(providerPermanent, "Instagram media URL must use the configured HTTPS media domain")
	}
	return nil
}

var _ PublishAdapter = (*instagramPublishAdapter)(nil)
