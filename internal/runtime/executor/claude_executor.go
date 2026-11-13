package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	sigcompat "github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	responsesclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// ClaudeExecutor handles Anthropic Claude over the messages API and keeps bounded
// in-memory thread alias state for OAuth continuations.
// If api_key is unavailable on auth, it falls back to legacy via ClientAdapter.
type ClaudeExecutor struct {
	cfg                     *config.Config
	requestLogProvider      string
	upstreamModelNormalizer func(string) string
	oauthProfileFetcher     claudeOAuthProfileFetcher
	// oauthToolAliases is shared by every ForAPIKey copy of this executor;
	// constructors allocate it so copies made per request reuse one store.
	oauthToolAliases *claudeOAuthToolAliasStore
	threadBetas      *helps.ClaudeThreadBetaStore
}

type claudeOAuthCancellationError struct {
	cause error
}

func (e *claudeOAuthCancellationError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *claudeOAuthCancellationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *claudeOAuthCancellationError) IsRequestScoped() bool {
	return e != nil
}

func newClaudeOAuthCancellationError(ctx context.Context, oauth bool, err error) error {
	if !oauth {
		return nil
	}
	cause := err
	if ctx != nil && ctx.Err() != nil {
		cause = ctx.Err()
	}
	if !errors.Is(cause, context.Canceled) {
		return nil
	}
	return &claudeOAuthCancellationError{cause: cause}
}

func shouldSanitizeClaudeMessagesForUpstream(baseModel string) bool {
	return sigcompat.SignatureProviderFromModelName(baseModel) == sigcompat.SignatureProviderClaude
}

func sanitizeClaudeMessagesForClaudeUpstreamWithDebug(ctx context.Context, body []byte, baseModel string, preserveEmptyThinkingBlocks ...bool) []byte {
	doc := ojson.NewDoc(body)
	if !sanitizeClaudeMessagesForClaudeUpstreamWithDebugDoc(ctx, doc, baseModel, preserveEmptyThinkingBlocks...) {
		return body
	}
	return doc.Bytes()
}

func sanitizeClaudeMessagesForClaudeUpstreamWithDebugDoc(ctx context.Context, doc *ojson.Doc, baseModel string, preserveEmptyThinkingBlocks ...bool) bool {
	if doc == nil {
		return false
	}
	// The internal commentary marker must never reach any upstream, so its
	// restore runs unconditionally, independent of the provider signature
	// sanitization gate below (which non-Claude model names skip entirely).
	changed := sigcompat.RestoreCodexCommentaryMarkersDoc(doc) > 0
	preserveEmpty := len(preserveEmptyThinkingBlocks) > 0 && preserveEmptyThinkingBlocks[0]
	if shouldSanitizeClaudeMessagesForUpstream(baseModel) || preserveEmpty {
		report, sanitized := sanitizeClaudeMessagesForClaudeUpstreamDoc(doc, baseModel, preserveEmpty)
		if sanitized {
			changed = true
		}
		logClaudeSignatureSanitizeReport(ctx, baseModel, report)
	}
	if sanitizeClaudeWebSearchDomainsDoc(doc) {
		changed = true
	}
	if helps.NormalizeClaudeToolCallIDsDoc(doc) {
		changed = true
	}
	return changed
}

