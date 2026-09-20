package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// TestClaudeBillingFingerprintMultiTurnUsesFirstUserText verifies that in a multi-turn conversation,
// the billing fingerprint derives from the first user message (the stable conversation anchor)
// rather than the latest user message. If it derives from the latest user message, every new
// turn produces a new buildHash, altering system[0] and invalidating the entire prompt cache prefix.
func TestClaudeBillingFingerprintMultiTurnUsesFirstUserText(t *testing.T) {
	payload := []byte(`{
		"model": "claude-opus-5",
		"system": "You are a helpful assistant.",
		"messages": [
			{"role": "user", "content": "initial conversation turn"},
			{"role": "assistant", "content": "Understood."},
			{"role": "user", "content": [
				{"type": "text", "text": "<system-reminder>currentDate</system-reminder>"},
				{"type": "text", "text": "second conversation turn"}
			]}
		]
	}`)

	want := "initial conversation turn"
	got := claudeBillingFingerprintMessageText(payload)
	if got != want {
		t.Fatalf("claudeBillingFingerprintMessageText() = %q, want first user text %q (latest user text busts prompt cache prefix across turns)", got, want)
	}
}

// TestClaudeBillingFingerprintEdgeCases checks boundaries: no user message, empty message,
// non-text parts only, reminder only, multiple text blocks.
func TestClaudeBillingFingerprintEdgeCases(t *testing.T) {
	// 1. No messages
	if got := claudeBillingFingerprintMessageText([]byte(`{"messages":[]}`)); got != "" {
		t.Fatalf("empty messages: got %q, want empty", got)
	}

	// 2. Only assistant message
	if got := claudeBillingFingerprintMessageText([]byte(`{"messages":[{"role":"assistant","content":"hi"}]}`)); got != "" {
		t.Fatalf("only assistant message: got %q, want empty", got)
	}

	// 3. User message without text
	if got := claudeBillingFingerprintMessageText([]byte(`{"messages":[{"role":"user","content":[{"type":"image"}]}]}`)); got != "" {
		t.Fatalf("user message with no text: got %q, want empty", got)
	}

	// 4. User message with only system reminder
	if got := claudeBillingFingerprintMessageText([]byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>date</system-reminder>"}]}]}`)); got != "" {
		t.Fatalf("user message with only reminder: got %q, want empty", got)
	}

	// 5. Multiple text blocks: skips reminder, takes real text
	multiPart := []byte(`{"messages":[{"role":"user","content":[
		{"type":"text","text":"<system-reminder>date</system-reminder>"},
		{"type":"text","text":"real user question"},
		{"type":"text","text":"additional context"}
	]}]}`)
	if got := claudeBillingFingerprintMessageText(multiPart); got != "additional context" {
		t.Fatalf("multi-part user message: got %q, want %q", got, "additional context")
	}
}

// TestClaudeCloakedSingleTurnPrefixStability asserts that repeated identical single-turn requests
// (such as the repro in Issue #5730) produce identical upstream request bodies so that Anthropic
// prompt caching hits rather than misses on every run.
func TestClaudeCloakedSingleTurnPrefixStability(t *testing.T) {
	var capturedBodies [][]byte
	call := 0
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		capturedBodies = append(capturedBodies, body)
		call++
		response := fmt.Sprintf(`{"id":"msg_%d","type":"message","model":"claude-opus-5","role":"assistant","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":10,"output_tokens":2}}`, call)
		header := make(http.Header)
		header.Set("Content-Type", "application/json")
		header.Set("request-id", fmt.Sprintf("req_upstream_%d", call))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(response)),
			Request:    req,
		}, nil
	})

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	auth := &cliproxyauth.Auth{
		ID:         "test-cloaked-oauth",
		Attributes: map[string]string{"api_key": "sk-ant-oat-test-oauth-key"},
		Metadata: map[string]any{
			"account_uuid": "11111111-2222-4333-8444-555555555555",
			claudeauth.ClaudeDeviceIDsMetadataKey: []string{
				"0000000000000000000000000000000000000000000000000000000000000001",
			},
		},
	}
	exec := NewClaudeExecutor(&config.Config{})
	options := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
	}

	systemPrompt := "You are an expert software engineer with deep knowledge of distributed systems..."
	payload1 := fmt.Sprintf(`{
		"model": "claude-opus-5",
		"system": [{"type": "text", "text": %q, "cache_control": {"type": "ephemeral"}}],
		"messages": [{"role": "user", "content": "Say OK."}],
		"max_tokens": 100
	}`, systemPrompt)

	req1 := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(payload1)}
	if _, err := exec.Execute(ctx, auth, req1, options); err != nil {
		t.Fatalf("request 1 failed: %v", err)
	}

	// Request 2: identical payload sent shortly after (reproducing Issue #5730)
	req2 := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(payload1)}
	if _, err := exec.Execute(ctx, auth, req2, options); err != nil {
		t.Fatalf("request 2 failed: %v", err)
	}

	if len(capturedBodies) != 2 {
		t.Fatalf("expected 2 captured bodies, got %d", len(capturedBodies))
	}

	body1 := capturedBodies[0]
	body2 := capturedBodies[1]

	if !bytes.Equal(body1, body2) {
		t.Fatalf("upstream request body changed between identical single-turn requests:\nBody 1: %s\nBody 2: %s", string(body1), string(body2))
	}
}

