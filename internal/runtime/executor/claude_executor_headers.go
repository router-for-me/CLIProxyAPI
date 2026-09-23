package executor

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/gin-gonic/gin"
)

// claudeCredentialUsesOAuth classifies the selected upstream credential. It is the
// single authority for every decision that has to agree with the OAuth beta
// profile, including the extended-cache-ttl beta and the matching body cache ttl.
func claudeCredentialUsesOAuth(auth *cliproxyauth.Auth, apiKey string) bool {
	if isClaudeOAuthToken(apiKey) {
		return true
	}
	if auth != nil && auth.AuthKind() == cliproxyauth.AuthKindAPIKey {
		return false
	}
	hasAPIKeyAttr := auth != nil && auth.Attributes != nil && strings.TrimSpace(auth.Attributes["api_key"]) != ""
	return !hasAPIKeyAttr
}

func copyClaudeCallerFingerprintHeaders(dst, src http.Header) {
	if dst == nil || src == nil {
		return
	}
	for name, values := range src {
		lowerName := strings.ToLower(strings.TrimSpace(name))
		if lowerName != "accept" && lowerName != "accept-encoding" && lowerName != "user-agent" &&
			lowerName != "x-app" && lowerName != "x-client-request-id" &&
			!strings.HasPrefix(lowerName, "anthropic-") &&
			!strings.HasPrefix(lowerName, "x-stainless-") &&
			!strings.HasPrefix(lowerName, "x-claude-code-") &&
			!strings.HasPrefix(lowerName, "x-claude-remote-") &&
			lowerName != "x-client-app" &&
			lowerName != "x-anthropic-additional-protection" {
			continue
		}
		dst.Del(name)
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

func applyClaudeHeaders(r *http.Request, auth *cliproxyauth.Auth, apiKey string, stream bool, extraBetas []string, body []byte, cfg *config.Config, incomingHeaders http.Header, confirmedClaudeCode bool, sessionIDs ...string) error {
	return applyClaudeHeadersWithNativeProfile(
		r,
		auth,
		apiKey,
		stream,
		extraBetas,
		body,
		cfg,
		incomingHeaders,
		confirmedClaudeCode,
		false,
		sessionIDs...,
	)
}

func applyClaudeHeadersWithNativeProfile(
	r *http.Request,
	auth *cliproxyauth.Auth,
	apiKey string,
	stream bool,
	extraBetas []string,
	body []byte,
	cfg *config.Config,
	incomingHeaders http.Header,
	confirmedClaudeCode bool,
	helperProfile bool,
	sessionIDs ...string,
) error {
	if r == nil {
		return nil
	}
	hdrDefault := func(cfgVal, fallback string) string {
		if cfgVal != "" {
			return cfgVal
		}
		return fallback
	}

	var hd config.ClaudeHeaderDefaults
	if cfg != nil {
		hd = cfg.ClaudeHeaderDefaults
	}

	// Authentication and wire fingerprint are separate authorities. File-backed
	// delegated providers still use Bearer auth, but only real Claude OAuth and
	// explicit fingerprint-profile opt-ins receive the CLI wire profile.
	credentialUsesBearer := claudeCredentialUsesOAuth(auth, apiKey)
	useAPIKey := !credentialUsesBearer
	fp := resolveClaudeFingerprintPolicy(cfg, auth, apiKey)
	wirePolicy, _ := resolveClaudeWirePolicy(cfg, auth, apiKey, confirmedClaudeCode)
	applyCLIFingerprint := fp.ProfileClaudeCodeCLI || wirePolicy.Cloak
	preserveCallerFingerprint := !applyCLIFingerprint && !confirmedClaudeCode
	useOAuthBetas := fp.UseOAuthBetas
	isAnthropicBase := isAnthropicUpstreamURL(r.URL)
	if strings.TrimSpace(apiKey) != "" {
		if isAnthropicBase && useAPIKey {
			r.Header.Del("Authorization")
			r.Header.Set("x-api-key", apiKey)
		} else {
			r.Header.Del("x-api-key")
			r.Header.Set("Authorization", "Bearer "+apiKey)
		}
	} else {
		r.Header.Del("Authorization")
		r.Header.Del("x-api-key")
	}
	r.Header.Set("Content-Type", "application/json")

	if incomingHeaders == nil {
		if ginCtx, ok := r.Context().Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			incomingHeaders = ginCtx.Request.Header
		}
	}
	stabilizeDeviceProfile := helps.ClaudeDeviceProfileStabilizationEnabled(cfg)
	var deviceProfile helps.ClaudeDeviceProfile
	if stabilizeDeviceProfile && confirmedClaudeCode {
		var errDeviceProfile error
		deviceProfile, errDeviceProfile = helps.ResolveClaudeDeviceProfileRequired(r.Context(), auth, apiKey, incomingHeaders, cfg)
		if errDeviceProfile != nil {
			return errDeviceProfile
		}
	}

	incomingBetas := strings.TrimSpace(strings.Join(helps.HeaderValuesCaseInsensitive(incomingHeaders, "Anthropic-Beta"), ","))
	countTokens := r.URL != nil && strings.HasSuffix(r.URL.Path, "/count_tokens")
	requestedMap := claudeRequestedBetas(incomingBetas, extraBetas)
	advisorNeeded := requestedMap[claudeAdvisorToolBeta] || claudeBodyHasAdvisorTool(body)

	baseBetas := incomingBetas
	if !preserveCallerFingerprint {
		baseBetas = claudeCodeCLIBetas(body, requestedMap, useOAuthBetas)
		if countTokens {
			baseBetas = claudeCountTokensBetasForCredential(useOAuthBetas)
			if advisorNeeded {
				baseBetas = withClaudeAdvisorToolBeta(baseBetas)
			}
		}
	}
	if confirmedClaudeCode && incomingBetas != "" {
		baseBetas = incomingBetas
		if advisorNeeded {
			baseBetas = withClaudeAdvisorToolBeta(baseBetas)
		}
		// Measured Haiku helper requests already carry the exact credential
		// beta profile and intentionally omit extended-cache-ttl.
		// Native Claude Code subagents and probes also omit extended-cache-ttl.
		if useOAuthBetas && !helperProfile {
			if countTokens {
				baseBetas = withClaudeCountTokensOAuthBeta(baseBetas)
			} else {
				isSubagent := helps.IsClaudeSubagentRequest(incomingHeaders, body)
				isProbe := helps.IsClaudeProbeOrHelperRequest(body)
				subagent1h := isSubagent && helps.ClaudeSubagentRequests1h(incomingHeaders, body)
				includeExtendedCacheTTL := (!isSubagent || subagent1h) && !isProbe
				baseBetas = withClaudeOAuthCredentialBetas(baseBetas, includeExtendedCacheTTL)
			}
		}
	}
	if preserveCallerFingerprint && advisorNeeded {
		baseBetas = withClaudeAdvisorToolBeta(baseBetas)
	}
	if !claudeRequestSupportsEffort(body, nil) {
		baseBetas = withoutClaudeBeta(baseBetas, claudeEffortBeta)
	}
	existingSet := make(map[string]bool)
	for _, beta := range strings.Split(baseBetas, ",") {
		if beta = strings.TrimSpace(beta); beta != "" {
			existingSet[beta] = true
		}
	}
	appendBeta := func(beta string) {
		beta = strings.TrimSpace(beta)
		if beta == "" || existingSet[beta] {
			return
		}
		if strings.TrimSpace(baseBetas) == "" {
			baseBetas = beta
		} else {
			baseBetas += "," + beta
		}
		existingSet[beta] = true
	}
	if preserveCallerFingerprint {
		// Caller-owned mode preserves both header and body-lifted betas verbatim.
		// The explicit speed=fast request still needs its protocol beta.
		if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(body, "speed").String()), "fast") {
			appendBeta(claudeFastModeBeta)
		}
		for _, beta := range extraBetas {
			appendBeta(beta)
		}
	} else {
		// On direct Anthropic an unconfirmed CLI-profile caller's managed betas
		// are dropped: appending them to the measured baseline produces a shape
		// real Claude Code never sends. Caller betas the proxy does not manage
		// are newer-client features the pinned profile predates; dropping them
		// fails those requests outright, so they are forwarded (#5738).
		// per-turn-control-2026-07-01 is assembled for models that send it.
		// Custom
		// gateways keep all caller extensions.
		if !confirmedClaudeCode && incomingBetas != "" {
			for _, beta := range strings.Split(incomingBetas, ",") {
				beta = strings.TrimSpace(beta)
				if beta == "" {
					continue
				}
				if isManagedClaudeBeta(beta) && isAnthropicBase {
					continue
				}
				appendBeta(beta)
			}
		}
		if !isAnthropicBase {
			for _, beta := range extraBetas {
				appendBeta(beta)
			}
		}
	}
	applyBetaHeader := func() {
		// Enforce strict native Claude Code 2.1.280 model & turn beta gating:
		if !claudeRequestSupportsEffort(body, nil) {
			baseBetas = withoutClaudeBeta(baseBetas, claudeEffortBeta)
		}
		reqProbeOrHelper := helps.IsClaudeProbeOrHelperRequest(body)
		if reqProbeOrHelper {
			baseBetas = withoutClaudeBeta(baseBetas, claudeServerSideFallbackBeta)
			baseBetas = withoutClaudeBeta(baseBetas, claudeThinkingDisplayUpdatesBeta)
			baseBetas = withoutClaudeBeta(baseBetas, claudeExtendedCacheTTLBeta)
		}
		reqThinkingType := gjson.GetBytes(body, "thinking.type").String()
		if reqThinkingType == "disabled" {
			baseBetas = withoutClaudeBeta(baseBetas, claudeThinkingDisplayUpdatesBeta)
		}
		if helps.IsClaudeSubagentRequest(incomingHeaders, body) && !helps.ClaudeSubagentRequests1h(incomingHeaders, body) {
			baseBetas = withoutClaudeBeta(baseBetas, claudeExtendedCacheTTLBeta)
		}
		if !reqProbeOrHelper && !countTokens && helps.ClaudePayloadHas1hTTL(body) {
			baseBetas = withClaudeExtendedCacheTTLBeta(baseBetas)
		}
		reqModel := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "model").String()))
		if isClaudeHaikuModel(reqModel) && !gjson.GetBytes(body, "fallbacks").Exists() {
			baseBetas = withoutClaudeBeta(baseBetas, claudeServerSideFallbackBeta)
		}

		if strings.TrimSpace(baseBetas) == "" {
			r.Header.Del("Anthropic-Beta")
			return
		}
		r.Header.Set("Anthropic-Beta", baseBetas)
	}
	applyBetaHeader()

	if preserveCallerFingerprint {
		defaultAccept := "application/json"
		defaultAcceptEncoding := "gzip, deflate, br, zstd"
		if stream && !isAnthropicBase {
			defaultAccept = "text/event-stream"
			defaultAcceptEncoding = "identity"
		}
		copyClaudeCallerFingerprintHeaders(r.Header, incomingHeaders)
		misc.EnsureHeader(r.Header, incomingHeaders, "Anthropic-Version", "2023-06-01")
		misc.EnsureHeader(r.Header, incomingHeaders, "Accept", defaultAccept)
		misc.EnsureHeader(r.Header, incomingHeaders, "Accept-Encoding", defaultAcceptEncoding)
		// Caller-owned mode forwards the caller's own User-Agent, but a caller that
		// sent none must not fall through to Go's transport default
		// ("Go-http-client/1.1"), which upstreams read as a bot signature. Identify
		// as CPA instead: honest about the hop, and not a fabricated client.
		misc.EnsureHeader(r.Header, incomingHeaders, "User-Agent", "CLIProxyAPI/"+buildinfo.Version)
		applyBetaHeader()
		var attrs map[string]string
		if auth != nil {
			attrs = auth.Attributes
		}
		util.ApplyCustomHeadersFromAttrs(r, attrs, incomingHeaders)
		// Scope the custom-header escape hatch exactly like the CLI path below, which
		// claws overrides back on api.anthropic.com (an operator Anthropic-Beta reaches
		// a first-party API that rejects unknown values) and on any streaming request
		// (an Accept override silently disables event negotiation), while letting a
		// non-streaming third-party gateway keep them. Restoring here means restoring
		// the caller's own choice, not CPA's default: this mode is caller-owned.
		restoreCallerTransport := func() {
			resetHeader := func(name, fallback string) {
				if value := strings.TrimSpace(incomingHeaders.Get(name)); value != "" {
					r.Header.Set(name, value)
					return
				}
				r.Header.Set(name, fallback)
			}
			resetHeader("Accept", defaultAccept)
			resetHeader("Accept-Encoding", defaultAcceptEncoding)
		}
		if isAnthropicBase {
			applyBetaHeader()
			restoreCallerTransport()
		} else if stream {
			restoreCallerTransport()
		}
		return nil
	}

	identityHeader := func(name, fallback string) {
		if confirmedClaudeCode {
			misc.EnsureHeader(r.Header, incomingHeaders, name, fallback)
			return
		}
		r.Header.Set(name, fallback)
	}
	identityHeader("Anthropic-Version", "2023-06-01")
	identityHeader("Anthropic-Dangerous-Direct-Browser-Access", "true")
	identityHeader("X-App", "cli")
	// Values below match Claude Code 2.1.280 / @anthropic-ai/sdk 0.112.1.
	identityHeader("X-Stainless-Retry-Count", "0")
	identityHeader("X-Stainless-Runtime", "node")
	identityHeader("X-Stainless-Lang", "js")
	// Native async SDK helpers add this header independently of body.stream.
	// Preserve it only after the complete native-client detector succeeds.
	if confirmedClaudeCode && helps.HeaderValueCaseInsensitive(incomingHeaders, "X-Stainless-Async") == "async" {
		r.Header.Set("X-Stainless-Async", "async")
	}
	// Claude Code omits X-Stainless-Timeout on count_tokens; only a confirmed
	// native client that sent one of its own keeps it there.
	if !countTokens {
		identityHeader("X-Stainless-Timeout", hdrDefault(hd.Timeout, "600"))
	} else if confirmedClaudeCode {
		if incomingTimeout := helps.HeaderValueCaseInsensitive(incomingHeaders, "X-Stainless-Timeout"); incomingTimeout != "" {
			r.Header.Set("X-Stainless-Timeout", incomingTimeout)
		}
	}
	// Selected-credential OAuth identity is an explicit native passthrough
	// exception. Callers pass the same agent-conversation UUID written to
	// metadata.user_id; legacy paths retain their previous cached fallback.
	sessionID := ""
	for _, candidate := range sessionIDs {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			sessionID = candidate
			break
		}
	}
	if sessionID != "" {
		r.Header.Set("X-Claude-Code-Session-Id", sessionID)
	} else {
		var errSessionID error
		sessionID, errSessionID = helps.CachedSessionIDRequired(r.Context(), apiKey)
		if errSessionID != nil {
			return errSessionID
		}
		identityHeader("X-Claude-Code-Session-Id", sessionID)
	}
	// Preserve native Claude Code subagent and environment headers when present in the incoming request.
	for _, hdr := range []string{
		"X-Claude-Code-Agent-Id",
		"X-Claude-Code-Parent-Agent-Id",
		"X-Claude-Remote-Container-Id",
		"X-Claude-Remote-Session-Id",
		"X-Client-App",
		"X-Anthropic-Additional-Protection",
	} {
		if val := helps.HeaderValueCaseInsensitive(incomingHeaders, hdr); val != "" {
			r.Header.Set(hdr, val)
		}
	}
	// Per-request UUID, matches Claude Code's x-client-request-id for first-party API.
	// identityHeader prefers the incoming value for a confirmed client, so a confirmed
	// helper keeps its own native request ID and this fresh UUID only covers a caller
	// that sent none. Helpers opt in on custom gateways too, but only to carry the
	// request ID they already sent: 2.1.258 attaches the header just for a first-party
	// base URL, so a helper that arrived without one keeps it absent upstream rather
	// than gaining a synthesized ID the real client would not have sent.
	if isAnthropicBase || (helperProfile && helps.HeaderValueCaseInsensitive(incomingHeaders, "x-client-request-id") != "") {
		identityHeader("x-client-request-id", uuid.New().String())
	}
	r.Header.Set("Connection", "keep-alive")
	// Regular Claude Code requests negotiate transport identically for streaming
	// and non-streaming requests. Claude Code 2.1.258 Haiku helpers offer the same
	// full compression set as the main thread (2.1.220's minimal probe offered gzip
	// only). Confirmed helpers preserve the incoming native values.
	applyTransportNegotiation := func() {
		if helperProfile {
			identityHeader("Accept", "application/json")
			identityHeader("Accept-Encoding", "gzip, deflate, br, zstd")
			return
		}
		if stream && !isAnthropicBase {
			// Other Anthropic-compatible upstreams (Kimi, custom gateways) may select
			// SSE from Accept and need not compress predictably, so they keep the
			// conservative contract.
			r.Header.Set("Accept", "text/event-stream")
			r.Header.Set("Accept-Encoding", "identity")
			return
		}
		r.Header.Set("Accept", "application/json")
		r.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	}
	applyTransportNegotiation()
	// Confirmed Claude Code requests may contribute their real software profile.
	// Unconfirmed clients always receive the CLI baseline instead of being
	// allowed to populate or reuse another client's software profile.
	if stabilizeDeviceProfile {
		if confirmedClaudeCode {
			helps.ApplyClaudeDeviceProfileHeaders(r, deviceProfile)
		} else {
			helps.ApplyClaudeDefaultDeviceProfileHeaders(r, cfg)
		}
	} else {
		helps.ApplyClaudeLegacyDeviceHeaders(r, incomingHeaders, cfg, confirmedClaudeCode)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(r, attrs, incomingHeaders)
	// Custom credential headers are a configuration escape hatch for third-party
	// gateways, so they keep the last word there. On api.anthropic.com they must
	// not rewrite the reconstructed identity: an overridden Anthropic-Beta yields a
	// combination real Claude Code never sends and the API rejects, and an
	// overridden Accept-Encoding contradicts the negotiated transport. Both were
	// reachable because this ran after the whole header set was assembled.
	if isAnthropicBase {
		r.Header.Set("Anthropic-Beta", baseBetas)
		applyTransportNegotiation()
	} else if stream {
		// Elsewhere only streaming is protected, so an Accept override cannot
		// silently disable event negotiation.
		applyTransportNegotiation()
	}
	return nil
}

