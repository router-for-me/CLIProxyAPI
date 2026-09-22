package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestRPCExecuteStreamKeepsHostCallbackScopeUntilStreamCloses(t *testing.T) {
	host := New()
	client := newStreamCallbackPluginClient()
	adapter := &rpcPluginAdapter{
		id:     "stream-plugin",
		host:   host,
		client: client,
	}

	stream, errStream := adapter.ExecuteStream(context.Background(), pluginapi.ExecutorRequest{Stream: true})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	waitForStreamCallbackPlugin(t, client)
	if client.callbackID == "" {
		t.Fatal("host callback id is empty")
	}
	if !callbackContextExists(host, client.callbackID) {
		t.Fatal("host callback scope closed before plugin stream closed")
	}

	closeReq, errMarshal := json.Marshal(rpcStreamCloseRequest{StreamID: client.streamID})
	if errMarshal != nil {
		t.Fatalf("marshal close request: %v", errMarshal)
	}
	if _, errClose := host.callFromPlugin(context.Background(), pluginabi.MethodHostStreamClose, closeReq); errClose != nil {
		t.Fatalf("close stream: %v", errClose)
	}
	for range stream.Chunks {
	}

	if callbackContextExists(host, client.callbackID) {
		t.Fatal("host callback scope remained open after plugin stream closed")
	}
}

func TestRPCExecuteStreamClosesHostCallbackScopeOnContextCancelWhileChunkPending(t *testing.T) {
	host := New()
	client := newStreamCallbackPluginClient()
	adapter := &rpcPluginAdapter{
		id:     "stream-plugin",
		host:   host,
		client: client,
	}
	ctx, cancel := context.WithCancel(context.Background())
	stream, errStream := adapter.ExecuteStream(ctx, pluginapi.ExecutorRequest{Stream: true})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	waitForStreamCallbackPlugin(t, client)

	emitReq, errMarshal := json.Marshal(rpcStreamEmitRequest{StreamID: client.streamID, Payload: []byte("pending")})
	if errMarshal != nil {
		t.Fatalf("marshal emit request: %v", errMarshal)
	}
	if _, errEmit := host.callFromPlugin(context.Background(), pluginabi.MethodHostStreamEmit, emitReq); errEmit != nil {
		t.Fatalf("emit stream: %v", errEmit)
	}
	cancel()
	for range stream.Chunks {
	}

	if callbackContextExists(host, client.callbackID) {
		t.Fatal("host callback scope remained open after context cancel")
	}
}

func callbackContextExists(host *Host, callbackID string) bool {
	if host == nil || host.callbackContexts == nil {
		return false
	}
	host.callbackContexts.mu.RLock()
	_, exists := host.callbackContexts.contexts[callbackID]
	host.callbackContexts.mu.RUnlock()
	return exists
}

type streamCallbackPluginClient struct {
	called     chan struct{}
	streamID   string
	callbackID string
}

func newStreamCallbackPluginClient() *streamCallbackPluginClient {
	return &streamCallbackPluginClient{called: make(chan struct{})}
}

func (c *streamCallbackPluginClient) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	if method != pluginabi.MethodExecutorExecuteStream {
		return nil, fmt.Errorf("method = %s, want %s", method, pluginabi.MethodExecutorExecuteStream)
	}
	var req rpcExecutorRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode executor stream request: %w", errUnmarshal)
	}
	c.streamID = req.StreamID
	c.callbackID = req.HostCallbackID
	close(c.called)
	return marshalRPCResult(rpcExecutorStreamResponse{
		Headers: http.Header{"Content-Type": []string{"text/event-stream"}},
	})
}

func (c *streamCallbackPluginClient) Shutdown() {}

func waitForStreamCallbackPlugin(t *testing.T, client *streamCallbackPluginClient) {
	t.Helper()
	select {
	case <-client.called:
	case <-time.After(time.Second):
		t.Fatal("plugin stream method was not called")
	}
}
