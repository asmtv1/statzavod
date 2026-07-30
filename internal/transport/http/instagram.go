package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type instagramAccount struct {
	ID                string `json:"id"`
	UserID            string `json:"user_id"`
	Username          string `json:"username"`
	Name              string `json:"name"`
	AccountType       string `json:"account_type"`
	ProfilePictureURL string `json:"profile_picture_url"`
	FollowersCount    int64  `json:"followers_count"`
	FollowsCount      int64  `json:"follows_count"`
	MediaCount        int64  `json:"media_count"`
}

type instagramMedia struct {
	ID               string `json:"id"`
	Caption          string `json:"caption"`
	MediaType        string `json:"media_type"`
	MediaProductType string `json:"media_product_type"`
	Permalink        string `json:"permalink"`
	ThumbnailURL     string `json:"thumbnail_url"`
	MediaURL         string `json:"media_url"`
	Timestamp        string `json:"timestamp"`
	Username         string `json:"username"`
	LikeCount        int64  `json:"like_count"`
	CommentsCount    int64  `json:"comments_count"`
}

type instagramInsightResponse struct {
	Data []struct {
		Name   string `json:"name"`
		Period string `json:"period"`
		Values []struct {
			Value   any    `json:"value"`
			EndTime string `json:"end_time"`
		} `json:"values"`
		TotalValue struct {
			Value any `json:"value"`
		} `json:"total_value"`
	} `json:"data"`
}

type instagramMediaMetrics struct {
	Views              *int64
	Reach              *int64
	Likes              *int64
	Comments           *int64
	Shares             *int64
	Saves              *int64
	Interactions       *int64
	WatchTimeMS        *int64
	AverageWatchTimeMS *int64
}

func (s *Server) syncInstagramAccount(ctx context.Context, job platformSyncJob, accessToken string) (syncResult, error) {
	client := newProviderClient("Instagram")
	account, err := s.fetchInstagramAccount(ctx, client, accessToken)
	if err != nil {
		return syncResult{}, err
	}
	metadata, _ := json.Marshal(map[string]any{"accountType": account.AccountType})
	if _, err = s.pool.Exec(ctx, `
		INSERT INTO account_metric_snapshots(platform_account_id,followers,follows,media_count,metadata)
		VALUES($1,$2,$3,$4,$5::jsonb)
	`, job.AccountID, account.FollowersCount, account.FollowsCount, account.MediaCount, string(metadata)); err != nil {
		return syncResult{}, err
	}
	if _, err = s.pool.Exec(ctx, `
		UPDATE platform_accounts SET username=$2,display_name=$3,avatar_url=$4,account_type=$5,updated_at=now()
		WHERE id=$1
	`, job.AccountID, account.Username, firstNonEmpty(account.Name, account.Username), account.ProfilePictureURL, account.AccountType); err != nil {
		return syncResult{}, err
	}

	media, err := s.fetchInstagramMedia(ctx, client, accessToken)
	if err != nil {
		return syncResult{}, err
	}
	result := syncResult{RecordsRead: len(media)}
	for _, item := range media {
		metrics, metricErr := s.fetchInstagramMediaInsights(ctx, client, accessToken, item)
		if metricErr != nil {
			return result, metricErr
		}
		if _, writeErr := s.upsertInstagramMedia(ctx, job, item, metrics); writeErr != nil {
			return result, writeErr
		}
		result.RecordsWritten++
	}
	if err := s.syncInstagramAccountInsights(ctx, client, accessToken, job.AccountID); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Server) fetchInstagramAccount(ctx context.Context, client providerClient, accessToken string) (instagramAccount, error) {
	fields := "id,user_id,username,name,account_type,profile_picture_url,followers_count,follows_count,media_count"
	endpoint := strings.TrimRight(s.config.InstagramAPIBase, "/") + "/me?" + url.Values{
		"fields":       {fields},
		"access_token": {accessToken},
	}.Encode()
	var account instagramAccount
	if err := client.JSON(ctx, http.MethodGet, endpoint, "", "", nil, &account); err != nil {
		return instagramAccount{}, err
	}
	if account.ID == "" {
		account.ID = account.UserID
	}
	if account.ID == "" || account.Username == "" {
		return instagramAccount{}, &providerError{Platform: "Instagram", Kind: providerSchema, Message: "account identity is incomplete"}
	}
	return account, nil
}

