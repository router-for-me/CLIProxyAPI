package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type testWebSocketObserverFunc func(context.Context, pluginapi.WebSocketResponseEvent) error

func (f testWebSocketObserverFunc) ObserveWebSocketResponseEvent(ctx context.Context, event pluginapi.WebSocketResponseEvent) error {
	return f(ctx, event)
}

func TestObserveWebSocketResponseEventInvokesPlugin(t *testing.T) {
	var got pluginapi.WebSocketResponseEvent
	called := false
	host := newHostWithRecords(capabilityRecord{
		id: "quota-tracker",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				WebSocketResponseObserver: testWebSocketObserverFunc(func(_ context.Context, event pluginapi.WebSocketResponseEvent) error {
					got = event
					called = true
					return nil
				}),
			},
		},
	})

	rawPayload := []byte(`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":42}}}`)
	host.ObserveWebSocketResponseEvent(context.Background(), pluginapi.WebSocketResponseEvent{
		RequestID:      "req-123",
		SourceFormat:   "openai",
		Model:          "gpt-5.3-codex",
		RequestedModel: "gpt-5.3-codex",
		Provider:       "codex",
		AuthID:         "auth-abc",
		AuthLabel:      "test-auth",
		AuthType:       "oauth",
		EventType:      "codex.rate_limits",
		Payload:        rawPayload,
	})

	if !called {
		t.Fatal("observer callback was not invoked")
	}
	if got.RequestID != "req-123" {
		t.Fatalf("RequestID = %q, want req-123", got.RequestID)
	}
	if got.Provider != "codex" {
		t.Fatalf("Provider = %q, want codex", got.Provider)
	}
	if got.AuthID != "auth-abc" || got.AuthLabel != "test-auth" {
		t.Fatalf("Auth = (%q, %q), want (auth-abc, test-auth)", got.AuthID, got.AuthLabel)
	}
	if got.EventType != "codex.rate_limits" {
		t.Fatalf("EventType = %q, want codex.rate_limits", got.EventType)
	}
	if !bytes.Equal(got.Payload, rawPayload) {
		t.Fatalf("Payload = %s, want %s", got.Payload, rawPayload)
	}
}

func TestObserveWebSocketResponseEventClonesPayloadAndMetadata(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	originalPayload := []byte(`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":1}}}`)
	originalMetadata := map[string]any{"key": "value"}
	called := false

	host := newHostWithRecords(capabilityRecord{
		id: "observer-clone",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				WebSocketResponseObserver: testWebSocketObserverFunc(func(_ context.Context, event pluginapi.WebSocketResponseEvent) error {
					event.Payload[0] = 'X'
					event.Metadata["key"] = "mutated"
					called = true
					return nil
				}),
			},
		},
	})

	host.ObserveWebSocketResponseEvent(ctx, pluginapi.WebSocketResponseEvent{
		RequestID: "req-clone",
		Payload:   originalPayload,
		Metadata:  originalMetadata,
	})

	if !called {
		t.Fatal("observer callback was not invoked")
	}
	if originalPayload[0] != '{' {
		t.Fatalf("original payload was mutated: %s", originalPayload)
	}
	if originalMetadata["key"] != "value" {
		t.Fatalf("original metadata was mutated: %#v", originalMetadata)
	}
}

func TestObserveWebSocketResponseEventFusesOnPanic(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "panicking-observer",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				WebSocketResponseObserver: testWebSocketObserverFunc(func(context.Context, pluginapi.WebSocketResponseEvent) error {
					panic("observer panic")
				}),
			},
		},
	})

	if !host.HasWebSocketResponseObservers() {
		t.Fatal("HasWebSocketResponseObservers() = false, want true")
	}

	host.ObserveWebSocketResponseEvent(context.Background(), pluginapi.WebSocketResponseEvent{
		RequestID: "req-panic",
		Payload:   []byte(`{"type":"test"}`),
	})

	if !host.isPluginFused("panicking-observer") {
		t.Fatal("isPluginFused(panicking-observer) = false, want true")
	}
	if host.HasWebSocketResponseObservers() {
		t.Fatal("HasWebSocketResponseObservers() = true after fusing, want false")
	}
}

