package cmd

import (
	"testing"
)

func TestRefreshExecutorCoversOAuthProviders(t *testing.T) {
	for _, p := range []string{"antigravity", "codex", "claude", "gemini", "kimi", "xai", "devin", "meta"} {
		if refreshExecutor(nil, p) == nil {
			t.Fatalf("%s has no refresher", p)
		}
	}
	if refreshExecutor(nil, "openai-compatibility") != nil {
		t.Fatal("an API-key provider was given a refresher")
	}
}
