package responses

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const namespaceRecoveryRequest = `{"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec"},{"type":"function","name":"wait"}]}]}]}`

func TestCustomToolNamespaceRecoveryPreservesStreamAndNonStream(t *testing.T) {
	for _, name := range []string{"functions__exec", "exec"} {
		t.Run(name, func(t *testing.T) {
			request := []byte(namespaceRecoveryRequest)
			args := `{"input":"text(\"测试\\n\");"}`
			call := fmt.Sprintf(`{"index":0,"id":"call_fixture","type":"function","function":{"name":%q,"arguments":%q}}`, name, args)
			raw := fmt.Sprintf(`{"id":"fixture","choices":[{"index":0,"message":{"tool_calls":[%s]},"finish_reason":"tool_calls"}]}`, call)
			response := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "fixture", request, nil, []byte(raw), nil)
			assertRecoveredCustomCall(t, gjson.GetBytes(response, "output.0"), true)
			var state any
			counts := map[string]int{}
			for _, line := range []string{
				fmt.Sprintf(`data: {"id":"fixture","choices":[{"index":0,"delta":{"tool_calls":[%s]},"finish_reason":"tool_calls"}]}`, call),
				"data: [DONE]",
			} {
				for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "fixture", request, nil, []byte(line), &state) {
					event, data := parseOpenAIResponsesSSEEvent(t, chunk)
					counts[event]++
					switch event {
					case "response.output_item.added", "response.output_item.done":
						assertRecoveredCustomCall(t, data.Get("item"), event == "response.output_item.done")
					case "response.completed":
						assertRecoveredCustomCall(t, data.Get("response.output.0"), true)
					default:
						if strings.HasPrefix(event, "response.function_call_arguments.") {
							t.Fatalf("custom call downgraded: %s", chunk)
						}
					}
				}
			}
			for _, event := range []string{"response.output_item.added", "response.output_item.done", "response.completed", "response.custom_tool_call_input.done"} {
				if counts[event] != 1 {
					t.Fatalf("%s count = %d", event, counts[event])
				}
			}
		})
	}
}

func assertRecoveredCustomCall(t *testing.T, item gjson.Result, withInput bool) {
	t.Helper()
	for key, want := range map[string]string{"type": "custom_tool_call", "name": "exec", "namespace": "functions", "call_id": "call_fixture"} {
		if got := item.Get(key).String(); got != want {
			t.Fatalf("%s = %q, want %q; item=%s", key, got, want, item)
		}
	}
	if withInput && item.Get("input").String() != "text(\"测试\\n\");" {
		t.Fatalf("input changed: %s", item)
	}
	if item.Get("arguments").Exists() {
		t.Fatal("custom call retained JSON arguments")
	}
}

func TestCustomToolReplayPreservesNamespaceAndResultPair(t *testing.T) {
	for _, namespace := range []string{`,"namespace":"functions"`, ""} {
		request := strings.TrimSuffix(namespaceRecoveryRequest, "]}") + fmt.Sprintf(`,{"type":"custom_tool_call","name":"exec"%s,"call_id":"call_fixture","input":"text(1);"},{"type":"custom_tool_call_output","call_id":"call_fixture","output":[{"type":"input_text","text":"1"}]}]}`, namespace)
		body := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("fixture", []byte(request), false)
		if got := gjson.GetBytes(body, "messages.0.tool_calls.0.function.name").String(); got != "functions__exec" {
			t.Fatalf("replay name %q: %s", got, body)
		}
		if got := gjson.GetBytes(body, "messages.1.tool_call_id").String(); got != "call_fixture" {
			t.Fatalf("result pair lost: %s", body)
		}
	}
}

func TestNamespaceRecoveryDoesNotGuessAmbiguousOrOverrideExactNames(t *testing.T) {
	for _, tt := range []struct{ request, name, want string }{
		{namespaceRecoveryRequest, "wait", "functions__wait"},
		{namespaceRecoveryRequest, "unknown", "unknown"},
		{`{"tools":[{"type":"function","name":"exec"},{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec"}]}]}`, "exec", "exec"},
		{`{"tools":[{"type":"namespace","name":"first","tools":[{"type":"custom","name":"exec"}]},{"type":"namespace","name":"second","tools":[{"type":"custom","name":"exec"}]}]}`, "exec", "exec"},
	} {
		if got := canonicalResponsesToolName([]byte(tt.request), tt.name); got != tt.want {
			t.Fatalf("got %q, want %q", got, tt.want)
		}
	}
}
