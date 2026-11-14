package claude

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
	sigcompat "github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
)

func TestGeminiClaudeCarrierPlacement(t *testing.T) {
	signature := capturedGeminiThinkingSignature
	carrier := `{"type":"thinking","thinking":"","signature":"` + signature + `"}`
	tool := `{"type":"tool_use","id":"call-1","name":"lookup","input":{}}`
	text := `{"type":"text","text":"answer"}`
	marked := func(direction, target string) string {
		return `{"type":"thinking","thinking":"","signature":"cpa-gemini-carrier-v1:` + direction + `:` + target + `:` + base64.RawStdEncoding.EncodeToString([]byte(signature)) + `"}`
	}
	for _, tt := range []struct {
		name       string
		content    string
		signatures []string
	}{
		{"leading function", carrier + `,` + tool, []string{signature}},
		{"trailing function", tool + `,` + carrier, []string{signature}},
		{"signed text before tool", carrier + `,` + text + `,` + tool, []string{signature, sigcompat.GeminiSkipThoughtSignatureValidator}},
		{"text only", carrier + `,` + text, []string{signature}},
		{"trailing text", text + `,` + carrier, []string{signature}},
		{"standalone", carrier, []string{signature}},
		{"marked next function", marked("next", "function") + `,` + tool, []string{signature}},
		{"prefixed signature", `{"type":"thinking","thinking":"","signature":"gemini#` + signature + `"},` + tool, []string{signature}},
		{"consecutive carriers", carrier + `,{"type":"thinking","thinking":"","signature":"EjQKMgEMOdbHO0Gd+c9Mxk4ELwPGbpCEcp2mFfYYLix2UVtBH3fL8GECc4+JITVnHF4qZDsE"},` + tool, []string{signature, "EjQKMgEMOdbHO0Gd+c9Mxk4ELwPGbpCEcp2mFfYYLix2UVtBH3fL8GECc4+JITVnHF4qZDsE"}},
		{"marked previous text", text + `,` + marked("previous", "text") + `,` + tool, []string{signature, sigcompat.GeminiSkipThoughtSignatureValidator}},
		{"marked standalone", marked("standalone", "any") + `,` + tool, []string{signature, sigcompat.GeminiSkipThoughtSignatureValidator}},
		{"wrong target", marked("next", "function") + `,` + text + `,` + tool, []string{"", sigcompat.GeminiSkipThoughtSignatureValidator}},
		{"foreign carrier", `{"type":"thinking","thinking":"","signature":"claude#invalid"},` + tool, []string{sigcompat.GeminiSkipThoughtSignatureValidator}},
		{"malformed carrier", `{"type":"thinking","thinking":"","signature":"cpa-gemini-carrier-v1:next:function:invalid"},` + tool, []string{sigcompat.GeminiSkipThoughtSignatureValidator}},
		{"no carrier", tool, []string{sigcompat.GeminiSkipThoughtSignatureValidator}},
		{"parallel sibling", carrier + `,` + tool + `,{"type":"tool_use","id":"call-2","name":"lookup","input":{}}`, []string{signature, sigcompat.GeminiSkipThoughtSignatureValidator}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request := ojson.NewDocString(`{"messages":[{"role":"assistant","content":[` + tt.content + `]},{"role":"user","content":[]}]}`)
			for _, block := range request.Get("messages.0.content").Array() {
				if block.Get("type").String() == "tool_use" {
					result := ojson.NewDocString(`{"type":"tool_result","tool_use_id":"","content":"ok"}`)
					_ = result.Set("tool_use_id", block.Get("id").String())
					_ = request.SetRawBytes("messages.1.content.-1", result.Bytes())
				}
			}
			if request.Get("messages.1.content.#").Int() == 0 {
				_ = request.SetRaw("messages.1.content", `[{"type":"text","text":"continue"}]`)
			}
			output, errConvert := ConvertClaudeRequestToGemini("gemini-4-carbon", request.Bytes(), false)
			if errConvert != nil {
				t.Fatal(errConvert)
			}
			parts := ojson.GetBytes(output, "contents.0.parts").Array()
			if len(parts) != len(tt.signatures) {
				t.Fatalf("got %d parts, want %d: %s", len(parts), len(tt.signatures), output)
			}
			for i, want := range tt.signatures {
				if got := parts[i].Get("thoughtSignature").String(); got != want {
					t.Errorf("part %d signature = %q, want %q; output=%s", i, got, want, output)
				}
			}
		})
	}
}

