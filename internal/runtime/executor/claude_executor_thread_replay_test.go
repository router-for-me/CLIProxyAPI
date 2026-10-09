package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const threadContinuationPayload = `{"thread":{"type":"continue","previous_message_id":"msg_prev"},"messages":[{"role":"user","content":[{"type":"text","text":"what did I say?"}]}]}`

func newThreadReplayGateway(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_gateway","type":"message","role":"assistant","model":"claude-3-5-sonnet-20241022","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	t.Cleanup(server.Close)
	return server, &hits
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

func TestClaudeExecutor_ThreadContinuationToGatewayRequestsReplay_Execute(t *testing.T) {
	server, hits := newThreadReplayGateway(t)
	executor := NewClaudeExecutor(&config.Config{})

	_, err := executor.Execute(context.Background(), threadReplayGatewayAuth(server.URL), cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: []byte(threadContinuationPayload),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})

	assertClaudeThreadReplayError(t, err)
	if got := hits.Load(); got != 0 {
		t.Fatalf("gateway received %d requests, want 0: it would answer without the earlier turns", got)
	}
}

func TestClaudeExecutor_ThreadContinuationToGatewayRequestsReplay_ExecuteStream(t *testing.T) {
	server, hits := newThreadReplayGateway(t)
	executor := NewClaudeExecutor(&config.Config{})

	_, err := executor.ExecuteStream(context.Background(), threadReplayGatewayAuth(server.URL), cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: []byte(threadContinuationPayload),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})

	assertClaudeThreadReplayError(t, err)
	if got := hits.Load(); got != 0 {
		t.Fatalf("gateway received %d requests, want 0: it would answer without the earlier turns", got)
	}
}

// The replay that follows carries the full conversation with thread create, which
// a gateway can serve even though it ignores the thread field.
func TestClaudeExecutor_ThreadCreateToGatewayPassesThrough(t *testing.T) {
	server, hits := newThreadReplayGateway(t)
	executor := NewClaudeExecutor(&config.Config{})

	_, err := executor.Execute(context.Background(), threadReplayGatewayAuth(server.URL), cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: []byte(`{"thread":{"type":"create"},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != nil {
		t.Fatalf("thread create to gateway error = %v, want success", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("gateway received %d requests, want 1", got)
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
