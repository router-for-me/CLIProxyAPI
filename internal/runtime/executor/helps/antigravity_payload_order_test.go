package helps

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestOrderAntigravityAgentPayload(t *testing.T) {
	tests := []struct{ name, raw, want string }{
		{
			name: "decoded spellings, duplicate keys and unknown extensions",
			raw:  `{"x-z":[{"z":1,"a":2}],"request":{"contents":[{"parts":[{"function_call":{"args":{"z":0,"\u0061":2,"a":3,"\u00e9":4,"b":5},"name":"tool","id":"call"}}],"r\u006fle":"user"}]},"x-a":{},"project":"example"}`,
			want: `{"project":"example","request":{"contents":[{"r\u006fle":"user","parts":[{"function_call":{"id":"call","name":"tool","args":{"\u0061":2,"a":3,"b":5,"z":0,"\u00e9":4}}}]}]},"x-z":[{"z":1,"a":2}],"x-a":{}}`,
		},
		{
			name: "partial argument records are not the tool arguments dictionary",
			raw:  `{"request":{"contents":[{"parts":[{"functionCall":{"partialArgs":[{"willContinue":true,"jsonPath":"$.x"}],"args":{"z":1,"a":2}}}]}]}}`,
			want: `{"request":{"contents":[{"parts":[{"functionCall":{"args":{"a":2,"z":1},"partialArgs":[{"willContinue":true,"jsonPath":"$.x"}]}}]}]}}`,
		},
		{
			name: "unranked fields still dispatch to schema and protocol modes",
			raw:  `{"request":{"x-z":{},"generation_config":{"thinkingConfig":{"thinkingBudget":1,"x-z":2,"thinkingLevel":"HIGH","x-a":3,"includeThoughts":true},"x-z":0,"response_json_schema":{"maxLength":4,"x-z":{},"type":"object","additionalProperties":{"description":"nested","type":"string"},"x-a":{}},"x-a":0},"labels":{},"x-a":{}}}`,
			want: `{"request":{"labels":{},"x-z":{},"generation_config":{"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"HIGH","thinkingBudget":1,"x-z":2,"x-a":3},"x-z":0,"response_json_schema":{"type":"object","maxLength":4,"x-z":{},"additionalProperties":{"type":"string","description":"nested"},"x-a":{}},"x-a":0},"x-a":{}}}`,
		},
		{
			name: "schema positions, map entries and scalar array order",
			raw:  `{"request":{"generation_config":{"response_json_schema":{"anyOf":[{"description":"first","type":"string"},{"items":[{"minimum":1,"type":"number"},false],"type":"array"}],"required":["z","type"],"properties":{"z":true,"type":{"enum":[3,1,2],"type":"integer"}},"type":"object"}}}}`,
			want: `{"request":{"generation_config":{"response_json_schema":{"type":"object","properties":{"type":{"type":"integer","enum":[3,1,2]},"z":true},"required":["z","type"],"anyOf":[{"type":"string","description":"first"},{"type":"array","items":[{"type":"number","minimum":1},false]}]}}}}`,
		},
		{
			name: "schema literals and definition names keep their input order",
			raw:  `{"request":{"generationConfig":{"responseSchema":{"default":{"z":1,"a":2},"$defs":{"Z":{"description":"Z","type":"string"},"A":{"enum":[{"y":3,"b":1}],"type":"object"}},"type":"object"}}}}`,
			want: `{"request":{"generationConfig":{"responseSchema":{"type":"object","default":{"z":1,"a":2},"$defs":{"Z":{"type":"string","description":"Z"},"A":{"type":"object","enum":[{"y":3,"b":1}]}}}}}}`,
		},
		{
			name: "separate edits under unchanged ancestors",
			raw:  `{"request":{"contents":[{"parts":[],"role":"user"},{"role":"model","parts":[{"functionCall":{"args":{"z":1,"a":2},"name":"one"}},{"functionCall":{"args":{"z":3,"a":4},"name":"two"}}]}],"labels":{"trajectory_id":"id","cascade_id":"id"}}}`,
			want: `{"request":{"contents":[{"role":"user","parts":[]},{"role":"model","parts":[{"functionCall":{"name":"one","args":{"a":2,"z":1}}},{"functionCall":{"name":"two","args":{"a":4,"z":3}}}]}],"labels":{"cascade_id":"id","trajectory_id":"id"}}}`,
		},
		{
			name: "whitespace and leaf bytes",
			raw:  "\r\n{\t\"request\" : { \"z\":{\"z\":0,\"a\":1}, \"contents\" : [ ]\n }, \"project\": \"x\\\"y\\\\\" }\n",
			want: `{"project":"x\"y\\","request":{"contents":[],"z":{"z":0,"a":1}}}`,
		},
		{
			name: "no changes", raw: `{"project":"example","request":{"contents":[],"labels":{"trajectory_id":"id"}},"x-z":null,"x-a":{}}`,
		},
		{
			name: "invalid JSON passes through", raw: `{"request":{}, "project":"broken"`,
		},
		{
			name: "deep extension does not disable ordering",
			raw:  `{"x-deep":` + strings.Repeat("[", 512) + `{"z":0,"a":1}` + strings.Repeat("]", 512) + `,"project":"example"}`,
			want: `{"project":"example","x-deep":` + strings.Repeat("[", 512) + `{"z":0,"a":1}` + strings.Repeat("]", 512) + `}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := ojson.NewDocString(tc.raw)
			want := tc.want
			if want == "" {
				want = tc.raw
			}
			got := SerializeAntigravityPayload(doc, true)
			if string(got) != want {
				t.Fatalf("ordered = %s, want %s", got, want)
			}
			if doc.String() != tc.raw {
				t.Fatal("serializer modified the source document")
			}
			if ojson.Valid(tc.raw) && !ojson.ValidBytes(got) {
				t.Fatal("serializer emitted invalid JSON")
			}
			if again := serializeOwnedAntigravityPayload(got, true); &again[0] != &got[0] {
				t.Fatal("ordering an already-ordered payload copied it")
			}
		})
	}
}

func TestOrderAntigravityAgentPayloadDeep(t *testing.T) {
	for _, depth := range []int{512, 5200} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			const contents = `"contents":[{"parts":[{"functionCall":{"args":`
			const labels = `"labels":{"cascade_id":"id","trajectory_id":"id"}`
			const inner = `{"z":1.20e+3,"a":"\u005c\/"}`
			const orderedInner = `{"a":"\u005c\/","z":1.20e+3}`
			raw := []byte(`{"request":{"labels":{"trajectory_id":"id","cascade_id":"id"},` + contents +
				strings.Repeat(`{"z":-0,"a":[`, depth) + inner + strings.Repeat(`]}`, depth) + `}}]}]},"project":"example"}`)
			want := []byte(`{"project":"example","request":{` + contents + strings.Repeat(`{"a":[`, depth) +
				orderedInner + strings.Repeat(`],"z":-0}`, depth) + `}}]}],` + labels + `}}`)
			if !ojson.ValidBytes(raw) || !ojson.ValidBytes(want) {
				t.Fatal("invalid deep test fixture")
			}
			if got := serializeOwnedAntigravityPayload(raw, true); !bytes.Equal(got, want) {
				prefixLen := min(90, len(got))
				t.Fatalf("expected ordering at the envelope, labels and every nested dictionary; got prefix %s", got[:prefixLen])
			}
			if got := serializeOwnedAntigravityPayload(want, true); &got[0] != &want[0] {
				t.Fatal("ordering an already-ordered deep payload copied it")
			}
		})
	}
}

func BenchmarkOrderAntigravityAgentPayloadSmall(b *testing.B) {
	want := []byte(`{"project":"example","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"labels":{"cascade_id":"c","trajectory_id":"t"}}}`)
	benchAntigravityOrder(b, "sorted", want, want)
	benchAntigravityOrder(b, "unsorted", []byte(`{"request":{"labels":{"trajectory_id":"t","cascade_id":"c"},"contents":[{"parts":[{"text":"hi"}],"role":"user"}]},"project":"example"}`), want)
}

