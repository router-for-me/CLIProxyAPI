package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestConfigureRequiresStatefulStreamSchema(t *testing.T) {
	raw, errMarshal := json.Marshal(lifecycleRequest{SchemaVersion: 2})
	if errMarshal != nil {
		t.Fatalf("marshal lifecycle request: %v", errMarshal)
	}
	if errConfigure := configure(raw); errConfigure == nil {
		t.Fatal("configure() error = nil for schema version 2")
	}
}

func TestRegistrationDeclaresSchema3StatefulStreamCapability(t *testing.T) {
	registration := pluginRegistration()
	if registration.SchemaVersion != 3 {
		t.Fatalf("schema version = %d, want 3", registration.SchemaVersion)
	}
	if !registration.Capabilities.StreamChunkInterceptor || !registration.Capabilities.StreamChunkInterceptorStateful {
		t.Fatalf("stream capabilities = %#v, want stateful stream interceptor", registration.Capabilities)
	}
}

func TestStreamStateIsIsolatedAndCleanedUp(t *testing.T) {
	resetState(pluginConfig{MaxChunks: 1})
	initStreamForTest(t, "stream-a")
	initStreamForTest(t, "stream-b")

	if response := interceptStreamForTest(t, pluginapi.StreamChunkInterceptRequest{StreamID: "stream-a", ChunkIndex: 0, Body: []byte("a-0")}); response.DropChunk {
		t.Fatal("stream-a first payload was dropped")
	}
	if response := interceptStreamForTest(t, pluginapi.StreamChunkInterceptRequest{StreamID: "stream-a", ChunkIndex: 1, Body: []byte("a-1")}); !response.DropChunk {
		t.Fatal("stream-a second payload was not dropped")
	}
	if response := interceptStreamForTest(t, pluginapi.StreamChunkInterceptRequest{StreamID: "stream-b", ChunkIndex: 0, Body: []byte("b-0")}); response.DropChunk {
		t.Fatal("stream-b first payload inherited stream-a state")
	}

	interceptStreamForTest(t, pluginapi.StreamChunkInterceptRequest{StreamID: "stream-a", ChunkIndex: pluginapi.StreamChunkEndIndex})
	if got := activeStreamCountForTest(); got != 1 {
		t.Fatalf("active stream count after stream-a end = %d, want 1", got)
	}
	interceptStreamForTest(t, pluginapi.StreamChunkInterceptRequest{StreamID: "stream-b", ChunkIndex: pluginapi.StreamChunkEndIndex})
	if got := activeStreamCountForTest(); got != 0 {
		t.Fatalf("active stream count after all ends = %d, want 0", got)
	}
}

func TestConcurrentStreamsRemainIsolated(t *testing.T) {
	resetState(pluginConfig{MaxChunks: 1})
	const streamCount = 32
	errors := make(chan error, streamCount)
	var wg sync.WaitGroup
	for i := range streamCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			streamID := fmt.Sprintf("stream-%d", i)
			requests := []pluginapi.StreamChunkInterceptRequest{
				{StreamID: streamID, ChunkIndex: pluginapi.StreamChunkHeaderInitIndex},
				{StreamID: streamID, ChunkIndex: 0, Body: []byte("first")},
				{StreamID: streamID, ChunkIndex: 1, Body: []byte("second")},
				{StreamID: streamID, ChunkIndex: pluginapi.StreamChunkEndIndex},
			}
			for index, req := range requests {
				response, errIntercept := interceptStream(req)
				if errIntercept != nil {
					errors <- fmt.Errorf("stream %s request %d: %w", streamID, index, errIntercept)
					return
				}
				if index == 1 && response.DropChunk {
					errors <- fmt.Errorf("stream %s first payload was dropped", streamID)
					return
				}
				if index == 2 && !response.DropChunk {
					errors <- fmt.Errorf("stream %s second payload was not dropped", streamID)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errors)
	for errConcurrent := range errors {
		t.Error(errConcurrent)
	}
	if got := activeStreamCountForTest(); got != 0 {
		t.Fatalf("active stream count after concurrent ends = %d, want 0", got)
	}
}

func initStreamForTest(t *testing.T, streamID string) {
	t.Helper()
	response := interceptStreamForTest(t, pluginapi.StreamChunkInterceptRequest{
		StreamID:   streamID,
		ChunkIndex: pluginapi.StreamChunkHeaderInitIndex,
	})
	if response.Headers.Get("X-Stateful-Stream") != "initialized" {
		t.Fatalf("stream %q init headers = %#v", streamID, response.Headers)
	}
}

func interceptStreamForTest(t *testing.T, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	t.Helper()
	response, errIntercept := interceptStream(req)
	if errIntercept != nil {
		t.Fatal(errIntercept)
	}
	return response
}

func interceptStream(req pluginapi.StreamChunkInterceptRequest) (pluginapi.StreamChunkInterceptResponse, error) {
	raw, errMarshal := json.Marshal(req)
	if errMarshal != nil {
		return pluginapi.StreamChunkInterceptResponse{}, fmt.Errorf("marshal stream request: %w", errMarshal)
	}
	rawEnvelope, errIntercept := interceptStreamChunk(raw)
	if errIntercept != nil {
		return pluginapi.StreamChunkInterceptResponse{}, fmt.Errorf("intercept stream chunk: %w", errIntercept)
	}
	var env envelope
	if errUnmarshal := json.Unmarshal(rawEnvelope, &env); errUnmarshal != nil {
		return pluginapi.StreamChunkInterceptResponse{}, fmt.Errorf("unmarshal envelope: %w", errUnmarshal)
	}
	var response pluginapi.StreamChunkInterceptResponse
	if errUnmarshal := json.Unmarshal(env.Result, &response); errUnmarshal != nil {
		return pluginapi.StreamChunkInterceptResponse{}, fmt.Errorf("unmarshal response: %w", errUnmarshal)
	}
	return response, nil
}

func resetState(cfg pluginConfig) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.config = cfg
	state.streams = make(map[string]int)
}

func activeStreamCountForTest() int {
	state.mu.Lock()
	defer state.mu.Unlock()
	return len(state.streams)
}
