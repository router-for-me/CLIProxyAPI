package executor

import (
	"context"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/claudeoauth"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func (e *ClaudeExecutor) applyClaudeOAuthFingerprintGate(ctx context.Context, auth *cliproxyauth.Auth, apiKey string, inboundHeaders http.Header, body []byte, baseModel string) ([]byte, *helps.ClaudeOAuthFingerprintGateResult, context.Context, error) {
	if !helps.ClaudeOAuthFingerprintEnabled(e.cfg, apiKey) {
		return body, nil, ctx, nil
	}
	out, result, err := helps.ClaudeOAuthFingerprintGate(ctx, e.cfg, auth, inboundHeaders, body, baseModel)
	if result != nil {
		ctx = helps.ContextWithClaudeOAuthFingerprint(ctx, result)
	}
	if err != nil {
		return out, result, ctx, statusErr{code: helps.ClaudeOAuthFingerprintHTTPStatus(err), msg: err.Error()}
	}
	return out, result, ctx, nil
}

func (e *ClaudeExecutor) applyClaudeOAuthStableFingerprintBody(auth *cliproxyauth.Auth, apiKey string, body []byte) []byte {
	if !helps.ClaudeOAuthFingerprintEnabled(e.cfg, apiKey) || !claudeoauth.OverrideDevice(e.cfg) || !claudeoauth.IsClaudeOAuthAuth(auth) {
		return body
	}
	return normalizeClaudeOAuthStableBillingHeader(body)
}
