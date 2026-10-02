package helps

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type pairRequestPluginHooks struct {
	calls       int64
	onNormalize func(body []byte)
}

func (h *pairRequestPluginHooks) NormalizeRequest(_ context.Context, _, _ sdktranslator.Format, _ string, body []byte, _ bool) []byte {
	h.calls++
	if h.onNormalize != nil {
		h.onNormalize(body)
	}
	updated, _ := sjson.SetBytes(body, "plugin_call", h.calls)
	return updated
}

func (*pairRequestPluginHooks) TranslateRequest(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, bool) ([]byte, bool) {
	return nil, false
}

func (*pairRequestPluginHooks) NormalizeResponseBefore(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) []byte {
	return nil
}

func (*pairRequestPluginHooks) TranslateResponse(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) ([]byte, bool) {
	return nil, false
}

func (*pairRequestPluginHooks) NormalizeResponseAfter(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) []byte {
	return nil
}

func geminiToolHistoryPayload(turns int) []byte {
	contents := []string{`{"role":"user","parts":[{"text":"start"}]}`}
	for i := 0; i < turns; i++ {
		contents = append(contents,
			fmt.Sprintf(`{"role":"user","parts":[{"text":"ask %d"}]}`, i),
			fmt.Sprintf(`{"role":"model","parts":[{"text":"think %d"},{"thoughtSignature":"sig-%d","functionCall":{"id":"c%d","name":"read_file","args":{"path":"a%d.go"}}}]}`, i, i, i, i),
			fmt.Sprintf(`{"role":"user","parts":[{"functionResponse":{"id":"c%d","name":"read_file","response":{"content":"data %d"}}}]}`, i, i),
			fmt.Sprintf(`{"role":"model","parts":[{"text":"answer %d"}]}`, i))
	}
	return []byte(fmt.Sprintf(
		`{"contents":[%s],"tools":[{"functionDeclarations":[{"name":"read_file","description":"read a file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}]}],"generationConfig":{"temperature":1}}`,
		strings.Join(contents, ",")))
}

// TestTranslateRequestPairMatchesSeparateTranslations pins the reuse fast path to
// the behavior of translating both payloads independently.
func TestTranslateRequestPairMatchesSeparateTranslations(t *testing.T) {
	from := sdktranslator.FormatGemini
	to := sdktranslator.FromString("antigravity")
	cfg := &config.Config{}
	const model = "gemini-3.6-flash-high"

	for _, turns := range []int{0, 1, 5, 20} {
		payload := geminiToolHistoryPayload(turns)
		// Same bytes in a different backing array forces the translate-twice branch.
		detached := append([]byte(nil), payload...)

		want := TranslateRequestWithCodexMultiAgentV2(context.Background(), http.Header{}, cfg, from, to, model, payload, true)

		reuseBase, reuseWork := TranslateRequestPairWithCodexMultiAgentV2(
			context.Background(), http.Header{}, cfg, from, to, model, payload, payload, true)
		twiceBase, twiceWork := TranslateRequestPairWithCodexMultiAgentV2(
			context.Background(), http.Header{}, cfg, from, to, model, payload, detached, true)

		for name, got := range map[string][]byte{
			"reuse baseline": reuseBase,
			"reuse working":  reuseWork,
			"twice baseline": twiceBase,
			"twice working":  twiceWork,
		} {
			if !bytes.Equal(want, got) {
				t.Fatalf("turns=%d: %s translation differs from a standalone translation", turns, name)
			}
		}

		if len(reuseBase) > 0 && &reuseBase[0] == &reuseWork[0] {
			t.Fatalf("turns=%d: working copy aliases the baseline; later in-place edits would corrupt it", turns)
		}

		// The caller mutates the working copy, so the baseline must stay intact.
		baselineBefore := append([]byte(nil), reuseBase...)
		reuseWork[0] = 'X'
		if !bytes.Equal(baselineBefore, reuseBase) {
			t.Fatalf("turns=%d: mutating the working copy changed the baseline", turns)
		}
	}
}

