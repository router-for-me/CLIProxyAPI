package executor

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// bedrockDefaultBaseURL is the standard Bedrock runtime endpoint template.
	// Region is substituted at request time.
	bedrockDefaultBaseURL = "https://bedrock-runtime.{region}.amazonaws.com"
	// bedrockMaxEventStreamMessageBytes limits a single EventStream frame size to avoid
	// unbounded allocations on malformed or hostile upstream payloads.
	bedrockMaxEventStreamMessageBytes = 8 * 1024 * 1024
)

// BedrockExecutor is a stateless executor for AWS Bedrock Converse API.
// It targets the POST /model/{modelId}/converse and /model/{modelId}/converse-stream
// endpoints, injecting the API key as a Bearer token.
type BedrockExecutor struct {
	cfg *config.Config
}

// NewBedrockExecutor creates an executor for AWS Bedrock.
func NewBedrockExecutor(cfg *config.Config) *BedrockExecutor {
	return &BedrockExecutor{cfg: cfg}
}

// Identifier implements cliproxyexecutor.Executor.
func (e *BedrockExecutor) Identifier() string { return constant.AWSBedrock }

// PrepareRequest injects the Bedrock API key as a Bearer token.
func (e *BedrockExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	_, apiKey, _ := e.resolveBedrockConfig(auth)
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	return nil
}

// HttpRequest sets auth headers and executes the request.
func (e *BedrockExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("bedrock executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute performs a non-streaming Bedrock Converse request.
func (e *BedrockExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	modelID := req.Model
	provider := constant.AWSBedrock
	if auth != nil && strings.TrimSpace(auth.Provider) != "" {
		provider = auth.Provider
	}
	var modelInfo *registry.ModelInfo
	if info := registry.GetGlobalRegistry().GetModelInfo(req.Model, provider); info != nil {
		modelInfo = info
		if info.Name != "" {
			modelID = info.Name
		}
	}
	baseModel := thinking.ParseSuffix(modelID).ModelName
	effectiveModelInfo := resolveBedrockModelInfo(baseModel, modelInfo)
	maxCompletionTokens := resolveBedrockMaxCompletionTokens(baseModel, effectiveModelInfo)
	payloadForTranslate := capBedrockRequestMaxTokens(req.Payload, maxCompletionTokens)

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey, _ := e.resolveBedrockConfig(auth)

	from := opts.SourceFormat
	to := sdktranslator.FromString(constant.BedrockConverse)

	originalPayloadSource := payloadForTranslate
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = capBedrockRequestMaxTokens(opts.OriginalRequest, maxCompletionTokens)
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayloadSource, false)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, payloadForTranslate, false)
	originalTranslated = stripBedrockUnsupportedToolConfig(originalTranslated, effectiveModelInfo)
	translated = stripBedrockUnsupportedToolConfig(translated, effectiveModelInfo)

	endpoint := buildBedrockEndpoint(baseURL, baseModel, false)
	httpResp, _, err := e.sendBedrockRequest(ctx, auth, endpoint, apiKey, translated, false)
	if err != nil {
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("bedrock executor: close response body error: %v", errClose)
		}
	}()
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)
	reporter.EnsurePublished(ctx)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, originalTranslated, translated, body, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

// ExecuteStream performs a streaming Bedrock Converse request via converse-stream.
func (e *BedrockExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	modelID := req.Model
	provider := constant.AWSBedrock
	if auth != nil && strings.TrimSpace(auth.Provider) != "" {
		provider = auth.Provider
	}
	var modelInfo *registry.ModelInfo
	if info := registry.GetGlobalRegistry().GetModelInfo(req.Model, provider); info != nil {
		modelInfo = info
		if info.Name != "" {
			modelID = info.Name
		}
	}
	baseModel := thinking.ParseSuffix(modelID).ModelName
	effectiveModelInfo := resolveBedrockModelInfo(baseModel, modelInfo)
	maxCompletionTokens := resolveBedrockMaxCompletionTokens(baseModel, effectiveModelInfo)
	payloadForTranslate := capBedrockRequestMaxTokens(req.Payload, maxCompletionTokens)

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey, _ := e.resolveBedrockConfig(auth)

	from := opts.SourceFormat
	to := sdktranslator.FromString(constant.BedrockConverse)

	originalPayloadSource := payloadForTranslate
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = capBedrockRequestMaxTokens(opts.OriginalRequest, maxCompletionTokens)
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayloadSource, true)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, payloadForTranslate, true)
	originalTranslated = stripBedrockUnsupportedToolConfig(originalTranslated, effectiveModelInfo)
	translated = stripBedrockUnsupportedToolConfig(translated, effectiveModelInfo)

	endpoint := buildBedrockEndpoint(baseURL, baseModel, true)
	httpResp, _, err := e.sendBedrockRequest(ctx, auth, endpoint, apiKey, translated, true)
	if err != nil {
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("bedrock executor: close response body error: %v", errClose)
			}
		}()
		var param any
		for {
			payload, err := readBedrockEvent(httpResp.Body)
			if err != nil {
				if err == io.EOF {
					break
				}
				helps.RecordAPIResponseError(ctx, e.cfg, err)
				reporter.PublishFailure(ctx)
				out <- cliproxyexecutor.StreamChunk{Err: err}
				break
			}

			helps.AppendAPIResponseChunk(ctx, e.cfg, payload)
			if len(bytes.TrimSpace(payload)) == 0 {
				continue
			}

			// payload already points to a per-event buffer allocated by readBedrockEvent.
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, originalTranslated, translated, payload, &param)
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}
			}
		}
		reporter.EnsurePublished(ctx)
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *BedrockExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opt cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	// Bedrock token counting could be implemented via specialized APIs,
	// but for now we return 0/placeholder or use a local tokenizer.
	return cliproxyexecutor.Response{}, nil
}

