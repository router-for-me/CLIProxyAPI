package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestRPCExecuteStreamUnloadClosesIdleStreamAndIsolatesReload(t *testing.T) {
	host := New()
	defer host.ShutdownAll()
	oldClient := newStreamCallbackPluginClient()
	old := loadStreamTestAdapter(host, "alpha", oldClient)
	stream, errStream := old.ExecuteStream(context.Background(), pluginapi.ExecutorRequest{Stream: true})
	if errStream != nil {
		t.Fatal(errStream)
	}
	otherClient := newStreamCallbackPluginClient()
	other := loadStreamTestAdapter(host, "beta", otherClient)
	otherStream, errOther := other.ExecuteStream(context.Background(), pluginapi.ExecutorRequest{Stream: true})
	if errOther != nil {
		t.Fatal(errOther)
	}

	if !host.UnloadPlugin("alpha") {
		t.Fatal("UnloadPlugin(alpha) = false")
	}
	assertRPCStreamFailed(t, stream.Chunks, errPluginInstanceClosed)
	if callbackContextExists(host, oldClient.callbackID) {
		t.Fatal("unloaded stream retained its callback scope")
	}
	closeRequest, errMarshal := json.Marshal(rpcStreamCloseRequest{StreamID: oldClient.streamID})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	oldContext := withHostCallbackIdentity(context.Background(), "alpha", old.instance)
	if _, errClose := host.callFromPlugin(oldContext, pluginabi.MethodHostStreamClose, closeRequest); errClose == nil {
		t.Fatal("unloaded instance could still close streams through callbacks")
	}

	reloadedClient := newStreamCallbackPluginClient()
	reloaded := loadStreamTestAdapter(host, "alpha", reloadedClient)
	reloadedStream, errReloaded := reloaded.ExecuteStream(context.Background(), pluginapi.ExecutorRequest{Stream: true})
	if errReloaded != nil {
		t.Fatal(errReloaded)
	}
	// Delayed cleanup of the old image must not cancel the replacement instance.
	host.closeHostHTTPCallbackInstance("alpha", old.instance)
	assertRPCStreamStillWorks(t, host, otherClient, otherStream.Chunks)
	assertRPCStreamStillWorks(t, host, reloadedClient, reloadedStream.Chunks)
}

func TestRPCExecuteStreamUnloadUnblocksSaturatedStreamWithoutConsumer(t *testing.T) {
	host := New()
	defer host.ShutdownAll()
	client := newStreamCallbackPluginClient()
	adapter := loadStreamTestAdapter(host, "alpha", client)
	response, errStream := adapter.ExecuteStream(context.Background(), pluginapi.ExecutorRequest{Stream: true})
	if errStream != nil {
		t.Fatal(errStream)
	}
	callbackClosed := make(chan struct{})
	if !host.addCallbackCleanup(client.callbackID, func() { close(callbackClosed) }) {
		t.Fatal("stream callback scope is already closed")
	}
	host.streams.mu.Lock()
	pump := host.streams.streams[client.streamID]
	host.streams.mu.Unlock()
	// One chunk is buffered downstream, one blocks in the forwarder, and the rest fill the pump.
	for range streamBridgeBufferSize + 2 {
		if errEmit := host.streams.emit(context.Background(), client.streamID, pluginapi.ExecutorStreamChunk{Payload: []byte("queued")}); errEmit != nil {
			t.Fatal(errEmit)
		}
	}
	emitContext := &streamBridgeNotifyContext{Context: context.Background(), ready: make(chan struct{})}
	emitDone := make(chan error, 1)
	go func() {
		emitDone <- host.streams.emit(emitContext, client.streamID, pluginapi.ExecutorStreamChunk{Payload: []byte("blocked")})
	}()
	waitForHostTestSignal(t, emitContext.ready, "saturated stream emit")
	select {
	case errEmit := <-emitDone:
		t.Fatalf("emit returned before unload: %v", errEmit)
	default:
	}

	if !host.UnloadPlugin("alpha") {
		t.Fatal("UnloadPlugin(alpha) = false")
	}
	select {
	case errEmit := <-emitDone:
		if errEmit == nil {
			t.Fatal("blocked emit succeeded after unload")
		}
	case <-time.After(time.Second):
		t.Fatal("unload did not unblock the pending emit")
	}
	// Wait without consuming: callback cleanup proves the blocked forwarder exited.
	waitForHostTestSignal(t, callbackClosed, "stream callback cleanup")
	waitForHostTestSignal(t, pump.finished, "stream pump termination")
	assertRPCStreamFailed(t, response.Chunks, errPluginInstanceClosed)
	host.streams.mu.Lock()
	_, exists := host.streams.streams[client.streamID]
	host.streams.mu.Unlock()
	if exists {
		t.Fatal("unload retained the stream bridge entry")
	}
}

