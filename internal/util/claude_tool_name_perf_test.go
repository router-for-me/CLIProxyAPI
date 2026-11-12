package util

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
)

// The legacy mapper is kept as a differential oracle for collision semantics.
func TestUnambiguousClaudeToolNames(t *testing.T) {
	requests := []string{
		``, `{`, `null`, `{"tools":null}`, `{"tools":{}}`,
		`{"tools":[{"name":"Read"}]}`,
		`{"tools":[{"name":"Read"},{"name":"read"},{"name":"_Read"}]}`,
		`{"tools":[{"name":"Read"},{"name":"Read"}]}`,
		`{"tools":[{"name":" " ,"function":{"name":"Read"}},{"name":"___"},{"name":"读取"}]}`,
		`{"tools":[{"name":" Read "},{"function":{"name":"Write"}}],"tools":[]}`,
	}
	for _, request := range requests {
		legacy := ToolNameMapFromClaudeRequest([]byte(request))
		cached := UnambiguousToolNameMapFromClaudeRequest([]byte(request))
		for _, name := range []string{"", "Read", "read", "READ", "_Read", "__read", " Read ", "Write", "write", "读取", "missing", "___"} {
			want := MapClaudeToolName(legacy, []byte(request), name)
			if got := MapToolName(cached, name); got != want {
				t.Errorf("request=%s name=%q got=%q want=%q", request, name, got, want)
			}
		}
	}
}

func TestUnambiguousClaudeToolNamesRandomized(t *testing.T) {
	rng := rand.New(rand.NewSource(6816))
	names := []string{"Read", "read", "READ", "_Read", "__read", "Write", "write", "读取", "", "___", " Read "}
	for i := 0; i < 500; i++ {
		tools := make([]map[string]any, rng.Intn(12))
		for j := range tools {
			name := names[rng.Intn(len(names))]
			if rng.Intn(2) == 0 {
				tools[j] = map[string]any{"name": name}
			} else {
				tools[j] = map[string]any{"function": map[string]string{"name": name}}
			}
		}
		request, err := json.Marshal(map[string]any{"tools": tools})
		if err != nil {
			t.Fatal(err)
		}
		legacy := ToolNameMapFromClaudeRequest(request)
		cached := UnambiguousToolNameMapFromClaudeRequest(request)
		for _, name := range names {
			if got, want := MapToolName(cached, name), MapClaudeToolName(legacy, request, name); got != want {
				t.Fatalf("request=%s name=%q got=%q want=%q", request, name, got, want)
			}
		}
	}
}

var claudeToolNameBenchmarkSink string

func BenchmarkClaudeToolNameLongRequest(b *testing.B) {
	request := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("context ", 81920) + `"}],"tools":[{"name":"Read"},{"name":"Write"}]}`)
	b.Run("legacy", func(b *testing.B) {
		names := ToolNameMapFromClaudeRequest(request)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			claudeToolNameBenchmarkSink = MapClaudeToolName(names, request, "Read")
		}
	})
	b.Run("cached", func(b *testing.B) {
		names := UnambiguousToolNameMapFromClaudeRequest(request)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			claudeToolNameBenchmarkSink = MapToolName(names, "Read")
		}
	})
}
