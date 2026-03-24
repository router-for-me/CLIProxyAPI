package executor

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const codexPreparedRequestCacheMetadataKey = "codex_prepared_request_cache"
const codexPreparedRequestCacheMinPayloadBytes = 8 << 10

type codexPreparedRequestPlanMode string

const (
	codexPreparedRequestPlanExecute       codexPreparedRequestPlanMode = "execute"
	codexPreparedRequestPlanExecuteStream codexPreparedRequestPlanMode = "execute_stream"
	codexPreparedRequestPlanCompact       codexPreparedRequestPlanMode = "compact"
)

type codexPreparedRequestPlanKey struct {
	mode           codexPreparedRequestPlanMode
	model          string
	requestedModel string
	sourceFormat   string
}

type codexPreparedRequestPlan struct {
	body           []byte
	conversationID string
}

type codexPreparedRequestCache struct {
	mu      sync.Mutex
	entries map[codexPreparedRequestPlanKey]codexPreparedRequestPlan
}

func existingCodexPreparedRequestCache(meta map[string]any) *codexPreparedRequestCache {
	if len(meta) == 0 {
		return nil
	}
	if cache, ok := meta[codexPreparedRequestCacheMetadataKey].(*codexPreparedRequestCache); ok && cache != nil {
		return cache
	}
	return nil
}

func ensureCodexPreparedRequestCache(meta map[string]any) *codexPreparedRequestCache {
	if cache := existingCodexPreparedRequestCache(meta); cache != nil {
		return cache
	}
	cache := &codexPreparedRequestCache{
		entries: make(map[codexPreparedRequestPlanKey]codexPreparedRequestPlan),
	}
	meta[codexPreparedRequestCacheMetadataKey] = cache
	return cache
}

func (c *codexPreparedRequestCache) getOrBuild(key codexPreparedRequestPlanKey, build func() (codexPreparedRequestPlan, error)) (codexPreparedRequestPlan, error) {
	if c == nil {
		return build()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if plan, ok := c.entries[key]; ok {
		return plan, nil
	}
	plan, err := build()
	if err != nil {
		return codexPreparedRequestPlan{}, err
	}
	c.entries[key] = plan
	return plan, nil
}

func (e *CodexExecutor) prepareCodexRequestPlan(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, mode codexPreparedRequestPlanMode) (codexPreparedRequestPlan, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	requestedModel := payloadRequestedModel(opts, req.Model)
	key := codexPreparedRequestPlanKey{
		mode:           mode,
		model:          baseModel,
		requestedModel: requestedModel,
		sourceFormat:   opts.SourceFormat.String(),
	}

	cache := existingCodexPreparedRequestCache(opts.Metadata)
	if cache == nil && !shouldCacheCodexPreparedRequestPlan(req, opts) {
		return e.buildCodexRequestPlan(ctx, req, opts, mode, baseModel, requestedModel)
	}
	cache = ensureCodexPreparedRequestCache(opts.Metadata)
	return cache.getOrBuild(key, func() (codexPreparedRequestPlan, error) {
		return e.buildCodexRequestPlan(ctx, req, opts, mode, baseModel, requestedModel)
	})
}

func (e *CodexExecutor) buildCodexRequestPlan(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, mode codexPreparedRequestPlanMode, baseModel, requestedModel string) (codexPreparedRequestPlan, error) {
	from := opts.SourceFormat
	to := codexPreparedRequestTargetFormat(mode)
	originalPayload := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayload = opts.OriginalRequest
	}
	needOriginal := payloadConfigNeedsOriginal(e.cfg, baseModel, to.String(), requestedModel)
	body, originalTranslated := translateRequestPairSelective(from, to, baseModel, req.Payload, originalPayload, mode == codexPreparedRequestPlanExecuteStream, needOriginal)

	var err error
	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return codexPreparedRequestPlan{}, err
	}

	body = applyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", body, originalTranslated, requestedModel)
	body = normalizeCodexPreparedBody(body, mode, baseModel)

	conversationID := codexPromptCacheID(ctx, from, req)
	if conversationID != "" {
		body = setJSONStringFieldIfNeeded(body, "prompt_cache_key", conversationID)
	}

	return codexPreparedRequestPlan{
		body:           body,
		conversationID: conversationID,
	}, nil
}

