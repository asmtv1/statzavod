package httpserver

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

var publishingMetricPlatforms = []string{"INSTAGRAM", "TIKTOK", "VK", "YOUTUBE"}
var providerLatencyBuckets = []float64{1, 5, 15, 30, 60, 120, 300}

type platformMetric struct {
	Platform string
	Value    float64
}

type providerLatencyMetric struct {
	Platform string
	Count    float64
	Sum      float64
	Buckets  []float64
}

type providerErrorMetric struct {
	Platform, Class string
	Value           float64
}

type publishingMetricsSnapshot struct {
	QueueDepth, OldestDue, Retries, Reauth, ReauthOldest []platformMetric
	Results                                              map[string]float64
	ProviderLatency                                      []providerLatencyMetric
	ProviderErrors                                       []providerErrorMetric
	TranscodingFailures, OrphanUploads, CleanupErrors    float64
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	if s.pool == nil {
		http.Error(w, "metrics unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	snapshot, err := s.collectPublishingMetrics(ctx)
	if err != nil {
		http.Error(w, "metrics unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/openmetrics-text; version=1.0.0; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(renderPublishingMetrics(snapshot))
}

func (s *Server) collectPublishingMetrics(ctx context.Context) (publishingMetricsSnapshot, error) {
	m := publishingMetricsSnapshot{Results: map[string]float64{"success": 0, "partial": 0, "failure": 0}}
	rows, err := s.pool.Query(ctx, `SELECT target.platform::text,count(*)::float8,GREATEST(0,EXTRACT(EPOCH FROM now()-min(job.run_at)))::float8 FROM content_publish_jobs job JOIN content_publish_targets target ON target.id=job.target_id AND target.organization_id=job.organization_id WHERE job.status IN ('READY','RETRY_SCHEDULED') AND job.run_at<=now() GROUP BY target.platform`)
	if err != nil {
		return m, err
	}
	for rows.Next() {
		var p platformMetric
		var age float64
		if err = rows.Scan(&p.Platform, &p.Value, &age); err != nil {
			rows.Close()
			return m, err
		}
		m.QueueDepth = append(m.QueueDepth, p)
		m.OldestDue = append(m.OldestDue, platformMetric{p.Platform, age})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return m, err
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, `SELECT CASE status WHEN 'PUBLISHED' THEN 'success' WHEN 'PARTIALLY_PUBLISHED' THEN 'partial' ELSE 'failure' END,count(*)::float8 FROM content_revisions WHERE status IN ('PUBLISHED','PARTIALLY_PUBLISHED','FAILED') GROUP BY status`)
	if err != nil {
		return m, err
	}
	for rows.Next() {
		var result string
		var value float64
		if err = rows.Scan(&result, &value); err != nil {
			rows.Close()
			return m, err
		}
		m.Results[result] += value
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return m, err
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, `SELECT target.platform::text,COALESCE(sum(GREATEST(job.execution_count-1,0)),0)::float8 FROM content_publish_jobs job JOIN content_publish_targets target ON target.id=job.target_id AND target.organization_id=job.organization_id GROUP BY target.platform`)
	if err != nil {
		return m, err
	}
	for rows.Next() {
		var p platformMetric
		if err = rows.Scan(&p.Platform, &p.Value); err != nil {
			rows.Close()
			return m, err
		}
		m.Retries = append(m.Retries, p)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return m, err
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, `SELECT target.platform::text,count(*)::float8,COALESCE(sum(EXTRACT(EPOCH FROM attempt.finished_at-attempt.started_at)),0)::float8,ARRAY[sum((EXTRACT(EPOCH FROM attempt.finished_at-attempt.started_at)<=1)::int),sum((EXTRACT(EPOCH FROM attempt.finished_at-attempt.started_at)<=5)::int),sum((EXTRACT(EPOCH FROM attempt.finished_at-attempt.started_at)<=15)::int),sum((EXTRACT(EPOCH FROM attempt.finished_at-attempt.started_at)<=30)::int),sum((EXTRACT(EPOCH FROM attempt.finished_at-attempt.started_at)<=60)::int),sum((EXTRACT(EPOCH FROM attempt.finished_at-attempt.started_at)<=120)::int),sum((EXTRACT(EPOCH FROM attempt.finished_at-attempt.started_at)<=300)::int)]::float8[] FROM content_publish_attempts attempt JOIN content_publish_targets target ON target.id=attempt.target_id AND target.organization_id=attempt.organization_id WHERE attempt.finished_at IS NOT NULL GROUP BY target.platform`)
	if err != nil {
		return m, err
	}
	for rows.Next() {
		var p providerLatencyMetric
		if err = rows.Scan(&p.Platform, &p.Count, &p.Sum, &p.Buckets); err != nil {
			rows.Close()
			return m, err
		}
		m.ProviderLatency = append(m.ProviderLatency, p)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return m, err
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, `SELECT platform::text,count(*)::float8,GREATEST(0,EXTRACT(EPOCH FROM now()-min(updated_at)))::float8 FROM content_publish_targets WHERE status='WAITING_FOR_REAUTH' GROUP BY platform`)
	if err != nil {
		return m, err
	}
	for rows.Next() {
		var p platformMetric
		var age float64
		if err = rows.Scan(&p.Platform, &p.Value, &age); err != nil {
			rows.Close()
			return m, err
		}
		m.Reauth = append(m.Reauth, p)
		m.ReauthOldest = append(m.ReauthOldest, platformMetric{p.Platform, age})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return m, err
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, `SELECT target.platform::text,COALESCE(attempt.error_code,''),count(*)::float8 FROM content_publish_attempts attempt JOIN content_publish_targets target ON target.id=attempt.target_id AND target.organization_id=attempt.organization_id WHERE attempt.status IN ('FAILED','WAITING_FOR_REAUTH') GROUP BY target.platform,attempt.error_code`)
	if err != nil {
		return m, err
	}
	byError := map[string]float64{}
	for rows.Next() {
		var platform, code string
		var value float64
		if err = rows.Scan(&platform, &code, &value); err != nil {
			rows.Close()
			return m, err
		}
		class := sanitizeProviderErrorClass(code)
		byError[platform+"\x00"+class] += value
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return m, err
	}
	rows.Close()
	for key, value := range byError {
		parts := strings.SplitN(key, "\x00", 2)
		m.ProviderErrors = append(m.ProviderErrors, providerErrorMetric{parts[0], parts[1], value})
	}

	err = s.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='REJECTED' AND rejected_reason IS NOT NULL)::float8,count(*) FILTER (WHERE status='UPLOADING' AND delete_after IS NOT NULL AND delete_after<=now())::float8 FROM media_assets`).Scan(&m.TranscodingFailures, &m.OrphanUploads)
	if err != nil {
		return m, err
	}
	var expiredSessions float64
	if err = s.pool.QueryRow(ctx, `SELECT count(*)::float8 FROM media_upload_sessions WHERE status='ACTIVE' AND expires_at<=now()`).Scan(&expiredSessions); err != nil {
		return m, err
	}
	m.OrphanUploads += expiredSessions
	if err = s.pool.QueryRow(ctx, `SELECT count(*)::float8 FROM media_cleanup_tasks WHERE status IN ('PENDING','RUNNING') AND NULLIF(last_error,'') IS NOT NULL`).Scan(&m.CleanupErrors); err != nil {
		return m, err
	}
	return m, nil
}

func sanitizeProviderErrorClass(code string) string {
	value := strings.ToUpper(strings.TrimSpace(code))
	switch {
	case strings.Contains(value, "AUTH"), strings.Contains(value, "TOKEN"), strings.Contains(value, "REAUTH"):
		return "auth"
	case strings.Contains(value, "PERMISSION"), strings.Contains(value, "SCOPE"), strings.Contains(value, "FORBIDDEN"):
		return "permission"
	case strings.Contains(value, "RATE"), strings.Contains(value, "THROTTL"), strings.Contains(value, "429"):
		return "rate_limit"
	case strings.Contains(value, "TIMEOUT"), strings.Contains(value, "RETRY"), strings.Contains(value, "UNAVAILABLE"):
		return "retryable"
	case strings.Contains(value, "SCHEMA"), strings.Contains(value, "INVALID"), strings.Contains(value, "FORMAT"):
		return "schema"
	default:
		return "other"
	}
}

func renderPublishingMetrics(m publishingMetricsSnapshot) []byte {
	var b bytes.Buffer
	writePlatform := func(name, typ string, values []platformMetric) {
		fmt.Fprintf(&b, "# TYPE %s %s\n", name, typ)
		by := map[string]float64{}
		for _, v := range values {
			by[v.Platform] = v.Value
		}
		for _, p := range publishingMetricPlatforms {
			fmt.Fprintf(&b, "%s{platform=%q} %g\n", name, p, by[p])
		}
	}
	writePlatform("statzavod_content_publish_queue_depth", "gauge", m.QueueDepth)
	writePlatform("statzavod_content_publish_oldest_due_seconds", "gauge", m.OldestDue)
	fmt.Fprintln(&b, "# TYPE statzavod_content_publish_results_total counter")
	for _, result := range []string{"failure", "partial", "success"} {
		fmt.Fprintf(&b, "statzavod_content_publish_results_total{result=%q} %g\n", result, m.Results[result])
	}
	writePlatform("statzavod_content_publish_retries_total", "counter", m.Retries)
	latency := map[string]providerLatencyMetric{}
	for _, v := range m.ProviderLatency {
		latency[v.Platform] = v
	}
	fmt.Fprintln(&b, "# TYPE statzavod_content_provider_latency_seconds histogram")
	for _, p := range publishingMetricPlatforms {
		v := latency[p]
		for i, le := range providerLatencyBuckets {
			n := float64(0)
			if i < len(v.Buckets) {
				n = v.Buckets[i]
			}
			fmt.Fprintf(&b, "statzavod_content_provider_latency_seconds_bucket{platform=%q,le=%q} %g\n", p, fmt.Sprintf("%g", le), n)
		}
		fmt.Fprintf(&b, "statzavod_content_provider_latency_seconds_bucket{platform=%q,le=\"+Inf\"} %g\n", p, v.Count)
		fmt.Fprintf(&b, "statzavod_content_provider_latency_seconds_sum{platform=%q} %g\n", p, v.Sum)
		fmt.Fprintf(&b, "statzavod_content_provider_latency_seconds_count{platform=%q} %g\n", p, v.Count)
	}
	writePlatform("statzavod_content_reauth_connections", "gauge", m.Reauth)
	writePlatform("statzavod_content_reauth_oldest_seconds", "gauge", m.ReauthOldest)
	fmt.Fprintf(&b, "# TYPE statzavod_content_transcoding_failures_total counter\nstatzavod_content_transcoding_failures_total %g\n", m.TranscodingFailures)
	fmt.Fprintf(&b, "# TYPE statzavod_content_orphan_uploads gauge\nstatzavod_content_orphan_uploads %g\n", m.OrphanUploads)
	fmt.Fprintf(&b, "# TYPE statzavod_content_cleanup_errors gauge\nstatzavod_content_cleanup_errors %g\n", m.CleanupErrors)
	fmt.Fprintln(&b, "# TYPE statzavod_content_provider_errors_total counter")
	sort.Slice(m.ProviderErrors, func(i, j int) bool {
		if m.ProviderErrors[i].Platform == m.ProviderErrors[j].Platform {
			return m.ProviderErrors[i].Class < m.ProviderErrors[j].Class
		}
		return m.ProviderErrors[i].Platform < m.ProviderErrors[j].Platform
	})
	for _, v := range m.ProviderErrors {
		fmt.Fprintf(&b, "statzavod_content_provider_errors_total{platform=%q,error_class=%q} %g\n", v.Platform, v.Class, v.Value)
	}
	fmt.Fprintln(&b, "# EOF")
	return b.Bytes()
}
