package helps

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// ApplyRequestThinking preserves the registry lookup path unless the auth
// manager bound an exact configured API-key model definition to this attempt.
func ApplyRequestThinking(body []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, fromFormat, toFormat, provider string) ([]byte, error) {
	originalSource := opts.OriginalRequest
	if len(originalSource) == 0 {
		originalSource = req.Payload
	}
	summaryConfig := translatedRequestSummaryConfig(body, req.Payload, originalSource, req.Model, fromFormat, toFormat)
	if modelInfo, ok := cliproxyauth.ResolvedAPIKeyModelInfo(req); ok {
		return thinking.ApplyThinkingWithModelInfoAndSummary(body, originalSource, req.Model, fromFormat, toFormat, provider, modelInfo, summaryConfig)
	}
	return thinking.ApplyThinkingWithSummary(body, req.Model, fromFormat, toFormat, provider, summaryConfig)
}
