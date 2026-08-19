package registry

import (
	"strings"
)

type openCodeRoute struct {
	Protocol string // "responses" | "messages" | "chat" | "gemini"
}

// openCodeRoutes maps a gateway ("zen" or "go") plus a normalized model ID to
// the protocol path used on that gateway's base URL. The map is generated from
// the official OpenCode Zen/Go docs endpoint tables (see opencode_routes.json).
var openCodeRoutes = map[string]map[string]string{}

// openCodeGeminiIDs lists model IDs routed to the Gemini /models/{id} path on
// the Zen gateway. Go has no Gemini models.
var openCodeGeminiIDs = map[string]bool{}

// openCodePrefixRules maps ID-prefix rules to protocol segments on each gateway.
// Rules are applied only when the model ID is not present as an explicit entry
// above. A model ID matches at most one rule.
var openCodeZenPrefixRules = []struct {
	prefix   string
	protocol string
}{
	{"gpt-", "responses"},
	{"grok-", "responses"},
	{"muse-", "responses"},
	{"claude-", "messages"},
	{"qwen", "messages"},
	{"minimax-", "messages"},
	{"gemini-", "gemini"},
}

// openCodeGoPrefixRules are the prefix rules for the Go gateway.
var openCodeGoPrefixRules = []struct {
	prefix   string
	protocol string
}{
	{"gpt-", "responses"},
	{"grok-", "responses"},
	{"muse-", "responses"},
	{"claude-", "messages"},
	{"qwen3", "messages"},
	{"minimax-", "messages"},
	{"deepseek-", "chat"},
	{"hy3", "chat"},
	{"mimo-", "chat"},
}

// ResolveOpenCodeProtocol returns the upstream protocol segment for the given
// gateway ("zen" or "go") and model ID. Returns "" when the model is unknown
// to this gateway (caller should error rather than guess).
func ResolveOpenCodeProtocol(gateway, modelID string) string {
	gw := strings.ToLower(strings.TrimSpace(gateway))
	model := strings.ToLower(strings.TrimSpace(modelID))
	if model == "" || gw == "" {
		return ""
	}

	// Explicit ID overrides (covers MiniMax divergence: messages on Go,
	// chat/completions on Zen).
	if m, ok := openCodeRoutes[gw]; ok {
		if p, ok := m[model]; ok {
			return p
		}
	}

	// Prefix rules.
	rules := openCodeZenPrefixRules
	if gw == "go" {
		rules = openCodeGoPrefixRules
	}
	for _, r := range rules {
		if strings.HasPrefix(model, r.prefix) {
			return r.protocol
		}
	}

	// Free-tier models on Zen are chat/completions only (not billed), so accept
	// any free- family as chat rather than erroring.
	if gw == "zen" && strings.HasSuffix(model, "-free") {
		return "chat"
	}

	return ""
}

// OpenCodeModelPath returns the URL path appended to the gateway base URL for
// the given model. It returns "" when the model is unknown to the gateway.
func OpenCodeModelPath(gateway, modelID string) string {
	switch ResolveOpenCodeProtocol(gateway, modelID) {
	case "responses":
		return "/v1/responses"
	case "messages":
		return "/v1/messages"
	case "chat":
		return "/v1/chat/completions"
	case "gemini":
		return "/v1/models/" + modelID
	}
	return ""
}

// OpenCodeModelKnown reports whether the gateway+model combination resolves to
// a known upstream route.
func OpenCodeModelKnown(gateway, modelID string) bool {
	return OpenCodeModelPath(gateway, modelID) != ""
}