// Refresh is a no-op for API-key based providers.
func (e *BedrockExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("bedrock executor: refresh called")
	_ = ctx
	return auth, nil
}

// resolveBedrockConfig extracts baseURL, apiKey and region from the auth entry.
func (e *BedrockExecutor) resolveBedrockConfig(auth *cliproxyauth.Auth) (baseURL, apiKey, region string) {
	if auth == nil || auth.Attributes == nil {
		return
	}
	apiKey = strings.TrimSpace(auth.Attributes["api_key"])
	region = strings.TrimSpace(auth.Attributes["region"])
	baseURL = strings.TrimSpace(auth.Attributes["base_url"])

	// Build default base URL if not set explicitly
	if baseURL == "" {
		if region == "" {
			region = "us-east-1"
		}
		baseURL = strings.ReplaceAll(bedrockDefaultBaseURL, "{region}", region)
	}
	return
}

// buildBedrockEndpoint constructs the Bedrock endpoint for a model ID.
// Streaming uses /converse-stream, non-streaming uses /converse.
func buildBedrockEndpoint(baseURL, modelID string, stream bool) string {
	base := strings.TrimSuffix(baseURL, "/")
	suffix := "/converse"
	if stream {
		suffix = "/converse-stream"
	}
	return base + "/model/" + modelID + suffix
}

func (e *BedrockExecutor) sendBedrockRequest(ctx context.Context, auth *cliproxyauth.Auth, endpoint, apiKey string, body []byte, stream bool) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("User-Agent", "cli-proxy-bedrock")
	if stream {
		req.Header.Set("Accept", "application/vnd.amazon.eventstream")
		req.Header.Set("Cache-Control", "no-cache")
	}

	authID, authLabel, authType, authValue := bedrockAuthMetadata(auth)
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       endpoint,
		Method:    http.MethodPost,
		Headers:   req.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	resp, err := httpClient.Do(req)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil, nil
	}

	b, _ := io.ReadAll(resp.Body)
	helps.AppendAPIResponseChunk(ctx, e.cfg, b)
	if stream {
		helps.LogWithRequestID(ctx).Debugf("bedrock stream request error, status: %d, body: %s", resp.StatusCode, helps.SummarizeErrorBody(resp.Header.Get("Content-Type"), b))
	} else {
		helps.LogWithRequestID(ctx).Debugf("bedrock request error, status: %d, body: %s", resp.StatusCode, helps.SummarizeErrorBody(resp.Header.Get("Content-Type"), b))
	}
	if errClose := resp.Body.Close(); errClose != nil {
		log.Errorf("bedrock executor: close response body error: %v", errClose)
	}
	return nil, b, statusErr{code: resp.StatusCode, msg: string(b)}
}

func bedrockAuthMetadata(auth *cliproxyauth.Auth) (id, label, authType, authValue string) {
	if auth == nil {
		return "", "", "", ""
	}
	id = auth.ID
	label = auth.Label
	authType, authValue = auth.AccountInfo()
	return id, label, authType, authValue
}

func capBedrockRequestMaxTokens(payload []byte, maxCompletionTokens int) []byte {
	effectiveMax := effectiveBedrockRequestMaxTokens(maxCompletionTokens)
	if effectiveMax <= 0 || len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}
	maxTokens := gjson.GetBytes(payload, "max_tokens")
	if !maxTokens.Exists() {
		return payload
	}
	requested := maxTokens.Int()
	if requested <= 0 || requested <= int64(effectiveMax) {
		return payload
	}
	out, err := sjson.SetBytes(payload, "max_tokens", effectiveMax)
	if err != nil {
		return payload
	}
	return out
}