// doClaudeUpstreamRequest is the single send boundary for every Claude upstream
// call. Folding the wire-casing pass in here makes it structurally impossible
// for one of the three request paths to drift away from the others, which is
// exactly how the streaming and non-streaming beta sets diverged before.
func doClaudeUpstreamRequest(client *http.Client, req *http.Request) (*http.Response, error) {
	applyClaudeWireHeaderCasing(req)
	cliproxyexecutor.MarkUpstreamAttempt(req.Context())
	return client.Do(req)
}

// claudeWireHeaderCasing maps Go's canonical header name to the exact casing
// Claude Code 2.1.220 puts on the wire. Only the names that differ are listed;
// the other twelve already survive canonicalisation unchanged.
var claudeWireHeaderCasing = map[string]string{
	"X-Stainless-Os":      "X-Stainless-OS",
	"Anthropic-Beta":      "anthropic-beta",
	"Anthropic-Version":   "anthropic-version",
	"X-App":               "x-app",
	"X-Client-Request-Id": "x-client-request-id",

	"Anthropic-Dangerous-Direct-Browser-Access": "anthropic-dangerous-direct-browser-access",
}

// applyClaudeWireHeaderCasing restores the header name casing of the real client.
//
// CPA negotiates ALPN http/1.1 with Anthropic, so header names reach the server
// verbatim rather than lowercased by HPACK, which makes casing observable. Go
// canonicalises every name passed through Header.Set, turning the client's
// anthropic-beta and x-app into Anthropic-Beta and X-App. Writing the map keys
// directly is the only way to keep the original casing.
//
// This also fixes ordering for free: Go sorts header names bytewise when it
// serialises them, and the real client's order is exactly that same bytewise
// sort, so correct casing reproduces the correct order. Host, User-Agent and
// Content-Length remain misplaced because Go writes them ahead of the sorted
// block; that needs transport-level surgery and is out of scope here.
//
// Call this immediately before handing the request to the client and nowhere
// else. The rewritten keys are unreachable through Header.Get, which
// canonicalises its argument, so running it any earlier would silently hide
// these headers from the rest of the pipeline.
func applyClaudeWireHeaderCasing(r *http.Request) {
	if r == nil || r.Header == nil || !isAnthropicUpstreamURL(r.URL) {
		return
	}
	for canonical, wire := range claudeWireHeaderCasing {
		values, ok := r.Header[canonical]
		if !ok {
			continue
		}
		delete(r.Header, canonical)
		r.Header[wire] = values
	}
}

