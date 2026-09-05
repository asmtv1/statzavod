package httpserver

// VK Video fallback deliberately uses video.save + upload_url. VK Clips has
// no supported create method in this adapter. Provider operation IDs are phase
// qualified so a worker can resume safely after a crash.
import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type vkPublishAdapter struct {
	s             *Server
	accessToken   func(context.Context, PublishRequest) (string, error)
	persist       func(context.Context, PublishRequest, string) error
	persistResume func(context.Context, PublishRequest, vkUploadResume) (string, error)
	loadResume    func(context.Context, PublishRequest) (vkUploadResume, error)
	openMedia     func(context.Context, PublishRequest, int64) (io.ReadCloser, int64, error)
	uploadHTTP    *http.Client
}
type vkPublishOptions struct {
	Owner       string `json:"owner"`
	OwnerID     int64  `json:"ownerId"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Privacy     string `json:"privacy"`
	WallPost    bool   `json:"wallPost"`
}
type vkUploadResume struct {
	Owner     int64  `json:"owner"`
	VideoID   int64  `json:"videoId"`
	UploadURL string `json:"uploadUrl"`
}

func newVKPublishAdapter(s *Server) *vkPublishAdapter {
	a := &vkPublishAdapter{s: s, openMedia: s.openPublishMedia}
	a.uploadHTTP = newValidatedProviderHTTPClient(validateVKUploadURL)
	a.persist = a.defaultVKPersist
	a.persistResume = a.defaultVKPersistResume
	a.loadResume = a.defaultVKLoadResume
	a.accessToken = func(ctx context.Context, r PublishRequest) (string, error) {
		t, _, e := s.accessTokenForSync(ctx, platformSyncJob{OrganizationID: r.OrganizationID, CompanyID: r.CompanyID, AccountID: r.AccountID, Platform: "VK"})
		return t, normalizeVKPublishError(e)
	}
	return a
}
func (a *vkPublishAdapter) Publish(ctx context.Context, r PublishRequest) (PublishResult, error) {
	if !a.s.config.ContentPublishingEnabled || !a.s.config.ContentVKEnabled {
		return PublishResult{}, vkPublishError(providerPermanent, "VK Video content publishing is disabled")
	}
	if strings.TrimSpace(r.ProviderOperationID) != "" {
		return a.Poll(ctx, r)
	}
	token, e := a.accessToken(ctx, r)
	if e != nil {
		return PublishResult{}, e
	}
	snap, e := decodeContentPublishSnapshot(r.Snapshot)
	if e != nil {
		return PublishResult{}, vkPublishError(providerPermanent, "VK Video publish snapshot is unavailable")
	}
	var o vkPublishOptions
	if json.Unmarshal(snap.PlatformOptions, &o) != nil {
		return PublishResult{}, vkPublishError(providerPermanent, "VK Video options are invalid")
	}
	if o.Owner == "" {
		o.Owner = snap.AccountType
	}
	if o.OwnerID == 0 {
		o.OwnerID = parseVKOwnerID(snap.AccountExternalID, o.Owner)
	}
	if o.OwnerID == 0 || (o.Owner != "user" && o.Owner != "community") {
		return PublishResult{}, vkPublishError(providerPermanent, "VK Video owner must be an explicit user or community")
	}
	if o.Title == "" {
		o.Title = snap.Caption
	}
	if o.Privacy == "" {
		o.Privacy = "public"
	}
	if o.Privacy != "public" && o.Privacy != "private" {
		return PublishResult{}, vkPublishError(providerPermanent, "VK Video privacy must be public or private")
	}
	// Persist before provider I/O. If this phase is observed later, recovery is
	// fail-closed because video.save is not safely repeatable.
	intent := vkOp("save", r.TargetID)
	if e = a.persist(ctx, r, intent); e != nil {
		return PublishResult{}, e
	}
	r.ProviderOperationID = intent
	var saved struct {
		UploadURL string `json:"upload_url"`
		VideoID   int64  `json:"video_id"`
		OwnerID   int64  `json:"owner_id"`
	}
	form := url.Values{"owner_id": {strconv.FormatInt(o.OwnerID, 10)}, "name": {o.Title}, "description": {o.Description}, "is_private": {strconv.FormatBool(o.Privacy == "private")}}
	if o.WallPost {
		form.Set("wallpost", "1")
	}
	if e = a.callAPI(ctx, token, "video.save", form, &saved, providerOneShot); e != nil {
		return PublishResult{}, vkCreateError("VK Video save", e)
	}
	if saved.UploadURL == "" || saved.VideoID == 0 {
		return PublishResult{}, vkPublishError(providerSchema, "VK Video did not return an upload URL and video ID")
	}
	if saved.OwnerID == 0 {
		saved.OwnerID = o.OwnerID
	}
	if e = validateVKUploadURL(saved.UploadURL); e != nil {
		return PublishResult{}, vkPublishError(providerPermanent, "VK Video returned an unsafe upload URL")
	}
	op, e := a.persistResume(ctx, r, vkUploadResume{Owner: saved.OwnerID, VideoID: saved.VideoID, UploadURL: saved.UploadURL})
	if e != nil {
		return PublishResult{}, e
	}
	return a.upload(ctx, r, token, op, saved.OwnerID, saved.VideoID, saved.UploadURL, o.WallPost)
}
func (a *vkPublishAdapter) Poll(ctx context.Context, r PublishRequest) (PublishResult, error) {
	token, e := a.accessToken(ctx, r)
	if e != nil {
		return PublishResult{}, e
	}
	phase, parts := parseVKOperation(r.ProviderOperationID)
	switch phase {
	case "save":
		return PublishResult{}, vkPublishError(providerPermanent, "VK Video save outcome is uncertain; inspect the existing operation before retrying")
	case "upload":
		if len(parts) != 1 {
			return PublishResult{}, vkPublishError(providerPermanent, "VK Video upload operation is invalid")
		}
		resume, e := a.loadResume(ctx, r)
		if e != nil || resume.Owner == 0 || resume.VideoID == 0 || resume.UploadURL == "" {
			return PublishResult{}, vkPublishError(providerPermanent, "VK Video upload URL is invalid")
		}
		if e = validateVKUploadURL(resume.UploadURL); e != nil {
			return PublishResult{}, vkPublishError(providerPermanent, "VK Video upload URL is unsafe")
		}
		snap, _ := decodeContentPublishSnapshot(r.Snapshot)
		var o vkPublishOptions
		_ = json.Unmarshal(snap.PlatformOptions, &o)
		return a.upload(ctx, r, token, r.ProviderOperationID, resume.Owner, resume.VideoID, resume.UploadURL, o.WallPost)
	case "video":
		if len(parts) != 2 {
			return PublishResult{}, vkPublishError(providerPermanent, "VK Video operation is invalid")
		}
		owner, _ := strconv.ParseInt(parts[0], 10, 64)
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		snap, _ := decodeContentPublishSnapshot(r.Snapshot)
		var o vkPublishOptions
		_ = json.Unmarshal(snap.PlatformOptions, &o)
		if !o.WallPost {
			return PublishResult{ProviderOperationID: r.ProviderOperationID, ExternalID: vkExternal(owner, id), ExternalURL: vkURL(owner, id)}, nil
		}
		intent := vkOp("wall", r.TargetID)
		if e = a.persist(ctx, r, intent); e != nil {
			return PublishResult{}, e
		}
		r.ProviderOperationID = intent
		var wall struct {
			PostID int64 `json:"post_id"`
		}
		if e = a.callAPI(ctx, token, "wall.post", url.Values{"owner_id": {strconv.FormatInt(owner, 10)}, "attachments": {"video" + vkExternal(owner, id)}, "from_group": {strconv.FormatBool(owner < 0)}}, &wall, providerOneShot); e != nil {
			return PublishResult{}, vkCreateError("VK wall post", e)
		}
		if wall.PostID == 0 {
			return PublishResult{}, vkPublishError(providerSchema, "VK wall post did not return post ID")
		}
		op := vkWallDone(owner, id, wall.PostID)
		if e = a.persist(ctx, r, op); e != nil {
			return PublishResult{}, e
		}
		return PublishResult{ProviderOperationID: op, ExternalID: vkExternal(owner, id), ExternalURL: vkURL(owner, id)}, nil
	case "wall":
		return PublishResult{}, vkPublishError(providerPermanent, "VK wall post outcome is uncertain; refusing a duplicate post")
	case "wall-done":
		return PublishResult{ProviderOperationID: r.ProviderOperationID, ExternalID: vkExternal(mustInt(parts, 0), mustInt(parts, 1)), ExternalURL: vkURL(mustInt(parts, 0), mustInt(parts, 1))}, nil
	default:
		return PublishResult{}, vkPublishError(providerPermanent, "VK Video operation is invalid")
	}
}
func (a *vkPublishAdapter) upload(ctx context.Context, r PublishRequest, token, op string, owner, id int64, uploadURL string, wall bool) (PublishResult, error) {
	if e := validateVKUploadURL(uploadURL); e != nil {
		return PublishResult{}, vkPublishError(providerPermanent, "VK Video upload URL is unsafe")
	}
	body, _, e := a.openMedia(ctx, r, 0)
	if e != nil {
		return PublishResult{}, vkPublishError(providerRetryable, "VK Video media is unavailable")
	}
	defer body.Close()
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		defer pw.Close()
		part, er := mw.CreateFormFile("video_file", "video.mp4")
		if er == nil {
			_, er = io.Copy(part, body)
		}
		if er == nil {
			er = mw.Close()
		}
		_ = pw.CloseWithError(er)
	}()
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, pr)
	if e != nil {
		return PublishResult{}, vkPublishError(providerRetryable, "VK Video upload request failed")
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.ContentLength = -1
	resp, e := a.uploadHTTP.Do(req)
	if e != nil {
		return PublishResult{ProviderOperationID: op, Pending: true}, vkPublishError(providerRetryable, "VK Video upload is temporarily unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if (resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound) && strings.HasPrefix(op, "vk:upload:") {
			// VK upload URLs expire. Re-saving with the already allocated video_id
			// refreshes the URL without allocating a second VK Video object.
			var refreshed struct {
				UploadURL string `json:"upload_url"`
			}
			form := url.Values{"owner_id": {strconv.FormatInt(owner, 10)}, "video_id": {strconv.FormatInt(id, 10)}}
			if e := a.callAPI(ctx, token, "video.save", form, &refreshed, providerOneShot); e != nil || refreshed.UploadURL == "" || validateVKUploadURL(refreshed.UploadURL) != nil {
				return PublishResult{}, vkPublishError(providerPermanent, "VK Video upload URL expired; refresh failed safely")
			}
			newOp, e := a.persistResume(ctx, r, vkUploadResume{Owner: owner, VideoID: id, UploadURL: refreshed.UploadURL})
			if e != nil {
				return PublishResult{}, e
			}
			r.ProviderOperationID = newOp
			return a.upload(ctx, r, token, newOp, owner, id, refreshed.UploadURL, wall)
		}
		return PublishResult{}, vkResponseError(resp)
	}
	next := vkOp("video", fmt.Sprintf("%d:%d", owner, id))
	if wall {
		next = vkOp("video", fmt.Sprintf("%d:%d", owner, id))
	}
	if e = a.persist(ctx, r, next); e != nil {
		return PublishResult{}, e
	}
	return PublishResult{ProviderOperationID: next, Pending: true}, nil
}
func (a *vkPublishAdapter) callAPI(ctx context.Context, token, method string, form url.Values, out any, mode providerRequestMode) error {
	endpoint := strings.TrimRight(a.s.config.VKAPIBase, "/") + "/method/" + method + "?" + form.Encode() + "&access_token=" + url.QueryEscape(token) + "&v=" + url.QueryEscape(a.s.config.VKAPIVersion)
	var w struct {
		Response json.RawMessage `json:"response"`
		Error    *vkAPIError     `json:"error"`
	}
	if e := newProviderClient("VK").JSONWithMode(ctx, http.MethodPost, endpoint, "", "", nil, &w, mode); e != nil {
		return normalizeVKPublishError(e)
	}
	if w.Error != nil {
		return normalizeVKPublishError(classifyVKError(w.Error))
	}
	if json.Unmarshal(w.Response, out) != nil {
		return vkPublishError(providerSchema, "VK returned an invalid response")
	}
	return nil
}
func (a *vkPublishAdapter) defaultVKPersist(ctx context.Context, r PublishRequest, op string) error {
	q, e := a.s.pool.Exec(ctx, `UPDATE content_publish_targets SET provider_operation_id=$3,provider_resume_ciphertext=NULL,provider_resume_nonce=NULL,provider_resume_key_version=NULL,updated_at=now() WHERE id=$1 AND organization_id=$2 AND COALESCE(provider_operation_id,'') IN ('',$4)`, r.TargetID, r.OrganizationID, op, r.ProviderOperationID)
	if e != nil || q.RowsAffected() != 1 {
		return vkPublishError(providerRetryable, "could not persist VK Video operation")
	}
	return nil
}
func (a *vkPublishAdapter) defaultVKPersistResume(ctx context.Context, r PublishRequest, resume vkUploadResume) (string, error) {
	op := "vk:upload:" + makeToken()
	payload, _ := json.Marshal(resume)
	if e := a.s.persistProviderResume(ctx, r, op, "VK", payload); e != nil {
		return "", vkPublishError(providerRetryable, "could not persist VK Video upload checkpoint")
	}
	return op, nil
}
func (a *vkPublishAdapter) defaultVKLoadResume(ctx context.Context, r PublishRequest) (vkUploadResume, error) {
	var resume vkUploadResume
	payload, e := a.s.loadProviderResume(ctx, r, "VK")
	if e != nil {
		return resume, e
	}
	if json.Unmarshal(payload, &resume) != nil {
		return resume, errors.New("invalid VK upload resume payload")
	}
	return resume, nil
}
func vkOp(p, v string) string {
	return "vk:" + p + ":" + base64.RawURLEncoding.EncodeToString([]byte(v))
}
func vkWallDone(o, id, post int64) string {
	return "vk:wall-done:" + fmt.Sprintf("%d:%d:%d", o, id, post)
}
func parseVKOperation(v string) (string, []string) {
	p := strings.Split(v, ":")
	if len(p) < 3 || p[0] != "vk" {
		return "", nil
	}
	if p[1] == "upload" && len(p) == 3 {
		return p[1], p[2:]
	}
	if p[1] == "wall-done" && len(p) == 5 {
		return p[1], p[2:]
	}
	if p[1] == "video" && len(p) >= 3 {
		decoded, err := base64.RawURLEncoding.DecodeString(strings.Join(p[2:], ":"))
		if err != nil {
			return "", nil
		}
		return p[1], strings.Split(string(decoded), ":")
	}
	if p[1] == "save" || p[1] == "wall" {
		return p[1], p[2:]
	}
	return "", nil
}
func parseVKOwnerID(v, o string) int64 {
	i, _ := strconv.ParseInt(v, 10, 64)
	if o == "community" && i > 0 {
		i = -i
	}
	return i
}
func mustInt(p []string, i int) int64 { v, _ := strconv.ParseInt(p[i], 10, 64); return v }
func vkExternal(o, id int64) string   { return fmt.Sprintf("%d_%d", o, id) }
func vkURL(o, id int64) string        { return "https://vk.ru/video" + vkExternal(o, id) }
func vkPublishError(k providerErrorKind, m string) error {
	return &providerError{Platform: "VK", Kind: k, Message: m}
}
func normalizeVKPublishError(e error) error {
	if e == nil {
		return nil
	}
	if isProviderKind(e, providerAuth) {
		return vkPublishError(providerAuth, "VK authorization expired; reconnect the account")
	}
	if isProviderKind(e, providerPermission) {
		return vkPublishError(providerPermission, "VK Video permission is missing; reconnect and grant video and wall permissions")
	}
	if isProviderKind(e, providerSchema) {
		return vkPublishError(providerSchema, "VK returned an invalid publishing response")
	}
	if isProviderKind(e, providerPermanent) {
		return vkPublishError(providerPermanent, "VK provider outcome is uncertain; manual reconciliation is required")
	}
	return vkPublishError(providerRetryable, "VK Video provider request failed")
}

func validateVKUploadURL(value string) error {
	return validateProviderURL(value, ".vk.com", ".vkuseraudio.net", ".vkuserlive.net", ".vkcdnservice.com", ".vk-cdn.net")
}

func vkCreateError(action string, err error) error {
	if isProviderKind(err, providerPermanent, providerRateLimit, providerRetryable) {
		return vkPublishError(providerPermanent, action+" outcome is uncertain; manual reconciliation is required")
	}
	return err
}
func vkResponseError(r *http.Response) error {
	if r.StatusCode == 401 {
		return vkPublishError(providerAuth, "VK authorization expired; reconnect the account")
	}
	if r.StatusCode == 403 {
		return vkPublishError(providerPermission, "VK Video permission is missing")
	}
	if r.StatusCode >= 500 {
		return vkPublishError(providerRetryable, "VK Video is temporarily unavailable")
	}
	return vkPublishError(providerPermanent, "VK Video upload was rejected")
}

var _ PublishAdapter = (*vkPublishAdapter)(nil)
