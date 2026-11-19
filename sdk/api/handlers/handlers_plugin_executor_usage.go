package handlers

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func parsePluginExecutorResponseUsage(protocol string, payload []byte) usage.Detail {
	return helps.ParsePluginExecutorResponseUsage(protocol, payload)
}

// parsePluginExecutorActionResponseUsage parses usage for a plugin executor response that
// belongs to a native, non-chat model action. Decisions responses expose snake_case
// totals under a top-level "usage" object, which the Gemini chat parser cannot read, so a
// plugin-routed decisions call would otherwise be recorded with zero tokens.
func parsePluginExecutorActionResponseUsage(protocol, action string, payload []byte) usage.Detail {
	if strings.EqualFold(strings.TrimSpace(action), helps.GeminiDecisionsAction) {
		if detail := helps.ParseGeminiDecisionsUsage(payload); detail.TotalTokens > 0 || detail.InputTokens > 0 || detail.OutputTokens > 0 {
			return detail
		}
	}
	return parsePluginExecutorResponseUsage(protocol, payload)
}

func observePluginExecutorStreamUsage(protocol string, payload []byte, buffer *helps.StreamUsageBuffer) {
	helps.ObservePluginExecutorStreamUsage(protocol, payload, buffer)
}

func observePluginExecutorStreamTTFT(protocol string, reporter *helps.UsageReporter, payload []byte) {
	helps.ObservePluginExecutorStreamTTFT(protocol, reporter, payload)
}