// TestTranslateRequestPairTranslatesDistinctPayloads guards the case where the
// executor really does hand over two different requests.
func TestTranslateRequestPairTranslatesDistinctPayloads(t *testing.T) {
	from := sdktranslator.FormatGemini
	to := sdktranslator.FromString("antigravity")
	cfg := &config.Config{}
	const model = "gemini-3.6-flash-high"

	original := geminiToolHistoryPayload(2)
	request := geminiToolHistoryPayload(4)

	base, work := TranslateRequestPairWithCodexMultiAgentV2(
		context.Background(), http.Header{}, cfg, from, to, model, original, request, true)

	wantBase := TranslateRequestWithCodexMultiAgentV2(context.Background(), http.Header{}, cfg, from, to, model, original, true)
	wantWork := TranslateRequestWithCodexMultiAgentV2(context.Background(), http.Header{}, cfg, from, to, model, request, true)

	if !bytes.Equal(wantBase, base) {
		t.Fatal("baseline translation differs for distinct payloads")
	}
	if !bytes.Equal(wantWork, work) {
		t.Fatal("working translation differs for distinct payloads")
	}
	if bytes.Equal(base, work) {
		t.Fatal("distinct payloads produced identical translations; the reuse path was taken by mistake")
	}
}

func TestTranslateRequestPairInvokesPluginOncePerInput(t *testing.T) {
	hooks := &pairRequestPluginHooks{}
	sdktranslator.SetPluginHooks(hooks)
	t.Cleanup(func() { sdktranslator.SetPluginHooks(nil) })

	payload := geminiToolHistoryPayload(1)
	for _, stream := range []bool{false, true} {
		for _, distinct := range []bool{false, true} {
			hooks.calls = 0
			request := payload
			wantCalls := int64(1)
			if distinct {
				request = geminiToolHistoryPayload(2)
				wantCalls = 2
			}
			base, work := TranslateRequestPairWithCodexMultiAgentV2(
				context.Background(), http.Header{}, &config.Config{},
				sdktranslator.FormatGemini, sdktranslator.FormatAntigravity,
				"gemini-3.6-flash-high", payload, request, stream,
			)
			if hooks.calls != wantCalls {
				t.Fatalf("stream=%v distinct=%v: plugin calls = %d, want %d", stream, distinct, hooks.calls, wantCalls)
			}
			if got := gjson.GetBytes(base, "plugin_call").Int(); got != 1 {
				t.Fatalf("baseline plugin_call = %d, want 1", got)
			}
			if got := gjson.GetBytes(work, "plugin_call").Int(); got != wantCalls {
				t.Fatalf("working plugin_call = %d, want %d", got, wantCalls)
			}
			if !distinct && !bytes.Equal(base, work) {
				t.Fatal("same input produced different plugin results")
			}
			baselineBefore := bytes.Clone(base)
			inputBefore := bytes.Clone(request)
			work[0] = 'X'
			if !bytes.Equal(base, baselineBefore) || !bytes.Equal(request, inputBefore) {
				t.Fatal("working buffer aliases the baseline or input")
			}
		}
	}
}