func claudeCreds(a *cliproxyauth.Auth) (apiKey, baseURL string) {
	if a == nil {
		return "", ""
	}
	if a.Attributes != nil {
		apiKey = a.Attributes["api_key"]
		baseURL = a.Attributes["base_url"]
	}
	if apiKey == "" {
		apiKey = claudeauth.ReadMetadataString(&a.Metadata, "access_token")
	}
	return
}

// claudePayloadHasMidSystemMessage reports whether the caller placed a
// {"role":"system"} turn inside messages.
func claudePayloadHasMidSystemMessage(payload []byte) bool {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return false
	}
	found := false
	messages.ForEach(func(_, message gjson.Result) bool {
		if strings.EqualFold(strings.TrimSpace(message.Get("role").String()), "system") {
			found = true
			return false
		}
		return true
	})
	return found
}

func rebuildMidSystemMessagesToTopLevel(payload []byte) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload
	}

	var movedSystemParts []string
	keptMessages := make([]string, 0, int(messages.Get("#").Int()))
	messages.ForEach(func(_, message gjson.Result) bool {
		if strings.EqualFold(strings.TrimSpace(message.Get("role").String()), "system") {
			movedSystemParts = append(movedSystemParts, claudeSystemTextParts(message.Get("content"))...)
			return true
		}
		keptMessages = append(keptMessages, message.Raw)
		return true
	})
	if len(movedSystemParts) == 0 {
		return payload
	}

	systemParts := claudeSystemTextParts(gjson.GetBytes(payload, "system"))
	systemParts = append(systemParts, movedSystemParts...)
	if len(systemParts) > 0 {
		if updated, errSetSystem := sjson.SetRawBytes(payload, "system", rawJSONArray(systemParts)); errSetSystem == nil {
			payload = updated
		}
	}
	if updated, errSetMessages := sjson.SetRawBytes(payload, "messages", rawJSONArray(keptMessages)); errSetMessages == nil {
		payload = updated
	}
	return payload
}

