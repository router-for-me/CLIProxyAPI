package pluginhost

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestHostHandleServerToolUsesHighestPriorityHandlerOnly(t *testing.T) {
	var highCalls int
	var lowCalls int
	host := newHostWithRecords(
		capabilityRecord{
			id:       "low",
			priority: 1,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{ServerToolHandler: serverToolHandlerFunc{
				handle: func(context.Context, pluginapi.ServerToolRequest) (pluginapi.ServerToolResponse, error) {
					lowCalls++
					return pluginapi.ServerToolResponse{Handled: true, Payload: []byte("low")}, nil
				},
			}}},
		},
		capabilityRecord{
			id:       "high",
			priority: 10,
			meta:     pluginapi.Metadata{Name: "high", Version: "1.0.0"},
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{ServerToolHandler: serverToolHandlerFunc{
				handle: func(ctx context.Context, req pluginapi.ServerToolRequest) (pluginapi.ServerToolResponse, error) {
					highCalls++
					if req.Plugin.Name != "high" {
						t.Fatalf("req.Plugin.Name = %q, want high", req.Plugin.Name)
					}
					return pluginapi.ServerToolResponse{Handled: true, Payload: []byte("high")}, nil
				},
			}}},
		},
	)

	resp, handled, errHandle := host.HandleServerTool(context.Background(), serverToolRequest())
	if errHandle != nil {
		t.Fatalf("HandleServerTool() error = %v, want nil", errHandle)
	}
	if !handled {
		t.Fatal("HandleServerTool() handled = false, want true")
	}
	if string(resp.Payload) != "high" {
		t.Fatalf("HandleServerTool() payload = %q, want high", resp.Payload)
	}
	if highCalls != 1 {
		t.Fatalf("high calls = %d, want 1", highCalls)
	}
	if lowCalls != 0 {
		t.Fatalf("low calls = %d, want 0", lowCalls)
	}
}

func TestValidPluginAcceptsServerToolHandlerOnlyCapability(t *testing.T) {
	plugin := pluginapi.Plugin{
		Metadata: pluginapi.Metadata{
			Name:             "server-tool",
			Version:          "1.0.0",
			Author:           "test",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
		},
		Capabilities: pluginapi.Capabilities{
			ServerToolHandler: serverToolHandlerFunc{
				handle: func(context.Context, pluginapi.ServerToolRequest) (pluginapi.ServerToolResponse, error) {
					return pluginapi.ServerToolResponse{}, nil
				},
				handleStream: func(context.Context, pluginapi.ServerToolRequest) (pluginapi.ServerToolStreamResponse, error) {
					return pluginapi.ServerToolStreamResponse{}, nil
				},
			},
		},
	}
	if !validPlugin(plugin) {
		t.Fatal("validPlugin() = false, want true for server tool handler only plugin")
	}
}

func TestHostHandleServerToolUnhandledDoesNotCallLowerPriorityHandler(t *testing.T) {
	var lowCalls int
	host := newHostWithRecords(
		capabilityRecord{
			id:       "low",
			priority: 1,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{ServerToolHandler: serverToolHandlerFunc{
				handle: func(context.Context, pluginapi.ServerToolRequest) (pluginapi.ServerToolResponse, error) {
					lowCalls++
					return pluginapi.ServerToolResponse{Handled: true, Payload: []byte("low")}, nil
				},
			}}},
		},
		capabilityRecord{
			id:       "high",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{ServerToolHandler: serverToolHandlerFunc{
				handle: func(context.Context, pluginapi.ServerToolRequest) (pluginapi.ServerToolResponse, error) {
					return pluginapi.ServerToolResponse{Handled: false}, nil
				},
			}}},
		},
	)

	_, handled, errHandle := host.HandleServerTool(context.Background(), serverToolRequest())
	if errHandle != nil {
		t.Fatalf("HandleServerTool() error = %v, want nil", errHandle)
	}
	if handled {
		t.Fatal("HandleServerTool() handled = true, want false")
	}
	if lowCalls != 0 {
		t.Fatalf("low calls = %d, want 0", lowCalls)
	}
}

func TestHostHandleServerToolReturnsHandlerError(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "handler",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{ServerToolHandler: serverToolHandlerFunc{
			handle: func(context.Context, pluginapi.ServerToolRequest) (pluginapi.ServerToolResponse, error) {
				return pluginapi.ServerToolResponse{}, errors.New("search backend unavailable")
			},
		}}},
	})

	_, handled, errHandle := host.HandleServerTool(context.Background(), serverToolRequest())
	if !handled {
		t.Fatal("HandleServerTool() handled = false, want true")
	}
	if errHandle == nil || !strings.Contains(errHandle.Error(), "search backend unavailable") {
		t.Fatalf("HandleServerTool() error = %v, want search backend unavailable", errHandle)
	}
}

func TestHostHandleServerToolPanicFusesAndFallsBack(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "handler",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{ServerToolHandler: serverToolHandlerFunc{
			handle: func(context.Context, pluginapi.ServerToolRequest) (pluginapi.ServerToolResponse, error) {
				panic("boom")
			},
		}}},
	})

	_, handled, errHandle := host.HandleServerTool(context.Background(), serverToolRequest())
	if handled {
		t.Fatal("HandleServerTool() handled = true, want false")
	}
	if errHandle != nil {
		t.Fatalf("HandleServerTool() error = %v, want nil", errHandle)
	}
	if !host.isPluginFused("handler") {
		t.Fatal("server tool plugin was not fused after panic")
	}
}

func TestHostHandleServerToolStreamReturnsChunks(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "handler",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{ServerToolHandler: serverToolHandlerFunc{
			handleStream: func(context.Context, pluginapi.ServerToolRequest) (pluginapi.ServerToolStreamResponse, error) {
				chunks := make(chan pluginapi.ServerToolStreamChunk, 1)
				chunks <- pluginapi.ServerToolStreamChunk{Payload: []byte("event: message_stop\n\n")}
				close(chunks)
				return pluginapi.ServerToolStreamResponse{Handled: true, Chunks: chunks}, nil
			},
		}}},
	})

	resp, handled, errHandle := host.HandleServerToolStream(context.Background(), serverToolRequest())
	if errHandle != nil {
		t.Fatalf("HandleServerToolStream() error = %v, want nil", errHandle)
	}
	if !handled || !resp.Handled {
		t.Fatalf("HandleServerToolStream() handled = %v response.Handled = %v, want true", handled, resp.Handled)
	}
	chunk, ok := <-resp.Chunks
	if !ok {
		t.Fatal("HandleServerToolStream() channel closed before first chunk")
	}
	if string(chunk.Payload) != "event: message_stop\n\n" {
		t.Fatalf("stream payload = %q", chunk.Payload)
	}
}

func serverToolRequest() pluginapi.ServerToolRequest {
	return pluginapi.ServerToolRequest{
		Provider:        "antigravity",
		AuthID:          "auth-1",
		AuthProvider:    "antigravity",
		RouteModel:      "gemini-3.5-flash",
		UpstreamModel:   "gemini-3.5-flash",
		SourceFormat:    "anthropic",
		Stream:          true,
		OriginalRequest: []byte(`{"tools":[{"type":"web_search_20250305"}]}`),
		Payload:         []byte(`{"tools":[{"type":"web_search_20250305"}]}`),
	}
}
