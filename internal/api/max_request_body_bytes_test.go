package api

import (
	"bytes"
	"context"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/klauspost/compress/zstd"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

type countingBodyReader struct {
	reader io.Reader
	read   atomic.Int64
}

func (r *countingBodyReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.read.Add(int64(n))
	}
	return n, err
}

type bodyReadingAccessProvider struct {
	readBytes atomic.Int64
}

func (p *bodyReadingAccessProvider) Identifier() string {
	return "plugin:test:body-auth"
}

func (p *bodyReadingAccessProvider) Authenticate(_ context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if r == nil || r.Body == nil {
		return nil, sdkaccess.NewNotHandledError()
	}
	body, errRead := io.ReadAll(r.Body)
	p.readBytes.Store(int64(len(body)))
	if errRead != nil {
		return nil, sdkaccess.NewRequestEntityTooLargeError(errRead)
	}
	return nil, sdkaccess.NewNotHandledError()
}

func parseTestServerConfigYAML(t *testing.T, raw string) *proxyconfig.Config {
	t.Helper()
	cfg, errParse := proxyconfig.ParseConfigBytes([]byte(raw))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	return cfg
}

func TestMaxRequestBodyBytesConfigV8Validation(t *testing.T) {
	raw := "config-version: 8\nserver:\n  max-request-body-bytes: 64\n"
	if errValidate := proxyconfig.ValidateV8Config([]byte(raw)); errValidate != nil {
		t.Fatalf("ValidateV8Config() error = %v, want nil", errValidate)
	}

	cfg := parseTestServerConfigYAML(t, raw)
	if cfg.MaxRequestBodyBytes != 64 {
		t.Fatalf("cfg.MaxRequestBodyBytes = %d, want 64", cfg.MaxRequestBodyBytes)
	}

	legacyCfg := parseTestServerConfigYAML(t, "max-request-body-bytes: 128\n")
	if legacyCfg.MaxRequestBodyBytes != 128 {
		t.Fatalf("legacyCfg.MaxRequestBodyBytes = %d, want 128", legacyCfg.MaxRequestBodyBytes)
	}

	precedenceCfg := parseTestServerConfigYAML(t, "max-request-body-bytes: 128\nserver:\n  max-request-body-bytes: 0\n")
	if precedenceCfg.MaxRequestBodyBytes != 0 {
		t.Fatalf("precedenceCfg.MaxRequestBodyBytes = %d, want 0", precedenceCfg.MaxRequestBodyBytes)
	}
}

