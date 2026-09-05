package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestValidateProductionProviderBase(t *testing.T) {
	for _, invalid := range []string{
		"http://oauth2.googleapis.com",
		"https://oauth2.googleapis.com.evil.example",
		"https://user@oauth2.googleapis.com",
		"https://oauth2.googleapis.com:8443",
		"https://oauth2.googleapis.com?next=https://evil.example",
	} {
		if err := validateProductionProviderBase(invalid, "oauth2.googleapis.com"); err == nil {
			t.Fatalf("unsafe production provider base accepted: %s", invalid)
		}
	}
	if err := validateProductionProviderBase("https://oauth2.googleapis.com/token", "oauth2.googleapis.com"); err != nil {
		t.Fatalf("official provider base rejected: %v", err)
	}
}

func TestProductionMediaUploadOriginMustMatchStorage(t *testing.T) {
	base := map[string]string{
		"DATABASE_URL": "postgres://test", "APP_ENV": "production",
		"BOOTSTRAP_PASSWORD": "unique", "TOKEN_ENCRYPTION_KEY": "configured",
		"CONTENT_PUBLISHING_ENABLED": "true", "MEDIA_S3_ENDPOINT": "https://storage.yandexcloud.net/",
		"MEDIA_UPLOAD_ORIGIN": "https://storage.yandexcloud.net", "MEDIA_S3_BUCKET": "private-bucket",
		"MEDIA_S3_ACCESS_KEY": "access", "MEDIA_S3_SECRET_KEY": "secret",
		"MEDIA_PUBLIC_BASE_URL":    "https://media.statzavod.ru",
		"MEDIA_DELIVERY_TOKEN_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32)),
	}
	for key, value := range base {
		t.Setenv(key, value)
	}
	if _, err := Load(); err != nil {
		t.Fatalf("valid production media origins rejected: %v", err)
	}

	t.Setenv("MEDIA_UPLOAD_ORIGIN", "https://evil.example")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "must equal") {
		t.Fatalf("mismatched upload origin accepted: %v", err)
	}

	t.Setenv("MEDIA_UPLOAD_ORIGIN", "https://storage.yandexcloud.net/bucket")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "clean HTTPS origin") {
		t.Fatalf("upload origin with path accepted: %v", err)
	}
}

func TestContentPublishingFeatureFlagsAreStrictAndIndependent(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("MEDIA_S3_ENDPOINT", "https://storage.example")
	t.Setenv("MEDIA_S3_BUCKET", "private")
	t.Setenv("MEDIA_S3_ACCESS_KEY", "access")
	t.Setenv("MEDIA_S3_SECRET_KEY", "secret")
	t.Setenv("MEDIA_PUBLIC_BASE_URL", "https://media.example")
	t.Setenv("MEDIA_DELIVERY_TOKEN_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	flags := []string{"CONTENT_PUBLISHING_ENABLED", "CONTENT_TIKTOK_ENABLED", "CONTENT_INSTAGRAM_ENABLED", "CONTENT_YOUTUBE_ENABLED", "CONTENT_VK_ENABLED"}
	for _, flag := range flags {
		t.Setenv(flag, "false")
	}
	for _, enabled := range flags {
		for _, flag := range flags {
			t.Setenv(flag, "false")
		}
		t.Setenv(enabled, "true")
		c, err := Load()
		if err != nil {
			t.Fatalf("load with only %s enabled: %v", enabled, err)
		}
		got := map[string]bool{
			"CONTENT_PUBLISHING_ENABLED": c.ContentPublishingEnabled,
			"CONTENT_TIKTOK_ENABLED":     c.ContentTikTokEnabled,
			"CONTENT_INSTAGRAM_ENABLED":  c.ContentInstagramEnabled,
			"CONTENT_YOUTUBE_ENABLED":    c.ContentYouTubeEnabled,
			"CONTENT_VK_ENABLED":         c.ContentVKEnabled,
		}
		for flag, value := range got {
			if value != (flag == enabled) {
				t.Fatalf("%s changed when only %s was enabled", flag, enabled)
			}
		}
	}
	t.Setenv("CONTENT_VK_ENABLED", "TRUE")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CONTENT_VK_ENABLED") {
		t.Fatalf("invalid flag value was not reported: %v", err)
	}
}

func TestProductionEnabledProviderReportsMissingCredentials(t *testing.T) {
	base := map[string]string{
		"DATABASE_URL": "postgres://test", "APP_ENV": "production",
		"BOOTSTRAP_PASSWORD": "unique", "TOKEN_ENCRYPTION_KEY": "configured",
		"CONTENT_PUBLISHING_ENABLED": "true", "CONTENT_YOUTUBE_ENABLED": "true",
		"MEDIA_S3_ENDPOINT": "https://storage.yandexcloud.net", "MEDIA_UPLOAD_ORIGIN": "https://storage.yandexcloud.net",
		"MEDIA_S3_BUCKET": "private", "MEDIA_S3_ACCESS_KEY": "access", "MEDIA_S3_SECRET_KEY": "secret",
		"MEDIA_PUBLIC_BASE_URL": "https://media.example", "MEDIA_DELIVERY_TOKEN_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32)),
	}
	for key, value := range base {
		t.Setenv(key, value)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "YOUTUBE_OAUTH_CLIENT_ID") {
		t.Fatalf("missing enabled-provider credentials were not reported before startup: %v", err)
	}
}
