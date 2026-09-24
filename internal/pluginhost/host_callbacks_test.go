package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

type fakeHostModelExecutor struct {
	executeModel       func(context.Context, handlers.ModelExecutionRequest) (handlers.ModelExecutionResponse, *interfaces.ErrorMessage)
	executeModelStream func(context.Context, handlers.ModelExecutionRequest) (handlers.ModelExecutionStream, *interfaces.ErrorMessage)
}

func (e *fakeHostModelExecutor) ExecuteModel(ctx context.Context, req handlers.ModelExecutionRequest) (handlers.ModelExecutionResponse, *interfaces.ErrorMessage) {
	return e.executeModel(ctx, req)
}

func (e *fakeHostModelExecutor) ExecuteModelStream(ctx context.Context, req handlers.ModelExecutionRequest) (handlers.ModelExecutionStream, *interfaces.ErrorMessage) {
	return e.executeModelStream(ctx, req)
}

func TestHostHTTPDoCallbackUsesHostHTTPClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		w.Header().Set("X-Test", "ok")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	req := pluginapi.HTTPRequest{
		Method: http.MethodPost,
		URL:    server.URL,
		Body:   []byte(`{"request":true}`),
	}
	rawReq, errMarshal := json.Marshal(req)
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}

	rawResp, errCall := New().callFromPlugin(context.Background(), pluginabi.MethodHostHTTPDo, rawReq)
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}

	resp, errDecode := decodeRPCEnvelope[pluginapi.HTTPResponse](rawResp)
	if errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if resp.StatusCode != http.StatusOK || string(resp.Body) != `{"ok":true}` {
		t.Fatalf("response = %#v, want status 200 body", resp)
	}
	if resp.Headers.Get("X-Test") != "ok" {
		t.Fatalf("X-Test = %q, want ok", resp.Headers.Get("X-Test"))
	}
}

func TestHostHTTPDoCallbackRestoresRegisteredRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	host := New()
	host.mu.Lock()
	host.runtimeConfig = &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}
	host.mu.Unlock()
	callbackID, closeCallback := host.openCallbackContext(ctx)
	defer closeCallback()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Err() != nil {
			t.Fatalf("request context error = %v", r.Context().Err())
		}
		w.Header().Set("X-Upstream", "ok")
		_, _ = w.Write([]byte("upstream-body"))
	}))
	defer server.Close()

	rawReq, errMarshal := json.Marshal(rpcHostHTTPRequest{
		HostCallbackID: callbackID,
		Method:         http.MethodPost,
		URL:            server.URL,
		Body:           []byte(`{"request":true}`),
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	if _, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostHTTPDo, rawReq); errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}

	rawAPIRequest, okRequest := ginCtx.Get("API_REQUEST")
	if !okRequest {
		t.Fatal("API_REQUEST was not captured on the original Gin context")
	}
	apiRequest, _ := rawAPIRequest.([]byte)
	if !bytes.Contains(apiRequest, []byte("=== API REQUEST 1 ===")) || !bytes.Contains(apiRequest, []byte(`{"request":true}`)) {
		t.Fatalf("API_REQUEST = %q, want upstream request details", apiRequest)
	}

	rawAPIResponse, okResponse := ginCtx.Get("API_RESPONSE")
	if !okResponse {
		t.Fatal("API_RESPONSE was not captured on the original Gin context")
	}
	apiResponse, _ := rawAPIResponse.([]byte)
	if !bytes.Contains(apiResponse, []byte("=== API RESPONSE 1 ===")) || !bytes.Contains(apiResponse, []byte("upstream-body")) {
		t.Fatalf("API_RESPONSE = %q, want upstream response details", apiResponse)
	}
}

func TestHostHTTPDoStreamCallbackReturnsBeforeUpstreamCompletes(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("first"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-release
		_, _ = w.Write([]byte("second"))
	}))
	defer server.Close()
	defer close(release)

	rawReq, errMarshal := json.Marshal(pluginapi.HTTPRequest{
		Method: http.MethodGet,
		URL:    server.URL,
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}

	type callResult struct {
		raw []byte
		err error
	}
	done := make(chan callResult, 1)
	host := New()
	go func() {
		rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostHTTPDoStream, rawReq)
		done <- callResult{raw: rawResp, err: errCall}
	}()

	var result callResult
	select {
	case result = <-done:
	case <-time.After(time.Second):
		t.Fatal("host.http.do_stream waited for the whole upstream response")
	}
	if result.err != nil {
		t.Fatalf("callFromPlugin() error = %v", result.err)
	}

	resp, errDecode := decodeRPCEnvelope[rpcHostHTTPStreamResponse](result.raw)
	if errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if resp.StreamID == "" {
		t.Fatalf("stream id is empty: %#v", resp)
	}
	readReq, errMarshal := json.Marshal(rpcHostHTTPStreamReadRequest{StreamID: resp.StreamID})
	if errMarshal != nil {
		t.Fatalf("marshal read request: %v", errMarshal)
	}
	rawRead, errRead := host.callFromPlugin(context.Background(), pluginabi.MethodHostHTTPStreamRead, readReq)
	if errRead != nil {
		t.Fatalf("read callback error = %v", errRead)
	}
	chunk, errDecode := decodeRPCEnvelope[rpcHostHTTPStreamReadResponse](rawRead)
	if errDecode != nil {
		t.Fatalf("decode read response: %v", errDecode)
	}
	if string(chunk.Payload) != "first" || chunk.Done || chunk.Error != "" {
		t.Fatalf("read chunk = %#v, want first payload", chunk)
	}

	closeReq, errMarshal := json.Marshal(rpcHostHTTPStreamCloseRequest{StreamID: resp.StreamID})
	if errMarshal != nil {
		t.Fatalf("marshal close request: %v", errMarshal)
	}
	if _, errClose := host.callFromPlugin(context.Background(), pluginabi.MethodHostHTTPStreamClose, closeReq); errClose != nil {
		t.Fatalf("close callback error = %v", errClose)
	}
}

func TestHostHTTPStreamReadIsPluginScoped(t *testing.T) {
	host := New()
	chunks := make(chan pluginapi.HTTPStreamChunk, 1)
	chunks <- pluginapi.HTTPStreamChunk{Payload: []byte("plugin-a data")}
	streamID := host.httpStreams.open("plugin-a", nil, chunks, nil, nil)
	request, errMarshal := json.Marshal(rpcHostHTTPStreamReadRequest{StreamID: streamID})
	if errMarshal != nil {
		t.Fatalf("marshal stream read request: %v", errMarshal)
	}

	pluginBContext := withHostCallbackPluginID(context.Background(), "plugin-b")
	if _, errRead := host.callFromPlugin(pluginBContext, pluginabi.MethodHostHTTPStreamRead, request); errRead == nil {
		t.Fatal("plugin-b read plugin-a's HTTP stream")
	}

	pluginAContext := withHostCallbackPluginID(context.Background(), "plugin-a")
	rawResponse, errRead := host.callFromPlugin(pluginAContext, pluginabi.MethodHostHTTPStreamRead, request)
	if errRead != nil {
		t.Fatalf("plugin-a stream read error = %v", errRead)
	}
	response, errDecode := decodeRPCEnvelope[rpcHostHTTPStreamReadResponse](rawResponse)
	if errDecode != nil {
		t.Fatalf("decode stream read response: %v", errDecode)
	}
	if string(response.Payload) != "plugin-a data" {
		t.Fatalf("plugin-a stream payload = %q, want plugin-a data", response.Payload)
	}
}

