package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type xaiPreparedRequest struct {
	baseModel             string
	from                  sdktranslator.Format
	responseFormat        sdktranslator.Format
	to                    sdktranslator.Format
	originalPayload       []byte
	body                  []byte
	namespaceTools        map[string]xaiNamespaceToolRef
	clientDeclaredTools   map[xaiClientToolKey]struct{}
	sessionID             string
	replayScope           xaiReasoningReplayScope
	filterInternalXSearch bool
}

type xaiNamespaceToolRef struct {
	namespace    string
	name         string
	isDispatcher bool
}

// xaiClientToolKey identifies a client-declared callable tool using the
// post-restore Responses shape (short name + optional namespace) and the
// effective upstream tool type after normalizeXAITool (client custom tools are
// sent as function). Response call types are matched against this effective
// kind so internal custom_tool_call traces are not exempted merely because a
// client declared an ordinary function/custom tool with the same short name,
// while legitimate function_call responses for normalized custom tools are kept.
type xaiClientToolKey struct {
	namespace string
	name      string
	toolType  string
}

func (e *XAIExecutor) prepareResponsesRequest(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (*xaiPreparedRequest, error) {
	return e.prepareResponsesRequestTo(ctx, req, opts, stream, sdktranslator.FormatCodex)
}

func (e *XAIExecutor) prepareResponsesRequestTo(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool, to sdktranslator.Format) (*xaiPreparedRequest, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := bytes.Clone(originalPayloadSource)
	originalTranslated := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, stream, helps.APIKeyModelIsCompat(req))
	originalTranslated = preserveXAIResponsesOutputControls(originalTranslated, originalPayload, from)
	body := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, bytes.Clone(req.Payload), stream, helps.APIKeyModelIsCompat(req))
	body = preserveXAIResponsesOutputControls(body, req.Payload, from)

	var err error
	body, err = helps.ApplyRequestThinking(body, req, opts, from.String(), e.Identifier(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body = helps.SetStringIfDifferent(body, "model", baseModel)
	body = helps.SetBoolIfDifferent(body, "stream", stream)
	body, _ = sjson.DeleteBytes(body, "previous_response_id")
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	body, _ = sjson.DeleteBytes(body, "stream_options")
	body = helps.RewriteCodexMultiAgentV2Input(ctx, opts.Headers, body, e.cfg)
	willInjectXSearch := e.cfg != nil && e.cfg.XAI.InjectXSearch
	shouldFold := xaiShouldFoldNamespaceTools(body, willInjectXSearch)
	namespaceTools := collectXAINamespaceToolRefsWithFold(body, shouldFold)
	// Collect before normalizeXAITools flattens namespace wrappers so keys match
	// the post-restore (namespace, short-name) shape used by the response filter.
	clientDeclaredTools := collectXAIClientDeclaredToolKeys(body)
	body = normalizeXAIToolsWithFold(body, shouldFold)
	body = promoteXAIAdditionalTools(body)
	// Drop choices that point at tools removed by normalizeXAITools before any
	// configured x_search injection, so no surviving choice references a deleted tool.
	body = normalizeXAINamespaceToolChoiceWithFold(body, shouldFold)
	body = normalizeXAIForcedWebSearchToolChoice(body)
	// Prune before rewriting image_generation choices so older models that still
	// strip the tool do not keep a leftover "required" selection.
	body = pruneXAIOrphanedToolChoice(body)
	body = normalizeXAIForcedImageGenerationToolChoice(body)
	body = normalizeXAIToolChoiceForTools(body)
	// Skip x_search injection when the request was forced to image_generation and
	// the remaining tools list is only that hosted tool. "required" plus extra
	// tools would let Grok call x_search instead of Imagine.
	if e.cfg != nil && e.cfg.XAI.InjectXSearch && !xaiToolChoiceRequiresImageGenerationOnly(body) {
		body = ensureXAINativeXSearchTool(body)
	}
	body = clampXAIToolsLimit(body, xaiMaxTools, namespaceTools)
	var replayScope xaiReasoningReplayScope
	body, replayScope, err = applyXAIReasoningReplayCacheRequired(ctx, from, req, opts, body)
	if err != nil {
		return nil, err
	}
	body = normalizeXAIInputCustomToolCalls(body)
	body = normalizeXAIInputNamespaceToolCallsWithFold(body, shouldFold)
	body = normalizeXAIInputReasoningItems(body)
	body = sanitizeXAIInputEncryptedContent(body)
	body = normalizeCodexInstructions(body)
	body = sanitizeXAIResponsesBody(body, baseModel)
	body = normalizeXAIImageRefs(body)

	sessionID, errSession := xaiResolveComposerSessionID(ctx, req, opts, baseModel)
	if errSession != nil {
		return nil, errSession
	}
	if sessionID != "" {
		body = helps.SetStringIfDifferent(body, "prompt_cache_key", sessionID)
	}

	return &xaiPreparedRequest{
		baseModel:             baseModel,
		from:                  from,
		responseFormat:        responseFormat,
		to:                    to,
		originalPayload:       originalPayload,
		body:                  body,
		namespaceTools:        namespaceTools,
		clientDeclaredTools:   clientDeclaredTools,
		sessionID:             sessionID,
		replayScope:           replayScope,
		filterInternalXSearch: xaiRequestHasNativeXSearch(body),
	}, nil
}

func (e *XAIExecutor) recordXAIRequest(ctx context.Context, auth *cliproxyauth.Auth, url string, headers http.Header, body []byte) {
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   headers,
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
}

func xaiCreds(auth *cliproxyauth.Auth) (token, baseURL string) {
	if auth == nil {
		return "", ""
	}
	if auth.Attributes != nil {
		token = strings.TrimSpace(auth.Attributes["api_key"])
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
	}
	if auth.Metadata != nil {
		if token == "" {
			token = xaiMetadataString(auth.Metadata, "access_token")
		}
		if baseURL == "" {
			baseURL = xaiMetadataString(auth.Metadata, "base_url")
		}
	}
	return token, baseURL
}

// xaiUsingAPI reports whether this xAI auth should use the official API path
// for HTTP chat and media. OAuth defaults to false to use Grok Build.
func xaiUsingAPI(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return true
	}
	if len(auth.Attributes) > 0 {
		if raw := strings.TrimSpace(auth.Attributes[xaiUsingAPIAttr]); raw != "" {
			parsed, errParse := strconv.ParseBool(raw)
			if errParse == nil {
				return parsed
			}
		}
	}
	if len(auth.Metadata) > 0 {
		raw, ok := auth.Metadata[xaiUsingAPIAttr]
		if ok && raw != nil {
			switch v := raw.(type) {
			case bool:
				return v
			case string:
				parsed, errParse := strconv.ParseBool(strings.TrimSpace(v))
				if errParse == nil {
					return parsed
				}
			default:
			}
		}
	}
	if raw := strings.TrimSpace(auth.Attributes["auth_kind"]); raw != "" {
		return !strings.EqualFold(raw, "oauth")
	}
	return !strings.EqualFold(xaiMetadataString(auth.Metadata, "auth_kind"), "oauth")
}

