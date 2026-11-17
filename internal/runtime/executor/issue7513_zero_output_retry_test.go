package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	internalcache "github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/wsrelay"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func issue7513RequestPayload(format sdktranslator.Format, model string, stream bool) []byte {
	switch format {
	case sdktranslator.FormatGemini:
		return []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	case sdktranslator.FormatClaude:
		if stream {
			return []byte(`{"model":"` + model + `","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		}
		return []byte(`{"model":"` + model + `","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)
	case sdktranslator.FormatOpenAI:
		if stream {
			return []byte(`{"model":"` + model + `","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		}
		return []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`)
	case sdktranslator.FormatOpenAIResponse:
		if stream {
			return []byte(`{"model":"` + model + `","stream":true,"input":"hi"}`)
		}
		return []byte(`{"model":"` + model + `","input":"hi"}`)
	default:
		return []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	}
}

func TestIssue7513_AntigravityZeroOutputAbnormalFinishReturns502BeforeBootstrap(t *testing.T) {
	reasons := []string{"MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL", "MALFORMED_RESPONSE"}
	formats := []sdktranslator.Format{
		sdktranslator.FormatGemini,
		sdktranslator.FormatClaude,
		sdktranslator.FormatOpenAI,
		sdktranslator.FormatOpenAIResponse,
	}

	for _, reason := range reasons {
		for _, format := range formats {
			for _, model := range []string{"gemini-4-hydrogen", "gemini-3-pro"} {
				// gemini-3-pro exercises AntigravityExecutor.executeClaudeNonStream in Execute().
				if model == "gemini-3-pro" && format != sdktranslator.FormatClaude && format != sdktranslator.FormatGemini {
					continue
				}
				t.Run(reason+"/"+format.String()+"/"+model, func(t *testing.T) {
					body := `{"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"` + reason + `"}],"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":10,"thoughtsTokenCount":313,"totalTokenCount":443},"modelVersion":"` + model + `","responseId":"resp-ag-7513"}}`
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if strings.Contains(r.URL.Path, "streamGenerateContent") {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = io.WriteString(w, "data: "+body+"\n\n")
							return
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, body)
					}))
					defer server.Close()

					auth := &cliproxyauth.Auth{
						ID:       "ag-" + t.Name(),
						Provider: "antigravity",
						Metadata: map[string]any{
							"access_token": "token-123",
							"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
							"project_id":   "project-1",
						},
						Attributes: map[string]string{"base_url": server.URL},
					}
					capture := &malformedFunctionCallUsageCapture{authID: auth.ID, records: make(chan usage.Record, 2)}
					usage.RegisterNamedPlugin(t.Name(), capture)
					t.Cleanup(func() {
						usage.RegisterNamedPlugin(t.Name(), malformedFunctionCallUsageNoop{})
					})

					exec := NewAntigravityExecutor(&config.Config{})
					nonStreamReq := cliproxyexecutor.Request{
						Model:   model,
						Payload: issue7513RequestPayload(format, model, false),
					}
					nonStreamOpts := cliproxyexecutor.Options{
						SourceFormat:   format,
						ResponseFormat: format,
					}
					_, errExec := exec.Execute(context.Background(), auth, nonStreamReq, nonStreamOpts)
					if errExec == nil {
						t.Fatalf("Execute() error = nil, want 502 error for %s", reason)
					}
					status, okStatus := errExec.(interface{ StatusCode() int })
					if !okStatus || status.StatusCode() != http.StatusBadGateway {
						t.Fatalf("Execute() error = %T %v, want status 502", errExec, errExec)
					}
					assertMalformedFunctionCallFailedUsage(t, capture.records)

					streamReq := cliproxyexecutor.Request{
						Model:   model,
						Payload: issue7513RequestPayload(format, model, true),
					}
					streamOpts := cliproxyexecutor.Options{
						SourceFormat:   format,
						ResponseFormat: format,
						Stream:         true,
					}
					streamRes, errStream := exec.ExecuteStream(context.Background(), auth, streamReq, streamOpts)
					if errStream != nil {
						t.Fatalf("ExecuteStream() error = %v", errStream)
					}
					var emittedPayloads [][]byte
					var streamErr error
					for chunk := range streamRes.Chunks {
						if chunk.Err != nil {
							streamErr = chunk.Err
							continue
						}
						if len(bytes.TrimSpace(chunk.Payload)) > 0 {
							emittedPayloads = append(emittedPayloads, bytes.Clone(chunk.Payload))
						}
					}
					if len(emittedPayloads) > 0 {
						t.Fatalf("ExecuteStream() emitted %d payload chunk(s) before error, want 0 so readStreamBootstrap does not commit 200: %q", len(emittedPayloads), emittedPayloads)
					}
					if streamErr == nil {
						t.Fatalf("ExecuteStream() chunk.Err = nil, want 502 error for %s", reason)
					}
					streamStatus, okStreamStatus := streamErr.(interface{ StatusCode() int })
					if !okStreamStatus || streamStatus.StatusCode() != http.StatusBadGateway {
						t.Fatalf("ExecuteStream() chunk.Err = %T %v, want status 502", streamErr, streamErr)
					}
					assertMalformedFunctionCallFailedUsage(t, capture.records)
				})
			}
		}
	}
}

func TestIssue7513_AntigravityLeadingEmptyAndSplitUsageZeroOutputReturns502WithoutPayload(t *testing.T) {
	leadingEmpty := `{"response":{"candidates":[{"content":{"role":"model","parts":[]}}],"modelVersion":"gemini-4-hydrogen","responseId":"resp-split-7513"}}`
	finishChunk := `{"response":{"candidates":[{"content":{"role":"model"},"finishReason":"UNEXPECTED_TOOL_CALL"}],"modelVersion":"gemini-4-hydrogen","responseId":"resp-split-7513"}}`
	usageChunk := `{"response":{"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":10,"thoughtsTokenCount":313,"totalTokenCount":443},"modelVersion":"gemini-4-hydrogen","responseId":"resp-split-7513"}}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+leadingEmpty+"\n\ndata: "+finishChunk+"\n\ndata: "+usageChunk+"\n\n")
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		ID:       t.Name(),
		Provider: "antigravity",
		Metadata: map[string]any{
			"access_token": "token-123",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-1",
		},
		Attributes: map[string]string{"base_url": server.URL},
	}
	capture := &malformedFunctionCallUsageCapture{authID: auth.ID, records: make(chan usage.Record, 1)}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(t.Name(), malformedFunctionCallUsageNoop{})
	})

	exec := NewAntigravityExecutor(&config.Config{})
	streamRes, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-4-hydrogen",
		Payload: issue7513RequestPayload(sdktranslator.FormatGemini, "gemini-4-hydrogen", true),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatGemini,
		ResponseFormat: sdktranslator.FormatGemini,
		Stream:         true,
	})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}

	var emittedPayloads [][]byte
	var streamErr error
	for chunk := range streamRes.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			continue
		}
		if len(bytes.TrimSpace(chunk.Payload)) > 0 {
			emittedPayloads = append(emittedPayloads, bytes.Clone(chunk.Payload))
		}
	}
	if len(emittedPayloads) > 0 {
		t.Fatalf("ExecuteStream() emitted %d payload chunk(s), want 0: %q", len(emittedPayloads), emittedPayloads)
	}
	if streamErr == nil {
		t.Fatal("ExecuteStream() chunk.Err = nil, want 502")
	}
	status, okStatus := streamErr.(interface{ StatusCode() int })
	if !okStatus || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("ExecuteStream() chunk.Err = %T %v, want 502", streamErr, streamErr)
	}
	assertMalformedFunctionCallFailedUsage(t, capture.records)
}

func issue7513GeminiOrVertexExecutor(t *testing.T, provider, baseURL, authID string) (context.Context, cliproxyauth.ProviderExecutor, *cliproxyauth.Auth) {
	t.Helper()
	ctx := t.Context()
	cfg := &config.Config{}
	auth := &cliproxyauth.Auth{ID: authID, Attributes: map[string]string{"api_key": "test-key", "base_url": baseURL}}
	switch provider {
	case "gemini":
		auth.Provider = "gemini"
		return ctx, NewGeminiExecutor(cfg), auth
	case "vertex_api_key":
		auth.Provider = "vertex"
		return ctx, NewGeminiVertexExecutor(cfg), auth
	case "vertex_service_account":
		saRaw := testVertexServiceAccountJSON(t, baseURL+"/token")
		saRoot := ojson.ParseBytes(saRaw)
		serviceAccount := map[string]any{
			"type":           saRoot.Get("type").String(),
			"project_id":     saRoot.Get("project_id").String(),
			"private_key_id": saRoot.Get("private_key_id").String(),
			"private_key":    saRoot.Get("private_key").String(),
			"client_email":   saRoot.Get("client_email").String(),
			"token_uri":      saRoot.Get("token_uri").String(),
		}
		auth.Provider = "vertex"
		auth.Attributes = nil
		auth.Metadata = map[string]any{"project_id": "proxy-test", "location": "global", "service_account": serviceAccount}
		target, errParse := url.Parse(baseURL)
		if errParse != nil {
			t.Fatal(errParse)
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		t.Cleanup(transport.CloseIdleConnections)
		ctx = context.WithValue(ctx, "cliproxy.roundtripper", issue6258RedirectTransport{target: target, base: transport})
		return ctx, NewGeminiVertexExecutor(cfg), auth
	default:
		t.Fatalf("unknown provider %q", provider)
		return nil, nil, nil
	}
}

func TestIssue7513_GeminiVertexAIStudioZeroOutputAbnormalFinishReturns502BeforeBootstrap(t *testing.T) {
	reasons := []string{"MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL", "MALFORMED_RESPONSE"}
	formats := []sdktranslator.Format{
		sdktranslator.FormatGemini,
		sdktranslator.FormatClaude,
		sdktranslator.FormatOpenAI,
		sdktranslator.FormatOpenAIResponse,
	}

	for _, provider := range []string{"gemini", "vertex_api_key", "vertex_service_account"} {
		for _, reason := range reasons {
			for _, format := range formats {
				t.Run(provider+"/"+reason+"/"+format.String(), func(t *testing.T) {
					body := `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"` + reason + `"}],"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":10,"thoughtsTokenCount":313,"totalTokenCount":443},"modelVersion":"gemini-4-argon","responseId":"resp-7513"}`
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/token" {
							w.Header().Set("Content-Type", "application/json")
							_, _ = io.WriteString(w, `{"access_token":"local-token","token_type":"Bearer","expires_in":3600}`)
							return
						}
						if strings.Contains(r.URL.Path, "streamGenerateContent") {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = io.WriteString(w, "data: "+body+"\n\n")
							return
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, body)
					}))
					defer server.Close()

					ctx, exec, auth := issue7513GeminiOrVertexExecutor(t, provider, server.URL, provider+"-"+reason+"-"+format.String())
					capture := &malformedFunctionCallUsageCapture{authID: auth.ID, records: make(chan usage.Record, 2)}
					usage.RegisterNamedPlugin(t.Name(), capture)
					t.Cleanup(func() {
						usage.RegisterNamedPlugin(t.Name(), malformedFunctionCallUsageNoop{})
					})

					_, errExec := exec.Execute(ctx, auth, cliproxyexecutor.Request{
						Model:   "gemini-4-argon",
						Payload: issue7513RequestPayload(format, "gemini-4-argon", false),
					}, cliproxyexecutor.Options{
						SourceFormat:   format,
						ResponseFormat: format,
					})
					if errExec == nil {
						t.Fatalf("Execute() error = nil, want 502 error for %s", reason)
					}
					status, okStatus := errExec.(interface{ StatusCode() int })
					if !okStatus || status.StatusCode() != http.StatusBadGateway {
						t.Fatalf("Execute() error = %T %v, want status 502", errExec, errExec)
					}
					assertMalformedFunctionCallFailedUsage(t, capture.records)

					streamRes, errStream := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
						Model:   "gemini-4-argon",
						Payload: issue7513RequestPayload(format, "gemini-4-argon", true),
					}, cliproxyexecutor.Options{
						SourceFormat:   format,
						ResponseFormat: format,
						Stream:         true,
					})
					if errStream != nil {
						t.Fatalf("ExecuteStream() error = %v", errStream)
					}
					var emittedPayloads [][]byte
					var streamErr error
					for chunk := range streamRes.Chunks {
						if chunk.Err != nil {
							streamErr = chunk.Err
							continue
						}
						if len(bytes.TrimSpace(chunk.Payload)) > 0 {
							emittedPayloads = append(emittedPayloads, bytes.Clone(chunk.Payload))
						}
					}
					if len(emittedPayloads) > 0 {
						t.Fatalf("ExecuteStream() emitted %d payload chunk(s) before error, want 0: %q", len(emittedPayloads), emittedPayloads)
					}
					if streamErr == nil {
						t.Fatalf("ExecuteStream() chunk.Err = nil, want 502 error for %s", reason)
					}
					streamStatus, okStreamStatus := streamErr.(interface{ StatusCode() int })
					if !okStreamStatus || streamStatus.StatusCode() != http.StatusBadGateway {
						t.Fatalf("ExecuteStream() chunk.Err = %T %v, want status 502", streamErr, streamErr)
					}
					assertMalformedFunctionCallFailedUsage(t, capture.records)
				})
			}
		}
	}
}

func TestIssue7513_AIStudioZeroOutputAbnormalFinishReturns502BeforeBootstrap(t *testing.T) {
	reasons := []string{"MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL", "MALFORMED_RESPONSE"}
	formats := []sdktranslator.Format{
		sdktranslator.FormatGemini,
		sdktranslator.FormatClaude,
		sdktranslator.FormatOpenAI,
		sdktranslator.FormatOpenAIResponse,
	}

	for _, reason := range reasons {
		for _, format := range formats {
			t.Run(reason+"/"+format.String(), func(t *testing.T) {
				malformedBody := `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"` + reason + `"}],"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":10,"thoughtsTokenCount":313,"totalTokenCount":443},"modelVersion":"gemini-4-argon","responseId":"resp-as-7513"}`
				authID := "aistudio-7513-" + reason + "-" + format.String()
				connected := make(chan struct{})
				relay := wsrelay.NewManager(wsrelay.Options{
					ProviderFactory: func(*http.Request) (string, error) { return authID, nil },
					OnConnected:     func(string) { close(connected) },
				})
				server := httptest.NewServer(relay.Handler())
				defer server.Close()
				defer func() {
					if errStop := relay.Stop(context.Background()); errStop != nil {
						t.Error(errStop)
					}
				}()
				conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+relay.Path(), nil)
				if errDial != nil {
					t.Fatal(errDial)
				}
				defer func() {
					if errClose := conn.Close(); errClose != nil {
						t.Error(errClose)
					}
				}()
				<-connected

				clientDone := make(chan error, 1)
				go func() {
					for i := 0; i < 3; i++ {
						var msg wsrelay.Message
						if errReadJSON := conn.ReadJSON(&msg); errReadJSON != nil {
							clientDone <- errReadJSON
							return
						}
						var responses []wsrelay.Message
						switch i {
						case 0, 2:
							responses = []wsrelay.Message{{
								ID:   msg.ID,
								Type: wsrelay.MessageTypeHTTPResp,
								Payload: map[string]any{
									"status":  float64(http.StatusOK),
									"headers": map[string]any{"Content-Type": "application/json"},
									"body":    malformedBody,
								},
							}}
						case 1:
							responses = []wsrelay.Message{
								{ID: msg.ID, Type: wsrelay.MessageTypeStreamStart, Payload: map[string]any{"status": 200}},
								{ID: msg.ID, Type: wsrelay.MessageTypeStreamChunk, Payload: map[string]any{"data": "data: " + malformedBody + "\n\n"}},
								{ID: msg.ID, Type: wsrelay.MessageTypeStreamEnd},
							}
						}
						for _, response := range responses {
							if errWriteJSON := conn.WriteJSON(response); errWriteJSON != nil {
								clientDone <- errWriteJSON
								return
							}
						}
					}
					clientDone <- nil
				}()

				capture := &malformedFunctionCallUsageCapture{authID: authID, records: make(chan usage.Record, 3)}
				usage.RegisterNamedPlugin(t.Name(), capture)
				t.Cleanup(func() {
					usage.RegisterNamedPlugin(t.Name(), malformedFunctionCallUsageNoop{})
				})

				exec := NewAIStudioExecutor(&config.Config{}, "aistudio", relay)
				auth := &cliproxyauth.Auth{ID: authID, Provider: "aistudio"}

				_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
					Model:   "gemini-4-argon",
					Payload: issue7513RequestPayload(format, "gemini-4-argon", false),
				}, cliproxyexecutor.Options{
					SourceFormat:   format,
					ResponseFormat: format,
				})
				if errExec == nil {
					t.Fatalf("Execute() error = nil, want 502 error for %s", reason)
				}
				status, okStatus := errExec.(interface{ StatusCode() int })
				if !okStatus || status.StatusCode() != http.StatusBadGateway {
					t.Fatalf("Execute() error = %T %v, want 502", errExec, errExec)
				}
				assertMalformedFunctionCallFailedUsage(t, capture.records)

				for _, mode := range []string{"stream_chunk", "http_resp"} {
					streamRes, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
						Model:   "gemini-4-argon",
						Payload: issue7513RequestPayload(format, "gemini-4-argon", true),
					}, cliproxyexecutor.Options{
						SourceFormat:   format,
						ResponseFormat: format,
						Stream:         true,
					})
					if errStream != nil {
						t.Fatalf("%s ExecuteStream() error = %v", mode, errStream)
					}
					var emittedPayloads [][]byte
					var streamErr error
					for chunk := range streamRes.Chunks {
						if chunk.Err != nil {
							streamErr = chunk.Err
							continue
						}
						if len(bytes.TrimSpace(chunk.Payload)) > 0 {
							emittedPayloads = append(emittedPayloads, bytes.Clone(chunk.Payload))
						}
					}
					if len(emittedPayloads) > 0 {
						t.Fatalf("%s ExecuteStream() emitted %d payload chunk(s), want 0: %q", mode, len(emittedPayloads), emittedPayloads)
					}
					if streamErr == nil {
						t.Fatalf("%s ExecuteStream() chunk.Err = nil, want 502", mode)
					}
					streamStatus, okStreamStatus := streamErr.(interface{ StatusCode() int })
					if !okStreamStatus || streamStatus.StatusCode() != http.StatusBadGateway {
						t.Fatalf("%s ExecuteStream() chunk.Err = %T %v, want 502", mode, streamErr, streamErr)
					}
					assertMalformedFunctionCallFailedUsage(t, capture.records)
				}

				if errClient := <-clientDone; errClient != nil {
					t.Fatal(errClient)
				}
			})
		}
	}
}

func TestIssue7513_ZeroOutputGuardsPreserveValidContentAndToolCalls(t *testing.T) {
	validToolResp := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"bash","args":{"command":"pwd"}}}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":5,"totalTokenCount":25},"modelVersion":"gemini-4-hydrogen"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+validToolResp+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, validToolResp)
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		ID:       t.Name(),
		Provider: "antigravity",
		Metadata: map[string]any{
			"access_token": "token-123",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-1",
		},
		Attributes: map[string]string{"base_url": server.URL},
	}
	exec := NewAntigravityExecutor(&config.Config{})

	resp, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-4-hydrogen",
		Payload: issue7513RequestPayload(sdktranslator.FormatGemini, "gemini-4-hydrogen", false),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatGemini,
		ResponseFormat: sdktranslator.FormatGemini,
	})
	if errExec != nil {
		t.Fatalf("Execute() error = %v, want nil when valid functionCall is present", errExec)
	}
	if got := ojson.GetBytesNoCopy(resp.Payload, "candidates.0.content.parts.0.functionCall.name").String(); got != "bash" {
		t.Fatalf("functionCall.name = %q, want bash; payload=%s", got, resp.Payload)
	}

	streamRes, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-4-hydrogen",
		Payload: issue7513RequestPayload(sdktranslator.FormatGemini, "gemini-4-hydrogen", true),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatGemini,
		ResponseFormat: sdktranslator.FormatGemini,
		Stream:         true,
	})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	var payload bytes.Buffer
	for chunk := range streamRes.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream chunk error when valid functionCall is present: %v", chunk.Err)
		}
		payload.Write(chunk.Payload)
	}
	if !strings.Contains(payload.String(), `"bash"`) {
		t.Fatalf("expected valid functionCall payload, got %s", payload.String())
	}
}

func TestIssue7513_AntigravityPostThinkingZeroOutputEmitsTerminalError(t *testing.T) {
	thoughtChunk := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"thinking only"}]}}],"modelVersion":"gemini-4-hydrogen","responseId":"resp-thought-7513"}}`
	malformedChunk := `{"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"UNEXPECTED_TOOL_CALL"}],"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":10,"thoughtsTokenCount":313,"totalTokenCount":443},"modelVersion":"gemini-4-hydrogen","responseId":"resp-thought-7513"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+thoughtChunk+"\n\ndata: "+malformedChunk+"\n\n")
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		ID:       t.Name(),
		Provider: "antigravity",
		Metadata: map[string]any{
			"access_token": "token-123",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-1",
		},
		Attributes: map[string]string{"base_url": server.URL},
	}
	capture := &malformedFunctionCallUsageCapture{authID: auth.ID, records: make(chan usage.Record, 1)}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(t.Name(), malformedFunctionCallUsageNoop{})
	})

	exec := NewAntigravityExecutor(&config.Config{})
	streamRes, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-4-hydrogen",
		Payload: issue7513RequestPayload(sdktranslator.FormatGemini, "gemini-4-hydrogen", true),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatGemini,
		ResponseFormat: sdktranslator.FormatGemini,
		Stream:         true,
	})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	var combined bytes.Buffer
	var streamErr error
	for chunk := range streamRes.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			continue
		}
		combined.Write(chunk.Payload)
	}
	if streamErr == nil {
		t.Fatalf("ExecuteStream() chunk.Err = nil, want 502 after post-thinking UNEXPECTED_TOOL_CALL; payload=%s", combined.String())
	}
	status, okStatus := streamErr.(interface{ StatusCode() int })
	if !okStatus || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("ExecuteStream() chunk.Err = %T %v, want 502", streamErr, streamErr)
	}
	if strings.Contains(combined.String(), "UNEXPECTED_TOOL_CALL") {
		t.Fatalf("stream unexpectedly forwarded abnormal terminal frame after post-thinking fault: %s", combined.String())
	}
	assertMalformedFunctionCallFailedUsage(t, capture.records)
}

func TestIssue7513_PostThinkingSplitUsageClaudeEmitsBlockStopAndError(t *testing.T) {
	for _, provider := range []string{"antigravity", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			var frame1, frame2, frame3 string
			if provider == "antigravity" {
				frame1 = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"planning"}]}}],"modelVersion":"gemini-4-hydrogen","responseId":"resp-split-1"}}`
				frame2 = `{"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"modelVersion":"gemini-4-hydrogen","responseId":"resp-split-1"}}`
				frame3 = `{"response":{"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":10,"thoughtsTokenCount":313,"totalTokenCount":443},"modelVersion":"gemini-4-hydrogen","responseId":"resp-split-1"}}`
			} else {
				frame1 = `{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"planning"}]}}],"modelVersion":"gemini-4-argon","responseId":"resp-split-1"}`
				frame2 = `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"modelVersion":"gemini-4-argon","responseId":"resp-split-1"}`
				frame3 = `{"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":10,"thoughtsTokenCount":313,"totalTokenCount":443},"modelVersion":"gemini-4-argon","responseId":"resp-split-1"}`
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: "+frame1+"\n\ndata: "+frame2+"\n\ndata: "+frame3+"\n\n")
			}))
			defer server.Close()

			var exec cliproxyauth.ProviderExecutor
			var auth *cliproxyauth.Auth
			model := "gemini-4-argon"
			if provider == "antigravity" {
				model = "gemini-4-hydrogen"
				exec = NewAntigravityExecutor(&config.Config{})
				auth = &cliproxyauth.Auth{
					ID:       "split-ag-" + t.Name(),
					Provider: "antigravity",
					Metadata: map[string]any{
						"access_token": "token-123",
						"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
						"project_id":   "project-1",
					},
					Attributes: map[string]string{"base_url": server.URL},
				}
			} else {
				exec = NewGeminiExecutor(&config.Config{})
				auth = &cliproxyauth.Auth{
					ID:         "split-gem-" + t.Name(),
					Provider:   "gemini",
					Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL},
				}
			}

			capture := &malformedFunctionCallUsageCapture{authID: auth.ID, records: make(chan usage.Record, 1)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() {
				usage.RegisterNamedPlugin(t.Name(), malformedFunctionCallUsageNoop{})
			})

			streamRes, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   model,
				Payload: issue7513RequestPayload(sdktranslator.FormatClaude, model, true),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatClaude,
				ResponseFormat: sdktranslator.FormatClaude,
				Stream:         true,
			})
			if errStream != nil {
				t.Fatalf("ExecuteStream() error = %v", errStream)
			}

			var combined bytes.Buffer
			var streamErr error
			for chunk := range streamRes.Chunks {
				if chunk.Err != nil {
					streamErr = chunk.Err
					continue
				}
				combined.Write(chunk.Payload)
			}
			if streamErr == nil {
				t.Fatalf("ExecuteStream() chunk.Err = nil, want 502; payload=%s", combined.String())
			}
			status, okStatus := streamErr.(interface{ StatusCode() int })
			if !okStatus || status.StatusCode() != http.StatusBadGateway {
				t.Fatalf("ExecuteStream() chunk.Err = %T %v, want 502", streamErr, streamErr)
			}
			outText := combined.String()
			stopIdx := strings.Index(outText, "event: content_block_stop\n")
			errIdx := strings.Index(outText, "event: error\n")
			if stopIdx < 0 || errIdx < 0 || stopIdx >= errIdx {
				t.Fatalf("expected content_block_stop before error event on split post-thought Claude stream:\n%s", outText)
			}
			assertMalformedFunctionCallFailedUsage(t, capture.records)
		})
	}
}

