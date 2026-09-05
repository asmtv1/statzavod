package httpserver

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/statzavod/statzavod/internal/config"
)

func TestContentPlatformFlagsAreIndependentAndRespectMasterFlag(t *testing.T) {
	s := &Server{config: config.Config{ContentPublishingEnabled: true, ContentInstagramEnabled: true}}
	for _, test := range []struct {
		platform string
		want     bool
	}{
		{"INSTAGRAM", true}, {"TIKTOK", false}, {"YOUTUBE", false}, {"VK", false}, {"UNKNOWN", false},
	} {
		if got := s.contentPlatformPublishingEnabled(test.platform); got != test.want {
			t.Fatalf("platform %s enabled=%t want=%t", test.platform, got, test.want)
		}
	}
	s.config.ContentPublishingEnabled = false
	if s.contentPlatformPublishingEnabled("INSTAGRAM") {
		t.Fatal("master publishing kill switch did not disable Instagram")
	}
}

func TestPublishingMetricsCollectorOnMigratedPostgres(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to a disposable PostgreSQL database migrated through 00026")
	}
	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var ready bool
	if err = pool.QueryRow(t.Context(), `SELECT to_regclass('public.media_cleanup_tasks') IS NOT NULL`).Scan(&ready); err != nil || !ready {
		t.Fatalf("TEST_DATABASE_URL must be migrated through 00026: ready=%v err=%v", ready, err)
	}
	snapshot, err := (&Server{pool: pool}).collectPublishingMetrics(t.Context())
	if err != nil {
		t.Fatalf("collect bounded production metrics: %v", err)
	}
	output := string(renderPublishingMetrics(snapshot))
	if !strings.Contains(output, "statzavod_content_publish_queue_depth") || !strings.HasSuffix(output, "# EOF\n") {
		t.Fatalf("collector returned invalid OpenMetrics: %s", output)
	}
}

func TestWorkerPlatformFlagStopsProviderBoundary(t *testing.T) {
	adapter := &fakePublishAdapter{publish: PublishResult{ExternalID: "unexpected"}}
	s := &Server{
		config:          config.Config{ContentPublishingEnabled: true, ContentYouTubeEnabled: false},
		publishAdapters: map[string]PublishAdapter{"YOUTUBE": adapter},
	}
	_, err := s.executeContentPublish(context.Background(), contentPublishJob{Platform: "YOUTUBE"})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled platform did not fail closed: %v", err)
	}
	if adapter.publishes != 0 || adapter.polls != 0 {
		t.Fatalf("disabled platform crossed provider boundary: publishes=%d polls=%d", adapter.publishes, adapter.polls)
	}
}

func TestPublishingMetricsHaveBoundedLabelsAndRedactProviderErrors(t *testing.T) {
	rawCodes := []string{
		"AUTH_token_very-secret", "permission-account-123", "rate_limit", "timeout operation-456",
		"invalid_schema", "unclassified secret@example.com bearer-credential",
	}
	byClass := map[string]float64{}
	for _, code := range rawCodes {
		byClass[sanitizeProviderErrorClass(code)]++
	}
	snapshot := publishingMetricsSnapshot{Results: map[string]float64{"success": 2, "partial": 1, "failure": 3}}
	for class, value := range byClass {
		snapshot.ProviderErrors = append(snapshot.ProviderErrors, providerErrorMetric{Platform: "TIKTOK", Class: class, Value: value})
	}
	output := string(renderPublishingMetrics(snapshot))
	for _, forbidden := range []string{"very-secret", "account-123", "operation-456", "secret@example.com", "bearer-credential"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("metrics leaked provider data %q: %s", forbidden, output)
		}
	}
	if strings.Count(output, "statzavod_content_publish_queue_depth{") != len(publishingMetricPlatforms) {
		t.Fatalf("queue metric cardinality is not fixed: %s", output)
	}
	if strings.Count(output, "statzavod_content_provider_errors_total{") > len(publishingMetricPlatforms)*6 {
		t.Fatalf("provider error cardinality exceeded its fixed bound: %s", output)
	}
	for _, forbiddenLabel := range []string{"tenant=", "organization=", "account=", "operation="} {
		if strings.Contains(output, forbiddenLabel) {
			t.Fatalf("forbidden high-cardinality label %q: %s", forbiddenLabel, output)
		}
	}
	if !strings.HasSuffix(output, "# EOF\n") {
		t.Fatalf("invalid OpenMetrics terminator: %q", output)
	}
}