func TestHostHTTPStreamsAreCallbackInstanceScoped(t *testing.T) {
	host := New()
	owner := &hostCallbackInstance{}
	otherOwner := &hostCallbackInstance{}
	chunks := make(chan pluginapi.HTTPStreamChunk, 1)
	chunks <- pluginapi.HTTPStreamChunk{Payload: []byte("owner data")}
	streamID := host.httpStreams.open("plugin", owner, chunks, nil, nil)
	readRequest, errMarshal := json.Marshal(rpcHostHTTPStreamReadRequest{StreamID: streamID})
	if errMarshal != nil {
		t.Fatalf("marshal stream read request: %v", errMarshal)
	}
	otherContext := withHostCallbackIdentity(context.Background(), "plugin", otherOwner)
	if _, errRead := host.callFromPlugin(otherContext, pluginabi.MethodHostHTTPStreamRead, readRequest); errRead == nil {
		t.Fatal("new callback instance read an old instance's HTTP stream")
	}
	closeRequest, errMarshal := json.Marshal(rpcHostHTTPStreamCloseRequest{StreamID: streamID})
	if errMarshal != nil {
		t.Fatalf("marshal stream close request: %v", errMarshal)
	}
	if _, errClose := host.callFromPlugin(otherContext, pluginabi.MethodHostHTTPStreamClose, closeRequest); errClose != nil {
		t.Fatalf("foreign instance close callback error = %v", errClose)
	}

	ownerContext := withHostCallbackIdentity(context.Background(), "plugin", owner)
	rawResponse, errRead := host.callFromPlugin(ownerContext, pluginabi.MethodHostHTTPStreamRead, readRequest)
	if errRead != nil {
		t.Fatalf("owner stream read error = %v", errRead)
	}
	response, errDecode := decodeRPCEnvelope[rpcHostHTTPStreamReadResponse](rawResponse)
	if errDecode != nil {
		t.Fatalf("decode stream read response: %v", errDecode)
	}
	if string(response.Payload) != "owner data" {
		t.Fatalf("owner stream payload = %q, want owner data", response.Payload)
	}
	host.httpStreams.close("plugin", owner, streamID)
}

func TestHostHTTPStreamCloseIsPluginScoped(t *testing.T) {
	host := New()
	chunks := make(chan pluginapi.HTTPStreamChunk, 1)
	chunks <- pluginapi.HTTPStreamChunk{Payload: []byte("plugin-a data")}
	streamID := host.httpStreams.open("plugin-a", nil, chunks, nil, nil)
	request, errMarshal := json.Marshal(rpcHostHTTPStreamCloseRequest{StreamID: streamID})
	if errMarshal != nil {
		t.Fatalf("marshal stream close request: %v", errMarshal)
	}

	pluginBContext := withHostCallbackPluginID(context.Background(), "plugin-b")
	if _, errClose := host.callFromPlugin(pluginBContext, pluginabi.MethodHostHTTPStreamClose, request); errClose != nil {
		t.Fatalf("plugin-b stream close callback error = %v", errClose)
	}

	pluginAContext := withHostCallbackPluginID(context.Background(), "plugin-a")
	readRequest, errMarshal := json.Marshal(rpcHostHTTPStreamReadRequest{StreamID: streamID})
	if errMarshal != nil {
		t.Fatalf("marshal stream read request: %v", errMarshal)
	}
	rawResponse, errRead := host.callFromPlugin(pluginAContext, pluginabi.MethodHostHTTPStreamRead, readRequest)
	if errRead != nil {
		t.Fatalf("plugin-a stream read after foreign close error = %v", errRead)
	}
	response, errDecode := decodeRPCEnvelope[rpcHostHTTPStreamReadResponse](rawResponse)
	if errDecode != nil {
		t.Fatalf("decode stream read response: %v", errDecode)
	}
	if string(response.Payload) != "plugin-a data" {
		t.Fatalf("plugin-a stream payload = %q, want plugin-a data", response.Payload)
	}
	host.httpStreams.close("plugin-a", nil, streamID)
}

func openHostHTTPOperation(t *testing.T, host *Host, ctx context.Context, callbackID string) string {
	t.Helper()
	rawRequest, errMarshal := json.Marshal(map[string]string{"host_callback_id": callbackID})
	if errMarshal != nil {
		t.Fatalf("marshal operation open request: %v", errMarshal)
	}
	rawResponse, errOpen := host.callFromPlugin(ctx, pluginabi.MethodHostHTTPOperationOpen, rawRequest)
	if errOpen != nil {
		t.Fatalf("host.http.operation_open error = %v", errOpen)
	}
	opened, errDecode := decodeRPCEnvelope[rpcHostHTTPOperationOpenResponse](rawResponse)
	if errDecode != nil {
		t.Fatalf("decode operation open response: %v", errDecode)
	}
	if opened.OperationID == "" {
		t.Fatal("operation_id is empty")
	}
	return opened.OperationID
}

func TestHostHTTPCallbacksCanBeCanceledBeforeResponse(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		method string
	}{
		{name: "do", method: pluginabi.MethodHostHTTPDo},
		{name: "do_stream", method: pluginabi.MethodHostHTTPDoStream},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			requestStarted := make(chan struct{})
			requestCanceled := make(chan struct{})
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(requestStarted)
				select {
				case <-r.Context().Done():
					close(requestCanceled)
				case <-release:
				}
				_, _ = w.Write([]byte("response"))
			}))
			defer server.Close()

			host := New()
			operationID := openHostHTTPOperation(t, host, context.Background(), "")
			rawRequest, errMarshal := json.Marshal(map[string]any{
				"operation_id": operationID,
				"request": map[string]any{
					"method": http.MethodGet,
					"url":    server.URL,
				},
			})
			if errMarshal != nil {
				t.Fatalf("marshal request: %v", errMarshal)
			}
			type callResult struct {
				raw []byte
				err error
			}
			done := make(chan callResult, 1)
			var result callResult
			var resultReceived bool
			defer func() {
				close(release)
				if !resultReceived {
					select {
					case result = <-done:
						resultReceived = true
					case <-time.After(2 * time.Second):
					}
				}
				if resultReceived && result.err == nil {
					streamResp, errDecode := decodeRPCEnvelope[rpcHostHTTPStreamResponse](result.raw)
					if errDecode == nil && streamResp.StreamID != "" {
						host.httpStreams.close("", nil, streamResp.StreamID)
					}
				}
			}()

			go func() {
				rawResp, errCall := host.callFromPlugin(context.Background(), testCase.method, rawRequest)
				done <- callResult{raw: rawResp, err: errCall}
			}()
			select {
			case <-requestStarted:
			case <-time.After(2 * time.Second):
				t.Fatal("upstream request did not start")
			}

			rawCancel, errMarshalCancel := json.Marshal(map[string]string{"operation_id": operationID})
			if errMarshalCancel != nil {
				t.Fatalf("marshal cancel request: %v", errMarshalCancel)
			}
			if _, errCancel := host.callFromPlugin(context.Background(), pluginabi.MethodHostHTTPCancel, rawCancel); errCancel != nil {
				t.Fatalf("host.http.cancel error = %v", errCancel)
			}
			select {
			case <-requestCanceled:
			case <-time.After(2 * time.Second):
				t.Fatal("upstream request context was not canceled")
			}
			select {
			case result = <-done:
				resultReceived = true
			case <-time.After(2 * time.Second):
				t.Fatal("HTTP callback did not return after cancellation")
			}
			if !errors.Is(result.err, context.Canceled) {
				t.Fatalf("HTTP callback error = %v, want context.Canceled", result.err)
			}
		})
	}
}

