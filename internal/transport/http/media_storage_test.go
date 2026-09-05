package httpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/statzavod/statzavod/internal/config"
)

func deliveryTestKey() string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
}

func tamperMediaDeliveryToken(t *testing.T, token string) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) == 0 {
		t.Fatalf("decode delivery token for tampering: bytes=%d error=%v", len(raw), err)
	}
	raw[len(raw)-1] ^= 1
	return base64.RawURLEncoding.EncodeToString(raw)
}

func nonCanonicalMediaDeliveryToken(t *testing.T, token string) string {
	t.Helper()
	if remainder := len(token) % 4; remainder != 2 && remainder != 3 {
		t.Fatalf("token length %d has no raw base64url pad bits", len(token))
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alphabet, token[len(token)-1])
	if last < 0 {
		t.Fatalf("invalid final base64url character %q", token[len(token)-1])
	}
	alternate := token[:len(token)-1] + string(alphabet[last^1])
	originalBytes, originalErr := base64.RawURLEncoding.DecodeString(token)
	alternateBytes, alternateErr := base64.RawURLEncoding.DecodeString(alternate)
	if originalErr != nil || alternateErr != nil || !bytes.Equal(originalBytes, alternateBytes) || alternate == token {
		t.Fatalf("failed to construct equivalent noncanonical token: originalErr=%v alternateErr=%v", originalErr, alternateErr)
	}
	return alternate
}

func TestProviderSafeMediaURLIsOpaqueHTTPSAndShortLived(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	s := &Server{config: config.Config{
		MediaPublicBaseURL:    "https://media.statzavod.ru",
		MediaS3Endpoint:       "https://storage.yandexcloud.net",
		MediaS3Bucket:         "private-production-bucket",
		MediaDeliveryTokenKey: deliveryTestKey(),
	}, now: func() time.Time { return now }}
	objectKey := "sha256/immutable-secret-object.mp4"
	delivery, err := s.providerSafeMediaURL(context.Background(), objectKey, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(delivery)
	if err != nil || u.Scheme != "https" || u.Host != "media.statzavod.ru" || u.RawQuery != "" {
		t.Fatalf("unsafe delivery URL %q error=%v", delivery, err)
	}
	for _, forbidden := range []string{"storage.yandexcloud.net", "private-production-bucket", objectKey, "immutable-secret-object", "X-Amz-", "Credential"} {
		if strings.Contains(delivery, forbidden) {
			t.Fatalf("delivery URL leaked %q: %s", forbidden, delivery)
		}
	}
	token := strings.TrimPrefix(u.Path, "/api/v1/media/delivery/")
	capability, err := s.openMediaDeliveryToken(token)
	if err != nil || capability.ObjectKey != objectKey || capability.ExpiresAt != now.Add(30*time.Minute).Unix() {
		t.Fatalf("capability=%+v error=%v", capability, err)
	}
	if _, err = s.providerSafeMediaURL(context.Background(), objectKey, mediaDeliveryMaxLifetime+time.Second); err == nil {
		t.Fatal("overlong capability lifetime was accepted")
	}
	tampered := tamperMediaDeliveryToken(t, token)
	if _, err = s.openMediaDeliveryToken(tampered); err == nil {
		t.Fatal("tampered capability was accepted")
	}
	noncanonical := nonCanonicalMediaDeliveryToken(t, token)
	if _, err = s.openMediaDeliveryToken(noncanonical); err == nil {
		t.Fatal("noncanonical equivalent capability was accepted")
	}
	if _, err = s.openMediaDeliveryToken(token + "="); err == nil {
		t.Fatal("padded capability was accepted")
	}
	if _, err = s.openMediaDeliveryToken(token[:len(token)-1] + "+"); err == nil {
		t.Fatal("capability with the standard base64 alphabet was accepted")
	}
	s.now = func() time.Time { return now.Add(31 * time.Minute) }
	if _, err = s.openMediaDeliveryToken(token); err == nil {
		t.Fatal("expired capability was accepted")
	}
}

func TestProviderSafeMediaURLRejectsUntrustedDeliveryConfiguration(t *testing.T) {
	for _, test := range []struct {
		name, publicBase, storageBase, key string
	}{
		{"http", "http://media.example", "https://storage.example", deliveryTestKey()},
		{"path", "https://media.example/prefix", "https://storage.example", deliveryTestKey()},
		{"credentials", "https://user@media.example", "https://storage.example", deliveryTestKey()},
		{"storage host", "https://storage.example", "https://storage.example", deliveryTestKey()},
		{"short key", "https://media.example", "https://storage.example", base64.StdEncoding.EncodeToString([]byte("short"))},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &Server{config: config.Config{MediaPublicBaseURL: test.publicBase, MediaS3Endpoint: test.storageBase, MediaDeliveryTokenKey: test.key}, now: time.Now}
			if _, err := s.providerSafeMediaURL(context.Background(), "private/key", time.Minute); err == nil {
				t.Fatal("untrusted delivery configuration was accepted")
			}
		})
	}
}

