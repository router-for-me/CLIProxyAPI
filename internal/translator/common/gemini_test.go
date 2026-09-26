package common

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestMergeAdjacentGeminiContents(t *testing.T) {
	t.Run("empty and single item", func(t *testing.T) {
		if got := MergeAdjacentGeminiContents(nil); len(got) != 0 {
			t.Fatalf("expected 0 items, got %d", len(got))
		}
		single := [][]byte{[]byte(`{"role":"user","parts":[{"text":"hello"}]}`)}
		if got := MergeAdjacentGeminiContents(single); len(got) != 1 {
			t.Fatalf("expected 1 item, got %d", len(got))
		}
	})

	t.Run("merges consecutive user turns", func(t *testing.T) {
		contents := [][]byte{
			[]byte(`{"role":"user","parts":[{"text":"first prompt"}]}`),
			[]byte(`{"role":"user","parts":[{"text":"<system-reminder>rule 1</system-reminder>"}]}`),
			[]byte(`{"role":"user","parts":[{"text":"<system-reminder>rule 2</system-reminder>"}]}`),
			[]byte(`{"role":"model","parts":[{"text":"assistant answer"}]}`),
			[]byte(`{"role":"user","parts":[{"text":"follow-up"}]}`),
		}

		merged := MergeAdjacentGeminiContents(contents)
		if len(merged) != 3 {
			t.Fatalf("expected 3 merged turns, got %d: %s", len(merged), JoinRawArray(merged))
		}

		// Turn 0: user with 3 parts
		turn0 := gjson.ParseBytes(merged[0])
		if turn0.Get("role").String() != "user" {
			t.Fatalf("expected role user, got %s", turn0.Get("role").String())
		}
		parts0 := turn0.Get("parts").Array()
		if len(parts0) != 3 {
			t.Fatalf("expected 3 parts in turn 0, got %d", len(parts0))
		}
		if parts0[0].Get("text").String() != "first prompt" ||
			parts0[1].Get("text").String() != "<system-reminder>rule 1</system-reminder>" ||
			parts0[2].Get("text").String() != "<system-reminder>rule 2</system-reminder>" {
			t.Fatalf("unexpected parts in turn 0: %v", parts0)
		}

		// Turn 1: model with 1 part
		turn1 := gjson.ParseBytes(merged[1])
		if turn1.Get("role").String() != "model" {
			t.Fatalf("expected role model, got %s", turn1.Get("role").String())
		}

		// Turn 2: user with 1 part
		turn2 := gjson.ParseBytes(merged[2])
		if turn2.Get("role").String() != "user" {
			t.Fatalf("expected role user, got %s", turn2.Get("role").String())
		}
	})

	t.Run("does not merge consecutive model turns to protect signature indices", func(t *testing.T) {
		contents := [][]byte{
			[]byte(`{"role":"user","parts":[{"text":"question"}]}`),
			[]byte(`{"role":"model","parts":[{"text":"thought","thought":true}]}`),
			[]byte(`{"role":"model","parts":[{"text":"answer"}]}`),
		}

		merged := MergeAdjacentGeminiContents(contents)
		if len(merged) != 3 {
			t.Fatalf("expected 3 turns (model turns kept unmerged), got %d: %s", len(merged), JoinRawArray(merged))
		}
	})

	t.Run("skips empty contents or contents with empty parts", func(t *testing.T) {
		contents := [][]byte{
			[]byte(``),
			[]byte(`{"role":"user","parts":[]}`),
			[]byte(`{"role":"user","parts":[{"text":"hello"}]}`),
		}
		merged := MergeAdjacentGeminiContents(contents)
		if len(merged) != 1 {
			t.Fatalf("expected 1 turn, got %d", len(merged))
		}
	})

	t.Run("merges consecutive user turns and reorders trailing text before functionResponse", func(t *testing.T) {
		contents := [][]byte{
			[]byte(`{"role":"user","parts":[{"functionResponse":{"name":"read","response":{"result":"ok"}}}]}`),
			[]byte(`{"role":"user","parts":[{"text":"<system-reminder>reminder</system-reminder>"}]}`),
		}
		merged := MergeAdjacentGeminiContents(contents)
		if len(merged) != 1 {
			t.Fatalf("expected 1 merged turn, got %d", len(merged))
		}
		parts := gjson.GetBytes(merged[0], "parts").Array()
		if len(parts) != 2 {
			t.Fatalf("expected 2 parts, got %d", len(parts))
		}
		if got := parts[0].Get("text").String(); got != "<system-reminder>reminder</system-reminder>" {
			t.Fatalf("expected parts[0] to be text, got %q", got)
		}
		if got := parts[1].Get("functionResponse.name").String(); got != "read" {
			t.Fatalf("expected parts[1] to be functionResponse, got %q", got)
		}
	})
}