func TestHostHTTPDoWithoutOperationIDIsCanceledWithPluginInstance(t *testing.T) {
	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		select {
		case <-r.Context().Done():
			close(requestCanceled)
		case <-release:
		}
		_, _ = w.Write([]byte("response"))
	}))
	defer server.Close()
	defer close(release)

	host := New()
	instance := &hostCallbackInstance{}
	callerContext := withHostCallbackIdentity(context.Background(), "plugin", instance)
	rawRequest, errMarshal := json.Marshal(map[string]string{
		"method": http.MethodGet,
		"url":    server.URL,
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	done := make(chan error, 1)
	go func() {
		_, errCall := host.callFromPlugin(callerContext, pluginabi.MethodHostHTTPDo, rawRequest)
		done <- errCall
	}()
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream request did not start")
	}

	host.closeHostHTTPCallbackInstance("plugin", instance)
	select {
	case <-requestCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("plugin instance shutdown did not cancel the upstream request")
	}
	select {
	case errCall := <-done:
		if !errors.Is(errCall, context.Canceled) {
			t.Fatalf("host.http.do error = %v, want context.Canceled", errCall)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("host.http.do did not return after plugin shutdown")
	}
}

func TestHostHTTPDoCanBeCanceledWhileReadingBody(t *testing.T) {
	headersSent := make(chan struct{})
	requestCanceled := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(headersSent)
		select {
		case <-r.Context().Done():
			close(requestCanceled)
		case <-release:
		}
		_, _ = w.Write([]byte("last"))
	}))
	defer server.Close()
	defer close(release)

	host := New()
	operationID := openHostHTTPOperation(t, host, context.Background(), "")
	rawRequest, errMarshal := json.Marshal(map[string]any{
		"operation_id": operationID,
		"request": map[string]any{
			"method": http.MethodGet,
			"url":    server.URL,
		},
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	type callResult struct {
		err error
	}
	done := make(chan callResult, 1)
	go func() {
		_, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostHTTPDo, rawRequest)
		done <- callResult{err: errCall}
	}()
	select {
	case <-headersSent:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream response headers were not sent")
	}

	rawCancel, errMarshalCancel := json.Marshal(map[string]string{"operation_id": operationID})
	if errMarshalCancel != nil {
		t.Fatalf("marshal cancel request: %v", errMarshalCancel)
	}
	if _, errCancel := host.callFromPlugin(context.Background(), pluginabi.MethodHostHTTPCancel, rawCancel); errCancel != nil {
		t.Fatalf("host.http.cancel error = %v", errCancel)
	}
	select {
	case <-requestCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream request context was not canceled while reading the body")
	}
	select {
	case result := <-done:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("host.http.do error = %v, want context.Canceled", result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("host.http.do did not return after body-read cancellation")
	}
}

func TestHostHTTPCancelBeforeDoPreventsRequest(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestStarted <- struct{}{}
		_, _ = w.Write([]byte("unexpected response"))
	}))
	defer server.Close()

	host := New()
	operationID := openHostHTTPOperation(t, host, context.Background(), "")
	rawCancel, errMarshal := json.Marshal(map[string]string{"operation_id": operationID})
	if errMarshal != nil {
		t.Fatalf("marshal cancel request: %v", errMarshal)
	}
	if _, errCancel := host.callFromPlugin(context.Background(), pluginabi.MethodHostHTTPCancel, rawCancel); errCancel != nil {
		t.Fatalf("host.http.cancel error = %v", errCancel)
	}

	rawRequest, errMarshalRequest := json.Marshal(map[string]any{
		"operation_id": operationID,
		"request": map[string]any{
			"method": http.MethodGet,
			"url":    server.URL,
		},
	})
	if errMarshalRequest != nil {
		t.Fatalf("marshal request: %v", errMarshalRequest)
	}
	if _, errDo := host.callFromPlugin(context.Background(), pluginabi.MethodHostHTTPDo, rawRequest); errDo == nil {
		t.Fatal("host.http.do succeeded after its operation was canceled")
	}
	select {
	case <-requestStarted:
		t.Fatal("upstream request started after operation cancellation")
	default:
	}
}

func TestHostHTTPStreamOperationCanBeCanceledAfterHeaders(t *testing.T) {
	headersSent := make(chan struct{})
	requestCanceled := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(headersSent)
		select {
		case <-r.Context().Done():
			close(requestCanceled)
		case <-release:
		}
		_, _ = w.Write([]byte("last"))
	}))
	defer server.Close()

	host := New()
	var streamID string
	defer func() {
		close(release)
		if streamID != "" {
			host.httpStreams.close("", nil, streamID)
		}
	}()
	operationID := openHostHTTPOperation(t, host, context.Background(), "")
	rawRequest, errMarshal := json.Marshal(map[string]any{
		"operation_id": operationID,
		"request": map[string]any{
			"method": http.MethodGet,
			"url":    server.URL,
		},
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	rawResponse, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostHTTPDoStream, rawRequest)
	if errCall != nil {
		t.Fatalf("host.http.do_stream error = %v", errCall)
	}
	streamResp, errDecode := decodeRPCEnvelope[rpcHostHTTPStreamResponse](rawResponse)
	if errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	streamID = streamResp.StreamID
	if streamID == "" {
		t.Fatal("stream_id is empty")
	}
	select {
	case <-headersSent:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream response headers were not sent")
	}
	rawCancel, errMarshalCancel := json.Marshal(map[string]string{"operation_id": operationID})
	if errMarshalCancel != nil {
		t.Fatalf("marshal cancel request: %v", errMarshalCancel)
	}
	if _, errCancel := host.callFromPlugin(context.Background(), pluginabi.MethodHostHTTPCancel, rawCancel); errCancel != nil {
		t.Fatalf("host.http.cancel error = %v", errCancel)
	}
	select {
	case <-requestCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream stream context was not canceled")
	}
	host.httpStreams.mu.Lock()
	_, streamOpen := host.httpStreams.streams[hostHTTPStreamKey{streamID: streamID}]
	host.httpStreams.mu.Unlock()
	if streamOpen {
		t.Fatal("canceled HTTP stream remains registered")
	}
}

func TestHostHTTPDoRejectsInvalidOrForeignCallbackContext(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		callerID    string
		closeBefore bool
	}{
		{name: "closed callback ID", callerID: "plugin-a", closeBefore: true},
		{name: "foreign callback ID", callerID: "plugin-b"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			requestStarted := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestStarted <- struct{}{}
				_, _ = w.Write([]byte("unexpected response"))
			}))
			defer server.Close()

			host := New()
			callbackID, closeCallback := host.openCallbackContextForPlugin(context.Background(), "plugin-a")
			defer closeCallback()
			if testCase.closeBefore {
				closeCallback()
			}
			callerContext := withHostCallbackPluginID(context.Background(), testCase.callerID)
			rawRequest, errMarshal := json.Marshal(map[string]string{
				"host_callback_id": callbackID,
				"method":           http.MethodGet,
				"url":              server.URL,
			})
			if errMarshal != nil {
				t.Fatalf("marshal request: %v", errMarshal)
			}
			if _, errDo := host.callFromPlugin(callerContext, pluginabi.MethodHostHTTPDo, rawRequest); errDo == nil {
				t.Fatal("host.http.do accepted an invalid or foreign callback ID")
			}
			select {
			case <-requestStarted:
				t.Fatal("upstream request started with an invalid or foreign callback ID")
			default:
			}
		})
	}
}

func TestHostHTTPOperationRejectsForeignCallbackContext(t *testing.T) {
	host := New()
	callbackID, closeCallback := host.openCallbackContextForPlugin(context.Background(), "plugin-a")
	defer closeCallback()
	callerContextB := withHostCallbackPluginID(context.Background(), "plugin-b")
	rawRequest, errMarshal := json.Marshal(map[string]string{"host_callback_id": callbackID})
	if errMarshal != nil {
		t.Fatalf("marshal operation open request: %v", errMarshal)
	}
	if _, errOpen := host.callFromPlugin(callerContextB, pluginabi.MethodHostHTTPOperationOpen, rawRequest); errOpen == nil {
		t.Fatal("plugin-b opened an HTTP operation in plugin-a's callback context")
	}
}

