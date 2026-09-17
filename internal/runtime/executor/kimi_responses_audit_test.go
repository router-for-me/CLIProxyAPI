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

// TestKimiExecutorResponsesPassthrough_RegressionCoverageGap originally
// established (2026-09-17) that the Kimi Responses path forwarded
// agent_message with encrypted_content unchanged, and that a controlled live
// probe reproduced the production failure: the upstream Kimi Responses
// endpoint rejects agent_message items with HTTP 400 invalid_request_error,
// breaking every Codex session that injected subagent handoff items.
//
// After normalizeKimiAgentMessages landed, this test now asserts the FIXED
// behavior: agent_message items are rewritten to plain user messages with
// encrypted_content surfaced as input_text, while interleaved tool outputs /
// developer messages are still forwarded unchanged.
func TestKimiExecutorResponsesPassthrough_RegressionCoverageGap(t *testing.T) {
	var upstreamBody []byte

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		var errRead error
		upstreamBody, errRead = io.ReadAll(req.Body)
		if errRead != nil {
			return nil, errRead
		}
		sseData := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_test\",\"status\":\"in_progress\",\"service_tier\":\"default\",\"model\":\"k3\"}}\n\n" +
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"usage\":{\"total_tokens\":5,\"input_tokens\":3,\"output_tokens\":2}}}\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(sseData)),
		}, nil
	}))

	cfg := &config.Config{}
	cfg.Codex.OptimizeMultiAgentV2 = true
	executor := NewKimiExecutor(cfg)
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{},
		Metadata:   map[string]any{"access_token": "test-key"},
	}

	payload := []byte(`{
		"model": "kimi-k3",
		"stream": true,
		"input": [
			{
				"type": "agent_message",
				"id": "amsg_1",
				"author": "/root",
				"recipient": "/root/sub",
				"content": [
					{"type": "input_text", "text": "task text"},
					{"type": "encrypted_content", "encrypted_content": "ciphertext-payload"}
				]
			},
			{"type": "function_call", "name": "view_image", "call_id": "call_view"},
			{"type": "function_call", "name": "js", "call_id": "call_js"},
			{"type": "function_call_output", "call_id": "call_view", "output": "ok_view"},
			{"type": "message", "role": "developer", "content": [{"type": "input_text", "text": "resize notice"}]},
			{"type": "function_call_output", "call_id": "call_js", "output": "ok_js"}
		]
	}`)

	headers := http.Header{}
	headers.Set("User-Agent", "Codex Desktop/0.154.0-alpha.6.2")

	result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "kimi-k3",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       true,
		Headers:      headers,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	// Drain chunks to complete stream execution
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
	}

	if len(upstreamBody) == 0 {
		t.Fatal("expected upstreamBody to be captured, got empty")
	}

	parsed := gjson.ParseBytes(upstreamBody)
	inputArray := parsed.Get("input").Array()
	if len(inputArray) != 6 {
		t.Fatalf("expected 6 input items forwarded upstream, got %d: %s", len(inputArray), string(upstreamBody))
	}

	// 1. Verify agent_message was normalized to a user message and encrypted_content surfaced as input_text
	item0 := inputArray[0]
	if item0.Get("type").String() != "message" || item0.Get("role").String() != "user" {
		t.Fatalf("input[0] = %q/%q, want message/user (agent_message normalized)", item0.Get("type").String(), item0.Get("role").String())
	}
	if item0.Get("content.0.type").String() != "input_text" || item0.Get("content.0.text").String() != "task text" {
		t.Fatalf("input[0].content[0] = %q, want input_text %q (delegated task text preserved)", item0.Get("content.0.text").String(), "task text")
	}
	if item0.Get("content.1.type").String() != "input_text" || item0.Get("content.1.text").String() != "ciphertext-payload" {
		t.Fatalf("input[0].content[1].type = %q, want input_text with ciphertext surfaced", item0.Get("content.1.type").String())
	}
	if item0.Get("content.1.encrypted_content").Exists() {
		t.Fatalf("input[0].content[1].encrypted_content still present after normalization: %s", item0.Get("content.1").Raw)
	}

	// 2. Verify interleaved developer message was forwarded UNCHANGED (not deferred, interleaved directly between call_view and call_js outputs)
	if inputArray[3].Get("type").String() != "function_call_output" || inputArray[3].Get("call_id").String() != "call_view" {
		t.Fatalf("input[3] mismatch: %s", inputArray[3].Raw)
	}
	if inputArray[4].Get("type").String() != "message" || inputArray[4].Get("role").String() != "developer" {
		t.Fatalf("input[4] mismatch: expected developer message preserved at index 4, got: %s", inputArray[4].Raw)
	}
	if inputArray[5].Get("type").String() != "function_call_output" || inputArray[5].Get("call_id").String() != "call_js" {
		t.Fatalf("input[5] mismatch: expected function_call_output call_js at index 5, got: %s", inputArray[5].Raw)
	}

	t.Logf("Verified: agent_message normalized for upstream Kimi; interleaved tool outputs/developer message forwarded unchanged")
}
