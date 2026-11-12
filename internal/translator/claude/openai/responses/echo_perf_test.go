package responses

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func goldenHash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// normalizeEvents parses SSE output, zeroes volatile created_at fields, and
// re-marshals canonically so hashes compare structure, not timestamps.
func normalizeEvents(t *testing.T, raw []byte) []byte {
	t.Helper()
	var out []byte
	var zero func(v any) any
	zero = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				if k == "created_at" {
					x[k] = float64(0)
				} else {
					x[k] = zero(val)
				}
			}
			return x
		case []any:
			for i := range x {
				x[i] = zero(x[i])
			}
			return x
		default:
			return v
		}
	}
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if bytes.HasPrefix(line, []byte("data: ")) {
			var v any
			if err := json.Unmarshal(line[6:], &v); err != nil {
				t.Fatalf("unparseable event: %s", line)
			}
			canon, _ := json.Marshal(zero(v))
			out = append(out, canon...)
			out = append(out, '\n')
		} else {
			out = append(out, line...)
			out = append(out, '\n')
		}
	}
	return out
}

func claudeSSEFixture() [][]byte {
	return [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg-1","type":"message","role":"assistant","model":"claude-x","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"ponder"}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-1"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hello"}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"editor__read","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"/tmp/a\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":2}`),
		[]byte(`data: {"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"toolu_2","name":"plain","input":{}}}`),
		[]byte(`data: {"type":"content_block_stop","index":3}`),
		[]byte(`data: {"type":"content_block_start","index":4,"content_block":{"type":"tool_use","id":"toolu_3","name":"unknown_tool","input":{}}}`),
		[]byte(`data: {"type":"content_block_stop","index":4}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":10,"output_tokens":9}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}
}

func runClaudeStream(request []byte) []byte {
	var param any
	var out []byte
	for _, chunk := range claudeSSEFixture() {
		out = append(out, bytes.Join(ConvertClaudeResponseToOpenAIResponses(context.Background(), "m", request, request, chunk, &param), nil)...)
	}
	return out
}

func claudeEchoRequests() []struct {
	name string
	raw  []byte
} {
	history := strings.Repeat("context ", 4096)
	nsTools := `[{"type":"namespace","name":"editor","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]},{"type":"function","name":"plain","parameters":{"type":"object"}}]`
	return []struct {
		name string
		raw  []byte
	}{
		{"nil", nil},
		{"empty", []byte(`{}`)},
		{"full", []byte(`{"model":"gpt-5","instructions":"` + history + `","max_output_tokens":99,"parallel_tool_calls":false,"reasoning":{"effort":"high"},"store":true,"temperature":1,"text":{"format":{"type":"text"}},"tool_choice":"auto","tools":` + nsTools + `,"top_p":0.5,"metadata":{"b":2,"a":1},"input":[{"role":"user","content":"hi"}]}`)},
		{"flat-collision", []byte(`{"model":"gpt-5","tools":[{"type":"function","name":"editor__read","parameters":{"type":"object"}},{"type":"namespace","name":"editor","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}],"input":[]}`)},
		{"weird-types", []byte(`{"model":42,"instructions":null,"temperature":"hot","store":"yes","tools":"none","metadata":[3],"input":[]}`)},
		{"duplicate-keys", []byte(`{"model":"first","input":[],"model":"second","temperature":0.1,"temperature":0.9}`)},
		{"history-calls", []byte(`{"model":"gpt-5","tools":` + nsTools + `,"input":[{"type":"function_call","call_id":"c0","name":"read","namespace":"editor","arguments":"{}"},{"type":"function_call_output","call_id":"c0","output":"ok"}]}`)},
	}
}

// Golden hashes captured from the pre-optimization implementation; the
// cached namespace resolution and single-pass echo extraction must keep
// stream and non-stream outputs identical (modulo created_at).
var claudeEchoGoldenHashes = map[string][2]string{
	"nil":            {"ab4bf61de3306512dcde13cdabb426c55226319673cc766c18751d1f41009dad", "99221176860da000324aab10d71af05082ec1ea3752234a85adfa300283d24ee"},
	"empty":          {"ab4bf61de3306512dcde13cdabb426c55226319673cc766c18751d1f41009dad", "99221176860da000324aab10d71af05082ec1ea3752234a85adfa300283d24ee"},
	"full":           {"197061d4b6a78413a453999e3aa5f61f15d919c4496deea1b6d141ce925f664a", "4510b918b334ba8bedeb79ebd0ffcb158d99597edaf2b2c80d90cddf736a8959"},
	"flat-collision": {"3390b4761e3e41d9239ebe0f97572205540f23a206cfbe4c2948af615cda43c3", "06e538b3c37fa6e5e7aa1992a5f60401829e34752ba531fd58d216709a0635ef"},
	"weird-types":    {"e5915b3997f997f570eaa5a8a09dcf2ad4fdac0795ead2791018d2ebe986dba7", "b60a62ba215b1aea49ba6013215b3f035764e95fcf02fd9eb670f4dc340e413a"},
	"duplicate-keys": {"750b93f22f590508161a38465bda84e3237c87b70ffab02b7f19243e8b8f5242", "65ada4367a73723f249349931d495aa178fb7ea9030ae3ec82b7e578a2431052"},
	"history-calls":  {"cdf54a66b81bdab55f032612ecc6a0e2d5d8c500ac8849ab88c69c2373bf9ad6", "de0e606c25ee77a78c5cc8c0653d621836b168e284ccef11ecca13827aef2a52"},
}

func TestClaudeEchoGoldenOutputs(t *testing.T) {
	for _, tc := range claudeEchoRequests() {
		want := claudeEchoGoldenHashes[tc.name]
		stream := normalizeEvents(t, runClaudeStream(tc.raw))
		if got := goldenHash(stream); got != want[0] {
			t.Errorf("%s stream: hash %s, want %s", tc.name, got, want[0])
		}
		var blob []byte
		for _, c := range claudeSSEFixture() {
			blob = append(blob, c...)
			blob = append(blob, '\n')
		}
		var p any
		non := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "m", tc.raw, tc.raw, blob, &p)
		var v any
		if len(non) > 0 {
			if err := json.Unmarshal(non, &v); err != nil {
				t.Fatalf("%s: nonstream unparseable: %v", tc.name, err)
			}
			canon, _ := json.Marshal(zeroCreated(v))
			// created_at is time-based in non-stream too; normalize via map walk.
			non = canon
		}
		if got := goldenHash(non); got != want[1] {
			t.Errorf("%s nonstream: hash %s, want %s", tc.name, got, want[1])
		}
	}
}

func BenchmarkClaudeResponsesToolStreamLongRequest(b *testing.B) {
	history := strings.Repeat("context ", 81920)
	request := []byte(fmt.Sprintf(`{"model":"gpt-5","instructions":%q,"tools":[{"type":"namespace","name":"editor","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]},{"type":"function","name":"plain","parameters":{"type":"object"}}],"input":[{"role":"user","content":"hi"}]}`, history))
	chunks := claudeSSEFixture()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var param any
		for _, chunk := range chunks {
			ConvertClaudeResponseToOpenAIResponses(context.Background(), "m", request, request, chunk, &param)
		}
	}
}

