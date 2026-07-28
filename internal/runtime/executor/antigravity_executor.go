// Package executor provides runtime execution capabilities for various AI service providers.
// This file implements the Antigravity executor that proxies requests to the antigravity
// upstream using OAuth credentials.
package executor

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	internalsignature "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	antigravityclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/antigravity/claude"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	antigravityBaseURLDaily                = "https://daily-cloudcode-pa.googleapis.com"
	antigravitySandboxBaseURLDaily         = "https://daily-cloudcode-pa.sandbox.googleapis.com"
	antigravityBaseURLProd                 = "https://cloudcode-pa.googleapis.com"
	antigravityCountTokensPath             = "/v1internal:countTokens"
	antigravityStreamPath                  = "/v1internal:streamGenerateContent"
	antigravityGeneratePath                = "/v1internal:generateContent"
	antigravityClientID                    = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	antigravityClientSecret                = "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"
	antigravityAuthType                    = "antigravity"
	refreshSkew                            = 3000 * time.Second
	antigravityCreditsHintRefreshInterval  = 10 * time.Minute
	antigravityCreditsHintRefreshTimeout   = 5 * time.Second
	antigravityShortQuotaCooldownThreshold = 5 * time.Minute
	antigravityInstantRetryThreshold       = 3 * time.Second
	// systemInstruction              = "You are Antigravity, a powerful agentic AI coding assistant designed by the Google Deepmind team working on Advanced Agentic Coding.You are pair programming with a USER to solve their coding task. The task may require creating a new codebase, modifying or debugging an existing codebase, or simply answering a question.**Absolute paths only****Proactiveness**"
)

// AntigravityExecutor proxies requests to the antigravity upstream.
type AntigravityExecutor struct {
	cfg *config.Config
}

// NewAntigravityExecutor creates a new Antigravity executor instance.
//
// Parameters:
//   - cfg: The application configuration
//
// Returns:
//   - *AntigravityExecutor: A new Antigravity executor instance
func NewAntigravityExecutor(cfg *config.Config) *AntigravityExecutor {
	return &AntigravityExecutor{cfg: cfg}
}

// antigravityTransport is a singleton HTTP/1.1 transport shared by all Antigravity requests.
// It is initialized once via antigravityTransportOnce to avoid leaking a new connection pool
// (and the goroutines managing it) on every request.
var (
	antigravityTransport     *http.Transport
	antigravityTransportOnce sync.Once
)

func cloneTransportWithHTTP11(base *http.Transport) *http.Transport {
	if base == nil {
		return nil
	}

	clone := base.Clone()
	clone.ForceAttemptHTTP2 = false
	// Wipe TLSNextProto to prevent implicit HTTP/2 upgrade.
	clone.TLSNextProto = make(map[string]func(authority string, c *tls.Conn) http.RoundTripper)
	if clone.TLSClientConfig == nil {
		clone.TLSClientConfig = &tls.Config{}
	} else {
		clone.TLSClientConfig = clone.TLSClientConfig.Clone()
	}
	// Actively advertise only HTTP/1.1 in the ALPN handshake.
	clone.TLSClientConfig.NextProtos = []string{"http/1.1"}
	return clone
}

// initAntigravityTransport creates the shared HTTP/1.1 transport exactly once.
func initAntigravityTransport() {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	antigravityTransport = cloneTransportWithHTTP11(base)
}

// newAntigravityHTTPClient creates an HTTP client specifically for Antigravity,
// enforcing HTTP/1.1 by disabling HTTP/2 to perfectly mimic Node.js https defaults.
// The underlying Transport is a singleton to avoid leaking connection pools.
func newAntigravityHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	antigravityTransportOnce.Do(initAntigravityTransport)

	client := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, timeout)
	// If no transport is set, use the shared HTTP/1.1 transport.
	if client.Transport == nil {
		client.Transport = antigravityTransport
		return client
	}

	// Preserve proxy settings from proxy-aware transports while forcing HTTP/1.1.
	if transport, ok := client.Transport.(*http.Transport); ok {
		client.Transport = cloneTransportWithHTTP11(transport)
	}
	return client
}

func sanitizeAntigravityGeminiRequestSignatures(modelName string, rawJSON []byte) []byte {
	if !antigravityUsesReasoningReplayCache(modelName) {
		return rawJSON
	}
	rawJSON = internalsignature.SanitizeGeminiRequestThoughtSignatures(rawJSON, "request.contents")
	return normalizeAntigravityGeminiFunctionResponseRoles(rawJSON)
}

