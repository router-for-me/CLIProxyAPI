package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestWithPromptCacheKeyLabels_LabelsOnceAndReachesUsageContext(t *testing.T) {
	payload := []byte(`{"model":"gpt-5.4","messages":[{"role":"system","content":"s"},{"role":"user","content":"u"}]}`)
	req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: payload}
	opts := withPromptCacheKeyLabels([]string{"codex"}, req, cliproxyexecutor.Options{})
	if got := cliproxyexecutor.PromptCacheKeySourceFromMetadata(opts.Metadata); got != cliproxyexecutor.PromptCacheKeySourceDerived {
		t.Fatalf("source = %q, want derived", got)
	}
	id := cliproxyexecutor.PromptCacheKeyIDFromMetadata(opts.Metadata)
	if len(id) != 16 {
		t.Fatalf("id = %q", id)
	}
	if _, ok := opts.Metadata["prompt_cache_key"]; ok {
		t.Fatalf("labels must never record the key itself: %#v", opts.Metadata)
	}

	// A second pass (retry, nested execution) keeps the first resolution.
	req.Payload = []byte(`{"model":"gpt-5.4","prompt_cache_key":"other","messages":[{"role":"user","content":"u"}]}`)
	again := withPromptCacheKeyLabels([]string{"codex"}, req, opts)
	if cliproxyexecutor.PromptCacheKeyIDFromMetadata(again.Metadata) != id {
		t.Fatalf("labels re-resolved: %#v", again.Metadata)
	}

	ctx := contextWithRequestedModelAlias(context.Background(), opts, "gpt-5.4")
	info := coreusage.PromptCacheKeyFromContext(ctx)
	if info.Source != cliproxyexecutor.PromptCacheKeySourceDerived || info.ID != id {
		t.Fatalf("usage context = %+v", info)
	}
}
