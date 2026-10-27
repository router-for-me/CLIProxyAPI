package helps

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const syntheticAccounting = `{"provider":"cursor-next","scope":"run","record_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","state":"pending","replay":false,"resumed":true}`

func TestPluginRunAccountingStrictValidation(t *testing.T) {
	for _, test := range []struct {
		name, ref string
		valid     bool
	}{
		{"valid", syntheticAccounting, true},
		{"optional_flags", strings.ReplaceAll(strings.ReplaceAll(syntheticAccounting, `,"replay":false`, ""), `,"resumed":true`, ""), true},
		{"absent", `null`, false},
		{"array", `[]`, false},
		{"other_provider", strings.Replace(syntheticAccounting, "cursor-next", "other-plugin", 1), false},
		{"other_scope", strings.Replace(syntheticAccounting, `"run"`, `"request"`, 1), false},
		{"bad_state", strings.Replace(syntheticAccounting, "pending", "context_unknown", 1), false},
		{"missing_state", strings.Replace(syntheticAccounting, `,"state":"pending"`, "", 1), false},
		{"short_id", strings.Replace(syntheticAccounting, strings.Repeat("a", 64), "abc", 1), false},
		{"uppercase_id", strings.Replace(syntheticAccounting, strings.Repeat("a", 64), strings.Repeat("A", 64), 1), false},
		{"invalid_id", strings.Replace(syntheticAccounting, strings.Repeat("a", 64), strings.Repeat("g", 64), 1), false},
		{"numeric_state", strings.Replace(syntheticAccounting, `"pending"`, `1`, 1), false},
		{"string_flag", strings.Replace(syntheticAccounting, `false`, `"false"`, 1), false},
		{"null_flag", strings.Replace(syntheticAccounting, `true`, `null`, 1), false},
		{"extra_counter", strings.TrimSuffix(syntheticAccounting, `}`) + `,"total_tokens":9}`, false},
		{"duplicate_key", strings.TrimSuffix(syntheticAccounting, `}`) + `,"state":"measured"}`, false},
		{"oversized", strings.Replace(syntheticAccounting, "cursor-next", strings.Repeat("x", 513), 1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			observed := NewPluginUsageObservation("openai")
			observed.Observe([]byte(`{"choices":[{"finish_reason":"tool_calls"}],"usage_accounting":` + test.ref + `}`))
			ref := observed.Detail().UsageAccounting
			if (ref != nil) != test.valid {
				t.Fatalf("valid reference=%v, want %v: %+v", ref != nil, test.valid, ref)
			}
			patched := observed.RestoreResponses([]byte(`{"object":"response","status":"completed","usage":{"input_tokens":0,"output_tokens":0}}`))
			if gjson.GetBytes(patched, "usage_accounting").Exists() != test.valid || gjson.GetBytes(patched, "usage").Exists() {
				t.Fatalf("reference changed unknown standard usage: %s", patched)
			}
		})
	}
	for _, state := range []string{"pending", "running", "measured", "deferred", "unknown"} {
		if parsePluginUsageAccounting(gjson.Parse(strings.Replace(syntheticAccounting, "pending", state, 1))) == nil {
			t.Fatalf("supported state %s rejected", state)
		}
	}
}

func TestPluginRunAccountingTerminalOnlyAndRequestIsolation(t *testing.T) {
	for _, body := range []string{
		`{"object":"chat.completion.chunk","choices":[{"delta":{"content":"thinking"}}],"usage_accounting":` + syntheticAccounting + `}`,
		`{"type":"response.created","response":{"object":"response","status":"in_progress","usage_accounting":` + syntheticAccounting + `}}`,
		`{"type":"response.output_text.delta","delta":"thinking","usage_accounting":` + syntheticAccounting + `}`,
	} {
		observed := NewPluginUsageObservation("openai")
		observed.Observe([]byte(body))
		if observed.Detail().UsageAccounting != nil {
			t.Fatalf("accepted nonterminal reference: %s", body)
		}
	}
	first := NewPluginUsageObservation("openai")
	first.Observe([]byte(`{"object":"chat.completion","usage_accounting":` + syntheticAccounting + `}`))
	ref := first.Detail().UsageAccounting
	if ref == nil {
		t.Fatal("missing valid reference")
	}
	ref.RecordID = "mutated"
	if first.Detail().UsageAccounting.RecordID == "mutated" {
		t.Fatal("returned reference aliases request state")
	}
	second := NewPluginUsageObservation("openai")
	patched := second.RestoreResponses([]byte(`{"object":"response","status":"completed"}`))
	if second.Detail().UsageAccounting != nil || gjson.GetBytes(patched, "usage_accounting").Exists() {
		t.Fatalf("reference leaked into the next request: %s", patched)
	}
}

func TestPluginRunAccountingSurvivesStreamSplitsAndIndependentUsage(t *testing.T) {
	source := []byte(`data: {"choices":[{"finish_reason":"stop"}],"usage_accounting":` + syntheticAccounting + "}\n\n")
	for split := 0; split <= len(source); split++ {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			observed := NewPluginUsageObservation("openai")
			observed.Observe(source[:split])
			observed.Observe(source[split:])
			patched := observed.RestoreResponses([]byte(`data: {"type":"response.completed","response":{"status":"completed"}}`))
			if !gjson.GetBytes(ExtractStreamJSONPayload(patched), "response.usage_accounting.resumed").Bool() {
				t.Fatalf("reference lost after split %d: %s", split, patched)
			}
		})
	}
	for _, protocol := range []string{"openai", "openai-response"} {
		for _, tokensFirst := range []bool{false, true} {
			var buffer StreamUsageBuffer
			tokens := `data: {"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}}`
			ref := `data: {"choices":[{"finish_reason":"stop"}],"usage_accounting":` + syntheticAccounting + `}`
			if protocol == "openai-response" {
				ref = `data: {"type":"response.completed","response":{"usage_accounting":` + syntheticAccounting + `}}`
			}
			frames := []string{tokens, ref}
			if !tokensFirst {
				frames = []string{ref, tokens}
			}
			for _, frame := range frames {
				ObservePluginExecutorStreamUsage(protocol, []byte(frame), &buffer)
			}
			detail, ok := buffer.Detail()
			if !ok || detail.UsageAccounting == nil || detail.InputTokens != 10 || detail.OutputTokens != 4 || detail.TotalTokens != 14 {
				t.Fatalf("independent snapshots lost: protocol=%s tokensFirst=%v detail=%+v", protocol, tokensFirst, detail)
			}
		}
	}
}