// TestClaudeCloakedSingleTurnPrefixStabilityWithPinnedSession verifies that even when
// a caller explicitly pins X-Session-ID on repeated identical single-turn requests,
// the upstream request body remains 100% byte-for-byte identical across requests.
func TestClaudeCloakedSingleTurnPrefixStabilityWithPinnedSession(t *testing.T) {
	var capturedBodies [][]byte
	call := 0
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		capturedBodies = append(capturedBodies, body)
		call++
		response := fmt.Sprintf(`{"id":"msg_%d","type":"message","model":"claude-opus-5","role":"assistant","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":10,"output_tokens":2}}`, call)
		header := make(http.Header)
		header.Set("Content-Type", "application/json")
		header.Set("request-id", fmt.Sprintf("req_upstream_%d", call))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(response)),
			Request:    req,
		}, nil
	})

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	auth := &cliproxyauth.Auth{
		ID:         "test-cloaked-oauth-pinned",
		Attributes: map[string]string{"api_key": "sk-ant-oat-test-oauth-key-pinned"},
		Metadata: map[string]any{
			"account_uuid": "11111111-2222-4333-8444-555555555555",
			claudeauth.ClaudeDeviceIDsMetadataKey: []string{
				"0000000000000000000000000000000000000000000000000000000000000001",
			},
		},
	}
	exec := NewClaudeExecutor(&config.Config{})
	options := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Headers:      http.Header{"X-Session-ID": []string{"pinned-session-42"}},
	}

	systemPrompt := "You are an expert software engineer with deep knowledge of distributed systems..."
	payload1 := fmt.Sprintf(`{
		"model": "claude-opus-5",
		"system": [{"type": "text", "text": %q, "cache_control": {"type": "ephemeral"}}],
		"messages": [{"role": "user", "content": "Say OK."}],
		"max_tokens": 100
	}`, systemPrompt)

	req1 := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(payload1)}
	if _, err := exec.Execute(ctx, auth, req1, options); err != nil {
		t.Fatalf("request 1 failed: %v", err)
	}

	req2 := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(payload1)}
	if _, err := exec.Execute(ctx, auth, req2, options); err != nil {
		t.Fatalf("request 2 failed: %v", err)
	}

	if len(capturedBodies) != 2 {
		t.Fatalf("expected 2 captured bodies, got %d", len(capturedBodies))
	}

	body1 := capturedBodies[0]
	body2 := capturedBodies[1]

	if !bytes.Equal(body1, body2) {
		t.Fatalf("upstream request body changed between identical requests with pinned session:\nBody 1: %s\nBody 2: %s", string(body1), string(body2))
	}
}