func codexPreparedRequestTargetFormat(mode codexPreparedRequestPlanMode) sdktranslator.Format {
	if mode == codexPreparedRequestPlanCompact {
		return sdktranslator.FromString("openai-response")
	}
	return sdktranslator.FromString("codex")
}

func normalizeCodexPreparedBody(body []byte, mode codexPreparedRequestPlanMode, baseModel string) []byte {
	state, ok := inspectCodexPreparedBody(body, baseModel)
	if !ok {
		return normalizeCodexPreparedBodyFallback(body, mode, baseModel)
	}
	switch mode {
	case codexPreparedRequestPlanExecute:
		if !state.modelMatches {
			body = setCodexJSONStringField(body, "model", baseModel)
		}
		if !state.streamTrue {
			body = setCodexJSONBoolField(body, "stream", true)
		}
		if state.hasPreviousResponseID {
			body = deleteCodexJSONField(body, "previous_response_id")
		}
		if state.hasPromptCacheRetention {
			body = deleteCodexJSONField(body, "prompt_cache_retention")
		}
		if state.hasSafetyIdentifier {
			body = deleteCodexJSONField(body, "safety_identifier")
		}
		if !state.hasInstructions {
			body = setCodexJSONStringField(body, "instructions", "")
		}
	case codexPreparedRequestPlanExecuteStream:
		if state.hasPreviousResponseID {
			body = deleteCodexJSONField(body, "previous_response_id")
		}
		if state.hasPromptCacheRetention {
			body = deleteCodexJSONField(body, "prompt_cache_retention")
		}
		if state.hasSafetyIdentifier {
			body = deleteCodexJSONField(body, "safety_identifier")
		}
		if !state.modelMatches {
			body = setCodexJSONStringField(body, "model", baseModel)
		}
		if !state.streamTrue {
			body = setCodexJSONBoolField(body, "stream", true)
		}
		if !state.hasInstructions {
			body = setCodexJSONStringField(body, "instructions", "")
		}
	case codexPreparedRequestPlanCompact:
		if !state.modelMatches {
			body = setCodexJSONStringField(body, "model", baseModel)
		}
		if state.hasStream {
			body = deleteCodexJSONField(body, "stream")
		}
		if !state.hasInstructions {
			body = setCodexJSONStringField(body, "instructions", "")
		}
	}
	return body
}

type codexPreparedBodyState struct {
	modelMatches            bool
	streamTrue              bool
	hasStream               bool
	hasPreviousResponseID   bool
	hasPromptCacheRetention bool
	hasSafetyIdentifier     bool
	hasInstructions         bool
}

func inspectCodexPreparedBody(body []byte, baseModel string) (codexPreparedBodyState, bool) {
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return codexPreparedBodyState{}, false
	}

	var state codexPreparedBodyState
	root.ForEach(func(key, value gjson.Result) bool {
		switch key.Str {
		case "model":
			state.modelMatches = value.Type == gjson.String && value.Str == baseModel
		case "stream":
			state.hasStream = true
			state.streamTrue = value.Type == gjson.True
		case "previous_response_id":
			state.hasPreviousResponseID = true
		case "prompt_cache_retention":
			state.hasPromptCacheRetention = true
		case "safety_identifier":
			state.hasSafetyIdentifier = true
		case "instructions":
			state.hasInstructions = true
		}
		return true
	})
	return state, true
}

