package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cluster"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type clusterForwardFamily int
type clusterOperationKind int

const (
	clusterForwardUnknown clusterForwardFamily = iota
	clusterForwardOpenAI
	clusterForwardOpenAIResponses
	clusterForwardClaude
	clusterForwardGemini
)

const (
	clusterOperationExecute clusterOperationKind = iota
	clusterOperationCountTokens
	clusterOperationStream
)

// ClusterExecutor forwards requests directly to a peer node's public API.
type ClusterExecutor struct {
	provider string
	cfg      *config.Config
	cluster  *cluster.Service
}

func NewClusterExecutor(provider string, cfg *config.Config, clusterService *cluster.Service) *ClusterExecutor {
	return &ClusterExecutor{
		provider: strings.TrimSpace(provider),
		cfg:      cfg,
		cluster:  clusterService,
	}
}

func (e *ClusterExecutor) Identifier() string {
	if e == nil {
		return ""
	}
	return e.provider
}

func (e *ClusterExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.executeHTTP(ctx, auth, req, opts, clusterOperationExecute)
}

func (e *ClusterExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.executeHTTP(ctx, auth, req, opts, clusterOperationCountTokens)
}

func (e *ClusterExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	binding, outbound, family, err := e.prepareOutboundRequest(auth, req, opts, clusterOperationStream)
	if err != nil {
		return nil, err
	}

	resp, _, err := e.cluster.DoPublicRequest(ctx, binding, outbound.method, outbound.path, outbound.rawQuery, outbound.headers, outbound.body, authInjectorForFamily(family))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errResp := readErrorResponse(resp)
		return nil, errResp
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			_ = resp.Body.Close()
		}()

		switch family {
		case clusterForwardOpenAI:
			streamOpenAIChunks(resp.Body, outbound.path == "/v1/completions", out)
		case clusterForwardGemini:
			streamGeminiChunks(resp.Body, out)
		case clusterForwardClaude, clusterForwardOpenAIResponses:
			streamSSELines(resp.Body, out)
		default:
			streamSSELines(resp.Body, out)
		}
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: resp.Header.Clone(),
		Chunks:  out,
	}, nil
}

func (e *ClusterExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

func (e *ClusterExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	binding, outbound, family, err := e.prepareHTTPOutboundRequest(auth, req)
	if err != nil {
		return nil, err
	}
	resp, _, err := e.cluster.DoPublicRequest(ctx, binding, outbound.method, outbound.path, outbound.rawQuery, outbound.headers, outbound.body, authInjectorForFamily(family))
	return resp, err
}

type outboundForwardRequest struct {
	method   string
	path     string
	rawQuery string
	headers  http.Header
	body     []byte
}

func (e *ClusterExecutor) executeHTTP(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, operation clusterOperationKind) (cliproxyexecutor.Response, error) {
	binding, outbound, family, err := e.prepareOutboundRequest(auth, req, opts, operation)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	resp, _, err := e.cluster.DoPublicRequest(ctx, binding, outbound.method, outbound.path, outbound.rawQuery, outbound.headers, outbound.body, authInjectorForFamily(family))
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return cliproxyexecutor.Response{}, statusErrWithHeaders{
			statusErr: statusErr{code: resp.StatusCode, msg: string(body)},
			headers:   resp.Header.Clone(),
		}
	}
	if outbound.path == "/v1/completions" {
		body = convertCompletionsResponseToChat(body)
	}
	return cliproxyexecutor.Response{
		Payload: body,
		Headers: resp.Header.Clone(),
	}, nil
}

