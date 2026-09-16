package executor

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestClaudeBridgeBootstrapPreservesUpstreamFailureDelivery(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		for _, scenario := range []string{"overload", "frame_budget", "empty_incomplete"} {
			t.Run(transport+"/"+scenario, func(t *testing.T) {
				events := []string{codexCreatedEvent, codexInProgressEvent}
				if scenario == "frame_budget" {
					for i := 0; i <= codexBootstrapMaxBufferedFrames; i++ {
						events = append(events, codexInProgressEvent)
					}
				}
				if scenario == "empty_incomplete" {
					events = append(events, `{"type":"response.incomplete","response":{"id":"resp_1","output":[],"usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}}`)
				} else {
					events = append(events, codexOverloadEvent)
				}
				var server *httptest.Server
				if transport == "http" {
					server = codexSSEServer(events...)
				} else {
					server = codexWebsocketServer(t, events...)
				}
				defer server.Close()
				body := []byte(`{"model":"gpt-5.6-sol","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`)
				req := cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: body}
				opts := claudeResponsesBridgeOptions(body, true)
				var result *cliproxyexecutor.StreamResult
				var err error
				if transport == "http" {
					result, err = NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
				} else {
					result, err = NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
				}
				if scenario == "overload" {
					if err == nil || result != nil {
						t.Fatalf("overload must fail before exposing usage: result=%v err=%v", result, err)
					}
					return
				}
				if err != nil || result == nil {
					t.Fatalf("expected in-stream failure: result=%v err=%v", result, err)
				}
				payload, streamErr := drainChunks(result)
				if streamErr == nil {
					t.Fatal("expected terminal stream error")
				}
				if !strings.Contains(payload, "message_start") || !strings.Contains(payload, "input_tokens") {
					t.Fatalf("buffered Claude start and usage were lost: %s", payload)
				}
			})
		}
	}
}
