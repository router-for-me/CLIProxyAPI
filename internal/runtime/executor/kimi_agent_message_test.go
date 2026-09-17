package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// TestNormalizeKimiAgentMessages covers the payload rewrite that unblocks the
// upstream Kimi Responses endpoint for Codex subagent sessions (live-proven
// 2026-09-17: agent_message items -> HTTP 400 invalid_request_error).
func TestNormalizeKimiAgentMessages(t *testing.T) {
	t.Run("rewrites agent_message to user message and surfaces ciphertext", func(t *testing.T) {
		body := []byte(`{
			"model": "k3",
			"input": [
				{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "before"}]},
				{"type": "agent_message", "id": "amsg_1", "author": "/root", "recipient": "/root/sub",
					"content": [
						{"type": "input_text", "text": "task text"},
						{"type": "encrypted_content", "encrypted_content": "ciphertext-payload"}
					]},
				{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "after"}]}
			]
		}`)
		out := normalizeKimiAgentMessages(body)
		items := gjson.ParseBytes(out).Get("input").Array()
		if len(items) != 3 {
			t.Fatalf("expected 3 items, got %d: %s", len(items), string(out))
		}
		item := items[1]
		if item.Get("type").String() != "message" || item.Get("role").String() != "user" {
			t.Fatalf("item[1] = %q/%q, want message/user", item.Get("type").String(), item.Get("role").String())
		}
		if item.Get("content.0.text").String() != "task text" {
			t.Fatalf("delegated task text lost: %s", item.Get("content.0").Raw)
		}
		if item.Get("content.1.type").String() != "input_text" || item.Get("content.1.text").String() != "ciphertext-payload" {
			t.Fatalf("ciphertext not surfaced as input_text: %s", item.Get("content.1").Raw)
		}
		if item.Get("content.1.encrypted_content").Exists() {
			t.Fatalf("encrypted_content key must be removed: %s", item.Get("content.1").Raw)
		}
		if items[0].Get("content.0.text").String() != "before" || items[2].Get("content.0.text").String() != "after" {
			t.Fatalf("neighbouring items mutated: %s", string(out))
		}
	})

	t.Run("passes through payloads without input array", func(t *testing.T) {
		body := []byte(`{"model": "k3", "messages": [{"role": "user", "content": "hi"}]}`)
		if out := normalizeKimiAgentMessages(body); string(out) != string(body) {
			t.Fatalf("chat-format payload must be untouched, got %s", string(out))
		}
	})

	t.Run("passes through when no agent_message present", func(t *testing.T) {
		body := []byte(`{"input": [{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]}]}`)
		if out := normalizeKimiAgentMessages(body); string(out) != string(body) {
			t.Fatalf("agent_message-free payload must be untouched, got %s", string(out))
		}
	})

	t.Run("handles agent_message without content array", func(t *testing.T) {
		body := []byte(`{"input": [{"type": "agent_message", "id": "amsg_2"}]}`)
		out := normalizeKimiAgentMessages(body)
		item := gjson.ParseBytes(out).Get("input.0")
		if item.Get("type").String() != "message" || item.Get("role").String() != "user" {
			t.Fatalf("contentless agent_message not converted: %s", item.Raw)
		}
	})
}

// TestKimiExecutorResponsesNonStream_NormalizesAgentMessages exercises the
// non-streaming Responses dispatch (Execute -> executeResponses) to prove the
// normalization reaches the upstream request on that route too.
func TestKimiExecutorResponsesNonStream_NormalizesAgentMessages(t *testing.T) {
	var upstreamBody []byte

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		var errRead error
		upstreamBody, errRead = io.ReadAll(req.Body)
		if errRead != nil {
			return nil, errRead
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"resp_test","object":"response","status":"completed","model":"k3","output":[],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`)),
		}, nil
	}))

	cfg := &config.Config{}
	executor := NewKimiExecutor(cfg)
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{},
		Metadata:   map[string]any{"access_token": "test-key"},
	}

	payload := []byte(`{
		"model": "kimi-k3",
		"input": [
			{"type": "agent_message", "id": "amsg_1", "author": "/root", "recipient": "/root/sub",
				"content": [
					{"type": "input_text", "text": "task text"},
					{"type": "encrypted_content", "encrypted_content": "ciphertext-payload"}
				]},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hello"}]}
		]
	}`)

	_, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "kimi-k3",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if len(upstreamBody) == 0 {
		t.Fatal("expected upstreamBody to be captured, got empty")
	}
	parsed := gjson.ParseBytes(upstreamBody)
	item0 := parsed.Get("input.0")
	if item0.Get("type").String() != "message" || item0.Get("role").String() != "user" {
		t.Fatalf("upstream input[0] = %q/%q, want message/user (agent_message normalized): %s", item0.Get("type").String(), item0.Get("role").String(), string(upstreamBody))
	}
	if item0.Get("content.1.text").String() != "ciphertext-payload" {
		t.Fatalf("upstream ciphertext not surfaced: %s", item0.Get("content.1").Raw)
	}
	if parsed.Get("input.1.content.0.text").String() != "hello" {
		t.Fatalf("neighbouring user message mutated: %s", string(upstreamBody))
	}
}
