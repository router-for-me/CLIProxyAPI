package executor

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestTranslateCodexRequestPairReusesEqualPayload(t *testing.T) {
	from := sdktranslator.Format("codex-test-from-equal")
	to := sdktranslator.Format("codex-test-to-equal")
	var calls int32
	sdktranslator.Register(from, to, func(model string, rawJSON []byte, stream bool) []byte {
		atomic.AddInt32(&calls, 1)
		if model != "test-model" {
			t.Errorf("model = %q, want test-model", model)
		}
		if !stream {
			t.Error("stream = false, want true")
		}
		return append([]byte(nil), rawJSON...)
	}, sdktranslator.ResponseTransform{})

	payload := []byte(`{"model":"test-model","input":[{"role":"user"}]}`)
	originalTranslated, body, err := translateCodexRequestPair(from, to, "test-model", payload, bytes.Clone(payload), true)
	if err != nil {
		t.Fatalf("translateCodexRequestPair() error = %v", err)
	}

	if gotCalls := atomic.LoadInt32(&calls); gotCalls != 1 {
		t.Fatalf("TranslateRequest calls = %d, want 1", gotCalls)
	}
	if !bytes.Equal(originalTranslated, body) {
		t.Fatalf("translated payloads differ: original=%s body=%s", originalTranslated, body)
	}
}

func TestTranslateCodexRequestPairTranslatesDifferentPayloads(t *testing.T) {
	from := sdktranslator.Format("codex-test-from-different")
	to := sdktranslator.Format("codex-test-to-different")
	var calls int32
	sdktranslator.Register(from, to, func(_ string, rawJSON []byte, _ bool) []byte {
		atomic.AddInt32(&calls, 1)
		return append([]byte(nil), rawJSON...)
	}, sdktranslator.ResponseTransform{})

	originalPayload := []byte(`{"model":"test-model","input":[{"role":"system"}]}`)
	payload := []byte(`{"model":"test-model","input":[{"role":"user"}]}`)
	originalTranslated, body, err := translateCodexRequestPair(from, to, "test-model", originalPayload, payload, false)
	if err != nil {
		t.Fatalf("translateCodexRequestPair() error = %v", err)
	}

	if gotCalls := atomic.LoadInt32(&calls); gotCalls != 2 {
		t.Fatalf("TranslateRequest calls = %d, want 2", gotCalls)
	}
	if !bytes.Equal(originalTranslated, originalPayload) {
		t.Fatalf("original translated = %s, want %s", originalTranslated, originalPayload)
	}
	if !bytes.Equal(body, payload) {
		t.Fatalf("body = %s, want %s", body, payload)
	}
}

func TestTranslateCodexRequestPairRejectsLossyClaudePayload(t *testing.T) {
	tests := []struct {
		name     string
		original []byte
		payload  []byte
	}{
		{
			name:     "original payload",
			original: []byte(`{"messages":[{"role":"user","content":"hello"}],"stop_sequences":["END"]}`),
			payload:  []byte(`{"messages":[{"role":"user","content":"hello"}],"stop_sequences":["END"]}`),
		},
		{
			name:     "transformed payload",
			original: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
			payload:  []byte(`{"messages":[{"role":"user","content":[{"type":"audio","data":"ignored"}]}]}`),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := translateCodexRequestPair(sdktranslator.FormatClaude, sdktranslator.FormatCodex, "gpt-5.4", test.original, test.payload, true)
			if err == nil {
				t.Fatal("translateCodexRequestPair() error = nil, want rejection")
			}
			var requestScoped interface{ IsRequestScoped() bool }
			if !errors.As(err, &requestScoped) || !requestScoped.IsRequestScoped() {
				t.Fatalf("error %T is not request-scoped: %v", err, err)
			}
			var status interface{ StatusCode() int }
			if !errors.As(err, &status) || status.StatusCode() != http.StatusBadRequest {
				t.Fatalf("error status = %v, want %d", err, http.StatusBadRequest)
			}
		})
	}
}