func TestGeminiClaudeCarrierStaysInAssistantMessage(t *testing.T) {
	carrier := `{"type":"thinking","thinking":"","signature":"` + capturedGeminiThinkingSignature + `"}`
	request := []byte(`{"messages":[{"role":"assistant","content":[` + carrier + `]},{"role":"user","content":[` + carrier + `,{"type":"text","text":"continue"}]},{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"ok"}]}]}`)
	output, errConvert := ConvertClaudeRequestToGemini("gemini-4-carbon", request, false)
	if errConvert != nil {
		t.Fatal(errConvert)
	}
	var sawCall bool
	for _, content := range ojson.GetBytes(output, "contents").Array() {
		for _, part := range content.Get("parts").Array() {
			if content.Get("role").String() == "user" && part.Get("thoughtSignature").Exists() {
				t.Fatalf("user message replayed assistant signature: %s", output)
			}
			if part.Get("functionCall").Exists() {
				sawCall = true
				if part.Get("thoughtSignature").String() != sigcompat.GeminiSkipThoughtSignatureValidator {
					t.Fatalf("carrier leaked between messages: %s", output)
				}
			}
		}
	}
	if !sawCall {
		t.Fatalf("functionCall missing: %s", output)
	}
}

func TestGeminiClaudePartMetadataPreservesSupportedPayload(t *testing.T) {
	for _, tt := range []struct {
		name string
		part string
		tool bool
	}{
		{"text", `{"text":"answer","partMetadata":{"source":"model"}}`, false},
		{"tool", `{"functionCall":{"name":"lookup","args":{}},"partMetadata":{"source":"model"}}`, true},
	} {
		for _, mode := range []string{"nonstream", "stream same chunk", "stream split chunks"} {
			for _, signed := range []bool{false, true} {
				t.Run(tt.name+"/"+mode+"/signed="+strconv.FormatBool(signed), func(t *testing.T) {
					part := ojson.NewDocString(tt.part)
					if signed {
						_ = part.Set("thoughtSignature", capturedGeminiThinkingSignature)
					}
					content := geminiClaudeContentForParts(t, []string{part.String()}, mode)
					output := replayGeminiClaudeContent(t, content)
					parts := ojson.GetBytes(output, "contents.0.parts").Array()
					if ojson.GetBytes(output, "contents.0.role").String() != "model" || len(parts) != 1 {
						t.Fatalf("supported payload with metadata was dropped: %s; content=%s", output, content)
					}
					if tt.tool {
						if parts[0].Get("functionCall.name").String() != "lookup" || parts[0].Get("functionCall.args").Raw != `{}` {
							t.Errorf("metadata changed tool payload: %s", output)
						}
					} else if parts[0].Get("text").String() != "answer" {
						t.Errorf("metadata changed text payload: %s", output)
					}
					if signed && parts[0].Get("thoughtSignature").String() != capturedGeminiThinkingSignature {
						t.Errorf("metadata dropped native signature: %s", output)
					}
				})
			}
		}
	}
}

