package translator

import "context"

type codexToolArgumentNormalizationKey struct{}

// WithCodexToolArgumentNormalization selects the Codex integer-argument
// compatibility policy for responses translated with ctx. HTTP handlers select
// it from the inbound client identity; direct SDK callers can explicitly opt in.
// Without this policy, Registry leaves numeric representations unchanged.
func WithCodexToolArgumentNormalization(ctx context.Context, enabled bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, codexToolArgumentNormalizationKey{}, enabled)
}

// NormalizeCodexToolArgumentsForClient applies the request-scoped Codex policy
// to a Responses payload. Native transports that bypass Registry use this same
// boundary; without explicit opt-in the original payload is returned.
func NormalizeCodexToolArgumentsForClient(ctx context.Context, body []byte, stream bool) []byte {
	if !codexToolArgumentNormalizationEnabled(ctx) {
		return body
	}
	return CanonicalizeCodexToolArguments(body, stream)
}

func codexToolArgumentNormalizationEnabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	enabled, _ := ctx.Value(codexToolArgumentNormalizationKey{}).(bool)
	return enabled
}