// TestClaudeCloakedMultiTurnPrefixStability verifies that across turns in a multi-turn conversation:
//  1. The top-level system prefix fields (including cc_version buildHash, cc_prompt_id, and system.1 identity)
//     and messages[0]/messages[1] remain invariant between Turn 1, Turn 2, and Turn 3.
//  2. Neither turn injects dynamic cc_prev_req into the cloaked system header.
//  3. Repeated requests of the exact same multi-turn history produce 100% byte-identical upstream bodies.
func TestClaudeCloakedMultiTurnPrefixStability(t *testing.T) {
	var capturedBodies [][]byte
	call := 0
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		capturedBodies = append(capturedBodies, body)
		call++
		response := fmt.Sprintf(`{"id":"msg_%d","type":"message","model":"claude-opus-5","role":"assistant","content":[{"type":"text","text":"Understood."}],"usage":{"input_tokens":10,"output_tokens":2}}`, call)
		header := make(http.Header)
		header.Set("Content-Type", "application/json")
		header.Set("request-id", fmt.Sprintf("req_upstream_%d", call))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(response)),
			Request:    req,
		}, nil
	})

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	auth := &cliproxyauth.Auth{
		ID:         "test-cloaked-multiturn-oauth",
		Attributes: map[string]string{"api_key": "sk-ant-oat-test-oauth-key-multiturn"},
		Metadata: map[string]any{
			"account_uuid": "11111111-2222-4333-8444-555555555555",
			claudeauth.ClaudeDeviceIDsMetadataKey: []string{
				"0000000000000000000000000000000000000000000000000000000000000001",
			},
		},
	}
	exec := NewClaudeExecutor(&config.Config{})
	options := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
	}

	systemPrompt := "You are an expert software engineer with deep knowledge of distributed systems..."
	turn1Payload := fmt.Sprintf(`{
		"model": "claude-opus-5",
		"system": [{"type": "text", "text": %q, "cache_control": {"type": "ephemeral"}}],
		"messages": [{"role": "user", "content": "turn 1 user prompt"}],
		"max_tokens": 100
	}`, systemPrompt)

	turn2Payload := fmt.Sprintf(`{
		"model": "claude-opus-5",
		"system": [{"type": "text", "text": %q, "cache_control": {"type": "ephemeral"}}],
		"messages": [
			{"role": "user", "content": "turn 1 user prompt"},
			{"role": "assistant", "content": "Understood."},
			{"role": "user", "content": "turn 2 user prompt with new question"}
		],
		"max_tokens": 100
	}`, systemPrompt)

	if _, err := exec.Execute(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(turn1Payload)}, options); err != nil {
		t.Fatalf("turn 1 failed: %v", err)
	}
	if _, err := exec.Execute(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(turn2Payload)}, options); err != nil {
		t.Fatalf("turn 2 failed: %v", err)
	}

	if len(capturedBodies) != 2 {
		t.Fatalf("expected 2 captured bodies, got %d", len(capturedBodies))
	}

	body1 := capturedBodies[0]
	body2 := capturedBodies[1]

	// 1. Verify system[0] billing header version and buildHash match across turns
	sys1 := gjson.GetBytes(body1, "system.0.text").String()
	sys2 := gjson.GetBytes(body2, "system.0.text").String()
	ver1 := extractTag(sys1, "cc_version=")
	ver2 := extractTag(sys2, "cc_version=")
	if ver1 != ver2 {
		t.Fatalf("cc_version buildHash changed across turns: turn1=%q turn2=%q (busts prompt cache prefix)", ver1, ver2)
	}

	// 2. Verify system[1] CLI identity block matches across turns
	agent1 := gjson.GetBytes(body1, "system.1").Raw
	agent2 := gjson.GetBytes(body2, "system.1").Raw
	if agent1 != agent2 {
		t.Fatalf("system.1 identity block differs across turns:\nTurn 1: %s\nTurn 2: %s", agent1, agent2)
	}

	// 3. Verify messages[0] (the first user message anchor) matches across turns
	msg1User := gjson.GetBytes(body1, "messages.0").Raw
	msg2User := gjson.GetBytes(body2, "messages.0").Raw
	if msg1User != msg2User {
		t.Fatalf("messages.0 differs across turns:\nTurn 1: %s\nTurn 2: %s", msg1User, msg2User)
	}

	// 4. Verify messages[1] (the relocated caller system instructions) matches across turns
	msg1Sys := gjson.GetBytes(body1, "messages.1").Raw
	msg2Sys := gjson.GetBytes(body2, "messages.1").Raw
	if msg1Sys != msg2Sys {
		t.Fatalf("messages.1 differs across turns:\nTurn 1: %s\nTurn 2: %s", msg1Sys, msg2Sys)
	}

	// 5. Verify cc_prompt_id matches across turns
	prompt1 := extractTag(sys1, "cc_prompt_id=")
	prompt2 := extractTag(sys2, "cc_prompt_id=")
	if prompt1 == "" || prompt1 != prompt2 {
		t.Fatalf("cc_prompt_id differs across turns: turn1=%q turn2=%q", prompt1, prompt2)
	}

	// 6. Verify cc_prev_req is absent on both turns for standard cloaked clients
	if strings.Contains(sys1, "cc_prev_req=") || strings.Contains(sys2, "cc_prev_req=") {
		t.Fatalf("cc_prev_req unexpectedly present in cloaked turn: turn1=%s turn2=%s", sys1, sys2)
	}

	// 7. Verify repeated turn 2 produces 100% byte-identical request body
	if _, err := exec.Execute(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(turn2Payload)}, options); err != nil {
		t.Fatalf("turn 2 repeat failed: %v", err)
	}
	if len(capturedBodies) != 3 {
		t.Fatalf("expected 3 captured bodies, got %d", len(capturedBodies))
	}
	if !bytes.Equal(capturedBodies[1], capturedBodies[2]) {
		t.Fatalf("repeated multi-turn request body differs:\nTurn 2:        %s\nTurn 2 repeat: %s", string(capturedBodies[1]), string(capturedBodies[2]))
	}

	// 8. Verify Turn 3 maintains identical prompt cache prefix and repeated Turn 3 is identical
	turn3Payload := fmt.Sprintf(`{
		"model": "claude-opus-5",
		"system": [{"type": "text", "text": %q, "cache_control": {"type": "ephemeral"}}],
		"messages": [
			{"role": "user", "content": "turn 1 user prompt"},
			{"role": "assistant", "content": "Understood."},
			{"role": "user", "content": "turn 2 user prompt with new question"},
			{"role": "assistant", "content": "Here is the answer to question 2."},
			{"role": "user", "content": "turn 3 user prompt asking for more"}
		],
		"max_tokens": 100
	}`, systemPrompt)

	if _, err := exec.Execute(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(turn3Payload)}, options); err != nil {
		t.Fatalf("turn 3 failed: %v", err)
	}
	if _, err := exec.Execute(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(turn3Payload)}, options); err != nil {
		t.Fatalf("turn 3 repeat failed: %v", err)
	}

	if len(capturedBodies) != 5 {
		t.Fatalf("expected 5 captured bodies, got %d", len(capturedBodies))
	}

	body3 := capturedBodies[3]
	body3Repeat := capturedBodies[4]

	sys3 := gjson.GetBytes(body3, "system.0.text").String()
	ver3 := extractTag(sys3, "cc_version=")
	if ver3 != ver1 {
		t.Fatalf("turn 3 cc_version differs: turn1=%q turn3=%q", ver1, ver3)
	}
	prompt3 := extractTag(sys3, "cc_prompt_id=")
	if prompt3 != prompt1 {
		t.Fatalf("turn 3 cc_prompt_id differs: turn1=%q turn3=%q", prompt1, prompt3)
	}
	if strings.Contains(sys3, "cc_prev_req=") {
		t.Fatalf("turn 3 cc_prev_req unexpectedly present: %s", sys3)
	}
	if !bytes.Equal(body3, body3Repeat) {
		t.Fatalf("repeated turn 3 body differs:\nTurn 3:        %s\nTurn 3 repeat: %s", string(body3), string(body3Repeat))
	}
}

