package handlers

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// TestPluginExecutorRequestCarriesModelAction guards native model actions
// (embedContent, batchEmbedContents, decisions) for plugin-routed requests: the
// plugin only sees the wildcard route path, so the action must travel in metadata.
func TestPluginExecutorRequestCarriesModelAction(t *testing.T) {
	h := &BaseAPIHandler{}
	for _, action := range []string{"embedContent", "batchEmbedContents", "decisions"} {
		t.Run(action, func(t *testing.T) {
			req, opts := h.pluginExecutorRequest(context.Background(), "gemini", "gemini", "gemini-embedding-001", "gemini-embedding-001",
				[]byte(`{"requests":[]}`), "", false, modelExecutionOptions{Action: action, Path: "/v1beta/models/gemini-embedding-001:" + action})
			if got, _ := req.Metadata[coreexecutor.RequestActionMetadataKey].(string); got != action {
				t.Fatalf("req.Metadata[action] = %q, want %q", got, action)
			}
			if got, _ := opts.Metadata[coreexecutor.RequestActionMetadataKey].(string); got != action {
				t.Fatalf("opts.Metadata[action] = %q, want %q", got, action)
			}
			if got, _ := req.Metadata[coreexecutor.RequestPathMetadataKey].(string); got == "" {
				t.Fatal("request path metadata must still be recorded")
			}
		})
	}
}

func TestPluginExecutorRequestOmitsActionWhenAbsent(t *testing.T) {
	h := &BaseAPIHandler{}
	req, opts := h.pluginExecutorRequest(context.Background(), "gemini", "gemini", "gemini-2.5-pro", "gemini-2.5-pro",
		[]byte(`{"contents":[]}`), "", false, modelExecutionOptions{})
	if _, exists := req.Metadata[coreexecutor.RequestActionMetadataKey]; exists {
		t.Fatal("chat requests must not gain action metadata")
	}
	if _, exists := opts.Metadata[coreexecutor.RequestActionMetadataKey]; exists {
		t.Fatal("chat requests must not gain action metadata on options")
	}
}

// TestReapplyRequestActionMetadataSurvivesInterceptorRebuild covers the case the
// plugin path relies on: an interceptor rebuild drops metadata and the action is restored.
func TestReapplyRequestActionMetadataSurvivesInterceptorRebuild(t *testing.T) {
	rebuilt := coreexecutor.Request{Model: "gemini-decision-1", Payload: []byte(`{}`), Metadata: map[string]any{"other": "value"}}
	got := reapplyRequestActionMetadata(rebuilt, "decisions")
	if action, _ := got.Metadata[coreexecutor.RequestActionMetadataKey].(string); action != "decisions" {
		t.Fatalf("action = %q, want decisions", action)
	}
	if got.Metadata["other"] != "value" {
		t.Fatal("existing metadata must be preserved")
	}
	if again := reapplyRequestActionMetadata(got, "decisions"); again.Metadata[coreexecutor.RequestActionMetadataKey] != "decisions" {
		t.Fatal("reapplying an identical action must be a no-op")
	}
}

// TestParsePluginExecutorActionResponseUsageDecisions guards usage accounting for a
// decisions response returned directly by an executor plugin: the protocol is gemini,
// but the payload uses the snake_case top-level "usage" shape.
func TestParsePluginExecutorActionResponseUsageDecisions(t *testing.T) {
	payload := []byte(`{"answers":{"is_damaged":{"type":"yes_no","answer":true}},"usage":{"total_input_tokens":42,"total_output_tokens":0,"total_tokens":42}}`)

	detail := parsePluginExecutorActionResponseUsage("gemini", helps.GeminiDecisionsAction, payload)
	if detail.InputTokens != 42 || detail.TotalTokens != 42 {
		t.Fatalf("decisions usage = %+v, want input/total 42", detail)
	}
	if generic := parsePluginExecutorResponseUsage("gemini", payload); generic.InputTokens != 0 {
		t.Fatalf("the gemini chat parser must not read decisions usage, got %+v", generic)
	}
}

// TestParsePluginExecutorActionResponseUsageFallsBack keeps chat-shaped plugin responses
// on the existing parser even when an action is recorded.
func TestParsePluginExecutorActionResponseUsageFallsBack(t *testing.T) {
	payload := []byte(`{"candidates":[{"content":{"parts":[{"text":"hi"}]}}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10}}`)
	detail := parsePluginExecutorActionResponseUsage("gemini", helps.GeminiDecisionsAction, payload)
	if detail.InputTokens != 7 || detail.OutputTokens != 3 {
		t.Fatalf("fallback usage = %+v, want 7/3", detail)
	}
}

func TestParsePluginExecutorActionResponseUsageWithoutAction(t *testing.T) {
	payload := []byte(`{"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2,"totalTokenCount":7}}`)
	detail := parsePluginExecutorActionResponseUsage("gemini", "", payload)
	if detail.InputTokens != 5 || detail.OutputTokens != 2 {
		t.Fatalf("usage = %+v, want 5/2", detail)
	}
}