func BenchmarkOrderAntigravityAgentPayload(b *testing.B) {
	want := antigravityOrderBenchPayload("sorted")
	for _, order := range []string{"mixed", "unsorted", "sorted"} {
		input := antigravityOrderBenchPayload(order)
		if len(input) < 80<<10 || len(input) > 90<<10 {
			b.Fatalf("invalid benchmark fixture length (%d bytes)", len(input))
		}
		benchAntigravityOrder(b, order, input, want)
	}
}

func benchAntigravityOrder(b *testing.B, order string, input, want []byte) {
	b.Run(order, func(b *testing.B) {
		if !ojson.ValidBytes(input) || bytes.Equal(input, want) != (order == "sorted") {
			b.Fatal("invalid benchmark fixture")
		}
		b.SetBytes(int64(len(input)))
		b.ReportAllocs()
		var got []byte
		for b.Loop() {
			got = serializeOwnedAntigravityPayload(input, true)
		}
		if !bytes.Equal(got, want) || !ojson.ValidBytes(got) {
			b.Fatal("ordered benchmark result does not match the canonical fixture")
		}
	})
}

// The fixture approximates a request with a long system instruction, replayed
// function calls and responses, and several large declaration schemas.
func antigravityOrderBenchPayload(order string) []byte {
	object := func(mixedReverse bool, fields ...string) string {
		if order == "unsorted" || order == "mixed" && mixedReverse {
			slices.Reverse(fields)
		}
		return "{" + strings.Join(fields, ",") + "}"
	}
	text := strings.Repeat(`synthetic line with an escaped quote \" and newline \n; `, 480)
	callData := strings.Repeat(`example argument value; `, 70)
	responseData := strings.Repeat(`synthetic result content; `, 70)
	toolDescription := strings.Repeat(`Example of a tool with inputs and output. `, 24)
	content := []string{object(false, `"role":"user"`, `"parts":[{"text":"start"}]`)}
	var tools []string
	for turn := range 3 {
		var calls, responses []string
		for call := range 2 + turn%2 {
			id := fmt.Sprintf(`"id":"call-%d-%d"`, turn, call)
			const name = `"name":"example"`
			args := object(true, `"a":[`+object(false, `"a":1`, `"z":2`)+`,3]`, `"z":"`+callData+`"`)
			result := object(true, `"a":{"details":true}`, `"z":"`+responseData+`"`)
			calls = append(calls, object(false, `"functionCall":`+object(false, id, name, `"args":`+args), `"thoughtSignature":"example"`))
			responses = append(responses, object(false, `"functionResponse":`+object(false, id, name, `"response":`+result)))
		}
		content = append(content, object(false, `"role":"model"`, `"parts":[`+strings.Join(calls, ",")+`]`))
		content = append(content, object(false, `"role":"user"`, `"parts":[`+strings.Join(responses, ",")+`]`))
	}
	for i := range 14 {
		properties := object(true,
			`"a":`+object(false, `"type":"string"`, `"description":"example"`, `"minLength":1`),
			`"z":`+object(false, `"type":"array"`, `"description":"`+toolDescription+`"`,
				`"items":`+object(false, `"type":"object"`, `"properties":{"value":{"type":"string"}}`, `"required":["value"]`), `"minItems":1`))
		tools = append(tools, object(false, fmt.Sprintf(`"name":"tool_%d"`, i), `"description":"`+toolDescription+`"`,
			`"parameters":`+object(true, `"type":"object"`, `"properties":`+properties, `"required":["z","a"]`)))
	}
	request := object(true, `"contents":[`+strings.Join(content, ",")+`]`,
		`"systemInstruction":`+object(false, `"role":"user"`, `"parts":[{"text":"`+text+`"}]`),
		`"tools":[{"functionDeclarations":[`+strings.Join(tools, ",")+`]}]`,
		`"labels":`+object(true, `"cascade_id":"example"`, `"last_step_index":"5"`, `"model_enum":"MODEL_PLACEHOLDER"`,
			`"request_id":"example"`, `"root_cascade_id":"example"`, `"trajectory_id":"example"`, `"used_claude":"false"`,
			`"used_claude_conservative":"false"`, `"used_non_gemini_model":"false"`),
		`"generationConfig":`+object(false, `"maxOutputTokens":2500`, `"thinkingConfig":`+object(false, `"includeThoughts":true`, `"thinkingLevel":"HIGH"`)),
		`"sessionId":"example"`)
	return []byte(object(true, `"project":"example"`, `"requestId":"example"`, `"request":`+request,
		`"model":"example"`, `"userAgent":"antigravity"`, `"requestType":"agent"`))
}
