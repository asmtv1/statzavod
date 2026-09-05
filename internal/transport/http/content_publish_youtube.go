package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// youtubePublishAdapter implements YouTube Data API videos.insert. YouTube
// Shorts are ordinary videos with a vertical/short source; there is no
// separate Shorts publishing API.
type youtubePublishAdapter struct {
	s                *Server
	accessToken      func(context.Context, PublishRequest) (string, error)
	openMedia        func(context.Context, PublishRequest, int64) (io.ReadCloser, int64, error)
	mediaSize        func(context.Context, PublishRequest) (int64, error)
	persistOperation func(context.Context, PublishRequest, string) error
	persistResume    func(context.Context, PublishRequest, string) (string, error)
	loadResume       func(context.Context, PublishRequest) (string, error)
	initHTTP         *http.Client
	sessionHTTP      *http.Client
}

type youtubeTargetOptions struct {
	Title             string `json:"title"`
	Description       string `json:"description"`
	CategoryID        string `json:"categoryId"`
	PrivacyStatus     string `json:"privacyStatus"`
	MadeForKids       *bool  `json:"madeForKids"`
	NotifySubscribers *bool  `json:"notifySubscribers"`
}

func newYouTubePublishAdapter(s *Server) *youtubePublishAdapter {
	a := &youtubePublishAdapter{s: s}
	a.accessToken = func(ctx context.Context, r PublishRequest) (string, error) {
		t, _, err := s.accessTokenForSync(ctx, platformSyncJob{OrganizationID: r.OrganizationID, CompanyID: r.CompanyID, AccountID: r.AccountID, Platform: "YOUTUBE"})
		return t, normalizeYouTubeError(err)
	}
	a.openMedia = s.openPublishMedia
	a.mediaSize = func(ctx context.Context, r PublishRequest) (int64, error) {
		body, n, err := s.openPublishMedia(ctx, r, 0)
		if body != nil {
			body.Close()
		}
		return n, err
	}
	a.persistOperation = a.defaultPersistOperation
	a.persistResume = a.defaultPersistResume
	a.loadResume = a.defaultLoadResume
	provider := newProviderClient("YouTube")
	initHTTP := *provider.client
	initHTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	a.initHTTP = &initHTTP
	a.sessionHTTP = newValidatedProviderHTTPClient(validateYouTubeSessionURL)
	return a
}