func effectiveBedrockRequestMaxTokens(maxCompletionTokens int) int {
	if maxCompletionTokens <= 0 {
		return 0
	}
	// Some Bedrock models validate maxTokens as strictly lower than the published limit.
	// Keep one-token headroom to avoid boundary validation errors.
	if maxCompletionTokens > 1 {
		return maxCompletionTokens - 1
	}
	return maxCompletionTokens
}

func resolveBedrockMaxCompletionTokens(resolvedModelID string, modelInfo *registry.ModelInfo) int {
	if info := resolveBedrockModelInfo(resolvedModelID, modelInfo); info != nil && info.MaxCompletionTokens > 0 {
		return info.MaxCompletionTokens
	}
	return 0
}

func resolveBedrockModelInfo(resolvedModelID string, modelInfo *registry.ModelInfo) *registry.ModelInfo {
	if modelInfo != nil && modelInfo.MaxCompletionTokens > 0 && len(modelInfo.SupportedParameters) > 0 {
		return modelInfo
	}

	lookupIDs := collectBedrockModelLookupIDs(resolvedModelID)
	if modelInfo != nil {
		lookupIDs = append(lookupIDs, collectBedrockModelLookupIDs(modelInfo.Name)...)
		lookupIDs = append(lookupIDs, collectBedrockModelLookupIDs(modelInfo.ID)...)
	}

	seen := make(map[string]struct{}, len(lookupIDs))
	var candidate *registry.ModelInfo
	for _, id := range lookupIDs {
		key := strings.ToLower(strings.TrimSpace(id))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if info := registry.LookupModelInfo(id, constant.AWSBedrock); info != nil {
			if info.MaxCompletionTokens > 0 || len(info.SupportedParameters) > 0 {
				return info
			}
			if candidate == nil {
				candidate = info
			}
		}
	}
	if candidate != nil {
		return candidate
	}
	return modelInfo
}

func collectBedrockModelLookupIDs(modelID string) []string {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil
	}
	ids := []string{modelID}
	if canonical := stripBedrockGeoPrefix(modelID); canonical != "" && !strings.EqualFold(canonical, modelID) {
		ids = append(ids, canonical)
	}
	return ids
}

func stripBedrockUnsupportedToolConfig(payload []byte, modelInfo *registry.ModelInfo) []byte {
	if len(payload) == 0 || modelInfo == nil || !gjson.ValidBytes(payload) {
		return payload
	}
	if modelSupportsBedrockTools(modelInfo) {
		return payload
	}
	if !gjson.GetBytes(payload, "toolConfig").Exists() {
		return payload
	}
	out, err := sjson.DeleteBytes(payload, "toolConfig")
	if err != nil {
		return payload
	}
	return out
}

func modelSupportsBedrockTools(modelInfo *registry.ModelInfo) bool {
	if modelInfo == nil || len(modelInfo.SupportedParameters) == 0 {
		// Keep backward compatibility for models without explicit capability declaration.
		return true
	}
	for _, parameter := range modelInfo.SupportedParameters {
		if strings.EqualFold(strings.TrimSpace(parameter), "tools") {
			return true
		}
	}
	return false
}

func stripBedrockGeoPrefix(modelID string) string {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return ""
	}
	parts := strings.SplitN(modelID, ".", 2)
	if len(parts) != 2 {
		return modelID
	}
	prefix := strings.TrimSpace(parts[0])
	rest := strings.TrimSpace(parts[1])
	// Bedrock model IDs may include a short geography qualifier (e.g. "us.", "eu.", "apac.").
	// Keep this heuristic generic and avoid model-specific hardcoded rewrites.
	if prefix == "" || rest == "" || len(prefix) > 5 {
		return modelID
	}
	if !strings.Contains(rest, ".") && !strings.Contains(rest, ":") {
		return modelID
	}
	return rest
}