func TestIssue7513_EarlyPreliminaryUsageDoesNotLoseFinalUsageOnZeroOutputFailure(t *testing.T) {
	for _, provider := range []string{"antigravity", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			var frame1, frame2, frame3 string
			if provider == "antigravity" {
				frame1 = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"planning"}]}}],"usageMetadata":{"promptTokenCount":120,"totalTokenCount":120},"modelVersion":"gemini-4-hydrogen","responseId":"resp-prelim-1"}}`
				frame2 = `{"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"modelVersion":"gemini-4-hydrogen","responseId":"resp-prelim-1"}}`
				frame3 = `{"response":{"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":10,"thoughtsTokenCount":313,"totalTokenCount":443},"modelVersion":"gemini-4-hydrogen","responseId":"resp-prelim-1"}}`
			} else {
				frame1 = `{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"planning"}]}}],"usageMetadata":{"promptTokenCount":120,"totalTokenCount":120},"modelVersion":"gemini-4-argon","responseId":"resp-prelim-1"}`
				frame2 = `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"modelVersion":"gemini-4-argon","responseId":"resp-prelim-1"}`
				frame3 = `{"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":10,"thoughtsTokenCount":313,"totalTokenCount":443},"modelVersion":"gemini-4-argon","responseId":"resp-prelim-1"}`
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: "+frame1+"\n\ndata: "+frame2+"\n\ndata: "+frame3+"\n\n")
			}))
			defer server.Close()

			var exec cliproxyauth.ProviderExecutor
			var auth *cliproxyauth.Auth
			model := "gemini-4-argon"
			if provider == "antigravity" {
				model = "gemini-4-hydrogen"
				exec = NewAntigravityExecutor(&config.Config{})
				auth = &cliproxyauth.Auth{
					ID:       "prelim-ag-" + t.Name(),
					Provider: "antigravity",
					Metadata: map[string]any{
						"access_token": "token-123",
						"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
						"project_id":   "project-1",
					},
					Attributes: map[string]string{"base_url": server.URL},
				}
			} else {
				exec = NewGeminiExecutor(&config.Config{})
				auth = &cliproxyauth.Auth{
					ID:         "prelim-gem-" + t.Name(),
					Provider:   "gemini",
					Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL},
				}
			}

			capture := &malformedFunctionCallUsageCapture{authID: auth.ID, records: make(chan usage.Record, 1)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() {
				usage.RegisterNamedPlugin(t.Name(), malformedFunctionCallUsageNoop{})
			})

			streamRes, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   model,
				Payload: issue7513RequestPayload(sdktranslator.FormatClaude, model, true),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatClaude,
				ResponseFormat: sdktranslator.FormatClaude,
				Stream:         true,
			})
			if errStream != nil {
				t.Fatalf("ExecuteStream() error = %v", errStream)
			}
			var streamErr error
			for chunk := range streamRes.Chunks {
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
			}
			status, okStatus := streamErr.(interface{ StatusCode() int })
			if !okStatus || status.StatusCode() != http.StatusBadGateway {
				t.Fatalf("ExecuteStream() chunk.Err = %T %v, want 502", streamErr, streamErr)
			}
			assertMalformedFunctionCallFailedUsage(t, capture.records)
		})
	}
}