func TestTranslateCodexRequestPairPreservesModernClaudeToolResults(t *testing.T) {
	sharedSuffix := strings.Repeat("a", 70)
	firstToolName := "mcp__first_server__" + sharedSuffix
	secondToolName := "mcp__second_server__" + sharedSuffix
	payload := []byte(`{
		"model":"claude-opus-5",
		"max_tokens":4096,
		"tools":[
			{"name":"ToolSearch","input_schema":{"type":"object"}},
			{"name":"` + firstToolName + `","input_schema":{"type":"object"},"defer_loading":true},
			{"name":"` + secondToolName + `","input_schema":{"type":"object"},"defer_loading":true}
		],
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_search","name":"ToolSearch","input":{"query":"resource"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_search","content":[
				{"type":"text","text":"found tools"},
				{"type":"tool_reference","tool_name":"` + firstToolName + `"},
				{"type":"search_result","source":"https://example.test/result","title":"Result","content":[{"type":"text","text":"search body"}]},
				{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0xLjQK"},"title":"reference.pdf"},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}},
				{"type":"tool_reference","tool_name":"` + secondToolName + `"}
			]}]},
			{"role":"user","content":"continue"}
		]
	}`)

	_, body, err := translateCodexRequestPair(sdktranslator.FormatClaude, sdktranslator.FormatCodex, "gpt-5.6-sol", payload, bytes.Clone(payload), false)
	if err != nil {
		t.Fatalf("translateCodexRequestPair() error = %v", err)
	}

	var functionOutput gjson.Result
	inputs := gjson.GetBytes(body, "input").Array()
	for _, input := range inputs {
		if input.Get("type").String() == "function_call_output" {
			functionOutput = input
			break
		}
	}
	if !functionOutput.Exists() {
		t.Fatalf("missing function_call_output. Output: %s", body)
	}

	output := functionOutput.Get("output").Array()
	wantTypes := []string{"input_text", "input_text", "input_text", "input_file", "input_image", "input_text"}
	if len(output) != len(wantTypes) {
		t.Fatalf("got %d tool result items, want %d. Output: %s", len(output), len(wantTypes), body)
	}
	for i, wantType := range wantTypes {
		if got := output[i].Get("type").String(); got != wantType {
			t.Fatalf("output[%d].type = %q, want %q. Output: %s", i, got, wantType, body)
		}
	}

	firstReference := gjson.Parse(output[1].Get("text").String())
	secondReference := gjson.Parse(output[5].Get("text").String())
	if got, want := firstReference.Get("tool_name").String(), gjson.GetBytes(body, "tools.1.name").String(); got != want {
		t.Fatalf("first reference name = %q, want translated tool name %q", got, want)
	}
	if got, want := secondReference.Get("tool_name").String(), gjson.GetBytes(body, "tools.2.name").String(); got != want {
		t.Fatalf("second reference name = %q, want translated tool name %q", got, want)
	}
	if firstReference.Get("tool_name").String() == secondReference.Get("tool_name").String() {
		t.Fatalf("collision-safe translated tool names must differ. Output: %s", body)
	}

	if got := gjson.Parse(output[2].Get("text").String()).Get("content.0.text").String(); got != "search body" {
		t.Fatalf("search result content = %q, want search body", got)
	}
	if got := output[3].Get("file_data").String(); got != "data:application/pdf;base64,JVBERi0xLjQK" {
		t.Fatalf("document file_data = %q, want PDF data URL", got)
	}
	if got := output[3].Get("filename").String(); got != "reference.pdf" {
		t.Fatalf("document filename = %q, want reference.pdf", got)
	}
	if got := output[4].Get("image_url").String(); got != "data:image/png;base64,aW1hZ2U=" {
		t.Fatalf("image_url = %q, want image data URL", got)
	}
	if got := inputs[len(inputs)-1].Get("content.0.text").String(); got != "continue" {
		t.Fatalf("subsequent turn = %q, want continue", got)
	}
}
