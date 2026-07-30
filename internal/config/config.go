package config

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	DatabaseURL, HTTPAddr, Environment, CookieName, BootstrapEmail, BootstrapPassword, CORSOrigin string
	PublicBaseURL, TokenEncryptionKey, SMTPURL                                                    string
	YouTubeClientID, YouTubeClientSecret, YouTubeRedirectURL, YouTubeOAuthBase, YouTubeAPIBase    string
	YouTubeAnalyticsBase                                                                          string
	InstagramClientID, InstagramClientSecret, InstagramRedirectURL, InstagramOAuthBase            string
	InstagramTokenBase                                                                            string
	InstagramAPIBase, TikTokClientKey, TikTokClientSecret, TikTokRedirectURL, TikTokAPIBase       string
	VKClientID, VKClientSecret, VKRedirectURL, VKOAuthBase, VKAPIBase, VKAPIVersion               string
}

func Load() (Config, error) {
	c := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"), HTTPAddr: value("HTTP_ADDR", ":8080"),
		Environment: value("APP_ENV", "development"), CookieName: value("SESSION_COOKIE_NAME", "statzavod_session"),
		BootstrapEmail:    strings.ToLower(value("BOOTSTRAP_EMAIL", "admin@example.com")),
		BootstrapPassword: value("BOOTSTRAP_PASSWORD", "change-me-before-production"), CORSOrigin: value("CORS_ORIGIN", "http://localhost:5173"),
		PublicBaseURL:         value("PUBLIC_BASE_URL", "http://localhost:5173"),
		TokenEncryptionKey:    os.Getenv("TOKEN_ENCRYPTION_KEY"),
		YouTubeClientID:       os.Getenv("YOUTUBE_OAUTH_CLIENT_ID"),
		YouTubeClientSecret:   os.Getenv("YOUTUBE_OAUTH_CLIENT_SECRET"),
		YouTubeRedirectURL:    value("YOUTUBE_OAUTH_REDIRECT_URL", "http://localhost:8080/api/v1/oauth/youtube/callback"),
		YouTubeOAuthBase:      value("YOUTUBE_OAUTH_BASE", "https://oauth2.googleapis.com"),
		YouTubeAPIBase:        value("YOUTUBE_API_BASE", "https://www.googleapis.com/youtube/v3"),
		YouTubeAnalyticsBase:  value("YOUTUBE_ANALYTICS_BASE", "https://youtubeanalytics.googleapis.com/v2"),
		InstagramClientID:     os.Getenv("INSTAGRAM_OAUTH_CLIENT_ID"),
		InstagramClientSecret: os.Getenv("INSTAGRAM_OAUTH_CLIENT_SECRET"),
		InstagramRedirectURL:  value("INSTAGRAM_OAUTH_REDIRECT_URL", "http://localhost:8080/api/v1/oauth/instagram/callback"),
		InstagramOAuthBase:    value("INSTAGRAM_OAUTH_BASE", "https://www.instagram.com"),
		InstagramTokenBase:    value("INSTAGRAM_TOKEN_BASE", "https://api.instagram.com"),
		InstagramAPIBase:      value("INSTAGRAM_API_BASE", "https://graph.instagram.com"),
		TikTokClientKey:       os.Getenv("TIKTOK_CLIENT_KEY"), TikTokClientSecret: os.Getenv("TIKTOK_CLIENT_SECRET"),
		TikTokRedirectURL: value("TIKTOK_REDIRECT_URL", "http://localhost:8080/api/v1/oauth/tiktok/callback"),
		TikTokAPIBase:     value("TIKTOK_API_BASE", "https://open.tiktokapis.com"),
		VKClientID:        os.Getenv("VK_OAUTH_CLIENT_ID"),
		VKClientSecret:    os.Getenv("VK_OAUTH_CLIENT_SECRET"),
		VKRedirectURL:     value("VK_OAUTH_REDIRECT_URL", "http://localhost:8080/api/v1/oauth/vk/callback"),
		VKOAuthBase:       value("VK_OAUTH_BASE", "https://oauth.vk.ru"),
		VKAPIBase:         value("VK_API_BASE", "https://api.vk.ru"),
		VKAPIVersion:      value("VK_API_VERSION", "5.199"),
		SMTPURL:           os.Getenv("SMTP_URL"),
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
	return c, nil
}
func value(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