func TestIssue7513_EmptyFunctionCallPlaceholderReturns502(t *testing.T) {
	for _, provider := range []string{"antigravity", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			var body string
			model := "gemini-4-argon"
			if provider == "antigravity" {
				model = "gemini-4-hydrogen"
				body = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{}}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":60,"candidatesTokenCount":1,"totalTokenCount":61},"modelVersion":"gemini-4-hydrogen"}}`
			} else {
				body = `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{}}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":60,"candidatesTokenCount":1,"totalTokenCount":61},"modelVersion":"gemini-4-argon"}`
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "streamGenerateContent") {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: "+body+"\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()

			var exec cliproxyauth.ProviderExecutor
			var auth *cliproxyauth.Auth
			if provider == "antigravity" {
				exec = NewAntigravityExecutor(&config.Config{})
				auth = &cliproxyauth.Auth{
					ID:       "empty-fc-ag-" + t.Name(),
					Provider: "antigravity",
					Metadata: map[string]any{
						"access_token": "token-123",
						"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
						"project_id":   "project-1",
					},
					Attributes: map[string]string{"base_url": server.URL},
				}
			} else {
				exec = NewGeminiExecutor(&config.Config{})
				auth = &cliproxyauth.Auth{
					ID:         "empty-fc-gem-" + t.Name(),
					Provider:   "gemini",
					Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL},
				}
			}

			resp, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   model,
				Payload: issue7513RequestPayload(sdktranslator.FormatClaude, model, false),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatClaude,
				ResponseFormat: sdktranslator.FormatClaude,
			})
			if len(resp.Payload) != 0 {
				t.Fatalf("Execute() payload = %s, want empty for empty functionCall placeholder", resp.Payload)
			}
			status, okStatus := errExec.(interface{ StatusCode() int })
			if !okStatus || status.StatusCode() != http.StatusBadGateway {
				t.Fatalf("Execute() error = %T %v, want 502", errExec, errExec)
			}

			streamRes, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   model,
				Payload: issue7513RequestPayload(sdktranslator.FormatClaude, model, true),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatClaude,
				ResponseFormat: sdktranslator.FormatClaude,
				Stream:         true,
			})
			if errStream != nil {
				t.Fatalf("ExecuteStream() error = %v", errStream)
			}
			var chunks []cliproxyexecutor.StreamChunk
			var streamErr error
			for chunk := range streamRes.Chunks {
				if chunk.Err != nil {
					streamErr = chunk.Err
					continue
				}
				if len(chunk.Payload) > 0 {
					chunks = append(chunks, chunk)
				}
			}
			if len(chunks) != 0 {
				t.Fatalf("expected zero pre-bootstrap chunks for empty functionCall placeholder, got %d", len(chunks))
			}
			status, okStatus = streamErr.(interface{ StatusCode() int })
			if !okStatus || status.StatusCode() != http.StatusBadGateway {
				t.Fatalf("ExecuteStream() chunk.Err = %T %v, want 502", streamErr, streamErr)
			}
		})
	}
}

