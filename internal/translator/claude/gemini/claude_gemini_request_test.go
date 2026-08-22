package gemini

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiRequestToClaude_PreservesCustomToolIDs(t *testing.T) {
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

			out := ConvertGeminiRequestToClaude("claude-sonnet-4", raw, false)

			gotCallID := gjson.GetBytes(out, "messages.0.content.0.id").String()
			if gotCallID != tt.want {
				t.Fatalf("expected tool_use id %q, got %q; output=%s", tt.want, gotCallID, string(out))
			}

			gotResultID := gjson.GetBytes(out, "messages.1.content.0.tool_use_id").String()
			if gotResultID != tt.want {
				t.Fatalf("expected tool_result tool_use_id %q, got %q; output=%s", tt.want, gotResultID, string(out))
			}
		})
	}
}

func TestConvertGeminiRequestToClaude_GroupsConsecutiveRoleTurns(t *testing.T) {
	raw := []byte(`{
		"contents":[
			{"role":"model","parts":[{"text":"answer"}]},
			{"role":"model","parts":[{"functionCall":{"name":"first","id":"call_1","args":{}}}]},
			{"role":"model","parts":[{"functionCall":{"name":"second","id":"call_2","args":{}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"first","id":"call_1","response":{"result":"one"}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"second","id":"call_2","response":{"result":"two"}}}]}
		]
	}`)

	out := ConvertGeminiRequestToClaude("claude-test", raw, false)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("message count = %d, want 2. Output: %s", len(messages), string(out))
	}
	assistantContent := messages[0].Get("content").Array()
	wantAssistantTypes := []string{"text", "tool_use", "tool_use"}
	if len(assistantContent) != len(wantAssistantTypes) {
		t.Fatalf("assistant content count = %d, want %d. Output: %s", len(assistantContent), len(wantAssistantTypes), string(out))
	}
	for i, wantType := range wantAssistantTypes {
		if got := assistantContent[i].Get("type").String(); got != wantType {
			t.Fatalf("assistant content[%d].type = %q, want %q", i, got, wantType)
		}
	}
	userContent := messages[1].Get("content").Array()
	if len(userContent) != 2 {
		t.Fatalf("user content count = %d, want 2. Output: %s", len(userContent), string(out))
	}
	for i, wantID := range []string{"call_1", "call_2"} {
		if got := userContent[i].Get("type").String(); got != "tool_result" {
			t.Fatalf("user content[%d].type = %q, want tool_result", i, got)
		}
		if got := userContent[i].Get("tool_use_id").String(); got != wantID {
			t.Fatalf("user content[%d].tool_use_id = %q, want %q", i, got, wantID)
		}
	}
}

func TestConvertGeminiRequestToClaude_KeepsSystemInstructionUserSeparate(t *testing.T) {
	raw := []byte(`{
		"system_instruction":{"parts":[{"text":"system rule"}]},
		"contents":[{"role":"user","parts":[{"text":"question"}]}]
	}`)
	out := ConvertGeminiRequestToClaude("claude-test", raw, false)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("message count = %d, want 2. Output: %s", len(messages), string(out))
	}
	if got := messages[0].Get("content.0.text").String(); got != "system rule" {
		t.Fatalf("system user text = %q, want system rule", got)
	}
	if got := messages[1].Get("content.0.text").String(); got != "question" {
		t.Fatalf("ordinary user text = %q, want question", got)
	}
}

func TestConvertGeminiRequestToClaude_DropsTemperature(t *testing.T) {
	raw := []byte(`{
		"generationConfig": {
			"temperature": 0.2,
			"topP": 0.8
		},
		"contents": [
			{
				"role": "user",
				"parts": [{"text": "hi"}]
			}
		]
	}`)

	out := ConvertGeminiRequestToClaude("claude-sonnet-5", raw, false)

	if gjson.GetBytes(out, "temperature").Exists() {
		t.Fatalf("temperature should be removed")
	}
	if got := gjson.GetBytes(out, "top_p").Float(); got != 0.8 {
		t.Fatalf("top_p = %v, want 0.8", got)
	}
}