func (e *ClusterExecutor) prepareOutboundRequest(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, operation clusterOperationKind) (*cluster.PeerBinding, outboundForwardRequest, clusterForwardFamily, error) {
	if e == nil || e.cluster == nil {
		return nil, outboundForwardRequest{}, clusterForwardUnknown, statusErr{code: http.StatusConflict, msg: "cluster executor is not configured"}
	}
	if metadataBool(opts.Metadata, cliproxyexecutor.ClusterForwardedMetadataKey) {
		return nil, outboundForwardRequest{}, clusterForwardUnknown, statusErr{code: http.StatusBadGateway, msg: "cluster-forwarded request cannot be forwarded again"}
	}
	if auth == nil {
		return nil, outboundForwardRequest{}, clusterForwardUnknown, statusErr{code: http.StatusUnauthorized, msg: "missing cluster peer auth"}
	}
	binding, ok := cluster.BindingFromRuntime(auth.Runtime)
	if !ok || binding == nil {
		return nil, outboundForwardRequest{}, clusterForwardUnknown, statusErr{code: http.StatusUnauthorized, msg: "missing cluster peer runtime binding"}
	}

	method := metadataString(opts.Metadata, cliproxyexecutor.RequestMethodMetadataKey)
	if method == "" {
		method = http.MethodPost
	}
	path := metadataString(opts.Metadata, cliproxyexecutor.RequestPathMetadataKey)
	rawQuery := sanitizeRawQuery(metadataString(opts.Metadata, cliproxyexecutor.RequestRawQueryMetadataKey))
	if path == "" {
		path, rawQuery = inferFallbackRoute(req, opts, operation)
	}
	if path == "" {
		return nil, outboundForwardRequest{}, clusterForwardUnknown, statusErr{code: http.StatusNotImplemented, msg: "unsupported cluster route"}
	}
	if strings.EqualFold(method, http.MethodGet) && path == "/v1/responses" {
		return nil, outboundForwardRequest{}, clusterForwardUnknown, statusErr{code: http.StatusNotImplemented, msg: "cluster executor does not support websocket forwarding for GET /v1/responses"}
	}

	family := familyFromPath(path)
	if family == clusterForwardUnknown {
		return nil, outboundForwardRequest{}, clusterForwardUnknown, statusErr{code: http.StatusNotImplemented, msg: "unsupported cluster route"}
	}
	if family == clusterForwardGemini {
		path = rewriteGeminiPath(path, resolvePeerModel(binding, req.Model))
		if rawQuery == "" && opts.Alt != "" {
			rawQuery = url.Values{"alt": []string{opts.Alt}}.Encode()
		}
	}

	headers := metadataHeader(opts.Metadata, cliproxyexecutor.RequestHeadersMetadataKey)
	sanitizeOutboundHeaders(headers)
	headers.Set(cluster.HeaderHop, "1")
	if nodeID := e.localNodeID(); nodeID != "" {
		headers.Set(cluster.HeaderForwardedBy, nodeID)
	}
	if len(headers.Get("Content-Type")) == 0 {
		headers.Set("Content-Type", "application/json")
	}

	body := metadataBytes(opts.Metadata, cliproxyexecutor.RequestBodyOverrideMetadataKey)
	if len(body) == 0 {
		body = bytes.Clone(opts.OriginalRequest)
	}
	if len(body) == 0 {
		body = bytes.Clone(req.Payload)
	}
	if family != clusterForwardGemini {
		body = rewriteJSONModel(body, resolvePeerModel(binding, req.Model))
	}

	return binding, outboundForwardRequest{
		method:   method,
		path:     path,
		rawQuery: rawQuery,
		headers:  headers,
		body:     body,
	}, family, nil
}

