package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type youtubeChannel struct {
	ID      string `json:"id"`
	Snippet struct {
		Title       string `json:"title"`
		CustomURL   string `json:"customUrl"`
		Description string `json:"description"`
	} `json:"snippet"`
	ContentDetails struct {
		RelatedPlaylists struct {
			Uploads string `json:"uploads"`
		} `json:"relatedPlaylists"`
	} `json:"contentDetails"`
	Statistics struct {
		ViewCount       string `json:"viewCount"`
		SubscriberCount string `json:"subscriberCount"`
		VideoCount      string `json:"videoCount"`
	} `json:"statistics"`
}

type youtubePlaylistItem struct {
	ContentDetails struct {
		VideoID          string `json:"videoId"`
		VideoPublishedAt string `json:"videoPublishedAt"`
	} `json:"contentDetails"`
}

type youtubeVideo struct {
	ID      string `json:"id"`
	Snippet struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		PublishedAt string `json:"publishedAt"`
		Thumbnails  map[string]struct {
			URL string `json:"url"`
		} `json:"thumbnails"`
	} `json:"snippet"`
	ContentDetails struct {
		Duration string `json:"duration"`
	} `json:"contentDetails"`
	Statistics struct {
		ViewCount    string `json:"viewCount"`
		LikeCount    string `json:"likeCount"`
		CommentCount string `json:"commentCount"`
	} `json:"statistics"`
	Status struct {
		PrivacyStatus string `json:"privacyStatus"`
	} `json:"status"`
}

type youtubeAnalyticsReport struct {
	ColumnHeaders []struct {
		Name string `json:"name"`
	} `json:"columnHeaders"`
	Rows [][]any `json:"rows"`
}

func (s *Server) syncYouTubeAccount(ctx context.Context, job platformSyncJob, accessToken string) (syncResult, error) {
	client := newProviderClient("YouTube")
	channelEndpoint := strings.TrimRight(s.config.YouTubeAPIBase, "/") + "/channels?" + url.Values{
		"part":       {"snippet,contentDetails,statistics"},
		"mine":       {"true"},
		"maxResults": {"1"},
	}.Encode()
	var channelResponse struct {
		Items []youtubeChannel `json:"items"`
	}
	if err := client.JSON(ctx, http.MethodGet, channelEndpoint, accessToken, "", nil, &channelResponse); err != nil {
		return syncResult{}, err
	}
	if len(channelResponse.Items) == 0 || channelResponse.Items[0].ContentDetails.RelatedPlaylists.Uploads == "" {
		return syncResult{}, &providerError{Platform: "YouTube", Kind: providerSchema, Message: "channel uploads playlist is unavailable"}
	}
	channel := channelResponse.Items[0]
	if err := s.saveYouTubeAccountSnapshot(ctx, job.AccountID, channel); err != nil {
		return syncResult{}, err
	}

	videoIDs, err := s.fetchYouTubeUploadIDs(ctx, client, accessToken, channel.ContentDetails.RelatedPlaylists.Uploads)
	if err != nil {
		return syncResult{}, err
	}
	result := syncResult{RecordsRead: len(videoIDs)}
	publicationIDs := make(map[string]string, len(videoIDs))
	for start := 0; start < len(videoIDs); start += 50 {
		end := start + 50
		if end > len(videoIDs) {
			end = len(videoIDs)
		}
		videos, fetchErr := s.fetchYouTubeVideos(ctx, client, accessToken, videoIDs[start:end])
		if fetchErr != nil {
			return result, fetchErr
		}
		for _, video := range videos {
			publicationID, writeErr := s.upsertYouTubeVideo(ctx, job, video)
			if writeErr != nil {
				return result, writeErr
			}
			publicationIDs[video.ID] = publicationID
			result.RecordsWritten++
		}
	}

	if err := s.syncYouTubeAnalytics(ctx, client, accessToken, job.AccountID, publicationIDs); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Server) fetchYouTubeUploadIDs(ctx context.Context, client providerClient, accessToken, playlistID string) ([]string, error) {
	ids := make([]string, 0, 100)
	pageToken := ""
	for pages := 0; pages < 200; pages++ {
		values := url.Values{
			"part":       {"contentDetails"},
			"playlistId": {playlistID},
			"maxResults": {"50"},
		}
		if pageToken != "" {
			values.Set("pageToken", pageToken)
		}
		var response struct {
			Items         []youtubePlaylistItem `json:"items"`
			NextPageToken string                `json:"nextPageToken"`
		}
		endpoint := strings.TrimRight(s.config.YouTubeAPIBase, "/") + "/playlistItems?" + values.Encode()
		if err := client.JSON(ctx, http.MethodGet, endpoint, accessToken, "", nil, &response); err != nil {
			return nil, err
		}
		for _, item := range response.Items {
			if item.ContentDetails.VideoID != "" {
				ids = append(ids, item.ContentDetails.VideoID)
			}
		}
		if response.NextPageToken == "" {
			break
		}
		pageToken = response.NextPageToken
	}
	return ids, nil
}

