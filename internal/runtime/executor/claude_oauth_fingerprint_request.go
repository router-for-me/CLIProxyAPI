package executor

import (
	"context"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/claudeoauth"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	claudeOAuthStableBillingVersion    = "2.1.195"
	claudeOAuthStableBillingEntrypoint = "cli"
	claudeOAuthStableBetas             = "claude-code-20250219,context-1m-2025-08-07,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,effort-2025-11-24,oauth-2025-04-20"
)

type claudeOAuthFingerprintRequest struct {
	executor       *ClaudeExecutor
	auth           *cliproxyauth.Auth
	apiKey         string
	inboundHeaders http.Header
	sessionPayload []byte
	baseModel      string
	result         *helps.ClaudeOAuthFingerprintGateResult
	enabled        bool
}

func (e *ClaudeExecutor) newClaudeOAuthFingerprintRequest(auth *cliproxyauth.Auth, apiKey string, inboundHeaders http.Header, sessionPayload []byte, baseModel string) claudeOAuthFingerprintRequest {
	return claudeOAuthFingerprintRequest{
		executor:       e,
		auth:           auth,
		apiKey:         apiKey,
		inboundHeaders: inboundHeaders,
		sessionPayload: sessionPayload,
		baseModel:      baseModel,
		enabled:        helps.ClaudeOAuthFingerprintEnabled(e.cfg, apiKey),
	}
}

func (e *ClaudeExecutor) ShouldPrepareRequestAuth(auth *cliproxyauth.Auth) bool {
	if e == nil || !claudeoauth.Enabled(e.cfg) || !claudeoauth.IsClaudeOAuthAuth(auth) {
		return false
	}
	profile, ok := claudeoauth.ProfileFromAuth(auth)
	if !ok {
		return claudeoauth.GenerateMissingProfile(e.cfg)
	}
	_, changed, err := claudeoauth.NormalizeProfile(profile, e.cfg, claudeoauth.MetadataString(auth.Metadata, "account_uuid"))
	return err == nil && changed
}

func (e *ClaudeExecutor) PrepareRequestAuth(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	_ = ctx
	if e == nil || auth == nil || !e.ShouldPrepareRequestAuth(auth) {
		return auth, nil
	}
	updated := auth.Clone()
	changed, err := claudeoauth.EnsureAuthProfile(updated, e.cfg)
	if err != nil {
		return nil, err
	}
	if !changed {
		return nil, nil
	}
	return updated, nil
}

func (e *ClaudeExecutor) updateClaudeOAuthRefreshMetadata(auth *cliproxyauth.Auth, accountUUID string) error {
	if auth == nil {
		return nil
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	if accountUUID != "" {
		auth.Metadata["account_uuid"] = accountUUID
	}
	_, err := claudeoauth.EnsureAuthProfile(auth, e.cfg)
	return err
}

func (r *claudeOAuthFingerprintRequest) begin(ctx context.Context, body []byte) ([]byte, context.Context, error) {
	if r == nil || !r.enabled {
		return body, ctx, nil
	}
	out, result, nextCtx, err := r.executor.applyClaudeOAuthFingerprintGate(ctx, r.auth, r.apiKey, r.inboundHeaders, r.sessionPayload, body, r.baseModel)
	r.result = result
	if err != nil {
		return out, nextCtx, err
	}
	return r.stableBody(out), nextCtx, nil
}

func (r *claudeOAuthFingerprintRequest) finalizeBody(body []byte) []byte {
	if r == nil || !r.enabled {
		return body
	}
	body = r.stableBody(body)
	return removeClaudeBillingHeaderCCH(body)
}

func (r *claudeOAuthFingerprintRequest) stableBody(body []byte) []byte {
	if r == nil || !r.enabled {
		return body
	}
	if _, ok := helps.ClaudeOAuthProfileDeviceProfile(r.auth, r.executor.cfg); !ok {
		return body
	}
	return normalizeClaudeOAuthStableBillingHeader(body)
}

func (r *claudeOAuthFingerprintRequest) logOutbound(outboundHeaders http.Header, outboundBody []byte) {
	if r == nil || !r.enabled {
		return
	}
	helps.MaybeLogClaudeOAuthFingerprint(r.executor.cfg, r.auth, r.inboundHeaders, outboundHeaders, outboundBody, r.baseModel, r.result)
}

func claudeOAuthFingerprintBillingDefaults(cfg *config.Config, auth *cliproxyauth.Auth, oauthToken bool, version, entrypoint string) (string, string, bool) {
	if !oauthToken {
		return version, entrypoint, false
	}
	if _, ok := helps.ClaudeOAuthProfileDeviceProfile(auth, cfg); !ok {
		return version, entrypoint, false
	}
	return claudeOAuthStableBillingVersion, claudeOAuthStableBillingEntrypoint, true
}

func (e *ClaudeExecutor) applyClaudeOAuthFingerprintGate(ctx context.Context, auth *cliproxyauth.Auth, apiKey string, inboundHeaders http.Header, sessionPayload, body []byte, baseModel string) ([]byte, *helps.ClaudeOAuthFingerprintGateResult, context.Context, error) {
	if !helps.ClaudeOAuthFingerprintEnabled(e.cfg, apiKey) {
		return body, nil, ctx, nil
	}
	out, result, err := helps.ClaudeOAuthFingerprintGateWithSessionPayload(ctx, e.cfg, auth, inboundHeaders, sessionPayload, body, baseModel)
	if result != nil {
		ctx = helps.ContextWithClaudeOAuthFingerprint(ctx, result)
	}
	if err != nil {
		return out, result, ctx, statusErr{code: helps.ClaudeOAuthFingerprintHTTPStatus(err), msg: err.Error()}
	}
	return out, result, ctx, nil
}

func claudeOAuthStableBetasForModel(model string) string {
	if claudeOAuthModelUsesOpusBetas(model) {
		return claudeOAuthStableBetas
	}
	betas := strings.ReplaceAll(claudeOAuthStableBetas, ",context-1m-2025-08-07", "")
	return strings.ReplaceAll(betas, ",mid-conversation-system-2026-04-07", "")
}

func claudeOAuthModelUsesOpusBetas(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return false
	}
	return strings.Contains(model, "claude-opus-") || strings.HasPrefix(model, "opus-")
}