func (e *ClusterExecutor) prepareHTTPOutboundRequest(auth *cliproxyauth.Auth, req *http.Request) (*cluster.PeerBinding, outboundForwardRequest, clusterForwardFamily, error) {
	if req == nil {
		return nil, outboundForwardRequest{}, clusterForwardUnknown, fmt.Errorf("cluster executor: request is nil")
	}
	if auth == nil {
		return nil, outboundForwardRequest{}, clusterForwardUnknown, fmt.Errorf("cluster executor: auth is nil")
	}
	binding, ok := cluster.BindingFromRuntime(auth.Runtime)
	if !ok || binding == nil {
		return nil, outboundForwardRequest{}, clusterForwardUnknown, fmt.Errorf("cluster executor: missing peer runtime binding")
	}

	var body []byte
	if req.Body != nil {
		readBody, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, outboundForwardRequest{}, clusterForwardUnknown, err
		}
		body = readBody
	}

	path := strings.TrimSpace(req.URL.Path)
	if path == "" {
		return nil, outboundForwardRequest{}, clusterForwardUnknown, statusErr{code: http.StatusNotImplemented, msg: "unsupported cluster route"}
	}
	if strings.EqualFold(req.Method, http.MethodGet) && path == "/v1/responses" {
		return nil, outboundForwardRequest{}, clusterForwardUnknown, statusErr{code: http.StatusNotImplemented, msg: "cluster executor does not support websocket forwarding for GET /v1/responses"}
	}

	family := familyFromPath(path)
	if family == clusterForwardUnknown {
		return nil, outboundForwardRequest{}, clusterForwardUnknown, statusErr{code: http.StatusNotImplemented, msg: "unsupported cluster route"}
	}

	if family == clusterForwardGemini {
		path = rewriteGeminiPath(path, resolvePeerModel(binding, requestedModelFromGeminiPath(path)))
	} else {
		body = rewriteJSONModel(body, resolvePeerModel(binding, requestedModelFromBody(body)))
	}

	headers := req.Header.Clone()
	sanitizeOutboundHeaders(headers)
	headers.Set(cluster.HeaderHop, "1")
	if nodeID := e.localNodeID(); nodeID != "" {
		headers.Set(cluster.HeaderForwardedBy, nodeID)
	}
	if len(headers.Get("Content-Type")) == 0 && len(body) > 0 {
		headers.Set("Content-Type", "application/json")
	}

	return binding, outboundForwardRequest{
		method:   req.Method,
		path:     path,
		rawQuery: sanitizeRawQuery(req.URL.RawQuery),
		headers:  headers,
		body:     body,
	}, family, nil
}

func familyFromPath(path string) clusterForwardFamily {
	path = strings.TrimSpace(path)
	switch {
	case path == "/v1/chat/completions", path == "/v1/completions":
		return clusterForwardOpenAI
	case path == "/v1/responses", path == "/v1/responses/compact":
		return clusterForwardOpenAIResponses
	case path == "/v1/messages", path == "/v1/messages/count_tokens":
		return clusterForwardClaude
	case strings.HasPrefix(path, "/v1beta/models/"):
		return clusterForwardGemini
	default:
		return clusterForwardUnknown
	}
}

func inferFallbackRoute(req cliproxyexecutor.Request, opts cliproxyexecutor.Options, operation clusterOperationKind) (path string, rawQuery string) {
	body := metadataBytes(opts.Metadata, cliproxyexecutor.RequestBodyOverrideMetadataKey)
	if len(body) == 0 {
		body = bytes.Clone(opts.OriginalRequest)
	}
	if len(body) == 0 {
		body = bytes.Clone(req.Payload)
	}

	switch opts.SourceFormat.String() {
	case "claude":
		if operation == clusterOperationCountTokens {
			return "/v1/messages/count_tokens", ""
		}
		return "/v1/messages", ""
	case "gemini":
		action := "generateContent"
		if operation == clusterOperationCountTokens {
			action = "countTokens"
		}
		foundAction := false
		if req.Metadata != nil {
			if rawAction, _ := req.Metadata["action"].(string); strings.TrimSpace(rawAction) != "" {
				action = strings.TrimSpace(rawAction)
				foundAction = true
			}
		}
		if !foundAction && opts.Stream && operation != clusterOperationCountTokens {
			action = "streamGenerateContent"
		}
		modelID := strings.TrimSpace(req.Model)
		if parsed := strings.TrimSpace(modelID); parsed != "" {
			return "/v1beta/models/" + parsed + ":" + action, ""
		}
	case "openai-response":
		if opts.Alt == "responses/compact" {
			return "/v1/responses/compact", ""
		}
		return "/v1/responses", ""
	case "openai":
		if looksLikeOpenAICompletions(body) {
			return "/v1/completions", ""
		}
		return "/v1/chat/completions", ""
	}
	return "", ""
}

func looksLikeOpenAICompletions(body []byte) bool {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	if gjson.GetBytes(body, "messages").Exists() {
		return false
	}
	return gjson.GetBytes(body, "prompt").Exists()
}

func requestedModelFromBody(body []byte) string {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ""
	}
	return strings.TrimSpace(gjson.GetBytes(body, "model").String())
}

func requestedModelFromGeminiPath(path string) string {
	const prefix = "/v1beta/models/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	actionIndex := strings.LastIndex(strings.TrimSpace(path), ":")
	if actionIndex < 0 || actionIndex <= len(prefix) {
		return ""
	}
	return strings.TrimSpace(path[len(prefix):actionIndex])
}