// TestClaudeCloakedStreamingPrefixStability asserts that streaming requests also produce
// identical upstream bodies on repeated requests, including fully draining the stream.
func TestClaudeCloakedStreamingPrefixStability(t *testing.T) {
	var capturedBodies [][]byte
	call := 0
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		capturedBodies = append(capturedBodies, body)
		call++
		sse := fmt.Sprintf("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_%d\",\"type\":\"message\",\"model\":\"claude-opus-5\",\"role\":\"assistant\",\"content\":[]}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", call)
		header := make(http.Header)
		header.Set("Content-Type", "text/event-stream")
		header.Set("request-id", fmt.Sprintf("req_upstream_%d", call))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(sse)),
			Request:    req,
		}, nil
	})

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	auth := &cliproxyauth.Auth{
		ID:         "test-cloaked-stream-oauth",
		Attributes: map[string]string{"api_key": "sk-ant-oat-test-stream-oauth-key"},
		Metadata: map[string]any{
			"account_uuid": "11111111-2222-4333-8444-555555555555",
			claudeauth.ClaudeDeviceIDsMetadataKey: []string{
				"0000000000000000000000000000000000000000000000000000000000000001",
			},
		},
	}
	exec := NewClaudeExecutor(&config.Config{})
	options := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Stream:       true,
	}

	payload1 := `{
		"model": "claude-opus-5",
		"system": [{"type": "text", "text": "system instructions", "cache_control": {"type": "ephemeral"}}],
		"messages": [{"role": "user", "content": "Say OK."}],
		"max_tokens": 100
	}`

	resp1, err1 := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(payload1)}, options)
	if err1 != nil {
		t.Fatalf("stream 1 failed: %v", err1)
	}
	// Fully drain stream 1 chunks to trigger completion and commit continuity state
	if resp1 != nil && resp1.Chunks != nil {
		for range resp1.Chunks {
		}
	}

	resp2, err2 := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(payload1)}, options)
	if err2 != nil {
		t.Fatalf("stream 2 failed: %v", err2)
	}
	if resp2 != nil && resp2.Chunks != nil {
		for range resp2.Chunks {
		}
	}

	if len(capturedBodies) != 2 {
		t.Fatalf("expected 2 captured bodies, got %d", len(capturedBodies))
	}

	if !bytes.Equal(capturedBodies[0], capturedBodies[1]) {
		t.Fatalf("streaming request bodies differ:\nBody 1: %s\nBody 2: %s", string(capturedBodies[0]), string(capturedBodies[1]))
	}
}

