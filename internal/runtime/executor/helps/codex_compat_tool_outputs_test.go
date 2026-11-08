package helps

import (
	"bytes"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestNormalizeCodexCompatToolOutputsDoc(t *testing.T) {
	const prefix = `{"type":"function_call_output","id":"fco_context","name":"codex_apps_open_page","namespace":"agent",`
	for _, tc := range []struct {
		name      string
		item      string
		converted bool
	}{
		{"missing reference", prefix + `"output":"context"}`, true},
		{"empty reference", prefix + `"call_id":"","output":"context"}`, true},
		{"blank reference", prefix + `"call_id":" \t ","output":"context"}`, true},
		{"null reference", prefix + `"call_id":null,"output":"context"}`, true},
		{"blank aliases", prefix + `"call_id":" ","tool_call_id":"","callId":null,"output":"context"}`, true},
		{"empty output", prefix + `"output":""}`, true},
		{"item id is not a reference", `{"type":"function_call_output","id":"call_x","output":"context"}`, true},
		{"explicit reference", prefix + `"call_id":"call_previous","output":"context"}`, false},
		{"tool_call_id alias", prefix + `"call_id":" ","tool_call_id":"call_x","output":"context"}`, false},
		{"callId alias", prefix + `"callId":"call_x","output":"context"}`, false},
		{"invalid explicit reference", prefix + `"call_id":42,"output":"context"}`, false},
		{"structured output", prefix + `"output":[{"type":"input_image","image_url":"https://example.invalid/image"}]}`, false},
		{"object output", prefix + `"output":{"page_id":null}}`, false},
		{"missing output", prefix + `"call_id":null}`, false},
		{"custom tool output", `{"type":"custom_tool_call_output","name":"context","output":"context"}`, false},
		{"delegation create_thread", `{"type":"function_call_output","namespace":"codex_app","name":"create_thread","output":"context"}`, false},
		{"delegation send_message", `{"type":"function_call_output","namespace":"codex_app","name":"send_message_to_thread","output":"context"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := []byte(`{"model":"opaque-route","future":{"keep":true},"input":[{"type":"message","role":"user","content":"before"},` + tc.item + `,{"type":"message","role":"user","content":"after"}]}`)
			doc := ojson.NewDoc(original)
			NormalizeCodexCompatToolOutputsDoc(doc, false)
			if !bytes.Equal(doc.Bytes(), original) {
				t.Fatal("disabled compatibility changed the payload")
			}
			NormalizeCodexCompatToolOutputsDoc(doc, true)
			result := doc.Bytes()
			if !tc.converted {
				if !bytes.Equal(result, original) {
					t.Fatalf("unrelated item changed: %s", result)
				}
				return
			}
			item := doc.Get("input.1")
			if item.Get("type").String() != "message" || item.Get("role").String() != "user" || item.Get("content.0.type").String() != "input_text" {
				t.Fatalf("expected ordinary user text: %s", result)
			}
			source := ojson.Parse(tc.item)
			label := "agent.codex_apps_open_page"
			if source.Get("name").String() == "" {
				label = "tool"
			}
			if got := item.Get("content.0.text").String(); got != "Tool output from "+label+":\n"+source.Get("output").String() {
				t.Fatalf("context content changed: %q", got)
			}
			if item.Get("call_id").Exists() || item.Get("id").Exists() {
				t.Fatalf("tool identity leaked into user message: %s", result)
			}
			for _, path := range []string{"input.0", "input.2", "model", "future"} {
				if doc.Get(path).Raw != ojson.GetBytesNoCopy(original, path).Raw {
					t.Fatalf("unrelated field %s changed: %s", path, result)
				}
			}
			NormalizeCodexCompatToolOutputsDoc(doc, true)
			if !bytes.Equal(doc.Bytes(), result) {
				t.Fatal("repeated compatibility processing changed the user context")
			}
		})
	}
}

func TestNormalizeCodexCompatToolOutputsDocPreservesExactOutput(t *testing.T) {
	const text = " \n{\"page_id\":null}\n中文 context\t "
	doc := ojson.NewDocString(`{"input":[{"type":"function_call_output","name":"codex_apps_selected_page_part_1_of_1","output":""}]}`)
	_ = doc.Set("input.0.output", text)
	NormalizeCodexCompatToolOutputsDoc(doc, true)
	if got := doc.Get("input.0.content.0.text").String(); got != "Tool output from codex_apps_selected_page_part_1_of_1:\n"+text {
		t.Fatalf("output was trimmed or escaped incorrectly: %q", got)
	}
}
