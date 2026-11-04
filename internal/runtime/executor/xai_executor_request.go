package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

type xaiPreparedRequest struct {
	finalizePayload       helps.PayloadFinalizer
	applyPatch            *helps.ApplyPatchResponsesState
	baseModel             string
	from                  sdktranslator.Format
	responseFormat        sdktranslator.Format
	to                    sdktranslator.Format
	originalPayload       []byte
	body                  []byte
	doc                   *ojson.Doc
	namespaceTools        map[string]xaiNamespaceToolRef
	clientDeclaredTools   map[xaiClientToolKey]struct{}
	sessionID             string
	replayScope           xaiReasoningReplayScope
	filterInternalXSearch bool
	webSearchAlias        string
}

func (p *xaiPreparedRequest) ensureDoc() *ojson.Doc {
	if p == nil {
		return nil
	}
	if p.doc == nil {
		p.doc = ojson.NewDoc(p.body)
	}
	return p.doc
}

type xaiNamespaceToolRef struct {
	namespace    string
	name         string
	isDispatcher bool
	children     map[string]struct{}
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

func resolveXAISignatureTargetModel(baseModel string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	if resolved := resolveResponsesSignatureTargetModel(baseModel, req, opts); signature.SignatureProviderFromModelName(resolved) == signature.SignatureProviderGrok {
		return resolved
	}
	return "grok"
}

func (e *XAIExecutor) prepareResponsesRequest(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (*xaiPreparedRequest, error) {
	return e.prepareResponsesRequestTo(ctx, req, opts, stream, sdktranslator.FormatCodex)
}

func (e *XAIExecutor) prepareResponsesRequestDoc(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (*xaiPreparedRequest, error) {
	return e.prepareResponsesRequestToDoc(ctx, req, opts, stream, sdktranslator.FormatCodex)
}

func (e *XAIExecutor) prepareResponsesRequestTo(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool, to sdktranslator.Format) (*xaiPreparedRequest, error) {
	prepared, err := e.prepareResponsesRequestToDoc(ctx, req, opts, stream, to)
	if err != nil {
		return nil, err
	}
	if prepared.doc != nil {
		prepared.body = prepared.doc.Bytes()
	}
	return prepared, nil
}

func (e *XAIExecutor) prepareResponsesRequestToDoc(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool, to sdktranslator.Format) (*xaiPreparedRequest, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := bytes.Clone(originalPayloadSource)
	targetModel := resolveXAISignatureTargetModel(baseModel, req, opts)
	originalTranslated := helps.TranslateRequestWithAPIKeyModelCompatibilityForExecutor(ctx, opts.Headers, e.cfg, e.Identifier(), from, to, targetModel, originalPayload, stream, helps.APIKeyModelIsCompat(req))
	origDoc := ojson.NewDoc(originalTranslated)
	helps.SetStringIfDifferentDoc(origDoc, "model", baseModel)
	preserveXAIResponsesOutputControlsDoc(origDoc, originalPayload, from)
	originalTranslated = origDoc.Bytes()

	body, updatesChanged, err := helps.TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntentForExecutor(ctx, opts.Headers, e.cfg, e.Identifier(), from, to, targetModel, bytes.Clone(req.Payload), stream, helps.APIKeyModelIsCompat(req))
	if err != nil {
		return nil, err
	}
	doc := ojson.NewDoc(body)
	helps.SetStringIfDifferentDoc(doc, "model", baseModel)
	preserveXAIResponsesOutputControlsDoc(doc, req.Payload, from)

	if err = helps.ApplyRequestThinkingDoc(doc, req, opts, from.String(), e.Identifier(), e.Identifier(), updatesChanged); err != nil {
		return nil, err
	}

	finalizePayload := helps.NewPayloadFinalizer(e.cfg, e.Identifier(), baseModel, to.String(), "", originalTranslated, req, opts)
	helps.SetStringIfDifferentDoc(doc, "model", baseModel)
	helps.SetBoolIfDifferentDoc(doc, "stream", stream)
	_ = doc.Delete("previous_response_id")
	_ = doc.Delete("prompt_cache_retention")
	_ = doc.Delete("safety_identifier")
	_ = doc.Delete("stream_options")
	helps.RewriteCodexMultiAgentV2InputDoc(doc, ctx, opts.Headers, e.cfg)
	applyPatch := helps.NewApplyPatchResponsesState(from, originalPayload, originalTranslated)
	if errNormalizePatch := helps.NormalizeApplyPatchResponsesRequestDoc(doc, originalPayload); errNormalizePatch != nil {
		return nil, errNormalizePatch
	}
	willInjectXSearch := e.cfg != nil && e.cfg.XAI.InjectXSearch
	shouldFold := xaiShouldFoldNamespaceToolsDoc(doc, willInjectXSearch)
	namespaceTools := collectXAINamespaceToolRefsWithFoldDoc(doc, shouldFold)
	for name, ref := range namespaceTools {
		if ref.isDispatcher {
			applyPatch.AddDispatcher(name, ref.namespace)
		}
	}
	// Collect before normalizeXAITools flattens namespace wrappers so keys match
	// the post-restore (namespace, short-name) shape used by the response filter.
	clientDeclaredTools := collectXAIClientDeclaredToolKeysDoc(doc)
	normalizeXAIToolsWithFoldDoc(doc, shouldFold)
	promoteXAIAdditionalToolsDoc(doc)
	var webSearchAlias string
	if xaiHasClientWebSearchFunctionDoc(doc, namespaceTools) {
		webSearchAlias = xaiResolveClientWebSearchAliasDoc(doc)
		aliasXAIClientWebSearchFunctionDoc(doc, webSearchAlias, namespaceTools)
	}
	// Drop choices that point at tools removed by normalizeXAITools before any
	// configured x_search injection, so no surviving choice references a deleted tool.
	normalizeXAINamespaceToolChoiceWithFoldDoc(doc, shouldFold)
	// Prune before rewriting hosted tool choices so older models that still
	// strip the tool do not keep a leftover "required" selection.
	pruneXAIOrphanedToolChoiceDoc(doc)
	normalizeXAIForcedWebSearchToolChoiceDoc(doc)
	normalizeXAIForcedImageGenerationToolChoiceDoc(doc)
	normalizeXAIToolChoiceForToolsDoc(doc)
	// Skip x_search injection when the request was forced to a hosted tool and
	// the remaining tools list is only that hosted tool. "required" plus extra
	// tools would let Grok call x_search instead of the forced hosted tool.
	if e.cfg != nil && e.cfg.XAI.InjectXSearch && !xaiToolChoiceRequiresHostedToolOnlyAnyDoc(doc) {
		ensureXAINativeXSearchToolDoc(doc)
	}
	clampXAIToolsLimitDoc(doc, xaiMaxTools, namespaceTools)
	var replayScope xaiReasoningReplayScope
	replayScope, _, err = applyXAIReasoningReplayCacheRequiredDoc(ctx, from, req, opts, doc)
	if err != nil {
		return nil, err
	}
	normalizeXAIInputCustomToolCallsDoc(doc)
	normalizeXAIInputNamespaceToolCallsWithFoldDoc(doc, shouldFold)
	if webSearchAlias != "" {
		aliasXAIClientWebSearchInputDoc(doc, webSearchAlias, namespaceTools)
	}
	normalizeXAIInputReasoningItemsDoc(doc)
	sanitizeXAIInputEncryptedContentWithOptionsDoc(doc, true)
	normalizeCodexInstructionsDoc(doc)
	// stop is supported by Chat Completions but not by xAI's Responses API.
	// Thinking was handled before payload overrides and must not be revalidated here.
	_ = doc.Delete("stop")
	if xaiBytesMayContainImageRef(body) {
		normalizeXAIImageRefsDoc(doc)
	}

	sessionID, errSession := xaiResolveComposerSessionID(ctx, req, opts, baseModel)
	if errSession != nil {
		return nil, errSession
	}
	if sessionID != "" {
		helps.SetStringIfDifferentDoc(doc, "prompt_cache_key", sessionID)
	}

	return &xaiPreparedRequest{
		finalizePayload:       finalizePayload,
		applyPatch:            applyPatch,
		baseModel:             baseModel,
		from:                  from,
		responseFormat:        responseFormat,
		to:                    to,
		originalPayload:       originalPayload,
		doc:                   doc,
		namespaceTools:        namespaceTools,
		clientDeclaredTools:   clientDeclaredTools,
		sessionID:             sessionID,
		replayScope:           replayScope,
		filterInternalXSearch: xaiRequestHasNativeXSearchDoc(doc),
		webSearchAlias:        webSearchAlias,
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
// Websocket, compact, and speech intentionally do not use this helper:
// cli-chat-proxy does not implement /responses/compact (404), websocket
// upgrades (405), or /tts. Speech uses xaiSpeechRequestURL.
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

func xaiDefaultUserAgent() string {
	return "xai-grok-workspace/" + xaiClientVersion()
}

func ensureXAIUserAgent(r *http.Request, clientHeaders ...http.Header) {
	if r == nil {
		return
	}
	misc.EnsureHeader(r.Header, util.ResolveClientHeaders(r, clientHeaders...), "User-Agent", xaiDefaultUserAgent())
}

func applyXAIHeaders(r *http.Request, auth *cliproxyauth.Auth, token string, stream bool, sessionID string, clientHeaders ...http.Header) {
	applyXAIDefaultHeaders(r, token, stream, sessionID)
	ensureXAIUserAgent(r, clientHeaders...)
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
// requests. When using_api is true or a custom non-CLI-proxy base URL is used,
// this matches the standard applyXAIHeaders behavior (forwarding the downstream
// User-Agent or falling back to the Grok workspace User-Agent). CLI chat-proxy
// identity headers are only attached when using_api is false and the resolved
// chat base URL is the official CLI chat-proxy endpoint.
func applyXAIChatHeaders(r *http.Request, auth *cliproxyauth.Auth, token string, stream bool, sessionID string, clientHeaders ...http.Header) {
	if xaiUsingAPI(auth) || !xaiIsCLIChatProxyBaseURL(xaiChatBaseURL(auth)) {
		applyXAIHeaders(r, auth, token, stream, sessionID, clientHeaders...)
		return
	}
	applyXAIDefaultHeaders(r, token, stream, sessionID)
	clientVer := xaiClientVersion()
	r.Header.Set(xaiTokenAuthHeader, xaiTokenAuthValue)
	r.Header.Set(xaiClientVersionHeader, clientVer)
	r.Header.Set("User-Agent", "xai-grok-workspace/"+clientVer)
	r.Header.Set(xaiClientIdentifierHeader, xaiClientIdentifierValue)
	r.Header.Set(xaiAuthenticateResponseHeader, xaiAuthenticateResponseValue)
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
	if promptCacheKey := ojson.GetBytesNoCopy(req.Payload, "prompt_cache_key"); promptCacheKey.Exists() {
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
func xaiBytesMayContainImageRef(b []byte) bool {
	return bytes.Contains(b, []byte(`"image"`)) ||
		bytes.Contains(b, []byte(`"images"`)) ||
		bytes.Contains(b, []byte(`"reference_images"`)) ||
		bytes.Contains(b, []byte(`\u`))
}

func xaiStringMayContainImageRef(s string) bool {
	return strings.Contains(s, `"image"`) ||
		strings.Contains(s, `"images"`) ||
		strings.Contains(s, `"reference_images"`) ||
		strings.Contains(s, `\u`)
}

const (
	xaiKeyOther uint8 = iota
	xaiKeyImage
	xaiKeyImages
	xaiKeyURL
	xaiKeyImageURL
)

const (
	xaiValIgnore uint8 = iota
	xaiValRefURL
	xaiValRefImageURL
	xaiValRefNestedURL
)

const (
	xaiResolveNone uint8 = iota
	xaiResolveURL
	xaiResolveImageURL
	xaiResolveNestedImageURL
)

type xaiImageRefEdit struct {
	path        string
	url         string
	resolveMode uint8
	setURL      bool
	hasImageURL bool
	valid       bool
}

type xaiScanFrame struct {
	isObj         bool
	inKey         bool
	isImagesArr   bool
	isImageURLObj bool
	keyKind       uint8
	elemIdx       int
	keyStart      int
	keyEnd        int
}

type xaiRefCandidate struct {
	depth            int
	hasURL           bool
	urlNeedsTrim     bool
	urlMayNeedTrim   bool
	urlOverflow      bool
	urlLen           int
	urlBuf           [256]byte
	hasImageURL      bool
	hasImageURLVal   bool
	imageURLOverflow bool
	imageURLNested   bool
	imageURLLen      int
	imageURLBuf      [256]byte
}

type xaiImageRefScanner struct {
	frames    []xaiScanFrame
	framesBuf [32]xaiScanFrame

	refStack    []xaiRefCandidate
	refStackBuf [2]xaiRefCandidate

	inStr        bool
	strEsc       bool
	strIsKey     bool
	curKeyStart  int
	curKeyHasEsc bool
	keyArena     []byte
	keyArenaBuf  [256]byte

	valTarget      uint8
	valHasEsc      bool
	valHasNonSpace bool
	valOverflow    bool
	valFirstByte   byte
	valLastByte    byte
	valLen         int
	valBuf         [256]byte

	edits    []xaiImageRefEdit
	editsBuf [4]xaiImageRefEdit
}

var xaiImageRefScannerPool = sync.Pool{
	New: func() any {
		return &xaiImageRefScanner{}
	},
}

func (s *xaiImageRefScanner) init() {
	s.framesBuf[0] = xaiScanFrame{}
	s.frames = s.framesBuf[:1]
	s.refStack = s.refStackBuf[:0]
	s.keyArena = s.keyArenaBuf[:0]
	s.inStr = false
	s.strEsc = false
	s.strIsKey = false
	s.curKeyStart = 0
	s.curKeyHasEsc = false
	s.valTarget = xaiValIgnore
	s.edits = s.editsBuf[:0]
}

func (s *xaiImageRefScanner) reset() {
	clear(s.edits[:cap(s.edits)])
	clear(s.editsBuf[:])
	s.edits = nil
	clear(s.frames[:cap(s.frames)])
	clear(s.framesBuf[:])
	s.frames = nil
	clear(s.refStack[:cap(s.refStack)])
	clear(s.refStackBuf[:])
	s.refStack = nil
	s.keyArena = nil
}

func (s *xaiImageRefScanner) buildPathUpTo(depth int) string {
	var b strings.Builder
	for d := 1; d < depth; d++ {
		if d > 1 {
			b.WriteByte('.')
		}
		f := &s.frames[d]
		if f.isObj {
			key := s.keyArena[f.keyStart:f.keyEnd]
			for i := 0; i < len(key); i++ {
				ch := key[i]
				if ch == '.' || ch == '\\' || ch == '|' || ch == '#' || ch == '*' || ch == '?' || ch == '@' ||
					(i == 0 && (ch == ':' || ch == '[' || ch == '{' || ch == '!')) {
					b.WriteByte('\\')
				}
				b.WriteByte(ch)
			}
		} else {
			var idxBuf [20]byte
			b.Write(strconv.AppendInt(idxBuf[:0], int64(f.elemIdx), 10))
		}
	}
	return b.String()
}

func (s *xaiImageRefScanner) decodeValBuf() string {
	raw := s.valBuf[:s.valLen]
	if !s.valHasEsc {
		return string(raw)
	}
	var stackQuoted [514]byte
	stackQuoted[0] = '"'
	copy(stackQuoted[1:], raw)
	stackQuoted[1+len(raw)] = '"'
	return util.ParseGJSONBytesNoCopy(stackQuoted[:2+len(raw)]).String()
}

func (s *xaiImageRefScanner) finishValString() {
	target := s.valTarget
	s.valTarget = xaiValIgnore
	if len(s.refStack) == 0 {
		return
	}
	ref := &s.refStack[len(s.refStack)-1]
	switch target {
	case xaiValRefURL:
		if !s.valHasEsc && !s.valHasNonSpace {
			ref.hasURL = false
			ref.urlNeedsTrim = false
			ref.urlMayNeedTrim = false
			ref.urlOverflow = false
			ref.urlLen = 0
			return
		}
		if s.valOverflow {
			ref.urlOverflow = true
			if !s.valHasEsc && s.valFirstByte > ' ' && s.valFirstByte < 0x80 && s.valLastByte > ' ' && s.valLastByte < 0x80 {
				ref.hasURL = true
				ref.urlNeedsTrim = false
			} else {
				ref.urlMayNeedTrim = true
			}
			return
		}
		str := s.decodeValBuf()
		trimmed := strings.TrimSpace(str)
		if trimmed != "" {
			ref.hasURL = true
			ref.urlNeedsTrim = len(trimmed) != len(str)
			ref.urlLen = copy(ref.urlBuf[:], trimmed)
		} else {
			ref.hasURL = false
			ref.urlNeedsTrim = false
			ref.urlLen = 0
		}
	case xaiValRefImageURL, xaiValRefNestedURL:
		if target == xaiValRefNestedURL && ref.hasImageURLVal {
			return
		}
		if !s.valHasEsc && !s.valHasNonSpace {
			return
		}
		if s.valOverflow {
			ref.hasImageURLVal = true
			ref.imageURLOverflow = true
			ref.imageURLNested = target == xaiValRefNestedURL
			return
		}
		str := s.decodeValBuf()
		trimmed := strings.TrimSpace(str)
		if trimmed != "" {
			ref.hasImageURLVal = true
			ref.imageURLOverflow = false
			ref.imageURLNested = target == xaiValRefNestedURL
			ref.imageURLLen = copy(ref.imageURLBuf[:], trimmed)
		}
	}
}

func (s *xaiImageRefScanner) finishRefObject(closingDepth int) {
	ref := &s.refStack[len(s.refStack)-1]
	s.refStack = s.refStack[:len(s.refStack)-1]

	if ref.urlMayNeedTrim {
		path := s.buildPathUpTo(closingDepth)
		if path != "" {
			s.edits = append(s.edits, xaiImageRefEdit{
				path:        path,
				resolveMode: xaiResolveURL,
				setURL:      true,
				hasImageURL: ref.hasImageURL,
			})
		}
		return
	}

	if ref.hasURL {
		if !ref.urlNeedsTrim && !ref.hasImageURL {
			return
		}
		path := s.buildPathUpTo(closingDepth)
		if path == "" {
			return
		}
		if ref.urlOverflow {
			s.edits = append(s.edits, xaiImageRefEdit{
				path:        path,
				resolveMode: xaiResolveURL,
				setURL:      ref.urlNeedsTrim,
				hasImageURL: ref.hasImageURL,
			})
			return
		}
		s.edits = append(s.edits, xaiImageRefEdit{
			path:        path,
			url:         strings.Clone(string(ref.urlBuf[:ref.urlLen])),
			setURL:      true,
			hasImageURL: ref.hasImageURL,
		})
		return
	}

	if ref.hasImageURLVal {
		path := s.buildPathUpTo(closingDepth)
		if path == "" {
			return
		}
		if ref.imageURLOverflow {
			mode := xaiResolveImageURL
			if ref.imageURLNested {
				mode = xaiResolveNestedImageURL
			}
			s.edits = append(s.edits, xaiImageRefEdit{
				path:        path,
				resolveMode: mode,
				setURL:      true,
				hasImageURL: ref.hasImageURL,
			})
			return
		}
		s.edits = append(s.edits, xaiImageRefEdit{
			path:        path,
			url:         strings.Clone(string(ref.imageURLBuf[:ref.imageURLLen])),
			setURL:      true,
			hasImageURL: ref.hasImageURL,
		})
	}
}

func (s *xaiImageRefScanner) finishKey() {
	d := len(s.frames) - 1
	if d <= 0 {
		return
	}
	if s.curKeyHasEsc {
		rawKey := s.keyArena[s.curKeyStart:]
		var stackQuoted [260]byte
		var quoted []byte
		if 2+len(rawKey) <= len(stackQuoted) {
			stackQuoted[0] = '"'
			copy(stackQuoted[1:], rawKey)
			stackQuoted[1+len(rawKey)] = '"'
			quoted = stackQuoted[:2+len(rawKey)]
		} else {
			quoted = make([]byte, 2+len(rawKey))
			quoted[0] = '"'
			copy(quoted[1:], rawKey)
			quoted[1+len(rawKey)] = '"'
		}
		unescaped := util.ParseGJSONBytesNoCopy(quoted).String()
		s.keyArena = append(s.keyArena[:s.curKeyStart], unescaped...)
	}
	f := &s.frames[d]
	f.keyStart = s.curKeyStart
	f.keyEnd = len(s.keyArena)
	keyBytes := s.keyArena[f.keyStart:f.keyEnd]
	switch string(keyBytes) {
	case "image":
		f.keyKind = xaiKeyImage
	case "images", "reference_images":
		f.keyKind = xaiKeyImages
	case "url":
		f.keyKind = xaiKeyURL
	case "image_url":
		f.keyKind = xaiKeyImageURL
	default:
		f.keyKind = xaiKeyOther
	}
}

func (s *xaiImageRefScanner) Write(p []byte) (int, error) {
	i := 0
	for i < len(p) {
		if s.inStr {
			if !s.strEsc && !s.strIsKey && s.valTarget == xaiValIgnore {
				for i < len(p) && p[i] != '"' && p[i] != '\\' {
					i++
				}
				if i >= len(p) {
					break
				}
			}
			c := p[i]
			if s.strEsc {
				if s.strIsKey {
					s.keyArena = append(s.keyArena, c)
				} else if s.valTarget != xaiValIgnore {
					if !s.valOverflow {
						if s.valLen < len(s.valBuf) {
							s.valBuf[s.valLen] = c
							s.valLen++
						} else {
							s.valOverflow = true
						}
					}
					s.valLastByte = 0
				}
				s.strEsc = false
				i++
				continue
			}
			if c == '\\' {
				if s.strIsKey {
					s.curKeyHasEsc = true
					s.keyArena = append(s.keyArena, c)
				} else if s.valTarget != xaiValIgnore {
					s.valHasEsc = true
					if !s.valOverflow {
						if s.valLen < len(s.valBuf) {
							s.valBuf[s.valLen] = c
							s.valLen++
						} else {
							s.valOverflow = true
						}
					}
					s.valLastByte = 0
				}
				s.strEsc = true
				i++
				continue
			}
			if c == '"' {
				s.inStr = false
				if s.strIsKey {
					s.strIsKey = false
					s.finishKey()
				} else if s.valTarget != xaiValIgnore {
					s.finishValString()
				}
				i++
				continue
			}
			if s.strIsKey {
				s.keyArena = append(s.keyArena, c)
			} else if s.valTarget != xaiValIgnore {
				if s.valLen == 0 && !s.valOverflow {
					s.valFirstByte = c
				}
				if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
					s.valHasNonSpace = true
				}
				s.valLastByte = c
				if !s.valOverflow {
					if s.valLen < len(s.valBuf) {
						s.valBuf[s.valLen] = c
						s.valLen++
					} else {
						s.valOverflow = true
					}
				}
			}
			i++
			continue
		}

		for i < len(p) && (p[i] == ' ' || p[i] == '\t' || p[i] == '\r' || p[i] == '\n') {
			i++
		}
		if i >= len(p) {
			break
		}
		c := p[i]
		d := len(s.frames) - 1
		switch c {
		case ':':
			if d > 0 {
				s.frames[d].inKey = false
			}
			i++
		case ',':
			if d > 0 {
				if s.frames[d].isObj {
					s.frames[d].inKey = true
				} else {
					s.frames[d].elemIdx++
				}
			}
			i++
		case '}', ']':
			if d > 0 {
				if len(s.refStack) > 0 && s.refStack[len(s.refStack)-1].depth == d {
					s.finishRefObject(d)
				}
				s.keyArena = s.keyArena[:s.frames[d-1].keyEnd]
				s.frames = s.frames[:d]
			}
			i++
		case '"':
			s.inStr = true
			s.strEsc = false
			if d > 0 && s.frames[d].isObj && s.frames[d].inKey {
				s.strIsKey = true
				s.valTarget = xaiValIgnore
				s.curKeyStart = s.frames[d-1].keyEnd
				s.curKeyHasEsc = false
				s.keyArena = s.keyArena[:s.curKeyStart]
			} else {
				s.strIsKey = false
				s.valTarget = xaiValIgnore
				if len(s.refStack) > 0 {
					ref := &s.refStack[len(s.refStack)-1]
					if d == ref.depth && s.frames[d].isObj {
						switch s.frames[d].keyKind {
						case xaiKeyURL:
							s.valTarget = xaiValRefURL
						case xaiKeyImageURL:
							ref.hasImageURL = true
							s.valTarget = xaiValRefImageURL
						}
					} else if d == ref.depth+1 && s.frames[d].isImageURLObj && s.frames[d].keyKind == xaiKeyURL {
						s.valTarget = xaiValRefNestedURL
					}
					if s.valTarget != xaiValIgnore {
						s.valHasEsc = false
						s.valHasNonSpace = false
						s.valOverflow = false
						s.valFirstByte = 0
						s.valLastByte = 0
						s.valLen = 0
					}
				}
			}
			i++
		case '{', '[':
			isImagesArr := false
			isImageURLObj := false
			isRefObj := false
			if d > 0 {
				parent := &s.frames[d]
				if len(s.refStack) > 0 {
					ref := &s.refStack[len(s.refStack)-1]
					if d == ref.depth && parent.isObj && parent.keyKind == xaiKeyImageURL {
						ref.hasImageURL = true
						if c == '{' {
							isImageURLObj = true
						}
					}
				}
				if c == '{' {
					if (parent.isObj && parent.keyKind == xaiKeyImage) || (!parent.isObj && parent.isImagesArr) {
						isRefObj = true
					}
				} else if c == '[' && parent.isObj && parent.keyKind == xaiKeyImages {
					isImagesArr = true
				}
			}
			prevKeyEnd := 0
			if d > 0 {
				prevKeyEnd = s.frames[d].keyEnd
			}
			newDepth := len(s.frames)
			s.frames = append(s.frames, xaiScanFrame{
				isObj:         c == '{',
				inKey:         c == '{',
				isImagesArr:   isImagesArr,
				isImageURLObj: isImageURLObj,
				keyStart:      prevKeyEnd,
				keyEnd:        prevKeyEnd,
			})
			if isRefObj {
				s.refStack = append(s.refStack, xaiRefCandidate{depth: newDepth})
			}
			i++
		default:
			if d > 0 && len(s.refStack) > 0 {
				ref := &s.refStack[len(s.refStack)-1]
				if d == ref.depth && s.frames[d].isObj && s.frames[d].keyKind == xaiKeyImageURL {
					ref.hasImageURL = true
				}
			}
			i++
		}
	}
	return len(p), nil
}

func isSkippedUnderDeletedXAIImageURL(path string, edits []xaiImageRefEdit) bool {
	for i := range edits {
		outer := &edits[i]
		if !outer.valid || !outer.hasImageURL || outer.path == "" || len(path) <= len(outer.path)+11 {
			continue
		}
		if strings.HasPrefix(path, outer.path) && strings.HasPrefix(path[len(outer.path):], ".image_url.") {
			return true
		}
	}
	return false
}

func normalizeXAIImageRefsDoc(doc *ojson.Doc) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	scanner := xaiImageRefScannerPool.Get().(*xaiImageRefScanner)
	scanner.init()
	defer func() {
		scanner.reset()
		xaiImageRefScannerPool.Put(scanner)
	}()
	_, _ = doc.WriteTo(scanner)
	if len(scanner.edits) == 0 {
		return false
	}
	for i := range scanner.edits {
		edit := &scanner.edits[i]
		switch edit.resolveMode {
		case xaiResolveNone:
			edit.valid = true
		case xaiResolveURL:
			urlRes := doc.Get(edit.path + ".url")
			var orig string
			if urlRes.Type == ojson.String {
				orig = urlRes.String()
			}
			trimmed := strings.TrimSpace(orig)
			if trimmed == "" {
				imgRes := doc.Get(edit.path + ".image_url")
				if imgRes.Type == ojson.String {
					trimmed = strings.TrimSpace(imgRes.String())
				} else if imgRes.IsObject() {
					if nested := imgRes.Get("url"); nested.Type == ojson.String {
						trimmed = strings.TrimSpace(nested.String())
					}
				}
				if trimmed == "" {
					continue
				}
				edit.setURL = true
			} else if !edit.hasImageURL && trimmed == orig {
				continue
			} else {
				edit.setURL = trimmed != orig
			}
			edit.url = trimmed
			edit.valid = true
		case xaiResolveImageURL:
			edit.url = strings.TrimSpace(doc.Get(edit.path + ".image_url").String())
			if edit.url != "" {
				edit.valid = true
			}
		case xaiResolveNestedImageURL:
			edit.url = strings.TrimSpace(doc.Get(edit.path + ".image_url.url").String())
			if edit.url != "" {
				edit.valid = true
			}
		}
	}
	changed := false
	for i := range scanner.edits {
		edit := &scanner.edits[i]
		if !edit.valid || isSkippedUnderDeletedXAIImageURL(edit.path, scanner.edits) {
			continue
		}
		if edit.setURL {
			if errSet := doc.Set(edit.path+".url", edit.url); errSet == nil {
				changed = true
			}
		}
		if edit.hasImageURL {
			if errDel := doc.Delete(edit.path + ".image_url"); errDel == nil {
				changed = true
			}
		}
	}
	return changed
}

func normalizeXAIImageRefs(body []byte) []byte {
	if !xaiBytesMayContainImageRef(body) || !ojson.ValidBytes(body) {
		return body
	}
	doc := ojson.NewDoc(body)
	if !normalizeXAIImageRefsDoc(doc) {
		return body
	}
	return doc.Bytes()
}

func xaiIsVideoRequest(opts cliproxyexecutor.Options) bool {
	return opts.SourceFormat.String() == xaiVideoHandlerType
}

func xaiIsSpeechRequest(opts cliproxyexecutor.Options) bool {
	return opts.SourceFormat.String() == xaiSpeechHandlerType
}

// xaiSpeechRequestURL stays on the official API. cli-chat-proxy does not implement /tts,
// and a 404 there would cool the OAuth auth down as not_found.
func xaiSpeechRequestURL(auth *cliproxyauth.Auth) string {
	return strings.TrimSuffix(xaiCompactBaseURL(auth), "/") + xaiTTSPath
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
	doc := ojson.NewDoc(body)
	if !preserveXAIResponsesOutputControlsDoc(doc, source, from) {
		return body
	}
	return doc.Bytes()
}

func preserveXAIResponsesOutputControlsDoc(doc *ojson.Doc, source []byte, from sdktranslator.Format) bool {
	if doc == nil {
		return false
	}
	var maxOutputTokens ojson.Result
	switch from {
	case sdktranslator.FormatOpenAI:
		maxOutputTokens = ojson.GetBytesNoCopy(source, "max_completion_tokens")
		if !maxOutputTokens.Exists() || maxOutputTokens.Type == ojson.Null {
			maxOutputTokens = ojson.GetBytesNoCopy(source, "max_tokens")
		}
	case sdktranslator.FormatOpenAIResponse:
		maxOutputTokens = ojson.GetBytesNoCopy(source, "max_output_tokens")
	default:
		return false
	}

	changed := false
	if maxOutputTokens.Exists() && maxOutputTokens.Type != ojson.Null {
		if doc.SetRawBytes("max_output_tokens", []byte(maxOutputTokens.Raw)) == nil {
			changed = true
		}
	}
	for _, field := range []string{"temperature", "top_p", "top_k"} {
		value := ojson.GetBytesNoCopy(source, field)
		if value.Exists() && value.Type != ojson.Null {
			if doc.SetRawBytes(field, []byte(value.Raw)) == nil {
				changed = true
			}
		}
	}
	return changed
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

// ensureXAINativeXSearchTool appends {"type":"x_search"} when the final tools
// list does not already include native X Search. When tool_choice restricts the
// model to allowed_tools, x_search is also added there (without duplicates) so
// Grok can select the injected tool. When injection is enabled, HTTP and websocket
// executors both prepare payloads through prepareResponsesRequestTo, so this runs
// once before the body is submitted upstream.
func ensureXAINativeXSearchTool(body []byte) []byte {
	if !ojson.ValidBytes(body) {
		return body
	}
	doc := ojson.NewDoc(body)
	if !ensureXAINativeXSearchToolDoc(doc) {
		return body
	}
	return doc.Bytes()
}

func ensureXAINativeXSearchToolDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	changed := false
	if !xaiRequestHasNativeXSearchDoc(doc) {
		tools := doc.Get("tools")
		if !tools.Exists() || !tools.IsArray() {
			if doc.SetRawBytes("tools", []byte(`[{"type":"x_search"}]`)) == nil {
				changed = true
			}
		} else {
			if doc.SetRawBytes("tools.-1", xaiXSearchToolJSON) == nil {
				changed = true
			}
		}
	}
	if ensureXAINativeXSearchAllowedToolsDoc(doc) {
		changed = true
	}
	return changed
}

// ensureXAINativeXSearchAllowedTools appends x_search to tool_choice.tools when
// the choice mode is allowed_tools and x_search is not already listed.
func ensureXAINativeXSearchAllowedTools(body []byte) []byte {
	doc := ojson.NewDoc(body)
	if !ensureXAINativeXSearchAllowedToolsDoc(doc) {
		return body
	}
	return doc.Bytes()
}

func ensureXAINativeXSearchAllowedToolsDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	choice := doc.Get("tool_choice")
	if !choice.IsObject() || choice.Get("type").String() != "allowed_tools" {
		return false
	}
	allowed := choice.Get("tools")
	if !allowed.Exists() || !allowed.IsArray() {
		return doc.SetRawBytes("tool_choice.tools", []byte(`[{"type":"x_search"}]`)) == nil
	}
	for _, tool := range allowed.Array() {
		if strings.TrimSpace(tool.Get("type").String()) == xaiXSearchToolType {
			return false
		}
	}
	return doc.SetRawBytes("tool_choice.tools.-1", xaiXSearchToolJSON) == nil
}

// xaiHasClientWebSearchFunction reports whether body declares a client function
// or custom tool named "web_search" (without namespace), excluding folded namespace dispatchers.
func xaiHasClientWebSearchFunction(body []byte, namespaceTools map[string]xaiNamespaceToolRef) bool {
	if !ojson.ValidBytes(body) {
		return false
	}
	return xaiHasClientWebSearchFunctionResult(ojson.GetBytesNoCopy(body, "tools"), namespaceTools)
}

func xaiHasClientWebSearchFunctionDoc(doc *ojson.Doc, namespaceTools map[string]xaiNamespaceToolRef) bool {
	if doc == nil {
		return false
	}
	return xaiHasClientWebSearchFunctionResult(doc.Get("tools"), namespaceTools)
}

func xaiHasClientWebSearchFunctionResult(tools ojson.Result, namespaceTools map[string]xaiNamespaceToolRef) bool {
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		toolType := strings.TrimSpace(tool.Get("type").String())
		name := strings.TrimSpace(tool.Get("name").String())
		if (toolType == xaiFunctionToolType || toolType == xaiCustomToolType) && name == xaiWebSearchToolType {
			if _, isNamespace := namespaceTools[name]; isNamespace {
				continue
			}
			return true
		}
	}
	return false
}

// xaiBodyHasToolNamed reports whether any tool in tools or input has the given name.
func xaiBodyHasToolNamed(body []byte, name string) bool {
	return xaiToolsOrInputHaveToolNamed(ojson.GetBytesNoCopy(body, "tools"), ojson.GetBytesNoCopy(body, "input"), name)
}

func xaiBodyHasToolNamedDoc(doc *ojson.Doc, name string) bool {
	if doc == nil {
		return false
	}
	return xaiToolsOrInputHaveToolNamed(doc.Get("tools"), doc.Get("input"), name)
}

func xaiToolsOrInputHaveToolNamed(tools, input ojson.Result, name string) bool {
	if tools.IsArray() {
		for _, tool := range tools.Array() {
			if strings.TrimSpace(tool.Get("name").String()) == name {
				return true
			}
			if nested := tool.Get("tools"); nested.IsArray() {
				for _, child := range nested.Array() {
					if strings.TrimSpace(child.Get("name").String()) == name {
						return true
					}
				}
			}
		}
	}
	if input.IsArray() {
		for _, item := range input.Array() {
			if strings.TrimSpace(item.Get("name").String()) == name {
				return true
			}
		}
	}
	return false
}

// xaiResolveClientWebSearchAlias picks a candidate alias that does not collide
// with any client tool already present in the request.
func xaiResolveClientWebSearchAlias(body []byte) string {
	candidate := xaiClientWebSearchAlias
	if !xaiBodyHasToolNamed(body, candidate) {
		return candidate
	}
	for i := 1; ; i++ {
		next := fmt.Sprintf("%s_%d", xaiClientWebSearchAlias, i)
		if !xaiBodyHasToolNamed(body, next) {
			return next
		}
	}
}

func xaiResolveClientWebSearchAliasDoc(doc *ojson.Doc) string {
	candidate := xaiClientWebSearchAlias
	if !xaiBodyHasToolNamedDoc(doc, candidate) {
		return candidate
	}
	for i := 1; ; i++ {
		next := fmt.Sprintf("%s_%d", xaiClientWebSearchAlias, i)
		if !xaiBodyHasToolNamedDoc(doc, next) {
			return next
		}
	}
}

// aliasXAIClientWebSearchInput renames client-declared function calls named
// "web_search" to alias in replayed input history.
func aliasXAIClientWebSearchInput(body []byte, alias string, namespaceTools map[string]xaiNamespaceToolRef) []byte {
	if !ojson.ValidBytes(body) || alias == "" {
		return body
	}
	doc := ojson.NewDoc(body)
	if !aliasXAIClientWebSearchInputDoc(doc, alias, namespaceTools) {
		return body
	}
	return doc.Bytes()
}

func aliasXAIClientWebSearchInputDoc(doc *ojson.Doc, alias string, namespaceTools map[string]xaiNamespaceToolRef) bool {
	if doc == nil || alias == "" {
		return false
	}
	if _, isNamespace := namespaceTools[xaiWebSearchToolType]; isNamespace {
		return false
	}
	input := doc.Get("input")
	if !input.IsArray() {
		return false
	}
	changed := false
	for idx, item := range input.Array() {
		itemType := strings.TrimSpace(item.Get("type").String())
		itemName := strings.TrimSpace(item.Get("name").String())
		itemNamespace := strings.TrimSpace(item.Get("namespace").String())
		if (itemType == "function_call" || itemType == "custom_tool_call" || itemType == "function_call_output") &&
			itemName == xaiWebSearchToolType && itemNamespace == "" {
			if doc.Set(fmt.Sprintf("input.%d.name", idx), alias) == nil {
				changed = true
			}
		}
	}
	return changed
}

// aliasXAIClientWebSearchFunction renames client-declared function tools named
// "web_search" to a non-colliding alias in tools declarations, tool_choice,
// and replayed input history to prevent xAI from hijacking them into hosted
// server-side search. Namespace dispatcher tools named "web_search" are untouched.
func aliasXAIClientWebSearchFunction(body []byte, alias string, namespaceTools map[string]xaiNamespaceToolRef) []byte {
	if !ojson.ValidBytes(body) || alias == "" {
		return body
	}
	doc := ojson.NewDoc(body)
	if !aliasXAIClientWebSearchFunctionDoc(doc, alias, namespaceTools) {
		return body
	}
	return doc.Bytes()
}

func aliasXAIClientWebSearchFunctionDoc(doc *ojson.Doc, alias string, namespaceTools map[string]xaiNamespaceToolRef) bool {
	if doc == nil || alias == "" {
		return false
	}
	changed := false
	// 1. Alias in tools (excluding namespace dispatchers)
	tools := doc.Get("tools")
	if tools.IsArray() {
		for idx, tool := range tools.Array() {
			toolType := strings.TrimSpace(tool.Get("type").String())
			name := strings.TrimSpace(tool.Get("name").String())
			if (toolType == xaiFunctionToolType || toolType == xaiCustomToolType) && name == xaiWebSearchToolType {
				if _, isNamespace := namespaceTools[name]; isNamespace {
					continue
				}
				if doc.Set(fmt.Sprintf("tools.%d.name", idx), alias) == nil {
					changed = true
				}
			}
		}
	}

	// 2. Alias in tool_choice (only when unnamespaced and not a namespace dispatcher)
	choice := doc.Get("tool_choice")
	if choice.IsObject() {
		if fnName := choice.Get("function.name"); fnName.Exists() && strings.TrimSpace(fnName.String()) == xaiWebSearchToolType {
			if strings.TrimSpace(choice.Get("function.namespace").String()) == "" {
				if _, isNamespace := namespaceTools[xaiWebSearchToolType]; !isNamespace {
					if doc.Set("tool_choice.function.name", alias) == nil {
						changed = true
					}
				}
			}
		}
		if name := choice.Get("name"); name.Exists() && strings.TrimSpace(name.String()) == xaiWebSearchToolType {
			if strings.TrimSpace(choice.Get("namespace").String()) == "" {
				choiceType := strings.TrimSpace(choice.Get("type").String())
				if choiceType == xaiFunctionToolType || choiceType == "tool" {
					if _, isNamespace := namespaceTools[xaiWebSearchToolType]; !isNamespace {
						if doc.Set("tool_choice.name", alias) == nil {
							changed = true
						}
					}
				}
			}
		}
		if allowed := choice.Get("tools"); allowed.IsArray() {
			for idx, allowedTool := range allowed.Array() {
				if strings.TrimSpace(allowedTool.Get("namespace").String()) != "" {
					continue
				}
				if _, isNamespace := namespaceTools[xaiWebSearchToolType]; isNamespace {
					continue
				}
				allowedType := strings.TrimSpace(allowedTool.Get("type").String())
				allowedName := strings.TrimSpace(allowedTool.Get("name").String())
				if (allowedType == xaiFunctionToolType || allowedType == "tool") && allowedName == xaiWebSearchToolType {
					if doc.Set(fmt.Sprintf("tool_choice.tools.%d.name", idx), alias) == nil {
						changed = true
					}
				} else if allowedName == xaiWebSearchToolType && allowedType != xaiWebSearchToolType {
					if doc.Set(fmt.Sprintf("tool_choice.tools.%d.name", idx), alias) == nil {
						changed = true
					}
				}
			}
		}
	}

	// 3. Alias in input (conversation history, only when unnamespaced)
	if aliasXAIClientWebSearchInputDoc(doc, alias, namespaceTools) {
		changed = true
	}
	return changed
}

// normalizeXAIForcedWebSearchToolChoice rewrites web_search choices into a
// ModelToolChoice variant accepted by xAI chat-proxy.
func normalizeXAIForcedWebSearchToolChoice(body []byte) []byte {
	return normalizeXAIForcedHostedToolChoice(body, xaiWebSearchToolType)
}

func normalizeXAIForcedWebSearchToolChoiceDoc(doc *ojson.Doc) bool {
	return normalizeXAIForcedHostedToolChoiceDoc(doc, xaiWebSearchToolType)
}

// normalizeXAIForcedImageGenerationToolChoice rewrites image_generation choices
// into a ModelToolChoice variant accepted by xAI chat-proxy.
func normalizeXAIForcedImageGenerationToolChoice(body []byte) []byte {
	return normalizeXAIForcedHostedToolChoice(body, xaiImageGenerationToolType)
}

func normalizeXAIForcedImageGenerationToolChoiceDoc(doc *ojson.Doc) bool {
	return normalizeXAIForcedHostedToolChoiceDoc(doc, xaiImageGenerationToolType)
}

// normalizeXAIForcedHostedToolChoice rewrites choices for a hosted tool (web_search
// or image_generation) into a ModelToolChoice variant accepted by xAI chat-proxy.
// `{type: <toolType>}` becomes the string "required" and the tools list is reduced
// to that hosted tool so later x_search injection cannot broaden the restriction.
// An allowed_tools list that only names that hosted tool becomes the original mode
// ("auto" or "required") and is likewise reduced to that hosted tool. Mixed lists
// drop the hosted tool entry so the remaining hosted/function choices can still
// deserialize.
func normalizeXAIForcedHostedToolChoice(body []byte, toolType string) []byte {
	doc := ojson.NewDoc(body)
	if !normalizeXAIForcedHostedToolChoiceDoc(doc, toolType) {
		return body
	}
	return doc.Bytes()
}

func normalizeXAIForcedHostedToolChoiceDoc(doc *ojson.Doc, toolType string) bool {
	if doc == nil {
		return false
	}
	choice := doc.Get("tool_choice")
	if !choice.IsObject() {
		return false
	}
	choiceType := strings.TrimSpace(choice.Get("type").String())
	if choiceType == toolType {
		changed := xaiKeepOnlyHostedToolsDoc(doc, toolType)
		if doc.Set("tool_choice", "required") == nil {
			changed = true
		}
		return changed
	}
	if choiceType != "allowed_tools" {
		return false
	}
	allowed := choice.Get("tools")
	if !allowed.IsArray() {
		return false
	}
	filtered := make([][]byte, 0, len(allowed.Array()))
	stripped := false
	for _, tool := range allowed.Array() {
		if strings.TrimSpace(tool.Get("type").String()) == toolType {
			stripped = true
			continue
		}
		filtered = append(filtered, []byte(tool.Raw))
	}
	if !stripped {
		return false
	}
	if len(filtered) == 0 {
		mode := strings.TrimSpace(choice.Get("mode").String())
		if mode != "auto" {
			mode = "required"
		}
		changed := xaiKeepOnlyHostedToolsDoc(doc, toolType)
		if doc.Set("tool_choice", mode) == nil {
			changed = true
		}
		return changed
	}
	return doc.SetRawBytes("tool_choice.tools", helps.JoinRawJSONArray(filtered)) == nil
}

func xaiKeepOnlyHostedTools(body []byte, toolType string) []byte {
	doc := ojson.NewDoc(body)
	if !xaiKeepOnlyHostedToolsDoc(doc, toolType) {
		return body
	}
	return doc.Bytes()
}

func xaiKeepOnlyHostedToolsDoc(doc *ojson.Doc, toolType string) bool {
	if doc == nil {
		return false
	}
	tools := doc.Get("tools")
	if !tools.IsArray() {
		return false
	}
	kept := make([][]byte, 0, 1)
	for _, tool := range tools.Array() {
		if strings.TrimSpace(tool.Get("type").String()) == toolType {
			kept = append(kept, []byte(tool.Raw))
		}
	}
	if len(kept) == 0 || len(kept) == len(tools.Array()) {
		return false
	}
	return doc.SetRawBytes("tools", helps.JoinRawJSONArray(kept)) == nil
}

func xaiToolChoiceRequiresHostedToolOnly(body []byte, toolType string) bool {
	return xaiToolChoiceRequiresHostedToolOnlyResults(ojson.GetBytesNoCopy(body, "tool_choice"), ojson.GetBytesNoCopy(body, "tools"), toolType)
}

func xaiToolChoiceRequiresHostedToolOnlyDoc(doc *ojson.Doc, toolType string) bool {
	if doc == nil {
		return false
	}
	return xaiToolChoiceRequiresHostedToolOnlyResults(doc.Get("tool_choice"), doc.Get("tools"), toolType)
}

func xaiToolChoiceRequiresHostedToolOnlyResults(choice, tools ojson.Result, toolType string) bool {
	if choice.Type != ojson.String {
		return false
	}
	switch choice.String() {
	case "required", "auto":
	default:
		return false
	}
	if !tools.IsArray() || len(tools.Array()) == 0 {
		return false
	}
	for _, tool := range tools.Array() {
		if strings.TrimSpace(tool.Get("type").String()) != toolType {
			return false
		}
	}
	return true
}

func xaiToolChoiceRequiresImageGenerationOnly(body []byte) bool {
	return xaiToolChoiceRequiresHostedToolOnly(body, xaiImageGenerationToolType)
}

func xaiToolChoiceRequiresWebSearchOnly(body []byte) bool {
	return xaiToolChoiceRequiresHostedToolOnly(body, xaiWebSearchToolType)
}

func xaiToolChoiceRequiresHostedToolOnlyAny(body []byte) bool {
	return xaiToolChoiceRequiresImageGenerationOnly(body) || xaiToolChoiceRequiresWebSearchOnly(body)
}

func xaiToolChoiceRequiresHostedToolOnlyAnyDoc(doc *ojson.Doc) bool {
	return xaiToolChoiceRequiresHostedToolOnlyDoc(doc, xaiImageGenerationToolType) ||
		xaiToolChoiceRequiresHostedToolOnlyDoc(doc, xaiWebSearchToolType)
}

func xaiSetToolChoiceString(body []byte, value string) []byte {
	updated, errSet := ojson.SetBytes(body, "tool_choice", value)
	if errSet != nil {
		return body
	}
	return updated
}

// pruneXAIOrphanedToolChoice removes tool_choice entries that no longer match
// any remaining tool after normalizeXAITools filtering. Forced choices that
// reference a deleted tool are dropped entirely; allowed_tools lists keep only
// choices that still resolve against the post-normalization tools set.
func pruneXAIOrphanedToolChoice(body []byte) []byte {
	if !ojson.ValidBytes(body) {
		return body
	}
	doc := ojson.NewDoc(body)
	if !pruneXAIOrphanedToolChoiceDoc(doc) {
		return body
	}
	return doc.Bytes()
}

func pruneXAIOrphanedToolChoiceDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	choice := doc.Get("tool_choice")
	if !choice.Exists() {
		return false
	}
	available := collectXAIAvailableToolChoiceKeysDoc(doc)
	if choice.Type == ojson.String {
		// auto / none / required are not tool references.
		return false
	}
	if !choice.IsObject() {
		return false
	}
	choiceType := strings.TrimSpace(choice.Get("type").String())
	switch choiceType {
	case "allowed_tools":
		return pruneXAIAllowedToolsChoiceDoc(doc, available)
	default:
		if choiceType == "" {
			return false
		}
		if xaiToolChoiceMatchesAvailable(choice, available) {
			return false
		}
		return doc.Delete("tool_choice") == nil
	}
}

func pruneXAIAllowedToolsChoice(body []byte, available map[xaiToolChoiceKey]struct{}) []byte {
	doc := ojson.NewDoc(body)
	if !pruneXAIAllowedToolsChoiceDoc(doc, available) {
		return body
	}
	return doc.Bytes()
}

func pruneXAIAllowedToolsChoiceDoc(doc *ojson.Doc, available map[xaiToolChoiceKey]struct{}) bool {
	if doc == nil {
		return false
	}
	allowed := doc.Get("tool_choice.tools")
	if !allowed.Exists() || !allowed.IsArray() {
		return doc.Delete("tool_choice") == nil
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
		return false
	}
	if len(filtered) == 0 {
		return doc.Delete("tool_choice") == nil
	}
	return doc.SetRawBytes("tool_choice.tools", helps.JoinRawJSONArray(filtered)) == nil
}

// xaiToolChoiceKey identifies a selectable tool the way xAI tool_choice entries
// reference it after namespace qualification: type alone for host tools, or
// type+name for function tools.
type xaiToolChoiceKey struct {
	toolType string
	name     string
}

func collectXAIAvailableToolChoiceKeys(body []byte) map[xaiToolChoiceKey]struct{} {
	return collectXAIAvailableToolChoiceKeysResults(ojson.GetBytesNoCopy(body, "tools"), ojson.GetBytesNoCopy(body, "input"))
}

func collectXAIAvailableToolChoiceKeysDoc(doc *ojson.Doc) map[xaiToolChoiceKey]struct{} {
	if doc == nil {
		return make(map[xaiToolChoiceKey]struct{})
	}
	return collectXAIAvailableToolChoiceKeysResults(doc.Get("tools"), doc.Get("input"))
}

func collectXAIAvailableToolChoiceKeysResults(toolsResult, inputResult ojson.Result) map[xaiToolChoiceKey]struct{} {
	keys := make(map[xaiToolChoiceKey]struct{})
	collect := func(tools ojson.Result) {
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
	collect(toolsResult)
	if inputResult.IsArray() {
		for _, item := range inputResult.Array() {
			if item.Get("type").String() == "additional_tools" {
				collect(item.Get("tools"))
			}
		}
	}
	return keys
}

func xaiToolChoiceMatchesAvailable(choice ojson.Result, available map[xaiToolChoiceKey]struct{}) bool {
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

func xaiCountFlattenedToolsWithSeen(tools ojson.Result, seenFunctionNames map[string]struct{}) int {
	if !tools.Exists() || !tools.IsArray() {
		return 0
	}
	count := 0
	for _, tool := range tools.Array() {
		switch tool.Get("type").String() {
		case xaiNamespaceToolType:
			namespaceName := tool.Get("name").String()
			if nestedTools := tool.Get("tools"); nestedTools.IsArray() {
				for _, nestedTool := range nestedTools.Array() {
					if nestedTool.Get("type").String() == xaiToolSearchType {
						continue
					}
					qualifiedName := qualifyXAINamespaceToolName(namespaceName, nestedTool.Get("name").String())
					if qualifiedName != "" && seenFunctionNames != nil {
						if _, seen := seenFunctionNames[qualifiedName]; seen {
							continue
						}
						seenFunctionNames[qualifiedName] = struct{}{}
					}
					count++
				}
			} else {
				count++
			}
		case xaiToolSearchType:
			// Tool search is stripped by normalizeXAITool
		case xaiFunctionToolType, xaiCustomToolType:
			name := strings.TrimSpace(tool.Get("name").String())
			if name != "" && seenFunctionNames != nil {
				if _, seen := seenFunctionNames[name]; seen {
					continue
				}
				seenFunctionNames[name] = struct{}{}
			}
			count++
		default:
			count++
		}
	}
	return count
}

func xaiCountFlattenedTools(tools ojson.Result) int {
	return xaiCountFlattenedToolsWithSeen(tools, make(map[string]struct{}))
}

func xaiTotalFlattenedToolsCount(body []byte, willInjectXSearch bool) int {
	seenFunctionNames := make(map[string]struct{})
	count := xaiCountFlattenedToolsWithSeen(ojson.GetBytesNoCopy(body, "tools"), seenFunctionNames)
	input := ojson.GetBytesNoCopy(body, "input")
	if input.Exists() && input.IsArray() {
		for _, item := range input.Array() {
			if item.Get("type").String() == "additional_tools" {
				count += xaiCountFlattenedToolsWithSeen(item.Get("tools"), seenFunctionNames)
			}
		}
	}
	if willInjectXSearch && !xaiRequestHasNativeXSearch(body) && !xaiToolChoiceRequiresHostedToolOnlyAny(body) {
		count++
	}
	return count
}

func xaiTotalFlattenedToolsCountDoc(doc *ojson.Doc, willInjectXSearch bool) int {
	if doc == nil {
		return 0
	}
	seenFunctionNames := make(map[string]struct{})
	count := xaiCountFlattenedToolsWithSeen(doc.Get("tools"), seenFunctionNames)
	input := doc.Get("input")
	if input.Exists() && input.IsArray() {
		for _, item := range input.Array() {
			if item.Get("type").String() == "additional_tools" {
				count += xaiCountFlattenedToolsWithSeen(item.Get("tools"), seenFunctionNames)
			}
		}
	}
	if willInjectXSearch && !xaiRequestHasNativeXSearchDoc(doc) && !xaiToolChoiceRequiresHostedToolOnlyAnyDoc(doc) {
		count++
	}
	return count
}

func xaiShouldFoldNamespaceTools(body []byte, willInjectXSearch bool) bool {
	return xaiTotalFlattenedToolsCount(body, willInjectXSearch) > xaiMaxTools
}

func xaiShouldFoldNamespaceToolsDoc(doc *ojson.Doc, willInjectXSearch bool) bool {
	return xaiTotalFlattenedToolsCountDoc(doc, willInjectXSearch) > xaiMaxTools
}

func collectXAINamespaceDeclarationsByName(body []byte) map[string][]ojson.Result {
	return collectXAINamespaceDeclarationsByNameResults(ojson.GetBytesNoCopy(body, "tools"), ojson.GetBytesNoCopy(body, "input"))
}

func collectXAINamespaceDeclarationsByNameDoc(doc *ojson.Doc) map[string][]ojson.Result {
	if doc == nil {
		return make(map[string][]ojson.Result)
	}
	return collectXAINamespaceDeclarationsByNameResults(doc.Get("tools"), doc.Get("input"))
}

func collectXAINamespaceDeclarationsByNameResults(toolsResult, inputResult ojson.Result) map[string][]ojson.Result {
	byName := make(map[string][]ojson.Result)
	collect := func(tools ojson.Result) {
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
			byName[namespaceName] = append(byName[namespaceName], tool)
		}
	}
	collect(toolsResult)
	if inputResult.Exists() && inputResult.IsArray() {
		for _, item := range inputResult.Array() {
			if item.Get("type").String() == "additional_tools" {
				collect(item.Get("tools"))
			}
		}
	}
	return byName
}

func buildXAINamespaceDispatcherTool(tool ojson.Result, extraDeclarations ...ojson.Result) []byte {
	namespaceName := strings.TrimSpace(tool.Get("name").String())
	if namespaceName == "" {
		return nil
	}
	description := strings.TrimSpace(tool.Get("description").String())

	declarations := make([]ojson.Result, 0, 1+len(extraDeclarations))
	declarations = append(declarations, tool)
	declarations = append(declarations, extraDeclarations...)

	var toolNames []string
	var toolDescriptions []string
	seenChildren := make(map[string]struct{})
	for _, decl := range declarations {
		if description == "" {
			description = strings.TrimSpace(decl.Get("description").String())
		}
		nestedTools := decl.Get("tools")
		if !nestedTools.IsArray() {
			continue
		}
		for _, child := range nestedTools.Array() {
			if child.Get("type").String() == xaiToolSearchType {
				continue
			}
			childName := strings.TrimSpace(child.Get("name").String())
			if childName == "" {
				continue
			}
			if _, exists := seenChildren[childName]; exists {
				continue
			}
			seenChildren[childName] = struct{}{}
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
					if ojson.Valid(inlined) {
						cleanDoc := ojson.NewDocString(inlined)
						if cleanDoc.Get("$defs").Exists() {
							_ = cleanDoc.Delete("$defs")
						}
						if cleanDoc.Get("definitions").Exists() {
							_ = cleanDoc.Delete("definitions")
						}
						paramStr = cleanDoc.String()
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

func normalizeXAIToolsDoc(doc *ojson.Doc) bool {
	return normalizeXAIToolsWithFoldDoc(doc, xaiShouldFoldNamespaceToolsDoc(doc, false))
}

func normalizeXAIToolsWithFold(body []byte, shouldFold bool) []byte {
	if !ojson.ValidBytes(body) {
		return body
	}
	doc := ojson.NewDoc(body)
	if !normalizeXAIToolsWithFoldDoc(doc, shouldFold) {
		return body
	}
	return doc.Bytes()
}

func normalizeXAIToolsWithFoldDoc(doc *ojson.Doc, shouldFold bool) bool {
	if doc == nil {
		return false
	}
	keepImageGeneration := xaiSupportsNativeImageGeneration(doc.Get("model").String())
	var namespaceDecls map[string][]ojson.Result
	if shouldFold {
		namespaceDecls = collectXAINamespaceDeclarationsByNameDoc(doc)
	}
	type pendingSet struct {
		path string
		raw  []byte
	}
	var updates []pendingSet
	planAtPath := func(path string) bool {
		tools := doc.Get(path)
		if !tools.Exists() || !tools.IsArray() {
			return true
		}
		filtered, changed, ok := normalizeXAIToolArrayWithNamespaces(tools, keepImageGeneration, shouldFold, namespaceDecls)
		if !ok {
			return false
		}
		if changed {
			updates = append(updates, pendingSet{path: path, raw: filtered})
		}
		return true
	}

	if !planAtPath("tools") {
		return false
	}
	input := doc.Get("input")
	if input.Exists() && input.IsArray() {
		for index, item := range input.Array() {
			if item.Get("type").String() != "additional_tools" {
				continue
			}
			if !planAtPath(fmt.Sprintf("input.%d.tools", index)) {
				return false
			}
		}
	}
	if len(updates) == 0 {
		return false
	}
	for _, u := range updates {
		if errSet := doc.SetRawBytes(u.path, u.raw); errSet != nil {
			return false
		}
	}
	return true
}

func xaiHasFunctionToolNamed(body []byte, name string) bool {
	return xaiHasFunctionToolNamedResults(ojson.GetBytesNoCopy(body, "tools"), ojson.GetBytesNoCopy(body, "input"), name)
}

func xaiHasFunctionToolNamedDoc(doc *ojson.Doc, name string) bool {
	if doc == nil {
		return false
	}
	return xaiHasFunctionToolNamedResults(doc.Get("tools"), doc.Get("input"), name)
}

func xaiHasFunctionToolNamedResults(tools, input ojson.Result, name string) bool {
	if name == "" {
		return false
	}
	if tools.IsArray() {
		for _, tool := range tools.Array() {
			if tool.Get("type").String() == xaiFunctionToolType && tool.Get("name").String() == name {
				return true
			}
		}
	}
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
	doc := ojson.NewDoc(body)
	if !clampXAIToolsLimitDoc(doc, maxTools, refs) {
		return body
	}
	return doc.Bytes()
}

func clampXAIToolsLimitDoc(doc *ojson.Doc, maxTools int, refs map[string]xaiNamespaceToolRef) bool {
	if doc == nil {
		return false
	}
	tools := doc.Get("tools")
	if !tools.IsArray() || len(tools.Array()) <= maxTools {
		return false
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
		return false
	}
	if errSet := doc.SetRawBytes("tools", raw); errSet != nil {
		return false
	}
	pruneXAIOrphanedToolChoiceDoc(doc)
	normalizeXAIToolChoiceForToolsDoc(doc)
	return true
}

// xaiDeduplicatedToolName returns the unique name key for tool declarations that
// must not repeat in the final xAI tools list (functions, custom tools, and
// namespace dispatchers). Hosted tools without a function name return "".
func xaiDeduplicatedToolName(tool ojson.Result) string {
	switch strings.TrimSpace(tool.Get("type").String()) {
	case xaiFunctionToolType, xaiCustomToolType, xaiNamespaceToolType:
		return strings.TrimSpace(tool.Get("name").String())
	default:
		return ""
	}
}

// promoteXAIAdditionalTools moves Responses Lite tool declarations to the
// top-level tools array because xAI does not accept additional_tools input items.
// Declarations are deduplicated by tool name with first-wins precedence so
// top-level tools remain authoritative over historical additional_tools copies.
func promoteXAIAdditionalTools(body []byte) []byte {
	if !ojson.ValidBytes(body) {
		return body
	}
	doc := ojson.NewDoc(body)
	if !promoteXAIAdditionalToolsDoc(doc) {
		return body
	}
	return doc.Bytes()
}

func promoteXAIAdditionalToolsDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	input := doc.Get("input")
	if !input.IsArray() {
		return false
	}

	inputItems := input.Array()
	remainingInput := make([]json.RawMessage, 0, len(inputItems))
	promotedTools := make([]ojson.Result, 0)
	for _, item := range inputItems {
		if item.Get("type").String() != "additional_tools" {
			remainingInput = append(remainingInput, json.RawMessage(item.Raw))
			continue
		}
		for _, tool := range item.Get("tools").Array() {
			promotedTools = append(promotedTools, tool)
		}
	}
	if len(remainingInput) == len(inputItems) {
		return false
	}

	rawInput, errMarshalInput := json.Marshal(remainingInput)
	if errMarshalInput != nil {
		return false
	}
	if len(promotedTools) == 0 {
		return doc.SetRawBytes("input", rawInput) == nil
	}

	topLevelTools := doc.Get("tools")
	topLevelArray := topLevelTools.Array()
	tools := make([]json.RawMessage, 0, len(topLevelArray)+len(promotedTools))
	seenNames := make(map[string]struct{}, len(topLevelArray)+len(promotedTools))
	if topLevelTools.IsArray() {
		for _, tool := range topLevelArray {
			if name := xaiDeduplicatedToolName(tool); name != "" {
				if _, exists := seenNames[name]; exists {
					continue
				}
				seenNames[name] = struct{}{}
			}
			tools = append(tools, json.RawMessage(tool.Raw))
		}
	}
	for _, tool := range promotedTools {
		if name := xaiDeduplicatedToolName(tool); name != "" {
			if _, exists := seenNames[name]; exists {
				continue
			}
			seenNames[name] = struct{}{}
		}
		tools = append(tools, json.RawMessage(tool.Raw))
	}
	rawTools, errMarshalTools := json.Marshal(tools)
	if errMarshalTools != nil {
		return false
	}
	if errSetInput := doc.SetRawBytes("input", rawInput); errSetInput != nil {
		return false
	}
	if errSetTools := doc.SetRawBytes("tools", rawTools); errSetTools != nil {
		return false
	}
	return true
}

func normalizeXAIToolArray(tools ojson.Result, keepImageGeneration, shouldFold bool) ([]byte, bool, bool) {
	return normalizeXAIToolArrayWithNamespaces(tools, keepImageGeneration, shouldFold, nil)
}

func normalizeXAIToolArrayWithNamespaces(tools ojson.Result, keepImageGeneration, shouldFold bool, namespaceDecls map[string][]ojson.Result) ([]byte, bool, bool) {
	toolItems := tools.Array()
	filtered := make([][]byte, 0, len(toolItems))
	seenNames := make(map[string]struct{}, len(toolItems))
	appendUnique := func(raw []byte) bool {
		if name := xaiDeduplicatedToolName(util.ParseGJSONBytesNoCopy(raw)); name != "" {
			if _, exists := seenNames[name]; exists {
				return true
			}
			seenNames[name] = struct{}{}
		}
		filtered = append(filtered, raw)
		return false
	}
	changed := false
	for _, tool := range toolItems {
		toolType := tool.Get("type").String()
		if toolType == xaiNamespaceToolType {
			changed = true
			if shouldFold {
				namespaceName := strings.TrimSpace(tool.Get("name").String())
				if dispatcher := buildXAINamespaceDispatcherTool(tool, namespaceDecls[namespaceName]...); len(dispatcher) > 0 {
					_ = appendUnique(dispatcher)
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
						if appendUnique(nestedRaw) {
							changed = true
						}
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
			if appendUnique(raw) {
				changed = true
			}
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
	doc := ojson.NewDoc(body)
	if !normalizeXAIToolChoiceForToolsDoc(doc) {
		return body
	}
	return doc.Bytes()
}

func normalizeXAIToolChoiceForToolsDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	tools := doc.Get("tools")
	hasTools := tools.Exists() && tools.IsArray() && len(tools.Array()) > 0
	if !hasTools {
		input := doc.Get("input")
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
		return false
	}
	changed := false
	if tools.Exists() {
		if doc.Delete("tools") == nil {
			changed = true
		}
	}
	if doc.Get("tool_choice").Exists() {
		if doc.Delete("tool_choice") == nil {
			changed = true
		}
	}
	if doc.Get("parallel_tool_calls").Exists() {
		if doc.Delete("parallel_tool_calls") == nil {
			changed = true
		}
	}
	return changed
}

// normalizeXAINamespaceToolChoice qualifies namespaced function choices using
// the same names sent in the flattened tools list. xAI does not accept the
// Responses namespace field on tool choices.
func normalizeXAINamespaceToolChoice(body []byte) []byte {
	return normalizeXAINamespaceToolChoiceWithFold(body, xaiShouldFoldNamespaceTools(body, false))
}

func normalizeXAINamespaceToolChoiceDoc(doc *ojson.Doc) bool {
	return normalizeXAINamespaceToolChoiceWithFoldDoc(doc, xaiShouldFoldNamespaceToolsDoc(doc, false))
}

func normalizeXAINamespaceToolChoiceWithFold(body []byte, shouldFold bool) []byte {
	if !ojson.ValidBytes(body) {
		return body
	}
	doc := ojson.NewDoc(body)
	if !normalizeXAINamespaceToolChoiceWithFoldDoc(doc, shouldFold) {
		return body
	}
	return doc.Bytes()
}

func normalizeXAINamespaceToolChoiceWithFoldDoc(doc *ojson.Doc, shouldFold bool) bool {
	if doc == nil {
		return false
	}
	type pendingChoiceEdit struct {
		path       string
		targetName string
	}
	var edits []pendingChoiceEdit
	planAtPath := func(path string) bool {
		toolChoice := doc.Get(path)
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
		if xaiHasFunctionToolNamedDoc(doc, namespaceName) {
			targetName = namespaceName
		} else if xaiHasFunctionToolNamedDoc(doc, qualifiedName) {
			targetName = qualifiedName
		} else if shouldFold {
			targetName = namespaceName
		} else {
			targetName = qualifiedName
		}
		if targetName == "" {
			return true
		}
		edits = append(edits, pendingChoiceEdit{path: path, targetName: targetName})
		return true
	}

	if !planAtPath("tool_choice") {
		return false
	}
	tools := doc.Get("tool_choice.tools")
	if tools.IsArray() {
		for index := range tools.Array() {
			if !planAtPath(fmt.Sprintf("tool_choice.tools.%d", index)) {
				return false
			}
		}
	}
	if len(edits) == 0 {
		return false
	}
	for _, edit := range edits {
		if errSet := doc.Set(edit.path+".name", edit.targetName); errSet != nil {
			return false
		}
		if errDel := doc.Delete(edit.path + ".namespace"); errDel != nil {
			return false
		}
	}
	return true
}

// xaiUnsupportedWebSearchFields are OpenAI Responses web_search options that the
// xAI Responses API rejects with "Argument not supported". Options xAI accepts,
// such as filters, user_location and search_content_types, are forwarded as is.
var xaiUnsupportedWebSearchFields = [...]string{"external_web_access", "search_context_size"}

// stripUnsupportedXAIWebSearchFields removes web_search options that xAI rejects
// and reports which ones were removed.
func stripUnsupportedXAIWebSearchFields(raw []byte) ([]byte, []string, bool) {
	doc := ojson.NewDoc(raw)
	removed, ok := stripUnsupportedXAIWebSearchFieldsDoc(doc)
	if !ok {
		return nil, nil, false
	}
	if len(removed) == 0 {
		return raw, nil, true
	}
	return doc.Bytes(), removed, true
}

func stripUnsupportedXAIWebSearchFieldsDoc(doc *ojson.Doc) ([]string, bool) {
	if doc == nil {
		return nil, false
	}
	var removed []string
	for _, field := range xaiUnsupportedWebSearchFields {
		if !doc.Get(field).Exists() {
			continue
		}
		if errDel := doc.Delete(field); errDel != nil {
			return nil, false
		}
		removed = append(removed, field)
	}
	return removed, true
}

func normalizeXAITool(tool ojson.Result, namespaceName string, keepImageGeneration bool) ([]byte, bool, bool) {
	toolType := tool.Get("type").String()
	changed := false
	if toolType == xaiToolSearchType {
		return nil, true, true
	}
	if toolType == xaiImageGenerationToolType && !keepImageGeneration {
		return nil, true, true
	}

	raw := []byte(tool.Raw)
	var toolDoc *ojson.Doc
	ensureToolDoc := func() *ojson.Doc {
		if toolDoc == nil {
			toolDoc = ojson.NewDoc(raw)
		}
		return toolDoc
	}
	getField := func(path string) ojson.Result {
		if toolDoc != nil {
			return toolDoc.Get(path)
		}
		return tool.Get(path)
	}

	if toolType == xaiFunctionToolType || toolType == xaiCustomToolType {
		if rawParams := getField("parameters"); rawParams.Exists() {
			inlinedParams := util.InlineLocalRefs(rawParams.Raw)
			if inlinedParams != rawParams.Raw {
				td := ensureToolDoc()
				if errSet := td.SetRawBytes("parameters", []byte(inlinedParams)); errSet == nil {
					if td.Get("parameters.$defs").Exists() {
						_ = td.Delete("parameters.$defs")
					}
					if td.Get("parameters.definitions").Exists() {
						_ = td.Delete("parameters.definitions")
					}
					changed = true
				}
			}
		}
		schemaChanged, ok := normalizeXAIObjectRootUnionBranchTypesDoc(ensureToolDoc())
		if !ok {
			return nil, false, false
		}
		if schemaChanged {
			changed = true
			log.Debugf("xai: added object types to root union branches for tool %s.%s", namespaceName, tool.Get("name").String())
		}
	}
	if toolType == xaiCustomToolType {
		if errSet := ensureToolDoc().Set("type", xaiFunctionToolType); errSet != nil {
			return nil, false, false
		}
		toolType = xaiFunctionToolType
		changed = true
	}
	if toolType == xaiWebSearchToolType {
		removed, ok := stripUnsupportedXAIWebSearchFieldsDoc(ensureToolDoc())
		if !ok {
			return nil, false, false
		}
		if len(removed) > 0 {
			changed = true
			log.Debugf("xai: removed unsupported web_search fields %v", removed)
		}
	}
	if toolType == xaiFunctionToolType && !getField("parameters").Exists() {
		if errSet := ensureToolDoc().SetRawBytes("parameters", []byte(`{"type":"object","properties":{}}`)); errSet != nil {
			return nil, false, false
		}
		changed = true
	}
	// Simplify the Codex Desktop automation schema and root unions that xAI
	// rejects because function parameters must resolve exclusively to objects.
	if toolType == xaiFunctionToolType && xaiFunctionParametersNeedSimplificationFields(tool.Get("type").String(), getField("name").String(), getField("parameters"), namespaceName) {
		td := ensureToolDoc()
		if errSet := td.SetRawBytes("parameters", []byte(xaiSafeFunctionParameters)); errSet != nil {
			return nil, false, false
		}
		if strict := tool.Get("strict"); strict.Exists() && strict.Bool() {
			if errSet := td.Set("strict", false); errSet != nil {
				return nil, false, false
			}
		}
		changed = true
		log.Debugf("xai: simplified parameters for tool %s.%s to avoid upstream schema rejection or hang", namespaceName, tool.Get("name").String())
	}
	if toolType == xaiFunctionToolType && strings.TrimSpace(namespaceName) != "" {
		qualifiedName := qualifyXAINamespaceToolName(namespaceName, tool.Get("name").String())
		if qualifiedName == "" {
			return nil, false, false
		}
		if errSet := ensureToolDoc().Set("name", qualifiedName); errSet != nil {
			return nil, false, false
		}
		changed = true
	}
	if changed && toolDoc != nil {
		return toolDoc.Bytes(), true, true
	}
	return raw, false, true
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
	return collectXAINamespaceToolRefsWithFoldResults(ojson.GetBytesNoCopy(body, "tools"), ojson.GetBytesNoCopy(body, "input"), shouldFold)
}

func collectXAINamespaceToolRefsWithFoldDoc(doc *ojson.Doc, shouldFold bool) map[string]xaiNamespaceToolRef {
	if doc == nil {
		return make(map[string]xaiNamespaceToolRef)
	}
	return collectXAINamespaceToolRefsWithFoldResults(doc.Get("tools"), doc.Get("input"), shouldFold)
}

func collectXAINamespaceToolRefsWithFoldResults(toolsResult, inputResult ojson.Result, shouldFold bool) map[string]xaiNamespaceToolRef {
	refs := make(map[string]xaiNamespaceToolRef)
	claimed := make(map[string]struct{})
	collect := func(tools ojson.Result) {
		if !tools.Exists() || !tools.IsArray() {
			return
		}
		for _, tool := range tools.Array() {
			toolType := strings.TrimSpace(tool.Get("type").String())
			if toolType == xaiFunctionToolType || toolType == xaiCustomToolType {
				if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
					claimed[name] = struct{}{}
					if shouldFold {
						if existing, ok := refs[name]; ok && !existing.isDispatcher {
							delete(refs, name)
						}
					}
				}
				continue
			}
			if toolType != xaiNamespaceToolType {
				continue
			}
			namespaceName := strings.TrimSpace(tool.Get("name").String())
			if namespaceName == "" {
				continue
			}
			if shouldFold {
				if _, exists := claimed[namespaceName]; !exists {
					claimed[namespaceName] = struct{}{}
					refs[namespaceName] = xaiNamespaceToolRef{
						namespace:    namespaceName,
						name:         "",
						isDispatcher: true,
						children:     make(map[string]struct{}),
					}
				}
			}
			for _, nestedTool := range tool.Get("tools").Array() {
				if nestedTool.Get("type").String() == xaiToolSearchType {
					continue
				}
				toolName := strings.TrimSpace(nestedTool.Get("name").String())
				qualifiedName := qualifyXAINamespaceToolName(namespaceName, toolName)
				if qualifiedName == "" {
					continue
				}
				if shouldFold {
					if dispRef, ok := refs[namespaceName]; ok && dispRef.isDispatcher && dispRef.children != nil {
						dispRef.children[toolName] = struct{}{}
					}
					if _, claimedByTool := claimed[qualifiedName]; !claimedByTool {
						if _, exists := refs[qualifiedName]; !exists {
							refs[qualifiedName] = xaiNamespaceToolRef{namespace: namespaceName, name: toolName, isDispatcher: false}
						}
					}
					continue
				}
				if _, exists := claimed[qualifiedName]; exists {
					continue
				}
				claimed[qualifiedName] = struct{}{}
				refs[qualifiedName] = xaiNamespaceToolRef{namespace: namespaceName, name: toolName, isDispatcher: false}
			}
		}
	}
	collect(toolsResult)
	if inputResult.Exists() && inputResult.IsArray() {
		for _, item := range inputResult.Array() {
			if item.Get("type").String() == "additional_tools" {
				collect(item.Get("tools"))
			}
		}
	}
	return refs
}

func normalizeXAIInputCustomToolCalls(body []byte) []byte {
	doc := ojson.NewDoc(body)
	if !normalizeXAIInputCustomToolCallsDoc(doc) {
		return body
	}
	return doc.Bytes()
}

func normalizeXAIInputCustomToolCallsDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	input := doc.Get("input")
	if !input.Exists() || !input.IsArray() {
		return false
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
			itemDoc := ojson.NewDoc([]byte(`{"type":"function_call"}`))
			_ = itemDoc.Set("call_id", callID)
			_ = itemDoc.Set("name", name)
			_ = itemDoc.Set("arguments", xaiCustomToolCallArguments(item.Get("input")))
			normalized = itemDoc.Bytes()
		case "custom_tool_call_output":
			callID := strings.TrimSpace(item.Get("call_id").String())
			if callID == "" {
				changed = true
				continue
			}
			itemDoc := ojson.NewDoc([]byte(`{"type":"function_call_output"}`))
			_ = itemDoc.Set("call_id", callID)
			_ = itemDoc.Set("output", xaiCustomToolCallOutput(item.Get("output")))
			normalized = itemDoc.Bytes()
		default:
			items = append(items, json.RawMessage(item.Raw))
			continue
		}
		items = append(items, json.RawMessage(normalized))
		changed = true
	}
	if !changed {
		return false
	}

	rawInput, errMarshal := json.Marshal(items)
	if errMarshal != nil {
		return false
	}
	return doc.SetRawBytes("input", rawInput) == nil
}

func xaiCustomToolCallArguments(input ojson.Result) string {
	if !input.Exists() {
		return "{}"
	}
	if input.Type == ojson.String {
		text := input.String()
		trimmed := strings.TrimSpace(text)
		if ojson.Valid(trimmed) {
			parsed := ojson.Parse(trimmed)
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

func xaiCustomToolCallOutput(output ojson.Result) string {
	if !output.Exists() {
		return ""
	}
	if output.Type == ojson.String {
		return output.String()
	}
	return output.Raw
}
