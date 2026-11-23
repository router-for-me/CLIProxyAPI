package helps

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	toolschema "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/tool-schema"
)

const codexBatchUnionTool = `{"type":"function","name":"choose","parameters":{"type":"object","properties":{"action":{"oneOf":[{"const":"a"},{"const":"b"},{"const":"c"},{"const":"d"},{"const":"e"},{"const":"f"},{"const":"g"},{"const":"h"}]}}}}`
const codexBatchSimpleTool = `{"type":"function","name":"read","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}`

func TestNormalizeCodexToolSchemasMatchesSequentialEdits(t *testing.T) {
	namespace := func(tools string) string {
		return `{"type":"namespace", "name":"workspace", "tools": [ ` + tools + ` ]}`
	}
	request := func(tools string) string {
		return "{\r\n  \"input\": \"tool_search \\u5de5\", \"tools\": [\n\t" + tools + "\n  ], \"metadata\": {\"number\": 900719925474099312345, \"value\": 1.00e+9}\r\n}"
	}
	inputs := []string{
		"", `{}`, `{"tools":null}`, `{"tools":[]}`, `{"tools":"unchanged"}`, `{"tools":{}}`,
		request(codexBatchSimpleTool),
		request(codexBatchUnionTool),
		request(codexBatchSimpleTool + ", \n" + codexBatchUnionTool + ",\t" + codexBatchSimpleTool),
		request(codexBatchUnionTool + ", \n" + codexBatchSimpleTool + ",\t" + codexBatchUnionTool),
		request(namespace(codexBatchUnionTool + ", \n" + codexBatchSimpleTool + ",\t" + codexBatchUnionTool)),
		request(namespace(namespace(codexBatchUnionTool)+",\t"+codexBatchUnionTool) + ",\n" + codexBatchUnionTool),
		request(`null, 123, {"type":"namespace","tools":[]}, ` + codexBatchUnionTool),
		`{"tools":[` + codexBatchUnionTool + `],"tools":[` + codexBatchSimpleTool + `]}`,
		`{"tools":[` + codexBatchUnionTool + `],"incomplete":`,
		`{"tools":[` + codexBatchUnionTool + `,`,
	}
	for index, input := range inputs {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			body := []byte(input)
			want := codexSequentialToolSchemas(body)
			got := NormalizeCodexToolSchemas(body)
			if !bytes.Equal(got, want) {
				t.Fatalf("output differs from sequential edits:\ngot  %s\nwant %s", got, want)
			}
			if string(body) != input {
				t.Fatal("normalization mutated caller payload")
			}
			if bytes.Equal(got, body) && len(body) > 0 && &got[0] != &body[0] {
				t.Fatal("unchanged payload was copied")
			}
		})
	}
}

func BenchmarkNormalizeCodexToolSchemas(b *testing.B) {
	for _, tt := range []struct {
		name      string
		count     int
		namespace bool
		change    bool
	}{
		{name: "flat_1", count: 1, change: true},
		{name: "flat_32", count: 32, change: true},
		{name: "flat_128", count: 128, change: true},
		{name: "namespace_128", count: 128, namespace: true, change: true},
		{name: "unchanged_128", count: 128},
	} {
		b.Run(tt.name, func(b *testing.B) {
			tool := codexBatchSimpleTool
			if tt.change {
				tool = codexBatchUnionTool
			}
			var declarations []string
			for index := 0; index < tt.count; index++ {
				declarations = append(declarations, strings.Replace(tool, `"name":"`, fmt.Sprintf(`"name":"tool_%d_`, index), 1))
			}
			tools := strings.Join(declarations, ",")
			if tt.namespace {
				tools = `{"type":"namespace","name":"workspace","tools":[` + tools + `]}`
			}
			body := []byte(`{"input":"` + strings.Repeat("x", 1<<20) + `","tools":[` + tools + `]}`)
			want := codexSequentialToolSchemas(body)
			if got := NormalizeCodexToolSchemas(body); !bytes.Equal(got, want) {
				b.Fatal("normalization differs from sequential edits")
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for b.Loop() {
				NormalizeCodexToolSchemas(body)
			}
		})
	}
}

// Frozen pre-batching traversal: compare exact bytes and benchmark the same
// input before/after without changing the schema normalization policy.
func codexSequentialToolSchemas(body []byte) []byte {
	tools := ojson.GetBytesNoCopy(body, "tools")
	if !tools.IsArray() {
		return body
	}
	doc := ojson.NewDoc(body)
	changed := false
	for index, tool := range tools.Array() {
		if updated, toolChanged := codexSequentialTool(tool); toolChanged {
			if errSet := doc.SetRawBytes(fmt.Sprintf("tools.%d", index), updated); errSet == nil {
				changed = true
			}
		}
	}
	if !changed {
		return body
	}
	return doc.Bytes()
}

func codexSequentialTool(tool ojson.Result) ([]byte, bool) {
	if tool.Get("type").String() != "namespace" {
		return toolschema.NormalizeSingleCodexTool(tool)
	}
	nested := tool.Get("tools")
	if !nested.IsArray() || len(nested.Array()) == 0 {
		return nil, false
	}
	doc := ojson.NewDocString(tool.Raw)
	changed := false
	for index, child := range nested.Array() {
		if updated, childChanged := codexSequentialTool(child); childChanged {
			if errSet := doc.SetRawBytes(fmt.Sprintf("tools.%d", index), updated); errSet == nil {
				changed = true
			}
		}
	}
	if !changed {
		return nil, false
	}
	return doc.Bytes(), true
}