func claudeSystemTextParts(content gjson.Result) []string {
	if !content.Exists() {
		return nil
	}
	if content.Type == gjson.String {
		text := content.String()
		if strings.TrimSpace(text) == "" {
			return nil
		}
		block := []byte(`{"type":"text","text":""}`)
		block, _ = sjson.SetBytes(block, "text", text)
		return []string{string(block)}
	}
	if !content.IsArray() {
		return nil
	}

	var parts []string
	content.ForEach(func(_, item gjson.Result) bool {
		if item.Type == gjson.String {
			text := item.String()
			if strings.TrimSpace(text) != "" {
				block := []byte(`{"type":"text","text":""}`)
				block, _ = sjson.SetBytes(block, "text", text)
				parts = append(parts, string(block))
			}
			return true
		}
		if item.IsObject() && item.Get("type").String() == "text" && strings.TrimSpace(item.Get("text").String()) != "" {
			parts = append(parts, item.Raw)
		}
		return true
	})
	return parts
}

func rawJSONArray(items []string) []byte {
	if len(items) == 0 {
		return []byte("[]")
	}
	var builder strings.Builder
	builder.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(item)
	}
	builder.WriteByte(']')
	return []byte(builder.String())
}

func isClaudeOAuthToken(apiKey string) bool {
	return strings.Contains(apiKey, "sk-ant-oat")
}

