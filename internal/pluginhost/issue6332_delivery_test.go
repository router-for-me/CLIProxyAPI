package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type issue6332NestedLifecycle struct {
	handlers.PluginInterceptorHost
	completed chan pluginapi.RequestCompletion
}

func (*issue6332NestedLifecycle) HasRequestInterceptors() bool { return false }
func (*issue6332NestedLifecycle) HasStreamInterceptors() bool  { return false }
func (h *issue6332NestedLifecycle) CompleteRequest(_ context.Context, c pluginapi.RequestCompletion) {
	h.completed <- c
}

// Exercise the actual plugin RPC callbacks and stream bridge, not a fake model executor.
func TestIssue6332PluginNestedDelivery(t *testing.T) {
	for _, mode := range []string{"complete", "cancel_partial", "cancel_complete"} {
		t.Run(mode, func(t *testing.T) {
			cancelStream := mode != "complete"
			const model = "gemini-3.7-flash"
			signature := strings.Repeat("s", 128)
			requests := make(chan []byte, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Error(errRead)
					return
				}
				requests <- body
				w.Header().Set("Content-Type", "text/event-stream")
				// Cancellation of partial upstream output must not commit replay.
				finish := `,"finishReason":"STOP"`
				if mode == "cancel_partial" {
					finish = ""
				}
				_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"native-call","name":"lookup","args":{"key":"value"}},"thoughtSignature":"`+signature+`"}]}`+finish+`}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33},"responseId":"nested"}}`+"\n\n")
				w.(http.Flusher).Flush()
				if cancelStream {
					<-r.Context().Done()
				}
			}))
			defer server.Close()
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(runtimeexecutor.NewAntigravityExecutor(&config.Config{}))
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "antigravity", Status: cliproxyauth.StatusActive, ProxyURL: "direct", Metadata: map[string]any{"access_token": "token", "expired": time.Now().Add(time.Hour).Format(time.RFC3339), "project_id": "project"}, Attributes: map[string]string{"base_url": server.URL}}
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatal(errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
			lifecycle := &issue6332NestedLifecycle{completed: make(chan pluginapi.RequestCompletion, 4)}
			base.SetPluginHost(lifecycle)
			host := New()
			host.SetModelExecutor(base)
			outer, finishOuter := usage.WithStreamDelivery(context.Background())
			// No outer completion is acknowledged until the nested tool loop finishes.
			defer finishOuter(context.Canceled)
			callbackID, closeCallback := host.openCallbackContextForPlugin(outer, "tool-loop-plugin")
			defer closeCallback()
			call := func(body string) (string, error) {
				raw, _ := json.Marshal(rpcHostModelExecutionRequest{HostCallbackID: callbackID, HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{EntryProtocol: "openai-response", ExitProtocol: "openai-response", Model: model, Stream: true, Body: []byte(body)}})
				response, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostModelExecuteStream, raw)
				if errCall != nil {
					return "", errCall
				}
				stream, errDecode := decodeRPCEnvelope[pluginapi.HostModelStreamResponse](response)
				if errDecode != nil {
					return "", errDecode
				}
				read, _ := json.Marshal(pluginapi.HostModelStreamReadRequest{StreamID: stream.StreamID})
				var output strings.Builder
				for {
					response, errRead := host.callFromPlugin(context.Background(), pluginabi.MethodHostModelStreamRead, read)
					if errRead != nil {
						return "", errRead
					}
					chunk, errDecodeChunk := decodeRPCEnvelope[pluginapi.HostModelStreamReadResponse](response)
					if errDecodeChunk != nil {
						return "", errDecodeChunk
					}
					if chunk.Error != "" {
						return "", fmt.Errorf("stream error: %s", chunk.Error)
					}
					output.Write(chunk.Payload)
					if cancelStream {
						closeCallback()
						return output.String(), nil
					}
					if chunk.Done {
						return output.String(), nil
					}
				}
			}
			result := make(chan error, 1)
			go func() {
				first := `{"model":"` + model + `","stream":true,"metadata":{"session_id":"` + t.Name() + `"},"input":[{"role":"user","content":"lookup"}]}`
				output, errCall := call(first)
				if errCall != nil || cancelStream {
					result <- errCall
					return
				}
				var callID string
				for _, line := range strings.Split(output, "\n") {
					event := gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
					for _, item := range event.Get("response.output").Array() {
						if item.Get("type").String() == "function_call" {
							callID = item.Get("call_id").String()
						}
					}
				}
				if callID == "" {
					result <- fmt.Errorf("missing function call: %s", output)
					return
				}
				second, _ := sjson.Set(first, "input.-1", map[string]any{"type": "function_call", "call_id": callID, "name": "lookup", "arguments": `{"key":"value"}`})
				second, _ = sjson.Set(second, "input.-1", map[string]any{"type": "function_call_output", "call_id": callID, "output": "found"})
				_, errCall = call(second)
				result <- errCall
			}()
			select {
			case errResult := <-result:
				if errResult != nil {
					t.Fatal(errResult)
				}
			case <-time.After(3 * time.Second):
				finishOuter(context.Canceled)
				closeCallback()
				<-result
				t.Fatal("nested tool loop blocked waiting for outer HTTP delivery")
			}
			if usage.StreamDeliverySupported(outer) {
				t.Error("nested producer leaked outer delivery support")
			}
			turns := 2
			if cancelStream {
				turns = 1
			}
			for range turns {
				select {
				case c := <-lifecycle.completed:
					want := pluginapi.RequestCompletionSucceeded
					if cancelStream {
						want = pluginapi.RequestCompletionCanceled
					}
					if c.Outcome != want {
						t.Errorf("nested lifecycle = %+v, want %s", c, want)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("nested lifecycle waits for outer delivery")
				}
			}
			<-requests
			if !cancelStream {
				second := <-requests
				if !strings.Contains(string(second), signature) {
					t.Errorf("second turn lost replay signature: %s", second)
				}
			} else {
				readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancelRead()
				items, _, _, errGet := cache.GetAntigravityReasoningReplayItemsWithSnapshotRequired(readCtx, model, "responses:"+t.Name())
				if errGet != nil || (len(items) > 0) != (mode == "cancel_complete") {
					t.Errorf("replay must follow upstream completeness, not delivery: %v, %v", items, errGet)
				}
			}
		})
	}
}
