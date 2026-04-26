package translator

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/requestinvariants"
)

func defaultRequestInvariantMiddleware(ctx context.Context, req RequestEnvelope, next RequestHandler) (RequestEnvelope, error) {
	translated, err := next(ctx, req)
	if err != nil {
		return translated, err
	}
	translated.Body = NormalizeRequestInvariants(req.Format, translated.Format, translated.Body)
	return translated, nil
}

// NormalizeRequestInvariants applies target-protocol request invariants to an
// already translated payload. It is safe to call multiple times.
func NormalizeRequestInvariants(from, to Format, body []byte) []byte {
	if len(body) == 0 {
		return body
	}

	out := body
	switch to {
	case FormatOpenAI:
		if normalized, _, err := requestinvariants.NormalizeOpenAIChatToolCallReasoning(out, true); err == nil {
			out = normalized
		}
	case FormatClaude:
		if normalized, _, err := requestinvariants.NormalizeClaudeMessagesToolUseReasoningPrefix(out, true); err == nil {
			out = normalized
		}
	}
	return out
}
