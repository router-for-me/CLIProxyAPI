package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

type rpcHostHTTPRequest struct {
	HTTPClientID   string                     `json:"http_client_id,omitempty"`
	HostCallbackID string                     `json:"host_callback_id,omitempty"`
	OperationID    string                     `json:"operation_id,omitempty"`
	Method         string                     `json:"method,omitempty"`
	URL            string                     `json:"url,omitempty"`
	Headers        httpHeader                 `json:"headers,omitempty"`
	Body           []byte                     `json:"body,omitempty"`
	WireProfile    *pluginapi.HTTPWireProfile `json:"wire_profile,omitempty"`
	Request        *httpRequest               `json:"request,omitempty"`
}

type httpHeader map[string][]string

type httpRequest struct {
	Method      string                     `json:"method,omitempty"`
	URL         string                     `json:"url,omitempty"`
	Headers     httpHeader                 `json:"headers,omitempty"`
	Body        []byte                     `json:"body,omitempty"`
	WireProfile *pluginapi.HTTPWireProfile `json:"wire_profile,omitempty"`
}

type rpcHostHTTPStreamResponse struct {
	StatusCode int                         `json:"status_code"`
	Headers    httpHeader                  `json:"headers,omitempty"`
	StreamID   string                      `json:"stream_id,omitempty"`
	Chunks     []pluginapi.HTTPStreamChunk `json:"chunks,omitempty"`
}

type rpcHostHTTPStreamReadRequest struct {
	StreamID string `json:"stream_id"`
}

type rpcHostHTTPStreamReadResponse struct {
	Payload []byte `json:"payload,omitempty"`
	Error   string `json:"error,omitempty"`
	Done    bool   `json:"done,omitempty"`
}

type rpcHostHTTPStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
}

type rpcHostHTTPOperationOpenRequest struct {
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcHostHTTPOperationOpenResponse struct {
	OperationID string `json:"operation_id"`
}

type rpcHostHTTPCancelRequest struct {
	HostCallbackID string `json:"host_callback_id,omitempty"`
	OperationID    string `json:"operation_id"`
}

type rpcHostLogRequest struct {
	HostCallbackID string         `json:"host_callback_id,omitempty"`
	Level          string         `json:"level,omitempty"`
	Message        string         `json:"message,omitempty"`
	Fields         map[string]any `json:"fields,omitempty"`
}

type rpcHostModelExecutionRequest struct {
	pluginapi.HostModelExecutionRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type dynamicHostCallbackEntry struct {
	host     *Host
	pluginID string
	instance *hostCallbackInstance
}

type hostCallbackPluginIDKey struct{}
type hostCallbackInstanceKey struct{}

type hostCallbackInstance struct {
	closed atomic.Bool
}

func withHostCallbackPluginID(ctx context.Context, pluginID string) context.Context {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		if ctx == nil {
			return context.Background()
		}
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, hostCallbackPluginIDKey{}, pluginID)
}

func withHostCallbackIdentity(ctx context.Context, pluginID string, instance *hostCallbackInstance) context.Context {
	ctx = withHostCallbackPluginID(ctx, pluginID)
	if ctx == nil {
		ctx = context.Background()
	}
	if instance != nil {
		ctx = context.WithValue(ctx, hostCallbackInstanceKey{}, instance)
	}
	return ctx
}

func hostCallbackInstanceFromContext(ctx context.Context) *hostCallbackInstance {
	if ctx == nil {
		return nil
	}
	instance, _ := ctx.Value(hostCallbackInstanceKey{}).(*hostCallbackInstance)
	return instance
}

func hostCallbackPluginIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	pluginID, _ := ctx.Value(hostCallbackPluginIDKey{}).(string)
	return strings.TrimSpace(pluginID)
}