func normalizeAntigravityGeminiFunctionResponseRoles(rawJSON []byte) []byte {
	rawJSON = repairAntigravityGeminiFunctionResponseNames(rawJSON)
	contents := gjson.GetBytes(rawJSON, "request.contents")
	if !contents.IsArray() {
		return rawJSON
	}
	type functionRef struct {
		id   string
		name string
	}
	out := rawJSON
	var pending []functionRef
	for contentIndex, content := range contents.Array() {
		parts := content.Get("parts")
		if !parts.IsArray() || len(parts.Array()) == 0 {
			pending = nil
			continue
		}
		var calls, responses []functionRef
		var responseParts []json.RawMessage
		hasOtherPart := false
		parts.ForEach(func(_, part gjson.Result) bool {
			switch {
			case part.Get("functionCall").Exists():
				calls = append(calls, functionRef{id: part.Get("functionCall.id").String(), name: part.Get("functionCall.name").String()})
			case part.Get("functionResponse").Exists():
				responses = append(responses, functionRef{id: part.Get("functionResponse.id").String(), name: part.Get("functionResponse.name").String()})
				responseParts = append(responseParts, json.RawMessage(part.Raw))
			default:
				hasOtherPart = true
			}
			return true
		})
		if len(calls) > 0 && len(responses) == 0 {
			pending = calls
			continue
		}
		if len(responses) == 0 {
			if hasOtherPart {
				pending = nil
			}
			continue
		}
		if hasOtherPart || len(calls) > 0 {
			pending = nil
			continue
		}

		if len(pending) == len(responses) {
			ordered := make([]json.RawMessage, 0, len(responseParts))
			used := make([]bool, len(responses))
			for _, call := range pending {
				matched := -1
				for responseIndex, response := range responses {
					if used[responseIndex] {
						continue
					}
					if (call.id != "" && response.id == call.id) || (call.id == "" && call.name != "" && response.name == call.name) {
						matched = responseIndex
						break
					}
				}
				if matched < 0 {
					ordered = nil
					break
				}
				used[matched] = true
				ordered = append(ordered, responseParts[matched])
			}
			if len(ordered) == len(responseParts) {
				if encoded, errMarshal := json.Marshal(ordered); errMarshal == nil {
					if updated, errSet := sjson.SetRawBytes(out, fmt.Sprintf("request.contents.%d.parts", contentIndex), encoded); errSet == nil {
						out = updated
					}
				}
			}
		}
		pending = nil
		if content.Get("role").String() != "model" {
			if updated, errSet := sjson.SetBytes(out, fmt.Sprintf("request.contents.%d.role", contentIndex), "model"); errSet == nil {
				out = updated
			}
		}
	}
	return out
}

func repairAntigravityGeminiFunctionResponseNames(rawJSON []byte) []byte {
	contents := gjson.GetBytes(rawJSON, "request.contents")
	if !contents.IsArray() {
		return rawJSON
	}
	callIDToName := make(map[string]string)
	contents.ForEach(func(_, content gjson.Result) bool {
		parts := content.Get("parts")
		if !parts.IsArray() {
			return true
		}
		parts.ForEach(func(_, part gjson.Result) bool {
			fc := part.Get("functionCall")
			if fc.Exists() {
				id := strings.TrimSpace(fc.Get("id").String())
				name := strings.TrimSpace(fc.Get("name").String())
				if id != "" && name != "" && name != "unknown" {
					callIDToName[id] = name
				}
			}
			return true
		})
		return true
	})
	if len(callIDToName) == 0 {
		return rawJSON
	}

	out := rawJSON
	contents.ForEach(func(contentIdx, content gjson.Result) bool {
		parts := content.Get("parts")
		if !parts.IsArray() {
			return true
		}
		parts.ForEach(func(partIdx, part gjson.Result) bool {
			fr := part.Get("functionResponse")
			if fr.Exists() {
				id := strings.TrimSpace(fr.Get("id").String())
				name := strings.TrimSpace(fr.Get("name").String())
				if id != "" && (name == "" || name == "unknown") {
					if realName, ok := callIDToName[id]; ok {
						path := fmt.Sprintf("request.contents.%d.parts.%d.functionResponse.name", contentIdx.Int(), partIdx.Int())
						if updated, errSet := sjson.SetBytes(out, path, realName); errSet == nil {
							out = updated
						}
					}
				}
			}
			return true
		})
		return true
	})
	return out
}

