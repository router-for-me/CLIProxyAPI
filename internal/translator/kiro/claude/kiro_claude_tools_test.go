package claude

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseInvokeBlock(t *testing.T) {
	tests := []struct {
		name       string
		block      string
		wantName   string
		wantParams map[string]interface{}
		wantOK     bool
	}{
		{
			name: "simple string params",
			block: `<invoke name="grepSearch">
<parameter name="query">卸载|uninstall</parameter>
<parameter name="includePattern">apps/desktop/**/*.{ts,tsx}</parameter>
</invoke>`,
			wantName: "grepSearch",
			wantParams: map[string]interface{}{
				"query":          "卸载|uninstall",
				"includePattern": "apps/desktop/**/*.{ts,tsx}",
			},
			wantOK: true,
		},
		{
			name: "json array param",
			block: `<invoke name="readMultipleFiles">
<parameter name="paths">["a.ts","b.ts"]</parameter>
</invoke>`,
			wantName: "readMultipleFiles",
			wantParams: map[string]interface{}{
				"paths": []interface{}{"a.ts", "b.ts"},
			},
			wantOK: true,
		},
		{
			name: "numeric param",
			block: `<invoke name="listDirectory">
<parameter name="path">src</parameter>
<parameter name="depth">2</parameter>
</invoke>`,
			wantName: "listDirectory",
			wantParams: map[string]interface{}{
				"path":  "src",
				"depth": float64(2),
			},
			wantOK: true,
		},
		{
			name: "antml namespace prefix",
			block: `<antml:invoke name="readFile">
<antml:parameter name="path">main.go</antml:parameter>
</antml:invoke>`,
			wantName:   "readFile",
			wantParams: map[string]interface{}{"path": "main.go"},
			wantOK:     true,
		},
		{
			name:     "missing name",
			block:    `<invoke><parameter name="x">y</parameter></invoke>`,
			wantName: "",
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, params, ok := parseInvokeBlock(tt.block)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if name != tt.wantName {
				t.Fatalf("name = %q, want %q", name, tt.wantName)
			}
			if !reflect.DeepEqual(params, tt.wantParams) {
				t.Fatalf("params = %#v, want %#v", params, tt.wantParams)
			}
		})
	}
}

