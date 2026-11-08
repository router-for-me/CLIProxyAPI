package gemini

import (
	"fmt"
	"strings"
	"testing"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
)

func TestConvertGeminiRequestToCodex_MaxOutputTokens(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no limit", `{}`, ""},
		{"camel config camel field", `{"generationConfig":{"maxOutputTokens":1024}}`, "1024"},
		{"camel config snake field", `{"generationConfig":{"max_output_tokens":1024}}`, "1024"},
		{"snake config snake field", `{"generation_config":{"max_output_tokens":2048}}`, "2048"},
		{"snake config camel field", `{"generation_config":{"maxOutputTokens":2048}}`, "2048"},
		{"top-level camel field", `{"maxOutputTokens":1024}`, "1024"},
		{"top-level snake field", `{"max_output_tokens":1024}`, "1024"},
		{"top-level max_tokens", `{"max_tokens":1024}`, "1024"},
		{"config takes precedence", `{"generationConfig":{"maxOutputTokens":1024},"maxOutputTokens":2048,"max_output_tokens":4096,"max_tokens":8192}`, "1024"},
		{"snake config takes precedence", `{"generation_config":{"max_output_tokens":1024},"max_output_tokens":2048}`, "1024"},
		{"camel config takes precedence", `{"generationConfig":{"maxOutputTokens":1024},"generation_config":{"max_output_tokens":2048}}`, "1024"},
		{"camel config field takes precedence", `{"generationConfig":{"maxOutputTokens":1024,"max_output_tokens":2048}}`, "1024"},
		{"top-level camel field takes precedence", `{"maxOutputTokens":1024,"max_output_tokens":2048,"max_tokens":4096}`, "1024"},
		{"top-level snake field takes precedence", `{"max_output_tokens":1024,"max_tokens":2048}`, "1024"},
		{"empty config uses top-level fallback", `{"generationConfig":{},"max_tokens":1024}`, "1024"},
		{"empty snake config uses top-level fallback", `{"generation_config":{},"max_tokens":1024}`, "1024"},
		{"invalid config field skipped", `{"generationConfig":{"maxOutputTokens":"1024","max_output_tokens":2048}}`, "2048"},
		{"null config field skipped", `{"generation_config":{"max_output_tokens":null},"max_tokens":1024}`, "1024"},
		{"invalid top-level fields skipped", `{"maxOutputTokens":false,"max_output_tokens":{},"max_tokens":1024}`, "1024"},
		{"zero takes precedence", `{"generationConfig":{"maxOutputTokens":0},"max_tokens":1024}`, "0"},
		{"non-numeric limits ignored", `{"generationConfig":{"maxOutputTokens":"1024"},"max_tokens":null}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, errConvert := ConvertGeminiRequestToCodex("codex-test", []byte(tt.input), false)
			if errConvert != nil {
				t.Fatalf("ConvertGeminiRequestToCodex() error = %v", errConvert)
			}
			if got := ojson.GetBytesNoCopy(out, "max_output_tokens").Raw; got != tt.want {
				t.Fatalf("max_output_tokens = %q, want %q; output=%s", got, tt.want, out)
			}
		})
	}
}

func TestConvertGeminiRequestToCodex_GenerationConfigAliases(t *testing.T) {
	for _, path := range []string{"generationConfig", "generation_config"} {
		t.Run(path, func(t *testing.T) {
			inputDoc := ojson.NewDocString(`{}`)
			_ = inputDoc.Set(path+".thinking_level", "high")
			_ = inputDoc.Set(path+".max_output_tokens", 1024)
			out, errConvert := ConvertGeminiRequestToCodex("codex-test", inputDoc.Bytes(), false)
			if errConvert != nil {
				t.Fatalf("ConvertGeminiRequestToCodex() error = %v", errConvert)
			}
			if got := ojson.GetBytesNoCopy(out, "reasoning.effort").String(); got != "high" {
				t.Fatalf("reasoning.effort = %q, want high; output=%s", got, out)
			}
			if got := ojson.GetBytesNoCopy(out, "max_output_tokens").Int(); got != 1024 {
				t.Fatalf("max_output_tokens = %d, want 1024; output=%s", got, out)
			}
		})
	}
}

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

			gotCallID := ojson.GetBytesNoCopy(out, "input.0.call_id").String()
			if gotCallID != tt.want {
				t.Fatalf("expected function_call call_id %q, got %q; output=%s", tt.want, gotCallID, string(out))
			}

			gotOutputID := ojson.GetBytesNoCopy(out, "input.1.call_id").String()
			if gotOutputID != tt.want {
				t.Fatalf("expected function_call_output call_id %q, got %q; output=%s", tt.want, gotOutputID, string(out))
			}
		})
	}
}

func TestConvertGeminiRequestToCodex_AcceptsInlineData(t *testing.T) {
	out, _ := ConvertGeminiRequestToCodex("gpt-5.1-codex", []byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]}]}`), false)
	if got := ojson.GetBytesNoCopy(out, "input.0.content.0.type").String(); got != "input_image" {
		t.Fatalf("content type = %q, want input_image. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "input.0.content.0.image_url").String(); got != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("image_url = %q, want data:image/png;base64,aGVsbG8=. Output: %s", got, string(out))
	}
}

