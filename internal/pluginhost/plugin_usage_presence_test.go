package pluginhost

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestPluginResponsesPreserveUsagePresenceAcrossHost(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, scenario := range []string{"measured", "unknown_reasoning", "unobserved", "zero"} {
				name := fmt.Sprintf("native=%v/stream=%v/%s", native, stream, scenario)
				t.Run(name, func(t *testing.T) {
					capture := newTestUsageCapturePlugin("presence-test")
					registerTestUsagePlugin(t, "presence-"+name, capture)
					capability := normalizeTestCapabilityRecord(capabilityRecord{id: "presence-plugin"})
					host := newHostWithRecords(capability)
					format := sdktranslator.FormatOpenAI
					usage := `{"prompt_tokens":100,"completion_tokens":30,"total_tokens":130,"prompt_tokens_details":{"cached_tokens":40,"cache_write_tokens":10},"completion_tokens_details":{"reasoning_tokens":12}}`
					if scenario == "unknown_reasoning" {
						usage = strings.Replace(usage, `,"completion_tokens_details":{"reasoning_tokens":12}`, "", 1)
					}
					if scenario == "zero" {
						usage = `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0,"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"completion_tokens_details":{"reasoning_tokens":0}}`
					}
					if native {
						format = sdktranslator.FormatOpenAIResponse
						usage = strings.NewReplacer("prompt_tokens_details", "input_tokens_details", "completion_tokens_details", "output_tokens_details", "prompt_tokens", "input_tokens", "completion_tokens", "output_tokens").Replace(usage)
					}
					usageField := `,"usage":` + usage
					if scenario == "unobserved" {
						usageField = ""
					}
					body := `{"id":"chatcmpl-presence","object":"chat.completion","model":"presence-model","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]` + usageField + `}`
					chunks := []string{
						"data: {\"id\":\"chatcmpl-presence\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"done\"}}]}\n\n",
						`data: {"id":"chatcmpl-presence","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]` + usageField + "}\n\n",
						"data: [DONE]\n\n",
					}
					if native {
						body = `{"id":"resp-presence","object":"response","model":"presence-model","output":[]` + usageField + `}`
						chunks = []string{`event: response.completed` + "\n" + `data: {"type":"response.completed","response":` + body + "}\n\n"}
					}
					exec := &fakeExecutor{identifier: "presence-test",
						execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
							return pluginapi.ExecutorResponse{Payload: []byte(body)}, nil
						},
						executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
							wire := []byte(strings.Join(chunks, ""))
							// Deliberately split every byte, including inside counter names.
							out := make(chan pluginapi.ExecutorStreamChunk, len(wire))
							for _, b := range wire {
								out <- pluginapi.ExecutorStreamChunk{Payload: []byte{b}}
							}
							close(out)
							return pluginapi.ExecutorStreamResponse{Chunks: out}, nil
						},
					}
					adapter := newExecutorAdapterForRecordForTest(host, capability, exec, []sdktranslator.Format{format}, []sdktranslator.Format{format})
					adapter.provider = "presence-test"
					auth := &coreauth.Auth{ID: "synthetic-auth", Provider: "presence-test"}
					req := coreexecutor.Request{Model: "presence-model", Payload: []byte(`{"model":"presence-model","messages":[{"role":"user","content":"synthetic"}]}`)}
					opts := coreexecutor.Options{SourceFormat: format, ResponseFormat: sdktranslator.FormatOpenAIResponse, Stream: stream}
					var response gjson.Result
					if stream {
						result, err := adapter.ExecuteStream(context.Background(), auth, req, opts)
						if err != nil {
							t.Fatal(err)
						}
						var wire bytes.Buffer
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
							wire.Write(chunk.Payload)
						}
						helps.IterateStreamLines(wire.Bytes(), func(line []byte) {
							json := helps.ExtractStreamJSONPayload(line)
							if gjson.GetBytes(json, "type").String() == "response.completed" {
								response = gjson.GetBytes(json, "response")
							}
						})
					} else {
						result, err := adapter.Execute(context.Background(), auth, req, opts)
						if err != nil {
							t.Fatal(err)
						}
						response = gjson.ParseBytes(result.Payload)
					}
					if !response.IsObject() {
						t.Fatal("missing completed response")
					}
					record := capture.waitRecord(t, time.Second)
					observed := record.Detail.TokenBreakdown.Observed
					if observed == nil || !record.Detail.TokenBreakdown.Valid() {
						t.Fatalf("missing accounting presence: %+v", record.Detail)
					}
					if scenario == "unobserved" {
						if response.Get("usage").Exists() || record.Detail.TokenBreakdown.Quality != coreusage.TokenAccountingQualityUnclassified || observed.InputTokens {
							t.Fatalf("unknown usage was presented as zero: client=%s detail=%+v", response.Raw, record.Detail)
						}
						return
					}
					if scenario == "unknown_reasoning" {
						if response.Get("usage.output_tokens_details").Exists() || observed.ReasoningTokens || record.Detail.TokenBreakdown.Quality != coreusage.TokenAccountingQualityUnclassified {
							t.Fatalf("unknown reasoning was presented as zero: %s", response.Raw)
						}
					} else if !observed.ReasoningTokens || !response.Get("usage.output_tokens_details.reasoning_tokens").Exists() || record.Detail.TokenBreakdown.Quality != coreusage.TokenAccountingQualityComplete {
						t.Fatalf("explicit reasoning lost: %s, %+v", response.Raw, record.Detail)
					}
					wantWrite := int64(10)
					if scenario == "zero" {
						wantWrite = 0
					}
					if write := response.Get("usage.input_tokens_details.cache_write_tokens"); !write.Exists() || write.Int() != wantWrite {
						t.Fatalf("cache write lost through host: %s", response.Raw)
					}
				})
			}
		}
	}
}