// TestClaudeCloakedOpenAIFormatPrefixStability verifies that requests arriving in OpenAI format
// (such as from LiteLLM or OpenAI client SDKs) translated to Claude produce 100% byte-identical
// upstream request bodies on repeated requests.
func TestClaudeCloakedOpenAIFormatPrefixStability(t *testing.T) {
	var capturedBodies [][]byte
	call := 0
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		capturedBodies = append(capturedBodies, body)
		call++
		sse := fmt.Sprintf("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_%d\",\"type\":\"message\",\"model\":\"claude-opus-5\",\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"OK\"}]}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", call)
		header := make(http.Header)
		header.Set("Content-Type", "text/event-stream")
		header.Set("request-id", fmt.Sprintf("req_upstream_%d", call))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(sse)),
			Request:    req,
		}, nil
	})

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	auth := &cliproxyauth.Auth{
		ID:         "test-cloaked-openai-oauth",
		Attributes: map[string]string{"api_key": "sk-ant-oat-test-openai-key"},
		Metadata: map[string]any{
			"account_uuid": "11111111-2222-4333-8444-555555555555",
			claudeauth.ClaudeDeviceIDsMetadataKey: []string{
				"0000000000000000000000000000000000000000000000000000000000000001",
			},
		},
	}
	exec := NewClaudeExecutor(&config.Config{})
	options := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
	}

	openaiPayload := []byte(`{
		"model": "claude-opus-5",
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "Say OK."}
		]
	}`)

	req1 := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: openaiPayload}
	if _, err := exec.Execute(ctx, auth, req1, options); err != nil {
		t.Fatalf("openai req 1 failed: %v", err)
	}

	req2 := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: openaiPayload}
	if _, err := exec.Execute(ctx, auth, req2, options); err != nil {
		t.Fatalf("openai req 2 failed: %v", err)
	}

	if len(capturedBodies) != 2 {
		t.Fatalf("expected 2 captured bodies, got %d", len(capturedBodies))
	}

	if !bytes.Equal(capturedBodies[0], capturedBodies[1]) {
		t.Fatalf("openai format upstream bodies differ:\nBody 1: %s\nBody 2: %s", string(capturedBodies[0]), string(capturedBodies[1]))
	}
}