// xaiChatBaseURL returns the base URL for xAI HTTP chat and media (image/video) requests.
// When auth using_api is true, the official API base URL logic is used. When it
// is false (including its OAuth default), empty or official default base_url is
// rewritten to the CLI chat-proxy endpoint; an explicit non-default base_url is
// still honored.
// Websocket and compact transports intentionally do not use this helper:
// cli-chat-proxy does not implement /responses/compact (404) or websocket
// upgrades (405).
func xaiChatBaseURL(auth *cliproxyauth.Auth) string {
	_, baseURL := xaiCreds(auth)
	if xaiUsingAPI(auth) {
		if baseURL == "" {
			return xaiauth.DefaultAPIBaseURL
		}
		return baseURL
	}
	if baseURL != "" && !xaiIsDefaultAPIBaseURL(baseURL) {
		return baseURL
	}
	return xaiauth.CLIChatProxyBaseURL
}

// xaiCompactBaseURL returns the base URL for xAI /responses/compact requests.
// Compact must stay on the official API (or an explicit non-CLI-proxy base_url).
// Reusing xaiChatBaseURL would pin OAuth traffic to cli-chat-proxy, which returns
// 404 for /responses/compact and then cools down the auth pool as not_found.
func xaiCompactBaseURL(auth *cliproxyauth.Auth) string {
	_, baseURL := xaiCreds(auth)
	if baseURL == "" || xaiIsCLIChatProxyBaseURL(baseURL) {
		return xaiauth.DefaultAPIBaseURL
	}
	return baseURL
}

func xaiNormalizeBaseURL(baseURL string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/")
}

func xaiIsDefaultAPIBaseURL(baseURL string) bool {
	return xaiNormalizeBaseURL(baseURL) == xaiNormalizeBaseURL(xaiauth.DefaultAPIBaseURL)
}

func xaiIsCLIChatProxyBaseURL(baseURL string) bool {
	return xaiNormalizeBaseURL(baseURL) == xaiNormalizeBaseURL(xaiauth.CLIChatProxyBaseURL)
}

// xaiBaseURLSource classifies a resolved xAI base URL for logging.
func xaiBaseURLSource(baseURL string) string {
	switch {
	case xaiIsDefaultAPIBaseURL(baseURL):
		return "DefaultAPIBaseURL"
	case xaiIsCLIChatProxyBaseURL(baseURL):
		return "CLIChatProxyBaseURL"
	default:
		return "custom"
	}
}

// logXAIResolvedBaseURL emits a console log for the resolved upstream base URL.
func logXAIResolvedBaseURL(ctx context.Context, baseURL string) {
	helps.LogWithRequestID(ctx).Infof("xai: using base_url=%s source=%s", baseURL, xaiBaseURLSource(baseURL))
}

func applyXAIHeaders(r *http.Request, auth *cliproxyauth.Auth, token string, stream bool, sessionID string, clientHeaders ...http.Header) {
	applyXAIDefaultHeaders(r, token, stream, sessionID)
	applyXAICustomHeaders(r, auth, clientHeaders...)
}

func applyXAIDefaultHeaders(r *http.Request, token string, stream bool, sessionID string) {
	r.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(token) != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	} else {
		r.Header.Del("Authorization")
	}
	if stream {
		r.Header.Set("Accept", "text/event-stream")
	} else {
		r.Header.Set("Accept", "application/json")
	}
	r.Header.Set("Connection", "Keep-Alive")
	if sessionID != "" {
		r.Header.Set("x-grok-conv-id", sessionID)
	}
}

func applyXAICustomHeaders(r *http.Request, auth *cliproxyauth.Auth, clientHeaders ...http.Header) {
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(r, attrs, clientHeaders...)
}

// applyXAIChatHeaders applies standard xAI headers for non-image/video chat
// requests. When using_api is true, this matches the standard
// applyXAIHeaders behavior. CLI chat-proxy identity headers are only attached
// when using_api is false and the resolved chat base URL is the official CLI
// chat-proxy endpoint.
func applyXAIChatHeaders(r *http.Request, auth *cliproxyauth.Auth, token string, stream bool, sessionID string, clientHeaders ...http.Header) {
	if xaiUsingAPI(auth) {
		applyXAIHeaders(r, auth, token, stream, sessionID, clientHeaders...)
		return
	}
	applyXAIDefaultHeaders(r, token, stream, sessionID)
	if xaiIsCLIChatProxyBaseURL(xaiChatBaseURL(auth)) {
		r.Header.Set(xaiTokenAuthHeader, xaiTokenAuthValue)
		r.Header.Set(xaiClientVersionHeader, xaiClientVersionValue)
		r.Header.Set("User-Agent", "xai-grok-workspace/"+xaiClientVersionValue)
		r.Header.Set(xaiClientIdentifierHeader, xaiClientIdentifierValue)
		r.Header.Set(xaiAuthenticateResponseHeader, xaiAuthenticateResponseValue)
	}
	applyXAICustomHeaders(r, auth, clientHeaders...)
}

func xaiResolveComposerSessionID(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, baseModel string) (string, error) {
	if sessionID := xaiExecutionSessionID(req, opts); sessionID != "" {
		return sessionID, nil
	}
	if !xaiRequiresIsolatedConversation(baseModel) {
		return "", nil
	}
	cached, ok, errCache := helps.ClaudeCodePromptCache(ctx, baseModel, req.Payload, opts.Headers)
	if errCache != nil {
		return "", errCache
	}
	if ok {
		return cached.ID, nil
	}
	return uuid.NewString(), nil
}

func xaiExecutionSessionID(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	if value := xaiMetadataString(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); value != "" {
		return value
	}
	if value := xaiMetadataString(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); value != "" {
		return value
	}
	if promptCacheKey := gjson.GetBytes(req.Payload, "prompt_cache_key"); promptCacheKey.Exists() {
		if value := strings.TrimSpace(promptCacheKey.String()); value != "" {
			return value
		}
	}
	return helps.DerivedSessionUUID("xai", opts.Metadata, req.Metadata)
}

func xaiRequiresIsolatedConversation(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), xaiComposerModelPrefix)
}

