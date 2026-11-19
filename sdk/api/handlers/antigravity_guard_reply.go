package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"

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

// selectedAuthRecorder keeps the last credential the auth manager selected.
type selectedAuthRecorder struct {
	mu sync.Mutex
	id string
}

func (r *selectedAuthRecorder) set(id string) {
	r.mu.Lock()
	r.id = id
	r.mu.Unlock()
}

func (r *selectedAuthRecorder) get() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.id
}

// trackSelectedAuth registers a selected-auth callback in meta, chained before
// any callback already present. The auth manager clones metadata, so the
// callback is the way to learn which credential served the request.
func trackSelectedAuth(meta map[string]any) *selectedAuthRecorder {
	if meta == nil {
		return nil
	}
	recorder := &selectedAuthRecorder{}
	previous, _ := meta[coreexecutor.SelectedAuthCallbackMetadataKey].(func(string))
	meta[coreexecutor.SelectedAuthCallbackMetadataKey] = func(authID string) {
		recorder.set(authID)
		if previous != nil {
			previous(authID)
		}
	}
	return recorder
}

// antigravityGuardReply returns the fixed guard reply when the credential that
// produced msg is an Antigravity one and msg is Google's content-filter 429.
// Every other error is returned unchanged.
func (h *BaseAPIHandler) antigravityGuardReply(entryProtocol string, selected *selectedAuthRecorder, msg *interfaces.ErrorMessage) *interfaces.ErrorMessage {
	if h == nil || h.AuthManager == nil || msg == nil || msg.Error == nil {
		return msg
	}
	auth, ok := h.AuthManager.GetByID(selected.get())
	if !ok || auth == nil {
		return msg
	}
	return antigravityGuardReplyFor(entryProtocol, auth.Provider, msg)
}

func antigravityGuardReplyFor(entryProtocol, provider string, msg *interfaces.ErrorMessage) *interfaces.ErrorMessage {
	if msg.DirectResponse || msg.StatusCode != http.StatusTooManyRequests || !strings.EqualFold(provider, "antigravity") ||
		!internalconfig.IsAntigravityContentFilter429(msg.Error.Error()) {
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
