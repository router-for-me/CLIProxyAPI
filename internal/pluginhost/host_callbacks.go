package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

type rpcHostHTTPRequest struct {
	HTTPClientID   string       `json:"http_client_id,omitempty"`
	HostCallbackID string       `json:"host_callback_id,omitempty"`
	Method         string       `json:"method,omitempty"`
	URL            string       `json:"url,omitempty"`
	Headers        httpHeader   `json:"headers,omitempty"`
	Body           []byte       `json:"body,omitempty"`
	Request        *httpRequest `json:"request,omitempty"`
}

type httpHeader map[string][]string

type httpRequest struct {
	Method  string     `json:"method,omitempty"`
	URL     string     `json:"url,omitempty"`
	Headers httpHeader `json:"headers,omitempty"`
	Body    []byte     `json:"body,omitempty"`
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
}

type hostCallbackPluginIDKey struct{}

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

func hostCallbackPluginIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	pluginID, _ := ctx.Value(hostCallbackPluginIDKey{}).(string)
	return strings.TrimSpace(pluginID)
}

func (h *Host) callFromPlugin(ctx context.Context, method string, request []byte) ([]byte, error) {
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
	case pluginabi.MethodHostHTTPStreamRead:
		return h.callHostHTTPStreamRead(ctx, request)
	case pluginabi.MethodHostHTTPStreamClose:
		return h.callHostHTTPStreamClose(request)
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
	httpReq, callbackID, errDecode := decodeHostHTTPRequestWithCallbackID(request)
	if errDecode != nil {
		return nil, errDecode
	}
	ctx = h.resolveCallbackContext(callbackID, ctx)
	resp, errDo := h.newHTTPClient(nil).Do(ctx, httpReq)
	if errDo != nil {
		return nil, errDo
	}
	return marshalRPCResult(resp)
}

func (h *Host) callHostHTTPDoStream(ctx context.Context, request []byte) ([]byte, error) {
	httpReq, callbackID, errDecode := decodeHostHTTPRequestWithCallbackID(request)
	if errDecode != nil {
		return nil, errDecode
	}
	ctx = h.resolveCallbackContext(callbackID, ctx)
	if ctx == nil {
		ctx = context.Background()
	}
	streamCtx, cancel := context.WithCancel(ctx)
	resp, errDo := h.newHTTPClient(nil).DoStream(streamCtx, httpReq)
	if errDo != nil {
		cancel()
		return nil, errDo
	}
	streamID := ""
	if h != nil && h.httpStreams != nil {
		streamID = h.httpStreams.open(resp.Chunks, cancel)
	}
	if streamID == "" {
		cancel()
		return nil, fmt.Errorf("host http stream bridge is unavailable")
	}
	return marshalRPCResult(rpcHostHTTPStreamResponse{
		StatusCode: resp.StatusCode,
		Headers:    httpHeader(resp.Headers),
		StreamID:   streamID,
	})
}

func (h *Host) callHostHTTPStreamRead(ctx context.Context, request []byte) ([]byte, error) {
	var req rpcHostHTTPStreamReadRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host http stream read request: %w", errUnmarshal)
	}
	if h == nil || h.httpStreams == nil {
		return nil, fmt.Errorf("host http stream bridge is unavailable")
	}
	chunk, done, errRead := h.httpStreams.read(ctx, req.StreamID)
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

func (h *Host) callHostHTTPStreamClose(request []byte) ([]byte, error) {
	var req rpcHostHTTPStreamCloseRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host http stream close request: %w", errUnmarshal)
	}
	if h != nil && h.httpStreams != nil {
		h.httpStreams.close(req.StreamID)
	}
	return marshalRPCResult(rpcEmptyResponse{})
}

func decodeHostHTTPRequest(raw []byte) (pluginapi.HTTPRequest, error) {
	httpReq, _, errDecode := decodeHostHTTPRequestWithCallbackID(raw)
	return httpReq, errDecode
}

func decodeHostHTTPRequestWithCallbackID(raw []byte) (pluginapi.HTTPRequest, string, error) {
	var req rpcHostHTTPRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return pluginapi.HTTPRequest{}, "", fmt.Errorf("decode host http request: %w", errUnmarshal)
	}
	if req.Request != nil {
		return pluginapi.HTTPRequest{
			Method:  req.Request.Method,
			URL:     req.Request.URL,
			Headers: map[string][]string(req.Request.Headers),
			Body:    append([]byte(nil), req.Request.Body...),
		}, req.HostCallbackID, nil
	}
	return pluginapi.HTTPRequest{
		Method:  req.Method,
		URL:     req.URL,
		Headers: map[string][]string(req.Headers),
		Body:    append([]byte(nil), req.Body...),
	}, req.HostCallbackID, nil
}

