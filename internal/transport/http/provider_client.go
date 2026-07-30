package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
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

func newProviderClient(platform string) providerClient {
	return providerClient{
		platform: platform,
		client: &http.Client{
			Timeout: 20 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				// Never forward a bearer token to a different host.
				if len(via) > 0 && req.URL.Host != via[0].URL.Host {
					req.Header.Del("Authorization")
				}
				return nil
			},
		},
		retries: 3,
	}
}

func (c providerClient) JSON(ctx context.Context, method, endpoint, bearer, contentType string, body io.Reader, target any) error {
	var lastErr error
	for attempt := 0; attempt < c.retries; attempt++ {
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

		resp, err := c.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = &providerError{Platform: c.platform, Kind: providerRetryable, Message: "network request failed"}
		} else {
			lastErr = c.decodeResponse(resp, target)
			if lastErr == nil {
				return nil
			}
		}

		var apiErr *providerError
		if !errors.As(lastErr, &apiErr) || (apiErr.Kind != providerRateLimit && apiErr.Kind != providerRetryable) || attempt == c.retries-1 {
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
			}
		case map[string]any:
			if text, ok := value["message"].(string); ok && strings.TrimSpace(text) != "" {
				message = text
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
