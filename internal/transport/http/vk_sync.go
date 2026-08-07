package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/statzavod/statzavod/internal/platforms"
)

type vkAPIError struct {
	Code int    `json:"error_code"`
	Text string `json:"error_msg"`
}

func (s *Server) fetchVKCurrentProfile(ctx context.Context, accessToken string) (platformProfile, error) {
	var out struct {
		Response []struct {
			ID         int64  `json:"id"`
			FirstName  string `json:"first_name"`
			LastName   string `json:"last_name"`
			ScreenName string `json:"screen_name"`
			Photo      string `json:"photo_200"`
		} `json:"response"`
		Error *vkAPIError `json:"error"`
	}
	endpoint := strings.TrimRight(s.config.VKAPIBase, "/") + "/method/users.get?" + url.Values{
		"access_token": {accessToken}, "fields": {"screen_name,photo_200"}, "v": {s.config.VKAPIVersion},
	}.Encode()
	if err := doJSON(ctx, http.MethodGet, endpoint, "", &out); err != nil {
		return platformProfile{}, err
	}
	if out.Error != nil {
		return platformProfile{}, classifyVKError(out.Error)
	}
	if len(out.Response) == 0 || out.Response[0].ID == 0 {
		return platformProfile{}, &providerError{Platform: "VK", Kind: providerSchema, Message: "profile is unavailable"}
	}
	user := out.Response[0]
	username := user.ScreenName
	if username == "" {
		username = "id" + strconv.FormatInt(user.ID, 10)
	}
	return platformProfile{
		Username: username, DisplayName: firstNonEmpty(strings.TrimSpace(user.FirstName+" "+user.LastName), username),
		ProfileURL: "https://vk.ru/" + username, AvatarURL: user.Photo, AccountType: "COMPANY_OPERATOR",
	}, nil
}

