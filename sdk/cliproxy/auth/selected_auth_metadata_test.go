package auth

import (
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestPublishSelectedAuthMetadataIncludesStableIndex(t *testing.T) {
	auth := &Auth{
		ID:       "auth-1",
		Provider: "codex",
		FileName: "auth-1.json",
	}
	selectedAuthID := ""
	selectedAuthIndex := ""
	meta := map[string]any{
		cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(authID string) {
			selectedAuthID = authID
		},
		cliproxyexecutor.SelectedAuthIndexCallbackMetadataKey: func(authIndex string) {
			selectedAuthIndex = authIndex
		},
	}

	publishSelectedAuthMetadata(meta, auth)

	if selectedAuthID != auth.ID {
		t.Fatalf("selected auth ID = %q, want %q", selectedAuthID, auth.ID)
	}
	if selectedAuthIndex == "" || selectedAuthIndex != auth.Index {
		t.Fatalf("selected auth index = %q, want %q", selectedAuthIndex, auth.Index)
	}
	if got := meta[cliproxyexecutor.SelectedAuthMetadataKey]; got != auth.ID {
		t.Fatalf("selected auth metadata = %#v, want %q", got, auth.ID)
	}
	if got := meta[cliproxyexecutor.SelectedAuthIndexMetadataKey]; got != auth.Index {
		t.Fatalf("selected auth index metadata = %#v, want %q", got, auth.Index)
	}
}

func TestPublishSelectedAuthMetadataReportsCredentialKind(t *testing.T) {
	for _, test := range []struct {
		name string
		auth *Auth
		want string
	}{
		{name: "api key", auth: &Auth{ID: "key-1", Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "k"}}, want: AuthKindAPIKey},
		{name: "login", auth: &Auth{ID: "login-1", Provider: "claude", Attributes: map[string]string{AttributeAuthKind: "oauth"}}, want: AuthKindOAuth},
	} {
		t.Run(test.name, func(t *testing.T) {
			kind := "not called"
			meta := map[string]any{
				cliproxyexecutor.SelectedAuthKindCallbackMetadataKey: func(value string) { kind = value },
			}
			publishSelectedAuthMetadata(meta, test.auth)
			if kind != test.want {
				t.Fatalf("selected auth kind = %q, want %q", kind, test.want)
			}
		})
	}
}
