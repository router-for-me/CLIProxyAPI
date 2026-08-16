package executor

import (
	"testing"

	cursorauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestResolveCursorModelEffortVariant(t *testing.T) {
	auth := &cliproxyauth.Auth{Metadata: map[string]any{cursorauth.ModelCacheKey: []cursorauth.ModelDetails{
		{ID: "gpt-test"}, {ID: "gpt-test-low"}, {ID: "gpt-test-high"},
	}}}
	model, err := resolveCursorModel(auth, "gpt-test-low", "high")
	if err != nil {
		t.Fatalf("resolveCursorModel() error = %v", err)
	}
	if model != "gpt-test-high" {
		t.Fatalf("model = %q", model)
	}
	if _, err = resolveCursorModel(auth, "gpt-test", "xhigh"); err == nil {
		t.Fatal("missing effort variant was accepted")
	}
}
