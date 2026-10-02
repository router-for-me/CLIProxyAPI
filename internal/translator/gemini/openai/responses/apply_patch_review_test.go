package responses

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const geminiMixedPatchReviewRequest = `{"tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"lookup"}]}`

func geminiPatchReviewPart(indexKey string, index int, id, name, args string) string {
	return fmt.Sprintf(`{"%s":%d,"functionCall":{"id":%q,"name":%q,"args":%s}}`, indexKey, index, id, name, args)
}

func geminiPatchReviewFrame(parts []string, terminal bool) []byte {
	finish := ""
	if terminal {
		finish = `,"finishReason":"STOP"`
	}
	return []byte(`{"candidates":[{"content":{"parts":[` + strings.Join(parts, ",") + `]}` + finish + `}]}`)
}

func geminiPatchReviewEvents(t *testing.T, chunks [][]byte) []gjson.Result {
	t.Helper()
	var events []gjson.Result
	for _, chunk := range chunks {
		_, event := parseSSEEvent(t, chunk)
		events = append(events, event)
	}
	return events
}

func TestGeminiApplyPatchCrossKeyEvidenceConflict(t *testing.T) {
	for _, indexKey := range []string{"partIndex", "index"} {
		for _, order := range []string{"ordinary first", "patch first"} {
			for _, direction := range []string{"ordinary index patch ID", "patch index ordinary ID"} {
				for _, name := range []string{"lookup", "apply_patch", ""} {
					for _, mode := range []string{"stream separate frames", "stream single frame", "non-stream"} {
						t.Run(strings.Join([]string{indexKey, order, direction, name, mode}, "/"), func(t *testing.T) {
							ordinary := geminiPatchReviewPart(indexKey, 3, "ordinary", "lookup", `{"x":1}`)
							patch := geminiPatchReviewPart(indexKey, 2, "patch", "apply_patch", `{"input":"p"}`)
							parts := []string{ordinary, patch}
							if order == "patch first" {
								parts = []string{patch, ordinary}
							}
							index, id := 3, "patch"
							if direction == "patch index ordinary ID" {
								index, id = 2, "ordinary"
							}
							args := `{"x":1}`
							if name == "apply_patch" {
								args = `{"input":"secret"}`
							}
							conflict := geminiPatchReviewPart(indexKey, index, id, name, args)
							parts = append(parts, conflict)
							request := []byte(geminiMixedPatchReviewRequest)
							var param any
							if mode == "non-stream" {
								out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini", request, nil, geminiPatchReviewFrame(parts, true), &param)
								if len(out) != 0 {
									t.Fatalf("patch provenance discarded: %s", out)
								}
								if state, ok := param.(interface{ ToolInputError() error }); !ok || state.ToolInputError() == nil {
									t.Fatal("missing non-stream tool input error")
								}
								return
							}
							send := func(raw []byte) []gjson.Result {
								return geminiPatchReviewEvents(t, ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini", request, nil, raw, &param))
							}
							var events []gjson.Result
							if mode == "stream single frame" {
								events = send(geminiPatchReviewFrame(parts, true))
							} else {
								for _, part := range parts[:2] {
									events = append(events, send(geminiPatchReviewFrame([]string{part}, false))...)
								}
								events = append(events, send(geminiPatchReviewFrame([]string{conflict}, true))...)
							}
							counts := map[string]int{}
							for _, event := range events {
								kind := event.Get("type").String()
								counts[kind]++
								if kind == "response.failed" && (event.Get("response.error.code").String() != "invalid_tool_arguments" || strings.Contains(event.Raw, "secret") || strings.Contains(event.Raw, `\"input\"`)) {
									t.Fatalf("unsanitized failure: %s", event.Raw)
								}
								if kind == "response.custom_tool_call_input.delta" && event.Get("delta").String() != "p" || kind == "response.custom_tool_call_input.done" && event.Get("input").String() != "p" {
									t.Fatalf("collision leaked or replayed patch input: %s", event.Raw)
								}
							}
							if counts["response.failed"] != 1 || counts["response.completed"] != 0 || counts["response.output_item.added"] != 2 || counts["response.output_item.done"] != 2 || counts["response.custom_tool_call_input.delta"] != 1 || counts["response.custom_tool_call_input.done"] != 1 || counts["response.function_call_arguments.done"] != 1 {
								t.Fatalf("collision added a call or completed response: counts=%v events=%v", counts, events)
							}
							state := param.(*geminiToResponsesState)
							patchEvidence := state.FunctionEvidence["part:2"]
							ordinaryEvidence := state.FunctionEvidence["part:3"]
							if state.NextIndex != 2 || len(state.FunctionEvidence) != 4 || patchEvidence == ordinaryEvidence || state.FunctionEvidence["id:patch"] != patchEvidence || state.FunctionEvidence["id:ordinary"] != ordinaryEvidence || !patchEvidence.ApplyPatch || patchEvidence.PatchCall == nil || patchEvidence.RawName != "apply_patch" || ordinaryEvidence.ApplyPatch || ordinaryEvidence.RawName != "lookup" {
								t.Fatalf("collision changed established aliases/provenance: %+v", state.FunctionEvidence)
							}
							errInput := state.ToolInputError()
							if errInput == nil || !strings.Contains(errInput.Error(), "indexes") {
								t.Fatalf("missing cross-key tool input error: %v", errInput)
							}
							for _, raw := range [][]byte{[]byte(`[DONE]`), geminiPatchReviewFrame(nil, true), geminiPatchReviewFrame(parts, true), geminiPatchReviewFrame([]string{patch}, false)} {
								if more := send(raw); len(more) != 0 || state.ToolInputError() != errInput {
									t.Fatalf("failed response reopened or lost error: %v", more)
								}
							}
						})
					}
				}
			}
		}
	}
}

func TestGeminiApplyPatchCrossKeyOrdinaryLegacy(t *testing.T) {
	requests := []struct{ name, request, second string }{
		{"ordinary only in patch-enabled request", `{"tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"lookup"},{"type":"function","name":"second"}]}`, "second"},
		{"same-name function winner", `{"tools":[{"type":"function","name":"apply_patch"},{"type":"function","name":"lookup"},{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch"}]}],"input":[{"type":"additional_tools","tools":[{"type":"custom","name":"apply_patch"}]}]}`, "apply_patch"},
	}
	for _, request := range requests {
		for _, indexKey := range []string{"partIndex", "index"} {
			for _, order := range []string{"first second", "second first"} {
				for _, direction := range []string{"first index second ID", "second index first ID"} {
					t.Run(strings.Join([]string{request.name, indexKey, order, direction}, "/"), func(t *testing.T) {
						parts := []string{geminiPatchReviewPart(indexKey, 3, "first", "lookup", `{"x":1}`), geminiPatchReviewPart(indexKey, 2, "second", request.second, `{"x":2}`)}
						names := []string{"lookup", request.second}
						arguments := []string{`{"x":1}`, `{"x":2}`}
						if order == "second first" {
							parts[0], parts[1] = parts[1], parts[0]
							names[0], names[1] = names[1], names[0]
							arguments[0], arguments[1] = arguments[1], arguments[0]
						}
						index, id, name := 3, "second", "lookup"
						if direction == "second index first ID" {
							index, id, name = 2, "first", request.second
						}
						parts = append(parts, geminiPatchReviewPart(indexKey, index, id, name, `{"x":3}`))
						names = append(names, name)
						arguments = append(arguments, `{"x":3}`)
						var param any
						var events []gjson.Result
						for i, part := range parts {
							events = append(events, geminiPatchReviewEvents(t, ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini", []byte(request.request), nil, geminiPatchReviewFrame([]string{part}, i == len(parts)-1), &param))...)
						}
						counts := map[string]int{}
						var final gjson.Result
						for _, event := range events {
							counts[event.Get("type").String()]++
							if event.Get("type").String() == "response.completed" {
								final = event.Get("response")
							}
						}
						if counts["response.failed"] != 0 || counts["response.completed"] != 1 || counts["response.output_item.done"] != 3 || counts["response.custom_tool_call_input.done"] != 0 || param.(interface{ ToolInputError() error }).ToolInputError() != nil {
							t.Fatalf("ordinary cross-key legacy behavior changed: %v", events)
						}
						var nonStream any
						out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini", []byte(request.request), nil, geminiPatchReviewFrame(parts, true), &nonStream)
						for _, output := range []gjson.Result{final, gjson.ParseBytes(out)} {
							if output.Get("output.#").Int() != 3 {
								t.Fatalf("ordinary calls consolidated or rejected: %s", output.Raw)
							}
							for i, item := range output.Get("output").Array() {
								if item.Get("type").String() != "function_call" || item.Get("name").String() != names[i] || item.Get("arguments").String() != arguments[i] {
									t.Fatalf("ordinary winner changed: %s", item.Raw)
								}
							}
						}
						if state, ok := nonStream.(interface{ ToolInputError() error }); ok && state.ToolInputError() != nil {
							t.Fatal("ordinary non-stream call received patch error")
						}
					})
				}
			}
		}
	}
}