func TestRPCExecuteStreamUnloadCancelsPendingCall(t *testing.T) {
	host := New()
	defer host.ShutdownAll()
	client := &pendingStreamPluginClient{started: make(chan struct{})}
	adapter := loadStreamTestAdapter(host, "alpha", client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, errStream := adapter.ExecuteStream(ctx, pluginapi.ExecutorRequest{Stream: true})
		result <- errStream
	}()
	waitForHostTestSignal(t, client.started, "pending ExecuteStream call")
	unloaded := make(chan bool, 1)
	go func() { unloaded <- host.UnloadPlugin("alpha") }()
	if !waitForHostTestBool(t, unloaded, "unload with pending ExecuteStream") {
		t.Fatal("UnloadPlugin(alpha) = false")
	}
	select {
	case errStream := <-result:
		if !errors.Is(errStream, errPluginInstanceClosed) {
			t.Fatalf("ExecuteStream error = %v, want instance-closed cause", errStream)
		}
	case <-time.After(time.Second):
		t.Fatal("unload did not release the pending ExecuteStream call")
	}
	if callbackContextExists(host, client.callbackID) {
		t.Fatal("pending call retained its callback scope after unload")
	}
}

func TestCleanupWhenStreamDoneCancelsWhileIdle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	callbackClosed := make(chan struct{})
	// Cancellation must not depend on the plugin closing or emitting a chunk.
	chunks := cleanupWhenStreamDone(ctx, make(chan pluginapi.ExecutorStreamChunk), func() { close(callbackClosed) })
	cancel()
	assertRPCStreamFailed(t, chunks, context.Canceled)
	waitForHostTestSignal(t, callbackClosed, "idle callback cleanup")
}