func zeroCreated(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if k == "created_at" {
				x[k] = float64(0)
			} else {
				x[k] = zeroCreated(val)
			}
		}
		return x
	case []any:
		for i := range x {
			x[i] = zeroCreated(x[i])
		}
		return x
	default:
		return v
	}
}

func egHash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func egZero(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if k == "created_at" {
				x[k] = float64(0)
			} else {
				x[k] = egZero(val)
			}
		}
		return x
	case []any:
		for i := range x {
			x[i] = egZero(x[i])
		}
		return x
	default:
		return v
	}
}

func egNormalize(t *testing.T, raw []byte) []byte {
	t.Helper()
	var out []byte
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if bytes.HasPrefix(line, []byte("data: ")) {
			var v any
			if err := json.Unmarshal(line[6:], &v); err != nil {
				t.Fatalf("unparseable: %s", line)
			}
			canon, _ := json.Marshal(egZero(v))
			out = append(out, canon...)
		} else {
			out = append(out, line...)
		}
		out = append(out, '\n')
	}
	return out
}

func egChunks(names []string) [][]byte {
	out := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg-e","type":"message","role":"assistant","model":"c","content":[],"stop_reason":null,"usage":{"input_tokens":3,"output_tokens":1}}}`),
	}
	for i, name := range names {
		out = append(out,
			[]byte(fmt.Sprintf(`data: {"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":"toolu_%d","name":%q,"input":{}}}`, i, i, name)),
			[]byte(fmt.Sprintf(`data: {"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":"{}"}}`, i)),
			[]byte(fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, i)),
		)
	}
	out = append(out,
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":3,"output_tokens":4}}`),
		[]byte(`data: {"type":"message_stop"}`),
	)
	return out
}

