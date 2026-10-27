package helps

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tiktoken-go/tokenizer"
)

func TestClientContextEstimateIsExplicitAndNeverObservedUsage(t *testing.T) {
	for _, test := range []struct {
		name, flag, ref, measured string
		configured, want          bool
	}{
		{"enabled", `true`, syntheticAccounting, "", true, true},
		{"default_off", `false`, syntheticAccounting, "", true, false},
		{"not_configured", `true`, syntheticAccounting, "", false, false},
		{"missing_flag", `null`, syntheticAccounting, "", true, false},
		{"string_flag", `"true"`, syntheticAccounting, "", true, false},
		{"missing_ref", `true`, `null`, "", true, false},
		{"invalid_ref", `true`, strings.Replace(syntheticAccounting, `"run"`, `"request"`, 1), "", true, false},
		{"measured", `true`, syntheticAccounting, `,"usage":{"prompt_tokens":11,"completion_tokens":2,"total_tokens":13}`, true, false},
		{"measured_zero", `true`, syntheticAccounting, `,"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			observed := NewPluginUsageObservation("openai")
			if test.configured {
				observed.ConfigureClientContextEstimate([]byte(`{"messages":[{"role":"system","content":"System instructions are part of current context."},{"role":"user","content":"synthetic user"}],"tools":[{"type":"function","function":{"name":"probe","parameters":{"type":"object"}}}]}`))
			}
			observed.Observe([]byte(`{"object":"chat.completion","choices":[{"message":{"content":"synthetic output"},"finish_reason":"stop"}],"usage_accounting":` + test.ref + `,"cursor_usage":{"client_context_estimate":` + test.flag + `}` + test.measured + `}`))
			before := observed.Detail()
			patched := observed.RestoreResponses([]byte(`{"object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"synthetic output"}]}]}`))
			if gjson.GetBytes(patched, "context_usage.estimated").Bool() != test.want {
				t.Fatalf("estimate eligibility changed: %s", patched)
			}
			if test.want {
				if gjson.GetBytes(patched, "usage.input_tokens").Int() <= 0 || gjson.GetBytes(patched, "usage.output_tokens").Int() <= 0 || gjson.GetBytes(patched, "context_usage.encoding").String() != "cl100k_base" || gjson.GetBytes(patched, "usage.input_tokens_details").Exists() {
					t.Fatalf("missing or misleading estimate metadata: %s", patched)
				}
			} else if test.measured == "" && gjson.GetBytes(patched, "usage").Exists() {
				t.Fatalf("estimate injected without authorization: %s", patched)
			}
			after := observed.Detail()
			if before.InputTokens != after.InputTokens || before.OutputTokens != after.OutputTokens || before.TokenBreakdown.Observed.InputTokens != after.TokenBreakdown.Observed.InputTokens {
				t.Fatalf("client estimate became a provider measurement: before=%+v after=%+v", before, after)
			}
			if test.measured == "" && (after.TokenBreakdown.Observed.InputTokens || after.TokenBreakdown.Observed.OutputTokens) {
				t.Fatalf("unknown provider counters became measured: %+v", after)
			}
			if test.measured != "" && gjson.GetBytes(patched, "usage.input_tokens").Int() != before.InputTokens {
				t.Fatalf("observed measurement overwritten: %s", patched)
			}
		})
	}
}

func TestClientContextEstimateCountsFullPromptAndBoundedCurrentOutput(t *testing.T) {
	codec, err := tokenizer.Get(tokenizer.Cl100kBase)
	if err != nil {
		t.Fatal(err)
	}
	base := []byte(`{"messages":[{"role":"system","content":"base instructions"},{"role":"user","content":"user text"}],"tools":[{"type":"custom","name":"probe","format":{"type":"grammar","definition":"A"}}]}`)
	more := []byte(strings.Replace(string(base), `"A"`, `"`+strings.Repeat("grammar rule ", 200)+`"`, 1))
	small, err := countClientContextPrompt(codec, base)
	if err != nil {
		t.Fatal(err)
	}
	large, err := countClientContextPrompt(codec, more)
	if err != nil || large <= small+100 {
		t.Fatalf("custom tool grammar missing from prompt estimate: small=%d large=%d err=%v", small, large, err)
	}
	items := gjson.Parse(`[{"type":"reasoning","summary":[{"text":"reason "}]},{"type":"message","content":[{"text":"text "}]},{"type":"function_call","arguments":"{\"v\":1}"},{"type":"custom_tool_call","input":"custom"}]`)
	output, ok := clientContextOutputText(items)
	if !ok || output != `reason text {"v":1}custom` {
		t.Fatalf("current response segment was not accumulated once: %q/%v", output, ok)
	}
	oversized := gjson.Parse(`[{"type":"custom_tool_call","input":"` + strings.Repeat("x", maxClientContextOutputBytes+1) + `"}]`)
	if _, ok := clientContextOutputText(oversized); ok {
		t.Fatal("unbounded output was retained for client estimation")
	}
	observed := NewPluginUsageObservation("openai")
	observed.ConfigureClientContextEstimate([]byte(`{"messages":[],"padding":"` + strings.Repeat("x", maxClientContextPromptBytes) + `"}`))
	if observed.contextEstimate != nil {
		t.Fatal("unbounded prompt was retained for client estimation")
	}
}