func TestObserveWebSocketResponseEventSkipPlugin(t *testing.T) {
	calls := 0
	host := newHostWithRecords(capabilityRecord{
		id: "skipped-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				WebSocketResponseObserver: testWebSocketObserverFunc(func(context.Context, pluginapi.WebSocketResponseEvent) error {
					calls++
					return nil
				}),
			},
		},
	})

	host.ObserveWebSocketResponseEventExcept(context.Background(), pluginapi.WebSocketResponseEvent{
		RequestID: "req-skip",
	}, "skipped-plugin")

	if calls != 0 {
		t.Fatalf("observer calls = %d, want 0 when skipped", calls)
	}
}

func TestRegisterRPCPluginRegistersWebSocketResponseObserver(t *testing.T) {
	lookup := newTestSymbolLookup(&testPlugin{
		registerResult: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				WebSocketResponseObserver: testWebSocketObserverFunc(func(context.Context, pluginapi.WebSocketResponseEvent) error {
					return nil
				}),
			},
		},
	})

	registered, errRegister := registerRPCPlugin(context.Background(), nil, "rpc-observer", lookup, pluginabi.MethodPluginRegister, nil)
	if errRegister != nil {
		t.Fatalf("registerRPCPlugin() error = %v", errRegister)
	}
	if registered.Capabilities.WebSocketResponseObserver == nil {
		t.Fatal("WebSocketResponseObserver = nil, want RPC adapter")
	}
}

type rpcObserverRecordingClient struct {
	lastMethod  string
	lastRequest []byte
}

func (c *rpcObserverRecordingClient) Call(_ context.Context, method string, request []byte) ([]byte, error) {
	c.lastMethod = method
	c.lastRequest = bytes.Clone(request)
	return json.Marshal(pluginabi.Envelope{OK: true, Result: json.RawMessage(`{}`)})
}

func (c *rpcObserverRecordingClient) Shutdown() {}

func TestObserveWebSocketResponseEventRPCSanitizesMetadata(t *testing.T) {
	client := &rpcObserverRecordingClient{}
	adapter := &rpcPluginAdapter{
		id:     "rpc-observer-sanitize",
		client: client,
	}

	rawPayload := []byte(`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":99}}}`)
	unserializableMetadata := map[string]any{
		"safe_key":   "safe_value",
		"func_field": func() {},
		"chan_field": make(chan int),
	}

	err := adapter.ObserveWebSocketResponseEvent(context.Background(), pluginapi.WebSocketResponseEvent{
		RequestID:      "req-rpc-sanitize",
		SourceFormat:   "openai",
		Model:          "gpt-5.3-codex",
		RequestedModel: "gpt-5.3-codex",
		Provider:       "codex",
		AuthID:         "auth-xyz",
		AuthLabel:      "test-auth-rpc",
		AuthType:       "oauth",
		EventType:      "codex.rate_limits",
		Payload:        rawPayload,
		Metadata:       unserializableMetadata,
	})
	if err != nil {
		t.Fatalf("ObserveWebSocketResponseEvent() error = %v", err)
	}

	if client.lastMethod != pluginabi.MethodWebSocketResponseEvent {
		t.Fatalf("lastMethod = %q, want %q", client.lastMethod, pluginabi.MethodWebSocketResponseEvent)
	}

	var decoded rpcWebSocketResponseEvent
	if errUnmarshal := json.Unmarshal(client.lastRequest, &decoded); errUnmarshal != nil {
		t.Fatalf("unmarshal rpc request: %v", errUnmarshal)
	}

	if decoded.RequestID != "req-rpc-sanitize" {
		t.Fatalf("decoded RequestID = %q, want req-rpc-sanitize", decoded.RequestID)
	}
	if decoded.EventType != "codex.rate_limits" {
		t.Fatalf("decoded EventType = %q, want codex.rate_limits", decoded.EventType)
	}
	if decoded.Metadata["safe_key"] != "safe_value" {
		t.Fatalf("decoded safe_key = %v, want safe_value", decoded.Metadata["safe_key"])
	}
	if _, exists := decoded.Metadata["func_field"]; exists {
		t.Fatalf("func_field was not sanitized: %#v", decoded.Metadata)
	}
	if _, exists := decoded.Metadata["chan_field"]; exists {
		t.Fatalf("chan_field was not sanitized: %#v", decoded.Metadata)
	}
}