func xaiImageEndpointPath(opts cliproxyexecutor.Options) string {
	if opts.SourceFormat.String() != xaiImageHandlerType {
		return ""
	}

	path := xaiMetadataString(opts.Metadata, cliproxyexecutor.RequestPathMetadataKey)
	if strings.HasSuffix(path, "/images/edits") {
		return xaiImagesEditsPath
	}
	if strings.HasSuffix(path, "/images/generations") {
		return xaiImagesGenerationsPath
	}
	return xaiDefaultImageEndpointPath
}

// normalizeXAIImageRefs rewrites OpenAI-style image object fields to the xAI
// image API shape before the payload is sent upstream:
//
//	{"image":{"image_url":"https://..."}} → {"image":{"url":"https://..."}}
//
// Applies to image / images / reference_images anywhere in the JSON tree,
// including nested objects and array items. Does not rewrite chat content
// parts shaped as {"type":"image_url","image_url":{...}}.
func normalizeXAIImageRefs(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload any
	if errDecode := decoder.Decode(&payload); errDecode != nil {
		return body
	}

	if !normalizeXAIImageRefsValue(payload) {
		return body
	}
	normalized, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return body
	}
	return normalized
}

func normalizeXAIImageRefsValue(value any) bool {
	changed := false
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			switch key {
			case "image":
				changed = normalizeXAIImageRef(child) || changed
			case "images", "reference_images":
				if refs, ok := child.([]any); ok {
					for _, ref := range refs {
						changed = normalizeXAIImageRef(ref) || changed
					}
				}
			}
			changed = normalizeXAIImageRefsValue(child) || changed
		}
	case []any:
		for _, child := range node {
			changed = normalizeXAIImageRefsValue(child) || changed
		}
	}
	return changed
}

func normalizeXAIImageRef(value any) bool {
	ref, ok := value.(map[string]any)
	if !ok {
		return false
	}

	originalURL, _ := ref["url"].(string)
	url := strings.TrimSpace(originalURL)
	imageURL, hasImageURL := ref["image_url"]
	if url == "" {
		switch imageURL := imageURL.(type) {
		case string:
			url = strings.TrimSpace(imageURL)
		case map[string]any:
			url, _ = imageURL["url"].(string)
			url = strings.TrimSpace(url)
		}
	}
	if url == "" {
		return false
	}
	if url == originalURL && !hasImageURL {
		return false
	}

	// Always emit the xAI field name and drop the OpenAI alias.
	ref["url"] = url
	delete(ref, "image_url")
	return true
}

func xaiIsVideoRequest(opts cliproxyexecutor.Options) bool {
	return opts.SourceFormat.String() == xaiVideoHandlerType
}

func xaiVideoEndpointPath(opts cliproxyexecutor.Options) string {
	if !xaiIsVideoRequest(opts) {
		return ""
	}
	path := xaiMetadataString(opts.Metadata, cliproxyexecutor.RequestPathMetadataKey)
	if strings.HasSuffix(path, "/videos/edits") {
		return xaiVideosEditsPath
	}
	if strings.HasSuffix(path, "/videos/extensions") {
		return xaiVideosExtensionsPath
	}
	if strings.HasSuffix(path, "/videos/generations") {
		return xaiVideosGenerationsPath
	}
	return ""
}