type claudeMCPAliasOptions struct {
	secret string
}

func resolveClaudeMCPAliasOptions(ctx context.Context) claudeMCPAliasOptions {
	// Alias identity belongs to the downstream caller, not to the selected
	// upstream credential. This keeps names stable across OAuth refresh and auth
	// failover while giving one caller a shared virtual MCP server component.
	secret := strings.TrimSpace(helps.APIKeyFromContext(ctx))
	if secret == "" {
		secret = "cpa-claude-mcp-default-caller"
	}
	return claudeMCPAliasOptions{secret: secret}
}

// prepareClaudeOAuthToolNamesForUpstream applies one request-local MCP symbol
// table across every Claude OAuth request path.
func prepareClaudeOAuthToolNamesForUpstream(body []byte, mcpAliases claudeMCPAliasOptions) ([]byte, map[string]string) {
	return remapOAuthToolNamesWithOptions(body, mcpAliases)
}

func restoreClaudeOAuthToolNamesFromResponse(body []byte, reverseMap map[string]string) ([]byte, error) {
	return reverseRemapOAuthToolNames(body, reverseMap)
}

func restoreClaudeOAuthToolNamesFromStreamLine(line []byte, reverseMap map[string]string) ([]byte, error) {
	return reverseRemapOAuthToolNamesFromStreamLine(line, reverseMap)
}