func TestConvertGeminiRequestToClaude_AcceptsCamelInlineData(t *testing.T) {
	out := ConvertGeminiRequestToClaude("claude-sonnet-4", []byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]}]}`), false)
	if got := gjson.GetBytes(out, "messages.0.content.0.type").String(); got != "image" {
		t.Fatalf("content type = %q, want image. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.source.media_type").String(); got != "image/png" {
		t.Fatalf("media_type = %q, want image/png. Output: %s", got, string(out))
	}
}

func TestConvertGeminiRequestToClaude_SplitsNonImageInlineDataByMIME(t *testing.T) {
	out := ConvertGeminiRequestToClaude("claude-sonnet-4", []byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"audio/wav","data":"UklGRg=="}},{"inlineData":{"mimeType":"video/mp4","data":"AAAAIGZ0eXA="}},{"inlineData":{"mimeType":"application/pdf","data":"JVBERi0="}}]}]}`), false)

	if got := gjson.GetBytes(out, "messages.0.content.0.type").String(); got != "text" {
		t.Fatalf("audio fallback type = %q, want text. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.type").String(); got != "text" {
		t.Fatalf("video fallback type = %q, want text. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.2.type").String(); got != "document" {
		t.Fatalf("document content type = %q, want document. Output: %s", got, string(out))
	}
	if gjson.GetBytes(out, "messages.0.content.#(type==\"image\")").Exists() {
		t.Fatalf("non-image inlineData must not be converted to image. Output: %s", string(out))
	}
}

func TestConvertGeminiRequestToClaude_DropsHiddenThoughtParts(t *testing.T) {
	t.Run("thought-only turn", func(t *testing.T) {
		out := ConvertGeminiRequestToClaude("claude-test", []byte(`{
			"contents":[
				{"role":"model","parts":[{"thought":true,"text":"internal reasoning","thoughtSignature":"opaque-provider-state"}]},
				{"role":"user","parts":[{"text":"continue"}]}
			]
		}`), false)

		messages := gjson.GetBytes(out, "messages").Array()
		if len(messages) != 1 || messages[0].Get("role").String() != "user" || messages[0].Get("content.0.text").String() != "continue" {
			t.Fatalf("hidden thought turn was not dropped. Output: %s", string(out))
		}
	})

	t.Run("mixed turn", func(t *testing.T) {
		out := ConvertGeminiRequestToClaude("claude-test", []byte(`{
			"contents":[{"role":"model","parts":[
				{"thought":true,"text":"internal reasoning","thoughtSignature":"opaque-provider-state"},
				{"text":"visible answer"}
			]}]
		}`), false)

		content := gjson.GetBytes(out, "messages.0.content").Array()
		if len(content) != 1 || content[0].Get("type").String() != "text" || content[0].Get("text").String() != "visible answer" {
			t.Fatalf("hidden thought was not dropped independently of visible text. Output: %s", string(out))
		}
	})
}

func TestConvertGeminiRequestToClaude_DeterministicToolIDsAcrossRepeatedTranslations(t *testing.T) {
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

	out1 := ConvertGeminiRequestToClaude("claude-sonnet-4", raw, false)
	out2 := ConvertGeminiRequestToClaude("claude-sonnet-4", raw, false)

	id1_call0 := gjson.GetBytes(out1, "messages.1.content.0.id").String()
	id1_call1 := gjson.GetBytes(out1, "messages.1.content.1.id").String()
	id1_resp0 := gjson.GetBytes(out1, "messages.2.content.0.tool_use_id").String()
	id1_resp1 := gjson.GetBytes(out1, "messages.2.content.1.tool_use_id").String()
	id1_call2 := gjson.GetBytes(out1, "messages.3.content.0.id").String()

	id2_call0 := gjson.GetBytes(out2, "messages.1.content.0.id").String()
	id2_call1 := gjson.GetBytes(out2, "messages.1.content.1.id").String()
	id2_resp0 := gjson.GetBytes(out2, "messages.2.content.0.tool_use_id").String()
	id2_resp1 := gjson.GetBytes(out2, "messages.2.content.1.tool_use_id").String()
	id2_call2 := gjson.GetBytes(out2, "messages.3.content.0.id").String()

	if id1_call0 != id2_call0 || id1_call1 != id2_call1 || id1_call2 != id2_call2 {
		t.Fatalf("tool_use IDs are not deterministic across calls:\nout1 calls: [%s, %s, %s]\nout2 calls: [%s, %s, %s]",
			id1_call0, id1_call1, id1_call2, id2_call0, id2_call1, id2_call2)
	}

	if id1_resp0 != id2_resp0 || id1_resp1 != id2_resp1 {
		t.Fatalf("tool_result IDs are not deterministic across calls:\nout1 resps: [%s, %s]\nout2 resps: [%s, %s]",
			id1_resp0, id1_resp1, id2_resp0, id2_resp1)
	}

	if id1_call0 != id1_resp0 {
		t.Fatalf("first call ID %q does not match first response ID %q", id1_call0, id1_resp0)
	}
	if id1_call1 != id1_resp1 {
		t.Fatalf("second call ID %q does not match second response ID %q", id1_call1, id1_resp1)
	}
}

func TestConvertGeminiRequestToClaude_ToolIDsUniqueWithinRequest(t *testing.T) {
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

	out := ConvertGeminiRequestToClaude("claude-sonnet-4", raw, false)
	id1 := gjson.GetBytes(out, "messages.0.content.0.id").String()
	id2 := gjson.GetBytes(out, "messages.0.content.1.id").String()
	id3 := gjson.GetBytes(out, "messages.0.content.2.id").String()

	if id1 == "" || id2 == "" || id3 == "" {
		t.Fatalf("expected non-empty IDs, got id1=%q, id2=%q, id3=%q", id1, id2, id3)
	}
	if id1 == id2 || id1 == id3 || id2 == id3 {
		t.Fatalf("tool IDs must be unique within request: id1=%q, id2=%q, id3=%q", id1, id2, id3)
	}
}

func TestConvertGeminiRequestToClaude_ExplicitIDWinsOverGenerated(t *testing.T) {
	raw := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "func1", "id": "explicit_id_123", "args": {}}},
					{"functionCall": {"name": "func2", "args": {}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "func1", "id": "explicit_id_123", "response": {"result": "ok"}}},
					{"functionResponse": {"name": "func2", "response": {"result": "ok"}}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToClaude("claude-sonnet-4", raw, false)
	call0ID := gjson.GetBytes(out, "messages.0.content.0.id").String()
	call1ID := gjson.GetBytes(out, "messages.0.content.1.id").String()
	resp0ID := gjson.GetBytes(out, "messages.1.content.0.tool_use_id").String()
	resp1ID := gjson.GetBytes(out, "messages.1.content.1.tool_use_id").String()

	if call0ID != "explicit_id_123" {
		t.Fatalf("expected explicit ID %q, got %q", "explicit_id_123", call0ID)
	}
	if resp0ID != "explicit_id_123" {
		t.Fatalf("expected explicit response ID %q, got %q", "explicit_id_123", resp0ID)
	}
	if call1ID == "explicit_id_123" {
		t.Fatalf("generated ID must not collide with explicit ID, got %q", call1ID)
	}
	if resp1ID != call1ID {
		t.Fatalf("generated response ID %q must match generated call ID %q", resp1ID, call1ID)
	}
}