func TestIssue7513_PartialVisibleTextWithAbnormalFinishIsPreserved(t *testing.T) {
	for _, provider := range []string{"antigravity", "gemini"} {
		for _, format := range []sdktranslator.Format{sdktranslator.FormatClaude, sdktranslator.FormatGemini} {
			t.Run(provider+"/"+format.String(), func(t *testing.T) {
				var nonStreamBody, streamChunk1, streamChunk2 string
				model := "gemini-4-argon"
				if provider == "antigravity" {
					model = "gemini-4-hydrogen"
					nonStreamBody = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"partial answer"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":50,"candidatesTokenCount":3,"totalTokenCount":53},"modelVersion":"gemini-4-hydrogen"}}`
					streamChunk1 = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"partial answer"}]}}],"modelVersion":"gemini-4-hydrogen"}}`
					streamChunk2 = `{"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":50,"candidatesTokenCount":3,"totalTokenCount":53},"modelVersion":"gemini-4-hydrogen"}}`
				} else {
					nonStreamBody = `{"candidates":[{"content":{"role":"model","parts":[{"text":"partial answer"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":50,"candidatesTokenCount":3,"totalTokenCount":53},"modelVersion":"gemini-4-argon"}`
					streamChunk1 = `{"candidates":[{"content":{"role":"model","parts":[{"text":"partial answer"}]}}],"modelVersion":"gemini-4-argon"}`
					streamChunk2 = `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":50,"candidatesTokenCount":3,"totalTokenCount":53},"modelVersion":"gemini-4-argon"}`
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.Contains(r.URL.Path, "streamGenerateContent") {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: "+streamChunk1+"\n\ndata: "+streamChunk2+"\n\n")
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, nonStreamBody)
				}))
				defer server.Close()

				var exec cliproxyauth.ProviderExecutor
				var auth *cliproxyauth.Auth
				if provider == "antigravity" {
					exec = NewAntigravityExecutor(&config.Config{})
					auth = &cliproxyauth.Auth{
						ID:       "partial-ag-" + t.Name(),
						Provider: "antigravity",
						Metadata: map[string]any{
							"access_token": "token-123",
							"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
							"project_id":   "project-1",
						},
						Attributes: map[string]string{"base_url": server.URL},
					}
				} else {
					exec = NewGeminiExecutor(&config.Config{})
					auth = &cliproxyauth.Auth{
						ID:         "partial-gem-" + t.Name(),
						Provider:   "gemini",
						Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL},
					}
				}

				resp, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
					Model:   model,
					Payload: issue7513RequestPayload(format, model, false),
				}, cliproxyexecutor.Options{
					SourceFormat:   format,
					ResponseFormat: format,
				})
				if errExec != nil {
					t.Fatalf("Execute() error = %v, want nil when partial answer exists", errExec)
				}
				if !strings.Contains(string(resp.Payload), "partial answer") {
					t.Fatalf("Execute() payload missing partial answer: %s", resp.Payload)
				}

				streamRes, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
					Model:   model,
					Payload: issue7513RequestPayload(format, model, true),
				}, cliproxyexecutor.Options{
					SourceFormat:   format,
					ResponseFormat: format,
					Stream:         true,
				})
				if errStream != nil {
					t.Fatalf("ExecuteStream() error = %v", errStream)
				}
				var payload bytes.Buffer
				for chunk := range streamRes.Chunks {
					if chunk.Err != nil {
						t.Fatalf("ExecuteStream() unexpected err when partial visible output already emitted: %v", chunk.Err)
					}
					payload.Write(chunk.Payload)
				}
				if !strings.Contains(payload.String(), "partial answer") {
					t.Fatalf("ExecuteStream() payload missing partial answer: %s", payload.String())
				}
				if format == sdktranslator.FormatClaude && strings.Contains(payload.String(), "event: error\n") {
					t.Fatalf("unexpected Claude error event when partial answer exists: %s", payload.String())
				}
			})
		}
	}
}

