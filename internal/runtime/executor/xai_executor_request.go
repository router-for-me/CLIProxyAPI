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
	namespace string
	name      string
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
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, stream)
	originalTranslated = preserveXAIResponsesOutputControls(originalTranslated, originalPayload, from)
	body := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), stream)
	body = preserveXAIResponsesOutputControls(body, req.Payload, from)

	var err error
	body, err = thinking.ApplyThinking(body, req.Model, from.String(), e.Identifier(), e.Identifier())
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
	namespaceTools := collectXAINamespaceToolRefs(body)
	// Collect before normalizeXAITools flattens namespace wrappers so keys match
	// the post-restore (namespace, short-name) shape used by the response filter.
	clientDeclaredTools := collectXAIClientDeclaredToolKeys(body)
	body = normalizeXAITools(body)
	body = promoteXAIAdditionalTools(body)
	// Drop choices that point at tools removed by normalizeXAITools before any
	// configured x_search injection, so no surviving choice references a deleted tool.
	body = normalizeXAINamespaceToolChoice(body)
	body = pruneXAIOrphanedToolChoice(body)
	body = normalizeXAIToolChoiceForTools(body)
	if e.cfg != nil && e.cfg.XAI.InjectXSearch {
		body = ensureXAINativeXSearchTool(body)
	}
	var replayScope xaiReasoningReplayScope
	body, replayScope, err = applyXAIReasoningReplayCacheRequired(ctx, from, req, opts, body)
	if err != nil {
		return nil, err
	}
	body = normalizeXAIInputCustomToolCalls(body)
	body = normalizeXAIInputNamespaceToolCalls(body)
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
// for non-media HTTP chat. OAuth defaults to false to use Grok Build.
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

// xaiChatBaseURL returns the base URL for non-image/video xAI HTTP chat requests.
// When auth using_api is true, the official API base URL logic is used. When it
// is false (including its OAuth default), empty or official default base_url is
// rewritten to the CLI chat-proxy endpoint; an explicit non-default base_url is
// still honored.
// Websocket and compact transports intentionally do not use this helper:
// cli-chat-proxy only accepts HTTP POST chat and does not implement
// /responses/compact (404) or websocket upgrades (405).
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

func applyXAIHeaders(r *http.Request, auth *cliproxyauth.Auth, token string, stream bool, sessionID string) {
	applyXAIDefaultHeaders(r, token, stream, sessionID)
	applyXAICustomHeaders(r, auth)
}

func applyXAIDefaultHeaders(r *http.Request, token string, stream bool, sessionID string) {
	r.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(token) != "" {
		r.Header.Set("Authorization", "Bearer "+token)
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

func applyXAICustomHeaders(r *http.Request, auth *cliproxyauth.Auth) {
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(r, attrs)
}

// applyXAIChatHeaders applies standard xAI headers for non-image/video chat
// requests. When using_api is true, this matches the standard
// applyXAIHeaders behavior. CLI chat-proxy identity headers are only attached
// when using_api is false and the resolved chat base URL is the official CLI
// chat-proxy endpoint.
func applyXAIChatHeaders(r *http.Request, auth *cliproxyauth.Auth, token string, stream bool, sessionID string) {
	if xaiUsingAPI(auth) {
		applyXAIHeaders(r, auth, token, stream, sessionID)
		return
	}
	applyXAIDefaultHeaders(r, token, stream, sessionID)
	if xaiIsCLIChatProxyBaseURL(xaiChatBaseURL(auth)) {
		r.Header.Set(xaiTokenAuthHeader, xaiTokenAuthValue)
		r.Header.Set(xaiClientVersionHeader, xaiClientVersionValue)
		r.Header.Set("User-Agent", "xai-grok-workspace/"+xaiClientVersionValue)
	}
	applyXAICustomHeaders(r, auth)
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

func normalizeXAITools(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	original := body
	normalizeAtPath := func(path string) bool {
		tools := gjson.GetBytes(body, path)
		if !tools.Exists() || !tools.IsArray() {
			return true
		}
		filtered, changed, ok := normalizeXAIToolArray(tools)
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

func normalizeXAIToolArray(tools gjson.Result) ([]byte, bool, bool) {
	toolItems := tools.Array()
	filtered := make([][]byte, 0, len(toolItems))
	changed := false
	for _, tool := range toolItems {
		toolType := tool.Get("type").String()
		if toolType == xaiNamespaceToolType {
			changed = true
			namespaceName := tool.Get("name").String()
			if namespaceTools := tool.Get("tools"); namespaceTools.IsArray() {
				for _, nestedTool := range namespaceTools.Array() {
					nestedRaw, nestedChanged, ok := normalizeXAITool(nestedTool, namespaceName)
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
		raw, toolChanged, ok := normalizeXAITool(tool, "")
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
		qualifiedName := qualifyXAINamespaceToolName(namespaceName, toolName)
		if namespaceName == "" || qualifiedName == "" {
			return true
		}
		updated, errSet := sjson.SetBytes(body, path+".name", qualifiedName)
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

func normalizeXAITool(tool gjson.Result, namespaceName string) ([]byte, bool, bool) {
	toolType := tool.Get("type").String()
	changed := false
	if toolType == xaiToolSearchType || toolType == xaiImageGenerationToolType {
		return nil, true, true
	}
	if toolType == xaiCustomToolType && tool.Get("name").String() == "apply_patch" {
		return nil, true, true
	}

	raw := []byte(tool.Raw)
	schemaTool := tool
	if toolType == xaiFunctionToolType || toolType == xaiCustomToolType {
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
			for _, nestedTool := range tool.Get("tools").Array() {
				toolName := strings.TrimSpace(nestedTool.Get("name").String())
				qualifiedName := qualifyXAINamespaceToolName(namespaceName, toolName)
				if qualifiedName == "" {
					continue
				}
				refs[qualifiedName] = xaiNamespaceToolRef{namespace: namespaceName, name: toolName}
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
