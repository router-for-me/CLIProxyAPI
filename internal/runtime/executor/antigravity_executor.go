// Package executor provides runtime execution capabilities for various AI service providers.
// This file implements the Antigravity executor that proxies requests to the antigravity
// upstream using OAuth credentials.
package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	internalsignature "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	antigravityclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/antigravity/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
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

func (e *AntigravityExecutor) obfuscateSensitiveWords(payload []byte) []byte {
	if e == nil || e.cfg == nil || len(e.cfg.Antigravity.SensitiveWords) == 0 {
		return payload
	}
	matcher := helps.BuildSensitiveWordMatcher(e.cfg.Antigravity.SensitiveWords)
	return helps.ObfuscateSensitiveWordsInSystemInstruction(payload, matcher)
}

// Each Antigravity credential gets its own HTTP/1.1 connection pool. Sessions routed
// to the same auth reuse that pool, while different OAuth identities never share a
// TCP/TLS connection, matching the native client's one-credential process model.
// The cache is bounded so pools cannot accumulate when keys churn.
var (
	antigravityBaseTransport = defaultAntigravityBaseTransport()
	antigravityTransports    = helps.NewTransportCache[antigravityTransportKey](antigravityTransportCacheCapacity)
)

const (
	// antigravityTransportCacheCapacity caps how many Antigravity connection pools stay
	// alive. The bound exists only to stop entries from accumulating when keys churn, for
	// example when a credential's proxy is rotated through the management API or when an
	// SDK embedder supplies a freshly built base transport per request.
	//
	// It is sized for large deployments on purpose. An unused cache entry costs under 1 KB
	// and no goroutines, so capacity is close to free, whereas evicting a pool that is
	// still in active use forces the next request on that credential to redo the TCP + TLS
	// handshake and defeats the point of caching. Credential counts in the low thousands
	// are expected once Home-managed pools are included.
	//
	// Capacity is therefore NOT the lever for bounding memory: an idle pooled connection
	// costs roughly 38 KB plus three goroutines, and that total is driven by live traffic
	// and reclaimed by IdleConnTimeout. Shrinking this number does not save that memory,
	// it only causes pool thrashing.
	antigravityTransportCacheCapacity = 8192

	// antigravityMaxIdleConnsPerHost mirrors the value that
	// cloud.google.com/go/auth/httptransport and google.golang.org/api/transport/http
	// set on their base transport, which is the stack the native Antigravity client
	// uses. Both raise Go's DefaultMaxIdleConnsPerHost of 2 to 100 because the low
	// default forces concurrent requests to re-handshake instead of reusing pooled
	// connections.
	antigravityMaxIdleConnsPerHost = 100

	// antigravityIdleConnTimeout keeps pooled connections usable far longer than Go's
	// 90s default. Captured native traffic reuses a connection after idle gaps with a
	// p90 of roughly six minutes, and a 90s timeout would discard about an eighth of
	// the reuses the native client actually performs.
	antigravityIdleConnTimeout = 10 * time.Minute

	// antigravityAnonymousTransportScope is the pool scope for auth objects that carry
	// no identity at all. Reaching it means the auth has no ID, no source path and no
	// token of any kind, so there is no credential to keep isolated and a single shared
	// pool is safe. Allocating a private pool per request instead would leak a
	// connection pool, and the goroutines managing it, on every call.
	antigravityAnonymousTransportScope = "anonymous"
)

// antigravityTransportKey identifies one connection pool. At most one of proxy and
// base is set: proxy for a credential-scoped proxy pool, base for a transport handed
// in through the request context, and neither for a direct pool.
type antigravityTransportKey struct {
	credential string
	proxy      string
	base       *http.Transport
}

func defaultAntigravityBaseTransport() *http.Transport {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok && transport != nil {
		return transport
	}
	return &http.Transport{}
}

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
	// Native Antigravity sends no ALPN extension. With HTTP/2 disabled above,
	// an empty NextProtos keeps the wire shape aligned while using HTTP/1.1.
	clone.TLSClientConfig.NextProtos = nil
	applyAntigravityPoolLimits(clone)
	return clone
}