func TestGeminiClaudeDelayedTextSignaturePartBoundaryRoundTrip(t *testing.T) {
	parts := []string{`{"text":"unsigned"}`, `{"text":"signed"}`, `{"thoughtSignature":"` + capturedGeminiThinkingSignature + `"}`}
	for _, mode := range []string{"nonstream", "stream same chunk", "stream split chunks"} {
		t.Run(mode, func(t *testing.T) {
			content := geminiClaudeContentForParts(t, parts, mode)
			output := replayGeminiClaudeContent(t, content)
			actual := ojson.GetBytes(output, "contents.0.parts").Array()
			if len(actual) != 2 || actual[0].Get("text").String() != "unsigned" || actual[0].Get("thoughtSignature").Exists() || actual[1].Get("text").String() != "signed" || actual[1].Get("thoughtSignature").String() != capturedGeminiThinkingSignature {
				t.Fatalf("delayed signature changed native text part contents: %s; content=%s", output, content)
			}
		})
	}
}

func TestGeminiClaudeUnsupportedPayloadDoesNotRetargetSignature(t *testing.T) {
	signature := capturedGeminiThinkingSignature
	for _, tt := range []struct {
		name  string
		parts []string
	}{
		{"inline data", []string{`{"inlineData":{"mimeType":"image/png","data":"eA=="},"thoughtSignature":"` + signature + `"}`}},
		{"file data", []string{`{"fileData":{"mimeType":"image/png","fileUri":"gs://bucket/image"},"thoughtSignature":"` + signature + `"}`}},
		{"code", []string{`{"executableCode":{"language":"PYTHON","code":"1"},"thoughtSignature":"` + signature + `"}`}},
		{"delayed image signature", []string{`{"inlineData":{"mimeType":"image/png","data":"eA=="}}`, `{"thoughtSignature":"` + signature + `"}`}},
		{"delayed file signature", []string{`{"fileData":{"mimeType":"image/png","fileUri":"gs://bucket/image"}}`, `{"text":"","thought":true}`, `{"thoughtSignature":"` + signature + `"}`}},
	} {
		for _, mode := range []string{"nonstream", "stream same chunk", "stream split chunks"} {
			for _, prefix := range []string{`{"text":"before"}`, `{"functionCall":{"name":"lookup","args":{}}}`} {
				t.Run(tt.name+"/"+mode+"/"+prefix, func(t *testing.T) {
					parts := append([]string{prefix}, tt.parts...)
					parts = append(parts, `{"text":"after"}`)
					content := geminiClaudeContentForParts(t, parts, mode)
					output := replayGeminiClaudeContent(t, content)
					for _, part := range ojson.GetBytes(output, "contents.0.parts").Array() {
						if got := part.Get("thoughtSignature").String(); got != "" && got != sigcompat.GeminiSkipThoughtSignatureValidator {
							t.Errorf("unsupported payload signature was attached to a different part: %s; content=%s", output, content)
						}
					}
				})
			}
		}
	}
}

func geminiClaudeContentForParts(t *testing.T, parts []string, mode string) string {
	t.Helper()
	full := `{"candidates":[{"content":{"parts":[` + strings.Join(parts, ",") + `]},"finishReason":"STOP"}]}`
	if mode == "nonstream" {
		response := ConvertGeminiResponseToClaudeNonStream(context.Background(), "gemini-4-carbon", nil, nil, []byte(full), nil)
		return ojson.GetBytes(response, "content").Raw
	}
	chunks := []string{full}
	if mode == "stream split chunks" {
		chunks = nil
		for _, part := range parts {
			chunks = append(chunks, `{"candidates":[{"content":{"parts":[`+part+`]}}]}`)
		}
		chunks = append(chunks, `{"candidates":[{"finishReason":"STOP"}]}`)
	}
	chunks = append(chunks, "[DONE]")
	var param any
	var events []byte
	for _, chunk := range chunks {
		events = append(events, bytes.Join(ConvertGeminiResponseToClaude(context.Background(), "gemini-4-carbon", nil, nil, []byte(chunk), &param), nil)...)
	}
	return claudeContentFromGeminiStream(t, events).String()
}