func TestMergeAdjacentGeminiUserContents(t *testing.T) {
	t.Run("merges consecutive pure text user turns", func(t *testing.T) {
		contents := [][]byte{
			[]byte(`{"role":"user","parts":[{"text":"prompt 1"}]}`),
			[]byte(`{"role":"user","parts":[{"text":"prompt 2"}]}`),
		}
		merged := MergeAdjacentGeminiUserContents(contents)
		if len(merged) != 1 {
			t.Fatalf("expected 1 merged user turn, got %d", len(merged))
		}
		parts := gjson.GetBytes(merged[0], "parts").Array()
		if len(parts) != 2 {
			t.Fatalf("expected 2 parts, got %d", len(parts))
		}
	})

	t.Run("does not merge across functionResponse boundaries", func(t *testing.T) {
		contents := [][]byte{
			[]byte(`{"role":"user","parts":[{"functionResponse":{"name":"test","response":{"result":"ok"}}}]}`),
			[]byte(`{"role":"user","parts":[{"text":"user note"}]}`),
			[]byte(`{"role":"user","parts":[{"function_response":{"name":"test2","response":{"result":"ok2"}}}]}`),
		}
		merged := MergeAdjacentGeminiUserContents(contents)
		if len(merged) != 3 {
			t.Fatalf("expected 3 separate turns preserving functionResponse, got %d", len(merged))
		}
	})
}

func TestSplitGeminiFunctionResponseTurns(t *testing.T) {
	t.Run("splits mixed user turn with response first", func(t *testing.T) {
		input := [][]byte{
			[]byte(`{"role":"model","parts":[{"functionCall":{"name":"Bash"}}]}`),
			[]byte(`{"role":"user","parts":[{"text":"reminder"},{"functionResponse":{"name":"Bash"}}]}`),
		}
		got := SplitGeminiFunctionResponseTurns(input)
		if len(got) != 3 {
			t.Fatalf("got %d turns, want 3", len(got))
		}
		if !ContentHasGeminiFunctionResponse(got[1]) || ContentHasGeminiFunctionResponse(got[2]) {
			t.Fatalf("function response was not isolated before text: %s / %s", got[1], got[2])
		}
	})

	t.Run("keeps function response only turn intact", func(t *testing.T) {
		input := [][]byte{[]byte(`{"role":"user","parts":[{"functionResponse":{"name":"Bash"}}]}`)}
		got := SplitGeminiFunctionResponseTurns(input)
		if len(got) != 1 || string(got[0]) != string(input[0]) {
			t.Fatalf("got %s, want original turn", JoinRawArray(got))
		}
	})

	t.Run("handles multiple responses and alternate key", func(t *testing.T) {
		input := [][]byte{[]byte(`{"role":"user","parts":[{"text":"before"},{"functionResponse":{"name":"one"}},{"function_response":{"name":"two"}},{"text":"after"}]}`)}
		got := SplitGeminiFunctionResponseTurns(input)
		if len(got) != 2 || len(gjson.GetBytes(got[0], "parts").Array()) != 2 || len(gjson.GetBytes(got[1], "parts").Array()) != 2 {
			t.Fatalf("unexpected split: %s", JoinRawArray(got))
		}
		if !ContentHasGeminiFunctionResponse(got[0]) || ContentHasGeminiFunctionResponse(got[1]) {
			t.Fatalf("responses were not isolated: %s", JoinRawArray(got))
		}
	})

	t.Run("preserves non-user and empty turns", func(t *testing.T) {
		input := [][]byte{
			[]byte(`{"role":"model","parts":[{"functionResponse":{"name":"model-response"}}]}`),
			[]byte(`{"role":"user","parts":[]}`),
		}
		got := SplitGeminiFunctionResponseTurns(input)
		if len(got) != len(input) || string(got[0]) != string(input[0]) || string(got[1]) != string(input[1]) {
			t.Fatalf("turns were changed: %s", JoinRawArray(got))
		}
	})

	t.Run("splits each mixed turn independently", func(t *testing.T) {
		input := [][]byte{
			[]byte(`{"role":"user","parts":[{"text":"one"},{"functionResponse":{"name":"one"}}]}`),
			[]byte(`{"role":"user","parts":[{"text":"two"},{"functionResponse":{"name":"two"}}]}`),
		}
		got := SplitGeminiFunctionResponseTurns(input)
		if len(got) != 4 {
			t.Fatalf("got %d turns, want 4: %s", len(got), JoinRawArray(got))
		}
		for i := range got {
			if ContentHasGeminiFunctionResponse(got[i]) != (i%2 == 0) {
				t.Fatalf("unexpected turn %d: %s", i, got[i])
			}
		}
	})

	t.Run("hoists function response before intervening text reminder", func(t *testing.T) {
		input := [][]byte{
			[]byte(`{"role":"model","parts":[{"functionCall":{"name":"Bash"}}]}`),
			[]byte(`{"role":"user","parts":[{"text":"system reminder"}]}`),
			[]byte(`{"role":"user","parts":[{"functionResponse":{"name":"Bash"}}]}`),
		}
		got := SplitGeminiFunctionResponseTurns(input)
		if len(got) != 3 {
			t.Fatalf("got %d turns, want 3", len(got))
		}
		if !ContentHasGeminiFunctionResponse(got[1]) {
			t.Fatalf("expected function response immediately after model turn, got %s", got[1])
		}
		if gotText := gjson.GetBytes(got[2], "parts.0.text").String(); gotText != "system reminder" {
			t.Fatalf("expected text reminder after function response, got %s", got[2])
		}
	})

	t.Run("merges multiple function response turns into single turn following model", func(t *testing.T) {
		input := [][]byte{
			[]byte(`{"role":"model","parts":[{"functionCall":{"name":"A"}},{"functionCall":{"name":"B"}}]}`),
			[]byte(`{"role":"user","parts":[{"functionResponse":{"name":"A"}}]}`),
			[]byte(`{"role":"user","parts":[{"functionResponse":{"name":"B"}}]}`),
		}
		got := SplitGeminiFunctionResponseTurns(input)
		if len(got) != 2 {
			t.Fatalf("got %d turns, want 2", len(got))
		}
		if len(gjson.GetBytes(got[1], "parts").Array()) != 2 {
			t.Fatalf("expected 2 parts in single function response turn, got %s", got[1])
		}
	})
}