func (h *Host) callFromPlugin(ctx context.Context, method string, request []byte) ([]byte, error) {
	if instance := hostCallbackInstanceFromContext(ctx); instance != nil && instance.closed.Load() {
		return nil, fmt.Errorf("host plugin callback instance is closed")
	}
	switch method {
	case pluginabi.MethodHostModelExecute:
		return h.callHostModelExecute(ctx, request)
	case pluginabi.MethodHostModelExecuteStream:
		return h.callHostModelExecuteStream(ctx, request)
	case pluginabi.MethodHostModelStreamRead:
		return h.callHostModelStreamRead(ctx, request)
	case pluginabi.MethodHostModelStreamClose:
		return h.callHostModelStreamClose(request)
	case pluginabi.MethodHostHTTPDo:
		return h.callHostHTTPDo(ctx, request)
	case pluginabi.MethodHostHTTPDoStream:
		return h.callHostHTTPDoStream(ctx, request)
	case pluginabi.MethodHostHTTPOperationOpen:
		return h.callHostHTTPOperationOpen(ctx, request)
	case pluginabi.MethodHostHTTPCancel:
		return h.callHostHTTPCancel(ctx, request)
	case pluginabi.MethodHostHTTPStreamRead:
		return h.callHostHTTPStreamRead(ctx, request)
	case pluginabi.MethodHostHTTPStreamClose:
		return h.callHostHTTPStreamClose(ctx, request)
	case pluginabi.MethodHostStreamEmit:
		return h.callHostStreamEmit(ctx, request)
	case pluginabi.MethodHostStreamClose:
		return h.callHostStreamClose(request)
	case pluginabi.MethodHostLog:
		return h.callHostLog(ctx, request)
	case pluginabi.MethodHostAuthList:
		return h.callHostAuthList(ctx, request)
	case pluginabi.MethodHostAuthGet:
		return h.callHostAuthGet(ctx, request)
	case pluginabi.MethodHostAuthGetRuntime:
		return h.callHostAuthGetRuntime(ctx, request)
	case pluginabi.MethodHostAuthSave:
		return h.callHostAuthSave(ctx, request)
	case pluginabi.MethodHostAffinityLookup:
		return h.callHostAffinityLookup(ctx, request)
	default:
		return nil, fmt.Errorf("unsupported host callback %s", method)
	}
}

func (h *Host) callbackCallerPluginID(ctx context.Context, callbackID string) string {
	if pluginID := hostCallbackPluginIDFromContext(ctx); pluginID != "" {
		return pluginID
	}
	return h.callbackContextPluginID(callbackID)
}

func (h *Host) callHostHTTPDo(ctx context.Context, request []byte) ([]byte, error) {
	httpReq, callbackID, operationID, errDecode := decodeHostHTTPRequestWithOperationID(request)
	if errDecode != nil {
		return nil, errDecode
	}
	operation, errAcquire := h.acquireHostHTTPOperation(ctx, callbackID, operationID)
	if errAcquire != nil {
		return nil, errAcquire
	}
	defer operation.finish()
	resp, errDo := h.newHTTPClient(nil).Do(operation.ctx, httpReq)
	if errDo != nil {
		return nil, errDo
	}
	return marshalRPCResult(resp)
}

func newStreamContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(ctx)
}

func (h *Host) callHostHTTPDoStream(ctx context.Context, request []byte) ([]byte, error) {
	httpReq, callbackID, operationID, errDecode := decodeHostHTTPRequestWithOperationID(request)
	if errDecode != nil {
		return nil, errDecode
	}
	operation, errAcquire := h.acquireHostHTTPOperation(ctx, callbackID, operationID)
	if errAcquire != nil {
		return nil, errAcquire
	}
	streamCtx := operation.ctx
	streamID := ""
	keepStream := false
	defer func() {
		if keepStream {
			return
		}
		if streamID != "" && h != nil && h.httpStreams != nil {
			h.httpStreams.close(operation.pluginID, operation.instance, streamID)
		}
		operation.finish()
	}()

	resp, errDo := h.newHTTPClient(nil).DoStream(streamCtx, httpReq)
	if errDo != nil {
		return nil, errDo
	}
	if h != nil && h.httpStreams != nil {
		streamID = h.httpStreams.open(operation.pluginID, operation.instance, resp.Chunks, operation.cancel, operation.finish)
	}
	if streamID == "" {
		return nil, fmt.Errorf("host http stream bridge is unavailable")
	}
	rawResp, errMarshal := marshalRPCResult(rpcHostHTTPStreamResponse{
		StatusCode: resp.StatusCode,
		Headers:    httpHeader(resp.Headers),
		StreamID:   streamID,
	})
	if errMarshal != nil {
		return nil, errMarshal
	}
	if operation.entry != nil {
		cleanupStream := func() {
			h.httpStreams.close(operation.pluginID, operation.instance, streamID)
		}
		if !h.httpOperations.setCleanup(operation.pluginID, operation.operationID, operation.entry, cleanupStream) {
			return nil, context.Canceled
		}
	}
	keepStream = true
	return rawResp, nil
}

func (h *Host) callHostHTTPOperationOpen(ctx context.Context, request []byte) ([]byte, error) {
	var req rpcHostHTTPOperationOpenRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host http operation open request: %w", errUnmarshal)
	}
	operationID, errOpen := h.openHostHTTPOperation(ctx, req.HostCallbackID)
	if errOpen != nil {
		return nil, errOpen
	}
	return marshalRPCResult(rpcHostHTTPOperationOpenResponse{OperationID: operationID})
}