func xaiMetadataString(meta map[string]any, key string) string {
	if len(meta) == 0 || key == "" {
		return ""
	}
	value, ok := meta[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func preserveXAIResponsesOutputControls(body, source []byte, from sdktranslator.Format) []byte {
	var maxOutputTokens gjson.Result
	switch from {
	case sdktranslator.FormatOpenAI:
		maxOutputTokens = gjson.GetBytes(source, "max_completion_tokens")
		if !maxOutputTokens.Exists() || maxOutputTokens.Type == gjson.Null {
			maxOutputTokens = gjson.GetBytes(source, "max_tokens")
		}
	case sdktranslator.FormatOpenAIResponse:
		maxOutputTokens = gjson.GetBytes(source, "max_output_tokens")
	default:
		return body
	}

	if maxOutputTokens.Exists() && maxOutputTokens.Type != gjson.Null {
		body, _ = sjson.SetRawBytes(body, "max_output_tokens", []byte(maxOutputTokens.Raw))
	}
	for _, field := range []string{"temperature", "top_p", "top_k"} {
		value := gjson.GetBytes(source, field)
		if value.Exists() && value.Type != gjson.Null {
			body, _ = sjson.SetRawBytes(body, field, []byte(value.Raw))
		}
	}
	return body
}

// xaiGrokImageGenerationMinVersion is the first Grok line that accepts xAI's
// native Responses image_generation tool. Older conversation models still
// reject that hosted type, so the executor keeps stripping it there.
var xaiGrokImageGenerationMinVersion = xaiGrokVersion{major: 4, minor: 6}

type xaiGrokVersion struct {
	major int
	minor int
}

// xaiSupportsNativeImageGeneration reports whether the Grok model accepts
// xAI's native Responses image_generation tool. grok-4.20-* is an older
// product line whose dotted minor is not comparable to grok-4.6.
func xaiSupportsNativeImageGeneration(model string) bool {
	name := strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	if name == "" || !strings.HasPrefix(name, "grok-") {
		return false
	}
	rest := strings.TrimPrefix(name, "grok-")
	if rest == "4.20" || strings.HasPrefix(rest, "4.20-") {
		return false
	}
	ver, ok := xaiParseGrokVersionPrefix(rest)
	if !ok {
		return false
	}
	return xaiCompareGrokVersion(ver, xaiGrokImageGenerationMinVersion) >= 0
}

func xaiParseGrokVersionPrefix(rest string) (xaiGrokVersion, bool) {
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	if i == 0 {
		return xaiGrokVersion{}, false
	}
	major, err := strconv.Atoi(rest[:i])
	if err != nil {
		return xaiGrokVersion{}, false
	}
	if i == len(rest) || rest[i] != '.' {
		return xaiGrokVersion{major: major, minor: -1}, true
	}
	j := i + 1
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if j == i+1 {
		return xaiGrokVersion{major: major, minor: -1}, true
	}
	minor, err := strconv.Atoi(rest[i+1 : j])
	if err != nil {
		return xaiGrokVersion{}, false
	}
	return xaiGrokVersion{major: major, minor: minor}, true
}

func xaiCompareGrokVersion(a, b xaiGrokVersion) int {
	if a.major != b.major {
		if a.major < b.major {
			return -1
		}
		return 1
	}
	aMinor := a.minor
	if aMinor < 0 {
		aMinor = 0
	}
	bMinor := b.minor
	if bMinor < 0 {
		bMinor = 0
	}
	if aMinor < bMinor {
		return -1
	}
	if aMinor > bMinor {
		return 1
	}
	return 0
}

func sanitizeXAIResponsesBody(body []byte, model string) []byte {
	// stop is supported by Chat Completions but not by xAI's Responses API.
	body, _ = sjson.DeleteBytes(body, "stop")
	if !xaiSupportsReasoningEffort(model) {
		if gjson.GetBytes(body, "reasoning.effort").Exists() {
			log.Debugf("xai: stripping reasoning.effort for model %s (no thinking levels in model registry)", model)
		}
		body, _ = sjson.DeleteBytes(body, "reasoning.effort")
		if reasoning := gjson.GetBytes(body, "reasoning"); reasoning.Exists() && reasoning.IsObject() && len(reasoning.Map()) == 0 {
			body, _ = sjson.DeleteBytes(body, "reasoning")
		}
	}
	return body
}

// ensureXAINativeXSearchTool appends {"type":"x_search"} when the final tools
// list does not already include native X Search. When tool_choice restricts the
// model to allowed_tools, x_search is also added there (without duplicates) so
// Grok can select the injected tool. When injection is enabled, HTTP and websocket
// executors both prepare payloads through prepareResponsesRequestTo, so this runs
// once before the body is submitted upstream.
func ensureXAINativeXSearchTool(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	if !xaiRequestHasNativeXSearch(body) {
		tools := gjson.GetBytes(body, "tools")
		if !tools.Exists() || !tools.IsArray() {
			body, _ = sjson.SetRawBytes(body, "tools", []byte(`[{"type":"x_search"}]`))
		} else {
			body, _ = sjson.SetRawBytes(body, "tools.-1", xaiXSearchToolJSON)
		}
	}
	return ensureXAINativeXSearchAllowedTools(body)
}

// ensureXAINativeXSearchAllowedTools appends x_search to tool_choice.tools when
// the choice mode is allowed_tools and x_search is not already listed.
func ensureXAINativeXSearchAllowedTools(body []byte) []byte {
	choice := gjson.GetBytes(body, "tool_choice")
	if !choice.IsObject() || choice.Get("type").String() != "allowed_tools" {
		return body
	}
	allowed := choice.Get("tools")
	if !allowed.Exists() || !allowed.IsArray() {
		body, _ = sjson.SetRawBytes(body, "tool_choice.tools", []byte(`[{"type":"x_search"}]`))
		return body
	}
	for _, tool := range allowed.Array() {
		if strings.TrimSpace(tool.Get("type").String()) == xaiXSearchToolType {
			return body
		}
	}
	body, _ = sjson.SetRawBytes(body, "tool_choice.tools.-1", xaiXSearchToolJSON)
	return body
}

// normalizeXAIForcedWebSearchToolChoice rewrites Codex's hosted-tool choice
// into the allowed_tools form accepted by xAI's ModelToolChoice schema.
func normalizeXAIForcedWebSearchToolChoice(body []byte) []byte {
	return normalizeXAIForcedHostedToolChoice(body, xaiWebSearchToolType)
}

// normalizeXAIForcedImageGenerationToolChoice rewrites image_generation choices
// into a ModelToolChoice variant accepted by xAI chat-proxy. `{type: image_generation}`
// becomes the string "required" and the tools list is reduced to image_generation
// so later x_search injection cannot broaden the restriction. An allowed_tools
// list that only names that hosted tool becomes the original mode ("auto" or
// "required") and is likewise reduced to image_generation. Mixed lists drop the
// image_generation entry so the remaining hosted/function choices can still
// deserialize.
func normalizeXAIForcedImageGenerationToolChoice(body []byte) []byte {
	choice := gjson.GetBytes(body, "tool_choice")
	if !choice.IsObject() {
		return body
	}
	choiceType := strings.TrimSpace(choice.Get("type").String())
	if choiceType == xaiImageGenerationToolType {
		body = xaiKeepOnlyImageGenerationTools(body)
		return xaiSetToolChoiceString(body, "required")
	}
	if choiceType != "allowed_tools" {
		return body
	}
	allowed := choice.Get("tools")
	if !allowed.IsArray() {
		return body
	}
	filtered := make([][]byte, 0, len(allowed.Array()))
	stripped := false
	for _, tool := range allowed.Array() {
		if strings.TrimSpace(tool.Get("type").String()) == xaiImageGenerationToolType {
			stripped = true
			continue
		}
		filtered = append(filtered, []byte(tool.Raw))
	}
	if !stripped {
		return body
	}
	if len(filtered) == 0 {
		mode := strings.TrimSpace(choice.Get("mode").String())
		if mode != "auto" {
			mode = "required"
		}
		body = xaiKeepOnlyImageGenerationTools(body)
		return xaiSetToolChoiceString(body, mode)
	}
	updated, errSet := sjson.SetRawBytes(body, "tool_choice.tools", helps.JoinRawJSONArray(filtered))
	if errSet != nil {
		return body
	}
	return updated
}

func xaiKeepOnlyImageGenerationTools(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body
	}
	kept := make([][]byte, 0, 1)
	for _, tool := range tools.Array() {
		if strings.TrimSpace(tool.Get("type").String()) == xaiImageGenerationToolType {
			kept = append(kept, []byte(tool.Raw))
		}
	}
	if len(kept) == 0 || len(kept) == len(tools.Array()) {
		return body
	}
	updated, errSet := sjson.SetRawBytes(body, "tools", helps.JoinRawJSONArray(kept))
	if errSet != nil {
		return body
	}
	return updated
}

func xaiToolChoiceRequiresImageGenerationOnly(body []byte) bool {
	choice := gjson.GetBytes(body, "tool_choice")
	if choice.Type != gjson.String {
		return false
	}
	switch choice.String() {
	case "required", "auto":
	default:
		return false
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() || len(tools.Array()) == 0 {
		return false
	}
	for _, tool := range tools.Array() {
		if strings.TrimSpace(tool.Get("type").String()) != xaiImageGenerationToolType {
			return false
		}
	}
	return true
}

func xaiSetToolChoiceString(body []byte, value string) []byte {
	updated, errSet := sjson.SetBytes(body, "tool_choice", value)
	if errSet != nil {
		return body
	}
	return updated
}

func normalizeXAIForcedHostedToolChoice(body []byte, toolType string) []byte {
	choice := gjson.GetBytes(body, "tool_choice")
	if !choice.IsObject() || strings.TrimSpace(choice.Get("type").String()) != toolType {
		return body
	}

	allowedChoice := []byte(`{"type":"allowed_tools","mode":"required","tools":[]}`)
	allowedChoice, errSetAllowed := sjson.SetRawBytes(allowedChoice, "tools.-1", []byte(choice.Raw))
	if errSetAllowed != nil {
		return body
	}
	updated, errSetChoice := sjson.SetRawBytes(body, "tool_choice", allowedChoice)
	if errSetChoice != nil {
		return body
	}
	return updated
}

// pruneXAIOrphanedToolChoice removes tool_choice entries that no longer match
// any remaining tool after normalizeXAITools filtering. Forced choices that
// reference a deleted tool are dropped entirely; allowed_tools lists keep only
// choices that still resolve against the post-normalization tools set.
func pruneXAIOrphanedToolChoice(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	choice := gjson.GetBytes(body, "tool_choice")
	if !choice.Exists() {
		return body
	}
	available := collectXAIAvailableToolChoiceKeys(body)
	if choice.Type == gjson.String {
		// auto / none / required are not tool references.
		return body
	}
	if !choice.IsObject() {
		return body
	}
	choiceType := strings.TrimSpace(choice.Get("type").String())
	switch choiceType {
	case "allowed_tools":
		return pruneXAIAllowedToolsChoice(body, available)
	default:
		if choiceType == "" {
			return body
		}
		if xaiToolChoiceMatchesAvailable(choice, available) {
			return body
		}
		body, _ = sjson.DeleteBytes(body, "tool_choice")
		return body
	}
}

func pruneXAIAllowedToolsChoice(body []byte, available map[xaiToolChoiceKey]struct{}) []byte {
	allowed := gjson.GetBytes(body, "tool_choice.tools")
	if !allowed.Exists() || !allowed.IsArray() {
		body, _ = sjson.DeleteBytes(body, "tool_choice")
		return body
	}
	allowedItems := allowed.Array()
	filtered := make([][]byte, 0, len(allowedItems))
	changed := false
	for _, tool := range allowedItems {
		if !xaiToolChoiceMatchesAvailable(tool, available) {
			changed = true
			continue
		}
		filtered = append(filtered, []byte(tool.Raw))
	}
	if !changed {
		return body
	}
	if len(filtered) == 0 {
		body, _ = sjson.DeleteBytes(body, "tool_choice")
		return body
	}
	body, _ = sjson.SetRawBytes(body, "tool_choice.tools", helps.JoinRawJSONArray(filtered))
	return body
}

// xaiToolChoiceKey identifies a selectable tool the way xAI tool_choice entries
// reference it after namespace qualification: type alone for host tools, or
// type+name for function tools.
type xaiToolChoiceKey struct {
	toolType string
	name     string
}

func collectXAIAvailableToolChoiceKeys(body []byte) map[xaiToolChoiceKey]struct{} {
	keys := make(map[xaiToolChoiceKey]struct{})
	collect := func(tools gjson.Result) {
		if !tools.IsArray() {
			return
		}
		for _, tool := range tools.Array() {
			toolType := strings.TrimSpace(tool.Get("type").String())
			if toolType == "" {
				continue
			}
			key := xaiToolChoiceKey{toolType: toolType}
			if toolType == xaiFunctionToolType || toolType == xaiCustomToolType {
				key.name = strings.TrimSpace(tool.Get("name").String())
				if key.name == "" {
					continue
				}
			}
			keys[key] = struct{}{}
		}
	}
	collect(gjson.GetBytes(body, "tools"))
	input := gjson.GetBytes(body, "input")
	if input.IsArray() {
		for _, item := range input.Array() {
			if item.Get("type").String() == "additional_tools" {
				collect(item.Get("tools"))
			}
		}
	}
	return keys
}

func xaiToolChoiceMatchesAvailable(choice gjson.Result, available map[xaiToolChoiceKey]struct{}) bool {
	toolType := strings.TrimSpace(choice.Get("type").String())
	if toolType == "" {
		return false
	}
	key := xaiToolChoiceKey{toolType: toolType}
	if toolType == xaiFunctionToolType || toolType == xaiCustomToolType {
		key.name = strings.TrimSpace(choice.Get("name").String())
		if key.name == "" {
			return false
		}
	}
	_, ok := available[key]
	return ok
}

func xaiCountFlattenedTools(tools gjson.Result) int {
	if !tools.Exists() || !tools.IsArray() {
		return 0
	}
	count := 0
	for _, tool := range tools.Array() {
		switch tool.Get("type").String() {
		case xaiNamespaceToolType:
			if nestedTools := tool.Get("tools"); nestedTools.IsArray() {
				count += len(nestedTools.Array())
			} else {
				count++
			}
		case xaiToolSearchType:
			// Tool search is stripped by normalizeXAITool
		default:
			count++
		}
	}
	return count
}

func xaiTotalFlattenedToolsCount(body []byte, willInjectXSearch bool) int {
	count := xaiCountFlattenedTools(gjson.GetBytes(body, "tools"))
	input := gjson.GetBytes(body, "input")
	if input.Exists() && input.IsArray() {
		for _, item := range input.Array() {
			if item.Get("type").String() == "additional_tools" {
				count += xaiCountFlattenedTools(item.Get("tools"))
			}
		}
	}
	if willInjectXSearch && !xaiRequestHasNativeXSearch(body) && !xaiToolChoiceRequiresImageGenerationOnly(body) {
		count++
	}
	return count
}

func xaiShouldFoldNamespaceTools(body []byte, willInjectXSearch bool) bool {
	return xaiTotalFlattenedToolsCount(body, willInjectXSearch) > xaiMaxTools
}

func buildXAINamespaceDispatcherTool(tool gjson.Result) []byte {
	namespaceName := strings.TrimSpace(tool.Get("name").String())
	if namespaceName == "" {
		return nil
	}
	description := strings.TrimSpace(tool.Get("description").String())

	var toolNames []string
	var toolDescriptions []string
	if nestedTools := tool.Get("tools"); nestedTools.IsArray() {
		for _, child := range nestedTools.Array() {
			childName := strings.TrimSpace(child.Get("name").String())
			if childName == "" {
				continue
			}
			toolNames = append(toolNames, childName)
			childDesc := strings.TrimSpace(child.Get("description").String())

			params := child.Get("parameters")
			if !params.Exists() {
				params = child.Get("input_schema")
			}

			var paramStr string
			if params.Exists() && params.Raw != "" {
				rawParams := strings.TrimSpace(params.Raw)
				if rawParams != "" && rawParams != "{}" && rawParams != `{"type":"object","properties":{}}` {
					inlined := util.InlineLocalRefs(rawParams)
					if gjson.Valid(inlined) {
						cleaned := []byte(inlined)
						if gjson.GetBytes(cleaned, "$defs").Exists() {
							cleaned, _ = sjson.DeleteBytes(cleaned, "$defs")
						}
						if gjson.GetBytes(cleaned, "definitions").Exists() {
							cleaned, _ = sjson.DeleteBytes(cleaned, "definitions")
						}
						paramStr = string(cleaned)
					} else {
						paramStr = inlined
					}
				}
			}

			var entry string
			if childDesc != "" {
				if paramStr != "" {
					entry = fmt.Sprintf("- %s: %s\n  Parameters: %s", childName, childDesc, paramStr)
				} else {
					entry = fmt.Sprintf("- %s: %s", childName, childDesc)
				}
			} else {
				if paramStr != "" {
					entry = fmt.Sprintf("- %s\n  Parameters: %s", childName, paramStr)
				} else {
					entry = fmt.Sprintf("- %s", childName)
				}
			}
			toolDescriptions = append(toolDescriptions, entry)
		}
	}

	fullDescription := description
	if len(toolDescriptions) > 0 {
		catalog := "Available tools in this namespace:\n" + strings.Join(toolDescriptions, "\n")
		if fullDescription != "" {
			fullDescription += "\n\n" + catalog
		} else {
			fullDescription = fmt.Sprintf("Tools in namespace %s.\n\n%s", namespaceName, catalog)
		}
	} else if fullDescription == "" {
		fullDescription = fmt.Sprintf("Tools in namespace %s.", namespaceName)
	}

	nameProp := map[string]any{
		"type":        "string",
		"description": fmt.Sprintf("Child tool name to execute in namespace %s", namespaceName),
	}
	if len(toolNames) > 0 {
		nameProp["enum"] = toolNames
	}

	dispatcher := map[string]any{
		"type":        xaiFunctionToolType,
		"name":        namespaceName,
		"description": fullDescription,
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": nameProp,
				"arguments": map[string]any{
					"type":                 "object",
					"description":          "Arguments object matching the parameter schema of the selected child tool",
					"additionalProperties": true,
				},
			},
			"required": []string{"name"},
		},
	}

	raw, errMarshal := json.Marshal(dispatcher)
	if errMarshal != nil {
		return nil
	}
	return raw
}

