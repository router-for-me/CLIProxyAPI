package auth

import (
	"context"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// withStreamUsageLimit applies the usage-limit response mode to a stream whose
// response may already be committed: when a 429 ends the stream and every
// eligible credential is then out of quota with a reset more than
// usageLimitMinResetIn away, the stream's final error becomes the usage-limit
// cooldown, so the handler's terminal error event carries the reset. It stays
// retryable while the Antigravity credits fallback can still serve the next
// request, as a request that fails before any output tries that fallback first.
// The failure itself is still recorded once, by wrapStreamResult.
func (m *Manager) withStreamUsageLimit(ctx context.Context, providers []string, model, routeModel string, opts cliproxyexecutor.Options, result *cliproxyexecutor.StreamResult) *cliproxyexecutor.StreamResult {
	if !QuotaCooldownUsageLimitModeEnabled() || m.HomeEnabled() {
		return result
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				if usageLimitErr := m.usageLimitResponseError(ctx, providers, model, opts, chunk.Err); usageLimitErr != nil && !m.antigravityCreditsCanServe(ctx, providers, routeModel, opts, chunk.Err) {
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

// antigravityCreditsCanServe reports whether the Antigravity credits fallback
// would run after err and has a credential to try for routeModel.
func (m *Manager) antigravityCreditsCanServe(ctx context.Context, providers []string, routeModel string, opts cliproxyexecutor.Options, err error) bool {
	if !hasAntigravityProvider(providers) || !shouldAttemptAntigravityCreditsFallback(m, err, providers) {
		return false
	}
	candidates, errCandidates := m.findAllAntigravityCreditsCandidateAuths(ctx, routeModel, opts)
	return errCandidates == nil && len(candidates) > 0
}