func (s *Server) refreshVKAccessToken(ctx context.Context, refreshToken, deviceID string) (oauthToken, error) {
	if deviceID == "" {
		return oauthToken{}, &providerError{Platform: "VK", Kind: providerAuth, Message: "VK ID device ID is missing"}
	}
	state, _, _, err := platforms.NewPKCE()
	if err != nil {
		return oauthToken{}, err
	}
	endpoint := strings.TrimRight(s.config.VKOAuthBase, "/") + "/oauth2/auth?" + url.Values{
		"grant_type": {"refresh_token"}, "redirect_uri": {s.config.VKRedirectURL}, "client_id": {s.config.VKClientID}, "device_id": {deviceID}, "state": {state},
	}.Encode()
	form := url.Values{"refresh_token": {refreshToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthToken{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := doRequestJSON(req, &out); err != nil || out.AccessToken == "" {
		return oauthToken{}, &providerError{Platform: "VK", Kind: providerAuth, Message: "VK ID token refresh failed"}
	}
	return oauthToken{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken, ExpiresIn: out.ExpiresIn, Scopes: splitScopes(out.Scope, nil)}, nil
}

type vkVideo struct {
	ID          int64  `json:"id"`
	OwnerID     int64  `json:"owner_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Date        int64  `json:"date"`
	Player      string `json:"player"`
	Views       int64  `json:"views"`
	Likes       struct {
		Count int64 `json:"count"`
	} `json:"likes"`
	Comments  int64  `json:"comments"`
	Shares    int64  `json:"-"`
	Permalink string `json:"-"`
	Image     []struct {
		URL    string `json:"url"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	} `json:"image"`
}

func (s *Server) syncVKCommunities(ctx context.Context, job platformSyncJob, accessToken string) (syncResult, error) {
	profile, err := s.fetchVKCurrentProfile(ctx, accessToken)
	if err != nil {
		return syncResult{}, err
	}
	if err := s.refreshPlatformAccountProfile(ctx, job, profile); err != nil {
		return syncResult{}, err
	}
	rows, err := s.pool.Query(ctx, `SELECT v.creator_id,v.community_url FROM creator_vk_assignments v JOIN company_vk_accounts a ON a.id=v.company_vk_account_id WHERE a.platform_account_id=$1 AND a.organization_id=$2 ORDER BY v.creator_id`, job.AccountID, job.OrganizationID)
	if err != nil {
		return syncResult{}, err
	}
	defer rows.Close()
	type assignment struct{ CreatorID, CommunityURL string }
	assignments := make([]assignment, 0)
	for rows.Next() {
		var item assignment
		if err := rows.Scan(&item.CreatorID, &item.CommunityURL); err != nil {
			return syncResult{}, err
		}
		assignments = append(assignments, item)
	}
	if err := rows.Err(); err != nil {
		return syncResult{}, err
	}
	result := syncResult{}
	for _, assignment := range assignments {
		groupID, err := s.resolveVKCommunity(ctx, accessToken, assignment.CommunityURL)
		if err != nil {
			return result, err
		}
		videos, err := s.fetchVKCommunityVideos(ctx, accessToken, groupID)
		if err != nil {
			return result, err
		}
		wallVideos, err := s.fetchVKCommunityWallVideos(ctx, accessToken, groupID)
		if err != nil {
			return result, err
		}
		byID := make(map[string]vkVideo, len(videos)+len(wallVideos))
		for _, video := range videos {
			byID[vkVideoExternalID(video)] = video
		}
		// Prefer the wall version: it carries the public post link and the
		// reactions that users see on the community post.
		for _, video := range wallVideos {
			byID[vkVideoExternalID(video)] = video
		}
		videos = videos[:0]
		for _, video := range byID {
			videos = append(videos, video)
		}
		result.RecordsRead += len(videos)
		for _, video := range videos {
			if err := s.upsertVKVideo(ctx, job, assignment.CreatorID, video); err != nil {
				return result, err
			}
			result.RecordsWritten++
		}
	}
	return result, nil
}

func (s *Server) fetchVKCommunityWallVideos(ctx context.Context, accessToken string, groupID int64) ([]vkVideo, error) {
	all := make([]vkVideo, 0)
	for offset := 0; ; offset += 100 {
		var out struct {
			Response struct {
				Count int `json:"count"`
				Items []struct {
					ID      int64  `json:"id"`
					OwnerID int64  `json:"owner_id"`
					Date    int64  `json:"date"`
					Text    string `json:"text"`
					Views   struct {
						Count int64 `json:"count"`
					} `json:"views"`
					Likes struct {
						Count int64 `json:"count"`
					} `json:"likes"`
					Comments struct {
						Count int64 `json:"count"`
					} `json:"comments"`
					Reposts struct {
						Count int64 `json:"count"`
					} `json:"reposts"`
					Attachments []struct {
						Type  string  `json:"type"`
						Video vkVideo `json:"video"`
					} `json:"attachments"`
				} `json:"items"`
			} `json:"response"`
			Error *vkAPIError `json:"error"`
		}
		endpoint := strings.TrimRight(s.config.VKAPIBase, "/") + "/method/wall.get?" + url.Values{
			"owner_id": {strconv.FormatInt(-groupID, 10)}, "count": {"100"}, "offset": {strconv.Itoa(offset)}, "access_token": {accessToken}, "v": {s.config.VKAPIVersion},
		}.Encode()
		if err := doJSON(ctx, http.MethodGet, endpoint, "", &out); err != nil {
			return nil, err
		}
		if out.Error != nil {
			return nil, classifyVKError(out.Error)
		}
		for _, post := range out.Response.Items {
			for _, attachment := range post.Attachments {
				if attachment.Type != "video" || attachment.Video.ID == 0 {
					continue
				}
				video := attachment.Video
				if video.Date == 0 {
					video.Date = post.Date
				}
				if video.Title == "" {
					video.Title = truncateVKText(post.Text, 120)
				}
				if video.Description == "" {
					video.Description = post.Text
				}
				if video.Views == 0 {
					video.Views = post.Views.Count
				}
				video.Likes.Count = post.Likes.Count
				video.Comments = post.Comments.Count
				video.Shares = post.Reposts.Count
				video.Permalink = fmt.Sprintf("https://vk.ru/wall%d_%d", post.OwnerID, post.ID)
				all = append(all, video)
			}
		}
		if len(out.Response.Items) < 100 || offset+len(out.Response.Items) >= out.Response.Count {
			break
		}
	}
	return all, nil
}

func truncateVKText(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}

func vkVideoExternalID(video vkVideo) string {
	return strconv.FormatInt(video.OwnerID, 10) + "_" + strconv.FormatInt(video.ID, 10)
}

func (s *Server) resolveVKCommunity(ctx context.Context, accessToken, communityURL string) (int64, error) {
	parsed, err := url.Parse(communityURL)
	if err != nil {
		return 0, &providerError{Platform: "VK", Kind: providerPermanent, Message: "invalid community URL"}
	}
	screenName := strings.Trim(path.Clean(parsed.Path), "/")
	if strings.HasPrefix(screenName, "club") {
		if id, parseErr := strconv.ParseInt(strings.TrimPrefix(screenName, "club"), 10, 64); parseErr == nil && id > 0 {
			return id, nil
		}
	}
	var out struct {
		Response struct {
			Type     string `json:"type"`
			ObjectID int64  `json:"object_id"`
		} `json:"response"`
		Error *vkAPIError `json:"error"`
	}
	endpoint := strings.TrimRight(s.config.VKAPIBase, "/") + "/method/utils.resolveScreenName?" + url.Values{
		"screen_name": {screenName}, "access_token": {accessToken}, "v": {s.config.VKAPIVersion},
	}.Encode()
	if err := doJSON(ctx, http.MethodGet, endpoint, "", &out); err != nil {
		return 0, err
	}
	if out.Error != nil {
		return 0, classifyVKError(out.Error)
	}
	if out.Response.Type != "group" || out.Response.ObjectID == 0 {
		return 0, &providerError{Platform: "VK", Kind: providerPermanent, Message: "VK URL does not point to a community"}
	}
	return out.Response.ObjectID, nil
}

func (s *Server) fetchVKCommunityVideos(ctx context.Context, accessToken string, groupID int64) ([]vkVideo, error) {
	all := make([]vkVideo, 0)
	for offset := 0; ; offset += 200 {
		var out struct {
			Response struct {
				Count int       `json:"count"`
				Items []vkVideo `json:"items"`
			} `json:"response"`
			Error *vkAPIError `json:"error"`
		}
		endpoint := strings.TrimRight(s.config.VKAPIBase, "/") + "/method/video.get?" + url.Values{
			"owner_id": {strconv.FormatInt(-groupID, 10)}, "count": {"200"}, "offset": {strconv.Itoa(offset)}, "extended": {"1"}, "access_token": {accessToken}, "v": {s.config.VKAPIVersion},
		}.Encode()
		if err := doJSON(ctx, http.MethodGet, endpoint, "", &out); err != nil {
			return nil, err
		}
		if out.Error != nil {
			return nil, classifyVKError(out.Error)
		}
		all = append(all, out.Response.Items...)
		if len(out.Response.Items) < 200 || len(all) >= out.Response.Count {
			break
		}
	}
	return all, nil
}

func classifyVKError(err *vkAPIError) error {
	kind := providerRetryable
	if err.Code == 5 {
		kind = providerAuth
	} else if err.Code == 7 || err.Code == 15 || err.Code == 27 {
		kind = providerPermission
	} else if err.Code == 100 || err.Code == 113 {
		kind = providerPermanent
	}
	return &providerError{Platform: "VK", Kind: kind, Message: fmt.Sprintf("VK API error %d: %s", err.Code, err.Text)}
}

func (s *Server) upsertVKVideo(ctx context.Context, job platformSyncJob, creatorID string, video vkVideo) error {
	externalID := vkVideoExternalID(video)
	thumbnail := ""
	maxArea := 0
	for _, image := range video.Image {
		if area := image.Width * image.Height; area > maxArea {
			maxArea, thumbnail = area, image.URL
		}
	}
	publishedAt := time.Unix(video.Date, 0).UTC()
	permalink := "https://vk.ru/video" + externalID
	if video.Permalink != "" {
		permalink = video.Permalink
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var publicationID string
	err = tx.QueryRow(ctx, `INSERT INTO publications(organization_id,creator_id,platform_account_id,platform,external_id,publication_type,title,description,permalink,thumbnail_url,published_at,status,metadata) VALUES($1,$2,$3,'VK',$4,'VIDEO',$5,$6,$7,$8,$9,'ACTIVE',jsonb_build_object('player',$10::text)) ON CONFLICT(organization_id,platform,external_id) DO UPDATE SET creator_id=excluded.creator_id,platform_account_id=excluded.platform_account_id,title=excluded.title,description=excluded.description,permalink=excluded.permalink,thumbnail_url=excluded.thumbnail_url,published_at=excluded.published_at,status='ACTIVE',metadata=excluded.metadata,updated_at=now() RETURNING id`, job.OrganizationID, creatorID, job.AccountID, externalID, video.Title, video.Description, permalink, thumbnail, publishedAt, video.Player).Scan(&publicationID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO publication_metric_snapshots(publication_id,views,likes,comments,shares,completeness_status) VALUES($1,$2,$3,$4,$5,'PARTIAL')`, publicationID, video.Views, video.Likes.Count, video.Comments, video.Shares)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
