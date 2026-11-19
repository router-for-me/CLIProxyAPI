package auth

import (
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// publishSelectedAuthProvider reports the provider of the selected auth to the
// optional SelectedAuthProviderCallbackMetadataKey callback.
func publishSelectedAuthProvider(meta map[string]any, auth *Auth) {
	callback, ok := meta[cliproxyexecutor.SelectedAuthProviderCallbackMetadataKey].(func(string))
	if !ok || callback == nil || auth == nil {
		return
	}
	if provider := strings.TrimSpace(auth.Provider); provider != "" {
		callback(provider)
	}
}