func TestRPCExecuteStreamUnloadFailsTranslatedResponseAfterPartialText(t *testing.T) {
	for _, withUsage := range []bool{true, false} {
		name := "without_usage_reporter"
		if withUsage {
			name = "with_usage_reporter"
		}
		t.Run(name, func(t *testing.T) {
			provider := "plugin-stream-unload-" + name
			usage := newTestUsageCapturePlugin(provider)
			registerTestUsagePlugin(t, provider, usage)
			record := normalizeTestCapabilityRecord(capabilityRecord{id: "alpha"})
			host := newHostWithRecords(record)
			defer host.ShutdownAll()
			client := newStreamCallbackPluginClient()
			rpc := loadStreamTestAdapter(host, "alpha", client)
			adapter := newExecutorAdapterForRecordForTest(host, record, rpcProviderExecutor{rpcPluginAdapter: rpc},
				[]sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})
			adapter.provider = provider
			var auth *coreauth.Auth
			if withUsage {
				auth = &coreauth.Auth{ID: "unload-auth", Provider: provider}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			response, errStream := adapter.ExecuteStream(ctx, auth, coreexecutor.Request{
				Model:   "test-model",
				Payload: []byte(`{"model":"test-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
			}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAIResponse})
			if errStream != nil {
				t.Fatal(errStream)
			}
			emitRequest, errMarshal := json.Marshal(rpcStreamEmitRequest{
				StreamID: client.streamID,
				Payload:  []byte("data: {\"id\":\"chatcmpl-unload\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial text\"},\"finish_reason\":null}]}\n\n"),
			})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			callbackContext := withHostCallbackIdentity(context.Background(), "alpha", rpc.instance)
			if _, errEmit := host.callFromPlugin(callbackContext, pluginabi.MethodHostStreamEmit, emitRequest); errEmit != nil {
				t.Fatal(errEmit)
			}
			var payload []byte
			for !bytes.Contains(payload, []byte(`"delta":"partial text"`)) {
				select {
				case chunk, ok := <-response.Chunks:
					if !ok || chunk.Err != nil {
						t.Fatalf("stream ended before partial text: open = %v, error = %v", ok, chunk.Err)
					}
					payload = append(payload, chunk.Payload...)
				case <-time.After(time.Second):
					t.Fatalf("partial text did not reach the Responses client: %s", payload)
				}
			}
			if !host.UnloadPlugin("alpha") {
				t.Fatal("UnloadPlugin(alpha) = false")
			}
			var termination error
		streamLoop:
			for {
				select {
				case chunk, ok := <-response.Chunks:
					if !ok {
						break streamLoop
					}
					payload = append(payload, chunk.Payload...)
					if chunk.Err != nil {
						termination = chunk.Err
					}
				case <-time.After(time.Second):
					t.Fatal("translated stream did not terminate after unload")
				}
			}
			if !errors.Is(termination, errPluginInstanceClosed) {
				t.Fatalf("translated stream error = %v, want instance-closed cause", termination)
			}
			if bytes.Contains(payload, []byte("response.completed")) || bytes.Contains(payload, []byte("[DONE]")) {
				t.Fatalf("unloaded stream emitted a success terminal: %s", payload)
			}
			if ctx.Err() != nil {
				t.Fatalf("unload canceled the original client context: %v", ctx.Err())
			}
			if withUsage {
				if usageRecord := usage.waitRecord(t, time.Second); !usageRecord.Failed {
					t.Fatalf("unloaded stream published successful usage: %+v", usageRecord)
				}
			}
		})
	}
}

type pendingStreamPluginClient struct {
	started    chan struct{}
	callbackID string
}

func (c *pendingStreamPluginClient) Call(ctx context.Context, _ string, request []byte) ([]byte, error) {
	var req rpcExecutorRequest
	if errDecode := json.Unmarshal(request, &req); errDecode != nil {
		return nil, errDecode
	}
	c.callbackID = req.HostCallbackID
	close(c.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*pendingStreamPluginClient) Shutdown() {}

func loadStreamTestAdapter(host *Host, id string, client pluginClient) *rpcPluginAdapter {
	guarded := newGuardedPluginClient(client)
	instance := guarded.callbackInstance()
	host.mu.Lock()
	host.loaded[id] = &loadedPlugin{id: id, client: guarded, callbackInstance: instance}
	host.mu.Unlock()
	return &rpcPluginAdapter{id: id, host: host, client: guarded, instance: instance}
}

func assertRPCStreamClosed(t *testing.T, chunks <-chan pluginapi.ExecutorStreamChunk) {
	t.Helper()
	select {
	case _, ok := <-chunks:
		if ok {
			t.Fatal("stream retained a chunk after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not close")
	}
}

func assertRPCStreamFailed(t *testing.T, chunks <-chan pluginapi.ExecutorStreamChunk, want error) {
	t.Helper()
	select {
	case chunk, ok := <-chunks:
		if !ok || !errors.Is(chunk.Err, want) {
			t.Fatalf("terminal chunk = %+v, open = %v, want %v", chunk, ok, want)
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not report its termination cause")
	}
	assertRPCStreamClosed(t, chunks)
}

func assertRPCStreamStillWorks(t *testing.T, host *Host, client *streamCallbackPluginClient, chunks <-chan pluginapi.ExecutorStreamChunk) {
	t.Helper()
	if !callbackContextExists(host, client.callbackID) {
		t.Fatal("another instance lost its callback scope")
	}
	if errEmit := host.streams.emit(context.Background(), client.streamID, pluginapi.ExecutorStreamChunk{Payload: []byte("active")}); errEmit != nil {
		t.Fatal(errEmit)
	}
	select {
	case chunk, ok := <-chunks:
		if !ok || string(chunk.Payload) != "active" {
			t.Fatalf("active stream result = %q, open = %v", chunk.Payload, ok)
		}
	case <-time.After(time.Second):
		t.Fatal("another instance stopped streaming")
	}
	host.streams.close(client.streamID, "")
	assertRPCStreamClosed(t, chunks)
	if callbackContextExists(host, client.callbackID) {
		t.Fatal("normal stream completion retained the callback scope")
	}
}