func (h *Host) callHostHTTPCancel(ctx context.Context, request []byte) ([]byte, error) {
	var req rpcHostHTTPCancelRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host http cancel request: %w", errUnmarshal)
	}
	operationID := strings.TrimSpace(req.OperationID)
	if operationID == "" {
		return nil, fmt.Errorf("host http operation id is required")
	}
	if h != nil && h.httpOperations != nil {
		pluginID := h.callbackCallerPluginID(ctx, req.HostCallbackID)
		instance := hostCallbackInstanceFromContext(ctx)
		if instance == nil {
			_, _, instance, _ = h.lookupCallbackContext(req.HostCallbackID)
		}
		h.httpOperations.cancel(pluginID, instance, operationID)
	}
	return marshalRPCResult(rpcEmptyResponse{})
}

func (h *Host) callHostHTTPStreamRead(ctx context.Context, request []byte) ([]byte, error) {
	var req rpcHostHTTPStreamReadRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host http stream read request: %w", errUnmarshal)
	}
	if h == nil || h.httpStreams == nil {
		return nil, fmt.Errorf("host http stream bridge is unavailable")
	}
	chunk, done, errRead := h.httpStreams.read(ctx, hostCallbackPluginIDFromContext(ctx), hostCallbackInstanceFromContext(ctx), req.StreamID)
	if errRead != nil {
		return nil, errRead
	}
	resp := rpcHostHTTPStreamReadResponse{
		Payload: append([]byte(nil), chunk.Payload...),
		Done:    done,
	}
	if chunk.Err != nil {
		resp.Error = chunk.Err.Error()
		resp.Done = true
	}
	return marshalRPCResult(resp)
}

func (h *Host) callHostHTTPStreamClose(ctx context.Context, request []byte) ([]byte, error) {
	var req rpcHostHTTPStreamCloseRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host http stream close request: %w", errUnmarshal)
	}
	if h != nil && h.httpStreams != nil {
		h.httpStreams.close(hostCallbackPluginIDFromContext(ctx), hostCallbackInstanceFromContext(ctx), req.StreamID)
	}
	return marshalRPCResult(rpcEmptyResponse{})
}

func decodeHostHTTPRequest(raw []byte) (pluginapi.HTTPRequest, error) {
	httpReq, _, errDecode := decodeHostHTTPRequestWithCallbackID(raw)
	return httpReq, errDecode
}

func decodeHostHTTPRequestWithCallbackID(raw []byte) (pluginapi.HTTPRequest, string, error) {
	httpReq, callbackID, _, errDecode := decodeHostHTTPRequestWithOperationID(raw)
	return httpReq, callbackID, errDecode
}

func decodeHostHTTPRequestWithOperationID(raw []byte) (pluginapi.HTTPRequest, string, string, error) {
	var req rpcHostHTTPRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return pluginapi.HTTPRequest{}, "", "", fmt.Errorf("decode host http request: %w", errUnmarshal)
	}
	if req.Request != nil {
		wireProfile := req.Request.WireProfile
		if wireProfile == nil {
			wireProfile = req.WireProfile
		}
		return pluginapi.HTTPRequest{
			Method:      req.Request.Method,
			URL:         req.Request.URL,
			Headers:     map[string][]string(req.Request.Headers),
			Body:        append([]byte(nil), req.Request.Body...),
			WireProfile: cloneWireProfile(wireProfile),
		}, req.HostCallbackID, strings.TrimSpace(req.OperationID), nil
	}
	return pluginapi.HTTPRequest{
		Method:      req.Method,
		URL:         req.URL,
		Headers:     map[string][]string(req.Headers),
		Body:        append([]byte(nil), req.Body...),
		WireProfile: cloneWireProfile(req.WireProfile),
	}, req.HostCallbackID, strings.TrimSpace(req.OperationID), nil
}

func cloneWireProfile(src *pluginapi.HTTPWireProfile) *pluginapi.HTTPWireProfile {
	if src == nil {
		return nil
	}
	dst := *src
	dst.HeaderProfile = append([]string(nil), src.HeaderProfile...)
	return &dst
}

func (h *Host) callHostStreamEmit(ctx context.Context, request []byte) ([]byte, error) {
	var req rpcStreamEmitRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode stream emit request: %w", errUnmarshal)
	}
	chunk := pluginapi.ExecutorStreamChunk{Payload: append([]byte(nil), req.Payload...)}
	if req.Error != "" {
		chunk.Err = fmt.Errorf("%s", req.Error)
	}
	if errEmit := h.streams.emit(ctx, req.StreamID, chunk); errEmit != nil {
		return nil, errEmit
	}
	return marshalRPCResult(rpcEmptyResponse{})
}