func TestGeminiClaudeSignedTextPartBoundaryRoundTrip(t *testing.T) {
	signed := `{"text":"signed","thoughtSignature":"` + capturedGeminiThinkingSignature + `"}`
	unsigned := `{"text":"unsigned"}`
	full := `{"candidates":[{"content":{"parts":[` + signed + `,` + unsigned + `]},"finishReason":"STOP"}]}`
	for _, mode := range []string{"nonstream", "stream same chunk", "stream split chunks"} {
		t.Run(mode, func(t *testing.T) {
			var content string
			if mode == "nonstream" {
				response := ConvertGeminiResponseToClaudeNonStream(context.Background(), "gemini-4-carbon", nil, nil, []byte(full), nil)
				content = ojson.GetBytes(response, "content").Raw
			} else {
				chunks := []string{full, "[DONE]"}
				if mode == "stream split chunks" {
					chunks = []string{`{"candidates":[{"content":{"parts":[` + signed + `]}}]}`, `{"candidates":[{"content":{"parts":[` + unsigned + `]},"finishReason":"STOP"}]}`, "[DONE]"}
				}
				var param any
				var events []byte
				for _, chunk := range chunks {
					events = append(events, bytes.Join(ConvertGeminiResponseToClaude(context.Background(), "gemini-4-carbon", nil, nil, []byte(chunk), &param), nil)...)
				}
				content = claudeContentFromGeminiStream(t, events).String()
			}
			output := replayGeminiClaudeContent(t, content)
			parts := ojson.GetBytes(output, "contents.0.parts").Array()
			if len(parts) != 2 || parts[0].Get("text").String() != "signed" || parts[0].Get("thoughtSignature").String() != capturedGeminiThinkingSignature || parts[1].Get("text").String() != "unsigned" || parts[1].Get("thoughtSignature").Exists() {
				t.Fatalf("signed text was merged with another native part: %s; content=%s", output, content)
			}
		})
	}
}

func TestGeminiClaudeCarrierDoesNotCrossNonEmptyThinking(t *testing.T) {
	signature := capturedGeminiThinkingSignature
	previous := `{"type":"thinking","thinking":"","signature":"cpa-gemini-carrier-v1:previous:text:` + base64.RawStdEncoding.EncodeToString([]byte(signature)) + `"}`
	for _, content := range []string{
		`[{"type":"text","text":"visible"},{"type":"thinking","thinking":"reasoning"},` + previous + `]`,
		`[{"type":"text","text":"visible"},{"type":"thinking","thinking":"","signature":"` + signature + `"},{"type":"thinking","thinking":"reasoning"},{"type":"text","text":"unrelated"}]`,
	} {
		for _, compat := range []bool{false, true} {
			request := []byte(`{"messages":[{"role":"assistant","content":` + content + `},{"role":"user","content":"continue"}]}`)
			output, errConvert := convertClaudeRequestToGemini("gemini-4-carbon", request, false, compat)
			if errConvert != nil {
				t.Fatal(errConvert)
			}
			for _, part := range ojson.GetBytes(output, "contents.0.parts").Array() {
				if !part.Get("thought").Bool() && part.Get("thoughtSignature").Exists() {
					t.Errorf("compat=%t: signature crossed nonempty thought: %s", compat, output)
				}
			}
		}
	}
}

func TestGeminiClaudeDroppedThoughtSignatureDoesNotReachVisibleText(t *testing.T) {
	chunks := []string{
		`{"candidates":[{"content":{"parts":[{"text":"visible"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"reasoning","thought":true}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":""}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"thoughtSignature":"` + capturedGeminiThinkingSignature + `"}]},"finishReason":"STOP"}]}`,
		`[DONE]`,
	}
	var param any
	var events []byte
	for _, chunk := range chunks {
		events = append(events, bytes.Join(ConvertGeminiResponseToClaude(context.Background(), "gemini-4-carbon", nil, nil, []byte(chunk), &param), nil)...)
	}
	content := claudeContentFromGeminiStream(t, events)
	output := replayGeminiClaudeContent(t, content.String())
	parts := ojson.GetBytes(output, "contents.0.parts").Array()
	if len(parts) != 1 || parts[0].Get("text").String() != "visible" || parts[0].Get("thoughtSignature").Exists() {
		t.Fatalf("dropped thought signature rebound to visible text: %s; content=%s", output, content.String())
	}
}

