package handlers

import (
	"context"
	"testing"

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