func (h *Host) callHostStreamClose(request []byte) ([]byte, error) {
	var req rpcStreamCloseRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode stream close request: %w", errUnmarshal)
	}
	h.streams.close(req.StreamID, req.Error)
	return marshalRPCResult(rpcEmptyResponse{})
}

func (h *Host) callHostModelExecute(ctx context.Context, request []byte) ([]byte, error) {
	var req rpcHostModelExecutionRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host model execution request: %w", errUnmarshal)
	}
	if req.Stream {
		return nil, fmt.Errorf("host.model.execute requires stream=false")
	}
	if errProxy := validateHostModelProxy(req.ProxyURL); errProxy != nil {
		return nil, errProxy
	}
	executor := h.currentModelExecutor()
	if executor == nil {
		return nil, fmt.Errorf("host model executor is unavailable")
	}
	skipPluginID := h.callbackCallerPluginID(ctx, req.HostCallbackID)
	ctx = h.resolveCallbackContext(req.HostCallbackID, ctx)
	resp, errMsg := executor.ExecuteModel(ctx, modelExecutionRequestFromPlugin(req.HostModelExecutionRequest, skipPluginID))
	if errMsg != nil {
		return nil, modelExecutionError(errMsg)
	}
	return marshalRPCResult(pluginapi.HostModelExecutionResponse{
		StatusCode: resp.StatusCode,
		Headers:    cloneHeader(resp.Headers),
		Body:       append([]byte(nil), resp.Body...),
	})
}

func modelExecutionRequestFromPlugin(req pluginapi.HostModelExecutionRequest, skipPluginID string) handlers.ModelExecutionRequest {
	return handlers.ModelExecutionRequest{
		EntryProtocol:           req.EntryProtocol,
		ExitProtocol:            req.ExitProtocol,
		Model:                   req.Model,
		Stream:                  req.Stream,
		Body:                    append([]byte(nil), req.Body...),
		Headers:                 cloneHeader(req.Headers),
		Query:                   cloneValues(req.Query),
		Alt:                     req.Alt,
		SkipInterceptorPluginID: skipPluginID,
		SkipRouterPluginID:      skipPluginID,
		ForcedProvider:          req.ForcedProvider,
		AuthID:                  req.AuthID,
		ProxyURL:                strings.TrimSpace(req.ProxyURL),
	}
}

func validateHostModelProxy(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if !proxyutil.ValidRequestProxy(raw) {
		return &modelExecutionStatusError{
			err:        fmt.Errorf("invalid proxy_url"),
			statusCode: http.StatusBadRequest,
		}
	}
	return nil
}

type modelExecutionStatusError struct {
	err        error
	statusCode int
}

func (e *modelExecutionStatusError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	if e.statusCode > 0 {
		return fmt.Sprintf("model execution failed with status %d", e.statusCode)
	}
	return "model execution failed"
}

func (e *modelExecutionStatusError) StatusCode() int {
	return e.statusCode
}

func (e *modelExecutionStatusError) Unwrap() error {
	return e.err
}

func modelExecutionError(errMsg *interfaces.ErrorMessage) error {
	if errMsg == nil {
		return nil
	}
	if errMsg.StatusCode > 0 && clienterror.HTTPStatusFromError(errMsg.Error) != errMsg.StatusCode {
		return &modelExecutionStatusError{
			err:        errMsg.Error,
			statusCode: errMsg.StatusCode,
		}
	}
	if errMsg.Error != nil {
		return errMsg.Error
	}
	return fmt.Errorf("model execution failed")
}

func (h *Host) callHostLog(ctx context.Context, request []byte) ([]byte, error) {
	var req rpcHostLogRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host log request: %w", errUnmarshal)
	}
	ctx = h.resolveCallbackContext(req.HostCallbackID, ctx)
	message := strings.TrimSpace(req.Message)
	if message == "" {
		message = "plugin log"
	}
	fields := log.Fields{}
	for key, value := range req.Fields {
		key = strings.TrimSpace(key)
		if key != "" {
			fields[key] = value
		}
	}
	if requestID := logging.GetRequestID(ctx); requestID != "" {
		fields["request_id"] = requestID
	}
	entry := log.WithFields(fields)
	switch strings.ToLower(strings.TrimSpace(req.Level)) {
	case "trace":
		entry.Trace(message)
	case "info":
		entry.Info(message)
	case "warn", "warning":
		entry.Warn(message)
	case "error":
		entry.Error(message)
	default:
		entry.Debug(message)
	}
	return marshalRPCResult(rpcEmptyResponse{})
}
