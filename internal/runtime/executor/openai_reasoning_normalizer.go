package executor

import (
	"github.com/router-for-me/CLIProxyAPI/v6/internal/requestinvariants"
)

func normalizeAssistantToolCallReasoningContent(body []byte, requireThinkingEnabled bool) ([]byte, int, error) {
	return requestinvariants.NormalizeOpenAIChatToolCallReasoning(body, requireThinkingEnabled)
}

func openAIChatReasoningEnabled(body []byte) bool {
	return requestinvariants.OpenAIChatReasoningEnabled(body)
}
