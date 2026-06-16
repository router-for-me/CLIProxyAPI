package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type rpcPluginAdapter struct {
	id     string
	host   *Host
	client pluginClient
}

type rpcAuthProvider struct {
	*rpcPluginAdapter
}

type rpcFrontendAuthProvider struct {
	*rpcPluginAdapter
}

type rpcProviderExecutor struct {
	*rpcPluginAdapter
}

type rpcThinkingApplier struct {
	*rpcPluginAdapter
}

type rpcResponseNormalizer struct {
	*rpcPluginAdapter
	method string
}

func registerRPCPlugin(ctx context.Context, host *Host, id string, client pluginClient, method string, configYAML []byte) (pluginapi.Plugin, error) {
	if client == nil {
		return pluginapi.Plugin{}, fmt.Errorf("plugin client is nil")
	}
	resp, errCall := callPlugin[rpcRegistration](ctx, client, method, rpcLifecycleRequest{ConfigYAML: bytes.Clone(configYAML)})
	if errCall != nil {
		return pluginapi.Plugin{}, errCall
	}
	adapter := &rpcPluginAdapter{id: id, host: host, client: client}
	plugin := pluginapi.Plugin{
		Metadata: resp.Metadata,
		Capabilities: pluginapi.Capabilities{
			FrontendAuthProviderExclusive: resp.Capabilities.FrontendAuthProvider && resp.Capabilities.FrontendAuthProviderExclusive,
			ExecutorModelScope:            resp.Capabilities.ExecutorModelScope,
			ExecutorInputFormats:          append([]string(nil), resp.Capabilities.ExecutorInputFormats...),
			ExecutorOutputFormats:         append([]string(nil), resp.Capabilities.ExecutorOutputFormats...),
		},
	}
	if resp.Capabilities.ModelRegistrar {
		plugin.Capabilities.ModelRegistrar = adapter
	}
	if resp.Capabilities.ModelProvider {
		plugin.Capabilities.ModelProvider = adapter
	}
	if resp.Capabilities.AuthProvider {
		plugin.Capabilities.AuthProvider = rpcAuthProvider{rpcPluginAdapter: adapter}
	}
	if resp.Capabilities.FrontendAuthProvider {
		plugin.Capabilities.FrontendAuthProvider = rpcFrontendAuthProvider{rpcPluginAdapter: adapter}
	}
	if resp.Capabilities.Scheduler {
		plugin.Capabilities.Scheduler = adapter
	}
	if resp.Capabilities.Executor {
		plugin.Capabilities.Executor = rpcProviderExecutor{rpcPluginAdapter: adapter}
	}
	if resp.Capabilities.RequestTranslator {
		plugin.Capabilities.RequestTranslator = adapter
	}
	if resp.Capabilities.RequestNormalizer {
		plugin.Capabilities.RequestNormalizer = adapter
	}
	if resp.Capabilities.RequestInterceptor {
		plugin.Capabilities.RequestInterceptor = adapter
	}
	if resp.Capabilities.ResponseTranslator {
		plugin.Capabilities.ResponseTranslator = adapter
	}
	if resp.Capabilities.ResponseBeforeTranslator {
		plugin.Capabilities.ResponseBeforeTranslator = rpcResponseNormalizer{rpcPluginAdapter: adapter, method: pluginabi.MethodResponseNormalizeBefore}
	}
	if resp.Capabilities.ResponseAfterTranslator {
		plugin.Capabilities.ResponseAfterTranslator = rpcResponseNormalizer{rpcPluginAdapter: adapter, method: pluginabi.MethodResponseNormalizeAfter}
	}
	if resp.Capabilities.ResponseInterceptor {
		plugin.Capabilities.ResponseInterceptor = adapter
	}
	if resp.Capabilities.StreamChunkInterceptor {
		plugin.Capabilities.StreamChunkInterceptor = adapter
	}
	if resp.Capabilities.ThinkingApplier {
		plugin.Capabilities.ThinkingApplier = rpcThinkingApplier{rpcPluginAdapter: adapter}
	}
	if resp.Capabilities.UsagePlugin {
		plugin.Capabilities.UsagePlugin = adapter
	}
	if resp.Capabilities.CommandLinePlugin {
		plugin.Capabilities.CommandLinePlugin = adapter
	}
	if resp.Capabilities.ManagementAPI {
		plugin.Capabilities.ManagementAPI = adapter
	}
	return plugin, nil
}

