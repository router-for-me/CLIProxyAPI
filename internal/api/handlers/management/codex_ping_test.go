package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

type codexPingTestExecutor struct {
	body     string
	chunks   []string
	err      error
	lastAuth *coreauth.Auth
	lastReq  cliproxyexecutor.Request
	lastOpts cliproxyexecutor.Options
}

func (e *codexPingTestExecutor) Identifier() string { return "codex" }

func (e *codexPingTestExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *codexPingTestExecutor) ExecuteStream(_ context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.err != nil {
		return nil, e.err
	}
	e.lastAuth = auth
	e.lastReq = req
	e.lastOpts = opts
	chunks := e.chunks
	if len(chunks) == 0 {
		chunks = []string{e.body}
	}
	ch := make(chan cliproxyexecutor.StreamChunk, len(chunks))
	for _, chunk := range chunks {
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk)}
	}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *codexPingTestExecutor) Refresh(context.Context, *coreauth.Auth) (*coreauth.Auth, error) {
	return nil, nil
}

func (e *codexPingTestExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *codexPingTestExecutor) HttpRequest(_ context.Context, _ *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected HttpRequest call")
}

func TestCodexPingValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name     string
		body     string
		auth     *coreauth.Auth
		wantCode int
	}{
		{name: "missing auth index", body: `{}`, wantCode: http.StatusBadRequest},
		{name: "unknown auth", body: `{"auth_index":"missing"}`, wantCode: http.StatusNotFound},
		{
			name: "disabled auth",
			auth: &coreauth.Auth{
				ID:         "disabled",
				Provider:   "codex",
				Disabled:   true,
				Attributes: map[string]string{"path": "/tmp/codex.json"},
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "non codex auth",
			auth: &coreauth.Auth{
				ID:         "gemini",
				Provider:   "gemini",
				Attributes: map[string]string{"path": "/tmp/gemini.json"},
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "codex api key config auth",
			auth: &coreauth.Auth{
				ID:         "codex-key",
				Provider:   "codex",
				Attributes: map[string]string{"api_key": "secret"},
			},
			wantCode: http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(&codexPingTestExecutor{})
			body := tc.body
			if tc.auth != nil {
				if _, errRegister := manager.Register(context.Background(), tc.auth); errRegister != nil {
					t.Fatalf("Register() error = %v", errRegister)
				}
				body = `{"auth_index":"` + tc.auth.EnsureIndex() + `"}`
			}

			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			req := httptest.NewRequest(http.MethodPost, "/v0/management/codex/ping", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			ctx.Request = req
			(&Handler{authManager: manager}).CodexPing(ctx)

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d, body %s", rec.Code, tc.wantCode, rec.Body.String())
			}
		})
	}
}

func TestCodexPingSendsStatelessResponsesRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	stream := `data: {"type":"response.output_text.delta","delta":"Pong"}` + "\n\n" +
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"input_tokens_details":{"cached_tokens":0},"output_tokens":2,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":3}}}` + "\n\n"
	exec := &codexPingTestExecutor{body: stream}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(exec)
	auth := &coreauth.Auth{
		ID:       "codex.json",
		Provider: "codex",
		Attributes: map[string]string{
			"path":     "/tmp/codex.json",
			"base_url": "https://codex.example.test/backend-api/codex",
		},
		Metadata: map[string]any{
			"access_token": "token",
			"account_id":   "acct_123",
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/codex/ping", strings.NewReader(`{"auth_index":"`+auth.EnsureIndex()+`"}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	(&Handler{authManager: manager}).CodexPing(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if exec.lastAuth == nil {
		t.Fatal("expected executor ExecuteStream to be called")
	}
	if exec.lastAuth.ID != auth.ID {
		t.Fatalf("auth id = %q, want %q", exec.lastAuth.ID, auth.ID)
	}
	if exec.lastReq.Model != codexPingDefaultModel {
		t.Fatalf("request model = %q, want %q", exec.lastReq.Model, codexPingDefaultModel)
	}
	if exec.lastReq.Format != sdktranslator.FromString("openai-response") {
		t.Fatalf("request format = %q", exec.lastReq.Format)
	}
	if !exec.lastOpts.Stream {
		t.Fatal("stream option = false, want true")
	}
	if exec.lastOpts.SourceFormat != sdktranslator.FromString("openai-response") {
		t.Fatalf("source format = %q", exec.lastOpts.SourceFormat)
	}
	if got := exec.lastOpts.Metadata[cliproxyexecutor.PinnedAuthMetadataKey]; got != auth.ID {
		t.Fatalf("pinned auth metadata = %v, want %q", got, auth.ID)
	}
	payload := gjson.ParseBytes(exec.lastReq.Payload)
	if got := payload.Get("model").String(); got != codexPingDefaultModel {
		t.Fatalf("model = %q, want %q", got, codexPingDefaultModel)
	}
	if got := payload.Get("stream").Bool(); !got {
		t.Fatal("stream = false, want true")
	}
	if got := payload.Get("store").Bool(); got {
		t.Fatal("store = true, want false")
	}
	if payload.Get("previous_response_id").Exists() {
		t.Fatal("previous_response_id should be absent")
	}
	if got := payload.Get("reasoning.effort").String(); got != codexPingDefaultReasoning {
		t.Fatalf("reasoning.effort = %q, want %q", got, codexPingDefaultReasoning)
	}
	if got := payload.Get("input").String(); got != codexPingDefaultPrompt {
		t.Fatalf("prompt = %q, want %q", got, codexPingDefaultPrompt)
	}

	var response codexPingResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &response); errDecode != nil {
		t.Fatalf("failed to decode response: %v", errDecode)
	}
	if response.Message != "Pong" {
		t.Fatalf("message = %q, want Pong", response.Message)
	}
	if response.Usage.TotalTokens != 3 || response.Usage.InputTokens != 1 || response.Usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

func TestParseCodexPingStreamFallbackMessageAndUsage(t *testing.T) {
	stream := []byte(`data: {"type":"response.completed","response":{"output":[{"content":[{"type":"output_text","text":"Pong"}]}],"usage":{"inputTokens":4,"cachedInputTokens":1,"outputTokens":2,"reasoningTokens":0}}}` + "\n\n")

	message, usage, completed := parseCodexPingStream(stream)
	if !completed {
		t.Fatal("completed = false, want true")
	}
	if message != "Pong" {
		t.Fatalf("message = %q, want Pong", message)
	}
	if usage.InputTokens != 4 || usage.CachedInputTokens != 1 || usage.OutputTokens != 2 || usage.TotalTokens != 6 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestCodexPingPreservesSplitStreamChunks(t *testing.T) {
	exec := &codexPingTestExecutor{chunks: []string{
		`data: {"type":"response.output_text.delta","delta":"Po`,
		`ng!"}` + "\n\n",
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}` + "\n\n",
	}}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(exec)
	auth := &coreauth.Auth{
		ID:         "codex.json",
		Provider:   "codex",
		Attributes: map[string]string{"path": "/tmp/codex.json"},
		Metadata:   map[string]any{"access_token": "token"},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	response, errPing := (&Handler{authManager: manager}).sendCodexPing(context.Background(), auth)
	if errPing != nil {
		t.Fatalf("sendCodexPing() error = %v", errPing)
	}
	if response.Message != "Pong!" {
		t.Fatalf("message = %q, want Pong!", response.Message)
	}
	if response.Usage.TotalTokens != 3 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

func TestCodexPingUpstreamFailureReturnsSafeError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	exec := &codexPingTestExecutor{err: errors.New("bad upstream secret-token")}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(exec)
	auth := &coreauth.Auth{
		ID:         "codex.json",
		Provider:   "codex",
		Attributes: map[string]string{"path": "/tmp/codex.json"},
		Metadata:   map[string]any{"access_token": "secret-token"},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/codex/ping", strings.NewReader(`{"auth_index":"`+auth.EnsureIndex()+`"}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	(&Handler{authManager: manager}).CodexPing(ctx)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d, body %s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret-token") || strings.Contains(rec.Body.String(), "bad upstream") {
		t.Fatalf("response leaked upstream detail: %s", rec.Body.String())
	}
}
