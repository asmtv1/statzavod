package config

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	DatabaseURL, HTTPAddr, Environment, CookieName, BootstrapEmail, BootstrapPassword, CORSOrigin                            string
	PublicBaseURL, TokenEncryptionKey, SMTPURL                                                                               string
	YouTubeClientID, YouTubeClientSecret, YouTubeRedirectURL, YouTubeOAuthBase, YouTubeAPIBase, YouTubeUploadBase            string
	YouTubeAnalyticsBase                                                                                                     string
	InstagramClientID, InstagramClientSecret, InstagramRedirectURL, InstagramOAuthBase                                       string
	InstagramTokenBase                                                                                                       string
	InstagramAPIBase, TikTokClientKey, TikTokClientSecret, TikTokRedirectURL, TikTokAPIBase                                  string
	InstagramFacebookRedirectURL, InstagramFacebookOAuthBase, InstagramFacebookGraphAPIBase                                  string
	InstagramFacebookClientID, InstagramFacebookClientSecret, InstagramFacebookConfigID                                      string
	VKClientID, VKClientSecret, VKRedirectURL, VKOAuthBase, VKAPIBase, VKAPIVersion                                          string
	ContentPublishingEnabled, ContentTikTokEnabled, ContentInstagramEnabled, ContentYouTubeEnabled, ContentVKEnabled         bool
	MediaS3Endpoint, MediaUploadOrigin, MediaS3Region, MediaS3Bucket, MediaS3AccessKey, MediaS3SecretKey, MediaPublicBaseURL string
	MediaDeliveryTokenKey                                                                                                    string
}