// applyAntigravityPoolLimits widens the connection pool so keep-alive actually
// survives concurrency and idle periods. Limits are only ever raised, so an
// operator-supplied base transport with a larger pool keeps its own settings.
func applyAntigravityPoolLimits(transport *http.Transport) {
	if transport == nil {
		return
	}
	// Go treats 0 as DefaultMaxIdleConnsPerHost (2) and a negative value as "never pool
	// an idle connection". Raise the default and smaller positive values, but leave a
	// negative value alone so an operator can still disable pooling outright.
	if transport.MaxIdleConnsPerHost >= 0 && transport.MaxIdleConnsPerHost < antigravityMaxIdleConnsPerHost {
		transport.MaxIdleConnsPerHost = antigravityMaxIdleConnsPerHost
	}
	// MaxIdleConns caps the pool across all hosts. Leaving it below the per-host limit
	// would silently throttle Antigravity, which talks to a single host at a time.
	// Zero means unlimited, so it must not be lowered.
	if transport.MaxIdleConns > 0 && transport.MaxIdleConns < transport.MaxIdleConnsPerHost {
		transport.MaxIdleConns = transport.MaxIdleConnsPerHost
	}
	// Zero already means "never expire idle connections", which is strictly longer.
	if transport.IdleConnTimeout > 0 && transport.IdleConnTimeout < antigravityIdleConnTimeout {
		transport.IdleConnTimeout = antigravityIdleConnTimeout
	}
}

// antigravityHTTP11Transport returns the HTTP/1.1 pool shared by every request that
// uses the same credential and the same base transport. The base is either the
// process default or a transport provided through the request context.
func antigravityHTTP11Transport(auth *cliproxyauth.Auth, base *http.Transport) *http.Transport {
	if base == nil {
		return nil
	}
	key := antigravityTransportKey{
		credential: antigravityTransportScope(auth),
		base:       base,
	}
	transport, errGet := antigravityTransports.Get(key, func() (*http.Transport, error) {
		return cloneTransportWithHTTP11(base), nil
	})
	if errGet != nil {
		// Defensive only: the builder above cannot fail. Never return nil here, because a
		// nil Transport makes http.Client fall back to http.DefaultTransport, which
		// advertises h2 over ALPN and would break the Antigravity wire fingerprint.
		log.Debugf("antigravity executor: cache HTTP/1.1 transport failed: %v", errGet)
		return cloneTransportWithHTTP11(base)
	}
	return transport
}

// antigravityProxiedHTTP11Transport returns the credential-scoped HTTP/1.1 pool for
// one proxy setting, or nil when the proxy setting cannot be turned into a
// transport. Keying on the normalized proxy string rather than on a prebuilt
// transport keeps one pool per credential and proxy instead of one per request.
func antigravityProxiedHTTP11Transport(auth *cliproxyauth.Auth, proxyURL string) *http.Transport {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return nil
	}
	key := antigravityTransportKey{
		credential: antigravityTransportScope(auth),
		proxy:      proxyURL,
	}
	transport, errGet := antigravityTransports.Get(key, func() (*http.Transport, error) {
		base, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
		if errBuild != nil {
			return nil, errBuild
		}
		if base == nil {
			return nil, fmt.Errorf("antigravity executor: proxy setting produced no transport")
		}
		return cloneTransportWithHTTP11(base), nil
	})
	if errGet != nil {
		// The caller falls back to NewProxyAwareHTTPClient, which reports the failure
		// and applies the context transport fallback.
		return nil
	}
	return transport
}

// antigravityTransportScope returns the connection-pool scope for one credential.
// Runtime auths always carry an ID. Incomplete auth objects, such as those built by
// tests, plugins or SDK embedders, fall back to another stable credential marker so
// they neither share a pool with an unrelated OAuth identity nor allocate a fresh
// pool, and with it a fresh set of pool goroutines, on every single request.
func antigravityTransportScope(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return antigravityAnonymousTransportScope
	}
	if id := strings.TrimSpace(auth.ID); id != "" {
		return "id:" + id
	}
	if auth.Attributes != nil {
		if path := strings.TrimSpace(auth.Attributes[cliproxyauth.AttributePath]); path != "" {
			return "path:" + path
		}
		if source := strings.TrimSpace(auth.Attributes[cliproxyauth.AttributeSource]); source != "" {
			return "source:" + source
		}
	}
	// Fall back to the credential material itself. Auth.Label is deliberately not used:
	// it is documented as an optional human readable label for logging and carries no
	// uniqueness guarantee, so two different OAuth identities sharing one label would
	// wrongly share a TCP/TLS pool.
	//
	// The refresh token is preferred over the access token because it stays stable
	// across token rotation. Keying on the access token would move a credential to a new
	// pool on every refresh, and would also strand refresh requests themselves, which
	// run before any access token exists.
	if refresh := strings.TrimSpace(metaStringValue(auth.Metadata, "refresh_token")); refresh != "" {
		return antigravityCredentialScope("refresh:", refresh)
	}
	if access := strings.TrimSpace(metaStringValue(auth.Metadata, "access_token")); access != "" {
		return antigravityCredentialScope("token:", access)
	}
	return antigravityAnonymousTransportScope
}