func callPlugin[T any](ctx context.Context, client pluginClient, method string, request any) (T, error) {
	var zero T
	rawRequest, errMarshal := json.Marshal(sanitizePluginRequest(request))
	if errMarshal != nil {
		return zero, fmt.Errorf("marshal plugin request %s: %w", method, errMarshal)
	}
	rawResp, errCall := client.Call(ctx, method, rawRequest)
	if errCall != nil {
		return zero, errCall
	}
	var envelope pluginabi.Envelope
	if errUnmarshal := json.Unmarshal(rawResp, &envelope); errUnmarshal != nil {
		return zero, fmt.Errorf("decode plugin envelope %s: %w", method, errUnmarshal)
	}
	out, errDecode := decodeEnvelopeResult[T](envelope)
	if errDecode != nil {
		return zero, fmt.Errorf("decode plugin result %s: %w", method, errDecode)
	}
	return out, nil
}

func sanitizePluginRequest(request any) any {
	switch req := request.(type) {
	case pluginapi.AuthLoginStartRequest:
		req.HTTPClient = nil
		return req
	case pluginapi.AuthLoginPollRequest:
		req.HTTPClient = nil
		return req
	case pluginapi.AuthRefreshRequest:
		req.HTTPClient = nil
		return req
	case pluginapi.AuthModelRequest:
		req.HTTPClient = nil
		return req
	case pluginapi.SchedulerPickRequest:
		req.Options.Metadata = sanitizePluginMetadata(req.Options.Metadata)
		for index := range req.Candidates {
			req.Candidates[index].Metadata = sanitizePluginMetadata(req.Candidates[index].Metadata)
		}
		return req
	case pluginapi.ExecutorRequest:
		req.HTTPClient = nil
		req.Metadata = sanitizePluginMetadata(req.Metadata)
		return req
	case pluginapi.RequestInterceptRequest:
		req.Metadata = sanitizePluginMetadata(req.Metadata)
		return req
	case pluginapi.ResponseInterceptRequest:
		req.Metadata = sanitizePluginMetadata(req.Metadata)
		return req
	case pluginapi.StreamChunkInterceptRequest:
		req.Metadata = sanitizePluginMetadata(req.Metadata)
		return req
	case rpcRequestInterceptRequest:
		req.Metadata = sanitizePluginMetadata(req.Metadata)
		return req
	case rpcResponseInterceptRequest:
		req.Metadata = sanitizePluginMetadata(req.Metadata)
		return req
	case rpcStreamChunkInterceptRequest:
		req.Metadata = sanitizePluginMetadata(req.Metadata)
		return req
	case pluginapi.ExecutorHTTPRequest:
		req.HTTPClient = nil
		return req
	case rpcExecutorRequest:
		req.HTTPClient = nil
		req.Metadata = sanitizePluginMetadata(req.Metadata)
		return req
	default:
		return request
	}
}

func sanitizePluginMetadata(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		if sanitized, ok := sanitizePluginMetadataValue(value); ok {
			dst[key] = sanitized
		}
	}
	if len(dst) == 0 {
		return nil
	}
	return dst
}

func sanitizePluginMetadataValue(value any) (any, bool) {
	switch v := value.(type) {
	case nil, string, bool, float64, float32,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return value, true
	case map[string]any:
		return sanitizePluginMetadata(v), true
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			if sanitized, ok := sanitizePluginMetadataValue(item); ok {
				out = append(out, sanitized)
			}
		}
		return out, true
	default:
		// RPC metadata crosses a JSON envelope, so unsupported Go values are normalized to JSON-compatible shapes.
		raw, errMarshal := json.Marshal(value)
		if errMarshal != nil {
			return nil, false
		}
		var decoded any
		if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
			return nil, false
		}
		return decoded, true
	}
}