func TestHostHTTPOperationRejectsClosedCallbackContext(t *testing.T) {
	host := New()
	callbackID, closeCallback := host.openCallbackContextForPlugin(context.Background(), "plugin-a")
	closeCallback()
	callerContext := withHostCallbackPluginID(context.Background(), "plugin-a")
	rawRequest, errMarshal := json.Marshal(map[string]string{"host_callback_id": callbackID})
	if errMarshal != nil {
		t.Fatalf("marshal operation open request: %v", errMarshal)
	}
	if _, errOpen := host.callFromPlugin(callerContext, pluginabi.MethodHostHTTPOperationOpen, rawRequest); errOpen == nil {
		t.Fatal("opened an HTTP operation with a closed callback context")
	}
}

func TestHostHTTPOperationRejectsUnboundCallbackContext(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestStarted <- struct{}{}
		_, _ = w.Write([]byte("unexpected response"))
	}))
	defer server.Close()

	host := New()
	callerContext := withHostCallbackPluginID(context.Background(), "plugin")
	operationID := openHostHTTPOperation(t, host, callerContext, "")
	callbackID, closeCallback := host.openCallbackContextForPlugin(context.Background(), "plugin")
	defer closeCallback()
	rawRequest, errMarshal := json.Marshal(map[string]any{
		"host_callback_id": callbackID,
		"operation_id":     operationID,
		"request": map[string]any{
			"method": http.MethodGet,
			"url":    server.URL,
		},
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	if _, errDo := host.callFromPlugin(callerContext, pluginabi.MethodHostHTTPDo, rawRequest); errDo == nil {
		t.Fatal("host.http.do accepted a callback context not bound at operation open")
	}
	select {
	case <-requestStarted:
		t.Fatal("upstream request started with an unbound callback context")
	default:
	}
}

func TestHostHTTPOperationRequiresMatchingCallbackID(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestStarted <- struct{}{}
		_, _ = w.Write([]byte("response"))
	}))
	defer server.Close()

	host := New()
	callerContext := withHostCallbackPluginID(context.Background(), "plugin")
	callbackID, closeCallback := host.openCallbackContextForPlugin(context.Background(), "plugin")
	defer closeCallback()
	otherCallbackID, closeOtherCallback := host.openCallbackContextForPlugin(context.Background(), "plugin")
	defer closeOtherCallback()
	operationID := openHostHTTPOperation(t, host, callerContext, callbackID)

	for _, mismatchedCallbackID := range []string{"", otherCallbackID} {
		rawRequest, errMarshal := json.Marshal(map[string]any{
			"host_callback_id": mismatchedCallbackID,
			"operation_id":     operationID,
			"method":           http.MethodGet,
			"url":              server.URL,
		})
		if errMarshal != nil {
			t.Fatalf("marshal mismatched request: %v", errMarshal)
		}
		if _, errDo := host.callFromPlugin(callerContext, pluginabi.MethodHostHTTPDo, rawRequest); errDo == nil {
			t.Fatalf("host.http.do accepted mismatched callback ID %q", mismatchedCallbackID)
		}
		select {
		case <-requestStarted:
			t.Fatalf("upstream request started with mismatched callback ID %q", mismatchedCallbackID)
		default:
		}
	}

	rawRequest, errMarshal := json.Marshal(map[string]any{
		"host_callback_id": callbackID,
		"operation_id":     operationID,
		"method":           http.MethodGet,
		"url":              server.URL,
	})
	if errMarshal != nil {
		t.Fatalf("marshal matching request: %v", errMarshal)
	}
	if _, errDo := host.callFromPlugin(callerContext, pluginabi.MethodHostHTTPDo, rawRequest); errDo != nil {
		t.Fatalf("host.http.do with matching callback ID error = %v", errDo)
	}
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("upstream request did not start with matching callback ID")
	}
}

func TestHostHTTPOperationClosesWithCallbackScope(t *testing.T) {
	host := New()
	callbackID, closeCallback := host.openCallbackContextForPlugin(context.Background(), "plugin-a")
	callerContext := withHostCallbackPluginID(context.Background(), "plugin-a")
	operationID := openHostHTTPOperation(t, host, callerContext, callbackID)
	closeCallback()

	host.httpOperations.mu.Lock()
	_, operationOpen := host.httpOperations.operations[hostHTTPOperationKey{pluginID: "plugin-a", operationID: operationID}]
	host.httpOperations.mu.Unlock()
	if operationOpen {
		t.Fatal("HTTP operation remained open after its callback scope closed")
	}
}

func TestHostStreamCallbacksEmitAndClose(t *testing.T) {
	host := New()
	streamID, chunks, cleanup := host.streams.open(context.Background())
	defer cleanup()

	emitReq, errMarshal := json.Marshal(rpcStreamEmitRequest{StreamID: streamID, Payload: []byte("chunk")})
	if errMarshal != nil {
		t.Fatalf("marshal emit request: %v", errMarshal)
	}
	if _, errEmit := host.callFromPlugin(context.Background(), pluginabi.MethodHostStreamEmit, emitReq); errEmit != nil {
		t.Fatalf("emit callback error = %v", errEmit)
	}

	closeReq, errMarshal := json.Marshal(rpcStreamCloseRequest{StreamID: streamID})
	if errMarshal != nil {
		t.Fatalf("marshal close request: %v", errMarshal)
	}
	if _, errClose := host.callFromPlugin(context.Background(), pluginabi.MethodHostStreamClose, closeReq); errClose != nil {
		t.Fatalf("close callback error = %v", errClose)
	}

	chunk, ok := <-chunks
	if !ok {
		t.Fatalf("stream closed before chunk")
	}
	if string(chunk.Payload) != "chunk" || chunk.Err != nil {
		t.Fatalf("chunk = %#v, want payload chunk", chunk)
	}
	if _, ok = <-chunks; ok {
		t.Fatalf("stream remains open after close")
	}
}

func TestHostModelExecuteRejectsInvalidProxyURL(t *testing.T) {
	host := New()
	called := false
	host.SetModelExecutor(&fakeHostModelExecutor{
		executeModel: func(ctx context.Context, req handlers.ModelExecutionRequest) (handlers.ModelExecutionResponse, *interfaces.ErrorMessage) {
			called = true
			return handlers.ModelExecutionResponse{StatusCode: http.StatusOK}, nil
		},
		executeModelStream: func(ctx context.Context, req handlers.ModelExecutionRequest) (handlers.ModelExecutionStream, *interfaces.ErrorMessage) {
			called = true
			chunks := make(chan handlers.ModelExecutionChunk)
			close(chunks)
			return handlers.ModelExecutionStream{StatusCode: http.StatusOK, Chunks: chunks}, nil
		},
	})
	for _, proxyURL := range []string{"direct", "socks4://127.0.0.1:1080", "http://", "http://:8080", "http://proxy.example:99999", "not a url"} {
		for _, method := range []string{pluginabi.MethodHostModelExecute, pluginabi.MethodHostModelExecuteStream} {
			stream := method == pluginabi.MethodHostModelExecuteStream
			rawReq, errMarshal := json.Marshal(rpcHostModelExecutionRequest{
				HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
					EntryProtocol: "openai",
					ExitProtocol:  "openai",
					Model:         "model-1",
					Stream:        stream,
					ProxyURL:      proxyURL,
					Body:          []byte(`{"request":true}`),
				},
			})
			if errMarshal != nil {
				t.Fatalf("marshal request: %v", errMarshal)
			}
			_, errCall := host.callFromPlugin(context.Background(), method, rawReq)
			if errCall == nil {
				t.Fatalf("%s proxy %q error = nil, want HTTP 400", method, proxyURL)
			}
			if clienterror.HTTPStatusFromError(errCall) != http.StatusBadRequest {
				t.Fatalf("%s proxy %q status = %d, want 400: %v", method, proxyURL, clienterror.HTTPStatusFromError(errCall), errCall)
			}
		}
	}
	if called {
		t.Fatal("model executor ran for an invalid proxy_url")
	}
}

