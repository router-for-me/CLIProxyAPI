package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const compactV2Item = `{"id":"cmp_1","type":"compaction","encrypted_content":"opaque-state","future":{"preserved":true}}`
const compactV2Input = `{"model":"gpt-5.4","prompt_cache_key":"compact-cache","reasoning":{"effort":"high","context":"all_turns"},"input":[{"role":"user","content":"history"},{"type":"compaction_trigger","future":"keep"}]}`

type compactionV2Executor interface {
	Execute(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}

func TestRemoteCompactionV2(t *testing.T) {
	for _, transport := range []string{"codex", "codex-buffered", "codex-native-lite", "websocket-http-fallback", "compat", "chain"} {
		for _, stream := range []bool{false, true} {
			for _, output := range []string{"done", "added", "terminal", "missing", "duplicate"} {
				t.Run(fmt.Sprintf("%s/stream=%v/%s", transport, stream, output), func(t *testing.T) {
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, _ := io.ReadAll(r.Body)
						if r.Header.Get("X-Codex-Beta-Features") != "responses_websockets_v2, remote_compaction_v2, future_feature" {
							t.Errorf("lost beta feature header: %v", r.Header.Values("X-Codex-Beta-Features"))
						}
						if r.URL.Path != "/responses" || r.Header.Get("Upgrade") != "" {
							t.Errorf("unexpected upstream path or transport: %s %s", r.URL.Path, r.Header.Get("Upgrade"))
						}
						if gjson.GetBytes(body, "reasoning.context").String() != "all_turns" {
							t.Errorf("lost reasoning context: %s", body)
						}
						if got := gjson.GetBytes(body, "input.1.type").String(); got != "compaction_trigger" {
							t.Errorf("lost trigger: %s", body)
						}
						if gjson.GetBytes(body, "input.1.future").String() != "keep" {
							t.Errorf("lost trigger fields: %s", body)
						}
						if gjson.GetBytes(body, "messages").Exists() || gjson.GetBytes(body, "stream_options").Exists() {
							t.Errorf("chat conversion leaked: %s", body)
						}
						w.Header().Set("Content-Type", "text/event-stream")
						final := "[]"
						switch output {
						case "done", "added":
							fmt.Fprintf(w, "data: {\"type\":\"response.output_item.%s\",\"output_index\":0,\"item\":%s}\n\n", output, compactV2Item)
						case "terminal":
							final = "[" + compactV2Item + "]"
						case "duplicate":
							final = "[" + compactV2Item + "," + compactV2Item + "]"
						}
						fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_cmp\",\"object\":\"response\",\"status\":\"completed\",\"output\":%s,\"usage\":{\"input_tokens\":9,\"output_tokens\":4,\"total_tokens\":13}}}\n\n", final)
					}))
					defer upstream.Close()
					cfg := &config.Config{}
					cfg.Codex.StreamBootstrapBuffering = transport == "codex-buffered"
					auth := &cliproxyauth.Auth{ID: "compact-test", Attributes: map[string]string{"base_url": upstream.URL, "api_key": "test"}}
					var executor compactionV2Executor = NewCodexExecutor(cfg)
					if transport == "websocket-http-fallback" {
						executor = NewCodexWebsocketsExecutor(cfg)
					}
					if transport == "compat" {
						executor = NewOpenAICompatExecutor("compat", cfg)
					}
					if transport == "chain" {
						inner := executor
						innerAuth := auth
						middle := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							body, _ := io.ReadAll(r.Body)
							if r.Header.Get("X-Codex-Beta-Features") != "responses_websockets_v2, remote_compaction_v2, future_feature" {
								t.Errorf("lost beta feature header: %v", r.Header.Values("X-Codex-Beta-Features"))
							}
							if r.URL.Path != "/responses" || gjson.GetBytes(body, "input.1.type").String() != "compaction_trigger" {
								t.Errorf("lost native request at CPA boundary: %s %s", r.URL.Path, body)
							}
							result, err := inner.ExecuteStream(r.Context(), innerAuth, cliproxyexecutor.Request{Model: "gpt-5.4", Payload: body}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: true, Headers: r.Header.Clone()})
							if err != nil {
								http.Error(w, err.Error(), http.StatusBadGateway)
								return
							}
							w.Header().Set("Content-Type", "text/event-stream")
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									fmt.Fprintf(w, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"unsupported_compaction\",\"message\":\"inner compact failed\"}}}\n\n")
									return
								}
								_, _ = w.Write(chunk.Payload)
								if !bytes.HasSuffix(chunk.Payload, []byte("\n\n")) {
									_, _ = w.Write([]byte("\n\n"))
								}
							}
						}))
						defer middle.Close()
						auth = &cliproxyauth.Auth{ID: "outer", Attributes: map[string]string{"base_url": middle.URL, "api_key": "test"}}
						executor = NewOpenAICompatExecutor("compat", cfg)
					}
					req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(compactV2Input)}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: stream, Headers: http.Header{"X-Codex-Beta-Features": {"responses_websockets_v2, remote_compaction_v2, future_feature"}}}
					if transport == "codex-native-lite" {
						opts.Headers.Set(codexResponsesLiteHeader, "true")
					}
					var result []byte
					var err error
					if stream {
						var response *cliproxyexecutor.StreamResult
						response, err = executor.ExecuteStream(context.Background(), auth, req, opts)
						if err == nil {
							for chunk := range response.Chunks {
								if chunk.Err != nil {
									err = chunk.Err
									continue
								}
								for _, line := range bytes.Split(chunk.Payload, []byte("\n")) {
									data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
									if gjson.GetBytes(data, "type").String() == "response.completed" {
										result = []byte(gjson.GetBytes(data, "response").Raw)
									}
									if gjson.GetBytes(data, "type").String() == "response.output_item.done" && gjson.GetBytes(data, "item").Raw != compactV2Item {
										t.Errorf("modified raw item: %s", data)
									}
								}
							}
						}
					} else {
						var response cliproxyexecutor.Response
						response, err = executor.Execute(context.Background(), auth, req, opts)
						result = response.Payload
					}
					if output == "missing" || output == "duplicate" {
						if err == nil || !strings.Contains(err.Error(), "unsupported_compaction") {
							t.Fatalf("want explicit compact failure, got %v; response=%s", err, result)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					items := gjson.GetBytes(result, "output").Array()
					if len(items) != 1 || items[0].Raw != compactV2Item {
						t.Fatalf("lost raw compaction item: %s", result)
					}
					if gjson.GetBytes(result, "usage.total_tokens").Int() != 13 {
						t.Fatalf("lost usage: %s", result)
					}
				})
			}
		}
	}
}

func TestOpenAICompatCompactedHistoryUsesResponses(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("X-Codex-Beta-Features") != "responses_websockets_v2, remote_compaction_v2, future_feature" {
			t.Errorf("lost beta feature header: %v", r.Header.Values("X-Codex-Beta-Features"))
		}
		if r.URL.Path != "/responses" || gjson.GetBytes(body, "input.0").Raw != compactV2Item {
			t.Errorf("lost compacted history: %s %s", r.URL.Path, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_next\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"continued\"}]}]}}\n\n")
	}))
	defer upstream.Close()
	executor := NewOpenAICompatExecutor("compat", &config.Config{})
	response, err := executor.Execute(context.Background(), &cliproxyauth.Auth{Attributes: map[string]string{"base_url": upstream.URL}}, cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":[` + compactV2Item + `,{"role":"user","content":"continue"}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Headers: http.Header{"X-Codex-Beta-Features": {"responses_websockets_v2, remote_compaction_v2, future_feature"}}})
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(response.Payload, "output.0.content.0.text").String() != "continued" {
		t.Fatalf("unexpected response: %s", response.Payload)
	}
}
