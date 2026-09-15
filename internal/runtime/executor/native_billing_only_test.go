package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/wsrelay"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const nativeBillingOnlyPayload = `{"candidates":[{"content":{"role":"model","parts":[{"text":"weather"}]},"groundingMetadata":{"webSearchQueries":["weather"]},"finishReason":"STOP"}]}`

func TestNativeStreamPublishesBillingWithoutTokens(t *testing.T) {
	for _, provider := range []string{"gemini", "antigravity", "antigravity_nonstream"} {
		for _, failed := range []bool{false, true} {
			name := provider + "/success"
			if failed {
				name = provider + "/truncated"
			}
			t.Run(name, func(t *testing.T) {
				capture := &websocketUsageCapture{authID: t.Name()}
				usage.RegisterNamedPlugin(t.Name(), capture)
				defer usage.RegisterNamedPlugin(t.Name(), &websocketUsageCapture{})
				payload := nativeBillingOnlyPayload
				if provider != "gemini" {
					payload = `{"response":` + payload + `}`
				}
				payload = "data: " + payload + "\n\n"
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					if failed {
						w.Header().Set("Content-Length", fmt.Sprint(len(payload)+100))
					}
					_, _ = w.Write([]byte(payload))
				}))
				defer server.Close()
				auth := &cliproxyauth.Auth{ID: t.Name(), Provider: provider, Attributes: map[string]string{"api_key": "test", "base_url": server.URL}, Metadata: map[string]any{"access_token": "test", "expired": time.Now().Add(time.Hour).Format(time.RFC3339), "project_id": "test"}}
				req := cliproxyexecutor.Request{Model: "gemini-2.5-flash", Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"weather"}]}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}
				var executionErr error
				if provider == "antigravity_nonstream" {
					_, executionErr = NewAntigravityExecutor(&config.Config{}).executeClaudeNonStream(context.Background(), auth, req, opts)
				} else {
					var result *cliproxyexecutor.StreamResult
					if provider == "gemini" {
						result, executionErr = NewGeminiExecutor(&config.Config{}).ExecuteStream(context.Background(), auth, req, opts)
					} else {
						result, executionErr = NewAntigravityExecutor(&config.Config{}).ExecuteStream(context.Background(), auth, req, opts)
					}
					if executionErr == nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								executionErr = chunk.Err
							}
						}
					}
				}
				if (executionErr != nil) != failed {
					t.Fatalf("execution error=%v, want failure=%v", executionErr, failed)
				}
				capture.mu.Lock()
				defer capture.mu.Unlock()
				if len(capture.records) != 1 {
					t.Fatalf("records=%d", len(capture.records))
				}
				record := capture.records[0]
				if record.Failed != failed || record.Detail.UsageObserved || !gjson.Get(record.Detail.RawUsage, "unpriced_server_tools").Bool() {
					t.Fatalf("native billing-only record lost: %+v", record)
				}
			})
		}
	}
}

func TestAIStudioStreamPublishesBillingWithoutTokens(t *testing.T) {
	authID := t.Name()
	connected := make(chan struct{})
	relay := wsrelay.NewManager(wsrelay.Options{ProviderFactory: func(*http.Request) (string, error) { return authID, nil }, OnConnected: func(string) { close(connected) }})
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	defer func() { _ = relay.Stop(context.Background()) }()
	conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+relay.Path(), nil)
	if errDial != nil {
		t.Fatal(errDial)
	}
	defer func() { _ = conn.Close() }()
	<-connected
	clientDone := make(chan error, 1)
	go func() {
		var msg wsrelay.Message
		if errRead := conn.ReadJSON(&msg); errRead != nil {
			clientDone <- errRead
			return
		}
		for _, event := range []wsrelay.Message{
			{ID: msg.ID, Type: wsrelay.MessageTypeStreamStart, Payload: map[string]any{"status": float64(http.StatusOK), "headers": map[string]any{"Content-Type": "text/event-stream"}}},
			{ID: msg.ID, Type: wsrelay.MessageTypeStreamChunk, Payload: map[string]any{"data": "data: " + nativeBillingOnlyPayload + "\n\n"}},
			{ID: msg.ID, Type: wsrelay.MessageTypeStreamEnd},
		} {
			if errWrite := conn.WriteJSON(event); errWrite != nil {
				clientDone <- errWrite
				return
			}
		}
		clientDone <- nil
	}()
	capture := &websocketUsageCapture{authID: authID}
	usage.RegisterNamedPlugin(t.Name(), capture)
	defer usage.RegisterNamedPlugin(t.Name(), &websocketUsageCapture{})
	result, errStream := NewAIStudioExecutor(&config.Config{}, "aistudio", relay).ExecuteStream(context.Background(), &cliproxyauth.Auth{ID: authID, Provider: "aistudio"}, cliproxyexecutor.Request{Model: "gemini-2.5-flash", Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"weather"}]}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini})
	if errStream != nil {
		t.Fatal(errStream)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
	}
	if errClient := <-clientDone; errClient != nil {
		t.Fatal(errClient)
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if len(capture.records) != 1 {
		t.Fatalf("records=%d", len(capture.records))
	}
	detail := capture.records[0].Detail
	if detail.UsageObserved || !gjson.Get(detail.RawUsage, "unpriced_server_tools").Bool() {
		t.Fatalf("relay lost billing-only metadata: %+v", detail)
	}
}