func normalizeCodexPreparedBodyFallback(body []byte, mode codexPreparedRequestPlanMode, baseModel string) []byte {
	switch mode {
	case codexPreparedRequestPlanExecute:
		body = setJSONStringFieldIfNeeded(body, "model", baseModel)
		body = setJSONBoolFieldIfNeeded(body, "stream", true)
		body = deleteJSONFieldIfExists(body, "previous_response_id")
		body = deleteJSONFieldIfExists(body, "prompt_cache_retention")
		body = deleteJSONFieldIfExists(body, "safety_identifier")
		body = ensureJSONStringField(body, "instructions", "")
	case codexPreparedRequestPlanExecuteStream:
		body = deleteJSONFieldIfExists(body, "previous_response_id")
		body = deleteJSONFieldIfExists(body, "prompt_cache_retention")
		body = deleteJSONFieldIfExists(body, "safety_identifier")
		body = setJSONStringFieldIfNeeded(body, "model", baseModel)
		body = ensureJSONStringField(body, "instructions", "")
	case codexPreparedRequestPlanCompact:
		body = setJSONStringFieldIfNeeded(body, "model", baseModel)
		body = deleteJSONFieldIfExists(body, "stream")
		body = ensureJSONStringField(body, "instructions", "")
	}
	return body
}

func setJSONStringFieldIfNeeded(body []byte, path, value string) []byte {
	result := gjson.GetBytes(body, path)
	if result.Exists() && result.Type == gjson.String && result.String() == value {
		return body
	}
	updated, err := sjson.SetBytes(body, path, value)
	if err != nil {
		return body
	}
	return updated
}

func setCodexJSONStringField(body []byte, path, value string) []byte {
	updated, err := sjson.SetBytes(body, path, value)
	if err != nil {
		return body
	}
	return updated
}

func ensureJSONStringField(body []byte, path, value string) []byte {
	if gjson.GetBytes(body, path).Exists() {
		return body
	}
	updated, err := sjson.SetBytes(body, path, value)
	if err != nil {
		return body
	}
	return updated
}

func setCodexJSONBoolField(body []byte, path string, value bool) []byte {
	updated, err := sjson.SetBytes(body, path, value)
	if err != nil {
		return body
	}
	return updated
}

func setJSONBoolFieldIfNeeded(body []byte, path string, value bool) []byte {
	result := gjson.GetBytes(body, path)
	if result.Exists() {
		if value && result.Type == gjson.True {
			return body
		}
		if !value && result.Type == gjson.False {
			return body
		}
	}
	updated, err := sjson.SetBytes(body, path, value)
	if err != nil {
		return body
	}
	return updated
}

func deleteCodexJSONField(body []byte, path string) []byte {
	updated, err := sjson.DeleteBytes(body, path)
	if err != nil {
		return body
	}
	return updated
}

func deleteJSONFieldIfExists(body []byte, path string) []byte {
	if !gjson.GetBytes(body, path).Exists() {
		return body
	}
	updated, err := sjson.DeleteBytes(body, path)
	if err != nil {
		return body
	}
	return updated
}

func codexPromptCacheID(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request) string {
	if from == "claude" {
		userIDResult := gjson.GetBytes(req.Payload, "metadata.user_id")
		if userIDResult.Exists() {
			key := req.Model + "-" + userIDResult.String()
			if cache, ok := getCodexCache(key); ok {
				return cache.ID
			}
			cache := codexCache{
				ID:     uuid.New().String(),
				Expire: time.Now().Add(1 * time.Hour),
			}
			setCodexCache(key, cache)
			return cache.ID
		}
		return ""
	}
	if from == "openai-response" {
		promptCacheKey := gjson.GetBytes(req.Payload, "prompt_cache_key")
		if promptCacheKey.Exists() {
			return promptCacheKey.String()
		}
		return ""
	}
	if from == "openai" {
		if apiKey := strings.TrimSpace(apiKeyFromContext(ctx)); apiKey != "" {
			return uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:"+apiKey)).String()
		}
	}
	return ""
}

func newCodexHTTPRequest(ctx context.Context, url string, body []byte, conversationID string) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if conversationID != "" {
		httpReq.Header.Set("Conversation_id", conversationID)
		httpReq.Header.Set("Session_id", conversationID)
	}
	return httpReq, nil
}

func codexResponseTranslatorNeedsRequestPayloads(from sdktranslator.Format) bool {
	return from != sdktranslator.FromString("openai-response")
}

func shouldCacheCodexPreparedRequestPlan(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) bool {
	if len(opts.Metadata) == 0 {
		return false
	}
	size := len(req.Payload)
	if len(opts.OriginalRequest) > size {
		size = len(opts.OriginalRequest)
	}
	return size >= codexPreparedRequestCacheMinPayloadBytes
}