func (s *Server) fetchYouTubeVideos(ctx context.Context, client providerClient, accessToken string, ids []string) ([]youtubeVideo, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	endpoint := strings.TrimRight(s.config.YouTubeAPIBase, "/") + "/videos?" + url.Values{
		"part": {"snippet,contentDetails,statistics,status"},
		"id":   {strings.Join(ids, ",")},
	}.Encode()
	var response struct {
		Items []youtubeVideo `json:"items"`
	}
	if err := client.JSON(ctx, http.MethodGet, endpoint, accessToken, "", nil, &response); err != nil {
		return nil, err
	}
	return response.Items, nil
}

func (s *Server) saveYouTubeAccountSnapshot(ctx context.Context, accountID string, channel youtubeChannel) error {
	metadata, _ := json.Marshal(map[string]any{
		"channelId":    channel.ID,
		"channelViews": parseInt64(channel.Statistics.ViewCount),
	})
	_, err := s.pool.Exec(ctx, `
		INSERT INTO account_metric_snapshots(platform_account_id,followers,media_count,views,metadata)
		VALUES($1,$2,$3,$4,$5::jsonb)
	`, accountID, parseInt64(channel.Statistics.SubscriberCount), parseInt64(channel.Statistics.VideoCount), parseInt64(channel.Statistics.ViewCount), string(metadata))
	return err
}

