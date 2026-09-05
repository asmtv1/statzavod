package httpserver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type countingBody struct {
	reader io.Reader
	read   int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.read += int64(n)
	return n, err
}
func (*countingBody) Close() error { return nil }

func TestCompleteMediaUploadRejectsOversizeBeforeDatabaseAndBoundsRead(t *testing.T) {
	input := bytes.Repeat([]byte("x"), int(contentCommandBodyLimit)+4096)
	body := &countingBody{reader: bytes.NewReader(input)}
	request := httptest.NewRequest(http.MethodPost, "/creator-portal/media/uploads/upload/complete", nil)
	request.Body = body
	request = request.WithContext(context.WithValue(request.Context(), principalKey, principal{}))
	response := httptest.NewRecorder()
	new(Server).completeMediaUpload(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if body.read > contentCommandBodyLimit+1 {
		t.Fatalf("oversize body read=%d limit=%d", body.read, contentCommandBodyLimit+1)
	}
}

func TestMediaCreateIdempotencyUsesEndpointLimitBeforeDatabase(t *testing.T) {
	input := bytes.Repeat([]byte("x"), int(mediaCreateBodyLimit)+4096)
	body := &countingBody{reader: bytes.NewReader(input)}
	request := httptest.NewRequest(http.MethodPost, "/creator-portal/media/uploads", nil)
	request.Body = body
	request.Header.Set("Idempotency-Key", "oversized-media-create")
	response := httptest.NewRecorder()
	if finish, ok := beginContentCommandIdempotency(response, request, nil, principal{}); ok || finish != nil {
		t.Fatal("oversized request unexpectedly reached the idempotency database boundary")
	}
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if body.read > mediaCreateBodyLimit+1 {
		t.Fatalf("oversize body read=%d limit=%d", body.read, mediaCreateBodyLimit+1)
	}
}

func TestApprovalPolicyRejectsOversizeBeforeAuthorizationDatabaseAndBoundsRead(t *testing.T) {
	input := bytes.Repeat([]byte("x"), int(mediaCreateBodyLimit)+4096)
	body := &countingBody{reader: bytes.NewReader(input)}
	request := httptest.NewRequest(http.MethodPut, "/creators/creator-id/content-approval-policy", nil)
	request.Body = body
	response := httptest.NewRecorder()
	new(Server).putContentApprovalPolicy(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if body.read > mediaCreateBodyLimit+1 {
		t.Fatalf("oversize body read=%d limit=%d", body.read, mediaCreateBodyLimit+1)
	}
}

func TestReadAndRestoreBoundedStopsAtLimitPlusOne(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/content-items/id/retry", nil)
	body := &countingBody{reader: bytes.NewReader(bytes.Repeat([]byte("a"), 4096))}
	request.Body = body
	_, err := ioReadAndRestoreBounded(request, 128)
	if !errors.Is(err, errContentCommandBodyTooLarge) || body.read != 129 {
		t.Fatalf("err=%v read=%d", err, body.read)
	}
}