func resolvePeerModel(binding *cluster.PeerBinding, requestedModel string) string {
	result := thinking.ParseSuffix(strings.TrimSpace(requestedModel))
	base := strings.TrimSpace(result.ModelName)
	if base == "" {
		base = strings.TrimSpace(requestedModel)
	}
	for _, prefix := range []string{strings.TrimSpace(binding.NodeID), strings.TrimSpace(binding.ConfiguredID)} {
		prefix = strings.Trim(prefix, "/")
		if prefix == "" {
			continue
		}
		needle := prefix + "/"
		if strings.HasPrefix(base, needle) {
			base = strings.TrimPrefix(base, needle)
			break
		}
	}
	if !result.HasSuffix || strings.TrimSpace(result.RawSuffix) == "" {
		return base
	}
	return base + "(" + result.RawSuffix + ")"
}

func rewriteGeminiPath(path, model string) string {
	if strings.TrimSpace(model) == "" {
		return path
	}
	const prefix = "/v1beta/models/"
	if !strings.HasPrefix(path, prefix) {
		return path
	}
	action := geminiActionFromPath(path)
	if action == "" {
		return path
	}
	return prefix + model + ":" + action
}

func rewriteJSONModel(body []byte, model string) []byte {
	if len(body) == 0 || strings.TrimSpace(model) == "" {
		return body
	}
	updated, err := sjson.SetBytes(body, "model", model)
	if err != nil {
		return body
	}
	return updated
}

func geminiActionFromPath(path string) string {
	idx := strings.LastIndex(strings.TrimSpace(path), ":")
	if idx < 0 || idx >= len(path)-1 {
		return ""
	}
	return strings.TrimSpace(path[idx+1:])
}

func sanitizeRawQuery(rawQuery string) string {
	if strings.TrimSpace(rawQuery) == "" {
		return ""
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return rawQuery
	}
	values.Del("key")
	values.Del("auth_token")
	return values.Encode()
}

func sanitizeOutboundHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	headers.Del("Authorization")
	headers.Del("X-Api-Key")
	headers.Del("X-Goog-Api-Key")
	headers.Del("X-Management-Key")
	headers.Del("Host")
	headers.Del("Content-Length")
	headers.Del(cluster.HeaderHop)
	headers.Del(cluster.HeaderForwardedBy)
}

func authInjectorForFamily(family clusterForwardFamily) cluster.PublicAuthInjector {
	return func(headers http.Header, apiKey string) {
		if headers == nil {
			return
		}
		headers.Del("Authorization")
		headers.Del("X-Api-Key")
		headers.Del("X-Goog-Api-Key")
		switch family {
		case clusterForwardClaude:
			headers.Set("X-Api-Key", apiKey)
		case clusterForwardGemini:
			headers.Set("X-Goog-Api-Key", apiKey)
		default:
			headers.Set("Authorization", "Bearer "+apiKey)
		}
	}
}

func streamOpenAIChunks(body io.Reader, completions bool, out chan<- cliproxyexecutor.StreamChunk) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(nil, streamScannerBuffer)
	for scanner.Scan() {
		payload := jsonPayload(scanner.Bytes())
		if len(payload) == 0 {
			continue
		}
		if completions {
			payload = convertCompletionsChunkToChat(payload)
			if len(payload) == 0 {
				continue
			}
		}
		out <- cliproxyexecutor.StreamChunk{Payload: bytes.Clone(payload)}
	}
	if err := scanner.Err(); err != nil {
		out <- cliproxyexecutor.StreamChunk{Err: err}
	}
}

func streamGeminiChunks(body io.Reader, out chan<- cliproxyexecutor.StreamChunk) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(nil, streamScannerBuffer)
	for scanner.Scan() {
		payload := jsonPayload(scanner.Bytes())
		if len(payload) == 0 {
			continue
		}
		out <- cliproxyexecutor.StreamChunk{Payload: bytes.Clone(payload)}
	}
	if err := scanner.Err(); err != nil {
		out <- cliproxyexecutor.StreamChunk{Err: err}
	}
}

