package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/ignite"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/router"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const ignitionPrompt = "Reply with OK."

// igniteHost adapts the engine and CPA's auth manager for the igniter.
type igniteHost struct {
	e *Engine
}

func (h igniteHost) Accounts() []router.AccountView {
	r := h.e.activeRouter()
	if r == nil {
		return nil
	}
	return r.Accounts()
}

func (h igniteHost) RequestRefresh(authID string) { h.e.refresh(authID) }

func (h igniteHost) Models(authID string) []string {
	var out []string
	for _, m := range registry.GetGlobalRegistry().GetModelsForClient(authID) {
		if m != nil && m.ID != "" {
			out = append(out, m.ID)
		}
	}
	return out
}

func (h igniteHost) CredentialStamp(authID string) string {
	m := h.e.authManager()
	if m == nil {
		return ""
	}
	a, ok := m.GetByID(authID)
	if !ok || a == nil {
		return ""
	}
	token, _ := a.Metadata["access_token"].(string)
	sum := sha256.Sum256([]byte(token + "\x00" + a.UpdatedAt.String()))
	return hex.EncodeToString(sum[:8])
}

// ignitionRequest builds the minimal request in the provider's own format,
// so no protocol translation is involved.
func ignitionRequest(provider, model string) ([]byte, sdktranslator.Format, error) {
	switch provider {
	case "claude":
		raw, err := json.Marshal(map[string]any{
			"model": model, "max_tokens": 16, "stream": true,
			"messages": []any{map[string]any{"role": "user", "content": ignitionPrompt}},
		})
		return raw, sdktranslator.FormatClaude, err
	case "codex":
		raw, err := json.Marshal(map[string]any{
			"model": model, "stream": true, "store": false, "instructions": ignitionPrompt,
			"input": []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": ignitionPrompt}}}},
		})
		return raw, sdktranslator.FormatOpenAIResponse, err
	}
	return nil, "", errors.New("provider cannot be ignited")
}

// Fire sends the ignition request through CPA's own executor, pinned to the
// account, and stops reading at the first streamed delta: by then the
// upstream has accepted the request and the window has started.
func (h igniteHost) Fire(ctx context.Context, provider, authID, model string) ignite.Result {
	m := h.e.authManager()
	if m == nil {
		return ignite.Result{Err: errors.New("auth manager unavailable")}
	}
	payload, format, err := ignitionRequest(provider, model)
	if err != nil {
		return ignite.Result{Err: err}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	opts := cliproxyexecutor.Options{
		Stream: true, SourceFormat: format, ResponseFormat: format, OriginalRequest: payload,
		Metadata: map[string]any{
			cliproxyexecutor.PinnedAuthMetadataKey:     authID,
			cliproxyexecutor.RequestedModelMetadataKey: model,
		},
	}
	res, err := m.ExecuteStream(ctx, []string{provider}, cliproxyexecutor.Request{Model: model, Payload: payload, Format: format}, opts)
	if err != nil {
		return classifyFireError(err)
	}
	if res == nil || res.Chunks == nil {
		return ignite.Result{}
	}
	defer func() {
		// Let the executor finish unwinding after cancellation.
		go func() {
			for range res.Chunks {
			}
		}()
	}()
	for chunk := range res.Chunks {
		if chunk.Err != nil {
			return classifyFireError(chunk.Err)
		}
		if bytes.Contains(chunk.Payload, []byte(`"delta"`)) {
			return ignite.Result{}
		}
	}
	return ignite.Result{}
}

func classifyFireError(err error) ignite.Result {
	out := ignite.Result{Err: err}
	var se cliproxyexecutor.StatusError
	if errors.As(err, &se) {
		out.Status = se.StatusCode()
	}
	var ae *coreauth.Error
	if errors.As(err, &ae) {
		if out.Status == 0 {
			out.Status = ae.HTTPStatus
		}
		switch strings.ToLower(ae.Code) {
		case "model_cooldown", "auth_unavailable":
			out.Cooldown = true
		}
	}
	return out
}