func TestIssue7513_ThoughtChunkStreamsSynchronouslyBeforeNextChunkForTTFT(t *testing.T) {
	firstChunkSent := make(chan struct{})
	releaseSecondChunk := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, okFlusher := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: "+`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"live thought"}]}}]}}`+"\n\n")
		if okFlusher {
			flusher.Flush()
		}
		close(firstChunkSent)
		select {
		case <-releaseSecondChunk:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "data: "+`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"final answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"totalTokenCount":12}}}`+"\n\n")
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		ID:       t.Name(),
		Provider: "antigravity",
		Metadata: map[string]any{
			"access_token": "token-123",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-1",
		},
		Attributes: map[string]string{"base_url": server.URL},
	}
	exec := NewAntigravityExecutor(&config.Config{})
	streamRes, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-4-hydrogen",
		Payload: issue7513RequestPayload(sdktranslator.FormatGemini, "gemini-4-hydrogen", true),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatGemini,
		ResponseFormat: sdktranslator.FormatGemini,
		Stream:         true,
	})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}

	<-firstChunkSent
	select {
	case chunk, okChunk := <-streamRes.Chunks:
		if !okChunk || chunk.Err != nil {
			t.Fatalf("unexpected chunk before second upstream frame: ok=%v err=%v", okChunk, chunk.Err)
		}
		if !strings.Contains(string(chunk.Payload), "live thought") {
			t.Fatalf("first streamed chunk = %s, want live thought", chunk.Payload)
		}
	case <-time.After(3 * time.Second):
		close(releaseSecondChunk)
		t.Fatal("thought chunk was buffered instead of streaming immediately for TTFT")
	}
	close(releaseSecondChunk)

	for chunk := range streamRes.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected trailing chunk error: %v", chunk.Err)
		}
	}
}