// antigravityCredentialScope derives a pool scope from secret credential material.
// Only a short digest is retained, and it is never logged, so a pool key cannot be
// used to recover the credential it came from.
func antigravityCredentialScope(prefix, secret string) string {
	digest := sha256.Sum256([]byte(secret))
	return prefix + hex.EncodeToString(digest[:8])
}

// newAntigravityHTTPClient creates an HTTP client specifically for Antigravity,
// enforcing HTTP/1.1 by disabling HTTP/2 to match the native Antigravity client, which
// negotiates TLS 1.3 without advertising an ALPN protocol and therefore never uses h2.
// The underlying Transport is always shared so keep-alive connections survive across
// requests instead of forcing a fresh TCP + TLS handshake every time.
func newAntigravityHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	// Native Antigravity reuses one transport across requests. Opt into a
	// credential-scoped proxy transport only here so other providers keep their
	// existing lifecycle and different OAuth identities remain isolated.
	if proxyURL := antigravityProxyURL(cfg, auth); proxyURL != "" {
		if transport := antigravityProxiedHTTP11Transport(auth, proxyURL); transport != nil {
			return &http.Client{Transport: transport, Timeout: timeout}
		}
		// Fall through so NewProxyAwareHTTPClient reports the failure and applies the
		// context transport fallback, preserving the previous behavior.
	}

	client := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, timeout)
	// Direct requests share an HTTP/1.1 pool only within the selected credential.
	if client.Transport == nil {
		client.Transport = antigravityHTTP11Transport(auth, antigravityBaseTransport)
		return client
	}

	// Preserve a context-provided transport while forcing HTTP/1.1. The cache key
	// includes credential identity, so sharing the base does not share TLS pools.
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		// A RoundTripper that is not an *http.Transport owns its own protocol behavior.
		return client
	}
	if transport == nil {
		// A typed-nil *http.Transport still satisfies the interface nil check in
		// NewProxyAwareHTTPClient. Leaving it in place would make http.Client fall back
		// to http.DefaultTransport, which advertises h2 over ALPN and breaks the
		// Antigravity fingerprint, so substitute the process base transport.
		transport = antigravityBaseTransport
	}
	client.Transport = antigravityHTTP11Transport(auth, transport)
	return client
}

func antigravityProxyURL(cfg *config.Config, auth *cliproxyauth.Auth) string {
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}
	if cfg != nil {
		return strings.TrimSpace(cfg.ProxyURL)
	}
	return ""
}

func sanitizeAntigravityGeminiRequestSignatures(modelName string, rawJSON []byte) []byte {
	if !antigravityUsesReasoningReplayCache(modelName) {
		return rawJSON
	}
	rawJSON = internalsignature.SanitizeGeminiRequestThoughtSignatures(rawJSON, "request.contents")
	return normalizeAntigravityGeminiFunctionResponseRoles(rawJSON)
}

type antigravityContentEdit struct {
	index       int64
	start       int
	end         int
	replacement []byte
}

