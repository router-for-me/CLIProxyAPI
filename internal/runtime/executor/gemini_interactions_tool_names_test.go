package executor

import (
	"context"
	"fmt"
	"strings"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestIssue5190ToolNamesAcrossChatAndResponses(t *testing.T) {
	const model = "antigravity-preview-05-2026"
	for _, format := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse} {
		t.Run(format.String(), func(t *testing.T) {
			original := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}},{"type":"function","function":{"name":"external_read_file","parameters":{"type":"object"}}}]}`)
			if format == sdktranslator.FormatOpenAIResponse {
				original = []byte(`{"input":"hi","tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}},{"type":"function","name":"external_read_file","parameters":{"type":"object"}}]}`)
			}
			request := sdktranslator.TranslateRequest(format, sdktranslator.FormatInteractions, model, original, false)
			for i, upstream := range []string{"external_read_file", "external_external_read_file"} {
				if got := gjson.GetBytes(request, fmt.Sprintf("tools.%d.name", i)).String(); got != upstream {
					t.Fatalf("upstream tool=%q want=%q: %s", got, upstream, request)
				}
				name := []string{"read_file", "external_read_file"}[i]
				raw := []byte(fmt.Sprintf(`{"id":"interaction_1","model":%q,"status":"requires_action","steps":[{"type":"function_call","id":"call_1","name":%q,"arguments":{}}]}`, model, upstream))
				var param any
				out := sdktranslator.TranslateNonStream(context.Background(), sdktranslator.FormatInteractions, format, model, original, request, raw, &param)
				path := "choices.0.message.tool_calls.0.function.name"
				if format == sdktranslator.FormatOpenAIResponse {
					path = "output.0.name"
				}
				if got := gjson.GetBytes(out, path).String(); got != name {
					t.Errorf("nonstream tool=%q want=%q: %s", got, name, out)
				}
				param = nil
				events := []string{
					fmt.Sprintf(`{"event_type":"interaction.created","interaction":{"id":"interaction_1","model":%q}}`, model),
					fmt.Sprintf(`{"event_type":"step.start","index":0,"step":{"type":"function_call","id":"call_1","name":%q,"arguments":{}}}`, upstream),
					`{"event_type":"step.stop","index":0}`,
					`{"event_type":"interaction.completed","interaction":{"id":"interaction_1","status":"requires_action"}}`,
				}
				found := false
				for _, event := range events {
					for _, chunk := range sdktranslator.TranslateStream(context.Background(), sdktranslator.FormatInteractions, format, model, original, request, []byte("data: "+event), &param) {
						for _, line := range strings.Split(string(chunk), "\n") {
							line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
							path := "choices.0.delta.tool_calls.0.function.name"
							if format == sdktranslator.FormatOpenAIResponse {
								path = "item.name"
							}
							if got := gjson.Get(line, path); got.Exists() {
								found = true
								if got.String() != name {
									t.Errorf("stream tool=%q want=%q: %s", got.String(), name, line)
								}
							}
						}
					}
				}
				if !found {
					t.Error("missing streamed tool name")
				}
			}
		})
	}
}