func sanitizeClaudeMessagesForClaudeUpstreamDoc(doc *ojson.Doc, targetModel string, preserveEmpty bool) (sigcompat.SignatureSanitizeReport, bool) {
	report := sigcompat.SignatureSanitizeReport{TargetProvider: sigcompat.SignatureProviderClaude}
	if doc == nil {
		return report, false
	}
	messages := doc.Get("messages")
	if !messages.IsArray() {
		return report, false
	}
	messageResults := messages.Array()
	modified := false
	var droppedMessages []int

	for i, message := range messageResults {
		content := message.Get("content")
		if !content.IsArray() {
			continue
		}
		contentResults := content.Array()
		var droppedParts []int

		for j, part := range contentResults {
			partPath := fmt.Sprintf("messages.%d.content.%d", i, j)
			partType := part.Get("type").String()
			if partType == "tool_use" {
				if stripClaudeToolUseProvenanceFieldsDoc(doc, partPath) {
					modified = true
					report.DroppedSignatures++
				}
				continue
			}
			if partType != "thinking" {
				continue
			}
			rawSignature := part.Get("signature").String()
			if sigcompat.IsCodexCommentarySignatureMarker(rawSignature) {
				if text := part.Get("thinking").String(); text != "" {
					_ = doc.Set(partPath+".type", "text")
					_ = doc.Delete(partPath + ".thinking")
					_ = doc.Delete(partPath + ".signature")
					_ = doc.Set(partPath+".text", text)
					report.DroppedSignatures++
					modified = true
					continue
				}
				report.DroppedBlocks++
				droppedParts = append(droppedParts, j)
				modified = true
				continue
			}
			if preserveEmpty {
				report.Preserved++
				continue
			}
			decision := sigcompat.DecideSignatureCompatibilityForModel(sigcompat.SignatureProviderClaude, targetModel, rawSignature, sigcompat.SignatureBlockKindClaudeThinking)
			decision.Reason = fmt.Sprintf("messages[%d].content[%d]: %s", i, j, decision.Reason)
			report.Decisions = append(report.Decisions, decision)

			switch decision.Action {
			case sigcompat.SignatureActionPreserve:
				report.Preserved++
				if decision.NormalizedSignature != "" && decision.NormalizedSignature != rawSignature {
					_ = doc.Set(partPath+".signature", decision.NormalizedSignature)
					modified = true
				}
			case sigcompat.SignatureActionReplaceWithGeminiBypass:
				report.ReplacedSignatures++
				_ = doc.Set(partPath+".signature", decision.ReplacementSignature)
				modified = true
			case sigcompat.SignatureActionDropSignature:
				report.DroppedSignatures++
				_ = doc.Delete(partPath + ".signature")
				modified = true
			default:
				report.DroppedBlocks++
				droppedParts = append(droppedParts, j)
				modified = true
			}
		}

		if len(droppedParts) > 0 {
			if len(droppedParts) == len(contentResults) {
				droppedMessages = append(droppedMessages, i)
				continue
			}
			for k := len(droppedParts) - 1; k >= 0; k-- {
				_ = doc.Delete(fmt.Sprintf("messages.%d.content.%d", i, droppedParts[k]))
			}
		}
	}

	for k := len(droppedMessages) - 1; k >= 0; k-- {
		_ = doc.Delete(fmt.Sprintf("messages.%d", droppedMessages[k]))
	}
	return report, modified
}

func stripClaudeToolUseProvenanceFieldsDoc(doc *ojson.Doc, partPath string) bool {
	changed := false
	for _, relPath := range []string{"signature", "thoughtSignature", "thought_signature", "extra_content.google.thought_signature", "model"} {
		sigPath := partPath + "." + relPath
		if !doc.Get(sigPath).Exists() {
			continue
		}
		if doc.Delete(sigPath) == nil {
			changed = true
		}
	}
	for _, objPath := range []string{partPath + ".extra_content.google", partPath + ".extra_content"} {
		res := doc.Get(objPath)
		if res.Exists() && res.IsObject() && len(res.Map()) == 0 {
			if doc.Delete(objPath) == nil {
				changed = true
			}
		}
	}
	return changed
}

// sanitizeClaudeWebSearchDomains removes empty allowed_domains/blocked_domains
// arrays from built-in web_search tools. Some clients (e.g. litellm) emit an
// empty array instead of omitting the field, and Anthropic rejects it with
// "Empty list of domains is ambiguous. Provide at least one domain or null.".
// Deleting the key is equivalent to leaving it unset.
func sanitizeClaudeWebSearchDomains(body []byte) []byte {
	tools := ojson.GetBytesNoCopy(body, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return body
	}
	doc := ojson.NewDoc(body)
	if !sanitizeClaudeWebSearchDomainsDoc(doc) {
		return body
	}
	return doc.Bytes()
}

func sanitizeClaudeWebSearchDomainsDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	tools := doc.Get("tools")
	if !tools.Exists() || !tools.IsArray() {
		return false
	}
	changed := false
	tools.ForEach(func(index, tool ojson.Result) bool {
		if !strings.HasPrefix(tool.Get("type").String(), "web_search_") {
			return true
		}
		for _, field := range []string{"allowed_domains", "blocked_domains"} {
			value := tool.Get(field)
			if value.Exists() && value.IsArray() && len(value.Array()) == 0 {
				path := fmt.Sprintf("tools.%d.%s", index.Int(), field)
				if errDelete := doc.Delete(path); errDelete == nil {
					changed = true
				}
			}
		}
		return true
	})
	return changed
}

