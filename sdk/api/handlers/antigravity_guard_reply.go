package handlers

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/MoeForge/moego/json"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Antigravity answers flagged prompt content with a signal-less 429 (see
// internal/config/antigravity_guard_defaults.go). The bundled guard plugin
// learns from that 429 and answers later matches locally with a fixed message.
// antigravityGuardReply gives the first rejected request the same message.
// Callers apply it after the request lifecycle event, so the plugin still
// receives Google's original error.

const (
	antigravityGuardMessage = "您的请求中有谷歌标记的特征，请修改提示词后重试"
	antigravityGuardCode    = "google_flagged_prompt"
)

// selectedProviderRecorder keeps the provider of the last auth the auth
// manager selected for a request.
type selectedProviderRecorder struct {
	mu       sync.Mutex
	provider string
}

func (r *selectedProviderRecorder) set(provider string) {
	r.mu.Lock()
	r.provider = provider
	r.mu.Unlock()
}

func (r *selectedProviderRecorder) get() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.provider
}

// trackSelectedProvider registers a selected-provider callback in meta,
// chained before any callback already present. The provider is captured at
// selection time because the auth manager clones metadata and Home runtime
// auths are released after a failed attempt.
func trackSelectedProvider(meta map[string]any) *selectedProviderRecorder {
	if meta == nil {
		return nil
	}
	recorder := &selectedProviderRecorder{}
	previous, _ := meta[coreexecutor.SelectedAuthProviderCallbackMetadataKey].(func(string))
	meta[coreexecutor.SelectedAuthProviderCallbackMetadataKey] = func(provider string) {
		recorder.set(provider)
		if previous != nil {
			previous(provider)
		}
	}
	return recorder
}

// antigravityGuardReply returns the fixed guard reply when the auth that
// produced msg is an Antigravity one, msg is Google's content-filter 429 and a
// request-scoped stop rule ended the request. Without that rule, e.g. after the
// explicit `antigravity: []` opt-out, and for every other error msg is
// returned unchanged.
func (h *BaseAPIHandler) antigravityGuardReply(ctx context.Context, entryProtocol, provider string, msg *interfaces.ErrorMessage) *interfaces.ErrorMessage {
	if msg == nil || msg.Error == nil || msg.DirectResponse || msg.StatusCode != http.StatusTooManyRequests ||
		!strings.EqualFold(provider, "antigravity") || !internalconfig.IsAntigravityContentFilter429(msg.Error.Error()) {
		return msg
	}
	if h == nil || h.AuthManager == nil || !h.AuthManager.StoppedByOAuthRequestScopedRule(ctx, provider, msg.Error) {
		return msg
	}
	return &interfaces.ErrorMessage{
		StatusCode:     http.StatusForbidden,
		Error:          msg.Error,
		DirectResponse: true,
		Body:           antigravityGuardBody(entryProtocol),
		Headers:        http.Header{"Content-Type": []string{"application/json"}},
	}
}

// antigravityGuardBody shapes the error like the client's protocol, matching
// the bundled plugin's local replies.
func antigravityGuardBody(entryProtocol string) []byte {
	var payload any
	switch protocol := strings.ToLower(entryProtocol); {
	case strings.Contains(protocol, "claude"):
		payload = map[string]any{"type": "error", "error": map[string]any{"type": "permission_error", "message": antigravityGuardMessage, "code": antigravityGuardCode}}
	case strings.Contains(protocol, "gemini"):
		payload = map[string]any{"error": map[string]any{"code": http.StatusForbidden, "message": antigravityGuardMessage, "status": "PERMISSION_DENIED", "reason": antigravityGuardCode}}
	default:
		payload = map[string]any{"error": map[string]any{"message": antigravityGuardMessage, "type": "invalid_request_error", "code": antigravityGuardCode}}
	}
	body, _ := json.Marshal(payload)
	return body
}