func TestReorderGeminiUserParts(t *testing.T) {
	t.Run("returns unchanged when no functionResponse", func(t *testing.T) {
		parts := [][]byte{
			[]byte(`{"text":"hello"}`),
			[]byte(`{"inline_data":{"mime_type":"image/png","data":"abc"}}`),
		}
		reordered := ReorderGeminiUserParts(parts)
		if len(reordered) != 2 || gjson.GetBytes(reordered[0], "text").String() != "hello" {
			t.Fatalf("unexpected parts: %v", reordered)
		}
	})

	t.Run("returns unchanged when text already precedes functionResponse", func(t *testing.T) {
		parts := [][]byte{
			[]byte(`{"text":"context"}`),
			[]byte(`{"functionResponse":{"name":"read","response":{"result":"ok"}}}`),
		}
		reordered := ReorderGeminiUserParts(parts)
		if len(reordered) != 2 || gjson.GetBytes(reordered[0], "text").String() != "context" {
			t.Fatalf("unexpected parts: %v", reordered)
		}
	})

	t.Run("reorders trailing text before functionResponse", func(t *testing.T) {
		parts := [][]byte{
			[]byte(`{"functionResponse":{"name":"read","response":{"result":"ok"}}}`),
			[]byte(`{"text":"<system-reminder>reminder</system-reminder>"}`),
		}
		reordered := ReorderGeminiUserParts(parts)
		if len(reordered) != 2 {
			t.Fatalf("expected 2 parts, got %d", len(reordered))
		}
		if got := gjson.GetBytes(reordered[0], "text").String(); got != "<system-reminder>reminder</system-reminder>" {
			t.Fatalf("expected parts[0] to be text, got %q", got)
		}
		if got := gjson.GetBytes(reordered[1], "functionResponse.name").String(); got != "read" {
			t.Fatalf("expected parts[1] to be functionResponse, got %q", got)
		}
	})

	t.Run("preserves relative order with multiple functionResponses and texts", func(t *testing.T) {
		parts := [][]byte{
			[]byte(`{"text":"leading"}`),
			[]byte(`{"functionResponse":{"name":"tool1","response":{"result":"1"}}}`),
			[]byte(`{"functionResponse":{"name":"tool2","response":{"result":"2"}}}`),
			[]byte(`{"text":"trailing"}`),
		}
		reordered := ReorderGeminiUserParts(parts)
		if len(reordered) != 4 {
			t.Fatalf("expected 4 parts, got %d", len(reordered))
		}
		if got := gjson.GetBytes(reordered[0], "text").String(); got != "leading" {
			t.Fatalf("parts[0] text = %q, want leading", got)
		}
		if got := gjson.GetBytes(reordered[1], "text").String(); got != "trailing" {
			t.Fatalf("parts[1] text = %q, want trailing", got)
		}
		if got := gjson.GetBytes(reordered[2], "functionResponse.name").String(); got != "tool1" {
			t.Fatalf("parts[2] name = %q, want tool1", got)
		}
		if got := gjson.GetBytes(reordered[3], "functionResponse.name").String(); got != "tool2" {
			t.Fatalf("parts[3] name = %q, want tool2", got)
		}
	})
}