func TestConvertGeminiRequestToCodex_SplitsNonImageInlineDataByMIME(t *testing.T) {
	out, _ := ConvertGeminiRequestToCodex("gpt-5.1-codex", []byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"audio/wav","data":"UklGRg=="}},{"inlineData":{"mimeType":"video/mp4","data":"AAAAIGZ0eXA="}},{"inlineData":{"mimeType":"application/pdf","data":"JVBERi0="}}]}]}`), false)

	if got := ojson.GetBytesNoCopy(out, "input.0.content.0.type").String(); got != "input_audio" {
		t.Fatalf("audio content type = %q, want input_audio. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "input.1.content.0.type").String(); got != "input_file" {
		t.Fatalf("video content type = %q, want input_file. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "input.2.content.0.type").String(); got != "input_file" {
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

		input := ojson.GetBytesNoCopy(out, "input").Array()
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

		input := ojson.GetBytesNoCopy(out, "input").Array()
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
	payload, _ = ojson.SetBytes(payload, "contents.0.parts.0.thoughtSignature", signature)

	out, _ := ConvertGeminiRequestToCodex("muse-spark-1.3", payload, false)
	input := ojson.GetBytesNoCopy(out, "input").Array()
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
	payload, _ = ojson.SetBytes(payload, "contents.0.parts.0.thoughtSignature", signature)

	for _, modelName := range []string{"gpt-5.1-codex", "claude-sonnet-4-6", "grok-4.5"} {
		t.Run(modelName, func(t *testing.T) {
			out, _ := ConvertGeminiRequestToCodex(modelName, payload, false)
			for _, item := range ojson.GetBytesNoCopy(out, "input").Array() {
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
			payload, _ = ojson.SetBytes(payload, "contents.0.parts.0.thoughtSignature", tc.echoed)
			out, _ := ConvertGeminiRequestToCodex(tc.model, payload, false)
			found := ""
			for _, item := range ojson.GetBytesNoCopy(out, "input").Array() {
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

	gotCall1 := ojson.GetBytesNoCopy(out1, "input.0.call_id").String()
	gotResp1 := ojson.GetBytesNoCopy(out1, "input.1.call_id").String()
	gotCall2 := ojson.GetBytesNoCopy(out1, "input.2.call_id").String()
	gotResp2 := ojson.GetBytesNoCopy(out1, "input.3.call_id").String()

	if gotCall1 != wantID1 || gotResp1 != wantID1 {
		t.Fatalf("expected first tool pair to have id %q, got call=%q, resp=%q", wantID1, gotCall1, gotResp1)
	}
	if gotCall2 != wantID2 || gotResp2 != wantID2 {
		t.Fatalf("expected second tool pair to have id %q, got call=%q, resp=%q", wantID2, gotCall2, gotResp2)
	}
}

func TestConvertGeminiRequestToCodex_DefaultsOmittedContentRoleToUser(t *testing.T) {
	raw := []byte(`{"contents":[{"parts":[{"text":"Reply with exactly: OK"}]}]}`)

	out, err := ConvertGeminiRequestToCodex("gpt-5.1-codex", raw, false)
	if err != nil {
		t.Fatalf("ConvertGeminiRequestToCodex() error = %v", err)
	}
	if got := ojson.GetBytesNoCopy(out, "input.0.role").String(); got != "user" {
		t.Fatalf("input.0.role = %q, want user; out=%s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "input.0.content.0.type").String(); got != "input_text" {
		t.Fatalf("input.0.content.0.type = %q, want input_text; out=%s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "input.0.content.0.text").String(); got != "Reply with exactly: OK" {
		t.Fatalf("input.0.content.0.text = %q, want Reply with exactly: OK; out=%s", got, string(out))
	}
}