func (a *youtubePublishAdapter) Publish(ctx context.Context, r PublishRequest) (PublishResult, error) {
	if strings.TrimSpace(r.ProviderOperationID) != "" {
		return a.Poll(ctx, r)
	}
	if !a.s.config.ContentPublishingEnabled || !a.s.config.ContentYouTubeEnabled {
		return PublishResult{}, youtubePublishError(providerPermanent, "YouTube content publishing is disabled")
	}
	token, err := a.accessToken(ctx, r)
	if err != nil {
		return PublishResult{}, err
	}
	snap, err := decodeContentPublishSnapshot(r.Snapshot)
	if err != nil {
		return PublishResult{}, youtubePublishError(providerPermanent, "YouTube publish snapshot is unavailable")
	}
	var options youtubeTargetOptions
	if json.Unmarshal(snap.PlatformOptions, &options) != nil || strings.TrimSpace(options.Title) == "" || strings.TrimSpace(options.CategoryID) == "" || options.MadeForKids == nil || options.NotifySubscribers == nil || !containsString([]string{"private", "unlisted", "public"}, strings.ToLower(options.PrivacyStatus)) {
		return PublishResult{}, youtubePublishError(providerPermanent, "YouTube title, category, privacy, made-for-kids and subscriber notification options are required")
	}
	mediaSize, err := a.mediaSize(ctx, r)
	if err != nil {
		return PublishResult{}, youtubePublishError(providerRetryable, "YouTube media is unavailable")
	}
	body := map[string]any{"snippet": map[string]any{"title": options.Title, "description": options.Description, "categoryId": options.CategoryID}, "status": map[string]any{"privacyStatus": strings.ToLower(options.PrivacyStatus), "selfDeclaredMadeForKids": *options.MadeForKids}}
	endpoint := strings.TrimRight(a.s.config.YouTubeUploadBase, "/") + "/videos?uploadType=resumable&part=snippet,status&notifySubscribers=" + strconv.FormatBool(*options.NotifySubscribers)
	// Resumable-session initialization is create-like.  Make the intent
	// durable before provider I/O; an uncertain response must never be replayed.
	intent := youtubeInitOperation(r.TargetID)
	if err = a.persistOperation(ctx, r, intent); err != nil {
		return PublishResult{}, err
	}
	r.ProviderOperationID = intent
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(mustJSON(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Upload-Content-Type", "video/*")
	req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(mediaSize, 10))
	resp, err := a.initHTTP.Do(req)
	if err != nil {
		return PublishResult{ProviderOperationID: intent}, youtubePublishError(providerPermanent, "YouTube upload initialization outcome is uncertain; manual reconciliation is required")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return PublishResult{ProviderOperationID: intent}, youtubePublishError(providerPermanent, "YouTube upload initialization outcome is uncertain; manual reconciliation is required")
		}
		return PublishResult{ProviderOperationID: intent}, youtubeResponseError(resp)
	}
	location := strings.TrimSpace(resp.Header.Get("Location"))
	if location == "" {
		return PublishResult{ProviderOperationID: intent}, youtubePublishError(providerSchema, "YouTube did not return a resumable upload session; manual reconciliation is required")
	}
	if err := validateYouTubeSessionURL(location); err != nil {
		return PublishResult{ProviderOperationID: intent}, youtubePublishError(providerPermanent, "YouTube returned an unsafe resumable upload session; manual reconciliation is required")
	}
	op, err := a.persistResume(ctx, r, location)
	if err != nil {
		return PublishResult{}, err
	}
	r.ProviderOperationID = op
	return a.resumeUpload(ctx, r, token, op)
}

func (a *youtubePublishAdapter) Poll(ctx context.Context, r PublishRequest) (PublishResult, error) {
	phase, id := parseYouTubeOperation(r.ProviderOperationID)
	if phase == "init" {
		return PublishResult{ProviderOperationID: r.ProviderOperationID}, youtubePublishError(providerPermanent, "YouTube upload initialization outcome is uncertain; manual reconciliation is required")
	}
	token, err := a.accessToken(ctx, r)
	if err != nil {
		return PublishResult{}, err
	}
	if phase == "session" {
		return a.resumeUpload(ctx, r, token, r.ProviderOperationID)
	}
	if phase != "video" {
		return PublishResult{}, youtubePublishError(providerPermanent, "YouTube publish operation is invalid")
	}
	var out struct {
		Items []struct {
			ID     string `json:"id"`
			Status struct {
				UploadStatus    string `json:"uploadStatus"`
				PrivacyStatus   string `json:"privacyStatus"`
				RejectionReason string `json:"rejectionReason"`
			} `json:"status"`
		} `json:"items"`
	}
	endpoint := strings.TrimRight(a.s.config.YouTubeAPIBase, "/") + "/videos?part=status&id=" + id
	if err := newProviderClient("YouTube").JSON(ctx, http.MethodGet, endpoint, token, "", nil, &out); err != nil {
		return PublishResult{}, normalizeYouTubeError(err)
	}
	if len(out.Items) == 0 {
		return PublishResult{}, youtubePublishError(providerSchema, "YouTube video was not found")
	}
	st := strings.ToUpper(out.Items[0].Status.UploadStatus)
	if st == "FAILED" || st == "REJECTED" {
		return PublishResult{}, youtubePublishError(providerPermanent, "YouTube rejected the uploaded video")
	}
	if st != "PROCESSED" && st != "UPLOADED" {
		return PublishResult{ProviderOperationID: r.ProviderOperationID, Pending: true}, nil
	}
	return PublishResult{ProviderOperationID: r.ProviderOperationID, ExternalID: id, ExternalURL: "https://www.youtube.com/shorts/" + id}, nil
}

func (a *youtubePublishAdapter) resumeUpload(ctx context.Context, r PublishRequest, token, op string) (PublishResult, error) {
	session, err := a.loadResume(ctx, r)
	if err != nil || session == "" {
		return PublishResult{}, youtubePublishError(providerPermanent, "YouTube upload session is invalid")
	}
	if err := validateYouTubeSessionURL(session); err != nil {
		return PublishResult{}, youtubePublishError(providerPermanent, "YouTube upload session URL is unsafe")
	}
	total, err := a.mediaSize(ctx, r)
	if err != nil {
		return PublishResult{}, youtubePublishError(providerRetryable, "YouTube media is unavailable")
	}
	// Query the durable session first. A 308 response tells us the next byte;
	// any network interruption leaves the same session operation for retry.
	next := int64(0)
	statusReq, _ := http.NewRequestWithContext(ctx, http.MethodPut, session, nil)
	statusReq.Header.Set("Authorization", "Bearer "+token)
	statusReq.Header.Set("Content-Range", "bytes */"+strconv.FormatInt(total, 10))
	statusReq.Header.Set("Content-Length", "0")
	if sr, e := a.sessionHTTP.Do(statusReq); e == nil {
		if sr.StatusCode == http.StatusUnauthorized {
			return PublishResult{}, youtubePublishError(providerAuth, "YouTube authorization expired; reconnect the account")
		}
		if sr.StatusCode == 308 {
			next = parseRange(sr.Header.Get("Range")) + 1
		} else if sr.StatusCode == http.StatusOK || sr.StatusCode == http.StatusCreated {
			var uploaded struct {
				ID string `json:"id"`
			}
			if json.NewDecoder(sr.Body).Decode(&uploaded) == nil && uploaded.ID != "" {
				videoOp := youtubeVideoOperation(uploaded.ID)
				if err := a.persistOperation(ctx, r, videoOp); err != nil {
					return PublishResult{}, err
				}
				return PublishResult{ProviderOperationID: videoOp, Pending: true}, nil
			}
			sr.Body.Close()
			return PublishResult{}, youtubePublishError(providerSchema, "YouTube completed upload response was invalid")
		} else if sr.StatusCode == http.StatusNotFound || sr.StatusCode == http.StatusGone {
			sr.Body.Close()
			return PublishResult{}, youtubePublishError(providerPermanent, "YouTube upload session expired; retry requires explicit investigation")
		} else if sr.StatusCode >= 500 {
			sr.Body.Close()
			return PublishResult{ProviderOperationID: op, Pending: true}, youtubePublishError(providerRetryable, "YouTube upload session status is temporarily unavailable")
		}
		sr.Body.Close()
	} else {
		if sr != nil && sr.Body != nil {
			sr.Body.Close()
		}
		if errors.Is(e, errProviderRedirect) {
			return PublishResult{}, youtubePublishError(providerPermanent, "YouTube upload session redirected to an unsafe host")
		}
		return PublishResult{ProviderOperationID: op, Pending: true}, youtubePublishError(providerRetryable, "YouTube upload session status is temporarily unavailable")
	}
	media, total, err := a.openMedia(ctx, r, next)
	if err != nil {
		return PublishResult{}, youtubePublishError(providerRetryable, "YouTube media is unavailable")
	}
	defer media.Close()
	if next >= total {
		return PublishResult{ProviderOperationID: op, Pending: true}, nil
	}
	up, _ := http.NewRequestWithContext(ctx, http.MethodPut, session, media)
	up.Header.Set("Authorization", "Bearer "+token)
	up.Header.Set("Content-Type", "video/*")
	up.Header.Set("Content-Length", strconv.FormatInt(total-next, 10))
	up.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", next, total-1, total))
	resp, e := a.sessionHTTP.Do(up)
	if e != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		if errors.Is(e, errProviderRedirect) {
			return PublishResult{}, youtubePublishError(providerPermanent, "YouTube upload session redirected to an unsafe host")
		}
		return PublishResult{ProviderOperationID: op, Pending: true}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode == 308 {
		return PublishResult{ProviderOperationID: op, Pending: true}, nil
	}
	if resp.StatusCode == 401 {
		return PublishResult{}, youtubePublishError(providerAuth, "YouTube authorization expired; reconnect the account")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return PublishResult{}, youtubeResponseError(resp)
	}
	var out struct {
		ID string `json:"id"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || out.ID == "" {
		return PublishResult{}, youtubePublishError(providerSchema, "YouTube upload response did not include a video ID")
	}
	videoOp := youtubeVideoOperation(out.ID)
	if err := a.persistOperation(ctx, r, videoOp); err != nil {
		return PublishResult{}, err
	}
	return PublishResult{ProviderOperationID: videoOp, Pending: true}, nil
}

func (a *youtubePublishAdapter) defaultPersistOperation(ctx context.Context, r PublishRequest, op string) error {
	q, err := a.s.pool.Exec(ctx, `UPDATE content_publish_targets SET provider_operation_id=$3,provider_resume_ciphertext=NULL,provider_resume_nonce=NULL,provider_resume_key_version=NULL,updated_at=now() WHERE id=$1 AND organization_id=$2 AND COALESCE(provider_operation_id,'')=$4`, r.TargetID, r.OrganizationID, op, r.ProviderOperationID)
	if err != nil || q.RowsAffected() != 1 {
		return youtubePublishError(providerRetryable, "could not persist YouTube upload operation")
	}
	return nil
}

func (a *youtubePublishAdapter) defaultPersistResume(ctx context.Context, r PublishRequest, session string) (string, error) {
	op := "yt:session:" + makeToken()
	payload, _ := json.Marshal(map[string]any{"sessionUrl": session})
	if err := a.s.persistProviderResume(ctx, r, op, "YOUTUBE", payload); err != nil {
		return "", youtubePublishError(providerRetryable, "could not persist YouTube upload session")
	}
	return op, nil
}

func (a *youtubePublishAdapter) defaultLoadResume(ctx context.Context, r PublishRequest) (string, error) {
	payload, err := a.s.loadProviderResume(ctx, r, "YOUTUBE")
	if err != nil {
		return "", err
	}
	var value struct {
		SessionURL string `json:"sessionUrl"`
	}
	if json.Unmarshal(payload, &value) != nil || value.SessionURL == "" {
		return "", errors.New("invalid YouTube resume payload")
	}
	return value.SessionURL, nil
}

type providerResumeEnvelope struct {
	Version        int             `json:"version"`
	TargetID       string          `json:"targetId"`
	OrganizationID string          `json:"organizationId"`
	Platform       string          `json:"platform"`
	Payload        json.RawMessage `json:"payload"`
}

func (s *Server) persistProviderResume(ctx context.Context, r PublishRequest, op, platform string, payload json.RawMessage) error {
	if s.envelope == nil {
		return errors.New("provider resume encryption is unavailable")
	}
	plain, err := json.Marshal(providerResumeEnvelope{Version: 1, TargetID: r.TargetID, OrganizationID: r.OrganizationID, Platform: platform, Payload: payload})
	if err != nil {
		return err
	}
	ciphertext, nonce, err := s.envelope.Encrypt(plain)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE content_publish_targets SET provider_operation_id=$3,provider_resume_ciphertext=$4,provider_resume_nonce=$5,provider_resume_key_version=1,updated_at=now() WHERE id=$1 AND organization_id=$2 AND COALESCE(provider_operation_id,'')=$6`, r.TargetID, r.OrganizationID, op, ciphertext, nonce, r.ProviderOperationID)
	if err != nil || tag.RowsAffected() != 1 {
		return errors.New("provider resume checkpoint CAS failed")
	}
	return nil
}

func (s *Server) loadProviderResume(ctx context.Context, r PublishRequest, platform string) (json.RawMessage, error) {
	if s.envelope == nil {
		return nil, errors.New("provider resume encryption is unavailable")
	}
	var ciphertext, nonce []byte
	var keyVersion int
	if err := s.pool.QueryRow(ctx, `SELECT provider_resume_ciphertext,provider_resume_nonce,provider_resume_key_version FROM content_publish_targets WHERE id=$1 AND organization_id=$2 AND provider_operation_id=$3`, r.TargetID, r.OrganizationID, r.ProviderOperationID).Scan(&ciphertext, &nonce, &keyVersion); err != nil {
		return nil, err
	}
	if keyVersion != 1 {
		return nil, errors.New("unsupported provider resume key version")
	}
	plain, err := s.envelope.Decrypt(ciphertext, nonce)
	if err != nil {
		return nil, err
	}
	var value providerResumeEnvelope
	if json.Unmarshal(plain, &value) != nil || value.Version != 1 || value.TargetID != r.TargetID || value.OrganizationID != r.OrganizationID || value.Platform != platform || len(value.Payload) == 0 {
		return nil, errors.New("provider resume payload binding mismatch")
	}
	return value.Payload, nil
}
func (s *Server) openPublishMedia(ctx context.Context, r PublishRequest, offset int64) (io.ReadCloser, int64, error) {
	snap, err := decodeContentPublishSnapshot(r.Snapshot)
	if err != nil {
		return nil, 0, err
	}
	storage, err := s.mediaStorage(ctx)
	if err != nil {
		return nil, 0, err
	}
	head, err := storage.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &storage.bucket, Key: &snap.MediaObjectKey})
	if err != nil || head.ContentLength == nil {
		if err == nil {
			err = fmt.Errorf("media size is unavailable")
		}
		return nil, 0, err
	}
	in := &s3.GetObjectInput{Bucket: &storage.bucket, Key: &snap.MediaObjectKey}
	if offset > 0 {
		value := fmt.Sprintf("bytes=%d-", offset)
		in.Range = &value
	}
	out, err := storage.client.GetObject(ctx, in)
	if err != nil {
		return nil, 0, err
	}
	return out.Body, *head.ContentLength, nil
}
func youtubeInitOperation(targetID string) string { return "yt:init:" + strings.TrimSpace(targetID) }
func youtubeVideoOperation(v string) string       { return "yt:video:" + v }
func parseYouTubeOperation(v string) (string, string) {
	p := strings.SplitN(v, ":", 3)
	if len(p) != 3 || p[0] != "yt" {
		return "", ""
	}
	return p[1], p[2]
}
func parseRange(v string) int64 {
	p := strings.LastIndex(v, "-")
	if p < 0 {
		return -1
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(v[p+1:]), 10, 64)
	return n
}
func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func youtubePublishError(k providerErrorKind, m string) error {
	return &providerError{Platform: "YouTube", Kind: k, Message: m}
}
func normalizeYouTubeError(err error) error {
	if err == nil {
		return nil
	}
	var p *providerError
	if errors.As(err, &p) {
		if p.Kind == providerAuth {
			return youtubePublishError(providerAuth, "YouTube authorization expired; reconnect the account")
		}
		return youtubePublishError(p.Kind, "YouTube request failed")
	}
	return youtubePublishError(providerRetryable, "YouTube request failed")
}
func youtubeResponseError(resp *http.Response) error {
	kind := providerPermanent
	if resp.StatusCode == 401 {
		kind = providerAuth
	}
	if resp.StatusCode == 403 {
		kind = providerPermission
	}
	if resp.StatusCode == 429 {
		kind = providerRateLimit
	}
	if resp.StatusCode >= 500 {
		kind = providerRetryable
	}
	return youtubePublishError(kind, "YouTube request was rejected")
}

func validateYouTubeSessionURL(value string) error {
	return validateProviderURL(value, "www.googleapis.com", "youtube.googleapis.com", "upload.youtube.com", ".googleapis.com")
}