// TestClaudeCloakedToolContinuationPreservesExplicitPromptID verifies that when a turn
// is initiated with an explicit cc_prompt_id, subsequent tool_result continuations that omit
// billing tags preserve that exact explicit prompt ID from continuity state.
func TestClaudeCloakedToolContinuationPreservesExplicitPromptID(t *testing.T) {
	var capturedBodies [][]byte
	call := 0
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		capturedBodies = append(capturedBodies, body)
		call++
		response := fmt.Sprintf(`{"id":"msg_%d","type":"message","model":"claude-opus-5","role":"assistant","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":10,"output_tokens":2}}`, call)
		header := make(http.Header)
		header.Set("Content-Type", "application/json")
		header.Set("request-id", fmt.Sprintf("req_upstream_%d", call))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(response)),
			Request:    req,
		}, nil
	})

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	auth := &cliproxyauth.Auth{
		ID:         "test-cloaked-explicit-prompt-oauth",
		Attributes: map[string]string{"api_key": "sk-ant-oat-test-explicit-prompt-key"},
		Metadata: map[string]any{
			"account_uuid": "11111111-2222-4333-8444-555555555555",
			claudeauth.ClaudeDeviceIDsMetadataKey: []string{
				"0000000000000000000000000000000000000000000000000000000000000001",
			},
		},
	}
	exec := NewClaudeExecutor(&config.Config{})
	options := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Headers:      http.Header{"Session-Id": []string{"sess-explicit-tool-1"}},
	}

	explicitUUID := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	turn1Payload := []byte(`{
		"model": "claude-opus-5",
		"system": [
			{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.258.000; cc_entrypoint=cli; cch=00000; cc_prompt_id=` + explicitUUID + `;"}
		],
		"messages": [{"role": "user", "content": "run tool"}]
	}`)

	if _, err := exec.Execute(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: turn1Payload}, options); err != nil {
		t.Fatalf("turn 1 failed: %v", err)
	}

	// Turn 1.1 tool result continuation (without any billing header)
	turn1ToolPayload := []byte(`{
		"model": "claude-opus-5",
		"messages": [
			{"role": "user", "content": "run tool"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "t1", "name": "bash", "input": {}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "content": "output"}]}
		]
	}`)

	if _, err := exec.Execute(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: turn1ToolPayload}, options); err != nil {
		t.Fatalf("turn 1.1 failed: %v", err)
	}

	if len(capturedBodies) != 2 {
		t.Fatalf("expected 2 captured bodies, got %d", len(capturedBodies))
	}

	sysTool := gjson.GetBytes(capturedBodies[1], "system.0.text").String()
	promptTool := extractTag(sysTool, "cc_prompt_id=")
	if promptTool != explicitUUID {
		t.Fatalf("tool continuation lost explicit prompt ID: got %q, want %q", promptTool, explicitUUID)
	}
}

// TestClaudeCloakedColdStartToolContinuationUsesDeterministicPromptID verifies that when
// a tool continuation arrives on a cold start (no prior committed session state, e.g. after a failure),
// it deterministically resolves promptID based on the conversation anchor rather than
// adopting an uncommitted random UUID.
func TestClaudeCloakedColdStartToolContinuationUsesDeterministicPromptID(t *testing.T) {
	var capturedBodies [][]byte
	call := 0
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		capturedBodies = append(capturedBodies, body)
		call++
		header := make(http.Header)
		header.Set("Content-Type", "application/json")
		header.Set("request-id", fmt.Sprintf("req_upstream_%d", call))

		// First call fails (e.g. 500 error, uncommitted state)
		if call == 1 {
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Header:     header,
				Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"api_error","message":"temporary failure"}}`)),
				Request:    req,
			}, nil
		}

		response := fmt.Sprintf(`{"id":"msg_%d","type":"message","model":"claude-opus-5","role":"assistant","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":10,"output_tokens":2}}`, call)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(response)),
			Request:    req,
		}, nil
	})

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	auth := &cliproxyauth.Auth{
		ID:         "test-cloaked-cold-start-tool-oauth",
		Attributes: map[string]string{"api_key": "sk-ant-oat-test-cold-start-key"},
		Metadata: map[string]any{
			"account_uuid": "11111111-2222-4333-8444-555555555555",
			claudeauth.ClaudeDeviceIDsMetadataKey: []string{
				"0000000000000000000000000000000000000000000000000000000000000001",
			},
		},
	}
	exec := NewClaudeExecutor(&config.Config{})
	options := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
	}

	// Cold start tool continuation turn: ending in tool_result without preceding request on this instance
	toolPayload := []byte(`{
		"model": "claude-opus-5",
		"messages": [
			{"role": "user", "content": "run tool"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "t1", "name": "bash", "input": {}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "content": "output"}]}
		]
	}`)

	req1 := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: toolPayload}
	// First execution fails (uncommitted)
	_, _ = exec.Execute(ctx, auth, req1, options)

	// Repeated cold start tool continuation retry
	req2 := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: toolPayload}
	if _, err := exec.Execute(ctx, auth, req2, options); err != nil {
		t.Fatalf("cold start tool continuation retry failed: %v", err)
	}

	if len(capturedBodies) != 2 {
		t.Fatalf("expected 2 captured bodies, got %d", len(capturedBodies))
	}

	sys1 := gjson.GetBytes(capturedBodies[0], "system.0.text").String()
	sys2 := gjson.GetBytes(capturedBodies[1], "system.0.text").String()
	p1 := extractTag(sys1, "cc_prompt_id=")
	p2 := extractTag(sys2, "cc_prompt_id=")
	if p1 == "" || p1 != p2 {
		t.Fatalf("cold start tool continuation prompt IDs differ between failure and retry: %q != %q", p1, p2)
	}
}