func normalizeXAITools(body []byte) []byte {
	return normalizeXAIToolsWithFold(body, xaiShouldFoldNamespaceTools(body, false))
}

func normalizeXAIToolsWithFold(body []byte, shouldFold bool) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	keepImageGeneration := xaiSupportsNativeImageGeneration(gjson.GetBytes(body, "model").String())
	original := body
	normalizeAtPath := func(path string) bool {
		tools := gjson.GetBytes(body, path)
		if !tools.Exists() || !tools.IsArray() {
			return true
		}
		filtered, changed, ok := normalizeXAIToolArray(tools, keepImageGeneration, shouldFold)
		if !ok {
			return false
		}
		if !changed {
			return true
		}
		updated, errSet := sjson.SetRawBytes(body, path, filtered)
		if errSet != nil {
			return false
		}
		body = updated
		return true
	}

	if !normalizeAtPath("tools") {
		return original
	}
	input := gjson.GetBytes(body, "input")
	if input.Exists() && input.IsArray() {
		for index, item := range input.Array() {
			if item.Get("type").String() != "additional_tools" {
				continue
			}
			if !normalizeAtPath(fmt.Sprintf("input.%d.tools", index)) {
				return original
			}
		}
	}
	return body
}

func xaiHasFunctionToolNamed(body []byte, name string) bool {
	if name == "" {
		return false
	}
	tools := gjson.GetBytes(body, "tools")
	if tools.IsArray() {
		for _, tool := range tools.Array() {
			if tool.Get("type").String() == xaiFunctionToolType && tool.Get("name").String() == name {
				return true
			}
		}
	}
	input := gjson.GetBytes(body, "input")
	if input.IsArray() {
		for _, item := range input.Array() {
			if item.Get("type").String() == "additional_tools" {
				for _, tool := range item.Get("tools").Array() {
					if tool.Get("type").String() == xaiFunctionToolType && tool.Get("name").String() == name {
						return true
					}
				}
			}
		}
	}
	return false
}

