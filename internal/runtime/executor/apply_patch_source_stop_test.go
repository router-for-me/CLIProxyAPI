package executor

import (
	"fmt"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// Run the R1 source trace through the actual Interactions HTTP executor and usage boundary.
func TestApplyPatchInteractionsSourceStopActualFailures(t *testing.T) {
	for _, first := range []string{"item", "call", "neither"} {
		for _, lateName := range []bool{false, true} {
			for _, input := range []string{"partial", "complete"} {
				for _, discovery := range []string{"terminal", "coincident-snapshot"} {
					t.Run(fmt.Sprintf("%s/late-name=%v/%s/%s", first, lateName, input, discovery), func(t *testing.T) {
						id, call, name := "", "", "apply_patch"
						if first == "item" {
							id = "a"
						}
						if first == "call" {
							call = "c"
						}
						if lateName {
							name = ""
						}
						before, after, final := `{"input":"p`, `q"}`, "pq"
						if input == "complete" {
							before, after, final = `{"input":"p"}`, " \t\n", "p"
						}
						snapshot := fmt.Sprintf(`{"index":0,"type":"function_call","id":"a","call_id":"c","name":"apply_patch","arguments":{"input":%q},"provider_secret":"RAW_SECRET"}`, final)
						post := fmt.Sprintf(`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":%q}}`, after)
						if discovery == "coincident-snapshot" {
							post = fmt.Sprintf(`{"event_type":"step.delta","index":0,"step":%s,"delta":{"type":"arguments_delta","arguments":%q}}`, snapshot, after)
						}
						source := [][]byte{
							[]byte(fmt.Sprintf(`{"event_type":"step.start","index":0,"step":{"type":"function_call","id":%q,"call_id":%q,"name":%q}}`, id, call, name)),
							[]byte(fmt.Sprintf(`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":%q}}`, before)),
							[]byte(`{"event_type":"step.stop","index":0}`),
							[]byte(post),
							[]byte(`{"event_type":"interaction.completed","steps":[` + snapshot + `],"usage":{"input_tokens":5,"output_tokens":3}}`),
							[]byte(`[DONE]`),
						}
						exec, auth := applyPatchIdentityExecutor(t, "interactions", source)
						checkUsage := task6CaptureFailureUsage(t, auth.ID)
						defer checkUsage()
						stream, errExecuteStream := exec.ExecuteStream(t.Context(), auth, cliproxyexecutor.Request{Model: "grok-4", Payload: []byte(task6PatchRequest)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
						if errExecuteStream != nil {
							t.Fatal(errExecuteStream)
						}
						failed, errors := 0, 0
						for chunk := range stream.Chunks {
							if chunk.Err != nil {
								errors++
								assertTask6PatchError(t, chunk.Err)
							}
							if strings.Contains(string(chunk.Payload), "RAW_SECRET") || strings.Contains(string(chunk.Payload), "[DONE]") {
								t.Fatalf("failure leaked/completed: %s", chunk.Payload)
							}
							for _, payload := range applyPatchTestPayloads(chunk.Payload) {
								if gjson.GetBytes(payload, "type").String() != "response.failed" || gjson.GetBytes(payload, "response.error.code").String() != "invalid_tool_arguments" {
									t.Fatalf("source-stopped unresolved patch published success/raw fallback: %s", payload)
								}
								failed++
							}
							if len(chunk.Payload) > 0 && len(applyPatchTestPayloads(chunk.Payload)) == 0 {
								t.Fatalf("raw fallback: %s", chunk.Payload)
							}
						}
						if failed != 1 || errors != 1 {
							t.Fatalf("failure count: failed=%d errors=%d", failed, errors)
						}
					})
				}
			}
		}
	}
}