// normalizeAntigravityGeminiFunctionResponseRoles edits each response turn in
// isolation, then splices all changed turns into the request with one body copy.
// Applying SJSON once per field made large histories scale with history size
// multiplied by the number of tool turns.
func normalizeAntigravityGeminiFunctionResponseRoles(rawJSON []byte) []byte {
	rawJSON = repairAntigravityGeminiFunctionResponseNames(rawJSON)
	contents := util.GetGJSONBytesNoCopy(rawJSON, "request.contents")
	if !contents.IsArray() {
		return rawJSON
	}
	type functionRef struct {
		id   string
		name string
	}

	edits := make([]antigravityContentEdit, 0)
	var pending []functionRef
	validOffsets := true
	contents.ForEach(func(contentIndex, content gjson.Result) bool {
		parts := content.Get("parts")
		if !parts.IsArray() {
			pending = nil
			return true
		}

		var calls, responses []functionRef
		var responseParts []json.RawMessage
		partCount := 0
		hasOtherPart := false
		parts.ForEach(func(_, part gjson.Result) bool {
			partCount++
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
		if partCount == 0 {
			pending = nil
			return true
		}
		if len(calls) > 0 && len(responses) == 0 {
			pending = calls
			return true
		}
		if len(responses) == 0 {
			if hasOtherPart {
				pending = nil
			}
			return true
		}
		if hasOtherPart || len(calls) > 0 {
			pending = nil
			return true
		}

		var contentJSON []byte
		contentChanged := false
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
				encoded, errMarshal := json.Marshal(ordered)
				if errMarshal == nil && !bytes.Equal(encoded, []byte(parts.Raw)) {
					contentJSON = []byte(content.Raw)
					if updated, errSet := sjson.SetRawBytes(contentJSON, "parts", encoded); errSet == nil {
						contentJSON = updated
						contentChanged = true
					}
				}
			}
		}
		pending = nil
		if content.Get("role").String() != "model" {
			if contentJSON == nil {
				contentJSON = []byte(content.Raw)
			}
			if updated, errSet := sjson.SetBytes(contentJSON, "role", "model"); errSet == nil {
				contentJSON = updated
				contentChanged = true
			}
		}
		if !contentChanged {
			return true
		}

		start := content.Index
		end := start + len(content.Raw)
		if start < 0 || end < start || end > len(rawJSON) || !bytes.Equal(rawJSON[start:end], []byte(content.Raw)) {
			validOffsets = false
		}
		edits = append(edits, antigravityContentEdit{
			index:       contentIndex.Int(),
			start:       start,
			end:         end,
			replacement: contentJSON,
		})
		return true
	})
	if len(edits) == 0 {
		return rawJSON
	}
	if !validOffsets {
		return applyAntigravityContentEditsWithSJSON(rawJSON, edits)
	}

	finalSize := len(rawJSON)
	cursor := 0
	for _, edit := range edits {
		if edit.start < cursor {
			return applyAntigravityContentEditsWithSJSON(rawJSON, edits)
		}
		finalSize += len(edit.replacement) - (edit.end - edit.start)
		if finalSize < 0 {
			return applyAntigravityContentEditsWithSJSON(rawJSON, edits)
		}
		cursor = edit.end
	}
	out := make([]byte, 0, finalSize)
	cursor = 0
	for _, edit := range edits {
		out = append(out, rawJSON[cursor:edit.start]...)
		out = append(out, edit.replacement...)
		cursor = edit.end
	}
	return append(out, rawJSON[cursor:]...)
}

// applyAntigravityContentEditsWithSJSON preserves the legacy path semantics if
// a GJSON result cannot be proven to point into the original request bytes.
func applyAntigravityContentEditsWithSJSON(rawJSON []byte, edits []antigravityContentEdit) []byte {
	out := rawJSON
	for _, edit := range edits {
		path := fmt.Sprintf("request.contents.%d", edit.index)
		if updated, errSet := sjson.SetRawBytes(out, path, edit.replacement); errSet == nil {
			out = updated
		}
	}
	return out
}

func repairAntigravityGeminiFunctionResponseNames(rawJSON []byte) []byte {
	contents := util.GetGJSONBytesNoCopy(rawJSON, "request.contents")
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
	tools := util.GetGJSONBytesNoCopy(payload, "tools")
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
	tools := util.GetGJSONBytesNoCopy(payload, "request.tools")
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
	messages := util.GetGJSONBytesNoCopy(rawJSON, "messages")
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

	// Connection management is a Request field, not a header, so the header
	// whitelist below cannot strip it. An inbound "Connection: close" makes Go's
	// server set Request.Close, and WithContext copies that field verbatim, which
	// would both leak the downstream header upstream and drain the shared pool.
	httpReq.Close = false

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

	// Inject Authorization: Bearer <token>
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}

	httpClient := newAntigravityHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}