func TestGeminiClaudeCarrierDoesNotConsumeWhitespaceThinking(t *testing.T) {
	request := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":" ","signature":"` + capturedGeminiThinkingSignature + `"},{"type":"tool_use","id":"call-1","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"ok"}]}]}`)
	for _, compat := range []bool{false, true} {
		output, errConvert := convertClaudeRequestToGemini("gemini-4-carbon", request, false, compat)
		if errConvert != nil {
			t.Fatal(errConvert)
		}
		parts := ojson.GetBytes(output, "contents.0.parts").Array()
		wantParts := 1
		if compat {
			wantParts = 2
		}
		if len(parts) != wantParts {
			t.Fatalf("compat=%t: nonempty thinking was consumed as a carrier: %s", compat, output)
		}
		if compat && (parts[0].Get("text").String() != " " || !parts[0].Get("thought").Bool() || parts[0].Get("thoughtSignature").String() != capturedGeminiThinkingSignature) {
			t.Errorf("compat thinking changed: %s", output)
		}
		if parts[len(parts)-1].Get("thoughtSignature").String() != sigcompat.GeminiSkipThoughtSignatureValidator {
			t.Errorf("compat=%t: nonempty thought signature leaked to tool: %s", compat, output)
		}
	}
}

func TestGeminiClaudeStreamingEmptyBlockDoesNotRetargetSignature(t *testing.T) {
	for _, empty := range []string{`{"text":""}`, `{"text":"","thought":true}`} {
		chunks := []string{
			`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"first","partialArgs":[],"willContinue":false}}]}}]}`,
			`{"candidates":[{"content":{"parts":[` + empty + `]}}]}`,
			`{"candidates":[{"content":{"parts":[{"thoughtSignature":"` + capturedGeminiThinkingSignature + `"}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"second","args":{}}}]},"finishReason":"STOP"}]}`,
			`[DONE]`,
		}
		var param any
		var events []byte
		for _, chunk := range chunks {
			events = append(events, bytes.Join(ConvertGeminiResponseToClaude(context.Background(), "gemini-4-carbon", nil, nil, []byte(chunk), &param), nil)...)
		}
		content := claudeContentFromGeminiStream(t, events)
		output := replayGeminiClaudeContent(t, content.String())
		parts := ojson.GetBytes(output, "contents.0.parts").Array()
		if len(parts) != 2 || parts[0].Get("thoughtSignature").String() != capturedGeminiThinkingSignature || parts[1].Get("thoughtSignature").String() != sigcompat.GeminiSkipThoughtSignatureValidator {
			t.Errorf("empty block %s retargeted tool signature: %s; content=%s", empty, output, content.String())
		}
	}
}

func TestGeminiClaudeNonStreamSignatureOnlyThoughtPartRoundTrip(t *testing.T) {
	upstream := []byte(`{"candidates":[{"content":{"parts":[{"text":"answer"},{"text":"","thought":true,"thoughtSignature":"` + capturedGeminiThinkingSignature + `"},{"functionCall":{"name":"lookup","args":{}}}]},"finishReason":"STOP"}]}`)
	response := ConvertGeminiResponseToClaudeNonStream(context.Background(), "gemini-4-carbon", nil, nil, upstream, nil)
	output := replayGeminiClaudeContent(t, ojson.GetBytes(response, "content").Raw)
	parts := ojson.GetBytes(output, "contents.0.parts").Array()
	if len(parts) != 2 || parts[0].Get("text").String() != "answer" || parts[0].Get("thoughtSignature").String() != capturedGeminiThinkingSignature || parts[1].Get("thoughtSignature").String() != sigcompat.GeminiSkipThoughtSignatureValidator {
		t.Fatalf("empty thought part retargeted visible text signature: %s; response=%s", output, response)
	}
}

func TestGeminiClaudeNonStreamConsecutiveTrailingSignatures(t *testing.T) {
	second := "EjQKMgEMOdbHO0Gd+c9Mxk4ELwPGbpCEcp2mFfYYLix2UVtBH3fL8GECc4+JITVnHF4qZDsE"
	upstream := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{}}},{"thoughtSignature":"` + capturedGeminiThinkingSignature + `"},{"thoughtSignature":"` + second + `"}]},"finishReason":"STOP"}]}`)
	response := ConvertGeminiResponseToClaudeNonStream(context.Background(), "gemini-4-carbon", nil, nil, upstream, nil)
	output := replayGeminiClaudeContent(t, ojson.GetBytes(response, "content").Raw)
	parts := ojson.GetBytes(output, "contents.0.parts").Array()
	if len(parts) != 2 || parts[0].Get("thoughtSignature").String() != second || parts[1].Get("thoughtSignature").String() != capturedGeminiThinkingSignature {
		t.Fatalf("consecutive signatures must retain their function target: %s; response=%s", output, response)
	}
}

