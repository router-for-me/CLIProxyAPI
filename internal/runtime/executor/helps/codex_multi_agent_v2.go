package helps

import (
	"context"
	"net/http"

	multiagentv2 "github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/optimize-multi-agent-v2"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// RewriteCodexSpawnAgentDescription optimizes spawn_agent definitions for
// official Codex clients when multi-agent v2 optimization is enabled.
func RewriteCodexSpawnAgentDescription(ctx context.Context, headers http.Header, payload []byte, cfg *config.Config) []byte {
	return multiagentv2.RewriteCodexSpawnAgentDescription(ctx, headers, payload, cfg)
}

// RewriteCodexMultiAgentV2Input converts official Codex multi-agent input into
// standard Responses API messages when multi-agent v2 optimization is enabled.
func RewriteCodexMultiAgentV2Input(ctx context.Context, headers http.Header, payload []byte, cfg *config.Config) []byte {
	return multiagentv2.RewriteCodexMultiAgentV2Input(ctx, headers, payload, cfg)
}

// TranslateRequestWithCodexMultiAgentV2 normalizes official Codex multi-agent
// input before translating it to a non-Codex target protocol.
func TranslateRequestWithCodexMultiAgentV2(ctx context.Context, headers http.Header, cfg *config.Config, from, to sdktranslator.Format, model string, payload []byte, stream bool) []byte {
	return multiagentv2.TranslateRequestWithCodexMultiAgentV2(ctx, headers, cfg, from, to, model, payload, stream)
}

// OptimizeCodexMultiAgentV2Request rewrites an eligible spawn_agent request and
// reports whether the collaboration namespace was renamed for upstream use.
func OptimizeCodexMultiAgentV2Request(ctx context.Context, headers http.Header, payload []byte, cfg *config.Config) ([]byte, bool) {
	return multiagentv2.OptimizeCodexMultiAgentV2Request(ctx, headers, payload, cfg)
}

// RestoreCodexMultiAgentV2Response restores optimized collaboration namespace
// values before an upstream response is translated and returned to the client.
func RestoreCodexMultiAgentV2Response(payload []byte, optimized bool) []byte {
	return multiagentv2.RestoreCodexMultiAgentV2Response(payload, optimized)
}