func Load() (Config, error) {
	publishingEnabled, err := featureFlag("CONTENT_PUBLISHING_ENABLED")
	if err != nil {
		return Config{}, err
	}
	tikTokEnabled, err := featureFlag("CONTENT_TIKTOK_ENABLED")
	if err != nil {
		return Config{}, err
	}
	instagramEnabled, err := featureFlag("CONTENT_INSTAGRAM_ENABLED")
	if err != nil {
		return Config{}, err
	}
	youTubeEnabled, err := featureFlag("CONTENT_YOUTUBE_ENABLED")
	if err != nil {
		return Config{}, err
	}
	vkEnabled, err := featureFlag("CONTENT_VK_ENABLED")
	if err != nil {
		return Config{}, err
	}
	c := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"), HTTPAddr: value("HTTP_ADDR", ":8080"),
		Environment: value("APP_ENV", "development"), CookieName: value("SESSION_COOKIE_NAME", "statzavod_session"),
		BootstrapEmail:    strings.ToLower(value("BOOTSTRAP_EMAIL", "admin@example.com")),
		BootstrapPassword: value("BOOTSTRAP_PASSWORD", "change-me-before-production"), CORSOrigin: value("CORS_ORIGIN", "http://localhost:5173"),
		PublicBaseURL:                 value("PUBLIC_BASE_URL", "http://localhost:5173"),
		TokenEncryptionKey:            os.Getenv("TOKEN_ENCRYPTION_KEY"),
		YouTubeClientID:               os.Getenv("YOUTUBE_OAUTH_CLIENT_ID"),
		YouTubeClientSecret:           os.Getenv("YOUTUBE_OAUTH_CLIENT_SECRET"),
		YouTubeRedirectURL:            value("YOUTUBE_OAUTH_REDIRECT_URL", "http://localhost:8080/api/v1/oauth/youtube/callback"),
		YouTubeOAuthBase:              value("YOUTUBE_OAUTH_BASE", "https://oauth2.googleapis.com"),
		YouTubeAPIBase:                value("YOUTUBE_API_BASE", "https://www.googleapis.com/youtube/v3"),
		YouTubeUploadBase:             value("YOUTUBE_UPLOAD_BASE", "https://www.googleapis.com/upload/youtube/v3"),
		YouTubeAnalyticsBase:          value("YOUTUBE_ANALYTICS_BASE", "https://youtubeanalytics.googleapis.com/v2"),
		InstagramClientID:             os.Getenv("INSTAGRAM_OAUTH_CLIENT_ID"),
		InstagramClientSecret:         os.Getenv("INSTAGRAM_OAUTH_CLIENT_SECRET"),
		InstagramRedirectURL:          value("INSTAGRAM_OAUTH_REDIRECT_URL", "http://localhost:8080/api/v1/oauth/instagram/callback"),
		InstagramOAuthBase:            value("INSTAGRAM_OAUTH_BASE", "https://www.instagram.com"),
		InstagramTokenBase:            value("INSTAGRAM_TOKEN_BASE", "https://api.instagram.com"),
		InstagramAPIBase:              value("INSTAGRAM_API_BASE", "https://graph.instagram.com"),
		InstagramFacebookRedirectURL:  value("INSTAGRAM_FACEBOOK_OAUTH_REDIRECT_URL", "http://localhost:8080/api/v1/oauth/instagram-facebook/callback"),
		InstagramFacebookOAuthBase:    value("INSTAGRAM_FACEBOOK_OAUTH_BASE", "https://www.facebook.com"),
		InstagramFacebookGraphAPIBase: value("INSTAGRAM_FACEBOOK_GRAPH_API_BASE", "https://graph.facebook.com"),
		InstagramFacebookClientID:     os.Getenv("INSTAGRAM_FACEBOOK_OAUTH_CLIENT_ID"),
		InstagramFacebookClientSecret: os.Getenv("INSTAGRAM_FACEBOOK_OAUTH_CLIENT_SECRET"),
		InstagramFacebookConfigID:     os.Getenv("INSTAGRAM_FACEBOOK_CONFIG_ID"),
		TikTokClientKey:               os.Getenv("TIKTOK_CLIENT_KEY"), TikTokClientSecret: os.Getenv("TIKTOK_CLIENT_SECRET"),
		TikTokRedirectURL:        value("TIKTOK_REDIRECT_URL", "http://localhost:8080/api/v1/oauth/tiktok/callback"),
		TikTokAPIBase:            value("TIKTOK_API_BASE", "https://open.tiktokapis.com"),
		VKClientID:               os.Getenv("VK_OAUTH_CLIENT_ID"),
		VKClientSecret:           os.Getenv("VK_OAUTH_CLIENT_SECRET"),
		VKRedirectURL:            value("VK_OAUTH_REDIRECT_URL", "http://localhost:8080/api/v1/oauth/vk/callback"),
		VKOAuthBase:              value("VK_OAUTH_BASE", "https://id.vk.ru"),
		VKAPIBase:                value("VK_API_BASE", "https://api.vk.ru"),
		VKAPIVersion:             value("VK_API_VERSION", "5.199"),
		SMTPURL:                  os.Getenv("SMTP_URL"),
		ContentPublishingEnabled: publishingEnabled,
		ContentTikTokEnabled:     tikTokEnabled,
		ContentInstagramEnabled:  instagramEnabled,
		ContentYouTubeEnabled:    youTubeEnabled,
		ContentVKEnabled:         vkEnabled,
		MediaS3Endpoint:          os.Getenv("MEDIA_S3_ENDPOINT"), MediaS3Region: value("MEDIA_S3_REGION", "ru-central1"), MediaS3Bucket: os.Getenv("MEDIA_S3_BUCKET"),
		MediaUploadOrigin: os.Getenv("MEDIA_UPLOAD_ORIGIN"),
		MediaS3AccessKey:  os.Getenv("MEDIA_S3_ACCESS_KEY"), MediaS3SecretKey: os.Getenv("MEDIA_S3_SECRET_KEY"), MediaPublicBaseURL: os.Getenv("MEDIA_PUBLIC_BASE_URL"),
		MediaDeliveryTokenKey: os.Getenv("MEDIA_DELIVERY_TOKEN_KEY"),
	}
	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if c.Environment == "production" && c.BootstrapPassword == "change-me-before-production" {
		return Config{}, fmt.Errorf("set a unique BOOTSTRAP_PASSWORD in production")
	}
	if c.Environment == "production" && c.TokenEncryptionKey == "" {
		return Config{}, fmt.Errorf("set TOKEN_ENCRYPTION_KEY in production")
	}
	if c.Environment == "production" {
		providerBases := []struct {
			name  string
			value string
			hosts []string
		}{
			{"YOUTUBE_OAUTH_BASE", c.YouTubeOAuthBase, []string{"oauth2.googleapis.com"}},
			{"YOUTUBE_API_BASE", c.YouTubeAPIBase, []string{"www.googleapis.com", "youtube.googleapis.com"}},
			{"YOUTUBE_UPLOAD_BASE", c.YouTubeUploadBase, []string{"www.googleapis.com", "youtube.googleapis.com"}},
			{"YOUTUBE_ANALYTICS_BASE", c.YouTubeAnalyticsBase, []string{"youtubeanalytics.googleapis.com"}},
			{"INSTAGRAM_OAUTH_BASE", c.InstagramOAuthBase, []string{"www.instagram.com"}},
			{"INSTAGRAM_TOKEN_BASE", c.InstagramTokenBase, []string{"api.instagram.com"}},
			{"INSTAGRAM_API_BASE", c.InstagramAPIBase, []string{"graph.instagram.com"}},
			{"INSTAGRAM_FACEBOOK_OAUTH_BASE", c.InstagramFacebookOAuthBase, []string{"www.facebook.com"}},
			{"INSTAGRAM_FACEBOOK_GRAPH_API_BASE", c.InstagramFacebookGraphAPIBase, []string{"graph.facebook.com"}},
			{"TIKTOK_API_BASE", c.TikTokAPIBase, []string{"open.tiktokapis.com"}},
			{"VK_OAUTH_BASE", c.VKOAuthBase, []string{"id.vk.ru"}},
			{"VK_API_BASE", c.VKAPIBase, []string{"api.vk.ru"}},
		}
		for _, providerBase := range providerBases {
			if err := validateProductionProviderBase(providerBase.value, providerBase.hosts...); err != nil {
				return Config{}, fmt.Errorf("%s must use an official HTTPS authority: %w", providerBase.name, err)
			}
		}
		if c.ContentPublishingEnabled {
			switch {
			case c.ContentTikTokEnabled && (c.TikTokClientKey == "" || c.TikTokClientSecret == ""):
				return Config{}, fmt.Errorf("set TIKTOK_CLIENT_KEY and TIKTOK_CLIENT_SECRET when TikTok publishing is enabled")
			case c.ContentYouTubeEnabled && (c.YouTubeClientID == "" || c.YouTubeClientSecret == ""):
				return Config{}, fmt.Errorf("set YOUTUBE_OAUTH_CLIENT_ID and YOUTUBE_OAUTH_CLIENT_SECRET when YouTube publishing is enabled")
			case c.ContentVKEnabled && (c.VKClientID == "" || c.VKClientSecret == ""):
				return Config{}, fmt.Errorf("set VK_OAUTH_CLIENT_ID and VK_OAUTH_CLIENT_SECRET when VK publishing is enabled")
			case c.ContentInstagramEnabled && !((c.InstagramClientID != "" && c.InstagramClientSecret != "") || (c.InstagramFacebookClientID != "" && c.InstagramFacebookClientSecret != "")):
				return Config{}, fmt.Errorf("configure an Instagram OAuth client pair when Instagram publishing is enabled")
			}
		}
	}
	if c.ContentPublishingEnabled && (c.MediaS3Endpoint == "" || c.MediaS3Bucket == "" || c.MediaS3AccessKey == "" || c.MediaS3SecretKey == "" || c.MediaPublicBaseURL == "" || c.MediaDeliveryTokenKey == "") {
		return Config{}, fmt.Errorf("set MEDIA_S3_ENDPOINT, MEDIA_S3_BUCKET, MEDIA_S3_ACCESS_KEY, MEDIA_S3_SECRET_KEY, MEDIA_PUBLIC_BASE_URL and MEDIA_DELIVERY_TOKEN_KEY when CONTENT_PUBLISHING_ENABLED=true")
	}
	if c.Environment == "production" && c.ContentPublishingEnabled && c.MediaUploadOrigin == "" {
		return Config{}, fmt.Errorf("set MEDIA_UPLOAD_ORIGIN when CONTENT_PUBLISHING_ENABLED=true in production")
	}
	if c.Environment == "production" && (c.MediaS3Endpoint != "" || c.MediaUploadOrigin != "") {
		storageOrigin, err := cleanHTTPSOrigin(c.MediaS3Endpoint)
		if err != nil {
			return Config{}, fmt.Errorf("MEDIA_S3_ENDPOINT must be a clean HTTPS origin: %w", err)
		}
		uploadOrigin, err := cleanHTTPSOrigin(c.MediaUploadOrigin)
		if err != nil {
			return Config{}, fmt.Errorf("MEDIA_UPLOAD_ORIGIN must be a clean HTTPS origin: %w", err)
		}
		if !strings.EqualFold(storageOrigin, uploadOrigin) {
			return Config{}, fmt.Errorf("MEDIA_UPLOAD_ORIGIN must equal the MEDIA_S3_ENDPOINT origin")
		}
	}
	if c.ContentPublishingEnabled {
		public, publicErr := url.Parse(strings.TrimSpace(c.MediaPublicBaseURL))
		storage, storageErr := url.Parse(strings.TrimSpace(c.MediaS3Endpoint))
		key, keyErr := base64.StdEncoding.DecodeString(strings.TrimSpace(c.MediaDeliveryTokenKey))
		if publicErr != nil || public.Scheme != "https" || public.Host == "" || public.User != nil || public.RawQuery != "" || public.Fragment != "" || (public.Path != "" && public.Path != "/") {
			return Config{}, fmt.Errorf("MEDIA_PUBLIC_BASE_URL must be a clean HTTPS origin")
		}
		if storageErr == nil && storage.Host != "" && strings.EqualFold(storage.Host, public.Host) {
			return Config{}, fmt.Errorf("MEDIA_PUBLIC_BASE_URL must not be the storage API host")
		}
		if keyErr != nil || len(key) != 32 {
			return Config{}, fmt.Errorf("MEDIA_DELIVERY_TOKEN_KEY must be exactly 32 bytes encoded as base64")
		}
	}
	return c, nil
}

// featureFlag deliberately accepts only the two documented values. A typo in
// production must stop the process before a worker can claim publishing jobs.
func featureFlag(key string) (bool, error) {
	switch os.Getenv(key) {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, fmt.Errorf("%s must be exactly true or false", key)
	}
}

func cleanHTTPSOrigin(value string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Port() != "" {
		return "", fmt.Errorf("invalid HTTPS origin")
	}
	return "https://" + strings.ToLower(strings.TrimSuffix(u.Hostname(), ".")), nil
}

func validateProductionProviderBase(value string, allowedHosts ...string) error {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" || u.User != nil || (u.Port() != "" && u.Port() != "443") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid HTTPS base URL")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	for _, allowed := range allowedHosts {
		if host == allowed {
			return nil
		}
	}
	return fmt.Errorf("host %q is not allowed", host)
}
func value(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