func TestProviderMediaDeliveryStreamsHeadGetAndRangeWithoutRedirect(t *testing.T) {
	objectKey := "sha256/private-object.mp4"
	media := []byte("0123456789")
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/private-bucket/"+objectKey {
			t.Fatalf("unexpected private storage path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "video/mp4")
		if got := r.Header.Get("Range"); got != "" {
			if got != "bytes=2-5" {
				t.Fatalf("unexpected range %q", got)
			}
			w.Header().Set("Content-Range", "bytes 2-5/10")
			w.Header().Set("Content-Length", "4")
			w.WriteHeader(http.StatusPartialContent)
			if r.Method == http.MethodGet {
				_, _ = w.Write(media[2:6])
			}
			return
		}
		w.Header().Set("Content-Length", "10")
		if r.Method == http.MethodGet {
			_, _ = w.Write(media)
		}
	}))
	defer storage.Close()

	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	s := &Server{config: config.Config{
		MediaPublicBaseURL: "https://media.statzavod.ru", MediaDeliveryTokenKey: deliveryTestKey(),
		MediaS3Endpoint: storage.URL, MediaS3Region: "us-east-1", MediaS3Bucket: "private-bucket",
		MediaS3AccessKey: "access", MediaS3SecretKey: "secret",
	}, now: func() time.Time { return now }}
	delivery, err := s.providerSafeMediaURL(context.Background(), objectKey, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	publicURL, _ := url.Parse(delivery)
	app := httptest.NewServer(s.Router())
	defer app.Close()
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return fmt.Errorf("redirect forbidden") }}

	for _, test := range []struct {
		method, rangeHeader string
		wantStatus          int
		wantBody            string
	}{
		{http.MethodHead, "", http.StatusOK, ""},
		{http.MethodGet, "", http.StatusOK, string(media)},
		{http.MethodHead, "bytes=2-5", http.StatusPartialContent, ""},
		{http.MethodGet, "bytes=2-5", http.StatusPartialContent, "2345"},
	} {
		req, _ := http.NewRequest(test.method, app.URL+publicURL.Path, nil)
		req.Host = publicURL.Host
		if test.rangeHeader != "" {
			req.Header.Set("Range", test.rangeHeader)
		}
		response, requestErr := client.Do(req)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != test.wantStatus || string(body) != test.wantBody {
			t.Fatalf("%s range=%q status=%d body=%q", test.method, test.rangeHeader, response.StatusCode, body)
		}
		if response.Header.Get("Location") != "" || response.Header.Get("Accept-Ranges") != "bytes" || response.Header.Get("Content-Type") != "video/mp4" {
			t.Fatalf("unsafe/incomplete delivery headers: %#v", response.Header)
		}
	}

	invalidReq, _ := http.NewRequest(http.MethodGet, app.URL+publicURL.Path, nil)
	invalidReq.Host = publicURL.Host
	invalidReq.Header.Set("Range", "bytes=1-2,4-5")
	invalidResponse, err := client.Do(invalidReq)
	if err != nil {
		t.Fatal(err)
	}
	invalidResponse.Body.Close()
	if invalidResponse.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("invalid range status=%d", invalidResponse.StatusCode)
	}

	token := strings.TrimPrefix(publicURL.Path, "/api/v1/media/delivery/")
	tamperedPath := "/api/v1/media/delivery/" + tamperMediaDeliveryToken(t, token)
	tamperedRequest, _ := http.NewRequest(http.MethodGet, app.URL+tamperedPath, nil)
	tamperedRequest.Host = publicURL.Host
	tamperedResponse, err := client.Do(tamperedRequest)
	if err != nil {
		t.Fatal(err)
	}
	tamperedResponse.Body.Close()
	if tamperedResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("tampered token status=%d", tamperedResponse.StatusCode)
	}

	wrongHostResponse, err := client.Get(app.URL + publicURL.Path)
	if err != nil {
		t.Fatal(err)
	}
	wrongHostResponse.Body.Close()
	if wrongHostResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("delivery capability accepted on a non-allowlisted host: %d", wrongHostResponse.StatusCode)
	}
}

