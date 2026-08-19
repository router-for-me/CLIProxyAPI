package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func newClaudeCustomUpstreamHeaderTestRequest(t *testing.T) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodPost, "https://gateway.example.com/v1/messages", nil)
}

// A chained CPA scopes execution state per (session, agent). Dropping the agent
// header on relay collapsed every Claude Code subagent into the downstream
// main-agent scope.
func TestApplyClaudeHeaders_RelaysAgentIDToCustomUpstream(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-agent-relay"}}
	for _, confirmed := range []bool{false, true} {
		incoming := http.Header{}
		incoming.Set(helps.ClaudeCodeAgentHeader, "subagent-42")

		req := newClaudeCustomUpstreamHeaderTestRequest(t)
		if err := applyClaudeHeaders(req, auth, "key-agent-relay", false, nil,
			[]byte(`{"model":"claude-opus-5"}`), nil, incoming, confirmed); err != nil {
			t.Fatalf("applyClaudeHeaders(confirmed=%v) error = %v", confirmed, err)
		}
		if got := req.Header.Get(helps.ClaudeCodeAgentHeader); got != "subagent-42" {
			t.Fatalf("confirmed=%v: %s = %q, want relayed subagent-42", confirmed, helps.ClaudeCodeAgentHeader, got)
		}
	}
}

// The client only sends the header for subagents; the relay must not fabricate
// one (not even the "main" sentinel) when the caller omitted it.
func TestApplyClaudeHeaders_NoAgentIDFabricatedWhenAbsent(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-agent-absent"}}
	req := newClaudeCustomUpstreamHeaderTestRequest(t)
	if err := applyClaudeHeaders(req, auth, "key-agent-absent", false, nil,
		[]byte(`{"model":"claude-opus-5"}`), nil, http.Header{}, false); err != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", err)
	}
	if got, ok := req.Header[helps.ClaudeCodeAgentHeader]; ok {
		t.Fatalf("%s = %q, want the header absent", helps.ClaudeCodeAgentHeader, got)
	}
}

// wsrelay and Home dispatch reconstruct header maps by hand, so the incoming
// key may not be in Go's canonical form.
func TestApplyClaudeHeaders_AgentIDRelayIsCaseInsensitive(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-agent-case"}}
	incoming := http.Header{"x-claude-code-agent-id": []string{"lower-agent"}}

	req := newClaudeCustomUpstreamHeaderTestRequest(t)
	if err := applyClaudeHeaders(req, auth, "key-agent-case", false, nil,
		[]byte(`{"model":"claude-opus-5"}`), nil, incoming, false); err != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", err)
	}
	if got := req.Header.Get(helps.ClaudeCodeAgentHeader); got != "lower-agent" {
		t.Fatalf("%s = %q, want relayed lower-agent", helps.ClaudeCodeAgentHeader, got)
	}
}

// Direct Anthropic keeps the measured Claude Code header set; the relay is
// scoped to Anthropic-compatible custom upstreams only.
func TestApplyClaudeHeaders_AgentIDStaysOffDirectAnthropic(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-agent-anthropic"}}
	for _, confirmed := range []bool{false, true} {
		incoming := http.Header{}
		incoming.Set(helps.ClaudeCodeAgentHeader, "subagent-42")

		req := newClaudeHeaderTestRequest(t, incoming)
		if err := applyClaudeHeaders(req, auth, "key-agent-anthropic", false, nil,
			[]byte(`{"model":"claude-opus-5"}`), nil, incoming, confirmed); err != nil {
			t.Fatalf("applyClaudeHeaders(confirmed=%v) error = %v", confirmed, err)
		}
		if got, ok := req.Header[helps.ClaudeCodeAgentHeader]; ok {
			t.Fatalf("confirmed=%v: %s = %q, want the header absent on api.anthropic.com", confirmed, helps.ClaudeCodeAgentHeader, got)
		}
	}
}

// End to end through Execute: the incoming header travels from Options.Headers
// to the custom upstream request.
func TestClaudeExecutor_AgentIDReachesCustomUpstream(t *testing.T) {
	var upstreamHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHeaders = r.Header.Clone()
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:         "claude-agent-relay-upstream",
		Attributes: map[string]string{"api_key": "sk-ant-oat-agent-relay", "base_url": server.URL},
		Metadata:   claudeOAuthTestMetadata(),
	}
	incoming := http.Header{}
	incoming.Set(helps.ClaudeCodeAgentHeader, "subagent-42")
	payload := []byte(`{"model":"claude-opus-5","system":"p","messages":[{"role":"user","content":"hi"}]}`)

	if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: bytes.Clone(payload),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: incoming}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := upstreamHeaders.Get(helps.ClaudeCodeAgentHeader); got != "subagent-42" {
		t.Fatalf("upstream %s = %q, want subagent-42", helps.ClaudeCodeAgentHeader, got)
	}
}
