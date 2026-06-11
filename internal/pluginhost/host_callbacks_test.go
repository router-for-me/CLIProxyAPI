package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

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