func decodeRPCEnvelope[T any](raw []byte) (T, error) {
	var zero T
	var envelope pluginabi.Envelope
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		return zero, errUnmarshal
	}
	return decodeEnvelopeResult[T](envelope)
}

func decodeEnvelopeResult[T any](envelope pluginabi.Envelope) (T, error) {
	var zero T
	if !envelope.OK {
		if envelope.Error != nil {
			return zero, fmt.Errorf("%s", envelope.Error.Message)
		}
		return zero, fmt.Errorf("plugin call failed")
	}
	if len(envelope.Result) == 0 {
		return zero, nil
	}
	var out T
	if errDecode := json.Unmarshal(envelope.Result, &out); errDecode != nil {
		return zero, errDecode
	}
	return out, nil
}

func marshalRPCEnvelope(result json.RawMessage) ([]byte, error) {
	if result == nil {
		result = json.RawMessage(`{}`)
	}
	return json.Marshal(pluginabi.Envelope{OK: true, Result: result})
}

func marshalRPCError(code, message string) []byte {
	raw, _ := json.Marshal(pluginabi.Envelope{
		OK: false,
		Error: &pluginabi.Error{
			Code:    code,
			Message: message,
		},
	})
	return raw
}

func (a *rpcPluginAdapter) openHostCallbackContext(ctx context.Context) (string, func()) {
	if a == nil || a.host == nil {
		return "", func() {}
	}
	return a.host.openCallbackContextForPlugin(ctx, a.id)
}

func (a *rpcPluginAdapter) RegisterModels(ctx context.Context, req pluginapi.ModelRegistrationRequest) (pluginapi.ModelRegistrationResponse, error) {
	return callPlugin[pluginapi.ModelRegistrationResponse](ctx, a.client, pluginabi.MethodModelRegister, req)
}

func (a *rpcPluginAdapter) StaticModels(ctx context.Context, req pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
	return callPlugin[pluginapi.ModelResponse](ctx, a.client, pluginabi.MethodModelStatic, req)
}

func (a *rpcPluginAdapter) ModelsForAuth(ctx context.Context, req pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
	callbackID, closeCallback := a.openHostCallbackContext(ctx)
	defer closeCallback()
	return callPlugin[pluginapi.ModelResponse](ctx, a.client, pluginabi.MethodModelForAuth, rpcAuthModelRequest{
		AuthModelRequest: req,
		HostCallbackID:   callbackID,
	})
}

func (a *rpcPluginAdapter) Pick(ctx context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
	return callPlugin[pluginapi.SchedulerPickResponse](ctx, a.client, pluginabi.MethodSchedulerPick, req)
}

func callPluginIdentifier(client pluginClient, method string) string {
	resp, errCall := callPlugin[rpcIdentifierResponse](context.Background(), client, method, rpcEmptyResponse{})
	if errCall != nil {
		return ""
	}
	return strings.TrimSpace(resp.Identifier)
}

func (a rpcAuthProvider) Identifier() string {
	return callPluginIdentifier(a.client, pluginabi.MethodAuthIdentifier)
}

func (a rpcFrontendAuthProvider) Identifier() string {
	return callPluginIdentifier(a.client, pluginabi.MethodFrontendAuthIdentifier)
}

func (a rpcProviderExecutor) Identifier() string {
	return callPluginIdentifier(a.client, pluginabi.MethodExecutorIdentifier)
}

func (a rpcThinkingApplier) Identifier() string {
	return callPluginIdentifier(a.client, pluginabi.MethodThinkingIdentifier)
}

func (a *rpcPluginAdapter) ParseAuth(ctx context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	return callPlugin[pluginapi.AuthParseResponse](ctx, a.client, pluginabi.MethodAuthParse, req)
}

func (a *rpcPluginAdapter) StartLogin(ctx context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	callbackID, closeCallback := a.openHostCallbackContext(ctx)
	defer closeCallback()
	return callPlugin[pluginapi.AuthLoginStartResponse](ctx, a.client, pluginabi.MethodAuthLoginStart, rpcAuthLoginStartRequest{
		AuthLoginStartRequest: req,
		HostCallbackID:        callbackID,
	})
}

