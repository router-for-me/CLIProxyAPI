package helps

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// ResolveAPIKeyModelInfo returns the exact configured model definition bound by
// the auth manager to this API-key execution attempt.
func ResolveAPIKeyModelInfo(auth *cliproxyauth.Auth, req cliproxyexecutor.Request) (*registry.ModelInfo, bool) {
	if auth == nil {
		return nil, false
	}
	return cliproxyauth.ResolvedAPIKeyModelInfo(req)
}

// ApplyRequestThinking preserves the existing registry path unless the auth
// manager selected an exact configured API-key model definition.
func ApplyRequestThinking(body []byte, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, fromFormat, toFormat, provider string) ([]byte, error) {
	if modelInfo, ok := ResolveAPIKeyModelInfo(auth, req); ok {
		sourceBody := opts.OriginalRequest
		if len(sourceBody) == 0 {
			sourceBody = req.Payload
		}
		return thinking.ApplyThinkingWithModelInfo(body, sourceBody, req.Model, fromFormat, toFormat, provider, modelInfo)
	}
	return thinking.ApplyThinking(body, req.Model, fromFormat, toFormat, provider)
}
