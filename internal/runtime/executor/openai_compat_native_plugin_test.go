package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type nativeResponsesPluginHooks struct {
	requestOK, responseOK       bool
	requestCalls, responseCalls atomic.Int32
}

func (h *nativeResponsesPluginHooks) NormalizeRequest(_ context.Context, _, _ sdktranslator.Format, _ string, body []byte, _ bool) []byte {
	return body
}
func (h *nativeResponsesPluginHooks) NormalizeResponseBefore(_ context.Context, _, _ sdktranslator.Format, _ string, _, _, body []byte, _ bool) []byte {
	return body
}
func (h *nativeResponsesPluginHooks) NormalizeResponseAfter(_ context.Context, _, _ sdktranslator.Format, _ string, _, _, body []byte, _ bool) []byte {
	return body
}
func (h *nativeResponsesPluginHooks) TranslateRequest(_ context.Context, from, to sdktranslator.Format, model string, body []byte, stream bool) ([]byte, bool) {
	if from != sdktranslator.FormatOpenAI || to != sdktranslator.FormatOpenAIResponse {
		return nil, false
	}
	h.requestCalls.Add(1)
	if !h.requestOK || gjson.GetBytes(body, "messages.0.content").String() != "hello plugin" {
		return nil, false
	}
	return []byte(fmt.Sprintf(`{"model":%q,"input":"hello plugin","stream":%t}`, model, stream)), true
}
func (h *nativeResponsesPluginHooks) TranslateResponse(_ context.Context, from, to sdktranslator.Format, _ string, _, _, body []byte, stream bool) ([]byte, bool) {
	if from != sdktranslator.FormatOpenAIResponse || to != sdktranslator.FormatOpenAI {
		return nil, false
	}
	h.responseCalls.Add(1)
	if !h.responseOK {
		return nil, false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(string(body), "data: "))
	if !gjson.Valid(payload) {
		return nil, false
	}
	result := `{"object":"chat.completion","choices":[],"plugin":true}`
	if stream {
		result = "data: " + result + "\n\n"
	}
	return []byte(result), true
}

func TestOpenAICompatNativeResponsesPluginTranslation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tt := range []struct {
			name                                                     string
			requestOK, responseOK, identityRequest, identityResponse bool
			wantCode                                                 int
		}{
			{name: "plugin routes", requestOK: true, responseOK: true},
			{name: "declined request", responseOK: true, wantCode: 400},
			{name: "declined response", requestOK: true, wantCode: 400},
			{name: "normalizer only", wantCode: 400},
			{name: "identity request", responseOK: true, identityRequest: true},
			{name: "identity response", requestOK: true, identityResponse: true},
			{name: "identity routes with declining plugin", identityRequest: true, identityResponse: true},
		} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, tt.name), func(t *testing.T) {
				hooks := &nativeResponsesPluginHooks{requestOK: tt.requestOK, responseOK: tt.responseOK}
				sdktranslator.SetPluginHooks(hooks)
				t.Cleanup(func() { sdktranslator.SetPluginHooks(nil) })
				var upstreamCalls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstreamCalls.Add(1)
					body, _ := io.ReadAll(r.Body)
					if !tt.identityRequest && (gjson.GetBytes(body, "input").String() != "hello plugin" || gjson.GetBytes(body, "messages").Exists()) {
						t.Errorf("untranslated request: %s", body)
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n")
					} else {
						_, _ = io.WriteString(w, `{"object":"response","output":[],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`)
					}
				}))
				defer server.Close()
				executor, auth := nativeResponsesExecutor(server.URL)
				auth.ID = t.Name()
				capture := &nativeResponsesUsageCapture{authID: auth.ID, records: make(chan usage.Record, 2)}
				usage.RegisterPlugin(capture)
				req, opts := nativeResponsesRequest(stream)
				if !tt.identityRequest {
					opts.SourceFormat = sdktranslator.FormatOpenAI
					req.Payload = []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hello plugin"}]}`)
					opts.OriginalRequest = req.Payload
				}
				opts.ResponseFormat = sdktranslator.FormatOpenAI
				if tt.identityResponse {
					opts.ResponseFormat = sdktranslator.FormatOpenAIResponse
				}
				var err error
				var output strings.Builder
				if stream {
					result, executeErr := executor.ExecuteStream(t.Context(), auth, req, opts)
					err = executeErr
					if result != nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								err = chunk.Err
							}
							output.Write(chunk.Payload)
						}
					}
				} else {
					result, executeErr := executor.Execute(t.Context(), auth, req, opts)
					err = executeErr
					output.Write(result.Payload)
				}
				if tt.wantCode == 0 {
					if err != nil {
						t.Fatal(err)
					}
					if !tt.identityResponse && !strings.Contains(output.String(), `"plugin":true`) {
						t.Fatalf("missing plugin translation: %s", output.String())
					}
				} else {
					status, ok := err.(interface{ StatusCode() int })
					if !ok || status.StatusCode() != tt.wantCode {
						t.Fatalf("error=%v, want status %d", err, tt.wantCode)
					}
					scope, ok := err.(interface{ IsRequestScoped() bool })
					if !ok || !scope.IsRequestScoped() {
						t.Fatalf("error is not request scoped: %v", err)
					}
					if output.Len() != 0 {
						t.Fatalf("untranslated response forwarded: %s", output.String())
					}
				}
				wantRequests := int32(2)
				wantUpstream := int32(1)
				if tt.identityRequest {
					wantRequests = 0
				} else if !tt.requestOK {
					wantRequests = 1
					wantUpstream = 0
				}
				wantResponses := wantUpstream
				if tt.identityResponse {
					wantResponses = 0
				}
				if got := hooks.requestCalls.Load(); got != wantRequests {
					t.Errorf("request plugin calls=%d, want %d", got, wantRequests)
				}
				if got := hooks.responseCalls.Load(); got != wantResponses {
					t.Errorf("response plugin calls=%d, want %d", got, wantResponses)
				}
				if got := upstreamCalls.Load(); got != wantUpstream {
					t.Errorf("upstream calls=%d, want %d", got, wantUpstream)
				}
				select {
				case record := <-capture.records:
					if record.Failed != (tt.wantCode != 0) {
						t.Errorf("usage Failed=%t, want %t", record.Failed, tt.wantCode != 0)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("usage record not published")
				}
			})
		}
	}
}

func TestOpenAICompatNativeResponsesPluginRequiresTerminalEvent(t *testing.T) {
	hooks := &nativeResponsesPluginHooks{responseOK: true}
	sdktranslator.SetPluginHooks(hooks)
	t.Cleanup(func() { sdktranslator.SetPluginHooks(nil) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n")
	}))
	defer server.Close()
	executor, auth := nativeResponsesExecutor(server.URL)
	req, opts := nativeResponsesRequest(true)
	opts.ResponseFormat = sdktranslator.FormatOpenAI
	result, err := executor.ExecuteStream(t.Context(), auth, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	status, ok := streamErr.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != 502 || !strings.Contains(streamErr.Error(), "terminal event") {
		t.Fatalf("error=%v, want missing terminal 502", streamErr)
	}
	if got := hooks.responseCalls.Load(); got != 1 {
		t.Errorf("response plugin calls=%d, want 1 (no synthetic DONE)", got)
	}
}
