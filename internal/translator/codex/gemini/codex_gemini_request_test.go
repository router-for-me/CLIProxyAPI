package gemini

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiRequestToCodex_PreservesCustomCallIDs(t *testing.T) {
	tests := []struct {
		name          string
		callField     string
		responseField string
		want          string
	}{
		{
			name:          "id",
			callField:     `"id":"call_gateway_id"`,
			responseField: `"id":"call_gateway_id"`,
			want:          "call_gateway_id",
		},
		{
			name:          "call_id",
			callField:     `"call_id":"call_gateway_call_id"`,
			responseField: `"call_id":"call_gateway_call_id"`,
			want:          "call_gateway_call_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{
				"contents": [
					{
						"role": "model",
						"parts": [
							{"functionCall": {"name": "lookup", %s, "args": {"query": "status"}}}
						]
					},
					{
						"role": "user",
						"parts": [
							{"functionResponse": {"name": "lookup", %s, "response": {"result": "ok"}}}
						]
					}
				]
			}`, tt.callField, tt.responseField))

			out := ConvertGeminiRequestToCodex("gpt-5.1-codex", raw, false)

			gotCallID := gjson.GetBytes(out, "input.0.call_id").String()
			if gotCallID != tt.want {
				t.Fatalf("expected function_call call_id %q, got %q; output=%s", tt.want, gotCallID, string(out))
			}

			gotOutputID := gjson.GetBytes(out, "input.1.call_id").String()
			if gotOutputID != tt.want {
				t.Fatalf("expected function_call_output call_id %q, got %q; output=%s", tt.want, gotOutputID, string(out))
			}
		})
	}
}

func TestConvertGeminiRequestToCodex_AcceptsInlineData(t *testing.T) {
	out := ConvertGeminiRequestToCodex("gpt-5.1-codex", []byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]}]}`), false)
	if got := gjson.GetBytes(out, "input.0.content.0.type").String(); got != "input_image" {
		t.Fatalf("content type = %q, want input_image. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.content.0.image_url").String(); got != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("image_url = %q, want data:image/png;base64,aGVsbG8=. Output: %s", got, string(out))
	}
}