func TestConvertGeminiRequestToClaude_ExplicitIDCollisionAvoided(t *testing.T) {
	raw := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "func1", "id": "toolu_1", "args": {}}},
					{"functionCall": {"name": "func2", "args": {}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "func1", "id": "toolu_1", "response": {"result": "ok1"}}},
					{"functionResponse": {"name": "func2", "response": {"result": "ok2"}}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToClaude("claude-sonnet-4", raw, false)
	call0ID := gjson.GetBytes(out, "messages.0.content.0.id").String()
	call1ID := gjson.GetBytes(out, "messages.0.content.1.id").String()
	resp0ID := gjson.GetBytes(out, "messages.1.content.0.tool_use_id").String()
	resp1ID := gjson.GetBytes(out, "messages.1.content.1.tool_use_id").String()

	if call0ID == call1ID {
		t.Fatalf("duplicate tool_use ID detected: call0=%q, call1=%q", call0ID, call1ID)
	}
	if call0ID != "toolu_1" {
		t.Fatalf("expected call0 ID %q, got %q", "toolu_1", call0ID)
	}
	if resp0ID != call0ID {
		t.Fatalf("response 0 ID %q does not match call 0 ID %q", resp0ID, call0ID)
	}
	if resp1ID != call1ID {
		t.Fatalf("response 1 ID %q does not match call 1 ID %q", resp1ID, call1ID)
	}
	if call1ID != "toolu_2" {
		t.Fatalf("expected call1 ID %q, got %q", "toolu_2", call1ID)
	}
}

func TestConvertGeminiRequestToClaude_ExplicitIDAfterGeneratedCollisionAvoided(t *testing.T) {
	raw := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "func1", "args": {}}},
					{"functionCall": {"name": "func2", "id": "toolu_1", "args": {}}},
					{"functionCall": {"name": "func3", "args": {}}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToClaude("claude-sonnet-4", raw, false)
	call0ID := gjson.GetBytes(out, "messages.0.content.0.id").String()
	call1ID := gjson.GetBytes(out, "messages.0.content.1.id").String()
	call2ID := gjson.GetBytes(out, "messages.0.content.2.id").String()

	if call0ID != "toolu_2" {
		t.Fatalf("expected call0 ID %q (skipping explicit toolu_1), got %q", "toolu_2", call0ID)
	}
	if call1ID != "toolu_1" {
		t.Fatalf("expected call1 ID %q (explicit), got %q", "toolu_1", call1ID)
	}
	if call2ID != "toolu_3" {
		t.Fatalf("expected call2 ID %q, got %q", "toolu_3", call2ID)
	}
}
