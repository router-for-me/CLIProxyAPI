package session

import (
	"net/http"
	"strings"
)

// Client types reported alongside a session identity. They describe which
// downstream tool sent the request and are never used as session identifiers.
const (
	ClientTypeClaudeCode   = "claude-code"
	ClientTypeCodex        = "codex"
	ClientTypeOpenCode     = "opencode"
	ClientTypeOpenAISDK    = "openai-sdk"
	ClientTypeAnthropicSDK = "anthropic-sdk"
	ClientTypeGoogleGenAI  = "google-genai"
	ClientTypeVercelAI     = "vercel-ai"
)

// clientTypeMaxLength bounds a client-declared X-Gateway-Client value.
const clientTypeMaxLength = 64

// DetectClientType identifies the downstream client from corroborating request
// signals. A User-Agent alone never confirms a coding agent: agent detection also
// requires a matching native header, because any application can copy a User-Agent.
// An unrecognised client returns an empty string rather than a guess.
func DetectClientType(headers http.Header) string {
	if headers == nil {
		return ""
	}
	if declared := declaredClientType(headers); declared != "" {
		return declared
	}

	userAgent := strings.ToLower(strings.TrimSpace(rawHeader(headers, "User-Agent")))
	switch {
	case isClaudeCode(headers, userAgent):
		return ClientTypeClaudeCode
	case isCodex(headers, userAgent):
		return ClientTypeCodex
	case strings.Contains(userAgent, "opencode/"):
		return ClientTypeOpenCode
	case strings.HasPrefix(userAgent, "ai/") || strings.Contains(userAgent, " ai-sdk/"):
		return ClientTypeVercelAI
	case strings.HasPrefix(userAgent, "openai/"):
		return ClientTypeOpenAISDK
	case strings.HasPrefix(userAgent, "anthropic/"):
		return ClientTypeAnthropicSDK
	case strings.Contains(userAgent, "google-genai") || rawHeader(headers, "X-Goog-Api-Client") != "":
		return ClientTypeGoogleGenAI
	}
	return ""
}

// declaredClientType returns a sanitized X-Gateway-Client value. The header is
// self-declared, so it only labels observability output and never grants trust.
func declaredClientType(headers http.Header) string {
	declared := strings.TrimSpace(NormalizeExplicitID(rawHeader(headers, "X-Gateway-Client")))
	if declared == "" || len(declared) > clientTypeMaxLength {
		return ""
	}
	return strings.ToLower(declared)
}

// isClaudeCode requires two independent Claude Code signals so that a plain
// Anthropic SDK request is never reported as the Claude Code client.
func isClaudeCode(headers http.Header, userAgent string) bool {
	claudeCLI := strings.HasPrefix(userAgent, "claude-cli/")
	sessionHeader := rawHeader(headers, "X-Claude-Code-Session-Id") != ""
	// Background requests report "cli-bg" instead of "cli".
	xApp := strings.TrimSpace(rawHeader(headers, "X-App"))
	cliApp := strings.EqualFold(xApp, "cli") || strings.EqualFold(xApp, "cli-bg")
	anthropicBeta := rawHeader(headers, "Anthropic-Beta") != ""

	signals := 0
	for _, present := range []bool{claudeCLI, sessionHeader, cliApp && anthropicBeta} {
		if present {
			signals++
		}
	}
	return signals >= 2
}

// isCodex accepts the Codex CLI User-Agent family or a Codex-specific header.
func isCodex(headers http.Header, userAgent string) bool {
	if strings.HasPrefix(userAgent, "codex") {
		return true
	}
	for _, name := range []string{"X-Codex-Turn-Metadata", "X-Codex-Window-Id", "X-Codex-Parent-Thread-Id"} {
		if rawHeader(headers, name) != "" {
			return true
		}
	}
	return false
}

// rawHeader reads a header case-insensitively without applying session ID validation.
func rawHeader(headers http.Header, name string) string {
	if headers == nil {
		return ""
	}
	if value := strings.TrimSpace(headers.Get(name)); value != "" {
		return value
	}
	for key, values := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	return ""
}
