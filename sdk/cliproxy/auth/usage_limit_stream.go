package auth

import (
	"context"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// withStreamUsageLimit applies the usage-limit response mode to a stream whose
// response may already be committed: when a 429 ends the stream and every
// eligible credential is then out of quota with a reset more than
// usageLimitMinResetIn away, the stream's final error becomes the usage-limit
// cooldown, so the handler's terminal error event carries the reset. The
// failure itself is still recorded once, by wrapStreamResult.
func (m *Manager) withStreamUsageLimit(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, result *cliproxyexecutor.StreamResult) *cliproxyexecutor.StreamResult {
	if !QuotaCooldownUsageLimitModeEnabled() || m.HomeEnabled() {
		return result
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				if usageLimitErr := m.usageLimitResponseError(ctx, providers, model, opts, chunk.Err); usageLimitErr != nil {
					chunk.Err = usageLimitErr
				}
			}
			select {
			case out <- chunk:
			case <-ctx.Done():
				discardStreamChunks(result.Chunks)
				return
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: result.Headers, Chunks: out}
}
