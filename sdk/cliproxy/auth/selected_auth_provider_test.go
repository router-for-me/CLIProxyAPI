package auth

import (
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestPublishSelectedAuthMetadataReportsProvider(t *testing.T) {
	var got string
	meta := map[string]any{cliproxyexecutor.SelectedAuthProviderCallbackMetadataKey: func(provider string) { got = provider }}
	publishSelectedAuthMetadata(meta, &Auth{ID: "auth-1", Provider: "antigravity"})
	if got != "antigravity" {
		t.Fatalf("provider callback = %q, want antigravity", got)
	}

	got = ""
	publishSelectedAuthMetadata(map[string]any{cliproxyexecutor.SelectedAuthProviderCallbackMetadataKey: func(provider string) { got = provider }}, &Auth{ID: "auth-2"})
	if got != "" {
		t.Fatalf("provider callback = %q for an auth without provider", got)
	}
}