func TestHostModelExecuteForwardsProxyURL(t *testing.T) {
	host := New()
	var got handlers.ModelExecutionRequest
	host.SetModelExecutor(&fakeHostModelExecutor{
		executeModel: func(ctx context.Context, req handlers.ModelExecutionRequest) (handlers.ModelExecutionResponse, *interfaces.ErrorMessage) {
			got = req
			return handlers.ModelExecutionResponse{StatusCode: http.StatusOK, Body: []byte(`{"ok":true}`)}, nil
		},
		executeModelStream: func(ctx context.Context, req handlers.ModelExecutionRequest) (handlers.ModelExecutionStream, *interfaces.ErrorMessage) {
			got = req
			chunks := make(chan handlers.ModelExecutionChunk)
			close(chunks)
			return handlers.ModelExecutionStream{StatusCode: http.StatusOK, Chunks: chunks}, nil
		},
	})
	for _, method := range []string{pluginabi.MethodHostModelExecute, pluginabi.MethodHostModelExecuteStream} {
		stream := method == pluginabi.MethodHostModelExecuteStream
		rawReq, errMarshal := json.Marshal(rpcHostModelExecutionRequest{
			HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
				EntryProtocol: "openai",
				ExitProtocol:  "openai",
				Model:         "model-1",
				Stream:        stream,
				ProxyURL:      "socks5h://user:pass@127.0.0.1:1080",
				Body:          []byte(`{"request":true}`),
			},
		})
		if errMarshal != nil {
			t.Fatalf("marshal request: %v", errMarshal)
		}
		if _, errCall := host.callFromPlugin(context.Background(), method, rawReq); errCall != nil {
			t.Fatalf("%s error = %v", method, errCall)
		}
		if got.ProxyURL != "socks5h://user:pass@127.0.0.1:1080" {
			t.Fatalf("%s proxy_url = %q", method, got.ProxyURL)
		}
	}
}

func TestHostModelExecuteCallback(t *testing.T) {
	host := New()
	var got handlers.ModelExecutionRequest
	host.SetModelExecutor(&fakeHostModelExecutor{
		executeModel: func(ctx context.Context, req handlers.ModelExecutionRequest) (handlers.ModelExecutionResponse, *interfaces.ErrorMessage) {
			got = req
			return handlers.ModelExecutionResponse{
				StatusCode: http.StatusAccepted,
				Headers:    http.Header{"X-Model": []string{"ok"}},
				Body:       []byte(`{"response":true}`),
			}, nil
		},
	})

	rawReq, errMarshal := json.Marshal(rpcHostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: "openai",
			ExitProtocol:  "claude",
			Model:         "model-1",
			Body:          []byte(`{"request":true}`),
			Headers:       http.Header{"X-Request": []string{"yes"}},
			Query:         url.Values{"alt": []string{"sse"}},
			Alt:           "raw",
		},
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostModelExecute, rawReq)
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}

	resp, errDecode := decodeRPCEnvelope[pluginapi.HostModelExecutionResponse](rawResp)
	if errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if resp.StatusCode != http.StatusAccepted || string(resp.Body) != `{"response":true}` {
		t.Fatalf("response = %#v, want accepted body", resp)
	}
	if resp.Headers.Get("X-Model") != "ok" {
		t.Fatalf("X-Model = %q, want ok", resp.Headers.Get("X-Model"))
	}
	if got.EntryProtocol != "openai" || got.ExitProtocol != "claude" || got.Model != "model-1" || got.Stream {
		t.Fatalf("request protocols/model/stream = %#v", got)
	}
	if string(got.Body) != `{"request":true}` {
		t.Fatalf("request body = %q, want original body", got.Body)
	}
	if got.Headers.Get("X-Request") != "yes" {
		t.Fatalf("request header = %q, want yes", got.Headers.Get("X-Request"))
	}
	if got.Query.Get("alt") != "sse" {
		t.Fatalf("query alt = %q, want sse", got.Query.Get("alt"))
	}
	if got.Alt != "raw" {
		t.Fatalf("alt = %q, want raw", got.Alt)
	}
}

func TestHostModelExecuteCallbackCarriesCallerPluginSkipID(t *testing.T) {
	host := New()
	var got handlers.ModelExecutionRequest
	host.SetModelExecutor(&fakeHostModelExecutor{
		executeModel: func(ctx context.Context, req handlers.ModelExecutionRequest) (handlers.ModelExecutionResponse, *interfaces.ErrorMessage) {
			got = req
			return handlers.ModelExecutionResponse{StatusCode: http.StatusOK, Body: []byte(`{"ok":true}`)}, nil
		},
	})
	callbackID, closeCallback := host.openCallbackContextForPlugin(context.Background(), "origin-plugin")
	defer closeCallback()

	rawReq, errMarshal := json.Marshal(rpcHostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: "openai",
			ExitProtocol:  "openai",
			Model:         "model-1",
			Body:          []byte(`{"request":true}`),
		},
		HostCallbackID: callbackID,
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	if _, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostModelExecute, rawReq); errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}
	if got.SkipInterceptorPluginID != "origin-plugin" {
		t.Fatalf("SkipInterceptorPluginID = %q, want origin-plugin", got.SkipInterceptorPluginID)
	}
	if got.SkipRouterPluginID != "origin-plugin" {
		t.Fatalf("SkipRouterPluginID = %q, want origin-plugin", got.SkipRouterPluginID)
	}
}

func TestHostModelStreamClosesWithCallbackScope(t *testing.T) {
	host := New()
	ctxSeen := make(chan context.Context, 1)
	host.SetModelExecutor(&fakeHostModelExecutor{
		executeModelStream: func(ctx context.Context, req handlers.ModelExecutionRequest) (handlers.ModelExecutionStream, *interfaces.ErrorMessage) {
			ctxSeen <- ctx
			return handlers.ModelExecutionStream{
				StatusCode: http.StatusOK,
				Headers:    http.Header{"X-Stream": []string{"ok"}},
				Chunks:     make(chan handlers.ModelExecutionChunk),
			}, nil
		},
	})
	callbackID, closeCallback := host.openCallbackContext(context.Background())
	defer closeCallback()

	rawReq, errMarshal := json.Marshal(rpcHostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: "openai",
			ExitProtocol:  "openai",
			Model:         "model-1",
			Stream:        true,
			Body:          []byte(`{"stream":true}`),
		},
		HostCallbackID: callbackID,
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostModelExecuteStream, rawReq)
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}
	resp, errDecode := decodeRPCEnvelope[pluginapi.HostModelStreamResponse](rawResp)
	if errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if resp.StreamID == "" {
		t.Fatalf("stream id is empty: %#v", resp)
	}

	var streamCtx context.Context
	select {
	case streamCtx = <-ctxSeen:
	case <-time.After(time.Second):
		t.Fatal("model executor was not called")
	}
	closeCallback()
	select {
	case <-streamCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("stream context was not canceled after callback scope closed")
	}
}

