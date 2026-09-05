package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type providerErrorKind string

const (
	providerAuth       providerErrorKind = "AUTH"
	providerPermission providerErrorKind = "PERMISSION"
	providerRateLimit  providerErrorKind = "RATE_LIMIT"
	providerRetryable  providerErrorKind = "RETRYABLE"
	providerSchema     providerErrorKind = "SCHEMA"
	providerPermanent  providerErrorKind = "PERMANENT"
)

type providerError struct {
	Platform   string
	Kind       providerErrorKind
	StatusCode int
	RetryAfter time.Duration
	Message    string
}

func (e *providerError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s API: %s", e.Platform, e.Message)
	}
	return fmt.Sprintf("%s API returned HTTP %d", e.Platform, e.StatusCode)
}

func isProviderKind(err error, kinds ...providerErrorKind) bool {
	var target *providerError
	if !errors.As(err, &target) {
		return false
	}
	for _, kind := range kinds {
		if target.Kind == kind {
			return true
		}
	}
	return false
}

type providerClient struct {
	platform string
	client   *http.Client
	retries  int
}

// providerRequestMode makes retry semantics explicit at every provider call.
// Create-like operations must use providerOneShot: a timeout, 429 or 5xx can
// mean the provider accepted the request, so replaying it could create a
// duplicate. Read/status operations may use providerSafeRetry.
type providerRequestMode uint8

const (
	providerSafeRetry providerRequestMode = iota
	providerOneShot
)

var errProviderRedirect = errors.New("provider redirect rejected")

func newProviderClient(platform string) providerClient {
	return providerClient{
		platform: platform,
		client: &http.Client{
			Timeout:       20 * time.Second,
			CheckRedirect: rejectProviderRedirect,
		},
		retries: 3,
	}
}

func (c providerClient) JSON(ctx context.Context, method, endpoint, bearer, contentType string, body io.Reader, target any) error {
	return c.JSONWithMode(ctx, method, endpoint, bearer, contentType, body, target, providerSafeRetry)
}

func (c providerClient) JSONWithMode(ctx context.Context, method, endpoint, bearer, contentType string, body io.Reader, target any, mode providerRequestMode) error {
	var lastErr error
	httpClient := c.client
	if mode == providerOneShot {
		clone := *c.client
		clone.CheckRedirect = rejectProviderRedirect
		httpClient = &clone
	}
	attempts := c.retries
	if mode == providerOneShot {
		attempts = 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		var requestBody io.Reader
		if body != nil {
			readCloser, ok := body.(io.ReadSeeker)
			if !ok {
				if attempt > 0 {
					return lastErr
				}
				requestBody = body
			} else {
				_, _ = readCloser.Seek(0, io.SeekStart)
				requestBody = readCloser
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint, requestBody)
		if err != nil {
			return err
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		req.Header.Set("Accept", "application/json")

		resp, err := httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, errProviderRedirect) {
				return &providerError{Platform: c.platform, Kind: providerPermanent, Message: errProviderRedirect.Error()}
			}
			lastErr = &providerError{Platform: c.platform, Kind: providerRetryable, Message: "network request failed"}
		} else {
			lastErr = c.decodeResponse(resp, target)
			if lastErr == nil {
				return nil
			}
		}

		var apiErr *providerError
		if mode == providerOneShot && errors.As(lastErr, &apiErr) && (apiErr.Kind == providerRateLimit || apiErr.Kind == providerRetryable) {
			return &providerError{Platform: c.platform, Kind: providerPermanent, StatusCode: apiErr.StatusCode, Message: "provider outcome is uncertain; manual reconciliation is required"}
		}
		if !errors.As(lastErr, &apiErr) || (apiErr.Kind != providerRateLimit && apiErr.Kind != providerRetryable) || attempt == attempts-1 {
			return lastErr
		}
		delay := apiErr.RetryAfter
		if delay <= 0 {
			maxDelay := 250 * time.Millisecond * time.Duration(1<<attempt)
			delay = time.Duration(rand.Int64N(int64(maxDelay) + 1))
		}
		if delay > 5*time.Second {
			delay = 5 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

// validateProviderURL is a fail-closed allowlist for capability/upload URLs.
// Entries beginning with a dot allow that DNS suffix and its apex.
func validateProviderURL(value string, allowedHosts ...string) error {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return fmt.Errorf("provider URL must be HTTPS without credentials or a non-standard port")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("provider URL host is not allowed")
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()) {
		return fmt.Errorf("provider URL address is not allowed")
	}
	allowed := false
	for _, entry := range allowedHosts {
		entry = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(entry), "."))
		if strings.HasPrefix(entry, ".") {
			apex := strings.TrimPrefix(entry, ".")
			allowed = host == apex || strings.HasSuffix(host, entry)
		} else {
			allowed = host == entry
		}
		if allowed {
			break
		}
	}
	if !allowed {
		return fmt.Errorf("provider URL host is not allowed")
	}
	return nil
}

func newValidatedProviderHTTPClient(_ func(string) error) *http.Client {
	return &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: rejectProviderRedirect,
	}
}

func rejectProviderRedirect(*http.Request, []*http.Request) error {
	return errProviderRedirect
}

func (c providerClient) decodeResponse(resp *http.Response, target any) error {
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if resp.StatusCode == http.StatusNoContent || target == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			return nil
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(target); err != nil && err != io.EOF {
			return &providerError{Platform: c.platform, Kind: providerSchema, StatusCode: resp.StatusCode, Message: "unexpected response format"}
		}
		return nil
	}

	kind := providerPermanent
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		kind = providerAuth
	case resp.StatusCode == http.StatusForbidden:
		kind = providerPermission
	case resp.StatusCode == http.StatusTooManyRequests:
		kind = providerRateLimit
	case resp.StatusCode >= 500:
		kind = providerRetryable
	}
	retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
	message := http.StatusText(resp.StatusCode)
	var payload struct {
		Error any `json:"error"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&payload) == nil && payload.Error != nil {
		switch value := payload.Error.(type) {
		case string:
			if strings.TrimSpace(value) != "" {
				message = value
				// OAuth token endpoints commonly report a revoked or invalid
				// refresh token as HTTP 400 + invalid_grant rather than 401.
				if resp.StatusCode == http.StatusBadRequest && strings.EqualFold(strings.TrimSpace(value), "invalid_grant") {
					kind = providerAuth
				}
			}
		case map[string]any:
			if text, ok := value["message"].(string); ok && strings.TrimSpace(text) != "" {
				message = text
			}
			// Meta Graph reports invalid/revoked OAuth tokens as HTTP 400 with
			// error code 190 instead of an HTTP authorization status.
			if code, ok := value["code"].(float64); ok && int(code) == 190 {
				kind = providerAuth
			}
		}
	}
	return &providerError{Platform: c.platform, Kind: kind, StatusCode: resp.StatusCode, RetryAfter: retryAfter, Message: message}
}

func parseRetryAfter(value string) time.Duration {
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return 0
}
