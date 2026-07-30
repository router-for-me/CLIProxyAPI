package helps

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// ApplyRequestThinking preserves the registry lookup path unless the auth
// manager bound an exact configured API-key model definition to this attempt.
func ApplyRequestThinking(body []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, fromFormat, toFormat, provider string) ([]byte, error) {
	sourceBody := opts.OriginalRequest
	if len(sourceBody) == 0 {
		sourceBody = req.Payload
	}
	if modelInfo, ok := cliproxyauth.ResolvedAPIKeyModelInfo(req); ok {
		return thinking.ApplyThinkingWithModelInfo(body, sourceBody, req.Model, fromFormat, toFormat, provider, modelInfo)
	}
	return thinking.ApplyThinkingWithSource(body, sourceBody, req.Model, fromFormat, toFormat, provider)
}