func logClaudeSignatureSanitizeReport(ctx context.Context, baseModel string, report sigcompat.SignatureSanitizeReport) {
	if report.DroppedBlocks == 0 && report.DroppedSignatures == 0 && report.ReplacedSignatures == 0 {
		return
	}

	fields := log.Fields{
		"component":           "signature_sanitizer",
		"executor":            "claude",
		"action":              "sanitize_claude_messages",
		"target_provider":     string(report.TargetProvider),
		"target_model":        baseModel,
		"preserved":           report.Preserved,
		"dropped_blocks":      report.DroppedBlocks,
		"dropped_signatures":  report.DroppedSignatures,
		"replaced_signatures": report.ReplacedSignatures,
	}
	if len(report.Decisions) > 0 {
		decision := report.Decisions[0]
		fields["first_block_kind"] = string(decision.BlockKind)
		fields["first_detected_provider"] = string(decision.DetectedProvider)
		fields["first_reason"] = decision.Reason
	}

	helps.LogWithRequestID(ctx).WithFields(fields).Debug("claude executor: sanitized signature history before upstream")
}

// Anthropic-compatible upstreams may reject or even crash when Claude models
// omit max_tokens. Prefer registered model metadata before using a fallback.
const defaultModelMaxTokens = 1024

// stripTranslatedDefaultMaxTokens removes the translator-synthesized default
// max_tokens from originalTranslated when the caller's original request did not
// explicitly supply an output token limit in the source protocol, allowing
// payload.default rules targeting max_tokens to take effect.
func stripTranslatedDefaultMaxTokens(from sdktranslator.Format, originalPayload, originalTranslated []byte) []byte {
	if len(originalTranslated) == 0 || from == sdktranslator.FormatClaude {
		return originalTranslated
	}
	hasExplicitMaxTokens := func(paths ...string) bool {
		for _, path := range paths {
			if v := ojson.GetBytesNoCopy(originalPayload, path); v.Exists() && v.Type != ojson.Null {
				return true
			}
		}
		return false
	}
	switch from {
	case sdktranslator.FormatOpenAI:
		if hasExplicitMaxTokens("max_tokens", "max_completion_tokens") {
			return originalTranslated
		}
	case sdktranslator.FormatOpenAIResponse:
		if hasExplicitMaxTokens("max_output_tokens") {
			return originalTranslated
		}
	case sdktranslator.FormatGemini:
		if hasExplicitMaxTokens("generationConfig.maxOutputTokens") {
			return originalTranslated
		}
	default:
		return originalTranslated
	}
	stripped, errDelete := ojson.DeleteBytes(originalTranslated, "max_tokens")
	if errDelete != nil {
		return originalTranslated
	}
	return stripped
}

func NewClaudeExecutor(cfg *config.Config) *ClaudeExecutor {
	return &ClaudeExecutor{cfg: cfg, oauthToolAliases: &claudeOAuthToolAliasStore{}, threadBetas: &helps.ClaudeThreadBetaStore{}}
}

