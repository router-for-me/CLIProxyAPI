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
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestPluginRunAccountingAcrossHost(t *testing.T) {
	const ref = `{"provider":"cursor-next","scope":"run","record_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","state":"pending","replay":false,"resumed":true}`
	for _, sourceResponses := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, scenario := range []string{"valid", "absent", "invalid", "context_estimate", "context_estimate_off", "measured_with_flag", "foreign_with_flag"} {
				t.Run(fmt.Sprintf("responses=%v/stream=%v/%s", sourceResponses, stream, scenario), func(t *testing.T) {
					capture := newTestUsageCapturePlugin("accounting-test")
					registerTestUsagePlugin(t, "run-accounting", capture)
					pluginID := "accounting-plugin"
					if scenario == "context_estimate" || scenario == "context_estimate_off" || scenario == "measured_with_flag" {
						pluginID = "cursor-next"
					}
					capability := normalizeTestCapabilityRecord(capabilityRecord{id: pluginID})
					host := newHostWithRecords(capability)
					field := `,"usage_accounting":` + ref
					if scenario == "absent" {
						field = ""
					} else if scenario == "invalid" {
						field = strings.Replace(field, `"scope":"run"`, `"scope":"request"`, 1)
					}
					if scenario == "context_estimate_off" {
						field += `,"cursor_usage":{"client_context_estimate":false}`
					} else if scenario == "context_estimate" || scenario == "measured_with_flag" || scenario == "foreign_with_flag" {
						field += `,"cursor_usage":{"client_context_estimate":true}`
					}
					if scenario == "measured_with_flag" {
						if sourceResponses {
							field += `,"usage":{"input_tokens":7,"output_tokens":2,"total_tokens":9}`
						} else {
							field += `,"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}`
						}
					}
					format := sdktranslator.FormatOpenAI
					body := `{"id":"chatcmpl-run","object":"chat.completion","model":"accounting-model","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]` + field + `}`
					frames := "data: {\"id\":\"chatcmpl-run\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"done\"}}]}\n\n" +
						`data: {"id":"chatcmpl-run","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]` + field + "}\n\ndata: [DONE]\n\n"
					if sourceResponses {
						format = sdktranslator.FormatOpenAIResponse
						body = `{"id":"resp-run","object":"response","status":"completed","model":"accounting-model","output":[{"id":"msg-run","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done","annotations":[]}]}]` + field + `}`
						frames = `data: {"type":"response.completed","response":` + body + "}\n\n"
					}
					exec := &fakeExecutor{identifier: "accounting-test",
						execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
							return pluginapi.ExecutorResponse{Payload: []byte(body)}, nil
						},
						executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
							wire := []byte(frames)
							chunks := make(chan pluginapi.ExecutorStreamChunk, len(wire))
							for _, b := range wire {
								chunks <- pluginapi.ExecutorStreamChunk{Payload: []byte{b}}
							}
							close(chunks)
							return pluginapi.ExecutorStreamResponse{Chunks: chunks}, nil
						},
					}
					adapter := newExecutorAdapterForRecordForTest(host, capability, exec, []sdktranslator.Format{format}, []sdktranslator.Format{format})
					adapter.provider = "accounting-test"
					auth := &coreauth.Auth{ID: "synthetic-run-auth", Provider: "accounting-test"}
					req := coreexecutor.Request{Model: "accounting-model", Payload: []byte(`{"model":"accounting-model","messages":[{"role":"system","content":"synthetic base instructions"},{"role":"user","content":"synthetic"}],"tools":[{"type":"function","function":{"name":"fixture","parameters":{"type":"object"}}}]}`)}
					if sourceResponses {
						req.Payload = []byte(`{"model":"accounting-model","instructions":"synthetic base instructions","input":[{"role":"user","content":"synthetic"}],"tools":[{"type":"function","name":"fixture","parameters":{"type":"object"}}]}`)
					}
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
						t.Fatal("missing terminal response")
					}
					record := capture.waitRecord(t, time.Second)
					wantRef := scenario != "absent" && scenario != "invalid"
					if response.Get("usage_accounting").Exists() != wantRef || (record.UsageAccounting != nil) != wantRef || (record.Detail.UsageAccounting != nil) != wantRef {
						t.Fatalf("reference lost or invalid reference accepted: response=%s record=%+v", response.Raw, record.UsageAccounting)
					}
					if wantRef && (record.UsageAccounting.RecordID != strings.Repeat("b", 64) || !record.UsageAccounting.Resumed || response.Get("usage_accounting.record_id").String() != record.UsageAccounting.RecordID) {
						t.Fatalf("reference changed across host: %s %+v", response.Raw, record.UsageAccounting)
					}
					if scenario == "measured_with_flag" {
						if response.Get("context_usage").Exists() || response.Get("usage.input_tokens").Int() != 7 || record.Detail.InputTokens != 7 || record.Detail.OutputTokens != 2 || !record.Detail.TokenBreakdown.Observed.InputTokens {
							t.Fatalf("client estimation replaced a real measurement: %s %+v", response.Raw, record.Detail)
						}
						return
					}
					if scenario == "context_estimate" {
						if !response.Get("context_usage.estimated").Bool() || response.Get("context_usage.encoding").String() != "cl100k_base" || response.Get("usage.input_tokens").Int() <= 0 || response.Get("usage.output_tokens").Int() <= 0 {
							t.Fatalf("missing explicit client context estimate: %s", response.Raw)
						}
					} else if response.Get("usage").Exists() || response.Get("context_usage").Exists() {
						t.Fatalf("client estimate enabled without Cursor opt-in: %s", response.Raw)
					}
					if record.Detail.InputTokens != 0 || record.Detail.OutputTokens != 0 || record.Detail.TokenBreakdown.Observed == nil || record.Detail.TokenBreakdown.Observed.InputTokens {
						t.Fatalf("Run reference created a request token measurement: %s %+v", response.Raw, record.Detail)
					}
				})
			}
		}
	}
}