func (s *Server) fetchInstagramMedia(ctx context.Context, client providerClient, accessToken string) ([]instagramMedia, error) {
	fields := "id,caption,media_type,media_product_type,permalink,thumbnail_url,media_url,timestamp,username,like_count,comments_count"
	next := strings.TrimRight(s.config.InstagramAPIBase, "/") + "/me/media?" + url.Values{
		"fields":       {fields},
		"limit":        {"100"},
		"access_token": {accessToken},
	}.Encode()
	items := make([]instagramMedia, 0, 100)
	for pages := 0; next != "" && pages < 200; pages++ {
		var response struct {
			Data   []instagramMedia `json:"data"`
			Paging struct {
				Next string `json:"next"`
			} `json:"paging"`
		}
		if err := client.JSON(ctx, http.MethodGet, next, "", "", nil, &response); err != nil {
			return nil, err
		}
		items = append(items, response.Data...)
		next = response.Paging.Next
	}
	return items, nil
}

func (s *Server) fetchInstagramMediaInsights(ctx context.Context, client providerClient, accessToken string, media instagramMedia) (instagramMediaMetrics, error) {
	metrics := instagramMediaMetrics{
		Likes:    int64Pointer(media.LikeCount),
		Comments: int64Pointer(media.CommentsCount),
	}
	type metricTarget struct {
		name string
		set  func(*int64)
	}
	targets := []metricTarget{
		{"views", func(value *int64) { metrics.Views = value }},
		{"reach", func(value *int64) { metrics.Reach = value }},
		{"shares", func(value *int64) { metrics.Shares = value }},
		{"saved", func(value *int64) { metrics.Saves = value }},
		{"total_interactions", func(value *int64) { metrics.Interactions = value }},
		{"ig_reels_video_view_total_time", func(value *int64) { metrics.WatchTimeMS = value }},
		{"ig_reels_avg_watch_time", func(value *int64) { metrics.AverageWatchTimeMS = value }},
	}
	names := make([]string, 0, len(targets))
	targetByName := make(map[string]func(*int64), len(targets))
	for _, target := range targets {
		names = append(names, target.name)
		targetByName[target.name] = target.set
	}
	endpoint := strings.TrimRight(s.config.InstagramAPIBase, "/") + "/" + url.PathEscape(media.ID) + "/insights?" + url.Values{
		"metric":       {strings.Join(names, ",")},
		"access_token": {accessToken},
	}.Encode()
	var response instagramInsightResponse
	err := client.JSON(ctx, http.MethodGet, endpoint, "", "", nil, &response)
	if err != nil {
		// Metric availability depends on media type and age. Authentication and
		// permission errors remain fatal; unsupported metric groups are skipped.
		if isProviderKind(err, providerAuth, providerPermission, providerRateLimit, providerRetryable) {
			return metrics, err
		}
		return metrics, nil
	}
	for index, item := range response.Data {
		if set := targetByName[item.Name]; set != nil {
			set(instagramInsightValue(instagramInsightResponse{Data: response.Data[index : index+1]}))
		}
	}
	return metrics, nil
}

