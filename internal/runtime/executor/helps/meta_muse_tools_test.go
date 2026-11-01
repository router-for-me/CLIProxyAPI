package helps

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestEnsureMetaMuseToolNamespacePreservesCallerTools(t *testing.T) {
	input := []byte(`{"input":"hello","tool_choice":"auto","tools":[{"type":"function","name":"muse","parameters":{"type":"object","properties":{"value":{"type":"integer"}}}},{"type":"namespace","name":"client","tools":[{"type":"function","name":"lookup"}]},{"type":"web_search"}]}`)
	original := bytes.Clone(input)
	got := EnsureMetaMuseToolNamespace(input)
	if !gjson.ValidBytes(got) {
		t.Fatalf("invalid JSON: %s", got)
	}
	tools := gjson.GetBytes(got, "tools").Array()
	before := gjson.GetBytes(input, "tools").Array()
	if len(tools) != len(before)+1 {
		t.Fatalf("tool count = %d, want %d", len(tools), len(before)+1)
	}
	namespace := tools[len(before)]
	if namespace.Get("type").String() != "namespace" || namespace.Get("name").String() != "muse" || namespace.Get("tools.0.name").String() != "submit_reminder_decision" {
		t.Fatalf("missing canonical namespace: %s", namespace.Raw)
	}
	for i, tool := range before {
		if tools[i].Raw != tool.Raw {
			t.Fatalf("caller tool %d changed: %s", i, tools[i].Raw)
		}
	}
	if gjson.GetBytes(got, "input").String() != "hello" || gjson.GetBytes(got, "tool_choice").String() != "auto" || !bytes.Equal(input, original) {
		t.Fatal("unrelated fields or caller buffer changed")
	}
	if again := EnsureMetaMuseToolNamespace(got); !bytes.Equal(again, got) {
		t.Fatal("second pass duplicated the namespace")
	}
}

func TestEnsureMetaMuseToolNamespaceLeavesUnrelatedBodiesUnchanged(t *testing.T) {
	for _, input := range []string{
		``, `{"input":"hello"}`, `{"tools":null}`, `{"tools":{}}`, `{"tools":[]}`,
		`{"tools":[{"type":"function","name":"lookup"},{"type":"namespace","name":"muse","description":"caller supplied","tools":[{"type":"function","name":"read_file"}]}]}`,
	} {
		if got := EnsureMetaMuseToolNamespace([]byte(input)); string(got) != input {
			t.Fatalf("body changed: %s -> %s", input, got)
		}
	}
}
