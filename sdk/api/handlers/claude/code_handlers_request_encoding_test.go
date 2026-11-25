package claude

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type issue7711ClaudeExecutor struct {
	called     *int32
	gotRequest *coreexecutor.Request
	gotOptions *coreexecutor.Options
}

func (e *issue7711ClaudeExecutor) Identifier() string { return "claude" }

func (e *issue7711ClaudeExecutor) record(req coreexecutor.Request, opts coreexecutor.Options) {
	atomic.AddInt32(e.called, 1)
	*e.gotRequest = req
	*e.gotOptions = opts
}

func (e *issue7711ClaudeExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	coreexecutor.MarkUpstreamAttempt(context.Background())
	e.record(req, opts)
	return coreexecutor.Response{Payload: []byte(`{"type":"message","model":"m"}`)}, nil
}

func (e *issue7711ClaudeExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	coreexecutor.MarkUpstreamAttempt(context.Background())
	e.record(req, opts)
	chunks := make(chan coreexecutor.StreamChunk)
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *issue7711ClaudeExecutor) CountTokens(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	coreexecutor.MarkUpstreamAttempt(context.Background())
	e.record(req, opts)
	return coreexecutor.Response{Payload: []byte(`{"input_tokens":1}`)}, nil
}

func (e *issue7711ClaudeExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *issue7711ClaudeExecutor) HttpRequest(_ context.Context, _ *coreauth.Auth, _ *http.Request) (*http.Response, error) {
	return nil, nil
}

func issue7711Gzip(t *testing.T, plain []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, errWrite := w.Write(plain); errWrite != nil {
		t.Fatalf("gzip write: %v", errWrite)
	}
	if errClose := w.Close(); errClose != nil {
		t.Fatalf("gzip close: %v", errClose)
	}
	return buf.Bytes()
}

func issue7711Zstd(t *testing.T, plain []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, errNew := zstd.NewWriter(&buf)
	if errNew != nil {
		t.Fatalf("zstd writer: %v", errNew)
	}
	if _, errWrite := w.Write(plain); errWrite != nil {
		t.Fatalf("zstd write: %v", errWrite)
	}
	if errClose := w.Close(); errClose != nil {
		t.Fatalf("zstd close: %v", errClose)
	}
	return buf.Bytes()
}

// TestClaudeRoutesDecodeRequestBody verifies that /v1/messages and
// /v1/messages/count_tokens decode Content-Encoding request bodies through the
// shared ReadRequestBody path instead of reading compressed bytes raw.
func TestClaudeRoutesDecodeRequestBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	plain := []byte(`{"model":"issue-7711-claude-model","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`)

	for _, tc := range []struct {
		name     string
		path     string
		encoding string
		body     []byte
	}{
		{name: "messages_gzip", path: "/v1/messages", encoding: "gzip", body: issue7711Gzip(t, plain)},
		{name: "messages_zstd", path: "/v1/messages", encoding: "zstd", body: issue7711Zstd(t, plain)},
		{name: "count_tokens_gzip", path: "/v1/messages/count_tokens", encoding: "gzip", body: issue7711Gzip(t, plain)},
		{name: "count_tokens_zstd", path: "/v1/messages/count_tokens", encoding: "zstd", body: issue7711Zstd(t, plain)},
		{name: "messages_plain", path: "/v1/messages", encoding: "", body: plain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var called int32
			var gotReq coreexecutor.Request
			var gotOpts coreexecutor.Options
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(&issue7711ClaudeExecutor{called: &called, gotRequest: &gotReq, gotOptions: &gotOpts})

			reg := registry.GetGlobalRegistry()
			authID := fmt.Sprintf("issue-7711-%s", tc.name)
			auth := &coreauth.Auth{
				ID:         authID,
				Provider:   "claude",
				Status:     coreauth.StatusActive,
				Attributes: map[string]string{"api_key": "key"},
			}
			if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
				t.Fatalf("register auth: %v", errReg)
			}
			reg.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: "issue-7711-claude-model"}})
			t.Cleanup(func() {
				reg.UnregisterClient(authID)
			})

			handler := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
			router := gin.New()
			router.POST("/v1/messages", handler.ClaudeMessages)
			router.POST("/v1/messages/count_tokens", handler.ClaudeCountTokens)

			req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.encoding != "" {
				req.Header.Set("Content-Encoding", tc.encoding)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if got := atomic.LoadInt32(&called); got == 0 {
				t.Fatalf("executor was never reached; handler saw compressed bytes (status=%d body=%s)", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "unknown provider for model") {
				t.Fatalf("misleading model-routing error: %s", rec.Body.String())
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			// The executor must see the same bytes a plain request delivers, and the
			// decoded body must not travel with a stale Content-Encoding header.
			if !bytes.Equal(gotReq.Payload, plain) {
				t.Fatalf("executor payload = %q, want %q", gotReq.Payload, plain)
			}
			if gotOpts.Headers != nil && gotOpts.Headers.Get("Content-Encoding") != "" {
				t.Fatalf("executor options still carry Content-Encoding %q", gotOpts.Headers.Get("Content-Encoding"))
			}
		})
	}
}