func clampXAIToolsLimit(body []byte, maxTools int, refs map[string]xaiNamespaceToolRef) []byte {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() || len(tools.Array()) <= maxTools {
		return body
	}
	allTools := tools.Array()
	var dispatcherTools []json.RawMessage
	var regularTools []json.RawMessage
	for _, tool := range allTools {
		name := strings.TrimSpace(tool.Get("name").String())
		if ref, ok := refs[name]; ok && ref.isDispatcher {
			dispatcherTools = append(dispatcherTools, json.RawMessage(tool.Raw))
		} else {
			regularTools = append(regularTools, json.RawMessage(tool.Raw))
		}
	}

	capped := make([]json.RawMessage, 0, maxTools)
	if len(dispatcherTools) >= maxTools {
		capped = append(capped, dispatcherTools[:maxTools]...)
	} else {
		capped = append(capped, dispatcherTools...)
		remainingSlots := maxTools - len(dispatcherTools)
		if len(regularTools) > remainingSlots {
			capped = append(capped, regularTools[:remainingSlots]...)
		} else {
			capped = append(capped, regularTools...)
		}
	}

	raw, errMarshal := json.Marshal(capped)
	if errMarshal != nil {
		return body
	}
	updated, errSet := sjson.SetRawBytes(body, "tools", raw)
	if errSet != nil {
		return body
	}
	updated = pruneXAIOrphanedToolChoice(updated)
	return normalizeXAIToolChoiceForTools(updated)
}

// promoteXAIAdditionalTools moves Responses Lite tool declarations to the
// top-level tools array because xAI does not accept additional_tools input items.
func promoteXAIAdditionalTools(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}

	inputItems := input.Array()
	remainingInput := make([]json.RawMessage, 0, len(inputItems))
	promotedTools := make([]json.RawMessage, 0)
	for _, item := range inputItems {
		if item.Get("type").String() != "additional_tools" {
			remainingInput = append(remainingInput, json.RawMessage(item.Raw))
			continue
		}
		for _, tool := range item.Get("tools").Array() {
			promotedTools = append(promotedTools, json.RawMessage(tool.Raw))
		}
	}
	if len(remainingInput) == len(inputItems) {
		return body
	}

	rawInput, errMarshalInput := json.Marshal(remainingInput)
	if errMarshalInput != nil {
		return body
	}
	updated, errSetInput := sjson.SetRawBytes(body, "input", rawInput)
	if errSetInput != nil {
		return body
	}
	if len(promotedTools) == 0 {
		return updated
	}

	topLevelTools := gjson.GetBytes(updated, "tools")
	tools := make([]json.RawMessage, 0, len(topLevelTools.Array())+len(promotedTools))
	if topLevelTools.IsArray() {
		for _, tool := range topLevelTools.Array() {
			tools = append(tools, json.RawMessage(tool.Raw))
		}
	}
	tools = append(tools, promotedTools...)
	rawTools, errMarshalTools := json.Marshal(tools)
	if errMarshalTools != nil {
		return body
	}
	updated, errSetTools := sjson.SetRawBytes(updated, "tools", rawTools)
	if errSetTools != nil {
		return body
	}
	return updated
}