func (s *Server) upsertInstagramMedia(ctx context.Context, job platformSyncJob, media instagramMedia, metrics instagramMediaMetrics) (string, error) {
	publishedAt, err := parseInstagramTimestamp(media.Timestamp)
	if err != nil {
		return "", &providerError{Platform: "Instagram", Kind: providerSchema, Message: "media publication date is invalid"}
	}
	publicationType := strings.ToUpper(media.MediaProductType)
	if publicationType == "" || publicationType == "FEED" {
		publicationType = strings.ToUpper(media.MediaType)
	}
	if publicationType == "" {
		publicationType = "MEDIA"
	}
	thumbnail := media.ThumbnailURL
	if thumbnail == "" && media.MediaType == "IMAGE" {
		thumbnail = media.MediaURL
	}
	title := media.Caption
	if len([]rune(title)) > 120 {
		title = string([]rune(title)[:120])
	}
	metadata, _ := json.Marshal(map[string]any{
		"mediaType":          media.MediaType,
		"mediaProductType":   media.MediaProductType,
		"averageWatchTimeMs": metrics.AverageWatchTimeMS,
	})
	var publicationID string
	err = s.pool.QueryRow(ctx, `
		INSERT INTO publications(
			organization_id,creator_id,platform_account_id,platform,external_id,
			publication_type,title,description,permalink,thumbnail_url,published_at,metadata
		) VALUES($1,$2,$3,'INSTAGRAM',$4,$5,$6,$7,$8,$9,$10,$11::jsonb)
		ON CONFLICT(organization_id,platform,external_id) DO UPDATE SET
			creator_id=excluded.creator_id,platform_account_id=excluded.platform_account_id,
			publication_type=excluded.publication_type,title=excluded.title,description=excluded.description,
			permalink=excluded.permalink,thumbnail_url=excluded.thumbnail_url,published_at=excluded.published_at,
			status='ACTIVE',metadata=excluded.metadata,updated_at=now()
		RETURNING id
	`, job.OrganizationID, job.CreatorID, job.AccountID, media.ID, publicationType, title, media.Caption,
		media.Permalink, thumbnail, publishedAt, string(metadata)).Scan(&publicationID)
	if err != nil {
		return "", err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO publication_metric_snapshots(
			publication_id,views,reach,likes,comments,shares,saves,watch_time_ms,completeness_status
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'PARTIAL')
	`, publicationID, metrics.Views, metrics.Reach, metrics.Likes, metrics.Comments, metrics.Shares, metrics.Saves, metrics.WatchTimeMS)
	return publicationID, err
}

func parseInstagramTimestamp(value string) (time.Time, error) {
	// Meta commonly returns offsets as +0000, while RFC3339 requires +00:00.
	// Accept both representations, including fractional seconds.
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999-0700",
		"2006-01-02T15:04:05-0700",
	}
	var parseErr error
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed, nil
		}
		parseErr = err
	}
	return time.Time{}, parseErr
}

func (s *Server) syncInstagramAccountInsights(ctx context.Context, client providerClient, accessToken, accountID string) error {
	end := time.Now().UTC()
	start := end.AddDate(0, 0, -30)
	metrics := []string{"views", "reach", "accounts_engaged", "total_interactions", "follows_and_unfollows"}
	for _, metric := range metrics {
		values := url.Values{
			"metric":       {metric},
			"period":       {"day"},
			"since":        {start.Format(time.RFC3339)},
			"until":        {end.Format(time.RFC3339)},
			"access_token": {accessToken},
		}
		endpoint := strings.TrimRight(s.config.InstagramAPIBase, "/") + "/me/insights?" + values.Encode()
		var response instagramInsightResponse
		err := client.JSON(ctx, http.MethodGet, endpoint, "", "", nil, &response)
		if err != nil {
			if isProviderKind(err, providerAuth, providerPermission, providerRateLimit, providerRetryable) {
				return err
			}
			continue
		}
		for _, item := range response.Data {
			for _, point := range item.Values {
				value := anyInt64(point.Value)
				if value == nil || point.EndTime == "" {
					continue
				}
				date, parseErr := time.Parse(time.RFC3339, point.EndTime)
				if parseErr != nil {
					continue
				}
				if err := s.upsertInstagramAccountDailyMetric(ctx, accountID, date, metric, *value); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *Server) upsertInstagramAccountDailyMetric(ctx context.Context, accountID string, date time.Time, metric string, value int64) error {
	column := map[string]string{
		"views":              "views",
		"reach":              "reach",
		"total_interactions": "interactions",
	}[metric]
	if column == "" {
		metadata, _ := json.Marshal(map[string]int64{metric: value})
		_, err := s.pool.Exec(ctx, `
			INSERT INTO account_daily_metrics(platform_account_id,metric_date,metadata)
			VALUES($1,$2,$3::jsonb)
			ON CONFLICT(platform_account_id,metric_date) DO UPDATE SET
				metadata=account_daily_metrics.metadata || excluded.metadata,updated_at=now()
		`, accountID, date.Format("2006-01-02"), string(metadata))
		return err
	}
	query := fmt.Sprintf(`
		INSERT INTO account_daily_metrics(platform_account_id,metric_date,%s)
		VALUES($1,$2,$3)
		ON CONFLICT(platform_account_id,metric_date) DO UPDATE SET
			%s=excluded.%s,updated_at=now()
	`, column, column, column)
	_, err := s.pool.Exec(ctx, query, accountID, date.Format("2006-01-02"), value)
	return err
}

func instagramInsightValue(response instagramInsightResponse) *int64 {
	if len(response.Data) == 0 {
		return nil
	}
	item := response.Data[0]
	if value := anyInt64(item.TotalValue.Value); value != nil {
		return value
	}
	if len(item.Values) > 0 {
		return anyInt64(item.Values[len(item.Values)-1].Value)
	}
	return nil
}

func anyInt64(value any) *int64 {
	switch number := value.(type) {
	case float64:
		result := int64(number)
		return &result
	case int64:
		return &number
	case json.Number:
		if result, err := number.Int64(); err == nil {
			return &result
		}
	case string:
		return parseInt64(number)
	case map[string]any:
		var sum int64
		found := false
		for _, nested := range number {
			if value := anyInt64(nested); value != nil {
				sum += *value
				found = true
			}
		}
		if found {
			return &sum
		}
	}
	return nil
}

func int64Pointer(value int64) *int64 {
	return &value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