func (a *rpcPluginAdapter) PollLogin(ctx context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	callbackID, closeCallback := a.openHostCallbackContext(ctx)
	defer closeCallback()
	return callPlugin[pluginapi.AuthLoginPollResponse](ctx, a.client, pluginabi.MethodAuthLoginPoll, rpcAuthLoginPollRequest{
		AuthLoginPollRequest: req,
		HostCallbackID:       callbackID,
	})
}

func (a *rpcPluginAdapter) RefreshAuth(ctx context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	callbackID, closeCallback := a.openHostCallbackContext(ctx)
	defer closeCallback()
	return callPlugin[pluginapi.AuthRefreshResponse](ctx, a.client, pluginabi.MethodAuthRefresh, rpcAuthRefreshRequest{
		AuthRefreshRequest: req,
		HostCallbackID:     callbackID,
	})
}

func (a *rpcPluginAdapter) Authenticate(ctx context.Context, req pluginapi.FrontendAuthRequest) (pluginapi.FrontendAuthResponse, error) {
	return callPlugin[pluginapi.FrontendAuthResponse](ctx, a.client, pluginabi.MethodFrontendAuthAuthenticate, req)
}

func (a *rpcPluginAdapter) Execute(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	callbackID, closeCallback := a.openHostCallbackContext(ctx)
	defer closeCallback()
	return callPlugin[pluginapi.ExecutorResponse](ctx, a.client, pluginabi.MethodExecutorExecute, rpcExecutorRequest{
		ExecutorRequest: req,
		HostCallbackID:  callbackID,
	})
}

func (a *rpcPluginAdapter) CountTokens(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	callbackID, closeCallback := a.openHostCallbackContext(ctx)
	defer closeCallback()
	return callPlugin[pluginapi.ExecutorResponse](ctx, a.client, pluginabi.MethodExecutorCountTokens, rpcExecutorRequest{
		ExecutorRequest: req,
		HostCallbackID:  callbackID,
	})
}

func (a *rpcPluginAdapter) HttpRequest(ctx context.Context, req pluginapi.ExecutorHTTPRequest) (pluginapi.ExecutorHTTPResponse, error) {
	callbackID, closeCallback := a.openHostCallbackContext(ctx)
	defer closeCallback()
	return callPlugin[pluginapi.ExecutorHTTPResponse](ctx, a.client, pluginabi.MethodExecutorHTTPRequest, rpcExecutorHTTPRequest{
		ExecutorHTTPRequest: req,
		HostCallbackID:      callbackID,
	})
}

func (a *rpcPluginAdapter) TranslateRequest(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
	return callPlugin[pluginapi.PayloadResponse](ctx, a.client, pluginabi.MethodRequestTranslate, req)
}

func (a *rpcPluginAdapter) NormalizeRequest(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
	return callPlugin[pluginapi.PayloadResponse](ctx, a.client, pluginabi.MethodRequestNormalize, req)
}

func (a *rpcPluginAdapter) InterceptRequestBeforeAuth(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	callbackID, closeCallback := a.openHostCallbackContext(ctx)
	defer closeCallback()
	return callPlugin[pluginapi.RequestInterceptResponse](ctx, a.client, pluginabi.MethodRequestInterceptBefore, rpcRequestInterceptRequest{
		RequestInterceptRequest: req,
		HostCallbackID:          callbackID,
	})
}

func (a *rpcPluginAdapter) InterceptRequestAfterAuth(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	callbackID, closeCallback := a.openHostCallbackContext(ctx)
	defer closeCallback()
	return callPlugin[pluginapi.RequestInterceptResponse](ctx, a.client, pluginabi.MethodRequestInterceptAfter, rpcRequestInterceptRequest{
		RequestInterceptRequest: req,
		HostCallbackID:          callbackID,
	})
}

func (a *rpcPluginAdapter) TranslateResponse(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
	return callPlugin[pluginapi.PayloadResponse](ctx, a.client, pluginabi.MethodResponseTranslate, req)
}

func (a rpcResponseNormalizer) NormalizeResponse(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
	return callPlugin[pluginapi.PayloadResponse](ctx, a.client, a.method, req)
}