func normalizeXAIToolArray(tools gjson.Result, keepImageGeneration, shouldFold bool) ([]byte, bool, bool) {
	toolItems := tools.Array()
	filtered := make([][]byte, 0, len(toolItems))
	changed := false
	for _, tool := range toolItems {
		toolType := tool.Get("type").String()
		if toolType == xaiNamespaceToolType {
			changed = true
			if shouldFold {
				if dispatcher := buildXAINamespaceDispatcherTool(tool); len(dispatcher) > 0 {
					filtered = append(filtered, dispatcher)
				}
				continue
			}
			namespaceName := tool.Get("name").String()
			if namespaceTools := tool.Get("tools"); namespaceTools.IsArray() {
				for _, nestedTool := range namespaceTools.Array() {
					nestedRaw, nestedChanged, ok := normalizeXAITool(nestedTool, namespaceName, keepImageGeneration)
					if !ok {
						return nil, false, false
					}
					changed = changed || nestedChanged
					if len(nestedRaw) > 0 {
						filtered = append(filtered, nestedRaw)
					}
				}
			}
			continue
		}
		raw, toolChanged, ok := normalizeXAITool(tool, "", keepImageGeneration)
		if !ok {
			return nil, false, false
		}
		changed = changed || toolChanged
		if len(raw) > 0 {
			filtered = append(filtered, raw)
		}
	}
	if !changed {
		return nil, false, true
	}
	return helps.JoinRawJSONArray(filtered), true, true
}

// normalizeXAIToolChoiceForTools drops tool_choice and parallel_tool_calls
// when tools are absent or empty (including after normalizeXAITools filtering).
// xAI rejects payloads that include tool_choice without any tools defined.
// Existence checks avoid unnecessary sjson parse/copy passes.
func normalizeXAIToolChoiceForTools(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	hasTools := tools.Exists() && tools.IsArray() && len(tools.Array()) > 0
	if !hasTools {
		input := gjson.GetBytes(body, "input")
		if input.Exists() && input.IsArray() {
			for _, item := range input.Array() {
				additionalTools := item.Get("tools")
				if item.Get("type").String() == "additional_tools" && additionalTools.IsArray() && len(additionalTools.Array()) > 0 {
					hasTools = true
					break
				}
			}
		}
	}
	if hasTools {
		return body
	}
	if tools.Exists() {
		body, _ = sjson.DeleteBytes(body, "tools")
	}
	if gjson.GetBytes(body, "tool_choice").Exists() {
		body, _ = sjson.DeleteBytes(body, "tool_choice")
	}
	if gjson.GetBytes(body, "parallel_tool_calls").Exists() {
		body, _ = sjson.DeleteBytes(body, "parallel_tool_calls")
	}
	return body
}

// normalizeXAINamespaceToolChoice qualifies namespaced function choices using
// the same names sent in the flattened tools list. xAI does not accept the
// Responses namespace field on tool choices.
func normalizeXAINamespaceToolChoice(body []byte) []byte {
	return normalizeXAINamespaceToolChoiceWithFold(body, xaiShouldFoldNamespaceTools(body, false))
}

func normalizeXAINamespaceToolChoiceWithFold(body []byte, shouldFold bool) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	original := body
	normalizeAtPath := func(path string) bool {
		toolChoice := gjson.GetBytes(body, path)
		if !toolChoice.IsObject() || toolChoice.Get("type").String() != xaiFunctionToolType {
			return true
		}
		namespaceName := strings.TrimSpace(toolChoice.Get("namespace").String())
		toolName := strings.TrimSpace(toolChoice.Get("name").String())
		if namespaceName == "" {
			return true
		}
		qualifiedName := qualifyXAINamespaceToolName(namespaceName, toolName)
		var targetName string
		if xaiHasFunctionToolNamed(body, namespaceName) {
			targetName = namespaceName
		} else if xaiHasFunctionToolNamed(body, qualifiedName) {
			targetName = qualifiedName
		} else if shouldFold {
			targetName = namespaceName
		} else {
			targetName = qualifiedName
		}
		if targetName == "" {
			return true
		}
		updated, errSet := sjson.SetBytes(body, path+".name", targetName)
		if errSet != nil {
			return false
		}
		updated, errDelete := sjson.DeleteBytes(updated, path+".namespace")
		if errDelete != nil {
			return false
		}
		body = updated
		return true
	}

	if !normalizeAtPath("tool_choice") {
		return original
	}
	tools := gjson.GetBytes(body, "tool_choice.tools")
	if tools.IsArray() {
		for index := range tools.Array() {
			if !normalizeAtPath(fmt.Sprintf("tool_choice.tools.%d", index)) {
				return original
			}
		}
	}
	return body
}

func normalizeXAITool(tool gjson.Result, namespaceName string, keepImageGeneration bool) ([]byte, bool, bool) {
	toolType := tool.Get("type").String()
	changed := false
	if toolType == xaiToolSearchType {
		return nil, true, true
	}
	if toolType == xaiImageGenerationToolType && !keepImageGeneration {
		return nil, true, true
	}
	if toolType == xaiCustomToolType && tool.Get("name").String() == "apply_patch" {
		return nil, true, true
	}

	raw := []byte(tool.Raw)
	schemaTool := tool
	if toolType == xaiFunctionToolType || toolType == xaiCustomToolType {
		if rawParams := schemaTool.Get("parameters"); rawParams.Exists() {
			inlinedParams := util.InlineLocalRefs(rawParams.Raw)
			if inlinedParams != rawParams.Raw {
				if updated, errSet := sjson.SetRawBytes(raw, "parameters", []byte(inlinedParams)); errSet == nil {
					if inlinedDefs := gjson.GetBytes(updated, "parameters.$defs"); inlinedDefs.Exists() {
						updated, _ = sjson.DeleteBytes(updated, "parameters.$defs")
					}
					if inlinedDefinitions := gjson.GetBytes(updated, "parameters.definitions"); inlinedDefinitions.Exists() {
						updated, _ = sjson.DeleteBytes(updated, "parameters.definitions")
					}
					raw = updated
					schemaTool = gjson.ParseBytes(raw)
					changed = true
				}
			}
		}
		updatedTool, schemaChanged, ok := normalizeXAIObjectRootUnionBranchTypes(raw)
		if !ok {
			return nil, false, false
		}
		raw = updatedTool
		if schemaChanged {
			schemaTool = gjson.ParseBytes(raw)
			changed = true
			log.Debugf("xai: added object types to root union branches for tool %s.%s", namespaceName, tool.Get("name").String())
		}
	}
	if toolType == xaiCustomToolType {
		updatedTool, errSet := sjson.SetBytes(raw, "type", xaiFunctionToolType)
		if errSet != nil {
			return nil, false, false
		}
		raw = updatedTool
		toolType = xaiFunctionToolType
		changed = true
	}
	if toolType == xaiWebSearchToolType && tool.Get("external_web_access").Exists() {
		updatedTool, errDel := sjson.DeleteBytes(raw, "external_web_access")
		if errDel != nil {
			return nil, false, false
		}
		raw = updatedTool
		changed = true
	}
	if toolType == xaiFunctionToolType && !schemaTool.Get("parameters").Exists() {
		updatedTool, errSet := sjson.SetRawBytes(raw, "parameters", []byte(`{"type":"object","properties":{}}`))
		if errSet != nil {
			return nil, false, false
		}
		raw = updatedTool
		changed = true
	}
	// Simplify the Codex Desktop automation schema and root unions that xAI
	// rejects because function parameters must resolve exclusively to objects.
	if toolType == xaiFunctionToolType && xaiFunctionParametersNeedSimplification(schemaTool, namespaceName) {
		updatedTool, errSet := sjson.SetRawBytes(raw, "parameters", []byte(xaiSafeFunctionParameters))
		if errSet != nil {
			return nil, false, false
		}
		raw = updatedTool
		if strict := tool.Get("strict"); strict.Exists() && strict.Bool() {
			updatedTool, errSet = sjson.SetBytes(raw, "strict", false)
			if errSet != nil {
				return nil, false, false
			}
			raw = updatedTool
		}
		changed = true
		log.Debugf("xai: simplified parameters for tool %s.%s to avoid upstream schema rejection or hang", namespaceName, tool.Get("name").String())
	}
	if toolType == xaiFunctionToolType && strings.TrimSpace(namespaceName) != "" {
		qualifiedName := qualifyXAINamespaceToolName(namespaceName, tool.Get("name").String())
		if qualifiedName == "" {
			return nil, false, false
		}
		updatedTool, errSet := sjson.SetBytes(raw, "name", qualifiedName)
		if errSet != nil {
			return nil, false, false
		}
		raw = updatedTool
		changed = true
	}
	return raw, changed, true
}

