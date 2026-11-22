package executor

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSanitizeKiroPayload(t *testing.T) {
	in := `{"model":"deepseek-3.2","stream":true,"temperature":0.2,"top_p":1,"tool_choice":"auto",
	"text":{"verbosity":"low"},"instructions":"be brief",
	"tools":[{"type":"function","name":"read_file","strict":true,"parameters":{"type":"object"}},{"type":"web_search"}],
	"input":[{"role":"developer","content":[{"type":"input_text","text":"dev rule"}]},
	{"type":"message","role":"user","content":"hi"}]}`
	var out map[string]any
	if err := json.Unmarshal(sanitizeKiroPayload([]byte(in)), &out); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"temperature", "top_p", "tool_choice", "text", "instructions"} {
		if _, ok := out[k]; ok {
			t.Fatalf("%s should be removed", k)
		}
	}
	tools := out["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %#v", tools)
	}
	if _, ok := tools[0].(map[string]any)["strict"]; ok {
		t.Fatal("strict should be removed")
	}
	input := out["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("input = %#v", input)
	}
	msg := input[0].(map[string]any)
	if msg["role"] != "user" || msg["type"] != nil {
		t.Fatalf("msg = %#v", msg)
	}
	c := msg["content"].(string)
	if !strings.Contains(c, "be brief") || !strings.Contains(c, "dev rule") || !strings.HasSuffix(c, "hi") {
		t.Fatalf("content = %q", c)
	}
}
