package helps

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
)

type countingPayloadMarshaler struct {
	calls *int
	value string
}

func (m countingPayloadMarshaler) MarshalJSON() ([]byte, error) {
	*m.calls = *m.calls + 1
	return json.Marshal(m.value)
}

func TestSetStringIfDifferentReusesCanonicalValue(t *testing.T) {
	input := []byte(`{"model":"gpt-test","messages":[]}`)
	output := SetStringIfDifferent(input, "model", "gpt-test")
	if &output[0] != &input[0] {
		t.Fatal("canonical string caused a payload copy")
	}
}

func TestSetStringIfDifferentNormalizesWrongType(t *testing.T) {
	input := []byte(`{"model":123}`)
	original := bytes.Clone(input)
	output := SetStringIfDifferent(input, "model", "123")
	model := gjson.GetBytes(output, "model")
	if model.Type != gjson.String || model.String() != "123" {
		t.Fatalf("model = %s, want string 123", model.Raw)
	}
	if !bytes.Equal(input, original) {
		t.Fatal("input payload was modified in place")
	}
}

func TestSetBoolIfDifferentReusesCanonicalValue(t *testing.T) {
	input := []byte(`{"stream":true,"input":[]}`)
	output := SetBoolIfDifferent(input, "stream", true)
	if &output[0] != &input[0] {
		t.Fatal("canonical boolean caused a payload copy")
	}
}

func TestSetBoolIfDifferentNormalizesWrongType(t *testing.T) {
	input := []byte(`{"stream":"true"}`)
	output := SetBoolIfDifferent(input, "stream", true)
	if stream := gjson.GetBytes(output, "stream"); stream.Type != gjson.True {
		t.Fatalf("stream = %s, want boolean true", stream.Raw)
	}
}

func TestSetRawIfDifferentReusesIdenticalRawValue(t *testing.T) {
	input := []byte(`{"metadata":{"source":"executor"},"input":[]}`)
	output := SetRawIfDifferent(input, "metadata", []byte(`{"source":"executor"}`))
	if &output[0] != &input[0] {
		t.Fatal("identical raw value caused a payload copy")
	}
}

func TestSetRawIfDifferentUpdatesDifferentRawValue(t *testing.T) {
	input := []byte(`{"metadata":"executor"}`)
	output := SetRawIfDifferent(input, "metadata", []byte(`{"source":"executor"}`))
	metadata := gjson.GetBytes(output, "metadata")
	if !metadata.IsObject() || metadata.Get("source").String() != "executor" {
		t.Fatalf("metadata = %s, want object", metadata.Raw)
	}
}

func TestApplyPayloadConfigReusesCanonicalOverrides(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{Name: "gpt-test", Protocol: "openai"}},
			Params: map[string]any{"stream": true, "model": "gpt-test"},
		}},
		OverrideRaw: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{Name: "gpt-test", Protocol: "openai"}},
			Params: map[string]any{"metadata": `{"source":"executor"}`},
		}},
	}}
	input := []byte(`{"model":"gpt-test","stream":true,"metadata":{"source":"executor"},"messages":[]}`)
	output := ApplyPayloadConfigWithRoot(cfg, "gpt-test", "openai", "", input, nil, "", "")
	if &output[0] != &input[0] {
		t.Fatal("canonical payload overrides caused a payload copy")
	}
}

func TestApplyPayloadConfigWithRequestTrackedReportsContextManagementTouches(t *testing.T) {
	const automatic = `{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}`
	modelRules := []config.PayloadModelRule{{Name: "claude-opus-5", Protocol: "claude"}}
	originalWithoutContextManagement := []byte(`{"model":"claude-opus-5"}`)

	for _, test := range []struct {
		name          string
		payload       string
		original      []byte
		payloadConfig config.PayloadConfig
		wantTouched   bool
	}{
		{
			name:     "default",
			payload:  `{"model":"claude-opus-5"}`,
			original: originalWithoutContextManagement,
			payloadConfig: config.PayloadConfig{Default: []config.PayloadRule{{
				Models: modelRules,
				Params: map[string]any{"context_management": map[string]any{"edits": []any{map[string]any{"type": "default"}}}},
			}}},
			wantTouched: true,
		},
		{
			name:     "raw default",
			payload:  `{"model":"claude-opus-5"}`,
			original: originalWithoutContextManagement,
			payloadConfig: config.PayloadConfig{DefaultRaw: []config.PayloadRule{{
				Models: modelRules,
				Params: map[string]any{"context_management": `{"edits":[{"type":"raw_default"}]}`},
			}}},
			wantTouched: true,
		},
		{
			name:    "canonical descendant override",
			payload: `{"model":"claude-opus-5","context_management":` + automatic + `}`,
			payloadConfig: config.PayloadConfig{Override: []config.PayloadRule{{
				Models: modelRules,
				Params: map[string]any{"context_management.edits.0.keep": "all"},
			}}},
			wantTouched: true,
		},
		{
			name:    "identical raw override",
			payload: `{"model":"claude-opus-5","context_management":` + automatic + `}`,
			payloadConfig: config.PayloadConfig{OverrideRaw: []config.PayloadRule{{
				Models: modelRules,
				Params: map[string]any{"context_management": automatic},
			}}},
			wantTouched: true,
		},
		{
			name:    "filter already absent",
			payload: `{"model":"claude-opus-5"}`,
			payloadConfig: config.PayloadConfig{Filter: []config.PayloadFilterRule{{
				Models: modelRules,
				Params: []string{"context_management"},
			}}},
			wantTouched: true,
		},
		{
			name:    "unrelated override",
			payload: `{"model":"claude-opus-5"}`,
			payloadConfig: config.PayloadConfig{Override: []config.PayloadRule{{
				Models: modelRules,
				Params: map[string]any{"thinking.type": "enabled"},
			}}},
		},
		{
			name:    "nonmatching override",
			payload: `{"model":"claude-opus-5"}`,
			payloadConfig: config.PayloadConfig{Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "other-model", Protocol: "claude"}},
				Params: map[string]any{"context_management": map[string]any{"edits": []any{}}},
			}}},
		},
		{
			name:     "default skipped for caller owned field",
			payload:  `{"model":"claude-opus-5","context_management":{"edits":[{"type":"caller"}]}}`,
			original: []byte(`{"model":"claude-opus-5","context_management":{"edits":[{"type":"caller"}]}}`),
			payloadConfig: config.PayloadConfig{Default: []config.PayloadRule{{
				Models: modelRules,
				Params: map[string]any{"context_management": map[string]any{"edits": []any{map[string]any{"type": "default"}}}},
			}}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{Payload: test.payloadConfig}
			_, touched := ApplyPayloadConfigWithRequestTracked(cfg, "claude-opus-5", "claude", "claude", "", []byte(test.payload), test.original, "claude-opus-5", "", nil, "context_management")
			if touched != test.wantTouched {
				t.Fatalf("context_management touched = %t, want %t", touched, test.wantTouched)
			}
		})
	}
}