func TestHostModelStreamReadAfterCallbackCloseReturnsDone(t *testing.T) {
	host := New()
	chunks := make(chan handlers.ModelExecutionChunk)
	host.SetModelExecutor(&fakeHostModelExecutor{
		executeModelStream: func(ctx context.Context, req handlers.ModelExecutionRequest) (handlers.ModelExecutionStream, *interfaces.ErrorMessage) {
			return handlers.ModelExecutionStream{
				StatusCode: http.StatusOK,
				Chunks:     chunks,
			}, nil
		},
	})
	callbackID, closeCallback := host.openCallbackContext(context.Background())

	rawReq, errMarshal := json.Marshal(rpcHostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: "openai",
			ExitProtocol:  "openai",
			Model:         "model-1",
			Stream:        true,
			Body:          []byte(`{"stream":true}`),
		},
		HostCallbackID: callbackID,
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostModelExecuteStream, rawReq)
	if errCall != nil {
		t.Fatalf("execute stream callback error = %v", errCall)
	}
	resp, errDecode := decodeRPCEnvelope[pluginapi.HostModelStreamResponse](rawResp)
	if errDecode != nil {
		t.Fatalf("decode stream response: %v", errDecode)
	}
	if resp.StreamID == "" {
		t.Fatalf("stream id is empty: %#v", resp)
	}

	closeCallback()
	readReq, errMarshal := json.Marshal(pluginapi.HostModelStreamReadRequest{StreamID: resp.StreamID})
	if errMarshal != nil {
		t.Fatalf("marshal read request: %v", errMarshal)
	}
	readDone := make(chan pluginapi.HostModelStreamReadResponse, 1)
	readErr := make(chan error, 1)
	go func() {
		rawRead, errRead := host.callFromPlugin(context.Background(), pluginabi.MethodHostModelStreamRead, readReq)
		if errRead != nil {
			readErr <- errRead
			return
		}
		doneResp, errDecodeRead := decodeRPCEnvelope[pluginapi.HostModelStreamReadResponse](rawRead)
		if errDecodeRead != nil {
			readErr <- errDecodeRead
			return
		}
		readDone <- doneResp
	}()
	select {
	case errRead := <-readErr:
		t.Fatalf("read after callback close error = %v", errRead)
	case doneResp := <-readDone:
		if !doneResp.Done || len(doneResp.Payload) != 0 || doneResp.Error != "" {
			t.Fatalf("read after callback close = %#v, want done without payload/error", doneResp)
		}
	case <-time.After(time.Second):
		t.Fatal("read after callback close blocked")
	}
}

func TestHostModelExecuteStreamStartupErrorCleansUp(t *testing.T) {
	host := New()
	ctxSeen := make(chan context.Context, 1)
	host.SetModelExecutor(&fakeHostModelExecutor{
		executeModelStream: func(ctx context.Context, req handlers.ModelExecutionRequest) (handlers.ModelExecutionStream, *interfaces.ErrorMessage) {
			ctxSeen <- ctx
			return handlers.ModelExecutionStream{}, &interfaces.ErrorMessage{
				StatusCode: http.StatusBadGateway,
			}
		},
	})

	rawReq, errMarshal := json.Marshal(pluginapi.HostModelExecutionRequest{
		EntryProtocol: "openai",
		ExitProtocol:  "openai",
		Model:         "model-1",
		Stream:        true,
		Body:          []byte(`{"stream":true}`),
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostModelExecuteStream, rawReq)
	if errCall == nil {
		t.Fatalf("execute stream callback error is nil, raw response = %q", rawResp)
	}
	if rawResp != nil {
		t.Fatalf("raw response = %q, want nil on startup error", rawResp)
	}
	if !strings.Contains(errCall.Error(), "status 502") {
		t.Fatalf("execute stream callback error = %v, want status 502", errCall)
	}

	var streamCtx context.Context
	select {
	case streamCtx = <-ctxSeen:
	case <-time.After(time.Second):
		t.Fatal("model executor was not called")
	}
	select {
	case <-streamCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("stream context was not canceled after startup error")
	}
	gotCount := hostModelStreamCountForTest(t, host)
	if gotCount != 0 {
		t.Fatalf("model stream count = %d, want 0", gotCount)
	}
}

func TestHostModelCallbacksValidateStreamMode(t *testing.T) {
	host := New()

	rawExecuteReq, errMarshal := json.Marshal(pluginapi.HostModelExecutionRequest{
		EntryProtocol: "openai",
		ExitProtocol:  "openai",
		Model:         "model-1",
		Stream:        true,
	})
	if errMarshal != nil {
		t.Fatalf("marshal execute request: %v", errMarshal)
	}
	_, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostModelExecute, rawExecuteReq)
	if errCall == nil || !strings.Contains(errCall.Error(), "host.model.execute requires stream=false") {
		t.Fatalf("execute callback error = %v, want stream=false validation error", errCall)
	}

	rawStreamReq, errMarshal := json.Marshal(pluginapi.HostModelExecutionRequest{
		EntryProtocol: "openai",
		ExitProtocol:  "openai",
		Model:         "model-1",
		Stream:        false,
	})
	if errMarshal != nil {
		t.Fatalf("marshal execute stream request: %v", errMarshal)
	}
	_, errCall = host.callFromPlugin(context.Background(), pluginabi.MethodHostModelExecuteStream, rawStreamReq)
	if errCall == nil || !strings.Contains(errCall.Error(), "host.model.execute_stream requires stream=true") {
		t.Fatalf("execute stream callback error = %v, want stream=true validation error", errCall)
	}
}

func TestHostModelCallbacksRequireExecutor(t *testing.T) {
	host := New()

	rawExecuteReq, errMarshal := json.Marshal(pluginapi.HostModelExecutionRequest{
		EntryProtocol: "openai",
		ExitProtocol:  "openai",
		Model:         "model-1",
	})
	if errMarshal != nil {
		t.Fatalf("marshal execute request: %v", errMarshal)
	}
	_, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostModelExecute, rawExecuteReq)
	if errCall == nil || !strings.Contains(errCall.Error(), "host model executor is unavailable") {
		t.Fatalf("execute callback error = %v, want unavailable executor error", errCall)
	}

	rawStreamReq, errMarshal := json.Marshal(pluginapi.HostModelExecutionRequest{
		EntryProtocol: "openai",
		ExitProtocol:  "openai",
		Model:         "model-1",
		Stream:        true,
	})
	if errMarshal != nil {
		t.Fatalf("marshal execute stream request: %v", errMarshal)
	}
	_, errCall = host.callFromPlugin(context.Background(), pluginabi.MethodHostModelExecuteStream, rawStreamReq)
	if errCall == nil || !strings.Contains(errCall.Error(), "host model executor is unavailable") {
		t.Fatalf("execute stream callback error = %v, want unavailable executor error", errCall)
	}
}

func TestHostModelStreamReadAndCloseValidateStreamID(t *testing.T) {
	host := New()

	rawReadReq, errMarshal := json.Marshal(pluginapi.HostModelStreamReadRequest{})
	if errMarshal != nil {
		t.Fatalf("marshal read request: %v", errMarshal)
	}
	_, errRead := host.callFromPlugin(context.Background(), pluginabi.MethodHostModelStreamRead, rawReadReq)
	if errRead == nil || !strings.Contains(errRead.Error(), "model stream id is required") {
		t.Fatalf("read callback error = %v, want required stream id error", errRead)
	}

	rawCloseReq, errMarshal := json.Marshal(pluginapi.HostModelStreamCloseRequest{})
	if errMarshal != nil {
		t.Fatalf("marshal close request: %v", errMarshal)
	}
	rawClose, errClose := host.callFromPlugin(context.Background(), pluginabi.MethodHostModelStreamClose, rawCloseReq)
	if errClose != nil {
		t.Fatalf("close callback error = %v", errClose)
	}
	_, errDecode := decodeRPCEnvelope[rpcEmptyResponse](rawClose)
	if errDecode != nil {
		t.Fatalf("decode close response: %v", errDecode)
	}
}