func TestMapKiroNativeTool(t *testing.T) {
	tests := []struct {
		name       string
		nativeName string
		params     map[string]interface{}
		wantName   string
		wantInput  map[string]interface{}
	}{
		{
			name:       "readFile -> Read",
			nativeName: "readFile",
			params:     map[string]interface{}{"path": "a.ts"},
			wantName:   "Read",
			wantInput:  map[string]interface{}{"file_path": "a.ts"},
		},
		{
			name:       "fsRead -> Read",
			nativeName: "fsRead",
			params:     map[string]interface{}{"path": "a.ts"},
			wantName:   "Read",
			wantInput:  map[string]interface{}{"file_path": "a.ts"},
		},
		{
			name:       "readMultipleFiles -> Read (first path)",
			nativeName: "readMultipleFiles",
			params:     map[string]interface{}{"paths": []interface{}{"a.ts", "b.ts"}},
			wantName:   "Read",
			wantInput:  map[string]interface{}{"file_path": "a.ts"},
		},
		{
			name:       "grepSearch -> Grep with glob",
			nativeName: "grepSearch",
			params:     map[string]interface{}{"query": "foo", "includePattern": "apps/**"},
			wantName:   "Grep",
			wantInput:  map[string]interface{}{"pattern": "foo", "glob": "apps/**"},
		},
		{
			name:       "grepSearch -> Grep without glob",
			nativeName: "grepSearch",
			params:     map[string]interface{}{"query": "foo"},
			wantName:   "Grep",
			wantInput:  map[string]interface{}{"pattern": "foo"},
		},
		{
			name:       "fileSearch -> Glob",
			nativeName: "fileSearch",
			params:     map[string]interface{}{"query": "*.vue"},
			wantName:   "Glob",
			wantInput:  map[string]interface{}{"pattern": "*.vue"},
		},
		{
			name:       "listDirectory -> Bash ls",
			nativeName: "listDirectory",
			params:     map[string]interface{}{"path": "src"},
			wantName:   "Bash",
			wantInput:  map[string]interface{}{"command": "ls src"},
		},
		{
			name:       "listDirectory depth>1 -> Bash ls -R",
			nativeName: "listDirectory",
			params:     map[string]interface{}{"path": "src", "depth": float64(3)},
			wantName:   "Bash",
			wantInput:  map[string]interface{}{"command": "ls -R src"},
		},
		{
			name:       "writeFile -> Write",
			nativeName: "writeFile",
			params:     map[string]interface{}{"path": "a.ts", "content": "hello"},
			wantName:   "Write",
			wantInput:  map[string]interface{}{"file_path": "a.ts", "content": "hello"},
		},
		{
			name:       "strReplaceEditor -> Edit",
			nativeName: "strReplaceEditor",
			params:     map[string]interface{}{"path": "a.ts", "oldStr": "x", "newStr": "y"},
			wantName:   "Edit",
			wantInput:  map[string]interface{}{"file_path": "a.ts", "old_string": "x", "new_string": "y"},
		},
		{
			name:       "executeBash -> Bash",
			nativeName: "executeBash",
			params:     map[string]interface{}{"command": "ls -la"},
			wantName:   "Bash",
			wantInput:  map[string]interface{}{"command": "ls -la"},
		},
		{
			name:       "unknown passes through",
			nativeName: "someCustomTool",
			params:     map[string]interface{}{"a": "b"},
			wantName:   "someCustomTool",
			wantInput:  map[string]interface{}{"a": "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotName, gotInput := mapKiroNativeTool(tt.nativeName, tt.params)
			if gotName != tt.wantName {
				t.Fatalf("name = %q, want %q", gotName, tt.wantName)
			}
			if !reflect.DeepEqual(gotInput, tt.wantInput) {
				t.Fatalf("input = %#v, want %#v", gotInput, tt.wantInput)
			}
		})
	}
}

func TestParseInvokeToolCalls(t *testing.T) {
	t.Run("extracts invoke and cleans text", func(t *testing.T) {
		text := "Let me search.\n\n<invoke name=\"grepSearch\">\n<parameter name=\"query\">foo</parameter>\n</invoke>\n"
		clean, toolUses := ParseInvokeToolCalls(text, nil)
		if len(toolUses) != 1 {
			t.Fatalf("expected 1 tool use, got %d", len(toolUses))
		}
		if toolUses[0].Name != "Grep" {
			t.Fatalf("tool name = %q, want Grep", toolUses[0].Name)
		}
		if toolUses[0].Input["pattern"] != "foo" {
			t.Fatalf("pattern = %#v, want foo", toolUses[0].Input["pattern"])
		}
		if strings.Contains(clean, "invoke") {
			t.Fatalf("clean text still contains invoke markup: %q", clean)
		}
		if !strings.Contains(clean, "Let me search.") {
			t.Fatalf("clean text lost leading content: %q", clean)
		}
	})

	t.Run("multiple invokes", func(t *testing.T) {
		text := `a<invoke name="readFile"><parameter name="path">x</parameter></invoke>b<invoke name="fileSearch"><parameter name="query">y</parameter></invoke>c`
		clean, toolUses := ParseInvokeToolCalls(text, nil)
		if len(toolUses) != 2 {
			t.Fatalf("expected 2 tool uses, got %d", len(toolUses))
		}
		if toolUses[0].Name != "Read" || toolUses[1].Name != "Glob" {
			t.Fatalf("names = %q,%q want Read,Glob", toolUses[0].Name, toolUses[1].Name)
		}
		if clean != "abc" {
			t.Fatalf("clean = %q, want %q", clean, "abc")
		}
	})

	t.Run("no invoke returns input unchanged", func(t *testing.T) {
		text := "plain text with no tool calls"
		clean, toolUses := ParseInvokeToolCalls(text, nil)
		if clean != text {
			t.Fatalf("clean = %q, want unchanged %q", clean, text)
		}
		if len(toolUses) != 0 {
			t.Fatalf("expected 0 tool uses, got %d", len(toolUses))
		}
	})

	t.Run("dedupes identical calls", func(t *testing.T) {
		text := `<invoke name="readFile"><parameter name="path">x</parameter></invoke><invoke name="readFile"><parameter name="path">x</parameter></invoke>`
		_, toolUses := ParseInvokeToolCalls(text, map[string]bool{})
		if len(toolUses) != 1 {
			t.Fatalf("expected 1 deduped tool use, got %d", len(toolUses))
		}
	})

	t.Run("incomplete invoke left as text", func(t *testing.T) {
		text := `before <invoke name="readFile"><parameter name="path">x</parameter>`
		clean, toolUses := ParseInvokeToolCalls(text, nil)
		if len(toolUses) != 0 {
			t.Fatalf("expected 0 tool uses for incomplete invoke, got %d", len(toolUses))
		}
		if !strings.Contains(clean, "<invoke") {
			t.Fatalf("incomplete invoke should remain as text, got %q", clean)
		}
	})
}