func (s *Server) upsertYouTubeVideo(ctx context.Context, job platformSyncJob, video youtubeVideo) (string, error) {
	publishedAt, err := time.Parse(time.RFC3339, video.Snippet.PublishedAt)
	if err != nil {
		return "", &providerError{Platform: "YouTube", Kind: providerSchema, Message: "video publication date is invalid"}
	}
	thumbnail := ""
	for _, key := range []string{"maxres", "standard", "high", "medium", "default"} {
		if value, ok := video.Snippet.Thumbnails[key]; ok {
			thumbnail = value.URL
			break
		}
	}
	duration := parseISO8601Duration(video.ContentDetails.Duration)
	metadata, _ := json.Marshal(map[string]any{"privacyStatus": video.Status.PrivacyStatus})
	var publicationID string
	err = s.pool.QueryRow(ctx, `
		INSERT INTO publications(
			organization_id,creator_id,platform_account_id,platform,external_id,
			publication_type,title,description,permalink,thumbnail_url,duration_ms,published_at,metadata
		) VALUES($1,$2,$3,'YOUTUBE',$4,'VIDEO',$5,$6,$7,$8,$9,$10,$11::jsonb)
		ON CONFLICT(organization_id,platform,external_id) DO UPDATE SET
			creator_id=excluded.creator_id,platform_account_id=excluded.platform_account_id,
			title=excluded.title,description=excluded.description,permalink=excluded.permalink,
			thumbnail_url=excluded.thumbnail_url,duration_ms=excluded.duration_ms,
			published_at=excluded.published_at,status='ACTIVE',metadata=excluded.metadata,updated_at=now()
		RETURNING id
	`, job.OrganizationID, job.CreatorID, job.AccountID, video.ID, video.Snippet.Title, video.Snippet.Description,
		"https://www.youtube.com/watch?v="+video.ID, thumbnail, duration.Milliseconds(), publishedAt, string(metadata)).Scan(&publicationID)
	if err != nil {
		return "", err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO publication_metric_snapshots(publication_id,views,likes,comments,completeness_status)
		VALUES($1,$2,$3,$4,'PARTIAL')
	`, publicationID, parseInt64(video.Statistics.ViewCount), parseInt64(video.Statistics.LikeCount), parseInt64(video.Statistics.CommentCount))
	return publicationID, err
}

func (s *Server) syncYouTubeAnalytics(ctx context.Context, client providerClient, accessToken, accountID string, publicationIDs map[string]string) error {
	end := time.Now().UTC().AddDate(0, 0, -1)
	start := end.AddDate(0, 0, -30)
	common := url.Values{
		"ids":       {"channel==MINE"},
		"startDate": {start.Format("2006-01-02")},
		"endDate":   {end.Format("2006-01-02")},
		"metrics":   {"views,likes,comments,shares,estimatedMinutesWatched"},
		"sort":      {"day"},
	}
	accountValues := cloneURLValues(common)
	accountValues.Set("dimensions", "day")
	var accountReport youtubeAnalyticsReport
	endpoint := strings.TrimRight(s.config.YouTubeAnalyticsBase, "/") + "/reports?" + accountValues.Encode()
	if err := client.JSON(ctx, http.MethodGet, endpoint, accessToken, "", nil, &accountReport); err != nil {
		return err
	}
	if err := s.saveYouTubeAccountDaily(ctx, accountID, accountReport); err != nil {
		return err
	}

	videoIDs := make([]string, 0, len(publicationIDs))
	for videoID := range publicationIDs {
		videoIDs = append(videoIDs, videoID)
	}
	for offset := 0; offset < len(videoIDs); offset += 200 {
		limit := offset + 200
		if limit > len(videoIDs) {
			limit = len(videoIDs)
		}
		values := cloneURLValues(common)
		values.Set("dimensions", "day,video")
		values.Set("filters", "video=="+strings.Join(videoIDs[offset:limit], ","))
		values.Set("sort", "day,video")
		var report youtubeAnalyticsReport
		endpoint = strings.TrimRight(s.config.YouTubeAnalyticsBase, "/") + "/reports?" + values.Encode()
		if err := client.JSON(ctx, http.MethodGet, endpoint, accessToken, "", nil, &report); err != nil {
			return err
		}
		if err := s.saveYouTubePublicationDaily(ctx, publicationIDs, report); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) saveYouTubeAccountDaily(ctx context.Context, accountID string, report youtubeAnalyticsReport) error {
	columns := youtubeColumnIndex(report)
	for _, row := range report.Rows {
		date, ok := youtubeString(row, youtubeIndex(columns, "day"))
		if !ok {
			continue
		}
		_, err := s.pool.Exec(ctx, `
			INSERT INTO account_daily_metrics(platform_account_id,metric_date,views,likes,comments,shares,watch_time_ms)
			VALUES($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT(platform_account_id,metric_date) DO UPDATE SET
				views=excluded.views,likes=excluded.likes,comments=excluded.comments,
				shares=excluded.shares,watch_time_ms=excluded.watch_time_ms,updated_at=now()
		`, accountID, date, youtubeInt(row, youtubeIndex(columns, "views")), youtubeInt(row, youtubeIndex(columns, "likes")),
			youtubeInt(row, youtubeIndex(columns, "comments")), youtubeInt(row, youtubeIndex(columns, "shares")),
			youtubeInt(row, youtubeIndex(columns, "estimatedMinutesWatched"))*60_000)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) saveYouTubePublicationDaily(ctx context.Context, publicationIDs map[string]string, report youtubeAnalyticsReport) error {
	columns := youtubeColumnIndex(report)
	for _, row := range report.Rows {
		date, dateOK := youtubeString(row, youtubeIndex(columns, "day"))
		videoID, videoOK := youtubeString(row, youtubeIndex(columns, "video"))
		publicationID, known := publicationIDs[videoID]
		if !dateOK || !videoOK || !known {
			continue
		}
		_, err := s.pool.Exec(ctx, `
			INSERT INTO publication_daily_metrics(publication_id,metric_date,views,likes,comments,shares,watch_time_ms)
			VALUES($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT(publication_id,metric_date) DO UPDATE SET
				views=excluded.views,likes=excluded.likes,comments=excluded.comments,
				shares=excluded.shares,watch_time_ms=excluded.watch_time_ms,updated_at=now()
		`, publicationID, date, youtubeInt(row, youtubeIndex(columns, "views")), youtubeInt(row, youtubeIndex(columns, "likes")),
			youtubeInt(row, youtubeIndex(columns, "comments")), youtubeInt(row, youtubeIndex(columns, "shares")),
			youtubeInt(row, youtubeIndex(columns, "estimatedMinutesWatched"))*60_000)
		if err != nil {
			return err
		}
	}
	return nil
}

func youtubeColumnIndex(report youtubeAnalyticsReport) map[string]int {
	columns := make(map[string]int, len(report.ColumnHeaders))
	for index, header := range report.ColumnHeaders {
		columns[header.Name] = index
	}
	return columns
}

func youtubeIndex(columns map[string]int, name string) int {
	index, ok := columns[name]
	if !ok {
		return -1
	}
	return index
}

func youtubeString(row []any, index int) (string, bool) {
	if index < 0 || index >= len(row) {
		return "", false
	}
	value, ok := row[index].(string)
	return value, ok && value != ""
}

func youtubeInt(row []any, index int) int64 {
	if index < 0 || index >= len(row) {
		return 0
	}
	switch value := row[index].(type) {
	case float64:
		return int64(value)
	case string:
		number, _ := strconv.ParseFloat(value, 64)
		return int64(number)
	default:
		return 0
	}
}

func cloneURLValues(values url.Values) url.Values {
	copy := make(url.Values, len(values))
	for key, list := range values {
		copy[key] = append([]string(nil), list...)
	}
	return copy
}

var isoDurationPattern = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$`)

func parseISO8601Duration(value string) time.Duration {
	match := isoDurationPattern.FindStringSubmatch(value)
	if match == nil {
		return 0
	}
	number := func(index int) int64 {
		if match[index] == "" {
			return 0
		}
		result, _ := strconv.ParseInt(match[index], 10, 64)
		return result
	}
	return time.Duration(number(1))*24*time.Hour +
		time.Duration(number(2))*time.Hour +
		time.Duration(number(3))*time.Minute +
		time.Duration(number(4))*time.Second
}