func TestApplyPayloadConfigProjectionOverrideWritesEveryMatch(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{Name: "gpt-test", Protocol: "openai"}},
			Params: map[string]any{"items.#.value": []any{1, 2}},
		}},
	}}
	input := []byte(`{"items":[{"value":1},{"value":2}]}`)
	output := ApplyPayloadConfigWithRoot(cfg, "gpt-test", "openai", "", input, nil, "", "")
	for _, path := range []string{"items.0.value", "items.1.value"} {
		if got := gjson.GetBytes(output, path).Raw; got != `[1,2]` {
			t.Fatalf("%s = %s, want [1,2]", path, got)
		}
	}
}

func TestApplyPayloadConfigProjectionOverrideRawWritesEveryMatch(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{
		OverrideRaw: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{Name: "gpt-test", Protocol: "openai"}},
			Params: map[string]any{"items.#.value": `[1,2]`},
		}},
	}}
	input := []byte(`{"items":[{"value":1},{"value":2}]}`)
	output := ApplyPayloadConfigWithRoot(cfg, "gpt-test", "openai", "", input, nil, "", "")
	for _, path := range []string{"items.0.value", "items.1.value"} {
		if got := gjson.GetBytes(output, path).Raw; got != `[1,2]` {
			t.Fatalf("%s = %s, want [1,2]", path, got)
		}
	}
}

func TestApplyPayloadConfigNormalizesByteSliceOverride(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{Name: "gpt-test", Protocol: "openai"}},
			Params: map[string]any{"value": []byte("abc")},
		}},
	}}
	input := []byte(`{"value":"YWJj"}`)
	output := ApplyPayloadConfigWithRoot(cfg, "gpt-test", "openai", "", input, nil, "", "")
	value := gjson.GetBytes(output, "value")
	if value.Type != gjson.String || value.String() != "abc" {
		t.Fatalf("value = %s, want string abc", value.Raw)
	}
}

func TestSetPayloadValueIfDifferentUsesSJSONNumberEncoding(t *testing.T) {
	input := []byte(`{"value":1.2}`)
	output := setPayloadValueIfDifferent(input, "value", float32(1.2))
	if got := gjson.GetBytes(output, "value").Raw; got != "1.2000000476837158" {
		t.Fatalf("value = %s, want sjson float32 encoding", got)
	}
	canonical := []byte(`{"value":1.2000000476837158}`)
	reused := setPayloadValueIfDifferent(canonical, "value", float32(1.2))
	if &reused[0] != &canonical[0] {
		t.Fatal("canonical float32 encoding caused a payload copy")
	}
}

func TestSetPayloadValueIfDifferentCallsMarshalerOnce(t *testing.T) {
	for _, input := range [][]byte{[]byte(`{"value":"old"}`), []byte(`{"value":"new"}`)} {
		calls := 0
		value := countingPayloadMarshaler{calls: &calls, value: "new"}
		output := setPayloadValueIfDifferent(input, "value", value)
		if calls != 1 {
			t.Fatalf("MarshalJSON calls = %d, want 1", calls)
		}
		if got := gjson.GetBytes(output, "value").String(); got != "new" {
			t.Fatalf("value = %q, want new", got)
		}
	}
}

func TestRemoveToolTypeReusesArrayWithoutMatch(t *testing.T) {
	input := []byte(`{"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`)
	output := removeToolTypeFromToolsArray(input, "tools", "image_generation")
	if &output[0] != &input[0] {
		t.Fatal("tool filtering without a match caused a payload copy")
	}
}

var benchmarkPayloadMutationOutput []byte

func BenchmarkSetStringIfDifferentLargeCanonicalPayload(b *testing.B) {
	input := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"` + strings.Repeat("x", 8<<20) + `"}]}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for b.Loop() {
		benchmarkPayloadMutationOutput = SetStringIfDifferent(input, "model", "gpt-test")
	}
}
