package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	threadReplayModel          = "claude-3-5-sonnet-20241022"
	threadContinuationPayload  = `{"thread":{"type":"continue","previous_message_id":"msg_prev"},"messages":[{"role":"user","content":[{"type":"text","text":"what did I say?"}]}]}`
	threadReplayPlainPayload   = `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	threadReplayJSONResponse   = `{"id":"msg_gateway","type":"message","role":"assistant","model":"claude-3-5-sonnet-20241022","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	threadReplayStreamResponse = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_gateway\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-3-5-sonnet-20241022\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
)

type threadReplayGateway struct {
	server   *httptest.Server
	hits     atomic.Int32
	mu       sync.Mutex
	lastBody []byte
}

func newThreadReplayGateway(t *testing.T) *threadReplayGateway {
	t.Helper()
	gateway := &threadReplayGateway{}
	gateway.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gateway.hits.Add(1)
		gateway.mu.Lock()
		gateway.lastBody = body
		gateway.mu.Unlock()
		if gjson.GetBytes(body, "stream").Bool() {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(threadReplayStreamResponse))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(threadReplayJSONResponse))
	}))
	t.Cleanup(gateway.server.Close)
	return gateway
}

func (g *threadReplayGateway) body() []byte {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastBody
}

func threadReplayGatewayAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "claude-gateway",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": baseURL,
		},
	}
}

// executeThreadReplayRequest runs one request through Execute or ExecuteStream and
// drains the stream so both paths report upstream outcomes the same way.
func executeThreadReplayRequest(t *testing.T, cfg *config.Config, baseURL, payload string, stream bool) error {
	t.Helper()
	executor := NewClaudeExecutor(cfg)
	req := cliproxyexecutor.Request{Model: threadReplayModel, Payload: []byte(payload)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
	auth := threadReplayGatewayAuth(baseURL)
	if !stream {
		_, err := executor.Execute(context.Background(), auth, req, opts)
		return err
	}
	result, err := executor.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		return err
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			return chunk.Err
		}
	}
	return nil
}

func assertClaudeThreadReplayError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("thread continuation to a gateway returned nil error, want thread-not-found replay signal")
	}
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != http.StatusNotFound {
		t.Fatalf("error = %v, want HTTP 404", err)
	}
	var scoped cliproxyexecutor.RequestScopedError
	if !errors.As(err, &scoped) || !scoped.IsRequestScoped() {
		t.Fatalf("error %T is not request-scoped; it would rotate or cool the credential", err)
	}
	if !clienterror.IsClaudeThreadNotFound(http.StatusNotFound, err) {
		t.Fatalf("error %v is not recognized as Claude thread-not-found, so the client would not replay", err)
	}
}

func threadReplayModels() []config.PayloadModelRule {
	return []config.PayloadModelRule{{Name: threadReplayModel}}
}

// The guard must judge the body that is actually sent, after payload rules run as
// the final semantic barrier, on both the Execute and ExecuteStream paths.
func TestClaudeExecutor_ThreadReplayGuardObservesFinalPayload(t *testing.T) {
	for _, tt := range []struct {
		name       string
		payloadCfg config.PayloadConfig
		payload    string
		wantReplay bool
		wantThread bool
	}{
		{
			name:       "continuation without rules requests replay",
			payload:    threadContinuationPayload,
			wantReplay: true,
		},
		{
			name: "filter removing thread reaches gateway",
			payloadCfg: config.PayloadConfig{Filter: []config.PayloadFilterRule{{
				Models: threadReplayModels(),
				Params: []string{"thread"},
			}}},
			payload: threadContinuationPayload,
		},
		{
			name: "override clearing previous message reaches gateway",
			payloadCfg: config.PayloadConfig{Override: []config.PayloadRule{{
				Models: threadReplayModels(),
				Params: map[string]any{"thread.previous_message_id": ""},
			}}},
			payload:    threadContinuationPayload,
			wantThread: true,
		},
		{
			name: "override introducing continuation requests replay",
			payloadCfg: config.PayloadConfig{Override: []config.PayloadRule{{
				Models: threadReplayModels(),
				Params: map[string]any{"thread.type": "continue", "thread.previous_message_id": "msg_injected"},
			}}},
			payload:    threadReplayPlainPayload,
			wantReplay: true,
		},
		{
			name:    "thread create reaches gateway",
			payload: `{"thread":{"type":"create"},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
			// The full replay a client sends after the 404 must pass through untouched.
			wantThread: true,
		},
	} {
		for _, stream := range []bool{false, true} {
			name := tt.name + "/execute"
			if stream {
				name = tt.name + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				gateway := newThreadReplayGateway(t)
				err := executeThreadReplayRequest(t, &config.Config{Payload: tt.payloadCfg}, gateway.server.URL, tt.payload, stream)
				if tt.wantReplay {
					assertClaudeThreadReplayError(t, err)
					if got := gateway.hits.Load(); got != 0 {
						t.Fatalf("gateway received %d requests, want 0: it would answer without the earlier turns", got)
					}
					return
				}
				if err != nil {
					t.Fatalf("request error = %v, want it forwarded to the gateway", err)
				}
				if got := gateway.hits.Load(); got != 1 {
					t.Fatalf("gateway received %d requests, want 1", got)
				}
				if got := gjson.GetBytes(gateway.body(), "thread").Exists(); got != tt.wantThread {
					t.Fatalf("upstream thread present = %v, want %v; body=%s", got, tt.wantThread, gateway.body())
				}
			})
		}
	}
}

func TestClaudeThreadContinuationNeedsReplay(t *testing.T) {
	continuation := []byte(threadContinuationPayload)
	for _, tt := range []struct {
		name    string
		payload []byte
		apiKey  string
		baseURL string
		want    bool
	}{
		{name: "gateway continuation", payload: continuation, apiKey: "test-key", baseURL: "https://relay.example.com", want: true},
		{name: "anthropic api key continuation", payload: continuation, apiKey: "test-key", baseURL: "https://api.anthropic.com", want: false},
		{name: "oauth behind forwarding base url", payload: continuation, apiKey: "sk-ant-oat01-test", baseURL: "https://egress.example.com", want: false},
		{name: "gateway thread create", payload: []byte(`{"thread":{"type":"create"},"messages":[]}`), apiKey: "test-key", baseURL: "https://relay.example.com", want: false},
		{name: "gateway without thread", payload: []byte(`{"messages":[]}`), apiKey: "test-key", baseURL: "https://relay.example.com", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := claudeThreadContinuationNeedsReplay(tt.payload, tt.apiKey, tt.baseURL); got != tt.want {
				t.Fatalf("claudeThreadContinuationNeedsReplay() = %v, want %v", got, tt.want)
			}
		})
	}
}