func TestContainsJSONRef(t *testing.T) {
	tests := []struct {
		name string
		json string
		want bool
	}{
		{
			name: "top-level $ref object",
			json: `{"$ref": "#/components/schemas/ErrorModel"}`,
			want: true,
		},
		{
			name: "nested $ref in object",
			json: `{"responses":{"400":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/ErrorModel"}}}}}}`,
			want: true,
		},
		{
			name: "nested $ref in array",
			json: `[{"schema":{"$ref":"#/components/schemas/ErrorModel"}}]`,
			want: true,
		},
		{
			name: "plain object without $ref",
			json: `{"temperature": 72, "city": "Seattle"}`,
			want: false,
		},
		{
			name: "plain array without $ref",
			json: `[1, 2, {"name": "test"}]`,
			want: false,
		},
		{
			name: "$ref with non-string value is not a schema ref",
			json: `{"$ref": 123}`,
			want: false,
		},
		{
			name: "primitive string containing $ref text is not a schema ref",
			json: `"this is just a string containing $ref"`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainsJSONRef(gjson.Parse(tt.json))
			if got != tt.want {
				t.Errorf("ContainsJSONRef() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSetGeminiFunctionResponseResult(t *testing.T) {
	t.Run("preserves $ref as string under result", func(t *testing.T) {
		part := []byte(`{"functionResponse":{"name":"test"}}`)
		res := gjson.Parse(`{"schema":{"$ref":"#/components/schemas/ErrorModel"}}`)
		updated := SetGeminiFunctionResponseResult(part, "functionResponse.response.result", res)
		val := gjson.GetBytes(updated, "functionResponse.response.result")
		if val.Type != gjson.String {
			t.Fatalf("expected string type, got %s (raw: %s)", val.Type, val.Raw)
		}
	})

	t.Run("keeps non-$ref object as raw JSON under result", func(t *testing.T) {
		part := []byte(`{"functionResponse":{"name":"test"}}`)
		res := gjson.Parse(`{"ok":true,"code":200}`)
		updated := SetGeminiFunctionResponseResult(part, "functionResponse.response.result", res)
		val := gjson.GetBytes(updated, "functionResponse.response.result")
		if !val.IsObject() {
			t.Fatalf("expected object type, got %s (raw: %s)", val.Type, val.Raw)
		}
		if !val.Get("ok").Bool() {
			t.Fatalf("expected ok=true, got %v", val.Get("ok"))
		}
	})

	t.Run("places stringified $ref under .result when path ends with response", func(t *testing.T) {
		part := []byte(`{"functionResponse":{"name":"test"}}`)
		res := gjson.Parse(`{"schema":{"$ref":"#/components/schemas/ErrorModel"}}`)
		updated := SetGeminiFunctionResponseResult(part, "functionResponse.response", res)
		val := gjson.GetBytes(updated, "functionResponse.response.result")
		if val.Type != gjson.String {
			t.Fatalf("expected string type under response.result, got %s (raw: %s)", val.Type, val.Raw)
		}
	})
}