func streamSSELines(body io.Reader, out chan<- cliproxyexecutor.StreamChunk) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(nil, streamScannerBuffer)
	for scanner.Scan() {
		line := scanner.Bytes()
		cloned := make([]byte, len(line)+1)
		copy(cloned, line)
		cloned[len(line)] = '\n'
		out <- cliproxyexecutor.StreamChunk{Payload: cloned}
	}
	if err := scanner.Err(); err != nil {
		out <- cliproxyexecutor.StreamChunk{Err: err}
	}
}

func readErrorResponse(resp *http.Response) error {
	if resp == nil {
		return statusErr{code: http.StatusBadGateway, msg: "cluster peer returned no response"}
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	body, _ := io.ReadAll(resp.Body)
	return statusErrWithHeaders{
		statusErr: statusErr{code: resp.StatusCode, msg: string(body)},
		headers:   resp.Header.Clone(),
	}
}

func metadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []byte:
		return strings.TrimSpace(string(typed))
	default:
		return ""
	}
}

func metadataBytes(metadata map[string]any, key string) []byte {
	if len(metadata) == 0 {
		return nil
	}
	value, ok := metadata[key]
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case []byte:
		return bytes.Clone(typed)
	case string:
		return []byte(typed)
	default:
		return nil
	}
}

func metadataHeader(metadata map[string]any, key string) http.Header {
	if len(metadata) == 0 {
		return make(http.Header)
	}
	value, ok := metadata[key]
	if !ok {
		return make(http.Header)
	}
	switch typed := value.(type) {
	case http.Header:
		return typed.Clone()
	default:
		return make(http.Header)
	}
}

func metadataBool(metadata map[string]any, key string) bool {
	if len(metadata) == 0 {
		return false
	}
	value, ok := metadata[key]
	if !ok {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func (e *ClusterExecutor) localNodeID() string {
	if e == nil || e.cfg == nil {
		return ""
	}
	return strings.TrimSpace(e.cfg.Cluster.NodeID)
}

func convertCompletionsResponseToChat(payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}
	var source map[string]any
	if err := json.Unmarshal(payload, &source); err != nil {
		return payload
	}
	choices, _ := source["choices"].([]any)
	chatChoices := make([]map[string]any, 0, len(choices))
	for _, rawChoice := range choices {
		choiceMap, _ := rawChoice.(map[string]any)
		if choiceMap == nil {
			continue
		}
		chatChoice := map[string]any{
			"index": choiceMap["index"],
			"message": map[string]any{
				"role":    "assistant",
				"content": stringFromAny(choiceMap["text"]),
			},
		}
		if finishReason, ok := choiceMap["finish_reason"]; ok {
			chatChoice["finish_reason"] = finishReason
		}
		if logprobs, ok := choiceMap["logprobs"]; ok {
			chatChoice["logprobs"] = logprobs
		}
		chatChoices = append(chatChoices, chatChoice)
	}

	target := map[string]any{
		"id":      source["id"],
		"object":  "chat.completion",
		"created": source["created"],
		"model":   source["model"],
		"choices": chatChoices,
	}
	if usage, ok := source["usage"]; ok {
		target["usage"] = usage
	}
	out, err := json.Marshal(target)
	if err != nil {
		return payload
	}
	return out
}

func convertCompletionsChunkToChat(payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}
	var source map[string]any
	if err := json.Unmarshal(payload, &source); err != nil {
		return payload
	}
	choices, _ := source["choices"].([]any)
	chatChoices := make([]map[string]any, 0, len(choices))
	for _, rawChoice := range choices {
		choiceMap, _ := rawChoice.(map[string]any)
		if choiceMap == nil {
			continue
		}
		chatChoice := map[string]any{
			"index": choiceMap["index"],
			"delta": map[string]any{
				"content": stringFromAny(choiceMap["text"]),
			},
		}
		if finishReason, ok := choiceMap["finish_reason"]; ok {
			chatChoice["finish_reason"] = finishReason
		}
		if logprobs, ok := choiceMap["logprobs"]; ok {
			chatChoice["logprobs"] = logprobs
		}
		chatChoices = append(chatChoices, chatChoice)
	}

	target := map[string]any{
		"id":      source["id"],
		"object":  "chat.completion.chunk",
		"created": source["created"],
		"model":   source["model"],
		"choices": chatChoices,
	}
	out, err := json.Marshal(target)
	if err != nil {
		return payload
	}
	return out
}

func stringFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}