// readBedrockEvent parses one binary AWS EventStream message from the reader
// and returns its payload, wrapped in a JSON object identifying the event type
// (e.g. {"contentBlockDelta": {...}}).
func readBedrockEvent(r io.Reader) ([]byte, error) {
	// 12-byte prelude
	prelude := make([]byte, 12)
	if _, err := io.ReadFull(r, prelude); err != nil {
		return nil, err
	}

	// Verify prelude CRC
	preludeCRC := binary.BigEndian.Uint32(prelude[8:12])
	calculatedPreludeCRC := crc32.ChecksumIEEE(prelude[0:8])
	if calculatedPreludeCRC != preludeCRC {
		log.Errorf("bedrock eventstream: prelude CRC mismatch: got %x, want %x", calculatedPreludeCRC, preludeCRC)
		return nil, fmt.Errorf("bedrock eventstream: prelude CRC mismatch")
	}

	totalLen := binary.BigEndian.Uint32(prelude[0:4])
	headerLen := binary.BigEndian.Uint32(prelude[4:8])
	log.Debugf("bedrock eventstream: msg totalLen=%d, headerLen=%d", totalLen, headerLen)

	if totalLen < 16 {
		return nil, fmt.Errorf("bedrock eventstream: total length too small")
	}
	if totalLen > bedrockMaxEventStreamMessageBytes {
		return nil, fmt.Errorf("bedrock eventstream: total length too large: %d", totalLen)
	}

	// Read headers + payload + 4 bytes message CRC
	remainingLen := totalLen - 12
	if remainingLen < 4 {
		return nil, fmt.Errorf("bedrock eventstream: malformed remaining length")
	}
	if headerLen > remainingLen-4 {
		return nil, fmt.Errorf("bedrock eventstream: invalid header length")
	}
	msg := make([]byte, remainingLen)
	if _, err := io.ReadFull(r, msg); err != nil {
		return nil, err
	}

	// Verify message CRC
	fullMsg := make([]byte, len(prelude)+len(msg))
	copy(fullMsg, prelude)
	copy(fullMsg[len(prelude):], msg)
	msgCRC := binary.BigEndian.Uint32(msg[int(remainingLen)-4:])
	calculatedMsgCRC := crc32.ChecksumIEEE(fullMsg[0 : totalLen-4])
	if calculatedMsgCRC != msgCRC {
		log.Errorf("bedrock eventstream: message CRC mismatch: got %x, want %x", calculatedMsgCRC, msgCRC)
		return nil, fmt.Errorf("bedrock eventstream: message CRC mismatch")
	}

	// Internal headers and payload
	headersRaw := msg[0:headerLen]
	payload := msg[headerLen : remainingLen-4]

	// Parse headers to find event type
	eventType := ""
	ptr := 0
	for ptr < int(headerLen) {
		if ptr >= len(headersRaw) {
			return nil, fmt.Errorf("bedrock eventstream: malformed header")
		}
		kLen := int(headersRaw[ptr])
		ptr++
		if kLen <= 0 || ptr+kLen > len(headersRaw) {
			return nil, fmt.Errorf("bedrock eventstream: malformed header key")
		}
		key := string(headersRaw[ptr : ptr+kLen])
		ptr += kLen
		if ptr >= len(headersRaw) {
			return nil, fmt.Errorf("bedrock eventstream: malformed header value type")
		}
		valType := headersRaw[ptr]
		ptr++

		switch valType {
		case 0, 1: // bool
			// No value bytes
		case 2: // byte
			ptr += 1
		case 3: // short
			ptr += 2
		case 4: // int
			ptr += 4
		case 5: // long
			ptr += 8
		case 6: // bytes
			if ptr+2 > len(headersRaw) {
				return nil, fmt.Errorf("bedrock eventstream: malformed bytes header length")
			}
			vLen := int(binary.BigEndian.Uint16(headersRaw[ptr : ptr+2]))
			ptr += 2 + vLen
		case 7: // string
			if ptr+2 > len(headersRaw) {
				return nil, fmt.Errorf("bedrock eventstream: malformed string header length")
			}
			vLen := int(binary.BigEndian.Uint16(headersRaw[ptr : ptr+2]))
			ptr += 2
			if ptr+vLen > len(headersRaw) {
				return nil, fmt.Errorf("bedrock eventstream: malformed string header value")
			}
			val := string(headersRaw[ptr : ptr+vLen])
			ptr += vLen
			if key == ":event-type" || key == ":exception-type" {
				eventType = val
			}
		case 8: // timestamp
			ptr += 8
		case 9: // uuid
			ptr += 16
		default:
			return nil, fmt.Errorf("bedrock eventstream: unsupported header value type: %d", valType)
		}
		if ptr > len(headersRaw) {
			return nil, fmt.Errorf("bedrock eventstream: malformed header value")
		}
	}

	// If we have an event type, wrap the payload JSON in a super-standardized way.
	// We want to provide:
	// 1. A "type" field for OpenAI translators.
	// 2. An event-specific wrapper key (e.g. "contentBlockDelta": {...}) for Claude translators.
	// 3. The original payload fields at the top level for existing field lookups.
	if eventType != "" && len(bytes.TrimSpace(payload)) > 0 {
		payloadTrimmed := bytes.TrimSpace(payload)
		if len(payloadTrimmed) > 0 && payloadTrimmed[0] == '{' && gjson.ValidBytes(payloadTrimmed) {
			withWrapper, err := sjson.SetRawBytes(payloadTrimmed, eventType, payloadTrimmed)
			if err == nil {
				withType, err := sjson.SetBytes(withWrapper, "type", eventType)
				if err == nil {
					return withType, nil
				}
			}
		}
	}

	return payload, nil
}