func replayGeminiClaudeContent(t *testing.T, content string) []byte {
	t.Helper()
	request := ojson.NewDocString(`{"messages":[{"role":"assistant","content":` + content + `},{"role":"user","content":[]}]}`)
	for _, block := range ojson.Parse(content).Array() {
		if block.Get("type").String() == "tool_use" {
			result := ojson.NewDocString(`{"type":"tool_result","tool_use_id":"","content":"ok"}`)
			_ = result.Set("tool_use_id", block.Get("id").String())
			_ = request.SetRawBytes("messages.1.content.-1", result.Bytes())
		}
	}
	if request.Get("messages.1.content.#").Int() == 0 {
		_ = request.SetRaw("messages.1.content", `[{"type":"text","text":"continue"}]`)
	}
	output, errConvert := ConvertClaudeRequestToGemini("gemini-4-carbon", request.Bytes(), false)
	if errConvert != nil {
		t.Fatal(errConvert)
	}
	return output
}

func TestGeminiClaudeStreamingCompletedToolSignatureRoundTrip(t *testing.T) {
	chunks := []string{
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"first","partialArgs":[],"willContinue":false}}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"thoughtSignature":"` + capturedGeminiThinkingSignature + `"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"second","args":{}}}]},"finishReason":"STOP"}]}`,
		`[DONE]`,
	}
	var param any
	var events []byte
	for _, chunk := range chunks {
		events = append(events, bytes.Join(ConvertGeminiResponseToClaude(context.Background(), "gemini-4-carbon", nil, nil, []byte(chunk), &param), nil)...)
	}
	content := claudeContentFromGeminiStream(t, events)
	request := ojson.NewDocString(`{"messages":[{"role":"assistant","content":[]},{"role":"user","content":[]}]}`)
	_ = request.SetRawBytes("messages.0.content", content.Bytes())
	for _, block := range ojson.Parse(content.String()).Array() {
		if block.Get("type").String() == "tool_use" {
			result := ojson.NewDocString(`{"type":"tool_result","tool_use_id":"","content":"ok"}`)
			_ = result.Set("tool_use_id", block.Get("id").String())
			_ = request.SetRawBytes("messages.1.content.-1", result.Bytes())
		}
	}
	output, errConvert := ConvertClaudeRequestToGemini("gemini-4-carbon", request.Bytes(), false)
	if errConvert != nil {
		t.Fatal(errConvert)
	}
	parts := ojson.GetBytes(output, "contents.0.parts").Array()
	if len(parts) != 2 || parts[0].Get("thoughtSignature").String() != capturedGeminiThinkingSignature || parts[1].Get("thoughtSignature").String() != sigcompat.GeminiSkipThoughtSignatureValidator {
		t.Fatalf("signature after a closed tool block must bind backward: %s; content=%s", output, content.String())
	}
}

func TestGeminiClaudeStreamingLateSignatureRoundTrip(t *testing.T) {
	chunks := []string{
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"first","partialArgs":[{"jsonPath":"$.q","stringValue":"Paris"}],"willContinue":true}}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"thoughtSignature":"` + capturedGeminiThinkingSignature + `"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{}}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"second","args":{}}}]},"finishReason":"STOP"}]}`,
		`[DONE]`,
	}
	var param any
	var events []byte
	for _, chunk := range chunks {
		events = append(events, bytes.Join(ConvertGeminiResponseToClaude(context.Background(), "gemini-4-carbon", nil, nil, []byte(chunk), &param), nil)...)
	}
	content := claudeContentFromGeminiStream(t, events)
	request := ojson.NewDocString(`{"messages":[{"role":"assistant","content":[]},{"role":"user","content":[]}]}`)
	_ = request.SetRawBytes("messages.0.content", content.Bytes())
	for _, block := range ojson.Parse(content.String()).Array() {
		if block.Get("type").String() == "tool_use" {
			result := ojson.NewDocString(`{"type":"tool_result","tool_use_id":"","content":"ok"}`)
			_ = result.Set("tool_use_id", block.Get("id").String())
			_ = request.SetRawBytes("messages.1.content.-1", result.Bytes())
		}
	}
	output, errConvert := ConvertClaudeRequestToGemini("gemini-4-carbon", request.Bytes(), false)
	if errConvert != nil {
		t.Fatal(errConvert)
	}
	parts := ojson.GetBytes(output, "contents.0.parts").Array()
	if len(parts) != 2 || parts[0].Get("thoughtSignature").String() != capturedGeminiThinkingSignature || parts[1].Get("thoughtSignature").String() != sigcompat.GeminiSkipThoughtSignatureValidator {
		t.Fatalf("late signature must stay on first call, got %s; content=%s", output, content.String())
	}
}