func TestSameByteSlice(t *testing.T) {
	buf := []byte("payload")
	cases := []struct {
		name string
		a, b []byte
		want bool
	}{
		{"identical slice", buf, buf, true},
		{"same array same length", buf[:3], buf[:3], true},
		{"equal bytes different array", buf, append([]byte(nil), buf...), false},
		{"different length", buf, buf[:3], false},
		{"both nil", nil, nil, true},
		{"nil and empty", nil, []byte{}, true},
		{"nil and non-empty", nil, buf, false},
		{"offset alias", buf, buf[1:], false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameByteSlice(tc.a, tc.b); got != tc.want {
				t.Fatalf("sameByteSlice() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTranslateRequestEnvelopePairWithCodexMultiAgentV2UsesModelInfo(t *testing.T) {
	trueVal := true
	falseVal := false
	const model = "gemini-3.8-flash-high"

	input := []byte(`{
		"model": "` + model + `",
		"input": "Search weather",
		"tools": [{"type": "web_search"}]
	}`)

	enabled := &registry.ModelInfo{
		ID:                 model,
		NativeCapabilities: &registry.NativeCapabilities{WebSearch: &trueVal},
	}
	envelope := sdktranslator.RequestEnvelope{Format: sdktranslator.FormatOpenAIResponse, Model: model, ModelInfo: enabled}
	base, work := TranslateRequestEnvelopePairWithCodexMultiAgentV2(context.Background(), http.Header{}, &config.Config{}, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatAntigravity, envelope, input, input)
	if gjson.GetBytes(base, "requestType").String() != "web_search" {
		t.Fatalf("expected baseline requestType web_search, got: %s", base)
	}
	if gjson.GetBytes(work, "requestType").String() != "web_search" {
		t.Fatalf("expected working requestType web_search, got: %s", work)
	}

	disabled := &registry.ModelInfo{
		ID:                 model,
		NativeCapabilities: &registry.NativeCapabilities{WebSearch: &falseVal},
	}
	envelope.ModelInfo = disabled
	_, workDisabled := TranslateRequestEnvelopePairWithCodexMultiAgentV2(context.Background(), http.Header{}, &config.Config{}, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatAntigravity, envelope, input, input)
	if gjson.GetBytes(workDisabled, "requestType").String() == "web_search" {
		t.Fatalf("expected non-web_search when capability disabled, got: %s", workDisabled)
	}
}

func TestTranslateRequestWithAPIKeyModelCompatibility_InvokesPluginNormalizers(t *testing.T) {
	var summaryDisplayInHook string
	hooks := &pairRequestPluginHooks{
		onNormalize: func(body []byte) {
			summaryDisplayInHook = gjson.GetBytes(body, "thinking.display").String()
		},
	}
	sdktranslator.SetPluginHooks(hooks)
	t.Cleanup(func() { sdktranslator.SetPluginHooks(nil) })

	cfg := &config.Config{}
	payload := []byte(`{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"high","include_reasoning":true}`)

	out := TranslateRequestWithAPIKeyModelCompatibility(
		context.Background(),
		http.Header{},
		cfg,
		sdktranslator.FormatOpenAI,
		sdktranslator.FormatClaude,
		"claude-3-5-sonnet",
		payload,
		false,
		true, // isCompat
	)

	if hooks.calls != 1 {
		t.Fatalf("plugin hook calls = %d, want 1", hooks.calls)
	}
	if got := gjson.GetBytes(out, "plugin_call").Int(); got != 1 {
		t.Fatalf("plugin_call = %d, want 1; output was %s", got, out)
	}
	// Assert summary config was applied to the body before invoking the normalizer hook
	if summaryDisplayInHook != "summarized" {
		t.Fatalf("expected thinking.display = summarized in body delivered to normalizer, got: %q", summaryDisplayInHook)
	}

	// Also verify that non-compat path invokes normalizers exactly once
	hooks.calls = 0
	outNonCompat := TranslateRequestWithAPIKeyModelCompatibility(
		context.Background(),
		http.Header{},
		cfg,
		sdktranslator.FormatClaude,
		sdktranslator.FormatOpenAI,
		"claude-3-5-sonnet",
		payload,
		false,
		false, // non-compat
	)
	if hooks.calls != 1 {
		t.Fatalf("non-compat plugin hook calls = %d, want 1", hooks.calls)
	}
	if got := gjson.GetBytes(outNonCompat, "plugin_call").Int(); got != 1 {
		t.Fatalf("non-compat plugin_call = %d, want 1; output was %s", got, outNonCompat)
	}

	// Also verify stream = true path
	hooks.calls = 0
	outStream := TranslateRequestWithAPIKeyModelCompatibility(
		context.Background(),
		http.Header{},
		cfg,
		sdktranslator.FormatClaude,
		sdktranslator.FormatOpenAI,
		"claude-3-5-sonnet",
		payload,
		true, // stream
		true, // isCompat
	)
	if hooks.calls != 1 {
		t.Fatalf("stream compat plugin hook calls = %d, want 1", hooks.calls)
	}
	if got := gjson.GetBytes(outStream, "plugin_call").Int(); got != 1 {
		t.Fatalf("stream compat plugin_call = %d, want 1; output was %s", got, outStream)
	}
}

func TestTranslateRequestCompatibilityForExecutorToolIntegerTypes(t *testing.T) {
	const responsesPayload = `{"input":"hi","tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"yield_time_ms":{"type":"number"},"unrelated":{"type":"number"}}}}]}`
	const claudePayload = `{"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"exec_command","input_schema":{"type":"object","properties":{"yield_time_ms":{"type":"number"},"unrelated":{"type":"number"}}}}]}`
	for _, route := range []struct {
		name       string
		from, to   sdktranslator.Format
		payload    string
		properties string
	}{
		{name: "responses_to_codex", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatCodex, payload: responsesPayload, properties: "tools.0.parameters.properties"},
		{name: "claude_to_codex", from: sdktranslator.FormatClaude, to: sdktranslator.FormatCodex, payload: claudePayload, properties: "tools.0.parameters.properties"},
		{name: "responses_to_claude", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatClaude, payload: responsesPayload, properties: "tools.0.input_schema.properties"},
		{name: "responses_passthrough", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatOpenAIResponse, payload: responsesPayload, properties: "tools.0.parameters.properties"},
	} {
		for _, target := range []struct {
			name     string
			preserve bool
		}{
			{name: "codex", preserve: true},
			{name: "codex-websockets", preserve: true},
			{name: "xai"},
			{name: "meta"},
			{name: ""},
		} {
			for _, ua := range []string{"codex_cli_rs/0.1", "curl/8.7.1", ""} {
				for _, compat := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/target=%s/ua=%s/compat=%t", route.name, target.name, ua, compat), func(t *testing.T) {
						var headers http.Header
						if ua != "" {
							headers = http.Header{"User-Agent": []string{ua}, "X-Openai-Subagent": []string{"collab_spawn"}}
						}
						payload := []byte(route.payload)
						out, changed := TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntentForExecutor(t.Context(), headers, &config.Config{}, target.name, route.from, route.to, "model", payload, false, compat)
						outputs := map[string][]byte{
							"update_intent": out,
							"body":          TranslateRequestWithAPIKeyModelCompatibilityForExecutor(t.Context(), headers, &config.Config{}, target.name, route.from, route.to, "model", payload, false, compat),
						}
						if target.name == "" {
							outputs["legacy_body"] = TranslateRequestWithAPIKeyModelCompatibility(t.Context(), headers, &config.Config{}, route.from, route.to, "model", payload, false, compat)
							outputs["legacy_update_intent"], _ = TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntent(t.Context(), headers, &config.Config{}, route.from, route.to, "model", payload, false, compat)
						}
						wantType := "number"
						if ua == "codex_cli_rs/0.1" && !target.preserve {
							wantType = "integer"
						}
						for entry, body := range outputs {
							if got := gjson.GetBytes(body, route.properties+".yield_time_ms.type").String(); got != wantType {
								t.Errorf("%s: yield_time_ms.type = %q, want %q; body=%s", entry, got, wantType, body)
							}
							if got := gjson.GetBytes(body, route.properties+".unrelated.type").String(); got != "number" {
								t.Errorf("%s: unrelated.type = %q, want number", entry, got)
							}
						}
						if changed {
							t.Error("schema normalization reported a plugin configuration update")
						}
						if string(payload) != route.payload {
							t.Error("source payload was mutated")
						}
						if ua != "" && (headers.Get("User-Agent") != ua || headers.Get("X-Openai-Subagent") != "collab_spawn") {
							t.Error("request headers were mutated")
						}
					})
				}
			}
		}
	}
}

func TestTranslateRequestWithCodexMultiAgentV2_NormalizesCodexToolTypes(t *testing.T) {
	payload := []byte(`{
		"model": "gpt-5.5",
		"input": [{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
		"tools": [
			{
				"type": "function",
				"name": "exec_command",
				"parameters": {
					"type": "object",
					"properties": {
						"yield_time_ms": {"type": "number"},
						"timeout_ms": {"type": "number"}
					}
				}
			}
		]
	}`)

	headers := http.Header{"User-Agent": []string{"codex-tui/0.154.0"}}
	out := TranslateRequestWithCodexMultiAgentV2(
		context.Background(),
		headers,
		&config.Config{},
		sdktranslator.FormatOpenAIResponse,
		sdktranslator.FormatClaude,
		"claude-opus-5-5",
		payload,
		false,
	)

	// In Claude format, tool is under tools[0].input_schema.properties
	if got := gjson.GetBytes(out, "tools.0.input_schema.properties.yield_time_ms.type").String(); got != "integer" {
		t.Errorf("translated Claude tool yield_time_ms type = %q, want integer; out=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.0.input_schema.properties.timeout_ms.type").String(); got != "integer" {
		t.Errorf("translated Claude tool timeout_ms type = %q, want integer; out=%s", got, out)
	}

	outGemini := TranslateRequestWithCodexMultiAgentV2(
		context.Background(),
		headers,
		&config.Config{},
		sdktranslator.FormatOpenAIResponse,
		sdktranslator.FormatGemini,
		"gemini-2.5-flash",
		payload,
		false,
	)

	// In Gemini format, tool is under functionDeclarations with parameters or parametersJsonSchema
	geminiParam := gjson.GetBytes(outGemini, "tools.0.functionDeclarations.0.parametersJsonSchema.properties")
	if !geminiParam.Exists() {
		geminiParam = gjson.GetBytes(outGemini, "tools.0.function_declarations.0.parameters.properties")
	}
	if got := geminiParam.Get("yield_time_ms.type").String(); got != "integer" {
		t.Errorf("translated Gemini tool yield_time_ms type = %q, want integer; out=%s", got, outGemini)
	}
	if got := geminiParam.Get("timeout_ms.type").String(); got != "integer" {
		t.Errorf("translated Gemini tool timeout_ms type = %q, want integer; out=%s", got, outGemini)
	}
}