func TestHostModelStreamReadReturnsPayloadAndTerminalError(t *testing.T) {
	host := New()
	chunks := make(chan handlers.ModelExecutionChunk, 2)
	chunks <- handlers.ModelExecutionChunk{Payload: []byte("first")}
	chunks <- handlers.ModelExecutionChunk{Err: &handlers.ModelExecutionStreamError{
		StatusCode: http.StatusBadGateway,
		Message:    "terminal boom",
	}}
	host.SetModelExecutor(&fakeHostModelExecutor{
		executeModelStream: func(ctx context.Context, req handlers.ModelExecutionRequest) (handlers.ModelExecutionStream, *interfaces.ErrorMessage) {
			return handlers.ModelExecutionStream{
				StatusCode: http.StatusOK,
				Headers:    http.Header{"X-Stream": []string{"ok"}},
				Chunks:     chunks,
			}, nil
		},
	})

	streamID := openHostModelStreamForTest(t, host)
	readReq, errMarshal := json.Marshal(pluginapi.HostModelStreamReadRequest{StreamID: streamID})
	if errMarshal != nil {
		t.Fatalf("marshal read request: %v", errMarshal)
	}
	rawRead, errRead := host.callFromPlugin(context.Background(), pluginabi.MethodHostModelStreamRead, readReq)
	if errRead != nil {
		t.Fatalf("read callback error = %v", errRead)
	}
	first, errDecode := decodeRPCEnvelope[pluginapi.HostModelStreamReadResponse](rawRead)
	if errDecode != nil {
		t.Fatalf("decode read response: %v", errDecode)
	}
	if string(first.Payload) != "first" || first.Done || first.Error != "" {
		t.Fatalf("first read = %#v, want payload without done", first)
	}

	rawRead, errRead = host.callFromPlugin(context.Background(), pluginabi.MethodHostModelStreamRead, readReq)
	if errRead != nil {
		t.Fatalf("terminal read callback error = %v", errRead)
	}
	terminal, errDecode := decodeRPCEnvelope[pluginapi.HostModelStreamReadResponse](rawRead)
	if errDecode != nil {
		t.Fatalf("decode terminal response: %v", errDecode)
	}
	if !terminal.Done || terminal.Error != "terminal boom" || len(terminal.Payload) != 0 {
		t.Fatalf("terminal read = %#v, want done terminal error", terminal)
	}
}

func TestHostModelStreamExplicitCloseCancelsStream(t *testing.T) {
	host := New()
	ctxSeen := make(chan context.Context, 1)
	host.SetModelExecutor(&fakeHostModelExecutor{
		executeModelStream: func(ctx context.Context, req handlers.ModelExecutionRequest) (handlers.ModelExecutionStream, *interfaces.ErrorMessage) {
			ctxSeen <- ctx
			return handlers.ModelExecutionStream{
				StatusCode: http.StatusOK,
				Chunks:     make(chan handlers.ModelExecutionChunk),
			}, nil
		},
	})

	streamID := openHostModelStreamForTest(t, host)
	var streamCtx context.Context
	select {
	case streamCtx = <-ctxSeen:
	case <-time.After(time.Second):
		t.Fatal("model executor was not called")
	}
	closeReq, errMarshal := json.Marshal(pluginapi.HostModelStreamCloseRequest{StreamID: streamID})
	if errMarshal != nil {
		t.Fatalf("marshal close request: %v", errMarshal)
	}
	if _, errClose := host.callFromPlugin(context.Background(), pluginabi.MethodHostModelStreamClose, closeReq); errClose != nil {
		t.Fatalf("close callback error = %v", errClose)
	}
	select {
	case <-streamCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("stream context was not canceled after explicit close")
	}
	if _, errClose := host.callFromPlugin(context.Background(), pluginabi.MethodHostModelStreamClose, closeReq); errClose != nil {
		t.Fatalf("second close callback error = %v", errClose)
	}
}

func openHostModelStreamForTest(t *testing.T, host *Host) string {
	t.Helper()
	rawReq, errMarshal := json.Marshal(pluginapi.HostModelExecutionRequest{
		EntryProtocol: "openai",
		ExitProtocol:  "openai",
		Model:         "model-1",
		Stream:        true,
		Body:          []byte(`{"stream":true}`),
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostModelExecuteStream, rawReq)
	if errCall != nil {
		t.Fatalf("execute stream callback error = %v", errCall)
	}
	resp, errDecode := decodeRPCEnvelope[pluginapi.HostModelStreamResponse](rawResp)
	if errDecode != nil {
		t.Fatalf("decode stream response: %v", errDecode)
	}
	if resp.StreamID == "" {
		t.Fatalf("stream id is empty: %#v", resp)
	}
	return resp.StreamID
}

func hostModelStreamCountForTest(t *testing.T, host *Host) int {
	t.Helper()
	host.modelStreams.mu.Lock()
	defer host.modelStreams.mu.Unlock()
	return len(host.modelStreams.streams)
}

func TestHostLogCallbackRestoresRegisteredRequestContext(t *testing.T) {
	host := New()
	ctx := logging.WithRequestID(context.Background(), "request-123")
	callbackID, closeCallback := host.openCallbackContext(ctx)
	defer closeCallback()

	var out bytes.Buffer
	logger := log.StandardLogger()
	originalOut := logger.Out
	originalFormatter := logger.Formatter
	originalLevel := logger.Level
	log.SetOutput(&out)
	log.SetFormatter(&log.TextFormatter{
		DisableColors:    true,
		DisableTimestamp: true,
	})
	log.SetLevel(log.InfoLevel)
	defer func() {
		log.SetOutput(originalOut)
		log.SetFormatter(originalFormatter)
		log.SetLevel(originalLevel)
	}()

	rawReq, errMarshal := json.Marshal(rpcHostLogRequest{
		HostCallbackID: callbackID,
		Level:          "info",
		Message:        "plugin callback message",
	})
	if errMarshal != nil {
		t.Fatalf("marshal log request: %v", errMarshal)
	}
	if _, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostLog, rawReq); errCall != nil {
		t.Fatalf("log callback error = %v", errCall)
	}

	got := out.String()
	if !strings.Contains(got, "plugin callback message") || !strings.Contains(got, "request_id=request-123") {
		t.Fatalf("log output = %q, want message and request_id field", got)
	}
}