func TestIssue7513_ZeroOutputAbnormalFinishDoesNotCommitReasoningReplay(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	sessionKey := "session:replay-zero-output-7513"
	body := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"thinking","thoughtSignature":"sig-should-not-cache"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":50,"thoughtsTokenCount":10,"totalTokenCount":60},"modelVersion":"gemini-3-flash-agent"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+body+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		ID:       t.Name(),
		Provider: "antigravity",
		Metadata: map[string]any{
			"access_token": "token-123",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-1",
		},
		Attributes: map[string]string{"base_url": server.URL},
	}
	exec := NewAntigravityExecutor(&config.Config{})
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatGemini,
		ResponseFormat: sdktranslator.FormatGemini,
		Metadata:       map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: sessionKey},
	}

	_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3-flash-agent",
		Payload: issue7513RequestPayload(sdktranslator.FormatGemini, "gemini-3-flash-agent", false),
	}, opts)
	if errExec == nil {
		t.Fatal("Execute() error = nil, want 502")
	}
	if items, ok := internalcache.GetAntigravityReasoningReplayItems("gemini-3-flash-agent", sessionKey); ok && len(items) > 0 {
		t.Fatalf("non-stream zero-output failure unexpectedly committed replay items: %d", len(items))
	}

	opts.Stream = true
	streamRes, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3-flash-agent",
		Payload: issue7513RequestPayload(sdktranslator.FormatGemini, "gemini-3-flash-agent", true),
	}, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	for range streamRes.Chunks {
	}
	if items, ok := internalcache.GetAntigravityReasoningReplayItems("gemini-3-flash-agent", sessionKey); ok && len(items) > 0 {
		t.Fatalf("stream zero-output failure unexpectedly committed replay items: %d", len(items))
	}
}

