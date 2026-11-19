package handlers

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/MoeForge/moego/json"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
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

// selectedAuthRecorder keeps a copy of the last auth the auth manager selected
// for a request.
type selectedAuthRecorder struct {
	mu   sync.Mutex
	auth *coreauth.Auth
}

func (r *selectedAuthRecorder) set(auth *coreauth.Auth) {
	r.mu.Lock()
	r.auth = auth
	r.mu.Unlock()
}

func (r *selectedAuthRecorder) get() *coreauth.Auth {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.auth
}

// trackSelectedAuth registers a selected-auth snapshot callback in meta,
// chained before any callback already present. The auth is captured at
// selection time because the auth manager clones metadata and Home runtime
// auths are released after a failed attempt.
func trackSelectedAuth(meta map[string]any) *selectedAuthRecorder {
	if meta == nil {
		return nil
	}
	recorder := &selectedAuthRecorder{}
	previous, _ := meta[coreauth.SelectedAuthSnapshotCallbackMetadataKey].(func(*coreauth.Auth))
	meta[coreauth.SelectedAuthSnapshotCallbackMetadataKey] = func(auth *coreauth.Auth) {
		recorder.set(auth)
		if previous != nil {
			previous(auth)
		}
	}
	return recorder
}

// antigravityGuardReply returns the fixed guard reply when auth, the auth that
// produced msg, is an Antigravity one, msg is Google's content-filter 429 and
// a request-scoped stop rule of auth ended the request. Without such a rule,
// e.g. after the explicit `antigravity: []` opt-out or a per-credential
// override, and for every other error msg is returned unchanged.
func (h *BaseAPIHandler) antigravityGuardReply(ctx context.Context, entryProtocol string, auth *coreauth.Auth, msg *interfaces.ErrorMessage) *interfaces.ErrorMessage {
	if msg == nil || msg.Error == nil || msg.DirectResponse || msg.StatusCode != http.StatusTooManyRequests ||
		auth == nil || !strings.EqualFold(auth.Provider, "antigravity") || !internalconfig.IsAntigravityContentFilter429(msg.Error.Error()) {
		return msg
	}
	if h == nil || h.AuthManager == nil || !h.AuthManager.StoppedByRequestScopedRule(ctx, auth, msg.Error) {
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