func TestServerMaxRequestBodyBytesRejectsContentLengthAndChunkedBodies(t *testing.T) {
	cfg := parseTestServerConfigYAML(t, `config-version: 8
server:
  max-request-body-bytes: 64
access:
  api-keys:
    - test-key
observability:
  logs:
    request-log: true
`)
	server := newTestServerWithConfig(t, cfg)

	t.Run("content-length exceeding limit returns 413 without reading body", func(t *testing.T) {
		counter := &countingBodyReader{reader: strings.NewReader(strings.Repeat("a", 128))}
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", counter)
		req.ContentLength = 128
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		server.engine.ServeHTTP(rr, req)

		if rr.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusRequestEntityTooLarge, rr.Body.String())
		}
		if got := counter.read.Load(); got != 0 {
			t.Fatalf("bytes read = %d, want 0", got)
		}
	})

	t.Run("unauthenticated chunked body with request-log enabled is bounded and returns 413", func(t *testing.T) {
		counter := &countingBodyReader{reader: strings.NewReader(strings.Repeat("b", 1024))}
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", io.NopCloser(counter))
		req.ContentLength = -1
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		server.engine.ServeHTTP(rr, req)

		if rr.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusRequestEntityTooLarge, rr.Body.String())
		}
		if got := counter.read.Load(); got > 65 {
			t.Fatalf("bytes read before rejection = %d, want <= 65", got)
		}
	})

	for _, endpoint := range []string{
		"/v1/chat/completions",
		"/v1/responses",
		"/v1/messages",
		"/v1beta/models/gemini-2.5-pro:generateContent",
		"/v1beta/interactions",
	} {
		t.Run("authenticated chunked body exceeding limit returns 413 on "+endpoint, func(t *testing.T) {
			cfgNoLog := parseTestServerConfigYAML(t, `config-version: 8
server:
  max-request-body-bytes: 64
access:
  api-keys:
    - test-key
`)
			srv := newTestServerWithConfig(t, cfgNoLog)
			payload := `{"model":"gpt-5","messages":[{"role":"user","content":"` + strings.Repeat("x", 128) + `"}]}`
			req := httptest.NewRequest(http.MethodPost, endpoint, io.NopCloser(strings.NewReader(payload)))
			req.ContentLength = -1
			req.Header.Set("Authorization", "Bearer test-key")
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()

			srv.engine.ServeHTTP(rr, req)

			if rr.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("%s status = %d, want %d; body=%s", endpoint, rr.Code, http.StatusRequestEntityTooLarge, rr.Body.String())
			}
		})
	}

	t.Run("zstd decompressed body exceeding limit returns 413", func(t *testing.T) {
		cfgZstd := parseTestServerConfigYAML(t, `config-version: 8
server:
  max-request-body-bytes: 128
access:
  api-keys:
    - test-key
`)
		srv := newTestServerWithConfig(t, cfgZstd)

		uncompressed := []byte(`{"model":"gpt-5","input":"` + strings.Repeat("z", 512) + `"}`)
		var compressed bytes.Buffer
		encoder, errEncoder := zstd.NewWriter(&compressed)
		if errEncoder != nil {
			t.Fatalf("zstd.NewWriter() error = %v", errEncoder)
		}
		if _, errWrite := encoder.Write(uncompressed); errWrite != nil {
			t.Fatalf("encoder.Write() error = %v", errWrite)
		}
		if errClose := encoder.Close(); errClose != nil {
			t.Fatalf("encoder.Close() error = %v", errClose)
		}
		if compressed.Len() >= 128 {
			t.Fatalf("compressed size = %d, want < 128", compressed.Len())
		}

		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(compressed.Bytes()))
		req.Header.Set("Authorization", "Bearer test-key")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Content-Encoding", "zstd")
		rr := httptest.NewRecorder()

		srv.engine.ServeHTTP(rr, req)

		if rr.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusRequestEntityTooLarge, rr.Body.String())
		}
	})

	t.Run("unauthenticated chunked body with plugin auth fallback is bounded and returns 413", func(t *testing.T) {
		cfgPluginAuth := parseTestServerConfigYAML(t, `config-version: 8
server:
  max-request-body-bytes: 64
access:
  api-keys:
    - test-key
`)
		srv := newTestServerWithConfig(t, cfgPluginAuth)
		fallbackProvider := &bodyReadingAccessProvider{}
		providers := append(srv.accessManager.Providers(), fallbackProvider)
		srv.accessManager.SetProviders(providers)

		counter := &countingBodyReader{reader: strings.NewReader(strings.Repeat("p", 1024))}
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", io.NopCloser(counter))
		req.ContentLength = -1
		req.Header.Set("Authorization", "Bearer wrong-key")
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		srv.engine.ServeHTTP(rr, req)

		if rr.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusRequestEntityTooLarge, rr.Body.String())
		}
		if got := counter.read.Load(); got > 65 {
			t.Fatalf("underlying bytes read = %d, want <= 65", got)
		}
		if got := fallbackProvider.readBytes.Load(); got > 65 {
			t.Fatalf("fallback provider readBytes = %d, want <= 65", got)
		}
	})

	t.Run("default off and hot reload update limit", func(t *testing.T) {
		cfgDefault := parseTestServerConfigYAML(t, `config-version: 8
access:
  api-keys:
    - test-key
`)
		srv := newTestServerWithConfig(t, cfgDefault)

		payload := `{"model":"gpt-5","messages":[{"role":"user","content":"` + strings.Repeat("d", 128) + `"}]}`
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer test-key")
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "unknown provider for model gpt-5") {
			t.Fatalf("default-off status = %d body = %s, want 400 model_not_found", rr.Code, rr.Body.String())
		}

		cfgReloaded := parseTestServerConfigYAML(t, `config-version: 8
server:
  max-request-body-bytes: 32
access:
  api-keys:
    - test-key
`)
		srv.UpdateClients(cfgReloaded)

		reqAfter := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
		reqAfter.Header.Set("Authorization", "Bearer test-key")
		reqAfter.Header.Set("Content-Type", "application/json")
		rrAfter := httptest.NewRecorder()
		srv.engine.ServeHTTP(rrAfter, reqAfter)
		if rrAfter.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("after hot reload status = %d, want %d; body=%s", rrAfter.Code, http.StatusRequestEntityTooLarge, rrAfter.Body.String())
		}
	})

	t.Run("exact boundary body is accepted", func(t *testing.T) {
		exactPayload := `{"model":"gpt-5","input":"ok"}`
		cfgExact := parseTestServerConfigYAML(t, "config-version: 8\nserver:\n  max-request-body-bytes: 30\naccess:\n  api-keys:\n    - test-key\n")
		srvExact := newTestServerWithConfig(t, cfgExact)
		if len(exactPayload) != 30 {
			t.Fatalf("len(exactPayload) = %d, want 30", len(exactPayload))
		}
		reqExact := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(exactPayload))
		reqExact.Header.Set("Authorization", "Bearer test-key")
		reqExact.Header.Set("Content-Type", "application/json")
		rrExact := httptest.NewRecorder()
		srvExact.engine.ServeHTTP(rrExact, reqExact)
		if rrExact.Code != http.StatusBadRequest || !strings.Contains(rrExact.Body.String(), "unknown provider for model gpt-5") {
			t.Fatalf("exact boundary status = %d body = %s, want 400 model_not_found", rrExact.Code, rrExact.Body.String())
		}
	})

	t.Run("negative limit disables request body cap", func(t *testing.T) {
		cfgNegative := parseTestServerConfigYAML(t, "config-version: 8\nserver:\n  max-request-body-bytes: -1\naccess:\n  api-keys:\n    - test-key\n")
		srvNegative := newTestServerWithConfig(t, cfgNegative)
		payload := `{"model":"gpt-5","input":"` + strings.Repeat("a", 256) + `"}`
		reqNeg := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(payload))
		reqNeg.Header.Set("Authorization", "Bearer test-key")
		reqNeg.Header.Set("Content-Type", "application/json")
		rrNeg := httptest.NewRecorder()
		srvNegative.engine.ServeHTTP(rrNeg, reqNeg)
		if rrNeg.Code != http.StatusBadRequest || !strings.Contains(rrNeg.Body.String(), "unknown provider for model gpt-5") {
			t.Fatalf("negative limit status = %d body = %s, want 400 model_not_found", rrNeg.Code, rrNeg.Body.String())
		}
	})

	t.Run("multipart chunked body exceeding limit returns 413", func(t *testing.T) {
		cfgMultipart := parseTestServerConfigYAML(t, "config-version: 8\nserver:\n  max-request-body-bytes: 30\naccess:\n  api-keys:\n    - test-key\n")
		srvMultipart := newTestServerWithConfig(t, cfgMultipart)
		var multipartBuf bytes.Buffer
		mw := multipart.NewWriter(&multipartBuf)
		if errField := mw.WriteField("model", "gpt-image-1"); errField != nil {
			t.Fatalf("WriteField(model) error = %v", errField)
		}
		if errField := mw.WriteField("prompt", strings.Repeat("p", 128)); errField != nil {
			t.Fatalf("WriteField(prompt) error = %v", errField)
		}
		if errClose := mw.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
		reqMultipart := httptest.NewRequest(http.MethodPost, "/v1/images/edits", io.NopCloser(bytes.NewReader(multipartBuf.Bytes())))
		reqMultipart.ContentLength = -1
		reqMultipart.Header.Set("Authorization", "Bearer test-key")
		reqMultipart.Header.Set("Content-Type", mw.FormDataContentType())
		rrMultipart := httptest.NewRecorder()
		srvMultipart.engine.ServeHTTP(rrMultipart, reqMultipart)
		if rrMultipart.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("multipart oversized status = %d, want %d; body=%s", rrMultipart.Code, http.StatusRequestEntityTooLarge, rrMultipart.Body.String())
		}
	})

	t.Run("zstd decompressed body exceeding limit with request-log enabled returns 413", func(t *testing.T) {
		cfgZstdWithLog := parseTestServerConfigYAML(t, "config-version: 8\nserver:\n  max-request-body-bytes: 128\naccess:\n  api-keys:\n    - test-key\nobservability:\n  logs:\n    request-log: true\n")
		srvZstdLog := newTestServerWithConfig(t, cfgZstdWithLog)
		uncompressed := []byte(`{"model":"gpt-5","input":"` + strings.Repeat("z", 512) + `"}`)
		var compressed bytes.Buffer
		encoder, errEncoder := zstd.NewWriter(&compressed)
		if errEncoder != nil {
			t.Fatalf("zstd.NewWriter() error = %v", errEncoder)
		}
		if _, errWrite := encoder.Write(uncompressed); errWrite != nil {
			t.Fatalf("encoder.Write() error = %v", errWrite)
		}
		if errClose := encoder.Close(); errClose != nil {
			t.Fatalf("encoder.Close() error = %v", errClose)
		}
		reqZstdLog := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(compressed.Bytes()))
		reqZstdLog.Header.Set("Authorization", "Bearer test-key")
		reqZstdLog.Header.Set("Content-Type", "application/json")
		reqZstdLog.Header.Set("Content-Encoding", "zstd")
		rrZstdLog := httptest.NewRecorder()
		srvZstdLog.engine.ServeHTTP(rrZstdLog, reqZstdLog)
		if rrZstdLog.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("zstd with request-log status = %d, want %d; body=%s", rrZstdLog.Code, http.StatusRequestEntityTooLarge, rrZstdLog.Body.String())
		}
	})

	t.Run("MaxInt64 limit does not overflow zstd LimitReader", func(t *testing.T) {
		exactPayload := `{"model":"gpt-5","input":"ok"}`
		cfgMaxInt64 := parseTestServerConfigYAML(t, "config-version: 8\naccess:\n  api-keys:\n    - test-key\nobservability:\n  logs:\n    request-log: true\n")
		cfgMaxInt64.MaxRequestBodyBytes = math.MaxInt64
		srvMaxInt64 := newTestServerWithConfig(t, cfgMaxInt64)
		var smallZstd bytes.Buffer
		smallEnc, errEncoder := zstd.NewWriter(&smallZstd)
		if errEncoder != nil {
			t.Fatalf("zstd.NewWriter() error = %v", errEncoder)
		}
		if _, errWrite := smallEnc.Write([]byte(exactPayload)); errWrite != nil {
			t.Fatalf("smallEnc.Write() error = %v", errWrite)
		}
		if errClose := smallEnc.Close(); errClose != nil {
			t.Fatalf("smallEnc.Close() error = %v", errClose)
		}
		reqMax := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(smallZstd.Bytes()))
		reqMax.Header.Set("Authorization", "Bearer test-key")
		reqMax.Header.Set("Content-Type", "application/json")
		reqMax.Header.Set("Content-Encoding", "zstd")
		rrMax := httptest.NewRecorder()
		srvMaxInt64.engine.ServeHTTP(rrMax, reqMax)
		if rrMax.Code != http.StatusBadRequest || !strings.Contains(rrMax.Body.String(), "unknown provider for model gpt-5") {
			t.Fatalf("MaxInt64 zstd status = %d body = %s, want 400 model_not_found", rrMax.Code, rrMax.Body.String())
		}
	})

	t.Run("v8 management chunked body exceeding limit returns 413", func(t *testing.T) {
		rawCfg := "config-version: 8\nserver:\n  max-request-body-bytes: 32\nmanagement:\n  allow-remote: true\n  secret-key: mgmt-secret\n"
		cfgMgmt := parseTestServerConfigYAML(t, rawCfg)
		srvMgmt := newTestServerWithConfig(t, cfgMgmt)
		if errWrite := os.WriteFile(srvMgmt.configFilePath, []byte(rawCfg), 0o600); errWrite != nil {
			t.Fatalf("WriteFile(configFilePath) error = %v", errWrite)
		}

		for _, endpoint := range []struct {
			method string
			path   string
		}{
			{http.MethodPost, "/v8/management/plugins/store/demo/install"},
			{http.MethodPut, "/v8/management/config"},
		} {
			reqMgmt := httptest.NewRequest(endpoint.method, endpoint.path, io.NopCloser(strings.NewReader(`{"version":"`+strings.Repeat("v", 128)+`"}`)))
			reqMgmt.ContentLength = -1
			reqMgmt.Header.Set("Authorization", "Bearer mgmt-secret")
			reqMgmt.Header.Set("Content-Type", "application/json")
			rrMgmt := httptest.NewRecorder()
			srvMgmt.engine.ServeHTTP(rrMgmt, reqMgmt)
			if rrMgmt.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("%s %s status = %d, want %d; body=%s", endpoint.method, endpoint.path, rrMgmt.Code, http.StatusRequestEntityTooLarge, rrMgmt.Body.String())
			}
		}
	})
}
