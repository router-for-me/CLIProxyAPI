package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func antigravityCompressionPayload(repeats int) []byte {
	return []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"` +
		strings.Repeat("compressible payload ", repeats) + `"}]}],"generationConfig":{"maxOutputTokens":32}}}`)
}

func buildAntigravityCompressionRequest(t *testing.T, cfg *config.Config, payload []byte) *http.Request {
	t.Helper()

	executor := NewAntigravityExecutor(cfg)
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "project-1"}}
	req, errBuild := executor.buildRequest(context.Background(), auth, "token", "gemini-3-flash", payload, false, "", "https://example.com")
	if errBuild != nil {
		t.Fatalf("buildRequest error: %v", errBuild)
	}
	return req
}

func TestAntigravityBuildRequest_CompressesLargePayload(t *testing.T) {
	cfg := &config.Config{}
	cfg.Antigravity.UpstreamRequestCompression = true

	req := buildAntigravityCompressionRequest(t, cfg, antigravityCompressionPayload(512))
	if got := req.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}

	reader, errReader := gzip.NewReader(req.Body)
	if errReader != nil {
		t.Fatalf("new gzip reader error: %v", errReader)
	}
	defer func() {
		if errClose := reader.Close(); errClose != nil {
			t.Errorf("close gzip reader error: %v", errClose)
		}
	}()
	decoded, errRead := io.ReadAll(reader)
	if errRead != nil {
		t.Fatalf("decompress request body error: %v", errRead)
	}
	if !bytes.Contains(decoded, []byte("compressible payload")) {
		t.Fatalf("decompressed body lost payload content: %s", decoded)
	}
}

func TestAntigravityBuildRequest_SkipsCompressionForSmallPayload(t *testing.T) {
	cfg := &config.Config{}
	cfg.Antigravity.UpstreamRequestCompression = true

	req := buildAntigravityCompressionRequest(t, cfg, antigravityCompressionPayload(1))
	if got := req.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty for small payload", got)
	}
}

func TestAntigravityBuildRequest_SkipsCompressionWhenDisabled(t *testing.T) {
	cfg := &config.Config{}

	req := buildAntigravityCompressionRequest(t, cfg, antigravityCompressionPayload(512))
	if got := req.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty when compression is disabled", got)
	}
}

func TestAntigravityBuildRequest_RespectsCompressionMinBytes(t *testing.T) {
	cfg := &config.Config{}
	cfg.Antigravity.UpstreamRequestCompression = true
	cfg.Antigravity.UpstreamRequestCompressionMinBytes = 1 << 20

	req := buildAntigravityCompressionRequest(t, cfg, antigravityCompressionPayload(512))
	if got := req.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty below min-bytes", got)
	}
}

func TestDoAntigravityRequest_RetriesPlainBodyOnRejection(t *testing.T) {
	plain := antigravityCompressionPayload(512)
	cfg := &config.Config{}
	cfg.Antigravity.UpstreamRequestCompression = true
	executor := NewAntigravityExecutor(cfg)
	bodyReader, compressed := executor.encodeAntigravityRequestBody(plain)
	if !compressed {
		t.Fatal("test payload did not compress")
	}
	compressedBody, errRead := io.ReadAll(bodyReader)
	if errRead != nil {
		t.Fatalf("read compressed body error: %v", errRead)
	}

	for _, status := range []int{http.StatusBadRequest, http.StatusUnsupportedMediaType} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var attempts atomic.Int32
			var firstEncoding, secondEncoding string
			var secondBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errBody := io.ReadAll(r.Body)
				if errBody != nil {
					t.Errorf("read server request body error: %v", errBody)
				}
				if attempts.Add(1) == 1 {
					firstEncoding = r.Header.Get("Content-Encoding")
					w.WriteHeader(status)
					return
				}
				secondEncoding = r.Header.Get("Content-Encoding")
				secondBody = body
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer server.Close()

			req, errRequest := http.NewRequest(http.MethodPost, server.URL, bytes.NewReader(compressedBody))
			if errRequest != nil {
				t.Fatalf("new request error: %v", errRequest)
			}
			req.Header.Set("Content-Encoding", "gzip")
			resp, errDo := doAntigravityRequest(server.Client(), nil, "test", req, plain)
			if errDo != nil {
				t.Fatalf("doAntigravityRequest error: %v", errDo)
			}
			defer func() {
				if errClose := resp.Body.Close(); errClose != nil {
					t.Errorf("close response body error: %v", errClose)
				}
			}()

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if got := attempts.Load(); got != 2 {
				t.Fatalf("attempts = %d, want 2", got)
			}
			if firstEncoding != "gzip" {
				t.Fatalf("first Content-Encoding = %q, want gzip", firstEncoding)
			}
			if secondEncoding != "" {
				t.Fatalf("second Content-Encoding = %q, want empty", secondEncoding)
			}
			if !bytes.Equal(secondBody, plain) {
				t.Fatalf("retried body differs from plain payload")
			}
		})
	}
}

func TestDoAntigravityRequest_DoesNotRetryOtherStatusCodes(t *testing.T) {
	plain := antigravityCompressionPayload(512)
	cfg := &config.Config{}
	cfg.Antigravity.UpstreamRequestCompression = true
	executor := NewAntigravityExecutor(cfg)
	bodyReader, compressed := executor.encodeAntigravityRequestBody(plain)
	if !compressed {
		t.Fatal("test payload did not compress")
	}
	compressedBody, errRead := io.ReadAll(bodyReader)
	if errRead != nil {
		t.Fatalf("read compressed body error: %v", errRead)
	}

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	req, errRequest := http.NewRequest(http.MethodPost, server.URL, bytes.NewReader(compressedBody))
	if errRequest != nil {
		t.Fatalf("new request error: %v", errRequest)
	}
	req.Header.Set("Content-Encoding", "gzip")
	resp, errDo := doAntigravityRequest(server.Client(), nil, "test", req, plain)
	if errDo != nil {
		t.Fatalf("doAntigravityRequest error: %v", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			t.Errorf("close response body error: %v", errClose)
		}
	}()
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}