func TestIssue7513_AIStudioMixedStreamChunkAndHTTPResp(t *testing.T) {
	runRelayEvents := func(t *testing.T, format sdktranslator.Format, events []wsrelay.Message) ([]byte, error) {
		t.Helper()
		authID := "aistudio-mixed-" + t.Name()
		connected := make(chan struct{})
		relay := wsrelay.NewManager(wsrelay.Options{
			ProviderFactory: func(*http.Request) (string, error) { return authID, nil },
			OnConnected:     func(string) { close(connected) },
		})
		server := httptest.NewServer(relay.Handler())
		defer server.Close()
		defer func() {
			if errStop := relay.Stop(context.Background()); errStop != nil {
				t.Error(errStop)
			}
		}()

		conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+relay.Path(), nil)
		if errDial != nil {
			t.Fatalf("dial failed: %v", errDial)
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				t.Error(errClose)
			}
		}()
		<-connected

		reqDone := make(chan error, 1)
		go func() {
			var first wsrelay.Message
			if errRead := conn.ReadJSON(&first); errRead != nil {
				reqDone <- errRead
				return
			}
			for _, ev := range events {
				ev.ID = first.ID
				if errWrite := conn.WriteJSON(ev); errWrite != nil {
					reqDone <- errWrite
					return
				}
			}
			reqDone <- nil
		}()

		exec := NewAIStudioExecutor(&config.Config{}, "aistudio", relay)
		auth := &cliproxyauth.Auth{ID: authID, Provider: "aistudio"}
		streamRes, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-4-argon",
			Payload: issue7513RequestPayload(format, "gemini-4-argon", true),
		}, cliproxyexecutor.Options{
			SourceFormat:   format,
			ResponseFormat: format,
			Stream:         true,
		})
		if errStream != nil {
			t.Fatalf("ExecuteStream() error = %v", errStream)
		}
		if errClient := <-reqDone; errClient != nil {
			t.Fatalf("relay client error = %v", errClient)
		}
		var payload bytes.Buffer
		var streamErr error
		for chunk := range streamRes.Chunks {
			if chunk.Err != nil {
				streamErr = chunk.Err
				continue
			}
			payload.Write(chunk.Payload)
		}
		return payload.Bytes(), streamErr
	}

	t.Run("stream_chunk_visible_text_then_http_resp_malformed_preserves_text", func(t *testing.T) {
		payload, streamErr := runRelayEvents(t, sdktranslator.FormatGemini, []wsrelay.Message{
			{Type: wsrelay.MessageTypeStreamStart, Payload: map[string]any{"status": float64(http.StatusOK)}},
			{Type: wsrelay.MessageTypeStreamChunk, Payload: map[string]any{"data": `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"partial answer"}]}}]}` + "\n\n"}},
			{Type: wsrelay.MessageTypeHTTPResp, Payload: map[string]any{
				"status":  float64(http.StatusOK),
				"headers": map[string]any{"Content-Type": "application/json"},
				"body":    `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":3,"totalTokenCount":23}}`,
			}},
		})
		if streamErr != nil {
			t.Fatalf("expected preserved partial output with nil error, got %v (payload=%s)", streamErr, payload)
		}
		if !strings.Contains(string(payload), "partial answer") {
			t.Fatalf("expected partial answer in payload, got %s", payload)
		}
	})

	t.Run("stream_chunk_visible_text_without_trailing_blank_line_then_http_resp_malformed_preserves_text", func(t *testing.T) {
		payload, streamErr := runRelayEvents(t, sdktranslator.FormatGemini, []wsrelay.Message{
			{Type: wsrelay.MessageTypeStreamStart, Payload: map[string]any{"status": float64(http.StatusOK)}},
			{Type: wsrelay.MessageTypeStreamChunk, Payload: map[string]any{"data": `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"partial answer"}]}}]}`}},
			{Type: wsrelay.MessageTypeHTTPResp, Payload: map[string]any{
				"status":  float64(http.StatusOK),
				"headers": map[string]any{"Content-Type": "application/json"},
				"body":    `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":3,"totalTokenCount":23}}`,
			}},
		})
		if streamErr != nil {
			t.Fatalf("expected preserved partial output with nil error, got %v (payload=%s)", streamErr, payload)
		}
		if !strings.Contains(string(payload), "partial answer") {
			t.Fatalf("expected partial answer in payload, got %s", payload)
		}
	})

	t.Run("stream_chunk_thought_then_http_resp_malformed_claude_emits_error_and_502", func(t *testing.T) {
		payload, streamErr := runRelayEvents(t, sdktranslator.FormatClaude, []wsrelay.Message{
			{Type: wsrelay.MessageTypeStreamStart, Payload: map[string]any{"status": float64(http.StatusOK)}},
			{Type: wsrelay.MessageTypeStreamChunk, Payload: map[string]any{"data": `data: {"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"hmm"}]}}]}` + "\n\n"}},
			{Type: wsrelay.MessageTypeHTTPResp, Payload: map[string]any{
				"status":  float64(http.StatusOK),
				"headers": map[string]any{"Content-Type": "application/json"},
				"body":    `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":20,"thoughtsTokenCount":5,"totalTokenCount":25}}`,
			}},
		})
		status, okStatus := streamErr.(interface{ StatusCode() int })
		if !okStatus || status.StatusCode() != http.StatusBadGateway {
			t.Fatalf("streamErr = %T %v, want 502", streamErr, streamErr)
		}
		if !strings.Contains(string(payload), "event: error\n") {
			t.Fatalf("expected Claude error event in payload, got %s", payload)
		}
	})

	t.Run("stream_chunk_empty_shell_then_http_resp_malformed_returns_empty_and_502", func(t *testing.T) {
		payload, streamErr := runRelayEvents(t, sdktranslator.FormatOpenAI, []wsrelay.Message{
			{Type: wsrelay.MessageTypeStreamStart, Payload: map[string]any{"status": float64(http.StatusOK)}},
			{Type: wsrelay.MessageTypeStreamChunk, Payload: map[string]any{"data": `data: {"candidates":[{"content":{"role":"model","parts":[]}}]}` + "\n\n"}},
			{Type: wsrelay.MessageTypeHTTPResp, Payload: map[string]any{
				"status":  float64(http.StatusOK),
				"headers": map[string]any{"Content-Type": "application/json"},
				"body":    `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"UNEXPECTED_TOOL_CALL"}],"usageMetadata":{"promptTokenCount":20,"totalTokenCount":20}}`,
			}},
		})
		if len(payload) != 0 {
			t.Fatalf("expected zero payload before bootstrap failure, got %s", payload)
		}
		status, okStatus := streamErr.(interface{ StatusCode() int })
		if !okStatus || status.StatusCode() != http.StatusBadGateway {
			t.Fatalf("streamErr = %T %v, want 502", streamErr, streamErr)
		}
	})
}