func (e *ClaudeExecutor) prepareClaudeThreadBetas(ctx context.Context, request *http.Request, auth *cliproxyauth.Auth, body []byte, extraBetas []string, payloadBetasConfigured bool, incomingHeaders http.Header, uncloak bool, metadataSets ...map[string]any) (*helps.ClaudeThreadBetaRequest, error) {
	if !ojson.GetBytesNoCopy(body, "thread").IsObject() {
		return nil, nil
	}
	apiKey, _ := claudeCreds(auth)
	fp := resolveClaudeFingerprintPolicy(e.cfg, auth, apiKey)
	identity := claudeDiagnosticsCredentialIdentity(auth)
	if identity == "" {
		identity = apiKey
	}
	caller := resolveClaudeMCPAliasOptions(ctx, metadataSets...).secret
	scope := sha256.Sum256([]byte(e.upstreamRequestLogProvider() + "\x00" + request.URL.Scheme + "://" + request.URL.Host + request.URL.Path + "\x00" + identity + "\x00" + caller))
	var hd config.ClaudeHeaderDefaults
	if e.cfg != nil {
		hd = e.cfg.ClaudeHeaderDefaults
	}
	var customBetas []string
	betaAttrs := make(map[string]string)
	if auth != nil && !isAnthropicUpstreamURL(request.URL) {
		for name, value := range auth.Attributes {
			if strings.EqualFold(name, "header:Anthropic-Beta") {
				customBetas = append(customBetas, value)
				betaAttrs[name] = value
			}
		}
	}
	sort.Strings(customBetas)
	customBetaApplied := false
	if len(customBetas) > 0 {
		probe := (&http.Request{Header: make(http.Header)}).WithContext(request.Context())
		util.ApplyCustomHeadersFromAttrs(probe, betaAttrs, incomingHeaders)
		resolved := helps.HeaderValuesCaseInsensitive(probe.Header, "Anthropic-Beta")
		customBetaApplied = len(resolved) > 0
		customBetas = append(customBetas, strings.Join(resolved, ","))
	}
	policy := sha256.Sum256([]byte(fmt.Sprintf("%q\x00%q\x00%q\x00%t\x00%t", hd.AnthropicBetaStrip, hd.AnthropicBetaAppend, customBetas, uncloak, fp.UseOAuthBetas)))
	// Literal executors need the same lazy-allocation protection as alias state.
	claudeOAuthToolAliasStoreInitMu.Lock()
	if e.threadBetas == nil {
		e.threadBetas = &helps.ClaudeThreadBetaStore{}
	}
	store := e.threadBetas
	claudeOAuthToolAliasStoreInitMu.Unlock()
	// Track only body declarations that actually contribute independent flags.
	independent := claudeRequestedBetas(strings.Join(helps.HeaderValuesCaseInsensitive(incomingHeaders, "Anthropic-Beta"), ","), hd.AnthropicBetaAppend)
	actual := claudeRequestedBetas(strings.Join(helps.HeaderValuesCaseInsensitive(request.Header, "Anthropic-Beta"), ","), nil)
	var bodyBetas []string
	for _, beta := range extraBetas {
		beta = strings.TrimSpace(beta)
		credentialBeta := fp.UseOAuthBetas && (beta == claudeOAuthBeta || beta == claudeExtendedCacheTTLBeta)
		if !customBetaApplied && actual[beta] && !independent[beta] && !credentialBeta {
			bodyBetas = append(bodyBetas, beta)
			independent[beta] = true
		}
	}
	state, found, errBind := store.Bind(string(scope[:]), ojson.GetBytesNoCopy(body, "thread.previous_message_id").String(), request.Header, helps.ClaudeThreadBetaPolicy{Fingerprint: hex.EncodeToString(policy[:]), BodyBetas: strings.Join(bodyBetas, ","), PayloadConfigured: payloadBetasConfigured})
	if errBind != nil {
		return nil, errBind
	}
	if !found {
		return nil, helps.ClaudeThreadBetaConflictError{}
	}
	// The restored profile must satisfy this final business payload on every path.
	declared := claudeRequestedBetas(strings.Join(helps.HeaderValuesCaseInsensitive(request.Header, "Anthropic-Beta"), ","), nil)
	for beta, needed := range map[string]bool{
		claudeFastModeBeta:         strings.EqualFold(ojson.GetBytesNoCopy(body, "speed").String(), "fast"),
		claudeAdvisorToolBeta:      claudeBodyHasAdvisorTool(body),
		claudeThinkingBindingBeta:  ojson.GetBytesNoCopy(body, "thinking.block_binding").Exists(),
		claudeExtendedCacheTTLBeta: helps.ClaudePayloadHas1hTTL(body),
	} {
		if needed && !declared[beta] {
			return nil, helps.ClaudeThreadBetaConflictError{}
		}
	}
	selectedAuthID := ""
	for _, metadata := range metadataSets {
		if selectedAuthID = metadataString(metadata, cliproxyexecutor.SelectedAuthMetadataKey); selectedAuthID != "" {
			break
		}
	}
	if auth != nil && auth.ID != "" && selectedAuthID == auth.ID && e.upstreamRequestLogProvider() == "claude" {
		ownerScope := cliproxysession.ClaudeThreadCallerScope(helps.APIKeyFromContext(ctx), metadataSets...)
		authID := strings.Clone(auth.ID)
		state.OnCommit(func(messageID string) {
			cliproxysession.RememberClaudeThreadAuth(ownerScope, messageID, authID)
		})
	}
	return state, nil
}

// WithConfig rebinds configuration without discarding successful Thread state.
// Like ForAPIKey, the copy shares pointer-owned stores and copies no mutexes.
func (e ClaudeExecutor) WithConfig(cfg *config.Config) *ClaudeExecutor {
	e.cfg = cfg
	return &e
}

func (e *ClaudeExecutor) Identifier() string { return "claude" }

