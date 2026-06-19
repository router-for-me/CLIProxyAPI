package auth

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// codexModelFallbacks maps primary Codex OAuth model base names to their fallback model base names.
// When the primary model exhausts its full retry cycle, the fallback model is tried transparently.
var codexModelFallbacks = map[string]string{
	"gpt-5.5": "gpt-5.4",
}

// codexFallbackModel returns the fallback base model for the given base model name, if one exists.
func codexFallbackModel(baseModel string) (string, bool) {
	fb, ok := codexModelFallbacks[baseModel]
	return fb, ok
}

// hasCodexProvider reports whether any of the given providers is the Codex OAuth provider.
func hasCodexProvider(providers []string) bool {
	for _, p := range providers {
		if strings.EqualFold(strings.TrimSpace(p), "codex") {
			return true
		}
	}
	return false
}

// buildCodexFallbackRequest constructs a fallback request substituting the fallback model
// and recording the original model name for transparent response rewriting.
// Returns the fallback request, fallback opts, and ok=true if a fallback mapping exists.
func buildCodexFallbackRequest(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Request, cliproxyexecutor.Options, bool) {
	parsed := thinking.ParseSuffix(req.Model)
	fbBase, ok := codexFallbackModel(parsed.ModelName)
	if !ok {
		return req, opts, false
	}

	fbModel := fbBase
	if parsed.HasSuffix {
		fbModel = fbBase + "(" + parsed.RawSuffix + ")"
	}

	fbReq := req
	fbReq.Model = fbModel

	fbMeta := make(map[string]any, len(opts.Metadata)+2)
	for k, v := range opts.Metadata {
		fbMeta[k] = v
	}
	fbMeta[cliproxyexecutor.CodexFallbackDisplayModelMetadataKey] = parsed.ModelName
	fbMeta[cliproxyexecutor.RequestedModelMetadataKey] = fbBase

	fbOpts := opts
	fbOpts.Metadata = fbMeta

	return fbReq, fbOpts, true
}

// tryCodexModelFallbackExecute attempts a non-streaming execution with the Codex model fallback.
// It runs the full retry cycle for the fallback model.
// Returns (response, true) on success, (zero, false) if no fallback applies or all attempts fail.
func (m *Manager) tryCodexModelFallbackExecute(ctx context.Context, normalized []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, bool) {
	if !hasCodexProvider(normalized) {
		return cliproxyexecutor.Response{}, false
	}
	fbReq, fbOpts, ok := buildCodexFallbackRequest(req, opts)
	if !ok {
		return cliproxyexecutor.Response{}, false
	}
	resp, err := m.runMixedRetry(ctx, normalized, fbReq, fbOpts)
	if err != nil {
		return cliproxyexecutor.Response{}, false
	}
	return resp, true
}

// tryCodexModelFallbackExecuteStream attempts a streaming execution with the Codex model fallback.
// It runs the full retry cycle for the fallback model.
// Returns (result, true) on success, (nil, false) if no fallback applies or all attempts fail.
func (m *Manager) tryCodexModelFallbackExecuteStream(ctx context.Context, normalized []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, bool) {
	if !hasCodexProvider(normalized) {
		return nil, false
	}
	fbReq, fbOpts, ok := buildCodexFallbackRequest(req, opts)
	if !ok {
		return nil, false
	}
	result, err := m.runStreamMixedRetry(ctx, normalized, fbReq, fbOpts)
	if err != nil {
		return nil, false
	}
	return result, true
}