func TestDecodeHostHTTPRequestWithWireProfile(t *testing.T) {
	t.Parallel()

	profile := &pluginapi.HTTPWireProfile{
		HTTP1Only:              true,
		DisableAutoCompression: true,
		HeaderProfile:          []string{"host", "user-agent"},
	}

	// Flat rpcHostHTTPRequest
	flatReq := rpcHostHTTPRequest{
		HostCallbackID: "cb-1",
		Method:         http.MethodPost,
		URL:            "https://example.com/api",
		WireProfile:    profile,
	}
	rawFlat, errMarshal := json.Marshal(flatReq)
	if errMarshal != nil {
		t.Fatalf("marshal flat request: %v", errMarshal)
	}
	decoded, callbackID, errDecode := decodeHostHTTPRequestWithCallbackID(rawFlat)
	if errDecode != nil {
		t.Fatalf("decode flat request error = %v", errDecode)
	}
	if callbackID != "cb-1" {
		t.Fatalf("callbackID = %q, want cb-1", callbackID)
	}
	if decoded.WireProfile == nil || !decoded.WireProfile.HTTP1Only || !decoded.WireProfile.DisableAutoCompression {
		t.Fatalf("decoded wire profile mismatch: %#v", decoded.WireProfile)
	}
	if len(decoded.WireProfile.HeaderProfile) != 2 || decoded.WireProfile.HeaderProfile[0] != "host" {
		t.Fatalf("decoded header profile mismatch: %#v", decoded.WireProfile.HeaderProfile)
	}

	// Nested httpRequest
	nestedReq := rpcHostHTTPRequest{
		HostCallbackID: "cb-2",
		Request: &httpRequest{
			Method:      http.MethodGet,
			URL:         "https://example.com/stream",
			WireProfile: profile,
		},
	}
	rawNested, errMarshalNested := json.Marshal(nestedReq)
	if errMarshalNested != nil {
		t.Fatalf("marshal nested request: %v", errMarshalNested)
	}
	decodedNested, callbackIDNested, errDecodeNested := decodeHostHTTPRequestWithCallbackID(rawNested)
	if errDecodeNested != nil {
		t.Fatalf("decode nested request error = %v", errDecodeNested)
	}
	if callbackIDNested != "cb-2" {
		t.Fatalf("callbackID = %q, want cb-2", callbackIDNested)
	}
	if decodedNested.WireProfile == nil || !decodedNested.WireProfile.HTTP1Only {
		t.Fatalf("decoded nested wire profile mismatch: %#v", decodedNested.WireProfile)
	}

	// Direct pluginapi.HTTPRequest JSON serialization (SDK contract)
	sdkReq := pluginapi.HTTPRequest{
		Method:      http.MethodPost,
		URL:         "https://example.com/sdk",
		WireProfile: profile,
	}
	rawSDK, errMarshalSDK := json.Marshal(sdkReq)
	if errMarshalSDK != nil {
		t.Fatalf("marshal sdk request: %v", errMarshalSDK)
	}
	decodedSDK, _, errDecodeSDK := decodeHostHTTPRequestWithCallbackID(rawSDK)
	if errDecodeSDK != nil {
		t.Fatalf("decode sdk request error = %v", errDecodeSDK)
	}
	if decodedSDK.WireProfile == nil || !decodedSDK.WireProfile.HTTP1Only || !decodedSDK.WireProfile.DisableAutoCompression {
		t.Fatalf("decoded sdk wire profile mismatch: %#v", decodedSDK.WireProfile)
	}
	if len(decodedSDK.WireProfile.HeaderProfile) != 2 || decodedSDK.WireProfile.HeaderProfile[0] != "host" {
		t.Fatalf("decoded sdk header profile mismatch: %#v", decodedSDK.WireProfile.HeaderProfile)
	}
}

func TestHostModelExecutePropagatesForcedProviderAndAuthID(t *testing.T) {
	host := New()
	var got handlers.ModelExecutionRequest
	host.SetModelExecutor(&fakeHostModelExecutor{
		executeModel: func(ctx context.Context, req handlers.ModelExecutionRequest) (handlers.ModelExecutionResponse, *interfaces.ErrorMessage) {
			got = req
			return handlers.ModelExecutionResponse{
				StatusCode: http.StatusOK,
				Body:       []byte(`{"ok":true}`),
			}, nil
		},
	})

	rawReq, errMarshal := json.Marshal(rpcHostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol:  "openai",
			ExitProtocol:   "openai",
			Model:          "test-model",
			ForcedProvider: "provider-x",
			AuthID:         "auth-xyz",
		},
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	_, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostModelExecute, rawReq)
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}
	if got.ForcedProvider != "provider-x" {
		t.Fatalf("got.ForcedProvider = %q, want %q", got.ForcedProvider, "provider-x")
	}
	if got.AuthID != "auth-xyz" {
		t.Fatalf("got.AuthID = %q, want %q", got.AuthID, "auth-xyz")
	}
}

func TestHostModelExecuteStreamPropagatesForcedProviderAndAuthID(t *testing.T) {
	host := New()
	var got handlers.ModelExecutionRequest
	host.SetModelExecutor(&fakeHostModelExecutor{
		executeModelStream: func(ctx context.Context, req handlers.ModelExecutionRequest) (handlers.ModelExecutionStream, *interfaces.ErrorMessage) {
			got = req
			chunks := make(chan handlers.ModelExecutionChunk, 1)
			chunks <- handlers.ModelExecutionChunk{Payload: []byte("chunk")}
			close(chunks)
			return handlers.ModelExecutionStream{
				StatusCode: http.StatusOK,
				Chunks:     chunks,
			}, nil
		},
	})

	rawReq, errMarshal := json.Marshal(rpcHostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol:  "openai",
			ExitProtocol:   "openai",
			Model:          "test-model",
			Stream:         true,
			ForcedProvider: "provider-stream",
			AuthID:         "auth-stream-abc",
		},
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	_, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostModelExecuteStream, rawReq)
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}
	if got.ForcedProvider != "provider-stream" {
		t.Fatalf("got.ForcedProvider = %q, want %q", got.ForcedProvider, "provider-stream")
	}
	if got.AuthID != "auth-stream-abc" {
		t.Fatalf("got.AuthID = %q, want %q", got.AuthID, "auth-stream-abc")
	}
}

type testHostStatusError struct {
	error
	status int
}

func (e testHostStatusError) StatusCode() int {
	return e.status
}

func TestModelExecutionErrorPreservesHTTPStatus(t *testing.T) {
	baseErr := errors.New("synthetic")
	errMsg := &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      baseErr,
	}
	err := modelExecutionError(errMsg)
	if got := clienterror.HTTPStatusFromError(err); got != http.StatusTooManyRequests {
		t.Fatalf("clienterror.HTTPStatusFromError() = %d, want %d", got, http.StatusTooManyRequests)
	}
	if !errors.Is(err, baseErr) {
		t.Fatal("expected errors.Is(err, baseErr) to be true")
	}

	// Verify status preservation when underlying error is nil
	errNilBase := modelExecutionError(&interfaces.ErrorMessage{StatusCode: http.StatusServiceUnavailable})
	if got := clienterror.HTTPStatusFromError(errNilBase); got != http.StatusServiceUnavailable {
		t.Fatalf("clienterror.HTTPStatusFromError(nilBase) = %d, want %d", got, http.StatusServiceUnavailable)
	}

	// Verify status preservation when underlying error already matches status
	matchingErr := testHostStatusError{error: errors.New("synthetic"), status: http.StatusTooManyRequests}
	errMatching := modelExecutionError(&interfaces.ErrorMessage{StatusCode: http.StatusTooManyRequests, Error: matchingErr})
	if errMatching != matchingErr {
		t.Fatalf("expected errMatching to return original matchingErr, got %#v", errMatching)
	}

	// Verify explicit status overrides mismatched underlying status while preserving unwrap
	errConflict := modelExecutionError(&interfaces.ErrorMessage{StatusCode: http.StatusServiceUnavailable, Error: matchingErr})
	if got := clienterror.HTTPStatusFromError(errConflict); got != http.StatusServiceUnavailable {
		t.Fatalf("clienterror.HTTPStatusFromError(errConflict) = %d, want %d", got, http.StatusServiceUnavailable)
	}
	var unwrapped testHostStatusError
	if !errors.As(errConflict, &unwrapped) {
		t.Fatal("expected errors.As(errConflict, &unwrapped) to be true")
	}
}

func TestHostModelExecuteCallbackPreservesHTTPStatusOnError(t *testing.T) {
	host := New()
	host.SetModelExecutor(&fakeHostModelExecutor{
		executeModel: func(ctx context.Context, req handlers.ModelExecutionRequest) (handlers.ModelExecutionResponse, *interfaces.ErrorMessage) {
			return handlers.ModelExecutionResponse{}, &interfaces.ErrorMessage{
				StatusCode: http.StatusTooManyRequests,
				Error:      errors.New("synthetic"),
			}
		},
	})

	rawReq, errMarshal := json.Marshal(rpcHostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: "openai",
			ExitProtocol:  "openai",
			Model:         "gpt-5.5",
		},
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	_, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostModelExecute, rawReq)
	if errCall == nil {
		t.Fatal("expected callFromPlugin to fail")
	}
	if got := clienterror.HTTPStatusFromError(errCall); got != http.StatusTooManyRequests {
		t.Fatalf("clienterror.HTTPStatusFromError(errCall) = %d, want %d", got, http.StatusTooManyRequests)
	}
}