func qualifyXAINamespaceToolName(namespaceName, toolName string) string {
	namespaceName = strings.TrimSpace(namespaceName)
	toolName = strings.TrimSpace(toolName)
	if namespaceName == "" || toolName == "" || strings.HasPrefix(toolName, "mcp__") {
		return toolName
	}
	prefix := namespaceName
	if !strings.HasSuffix(prefix, "__") {
		prefix += "__"
	}
	if strings.HasPrefix(toolName, prefix) {
		return toolName
	}
	return prefix + toolName
}

func collectXAINamespaceToolRefs(body []byte) map[string]xaiNamespaceToolRef {
	return collectXAINamespaceToolRefsWithFold(body, xaiShouldFoldNamespaceTools(body, false))
}

func collectXAINamespaceToolRefsWithFold(body []byte, shouldFold bool) map[string]xaiNamespaceToolRef {
	refs := make(map[string]xaiNamespaceToolRef)
	collect := func(tools gjson.Result) {
		if !tools.Exists() || !tools.IsArray() {
			return
		}
		for _, tool := range tools.Array() {
			if tool.Get("type").String() != xaiNamespaceToolType {
				continue
			}
			namespaceName := strings.TrimSpace(tool.Get("name").String())
			if namespaceName == "" {
				continue
			}
			if shouldFold {
				refs[namespaceName] = xaiNamespaceToolRef{namespace: namespaceName, name: "", isDispatcher: true}
			}
			for _, nestedTool := range tool.Get("tools").Array() {
				toolName := strings.TrimSpace(nestedTool.Get("name").String())
				qualifiedName := qualifyXAINamespaceToolName(namespaceName, toolName)
				if qualifiedName == "" {
					continue
				}
				refs[qualifiedName] = xaiNamespaceToolRef{namespace: namespaceName, name: toolName, isDispatcher: false}
			}
		}
	}
	collect(gjson.GetBytes(body, "tools"))
	input := gjson.GetBytes(body, "input")
	if input.Exists() && input.IsArray() {
		for _, item := range input.Array() {
			if item.Get("type").String() == "additional_tools" {
				collect(item.Get("tools"))
			}
		}
	}
	return refs
}

func normalizeXAIInputCustomToolCalls(body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || !input.IsArray() {
		return body
	}

	changed := false
	inputArray := input.Array()
	items := make([]json.RawMessage, 0, len(inputArray))
	for _, item := range inputArray {
		var normalized []byte
		switch item.Get("type").String() {
		case "custom_tool_call":
			callID := strings.TrimSpace(item.Get("call_id").String())
			name := strings.TrimSpace(item.Get("name").String())
			if callID == "" || name == "" {
				changed = true
				continue
			}
			normalized = []byte(`{"type":"function_call"}`)
			normalized, _ = sjson.SetBytes(normalized, "call_id", callID)
			normalized, _ = sjson.SetBytes(normalized, "name", name)
			normalized, _ = sjson.SetBytes(normalized, "arguments", xaiCustomToolCallArguments(item.Get("input")))
		case "custom_tool_call_output":
			callID := strings.TrimSpace(item.Get("call_id").String())
			if callID == "" {
				changed = true
				continue
			}
			normalized = []byte(`{"type":"function_call_output"}`)
			normalized, _ = sjson.SetBytes(normalized, "call_id", callID)
			normalized, _ = sjson.SetBytes(normalized, "output", xaiCustomToolCallOutput(item.Get("output")))
		default:
			items = append(items, json.RawMessage(item.Raw))
			continue
		}
		items = append(items, json.RawMessage(normalized))
		changed = true
	}
	if !changed {
		return body
	}

	rawInput, errMarshal := json.Marshal(items)
	if errMarshal != nil {
		return body
	}
	updated, errSet := sjson.SetRawBytes(body, "input", rawInput)
	if errSet != nil {
		return body
	}
	return updated
}

func xaiCustomToolCallArguments(input gjson.Result) string {
	if !input.Exists() {
		return "{}"
	}
	if input.Type == gjson.String {
		text := input.String()
		trimmed := strings.TrimSpace(text)
		if gjson.Valid(trimmed) {
			parsed := gjson.Parse(trimmed)
			if parsed.IsObject() {
				return parsed.Raw
			}
		}
		encoded, errMarshal := json.Marshal(text)
		if errMarshal != nil {
			return "{}"
		}
		return `{"input":` + string(encoded) + `}`
	}
	if input.IsObject() {
		return input.Raw
	}
	if input.Raw != "" {
		return `{"input":` + input.Raw + `}`
	}
	return "{}"
}

func xaiCustomToolCallOutput(output gjson.Result) string {
	if !output.Exists() {
		return ""
	}
	if output.Type == gjson.String {
		return output.String()
	}
	return output.Raw
}