func (h *Host) callHostStreamEmit(ctx context.Context, request []byte) ([]byte, error) {
	var req rpcStreamEmitRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode stream emit request: %w", errUnmarshal)
	}
	chunk := pluginapi.ExecutorStreamChunk{Payload: append([]byte(nil), req.Payload...)}
	if req.ErrorDetails != nil {
		chunk.Err = newPluginStreamError(req.ErrorDetails, req.Error)
	} else if req.Error != "" {
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
	h.streams.close(req.StreamID, req.Error, req.ErrorDetails)
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
	executor := h.currentModelExecutor()
	if executor == nil {
		return nil, fmt.Errorf("host model executor is unavailable")
	}
	skipPluginID := h.callbackCallerPluginID(ctx, req.HostCallbackID)
	ctx = h.resolveCallbackContext(req.HostCallbackID, ctx)
	resp, errMsg := executor.ExecuteModel(ctx, modelExecutionRequestFromPlugin(req.HostModelExecutionRequest, skipPluginID))
	if errMsg != nil {
		return marshalRPCResult(hostModelExecutionErrorResponse(errMsg))
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
	}
}

type pluginStreamError struct {
	statusCode int
	headers    httpHeader
	body       []byte
	message    string
}

func newPluginStreamError(details *pluginapi.HostModelError, fallback string) error {
	if details == nil {
		return fmt.Errorf("%s", fallback)
	}
	message := details.Message
	if message == "" && len(details.Body) > 0 {
		message = string(details.Body)
	}
	if message == "" {
		message = fallback
	}
	if message == "" && details.StatusCode > 0 {
		message = fmt.Sprintf("upstream returned status %d", details.StatusCode)
	}
	return &pluginStreamError{
		statusCode: details.StatusCode,
		headers:    httpHeader(cloneHeader(details.Headers)),
		body:       append([]byte(nil), details.Body...),
		message:    message,
	}
}

func (e *pluginStreamError) Error() string {
	if e == nil {
		return ""
	}
	if len(e.body) > 0 {
		return string(e.body)
	}
	return e.message
}

func (e *pluginStreamError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.statusCode
}

func (e *pluginStreamError) Headers() http.Header {
	if e == nil {
		return nil
	}
	return cloneHeader(http.Header(e.headers))
}

// hostModelExecutionErrorResponse builds a successful RPC result that carries a
// structured model failure in Error. Callers must inspect Error/StatusCode.
func hostModelExecutionErrorResponse(errMsg *interfaces.ErrorMessage) pluginapi.HostModelExecutionResponse {
	details := hostModelErrorFromMessage(errMsg)
	if details == nil {
		return pluginapi.HostModelExecutionResponse{}
	}
	return pluginapi.HostModelExecutionResponse{
		StatusCode: details.StatusCode,
		Headers:    cloneHeader(details.Headers),
		Body:       append([]byte(nil), details.Body...),
		Error:      details,
	}
}

// hostModelStreamErrorResponse builds a successful RPC result that carries a
// structured stream-startup failure in Error. StreamID is left empty.
func hostModelStreamErrorResponse(errMsg *interfaces.ErrorMessage) pluginapi.HostModelStreamResponse {
	details := hostModelErrorFromMessage(errMsg)
	if details == nil {
		return pluginapi.HostModelStreamResponse{}
	}
	return pluginapi.HostModelStreamResponse{
		StatusCode: details.StatusCode,
		Headers:    cloneHeader(details.Headers),
		Error:      details,
	}
}

// hostModelErrorFromMessage maps interfaces.ErrorMessage into HostModelError.
// Body is filled from the error string when no raw upstream body is available.
func hostModelErrorFromMessage(errMsg *interfaces.ErrorMessage) *pluginapi.HostModelError {
	if errMsg == nil {
		return nil
	}
	message := ""
	if errMsg.Error != nil {
		message = errMsg.Error.Error()
	}
	if message == "" && errMsg.StatusCode > 0 {
		message = fmt.Sprintf("model execution failed with status %d", errMsg.StatusCode)
	}
	return &pluginapi.HostModelError{
		StatusCode: errMsg.StatusCode,
		Headers:    cloneHeader(errMsg.Addon),
		Body:       []byte(message),
		Message:    message,
	}
}

func hostModelErrorFromError(err error) *pluginapi.HostModelError {
	if err == nil {
		return nil
	}
	message := err.Error()
	details := &pluginapi.HostModelError{
		Body:    []byte(message),
		Message: message,
	}
	if statusErr, ok := err.(interface{ StatusCode() int }); ok {
		details.StatusCode = statusErr.StatusCode()
	}
	if headerErr, ok := err.(interface{ Headers() http.Header }); ok {
		details.Headers = cloneHeader(headerErr.Headers())
	}
	if streamErr, ok := err.(*handlers.ModelExecutionStreamError); ok && streamErr != nil {
		details.StatusCode = streamErr.StatusCode
		details.Headers = cloneHeader(streamErr.Headers)
	}
	return details
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