func TestInvokeStreamParser(t *testing.T) {
	t.Run("single chunk", func(t *testing.T) {
		p := NewInvokeStreamParser(nil)
		text, tools := p.Feed(`hi <invoke name="readFile"><parameter name="path">x</parameter></invoke>`)
		if text != "hi " {
			t.Fatalf("text = %q, want %q", text, "hi ")
		}
		if len(tools) != 1 || tools[0].Name != "Read" {
			t.Fatalf("tools = %#v", tools)
		}
	})

	t.Run("across chunk boundaries", func(t *testing.T) {
		p := NewInvokeStreamParser(nil)
		var allText strings.Builder
		var allTools []KiroToolUse
		chunks := []string{
			"searching <inv",
			`oke name="grepSea`,
			`rch"><parameter name="qu`,
			`ery">foo|bar</par`,
			`ameter></inv`,
			`oke> done`,
		}
		for _, c := range chunks {
			text, tools := p.Feed(c)
			allText.WriteString(text)
			allTools = append(allTools, tools...)
		}
		text, tools := p.Flush()
		allText.WriteString(text)
		allTools = append(allTools, tools...)

		if len(allTools) != 1 {
			t.Fatalf("expected 1 tool, got %d (%#v)", len(allTools), allTools)
		}
		if allTools[0].Name != "Grep" || allTools[0].Input["pattern"] != "foo|bar" {
			t.Fatalf("tool = %#v", allTools[0])
		}
		got := allText.String()
		if strings.Contains(got, "invoke") {
			t.Fatalf("text leaked invoke markup: %q", got)
		}
		if !strings.Contains(got, "searching") || !strings.Contains(got, "done") {
			t.Fatalf("text missing surrounding content: %q", got)
		}
	})

	t.Run("incomplete invoke flushed as text", func(t *testing.T) {
		p := NewInvokeStreamParser(nil)
		p.Feed(`start <invoke name="readFile"><parameter name="path">x`)
		text, tools := p.Flush()
		if len(tools) != 0 {
			t.Fatalf("expected 0 tools, got %d", len(tools))
		}
		if !strings.Contains(text, "<invoke") {
			t.Fatalf("incomplete invoke should be flushed as text, got %q", text)
		}
	})

	t.Run("text only passes through", func(t *testing.T) {
		p := NewInvokeStreamParser(nil)
		text, tools := p.Feed("just some normal content")
		if text != "just some normal content" {
			t.Fatalf("text = %q", text)
		}
		if len(tools) != 0 {
			t.Fatalf("expected 0 tools, got %d", len(tools))
		}
	})
}