func (e *ClaudeExecutor) claudeWebSearchResultCacheScope(ctx context.Context, auth *cliproxyauth.Auth, from sdktranslator.Format, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, originalPayload []byte) (string, time.Duration, int) {
	if e == nil || e.cfg == nil || !e.cfg.Client.Codex.WebSearchResultCache.Enabled {
		return "", 0, 0
	}
	if !sourceFormatEqual(from, sdktranslator.FormatOpenAIResponse) {
		return "", 0, 0
	}
	sessionKey := xaiReasoningReplayIsolateSessionKey(ctx, codexReasoningReplaySessionKey(ctx, from, req, opts, originalPayload))
	if sessionKey == "" {
		return "", 0, 0
	}
	if credScope := claudeWebSearchResultCacheCredentialScope(auth); credScope != "" {
		sessionKey = credScope + ":" + sessionKey
	}
	cacheCfg := e.cfg.Client.Codex.WebSearchResultCache
	return sessionKey, cacheCfg.EffectiveTTL(), cacheCfg.EffectiveLimit()
}

func claudeWebSearchResultCacheCredentialScope(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	identity := strings.TrimSpace(auth.ID)
	if identity == "" {
		identity = helps.ClaudeCLIAuthIdentitySeed(auth)
	}
	if identity == "" {
		apiKey, baseURL := claudeCreds(auth)
		identity = strings.TrimSpace(apiKey)
		if identity == "" {
			identity = strings.TrimSpace(baseURL)
		}
	}
	if identity == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(identity))
	return "claude:" + hex.EncodeToString(sum[:8])
}

var _ cliproxyauth.NativeToolSearchSupport = (*ClaudeExecutor)(nil)

// SupportsNativeToolSearch reports whether a credential can receive Codex
// tool_search as Anthropic native tool search (defer_loading tools and
// tool_reference results) for the given upstream model. Anthropic's own origin
// qualifies; a custom base URL qualifies only when native-tool-search is
// enabled for it. Models that predate tool search never qualify, and nothing
// qualifies while upstream.claude.tool-search is off. That switch is read from
// the translator so the executor and the translator always agree on it.
func (e *ClaudeExecutor) SupportsNativeToolSearch(auth *cliproxyauth.Auth, upstreamModel string) bool {
	return e.supportsNativeToolSearch(responsesclaude.ClientToolSearchEnabled(), auth, upstreamModel)
}

func (e *ClaudeExecutor) supportsNativeToolSearch(toolSearch bool, auth *cliproxyauth.Auth, upstreamModel string) bool {
	if !toolSearch || helps.ClaudeModelLacksToolSearch(upstreamModel) {
		return false
	}
	_, baseURL := claudeCreds(auth)
	if baseURL = strings.TrimSpace(baseURL); baseURL == "" || isAnthropicUpstreamBase(baseURL) {
		return true
	}
	if auth != nil && auth.Attributes != nil && strings.EqualFold(strings.TrimSpace(auth.Attributes["native_tool_search"]), "true") {
		return true
	}
	entry := resolveClaudeKeyConfig(e.cfg, auth)
	return entry != nil && entry.NativeToolSearch
}

