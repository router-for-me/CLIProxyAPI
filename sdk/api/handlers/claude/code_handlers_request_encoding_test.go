package claude

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func gzipBytesForTest(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(raw); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func zstdBytesForTest(t *testing.T, raw []byte) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	defer func() { _ = enc.Close() }()
	return enc.EncodeAll(raw, nil)
}

type capturedClaudeRequest struct {
	payload         []byte
	contentEncoding string
}

func TestClaudeRoutesDecodeRequestContentEncoding(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const model = "request-encoding-claude-model"
	plain := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}],"stream":false}`)

	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		for _, tc := range []struct {
			name     string
			encoding string
			body     func(t *testing.T) []byte
		}{
			{"plain", "", func(*testing.T) []byte { return plain }},
			{"identity", "identity", func(*testing.T) []byte { return plain }},
			{"zstd", "zstd", func(t *testing.T) []byte { return zstdBytesForTest(t, plain) }},
			{"gzip", "gzip", func(t *testing.T) []byte { return gzipBytesForTest(t, plain) }},
			{"x-gzip", "x-gzip", func(t *testing.T) []byte { return gzipBytesForTest(t, plain) }},
			{"gzip_then_zstd", "gzip, zstd", func(t *testing.T) []byte { return zstdBytesForTest(t, gzipBytesForTest(t, plain)) }},
		} {
			t.Run(strings.TrimPrefix(path, "/v1/")+"/"+tc.name, func(t *testing.T) {
				captured := make(chan capturedClaudeRequest, 1)
				capture := func(ctx context.Context, req coreexecutor.Request) {
					enc := ""
					if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
						enc = ginCtx.Request.Header.Get("Content-Encoding")
					}
					captured <- capturedClaudeRequest{payload: append([]byte(nil), req.Payload...), contentEncoding: enc}
				}
				manager := coreauth.NewManager(nil, nil, nil)
				manager.RegisterExecutor(&issue7521ClaudeExecutor{
					executeFn: func(ctx context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
						capture(ctx, req)
						return coreexecutor.Response{Payload: []byte(`{"type":"message"}`)}, nil
					},
					streamFn: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
						t.Fatal("unexpected stream call")
						return nil, nil
					},
					countFn: func(ctx context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
						capture(ctx, req)
						return coreexecutor.Response{Payload: []byte(`{"input_tokens":1}`)}, nil
					},
				})
				authID := "request-encoding-" + strings.ReplaceAll(path, "/", "-") + "-" + tc.name
				if _, err := manager.Register(context.Background(), &coreauth.Auth{
					ID: authID, Provider: "claude", Status: coreauth.StatusActive,
					Attributes: map[string]string{"api_key": "key"},
				}); err != nil {
					t.Fatalf("register auth: %v", err)
				}
				reg := registry.GetGlobalRegistry()
				reg.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { reg.UnregisterClient(authID) })

				handler := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
				router := gin.New()
				router.POST("/v1/messages", handler.ClaudeMessages)
				router.POST("/v1/messages/count_tokens", handler.ClaudeCountTokens)

				req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(tc.body(t)))
				req.Header.Set("Content-Type", "application/json")
				if tc.encoding != "" {
					req.Header.Set("Content-Encoding", tc.encoding)
				}
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
				}
				got := <-captured
				if !bytes.Equal(got.payload, plain) {
					t.Fatalf("executor payload = %q, want %q", got.payload, plain)
				}
				if got.contentEncoding != "" {
					t.Fatalf("Content-Encoding still on request after decode: %q", got.contentEncoding)
				}
			})
		}
	}
}

func TestClaudeRoutesRejectBadRequestContentEncoding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	plain := []byte(`{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat("a", 4096) + `"}]}`)
	good := gzipBytesForTest(t, plain)

	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		for _, tc := range []struct {
			name     string
			encoding string
			body     []byte
			maxBytes int64
			want     int
		}{
			{"over_cap_gzip", "gzip", good, 1024, http.StatusRequestEntityTooLarge},
			{"malformed_gzip", "gzip", []byte("not gzip at all"), 0, http.StatusBadRequest},
			{"truncated_gzip", "gzip", good[:len(good)/2], 0, http.StatusBadRequest},
			{"unsupported_encoding", "br", good, 0, http.StatusBadRequest},
		} {
			t.Run(strings.TrimPrefix(path, "/v1/")+"/"+tc.name, func(t *testing.T) {
				handler := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
				router := gin.New()
				router.Use(func(c *gin.Context) {
					c.Set(handlers.MaxRequestBodyBytesContextKey, tc.maxBytes)
				})
				router.POST("/v1/messages", handler.ClaudeMessages)
				router.POST("/v1/messages/count_tokens", handler.ClaudeCountTokens)

				req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(tc.body))
				req.Header.Set("Content-Encoding", tc.encoding)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				if rec.Code != tc.want {
					t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.want, rec.Body.String())
				}
			})
		}
	}
}
