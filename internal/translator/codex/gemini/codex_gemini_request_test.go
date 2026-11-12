package gemini

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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

			out, _ := ConvertGeminiRequestToCodex("gpt-5.1-codex", raw, false)

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
	out, _ := ConvertGeminiRequestToCodex("gpt-5.1-codex", []byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]}]}`), false)
	if got := gjson.GetBytes(out, "input.0.content.0.type").String(); got != "input_image" {
		t.Fatalf("content type = %q, want input_image. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.content.0.image_url").String(); got != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("image_url = %q, want data:image/png;base64,aGVsbG8=. Output: %s", got, string(out))
	}
}

func TestConvertGeminiRequestToCodex_SplitsNonImageInlineDataByMIME(t *testing.T) {
	out, _ := ConvertGeminiRequestToCodex("gpt-5.1-codex", []byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"audio/wav","data":"UklGRg=="}},{"inlineData":{"mimeType":"video/mp4","data":"AAAAIGZ0eXA="}},{"inlineData":{"mimeType":"application/pdf","data":"JVBERi0="}}]}]}`), false)

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
		out, _ := ConvertGeminiRequestToCodex("codex-test", []byte(`{
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
		out, _ := ConvertGeminiRequestToCodex("codex-test", []byte(`{
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

func testGeminiMetaThoughtSignature() string {
	// Shape-valid synthetic Meta envelope; no captured value is embedded.
	signature := "Q-PaDgE"
	for len(signature) < 1426 {
		signature += "A"
	}
	return signature
}

func TestConvertGeminiRequestToCodex_PreservesMetaThoughtSignatureForMuseTarget(t *testing.T) {
	signature := testGeminiMetaThoughtSignature()
	payload := []byte(`{"contents":[{"role":"model","parts":[{"thought":true,"text":"internal reasoning","thoughtSignature":""},{"text":"visible answer"}]},{"role":"user","parts":[{"text":"continue"}]}]}`)
	payload, _ = sjson.SetBytes(payload, "contents.0.parts.0.thoughtSignature", signature)

	out, _ := ConvertGeminiRequestToCodex("muse-spark-1.3", payload, false)
	input := gjson.GetBytes(out, "input").Array()
	if len(input) != 3 {
		t.Fatalf("input len = %d, want reasoning+message+user. Output: %s", len(input), string(out))
	}
	if got := input[0].Get("type").String(); got != "reasoning" {
		t.Fatalf("input.0 type = %q, want reasoning. Output: %s", got, string(out))
	}
	if got := input[0].Get("encrypted_content").String(); got != signature {
		t.Fatalf("encrypted_content = %q, want Meta signature", got)
	}
	if got := input[0].Get("summary.0.text").String(); got != "internal reasoning" {
		t.Fatalf("summary text = %q, want thought text. Output: %s", got, string(out))
	}
	if got := input[1].Get("content.0.text").String(); got != "visible answer" {
		t.Fatalf("visible text = %q. Output: %s", got, string(out))
	}
}

func TestConvertGeminiRequestToCodex_DropsMetaThoughtSignatureForNonMetaTargets(t *testing.T) {
	signature := testGeminiMetaThoughtSignature()
	payload := []byte(`{"contents":[{"role":"model","parts":[{"thought":true,"text":"internal reasoning","thoughtSignature":""},{"text":"visible answer"}]},{"role":"user","parts":[{"text":"continue"}]}]}`)
	payload, _ = sjson.SetBytes(payload, "contents.0.parts.0.thoughtSignature", signature)

	for _, modelName := range []string{"gpt-5.1-codex", "claude-sonnet-4-6", "grok-4.5"} {
		t.Run(modelName, func(t *testing.T) {
			out, _ := ConvertGeminiRequestToCodex(modelName, payload, false)
			for _, item := range gjson.GetBytes(out, "input").Array() {
				if item.Get("type").String() == "reasoning" {
					t.Fatalf("Meta signature leaked to %s target. Output: %s", modelName, string(out))
				}
			}
		})
	}
}

func testGeminiGPTThoughtSignature() string {
	decoded := make([]byte, 89)
	decoded[0] = 0x80
	for i := 9; i < len(decoded); i++ {
		decoded[i] = byte(i)
	}
	return base64.RawURLEncoding.EncodeToString(decoded)
}

func TestConvertGeminiRequestToCodex_NormalizesPrefixedAndCarrierSignatures(t *testing.T) {
	gptSignature := testGeminiGPTThoughtSignature()
	metaSignature := testGeminiMetaThoughtSignature()
	// OpenRouter-appended suffix shape: one '.' plus 67 alphanumeric chars.
	routedMetaSignature := metaSignature + "." + strings.Repeat("Ab3", 23)[:67]
	cases := []struct {
		name      string
		model     string
		echoed    string
		want      string
		wantFound bool
	}{
		{"gpt cache prefixed", "gpt-5.1-codex", "gpt#" + gptSignature, gptSignature, true},
		{"carrier wrapped gpt prefixed", "gpt-5.1-codex", encodeGeminiCodexThoughtSignature("gpt#" + gptSignature), gptSignature, true},
		{"carrier wrapped meta", "muse-spark-1.3", encodeGeminiCodexThoughtSignature(metaSignature), metaSignature, true},
		{"carrier wrapped meta to gpt target", "gpt-5.1-codex", encodeGeminiCodexThoughtSignature(metaSignature), "", false},
		{"carrier wrapped routed meta", "meta/muse-wonder", encodeGeminiCodexThoughtSignature(routedMetaSignature), routedMetaSignature, true},
		{"carrier wrapped routed meta to gpt target", "gpt-5.1-codex", encodeGeminiCodexThoughtSignature(routedMetaSignature), "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(`{"contents":[{"role":"model","parts":[{"thought":true,"text":"internal reasoning","thoughtSignature":""},{"text":"visible answer"}]},{"role":"user","parts":[{"text":"continue"}]}]}`)
			payload, _ = sjson.SetBytes(payload, "contents.0.parts.0.thoughtSignature", tc.echoed)
			out, _ := ConvertGeminiRequestToCodex(tc.model, payload, false)
			found := ""
			for _, item := range gjson.GetBytes(out, "input").Array() {
				if item.Get("type").String() == "reasoning" {
					found = item.Get("encrypted_content").String()
				}
			}
			if tc.wantFound && found != tc.want {
				t.Fatalf("encrypted_content = %q, want normalized signature; output=%s", found, string(out))
			}
			if !tc.wantFound && found != "" {
				t.Fatalf("foreign signature leaked as reasoning item; output=%s", string(out))
			}
		})
	}
}

func TestConvertGeminiRequestToCodex_DeterministicCallIDs(t *testing.T) {
	raw := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "first_tool", "args": {"q": "one"}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "first_tool", "response": {"result": "ok1"}}}
				]
			},
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "second_tool", "args": {"q": "two"}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "second_tool", "response": {"result": "ok2"}}}
				]
			}
		]
	}`)

	out1, _ := ConvertGeminiRequestToCodex("gpt-5.1-codex", raw, false)
	out2, _ := ConvertGeminiRequestToCodex("gpt-5.1-codex", raw, false)

	if string(out1) != string(out2) {
		t.Fatalf("expected deterministic output across multiple conversions, got different outputs:\nout1=%s\nout2=%s", string(out1), string(out2))
	}

	wantID1 := "call_gemini_0000000000000001"
	wantID2 := "call_gemini_0000000000000002"

	gotCall1 := gjson.GetBytes(out1, "input.0.call_id").String()
	gotResp1 := gjson.GetBytes(out1, "input.1.call_id").String()
	gotCall2 := gjson.GetBytes(out1, "input.2.call_id").String()
	gotResp2 := gjson.GetBytes(out1, "input.3.call_id").String()

	if gotCall1 != wantID1 || gotResp1 != wantID1 {
		t.Fatalf("expected first tool pair to have id %q, got call=%q, resp=%q", wantID1, gotCall1, gotResp1)
	}
	if gotCall2 != wantID2 || gotResp2 != wantID2 {
		t.Fatalf("expected second tool pair to have id %q, got call=%q, resp=%q", wantID2, gotCall2, gotResp2)
	}
}