// applyNativeToolSearchFallback removes Codex tool_search from a Responses
// request before translation when the credential cannot use native tool
// search, so the translated body matches the pre-tool-search translation.
// While upstream.claude.tool-search is off the translator already produces
// that translation from the unmodified request, so nothing is removed.
//
// The returned context pins upstream.claude.tool-search for the rest of the
// request, so this decision and the request and response translations agree
// even if the configuration is reloaded mid-request.
func (e *ClaudeExecutor) applyNativeToolSearchFallback(ctx context.Context, auth *cliproxyauth.Auth, from sdktranslator.Format, upstreamModel string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (context.Context, cliproxyexecutor.Request, cliproxyexecutor.Options) {
	if from != sdktranslator.FormatOpenAIResponse {
		return ctx, req, opts
	}
	toolSearch := responsesclaude.ClientToolSearchEnabledFor(ctx)
	ctx = responsesclaude.WithClientToolSearch(ctx, toolSearch)
	if !toolSearch {
		return ctx, req, opts
	}
	if !helps.ResponsesHasToolSearch(req.Payload) && !helps.ResponsesHasToolSearch(opts.OriginalRequest) {
		return ctx, req, opts
	}
	if e.supportsNativeToolSearch(toolSearch, auth, upstreamModel) {
		return ctx, req, opts
	}
	req.Payload = helps.StripResponsesToolSearch(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		opts.OriginalRequest = helps.StripResponsesToolSearch(opts.OriginalRequest)
	}
	log.Debugf("claude executor: native tool search unavailable, removed tool_search from request (model=%s)", upstreamModel)
	return ctx, req, opts
}

// pinResponsesSystemItemPlacement decides, before translation, whether the
// Responses translator keeps mid-session system items in place as role=system
// turns, which keeps the prompt-cache prefix of earlier turns stable. The turn
// needs the cloaked Claude Code wire, which sends the mid-conversation system
// beta, and a model that accepts it. Strict cloaking drops every caller system
// instruction and rebuild_mid_system_message folds the turns into the system
// field, so both keep the top-level path, as does an uncloaked request, which
// forwards the caller's betas unchanged.
func (e *ClaudeExecutor) pinResponsesSystemItemPlacement(ctx context.Context, auth *cliproxyauth.Auth, from sdktranslator.Format, upstreamModel string, policy claudeWirePolicy, settings claudeCloakSettings) context.Context {
	if from != sdktranslator.FormatOpenAIResponse {
		return ctx
	}
	inPlace := policy.Cloak &&
		!settings.strictMode &&
		!util.IsClaudeLegacySystemReminderModel(upstreamModel) &&
		!rebuildMidSystemMessageEnabled(e.cfg, auth)
	return responsesclaude.WithTopLevelSystemItems(ctx, !inPlace)
}

func (e *ClaudeExecutor) modelLevelCooling() bool {
	return e != nil && e.cfg != nil && e.cfg.Claude.ModelLevelCooling
}

func (e *ClaudeExecutor) upstreamRequestLogProvider() string {
	if provider := strings.TrimSpace(e.requestLogProvider); provider != "" {
		return provider
	}
	return e.Identifier()
}

func (e *ClaudeExecutor) upstreamModel(baseModel string) string {
	if e.upstreamModelNormalizer != nil {
		return e.upstreamModelNormalizer(baseModel)
	}
	return baseModel
}

func (e *ClaudeExecutor) restoreResponseModel(payload []byte, model string) []byte {
	if e.upstreamModelNormalizer == nil || strings.TrimSpace(model) == "" {
		return payload
	}
	return restoreClaudeResponseModel(payload, model)
}

func restoreClaudeResponseModel(payload []byte, model string) []byte {
	if updated, changed := setClaudeResponseModel(payload, model); changed {
		return updated
	}

	trimmed := bytes.TrimSpace(payload)
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return payload
	}
	dataIndex := bytes.Index(payload, []byte("data:"))
	if dataIndex < 0 {
		return payload
	}
	rawJSON := bytes.TrimSpace(payload[dataIndex+len("data:"):])
	updated, changed := setClaudeResponseModel(rawJSON, model)
	if !changed {
		return payload
	}
	rebuilt := make([]byte, 0, dataIndex+len("data: ")+len(updated))
	rebuilt = append(rebuilt, payload[:dataIndex]...)
	rebuilt = append(rebuilt, []byte("data: ")...)
	rebuilt = append(rebuilt, updated...)
	return rebuilt
}

func setClaudeResponseModel(payload []byte, model string) ([]byte, bool) {
	if !ojson.ValidBytes(payload) {
		return payload, false
	}
	doc := ojson.NewDoc(payload)
	changed := false
	for _, path := range []string{"model", "message.model"} {
		if !doc.Get(path).Exists() {
			continue
		}
		if errSet := doc.Set(path, model); errSet == nil {
			changed = true
		}
	}
	if !changed {
		return payload, false
	}
	return doc.Bytes(), true
}

// PrepareRequest injects Claude credentials into the outgoing HTTP request.
func (e *ClaudeExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	apiKey, _ := claudeCreds(auth)
	useAPIKey := auth != nil && (auth.AuthKind() == cliproxyauth.AuthKindAPIKey || (auth.Attributes != nil && strings.TrimSpace(auth.Attributes["api_key"]) != ""))
	isAnthropicBase := isAnthropicUpstreamURL(req.URL)
	if strings.TrimSpace(apiKey) != "" {
		if isAnthropicBase && useAPIKey {
			req.Header.Del("Authorization")
			req.Header.Set("x-api-key", apiKey)
		} else {
			req.Header.Del("x-api-key")
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	} else {
		req.Header.Del("Authorization")
		req.Header.Del("x-api-key")
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects Claude credentials into the request and executes it.
func (e *ClaudeExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("claude executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// SupportsApplyPatch reports the actual executor contract, independent of its provider name.
func (e *ClaudeExecutor) SupportsApplyPatch() bool { return e != nil }