func normalizeClaudeOAuthStableBillingHeader(payload []byte) []byte {
	system := gjson.GetBytes(payload, "system")
	if !system.IsArray() {
		return payload
	}
	out := payload
	updatedAny := false
	system.ForEach(func(key, part gjson.Result) bool {
		billingHeader := part.Get("text").String()
		if !strings.HasPrefix(billingHeader, "x-anthropic-billing-header:") {
			return true
		}
		updatedHeader, updated := normalizeClaudeOAuthStableBillingHeaderText(billingHeader)
		if !updated {
			return true
		}
		next, errSet := sjson.SetBytes(out, "system."+key.String()+".text", updatedHeader)
		if errSet != nil {
			return true
		}
		out = next
		updatedAny = true
		return true
	})
	if !updatedAny {
		return payload
	}
	return out
}

func normalizeClaudeOAuthStableBillingHeaderText(billingHeader string) (string, bool) {
	updatedAny := false
	segments := strings.Split(billingHeader, ";")
	for i, segment := range segments {
		trimmed := strings.TrimSpace(segment)
		switch {
		case strings.HasPrefix(trimmed, "cc_version="):
			if replacement, ok := normalizeClaudeOAuthStableBillingVersionValue(strings.TrimPrefix(trimmed, "cc_version=")); ok {
				segments[i] = strings.Replace(segment, trimmed, "cc_version="+replacement, 1)
				updatedAny = true
			}
		case strings.HasPrefix(trimmed, "x-anthropic-billing-header: cc_version="):
			value := strings.TrimPrefix(trimmed, "x-anthropic-billing-header: cc_version=")
			if replacement, ok := normalizeClaudeOAuthStableBillingVersionValue(value); ok {
				segments[i] = strings.Replace(segment, trimmed, "x-anthropic-billing-header: cc_version="+replacement, 1)
				updatedAny = true
			}
		case strings.HasPrefix(trimmed, "cc_entrypoint="):
			segments[i] = strings.Replace(segment, trimmed, "cc_entrypoint="+claudeOAuthStableBillingEntrypoint, 1)
			updatedAny = true
		}
	}
	if !updatedAny {
		return billingHeader, false
	}
	return strings.Join(segments, ";"), true
}

func normalizeClaudeOAuthStableBillingVersionValue(value string) (string, bool) {
	value = strings.TrimSpace(value)
	lastDot := strings.LastIndex(value, ".")
	if lastDot < 0 || lastDot == len(value)-1 {
		return "", false
	}
	return claudeOAuthStableBillingVersion + value[lastDot:], true
}

func removeClaudeBillingHeaderCCH(body []byte) []byte {
	billingHeader := gjson.GetBytes(body, "system.0.text").String()
	if !strings.HasPrefix(billingHeader, "x-anthropic-billing-header:") {
		return body
	}
	updatedHeader, updated := removeClaudeBillingHeaderCCHText(billingHeader)
	if !updated {
		return body
	}
	out, err := sjson.SetBytes(body, "system.0.text", updatedHeader)
	if err != nil {
		return body
	}
	return out
}

func removeClaudeBillingHeaderCCHText(billingHeader string) (string, bool) {
	segments := strings.Split(billingHeader, ";")
	out := make([]string, 0, len(segments))
	updated := false
	for _, segment := range segments {
		if strings.HasPrefix(strings.TrimSpace(segment), "cch=") {
			updated = true
			continue
		}
		out = append(out, segment)
	}
	if !updated {
		return billingHeader, false
	}
	return strings.Join(out, ";"), true
}