func TestIssue7513_ThoughtMediaOnlyWithMalformedResponseReturns502(t *testing.T) {
	body := `{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"inlineData":{"mimeType":"image/png","data":"AA=="}}]},"finishReason":"MALFORMED_RESPONSE"}],"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":10,"thoughtsTokenCount":313,"totalTokenCount":443},"modelVersion":"gemini-4-argon"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+`{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"inlineData":{"mimeType":"image/png","data":"AA=="}}]}}]}`+"\n\ndata: "+`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_RESPONSE"}],"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":10,"thoughtsTokenCount":313,"totalTokenCount":443}}`+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	ctx, exec, auth := issue7513GeminiOrVertexExecutor(t, "gemini", server.URL, "gemini-thought-media-7513")
	capture := &malformedFunctionCallUsageCapture{authID: auth.ID, records: make(chan usage.Record, 2)}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(t.Name(), malformedFunctionCallUsageNoop{})
	})

	resp, errExec := exec.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "gemini-4-argon",
		Payload: issue7513RequestPayload(sdktranslator.FormatGemini, "gemini-4-argon", false),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatGemini,
		ResponseFormat: sdktranslator.FormatGemini,
	})
	if len(resp.Payload) != 0 {
		t.Fatalf("Execute() payload = %s, want empty when only thought media returned", resp.Payload)
	}
	status, okStatus := errExec.(interface{ StatusCode() int })
	if !okStatus || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("Execute() error = %T %v, want 502", errExec, errExec)
	}
	assertMalformedFunctionCallFailedUsage(t, capture.records)

	streamRes, errStream := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gemini-4-argon",
		Payload: issue7513RequestPayload(sdktranslator.FormatGemini, "gemini-4-argon", true),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatGemini,
		ResponseFormat: sdktranslator.FormatGemini,
		Stream:         true,
	})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	var streamErr error
	for chunk := range streamRes.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	status, okStatus = streamErr.(interface{ StatusCode() int })
	if !okStatus || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("ExecuteStream() chunk.Err = %T %v, want 502", streamErr, streamErr)
	}
	assertMalformedFunctionCallFailedUsage(t, capture.records)
}