func TestConvertGeminiRequestToCodex_SplitsNonImageInlineDataByMIME(t *testing.T) {
	out := ConvertGeminiRequestToCodex("gpt-5.1-codex", []byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"audio/wav","data":"UklGRg=="}},{"inlineData":{"mimeType":"video/mp4","data":"AAAAIGZ0eXA="}},{"inlineData":{"mimeType":"application/pdf","data":"JVBERi0="}}]}]}`), false)

	if got := gjson.GetBytes(out, "input.0.content.0.type").String(); got != "input_audio" {
		t.Fatalf("audio content type = %q, want input_audio. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.1.content.0.type").String(); got != "input_file" {
		t.Fatalf("video content type = %q, want input_file. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.2.content.0.type").String(); got != "input_file" {
		t.Fatalf("document content type = %q, want input_file. Output: %s", got, string(out))
	}
}

func TestConvertGeminiRequestToCodex_DropsHiddenThoughtParts(t *testing.T) {
	t.Run("thought-only turn", func(t *testing.T) {
		out := ConvertGeminiRequestToCodex("codex-test", []byte(`{
			"contents":[
				{"role":"model","parts":[{"thought":true,"text":"internal reasoning","thoughtSignature":"opaque-provider-state"}]},
				{"role":"user","parts":[{"text":"continue"}]}
			]
		}`), false)

		input := gjson.GetBytes(out, "input").Array()
		if len(input) != 1 || input[0].Get("role").String() != "user" || input[0].Get("content.0.text").String() != "continue" {
			t.Fatalf("hidden thought turn was not dropped. Output: %s", string(out))
		}
	})

	t.Run("mixed turn", func(t *testing.T) {
		out := ConvertGeminiRequestToCodex("codex-test", []byte(`{
			"contents":[{"role":"model","parts":[
				{"thought":true,"text":"internal reasoning","thoughtSignature":"opaque-provider-state"},
				{"text":"visible answer"}
			]}]
		}`), false)

		input := gjson.GetBytes(out, "input").Array()
		if len(input) != 1 || input[0].Get("content.0.type").String() != "output_text" || input[0].Get("content.0.text").String() != "visible answer" {
			t.Fatalf("hidden thought was not dropped independently of visible text. Output: %s", string(out))
		}
	})
}

func TestConvertGeminiRequestToCodex_DeterministicCallIDsAcrossRepeatedTranslations(t *testing.T) {
	raw := []byte(`{
		"contents": [
			{
				"role": "user",
				"parts": [{"text": "check weather in Paris and Tokyo"}]
			},
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "get_weather", "args": {"city": "Paris"}}},
					{"functionCall": {"name": "get_weather", "args": {"city": "Tokyo"}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "get_weather", "response": {"result": "Paris: 15C"}}},
					{"functionResponse": {"name": "get_weather", "response": {"result": "Tokyo: 20C"}}}
				]
			},
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "get_forecast", "args": {"city": "Paris"}}}
				]
			}
		]
	}`)

	out1 := ConvertGeminiRequestToCodex("gpt-5.1-codex", raw, false)
	out2 := ConvertGeminiRequestToCodex("gpt-5.1-codex", raw, false)

	call1_id1 := gjson.GetBytes(out1, "input.1.call_id").String()
	call2_id1 := gjson.GetBytes(out1, "input.2.call_id").String()
	resp1_id1 := gjson.GetBytes(out1, "input.3.call_id").String()
	resp2_id1 := gjson.GetBytes(out1, "input.4.call_id").String()
	call3_id1 := gjson.GetBytes(out1, "input.5.call_id").String()

	call1_id2 := gjson.GetBytes(out2, "input.1.call_id").String()
	call2_id2 := gjson.GetBytes(out2, "input.2.call_id").String()
	resp1_id2 := gjson.GetBytes(out2, "input.3.call_id").String()
	resp2_id2 := gjson.GetBytes(out2, "input.4.call_id").String()
	call3_id2 := gjson.GetBytes(out2, "input.5.call_id").String()

	if call1_id1 != call1_id2 || call2_id1 != call2_id2 || call3_id1 != call3_id2 {
		t.Fatalf("call_ids are not deterministic across calls:\nout1 calls: [%s, %s, %s]\nout2 calls: [%s, %s, %s]",
			call1_id1, call2_id1, call3_id1, call1_id2, call2_id2, call3_id2)
	}

	if resp1_id1 != resp1_id2 || resp2_id1 != resp2_id2 {
		t.Fatalf("function_call_output call_ids are not deterministic across calls:\nout1 resps: [%s, %s]\nout2 resps: [%s, %s]",
			resp1_id1, resp2_id1, resp1_id2, resp2_id2)
	}

	if call1_id1 != resp1_id1 {
		t.Fatalf("first call ID %q does not match first response ID %q", call1_id1, resp1_id1)
	}
	if call2_id1 != resp2_id1 {
		t.Fatalf("second call ID %q does not match second response ID %q", call2_id1, resp2_id1)
	}
}

func TestConvertGeminiRequestToCodex_CallIDsUniqueWithinRequest(t *testing.T) {
	raw := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "func1", "args": {}}},
					{"functionCall": {"name": "func2", "args": {}}},
					{"functionCall": {"name": "func3", "args": {}}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToCodex("gpt-5.1-codex", raw, false)
	id1 := gjson.GetBytes(out, "input.0.call_id").String()
	id2 := gjson.GetBytes(out, "input.1.call_id").String()
	id3 := gjson.GetBytes(out, "input.2.call_id").String()

	if id1 == "" || id2 == "" || id3 == "" {
		t.Fatalf("expected non-empty IDs, got id1=%q, id2=%q, id3=%q", id1, id2, id3)
	}
	if id1 == id2 || id1 == id3 || id2 == id3 {
		t.Fatalf("call IDs must be unique within request: id1=%q, id2=%q, id3=%q", id1, id2, id3)
	}
}

func TestConvertGeminiRequestToCodex_ExplicitIDWinsOverGenerated(t *testing.T) {
	raw := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "func1", "id": "explicit_call_123", "args": {}}},
					{"functionCall": {"name": "func2", "args": {}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "func1", "id": "explicit_call_123", "response": {"result": "ok"}}},
					{"functionResponse": {"name": "func2", "response": {"result": "ok"}}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToCodex("gpt-5.1-codex", raw, false)
	call0ID := gjson.GetBytes(out, "input.0.call_id").String()
	call1ID := gjson.GetBytes(out, "input.1.call_id").String()
	resp0ID := gjson.GetBytes(out, "input.2.call_id").String()
	resp1ID := gjson.GetBytes(out, "input.3.call_id").String()

	if call0ID != "explicit_call_123" {
		t.Fatalf("expected explicit ID %q, got %q", "explicit_call_123", call0ID)
	}
	if resp0ID != "explicit_call_123" {
		t.Fatalf("expected explicit response ID %q, got %q", "explicit_call_123", resp0ID)
	}
	if call1ID == "explicit_call_123" {
		t.Fatalf("generated ID must not collide with explicit ID, got %q", call1ID)
	}
	if resp1ID != call1ID {
		t.Fatalf("generated response ID %q must match generated call ID %q", resp1ID, call1ID)
	}
}

func TestConvertGeminiRequestToCodex_ExplicitIDCollisionAvoided(t *testing.T) {
	raw := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "func1", "id": "call_1", "args": {}}},
					{"functionCall": {"name": "func2", "args": {}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "func1", "id": "call_1", "response": {"result": "ok1"}}},
					{"functionResponse": {"name": "func2", "response": {"result": "ok2"}}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToCodex("gpt-5.1-codex", raw, false)
	call0ID := gjson.GetBytes(out, "input.0.call_id").String()
	call1ID := gjson.GetBytes(out, "input.1.call_id").String()
	resp0ID := gjson.GetBytes(out, "input.2.call_id").String()
	resp1ID := gjson.GetBytes(out, "input.3.call_id").String()

	if call0ID == call1ID {
		t.Fatalf("duplicate call_id detected: call0=%q, call1=%q", call0ID, call1ID)
	}
	if call0ID != "call_1" {
		t.Fatalf("expected call0 ID %q, got %q", "call_1", call0ID)
	}
	if resp0ID != call0ID {
		t.Fatalf("response 0 ID %q does not match call 0 ID %q", resp0ID, call0ID)
	}
	if resp1ID != call1ID {
		t.Fatalf("response 1 ID %q does not match call 1 ID %q", resp1ID, call1ID)
	}
	if call1ID != "call_2" {
		t.Fatalf("expected call1 ID %q, got %q", "call_2", call1ID)
	}
}

func TestConvertGeminiRequestToCodex_ExplicitIDAfterGeneratedCollisionAvoided(t *testing.T) {
	raw := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "func1", "args": {}}},
					{"functionCall": {"name": "func2", "id": "call_1", "args": {}}},
					{"functionCall": {"name": "func3", "args": {}}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToCodex("gpt-5.1-codex", raw, false)
	call0ID := gjson.GetBytes(out, "input.0.call_id").String()
	call1ID := gjson.GetBytes(out, "input.1.call_id").String()
	call2ID := gjson.GetBytes(out, "input.2.call_id").String()

	if call0ID != "call_2" {
		t.Fatalf("expected call0 ID %q (skipping explicit call_1), got %q", "call_2", call0ID)
	}
	if call1ID != "call_1" {
		t.Fatalf("expected call1 ID %q (explicit), got %q", "call_1", call1ID)
	}
	if call2ID != "call_3" {
		t.Fatalf("expected call2 ID %q, got %q", "call_3", call2ID)
	}
}