func validateAntigravityRequestSignatures(ctx context.Context, modelName string, from sdktranslator.Format, rawJSON []byte) ([]byte, error) {
	if from.String() != "claude" {
		return rawJSON, nil
	}
	before := countClaudeThinkingBlocks(rawJSON)
	if antigravityUsesReasoningReplayCache(modelName) {
		rawJSON = antigravityclaude.StripInvalidGeminiSignatureThinkingBlocks(rawJSON)
		logAntigravitySignatureStrip(before, countClaudeThinkingBlocks(rawJSON), "provider_cleanup", "empty_or_non_gemini_signature")
		return rawJSON, nil
	}
	// Claude models accept only Claude-format thinking signatures.
	rawJSON = antigravityclaude.StripEmptySignatureThinkingBlocks(rawJSON)
	logAntigravitySignatureStrip(before, countClaudeThinkingBlocks(rawJSON), "prefix_cleanup", "empty_or_non_claude_signature")
	if cache.SignatureCacheEnabled() {
		return rawJSON, nil
	}
	if !cache.SignatureBypassStrictMode() {
		// Non-strict bypass: let the translator handle invalid signatures
		// by dropping unsigned thinking blocks silently (no 400).
		return rawJSON, nil
	}
	before = countClaudeThinkingBlocks(rawJSON)
	rawJSON = antigravityclaude.StripInvalidBypassSignatureThinkingBlocks(rawJSON)
	logAntigravitySignatureStrip(before, countClaudeThinkingBlocks(rawJSON), "strict_bypass", "invalid_antigravity_claude_signature")
	return rawJSON, nil
}

func hasAntigravityClaudeTypedWebSearchTool(payload []byte) bool {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		switch tool.Get("type").String() {
		case "web_search_20250305", "web_search_20260209":
			return true
		}
	}
	return false
}

func hasAntigravityGoogleSearchTool(payload []byte) bool {
	tools := gjson.GetBytes(payload, "request.tools")
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		if tool.Get("googleSearch").Exists() {
			return true
		}
	}
	return false
}

func shouldResolveAntigravityWebSearchGroundingURLs(from sdktranslator.Format, originalRequestRawJSON, requestRawJSON []byte) bool {
	return from.String() == "claude" &&
		hasAntigravityClaudeTypedWebSearchTool(originalRequestRawJSON) &&
		hasAntigravityGoogleSearchTool(requestRawJSON)
}

func (e *AntigravityExecutor) resolveWebSearchGroundingURLs(ctx context.Context, auth *cliproxyauth.Auth, from sdktranslator.Format, originalRequestRawJSON, requestRawJSON, responseRawJSON []byte) []byte {
	if !shouldResolveAntigravityWebSearchGroundingURLs(from, originalRequestRawJSON, requestRawJSON) {
		return responseRawJSON
	}
	return helps.ResolveAntigravityGroundingURLs(ctx, e.cfg, auth, responseRawJSON)
}

func countClaudeThinkingBlocks(rawJSON []byte) int {
	messages := gjson.GetBytes(rawJSON, "messages")
	if !messages.IsArray() {
		return 0
	}

	count := 0
	messages.ForEach(func(_, message gjson.Result) bool {
		content := message.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(_, part gjson.Result) bool {
			if part.Get("type").String() == "thinking" {
				count++
			}
			return true
		})
		return true
	})
	return count
}

func logAntigravitySignatureStrip(before, after int, stage, reason string) {
	removed := before - after
	if removed <= 0 {
		return
	}
	log.WithFields(log.Fields{
		"component":       "signature_sanitizer",
		"executor":        "antigravity",
		"target_provider": "claude",
		"action":          "drop_thinking_blocks",
		"stage":           stage,
		"reason":          reason,
		"count":           removed,
	}).Debug("antigravity executor: dropped Claude thinking blocks with invalid signatures")
}

// Identifier returns the executor identifier.
func (e *AntigravityExecutor) Identifier() string { return antigravityAuthType }

// PrepareRequest injects Antigravity credentials into the outgoing HTTP request.
func (e *AntigravityExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	token, _, errToken := e.ensureAccessToken(req.Context(), auth)
	if errToken != nil {
		return errToken
	}
	if strings.TrimSpace(token) == "" {
		return statusErr{code: http.StatusUnauthorized, msg: "missing access token"}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// HttpRequest injects Antigravity credentials into the request and executes it.
// It uses a whitelist approach: all incoming headers are stripped and only
// the minimum set required by the Antigravity protocol is explicitly set.
func (e *AntigravityExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("antigravity executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)

	// --- Whitelist: save only the headers we need from the original request ---
	contentType := httpReq.Header.Get("Content-Type")

	// Wipe ALL incoming headers
	for k := range httpReq.Header {
		delete(httpReq.Header, k)
	}

	// --- Set only the headers Antigravity actually sends ---
	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}
	// Content-Length is managed automatically by Go's http.Client from the Body
	httpReq.Header.Set("User-Agent", resolveUserAgent(auth))
	httpReq.Close = true // sends Connection: close

	// Inject Authorization: Bearer <token>
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}

	httpClient := newAntigravityHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}