func claudeContentFromGeminiStream(t *testing.T, events []byte) *ojson.Doc {
	t.Helper()
	content := ojson.NewDocString(`[]`)
	arguments := make(map[int]string)
	for _, line := range strings.Split(string(events), "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		event := ojson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		index := int(event.Get("index").Int())
		switch event.Get("type").String() {
		case "content_block_start":
			_ = content.SetRaw("-1", event.Get("content_block").Raw)
		case "content_block_delta":
			switch event.Get("delta.type").String() {
			case "signature_delta":
				_ = content.Set(strconv.Itoa(index)+".signature", event.Get("delta.signature").String())
			case "input_json_delta":
				arguments[index] += event.Get("delta.partial_json").String()
			case "text_delta":
				path := strconv.Itoa(index) + ".text"
				_ = content.Set(path, content.Get(path).String()+event.Get("delta.text").String())
			case "thinking_delta":
				path := strconv.Itoa(index) + ".thinking"
				_ = content.Set(path, content.Get(path).String()+event.Get("delta.thinking").String())
			}
		case "content_block_stop":
			if args := arguments[index]; args != "" {
				if !ojson.Valid(args) {
					t.Fatalf("invalid streamed tool arguments: %q", args)
				}
				_ = content.SetRaw(strconv.Itoa(index)+".input", args)
			}
		}
	}
	return content
}