// Edge cases for the cached namespace resolution: hashed/sanitized tool
// names and the client tool_search winner rules. Hashes captured from the
// pre-optimization implementation.
func TestClaudeEchoGoldenEdgeCases(t *testing.T) {
	want := map[string][2]string{
		"long-and-invalid-names": {"41307ab5fdb3c147802cc04c12c1aebc2136c4b17fb7e0b0eb21cb750f7bd50c", "0dbf5c12e8f78697d4cdd86260fd918f1710909434738a190b2d6490ead74bcc"},
		"toolsearch-on":          {"e57dcaf468458da7a240fe19c41a3d689d90191797ef6cc4d1f03e6ca4fdad3d", "3cc6b91c93b5789e52bdcf8bd9bf8a95308604d76fbc9e683e04306c5d2b3721"},
		"toolsearch-off":         {"fd981f216a4da138561d4379ea8236f60aab2fbb96b292bbe5b296677a7e0bf3", "6674a51b19acb3b834840494c1110eb7e998ca5a9b808bb2731ddb69a0555d07"},
	}
	longName := "ns__" + strings.Repeat("x", 90)
	dotted := "my.tool name"
	collideA := "a.b"
	collideB := "a_b"
	toolsJSON := fmt.Sprintf(`[{"type":"namespace","name":"editor","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]},{"type":"function","name":%q,"parameters":{"type":"object"}},{"type":"function","name":%q,"parameters":{"type":"object"}},{"type":"function","name":%q,"parameters":{"type":"object"}},{"type":"function","name":%q,"parameters":{"type":"object"}}]`, longName, dotted, collideA, collideB)
	searchTools := `[{"type":"tool_search","execution":"client"},{"type":"function","name":"deferred_tool","defer_loading":true,"parameters":{"type":"object"}},{"type":"function","name":"plain","parameters":{"type":"object"}}]`

	cases := []struct {
		name       string
		request    []byte
		toolSearch bool
		names      func(req []byte) []string
	}{
		{"long-and-invalid-names", []byte(`{"model":"gpt-5","tools":` + toolsJSON + `,"input":[]}`), false, func(req []byte) []string {
			names := buildClaudeToolNames(gjson.ParseBytes(req), false)
			return []string{names.claudeName(longName), names.claudeName(dotted), names.claudeName(collideA), names.claudeName(collideB), "editor__read"}
		}},
		{"toolsearch-on", []byte(`{"model":"gpt-5","tools":` + searchTools + `,"input":[]}`), true, func(req []byte) []string {
			return []string{"tool_search", "plain"}
		}},
		{"toolsearch-off", []byte(`{"model":"gpt-5","tools":` + searchTools + `,"input":[]}`), false, func(req []byte) []string {
			return []string{"tool_search", "plain", "deferred_tool"}
		}},
	}
	for _, tc := range cases {
		ctx := WithClientToolSearch(context.Background(), tc.toolSearch)
		chunks := egChunks(tc.names(tc.request))
		var blob []byte
		for _, c := range chunks {
			blob = append(blob, c...)
			blob = append(blob, '\n')
		}
		var param any
		var out []byte
		for _, c := range chunks {
			out = append(out, bytes.Join(ConvertClaudeResponseToOpenAIResponses(ctx, "m", tc.request, tc.request, c, &param), nil)...)
		}
		var p any
		non := ConvertClaudeResponseToOpenAIResponsesNonStream(ctx, "m", tc.request, tc.request, blob, &p)
		if len(non) > 0 {
			var v any
			if err := json.Unmarshal(non, &v); err != nil {
				t.Fatalf("%s nonstream unparseable: %v", tc.name, err)
			}
			non, _ = json.Marshal(egZero(v))
		}
		if got := egHash(egNormalize(t, out)); got != want[tc.name][0] {
			t.Errorf("%s stream: hash %s, want %s", tc.name, got, want[tc.name][0])
		}
		if got := egHash(non); got != want[tc.name][1] {
			t.Errorf("%s nonstream: hash %s, want %s", tc.name, got, want[tc.name][1])
		}
	}
}