func TestMediaUploadPartContract(t *testing.T) {
	if got := mediaUploadPartCount(mediaUploadPartSize + 3); got != 2 {
		t.Fatalf("part count=%d", got)
	}
	if got := mediaUploadPartBytes(mediaUploadPartSize+3, 2); got != 3 {
		t.Fatalf("last part bytes=%d", got)
	}
	if !mediaUploadQuotaExceeded(0, 0, mediaUploadMaxActorSessions, 0, 1) {
		t.Fatal("repeated actor sessions must hit quota")
	}
	if !mediaUploadQuotaExceeded(mediaUploadMaxOrgSessions, 0, 0, 0, 1) {
		t.Fatal("repeated organization sessions must hit quota")
	}
	if !mediaUploadQuotaExceeded(0, mediaUploadMaxOrgBytes, 0, 0, 1) {
		t.Fatal("declared organization bytes must hit quota")
	}
}

func TestValidateUploadedMediaPartsUsesPersistedS3Sizes(t *testing.T) {
	tests := []struct {
		name, xml string
		expected  int64
		requested []types.CompletedPart
		want      string
	}{
		{
			name: "valid exact sizes", expected: mediaUploadPartSize + 3,
			xml:       listPartsXML([]partFixture{{1, "one", mediaUploadPartSize}, {2, "two", 3}}),
			requested: []types.CompletedPart{{PartNumber: i32(1), ETag: str("one")}, {PartNumber: i32(2), ETag: str("two")}},
		},
		{
			name: "oversized first part", expected: mediaUploadPartSize + 3,
			xml:       listPartsXML([]partFixture{{1, "one", mediaUploadPartSize + 1}, {2, "two", 2}}),
			requested: []types.CompletedPart{{PartNumber: i32(1), ETag: str("one")}, {PartNumber: i32(2), ETag: str("two")}}, want: "has size",
		},
		{
			name: "wrong final size", expected: mediaUploadPartSize + 3,
			xml:       listPartsXML([]partFixture{{1, "one", mediaUploadPartSize}, {2, "two", 4}}),
			requested: []types.CompletedPart{{PartNumber: i32(1), ETag: str("one")}, {PartNumber: i32(2), ETag: str("two")}}, want: "has size",
		},
		{
			name: "out of range", expected: 10,
			xml:       listPartsXML([]partFixture{{2, "two", 10}}),
			requested: []types.CompletedPart{{PartNumber: i32(2), ETag: str("two")}}, want: "outside",
		},
		{
			name: "etag mismatch", expected: 10,
			xml:       listPartsXML([]partFixture{{1, "stored", 10}}),
			requested: []types.CompletedPart{{PartNumber: i32(1), ETag: str("client")}}, want: "ETag",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Query().Get("uploadId") != "upload" {
					t.Fatalf("unexpected S3 request %s %s", r.Method, r.URL.String())
				}
				w.Header().Set("Content-Type", "application/xml")
				_, _ = w.Write([]byte(tt.xml))
			}))
			defer fake.Close()
			s := &Server{config: config.Config{MediaS3Endpoint: fake.URL, MediaS3Region: "us-east-1", MediaS3Bucket: "bucket", MediaS3AccessKey: "key", MediaS3SecretKey: "secret"}}
			storage, err := s.mediaStorage(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			err = validateUploadedMediaParts(context.Background(), storage, "object", "upload", tt.expected, tt.requested)
			if tt.want == "" && err != nil {
				t.Fatal(err)
			}
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("error=%v want %q", err, tt.want)
			}
		})
	}
}

type partFixture struct {
	number int32
	etag   string
	size   int64
}

func listPartsXML(parts []partFixture) string {
	var b strings.Builder
	b.WriteString(`<ListPartsResult><IsTruncated>false</IsTruncated>`)
	for _, part := range parts {
		fmt.Fprintf(&b, `<Part><PartNumber>%d</PartNumber><ETag>%s</ETag><Size>%d</Size></Part>`, part.number, part.etag, part.size)
	}
	b.WriteString(`</ListPartsResult>`)
	return b.String()
}

func i32(v int32) *int32   { return &v }
func str(v string) *string { return &v }