func (a *rpcPluginAdapter) InterceptResponse(ctx context.Context, req pluginapi.ResponseInterceptRequest) (pluginapi.ResponseInterceptResponse, error) {
	callbackID, closeCallback := a.openHostCallbackContext(ctx)
	defer closeCallback()
	return callPlugin[pluginapi.ResponseInterceptResponse](ctx, a.client, pluginabi.MethodResponseInterceptAfter, rpcResponseInterceptRequest{
		ResponseInterceptRequest: req,
		HostCallbackID:           callbackID,
	})
}

func (a *rpcPluginAdapter) InterceptStreamChunk(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) (pluginapi.StreamChunkInterceptResponse, error) {
	callbackID, closeCallback := a.openHostCallbackContext(ctx)
	defer closeCallback()
	return callPlugin[pluginapi.StreamChunkInterceptResponse](ctx, a.client, pluginabi.MethodResponseInterceptStreamChunk, rpcStreamChunkInterceptRequest{
		StreamChunkInterceptRequest: req,
		HostCallbackID:              callbackID,
	})
}

func (a rpcThinkingApplier) ApplyThinking(ctx context.Context, req pluginapi.ThinkingApplyRequest) (pluginapi.PayloadResponse, error) {
	callbackID, closeCallback := a.openHostCallbackContext(ctx)
	defer closeCallback()
	return callPlugin[pluginapi.PayloadResponse](ctx, a.client, pluginabi.MethodThinkingApply, rpcThinkingApplyRequest{
		ThinkingApplyRequest: req,
		HostCallbackID:       callbackID,
	})
}

func (a *rpcPluginAdapter) HandleUsage(ctx context.Context, record pluginapi.UsageRecord) {
	_, _ = callPlugin[rpcEmptyResponse](ctx, a.client, pluginabi.MethodUsageHandle, record)
}

func (a *rpcPluginAdapter) RegisterCommandLine(ctx context.Context, req pluginapi.CommandLineRegistrationRequest) (pluginapi.CommandLineRegistrationResponse, error) {
	return callPlugin[pluginapi.CommandLineRegistrationResponse](ctx, a.client, pluginabi.MethodCommandLineRegister, req)
}

func (a *rpcPluginAdapter) ExecuteCommandLine(ctx context.Context, req pluginapi.CommandLineExecutionRequest) (pluginapi.CommandLineExecutionResponse, error) {
	return callPlugin[pluginapi.CommandLineExecutionResponse](ctx, a.client, pluginabi.MethodCommandLineExecute, req)
}

func (a *rpcPluginAdapter) RegisterManagement(ctx context.Context, req pluginapi.ManagementRegistrationRequest) (pluginapi.ManagementRegistrationResponse, error) {
	resp, errCall := callPlugin[rpcManagementRegistrationResponse](ctx, a.client, pluginabi.MethodManagementRegister, req)
	if errCall != nil {
		return pluginapi.ManagementRegistrationResponse{}, errCall
	}
	routes := make([]pluginapi.ManagementRoute, 0, len(resp.Routes))
	for _, route := range resp.Routes {
		route.Handler = a
		routes = append(routes, route)
	}
	resources := make([]pluginapi.ResourceRoute, 0, len(resp.Resources))
	for _, route := range resp.Resources {
		route.Handler = a
		resources = append(resources, route)
	}
	return pluginapi.ManagementRegistrationResponse{Routes: routes, Resources: resources}, nil
}

func (a *rpcPluginAdapter) HandleManagement(ctx context.Context, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
	callbackID, closeCallback := a.openHostCallbackContext(ctx)
	defer closeCallback()
	return callPlugin[pluginapi.ManagementResponse](ctx, a.client, pluginabi.MethodManagementHandle, rpcManagementRequest{
		ManagementRequest: req,
		HostCallbackID:    callbackID,
	})
}

func httpResponseFromPlugin(resp pluginapi.ExecutorHTTPResponse, req *http.Request) *http.Response {
	status := resp.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     cloneHeader(resp.Headers),
		Body:       io.NopCloser(bytes.NewReader(bytes.Clone(resp.Body))),
		Request:    req,
	}
}
