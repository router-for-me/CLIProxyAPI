package handlers

import (
	"strings"

	"github.com/tidwall/gjson"
)

// webSearchToolTypePrefix is the common prefix of Anthropic's typed web_search server tools.
// The full type is versioned with a date suffix (e.g. "web_search_20250305",
// "web_search_20260209"); matching by prefix future-proofs detection against new versions.
const webSearchToolTypePrefix = "web_search"

// hasWebSearchTool reports whether the request payload declares an Anthropic web_search server
// tool, i.e. any entry in the top-level "tools" array whose "type" starts with "web_search".
//
// Only Anthropic-format requests carry such typed tools, so this is the sole detection gate:
// OpenAI tools use type "function" and Gemini uses "functionDeclarations", neither of which
// matches.
func hasWebSearchTool(rawJSON []byte) bool {
	tools := gjson.GetBytes(rawJSON, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		toolType := strings.ToLower(strings.TrimSpace(tool.Get("type").String()))
		if strings.HasPrefix(toolType, webSearchToolTypePrefix) {
			return true
		}
	}
	return false
}

// webSearchForwardTarget returns the model that a web_search-bearing request should be
// rerouted to, or "" when forwarding should not happen.
//
// The request body itself is never modified here — only the routing model is overridden by the
// caller. No target-capability validation is performed; forwarding to a target that cannot
// handle the request is the user's responsibility.
//
// Returns "" (no forwarding) when:
//   - the feature is disabled,
//   - no target model is configured,
//   - the request carries no web_search tool,
//   - the target equals the client-requested model (avoids a self-forward loop).
func (h *BaseAPIHandler) webSearchForwardTarget(requestedModel string, rawJSON []byte) string {
	cfg := h.Cfg
	if cfg == nil || !cfg.WebSearchForward.Enable {
		return ""
	}
	target := strings.TrimSpace(cfg.WebSearchForward.Model)
	if target == "" {
		return ""
	}
	if !hasWebSearchTool(rawJSON) {
		return ""
	}
	if strings.EqualFold(target, strings.TrimSpace(requestedModel)) {
		return ""
	}
	return target
}