func TestGeminiClaudeTrailingTextSignatureRoundTrip(t *testing.T) {
	for _, stream := range []bool{false, true} {
		upstream := []byte(`{"candidates":[{"content":{"parts":[{"text":"answer"},{"thoughtSignature":"` + capturedGeminiThinkingSignature + `"},{"functionCall":{"name":"lookup","args":{}}}]},"finishReason":"STOP"}]}`)
		var content string
		if stream {
			var param any
			events := bytes.Join(ConvertGeminiResponseToClaude(context.Background(), "gemini-4-carbon", nil, nil, upstream, &param), nil)
			events = append(events, bytes.Join(ConvertGeminiResponseToClaude(context.Background(), "gemini-4-carbon", nil, nil, []byte("[DONE]"), &param), nil)...)
			content = claudeContentFromGeminiStream(t, events).String()
		} else {
			response := ConvertGeminiResponseToClaudeNonStream(context.Background(), "gemini-4-carbon", nil, nil, upstream, nil)
			content = ojson.GetBytes(response, "content").Raw
		}
		request := ojson.NewDocString(`{"messages":[{"role":"assistant","content":` + content + `},{"role":"user","content":[{"type":"tool_result","tool_use_id":"","content":"ok"}]}]}`)
		_ = request.Set("messages.1.content.0.tool_use_id", ojson.Parse(content).Get("2.id").String())
		output, errConvert := ConvertClaudeRequestToGemini("gemini-4-carbon", request.Bytes(), false)
		if errConvert != nil {
			t.Fatal(errConvert)
		}
		parts := ojson.GetBytes(output, "contents.0.parts").Array()
		if len(parts) != 2 || parts[0].Get("text").String() != "answer" || parts[0].Get("thoughtSignature").String() != capturedGeminiThinkingSignature || parts[1].Get("thoughtSignature").String() != sigcompat.GeminiSkipThoughtSignatureValidator {
			t.Fatalf("stream=%t: text signature was rebound to a later tool: %s", stream, output)
		}
	}
}

func TestGeminiClaudeThoughtSignatureRoundTrip(t *testing.T) {
	vertexPayload := make([]byte, 405)
	copy(vertexPayload, []byte{1, 0x8f, 0x3d, 0x6b, 0x5f})
	for i := 5; i < len(vertexPayload); i++ {
		vertexPayload[i] = byte(0xa0 + i%0x20)
	}
	for _, signature := range []string{capturedGeminiThinkingSignature, base64.StdEncoding.EncodeToString(vertexPayload)} {
		t.Run(signature[:8], func(t *testing.T) {
			upstream := ojson.NewDocString(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"q":"Paris"}}}]},"finishReason":"STOP"}]}`)
			_ = upstream.Set("candidates.0.content.parts.0.thoughtSignature", signature)
			response := ConvertGeminiResponseToClaudeNonStream(context.Background(), "gemini-4-carbon", nil, nil, upstream.Bytes(), nil)
			content := ojson.GetBytes(response, "content")
			toolID := content.Get("1.id").String()
			if toolID == "" {
				t.Fatalf("missing tool_use after signature carrier: %s", response)
			}
			request := ojson.NewDocString(`{"messages":[{"role":"assistant","content":[]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"","content":"ok"}]}]}`)
			_ = request.SetRaw("messages.0.content", content.Raw)
			_ = request.Set("messages.1.content.0.tool_use_id", toolID)
			for _, compat := range []bool{false, true} {
				output, errConvert := convertClaudeRequestToGemini("gemini-4-carbon", request.Bytes(), false, compat)
				if errConvert != nil {
					t.Fatal(errConvert)
				}
				output = sigcompat.SanitizeGeminiRequestThoughtSignatures(output, "contents")
				parts := ojson.GetBytes(output, "contents.0.parts").Array()
				if len(parts) != 1 || !parts[0].Get("functionCall").Exists() {
					t.Fatalf("compat=%t: carrier must be attached to functionCall, got %s", compat, output)
				}
				if got := parts[0].Get("thoughtSignature").String(); got != signature {
					t.Fatalf("compat=%t: replayed signature = %q, want original %q", compat, got, signature)
				}
			}
		})
	}
}
