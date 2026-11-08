package helps

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"sync"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestPayloadFinalizerDefaultsUseOriginalAndNormalizeBeforeRules(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{
		Default:  []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "alias", Protocol: "openai", FromProtocol: "claude", Headers: map[string]string{"X-Test": "yes"}}}, Params: map[string]any{"missing": "user default", "present": "not applied"}}},
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "alias"}}, Params: map[string]any{"tools.0.function.parameters.properties.count.type": "number"}}},
		Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "alias"}}, Params: []string{"late"}}},
	}}
	req := cliproxyexecutor.Request{Model: "alias"}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{"X-Test": {"yes"}, "User-Agent": {"codex-cli/0.1"}}}
	finalize := NewPayloadFinalizer(cfg, "openai", "upstream", "openai", "", []byte(`{"present":"caller"}`), req, opts)
	body := []byte(`{"missing":"built-in","present":"caller","late":"injected","tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{"count":{"type":"integer"}}}}}]}`)
	out := FinalizePayload(WithPayloadFinalizer(context.Background(), finalize), body)
	if ojson.GetBytes(out, "missing").String() != "user default" || ojson.GetBytes(out, "present").String() != "caller" || ojson.GetBytes(out, "late").Exists() || ojson.GetBytes(out, "tools.0.function.parameters.properties.count.type").String() != "number" {
		t.Fatalf("unexpected final payload: %s", out)
	}
}

func TestPayloadFinalizerDevinBusinessFieldsAndCredentials(t *testing.T) {
	wire := BuildDevinGetChatMessageRequest("secret-token", "device", "model", "system", []DevinPrompt{{MessageID: "id", Source: 1, Content: "hello"}}, []DevinTool{{Name: "f", Parameters: []byte(`{"type":"object"}`)}}, nil, 1024, "session", "cascade", nil)
	unchanged, _, errUnchanged := FinalizeDevinPayload(wire, func(body []byte) []byte { return body })
	if errUnchanged != nil || !bytes.Equal(unchanged, wire) {
		t.Fatalf("no-op changed protobuf: %v", errUnchanged)
	}
	final, view, errFinalize := FinalizeDevinPayload(wire, func(body []byte) []byte {
		doc := ojson.NewDoc(body)
		_ = doc.Delete("system_prompt")
		_ = doc.Delete("tools")
		_ = doc.Set("completion_config.max_tokens", 0)
		_ = doc.Set("prompts.0.content", "configured")
		return doc.Bytes()
	})
	if errFinalize != nil {
		t.Fatal(errFinalize)
	}
	if ojson.GetBytes(view, "system_prompt").Exists() || ojson.GetBytes(view, "tools").Exists() {
		t.Fatalf("filter undone: %s", view)
	}
	decoded, errDecode := decodeDevinPayload(final, devinPayloadFields)
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if decoded["completion_config"].(map[string]any)["max_tokens"] != uint64(0) {
		t.Fatalf("default was re-injected: %v", decoded)
	}
	fieldOne := func(wire []byte) []byte {
		for len(wire) > 0 {
			num, kind, tagLen := protowire.ConsumeTag(wire)
			n := protowire.ConsumeFieldValue(num, kind, wire[tagLen:])
			if num == 1 {
				return wire[:tagLen+n]
			}
			wire = wire[tagLen+n:]
		}
		return nil
	}
	if !bytes.Equal(fieldOne(wire), fieldOne(final)) {
		t.Fatal("payload rules changed credentials")
	}
}

func TestPayloadFinalizerMultipartPreservesFiles(t *testing.T) {
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	if errWrite := writer.WriteField("model", "original"); errWrite != nil {
		t.Fatal(errWrite)
	}
	file, errFile := writer.CreateFormFile("image", "input.png")
	if errFile != nil {
		t.Fatal(errFile)
	}
	if _, errWrite := file.Write([]byte{0, 255, 13, 10}); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "original"}}, Params: map[string]any{"model": "configured"}}}}}
	req := cliproxyexecutor.Request{Model: "original", Payload: buffer.Bytes()}
	opts := cliproxyexecutor.Options{Headers: http.Header{"Content-Type": {writer.FormDataContentType()}}}
	body, contentType, errFinalize := ApplyMediaPayloadConfig(cfg, "openai", "original", "openai", req.Payload, writer.FormDataContentType(), req, opts)
	if errFinalize != nil {
		t.Fatal(errFinalize)
	}
	view, _, errView := mediaPayloadJSON(body, contentType)
	if errView != nil {
		t.Fatal(errView)
	}
	if ojson.GetBytes(view, "model").String() != "configured" || ojson.GetBytes(view, "image.data").String() != "AP8NCg==" {
		t.Fatalf("unexpected multipart business payload: %s", view)
	}
}

func TestPayloadRulesMatchFinalBodyAndTrackPaths(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"max_tokens": 100}}}}, Params: map[string]any{"unexpected": true}},
			{Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"max_tokens": 300}}}}, Params: map[string]any{"diagnostics.user": true}}},
		Filter: []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"max_tokens": 300}}}}, Params: []string{"context_management", "messages.0"}}},
	}}
	original := []byte(`{"max_tokens":100,"messages":[{},{}]}`)
	body, touched := ApplyPayloadConfigWithTrackedPaths(cfg, "model", "claude", "claude", "", []byte(`{"max_tokens":300,"context_management":{"builtin":true},"messages":[{},{},{}]}`), original, "model", "", nil, "diagnostics", "context_management")
	if ojson.GetBytes(body, "unexpected").Exists() || !ojson.GetBytes(body, "diagnostics.user").Bool() || ojson.GetBytes(body, "context_management").Exists() || ojson.GetBytes(body, "messages.#").Int() != 2 {
		t.Fatalf("rules did not match final body: %s", body)
	}
	if !touched["diagnostics"] || !touched["context_management"] {
		t.Fatalf("final tracking lost: %v", touched)
	}
}

func TestFinalizePayloadDocCrossMethodPipeline(t *testing.T) {
	original := []byte(`{"model":"gpt-4o","temperature":0.7}`)
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Default: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "gpt-4o"}},
				Params: map[string]any{"temperature": 0.2, "top_p": 0.9},
			}},
			Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "gpt-4o"}},
				Params: map[string]any{"max_tokens": 2048},
			}},
			Filter: []config.PayloadFilterRule{{
				Models: []config.PayloadModelRule{{Name: "gpt-4o"}},
				Params: []string{"extra"},
			}},
		},
	}
	req := cliproxyexecutor.Request{Model: "gpt-4o", Payload: original}
	opts := cliproxyexecutor.Options{}
	ctx := WithPayloadFinalizer(context.Background(), NewPayloadFinalizer(cfg, "openai", "gpt-4o", "openai", "", original, req, opts))

	doc := ojson.NewDoc([]byte(`{"model":"gpt-4o","temperature":0.7,"extra":true}`))
	SetStringIfDifferentDoc(doc, "model", "gpt-4o")
	SetBoolIfDifferentDoc(doc, "stream", true)
	FinalizePayloadDoc(ctx, doc)
	out := doc.Bytes()

	if got := ojson.GetBytesNoCopy(out, "temperature").Float(); got != 0.7 {
		t.Fatalf("temperature = %v, want 0.7 (preserved from original)", got)
	}
	if got := ojson.GetBytesNoCopy(out, "top_p").Float(); got != 0.9 {
		t.Fatalf("top_p = %v, want 0.9", got)
	}
	if got := ojson.GetBytesNoCopy(out, "max_tokens").Int(); got != 2048 {
		t.Fatalf("max_tokens = %d, want 2048", got)
	}
	if ojson.GetBytesNoCopy(out, "extra").Exists() {
		t.Fatalf("expected extra to be filtered out: %s", string(out))
	}
}

func TestExecutorHelpersDocCrossMethodPipelines(t *testing.T) {
	t.Run("claude doc pipeline", func(t *testing.T) {
		raw := []byte(`{"model":"claude-sonnet-5-5","tool_choice":{"type":"tool","name":"calc"},"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.280; cc_prev_req=old;"},{"type":"text","text":"secret word here"}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"bad id!","name":"calc","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"bad id!","content":"secret word here"}]}]}`)
		doc := ojson.NewDoc(raw)
		matcher := BuildSensitiveWordMatcher([]string{"secret"})
		if !NormalizeClaudeToolChoiceDoc("claude", "claude-sonnet-5-5", doc) {
			t.Fatal("expected NormalizeClaudeToolChoiceDoc to mutate doc")
		}
		if !NormalizeClaudeToolCallIDsDoc(doc) {
			t.Fatal("expected NormalizeClaudeToolCallIDsDoc to mutate doc")
		}
		if !ObfuscateSensitiveWordsDoc(doc, matcher) {
			t.Fatal("expected ObfuscateSensitiveWordsDoc to mutate doc")
		}
		if !StripClaudeBillingTagsDoc(doc) {
			t.Fatal("expected StripClaudeBillingTagsDoc to mutate doc")
		}
		if !InjectClaudeBillingTagsDoc(doc, "prev1", "prompt1") {
			t.Fatal("expected InjectClaudeBillingTagsDoc to mutate doc")
		}
		out := doc.Bytes()
		if got := ojson.GetBytesNoCopy(out, "tool_choice.type").String(); got != "auto" {
			t.Fatalf("tool_choice.type = %q, want auto", got)
		}
		if ojson.GetBytesNoCopy(out, "tool_choice.name").Exists() {
			t.Fatalf("expected tool_choice.name to be removed: %s", out)
		}
		useID := ojson.GetBytesNoCopy(out, "messages.0.content.0.id").String()
		resID := ojson.GetBytesNoCopy(out, "messages.1.content.0.tool_use_id").String()
		if useID == "bad id!" || useID != resID {
			t.Fatalf("tool call IDs not normalized consistently: %q vs %q", useID, resID)
		}
	})

	t.Run("openai compat doc pipeline", func(t *testing.T) {
		raw := []byte(`{"model":"gpt-4o","max_tokens":512,"messages":[{"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"ok"},{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`)
		doc := ojson.NewDoc(raw)
		if !NormalizeOpenAIMaxTokensDoc(doc, true) {
			t.Fatal("expected NormalizeOpenAIMaxTokensDoc to mutate doc")
		}
		if !NormalizeOpenAIToolResultsTextOnlyDoc(doc) {
			t.Fatal("expected NormalizeOpenAIToolResultsTextOnlyDoc to mutate doc")
		}
		out := doc.Bytes()
		if ojson.GetBytesNoCopy(out, "max_tokens").Exists() || ojson.GetBytesNoCopy(out, "max_completion_tokens").Int() != 512 {
			t.Fatalf("unexpected max_completion_tokens normalization: %s", out)
		}
		if ojson.GetBytesNoCopy(out, "messages.0.content").Type != ojson.String {
			t.Fatalf("expected tool content to be flattened to string: %s", out)
		}
	})

	t.Run("codex and responses doc pipeline", func(t *testing.T) {
		raw := []byte(`{"model":"gpt-5","tools":[{"type":"tool_search","execution":"client"},{"type":"web_search","search_content_types":["text"]},{"type":"function","name":"fn","parameters":{"type":"object"}}],"input":[{"type":"message","id":"bad_id","role":"user","content":"hi"},{"type":"tool_search_call","id":"ts_1","call_id":"c1","execution":"client"}]}`)
		doc := ojson.NewDoc(raw)
		if !StripResponsesToolSearchDoc(doc) {
			t.Fatal("expected StripResponsesToolSearchDoc to mutate doc")
		}
		if !SanitizeCodexInputItemIDsDoc(doc) {
			t.Fatal("expected SanitizeCodexInputItemIDsDoc to mutate doc")
		}
		if !SanitizeMetaWebSearchToolsDoc(doc) {
			t.Fatal("expected SanitizeMetaWebSearchToolsDoc to mutate doc")
		}
		if !EnsureMetaMuseToolNamespaceDoc(doc) {
			t.Fatal("expected EnsureMetaMuseToolNamespaceDoc to mutate doc")
		}
		out := doc.Bytes()
		if got := ojson.GetBytesNoCopy(out, "input.0.id").String(); got != "msg_bad_id" {
			t.Fatalf("input.0.id = %q, want msg_bad_id", got)
		}
		if len(ojson.GetBytesNoCopy(out, "input").Array()) != 1 {
			t.Fatalf("expected tool_search_call to be stripped: %s", out)
		}
		if ojson.GetBytesNoCopy(out, "tools.0.search_content_types").Exists() {
			t.Fatalf("expected search_content_types to be stripped: %s", out)
		}
	})

	t.Run("gemini and vertex doc pipeline", func(t *testing.T) {
		raw := []byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"fn","args":{}}},{"thought":true,"text":"thinking"}]},{"role":"model","parts":[{"thought":true,"text":"trailing thought"}]}]}`)
		doc := ojson.NewDoc(raw)
		if !DropGeminiTrailingThoughtOnlyContentDoc(doc, "contents") {
			t.Fatal("expected DropGeminiTrailingThoughtOnlyContentDoc to mutate doc")
		}
		if !StripVertexOpenAIResponsesToolCallIDsDoc(doc, "openai-response") {
			t.Fatal("expected StripVertexOpenAIResponsesToolCallIDsDoc to mutate doc")
		}
		if !EnsureGeminiBoundaryUserContentDoc(doc, "contents") {
			t.Fatal("expected EnsureGeminiBoundaryUserContentDoc to mutate doc")
		}
		out := doc.Bytes()
		contents := ojson.GetBytesNoCopy(out, "contents").Array()
		if len(contents) != 3 {
			t.Fatalf("len(contents) = %d, want 3: %s", len(contents), out)
		}
		if contents[0].Get("role").String() != "user" || contents[2].Get("role").String() != "user" {
			t.Fatalf("expected leading and trailing user turns: %s", out)
		}
		if contents[1].Get("parts.0.functionCall.id").Exists() {
			t.Fatalf("expected functionCall.id to be stripped: %s", out)
		}
	})
}

func TestFinalizeDocCustomWrapperCalledOnceAndPreservesOriginalFromBody(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Default: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "devin-model"}},
				Params: map[string]any{
					"completion_config.max_tokens": 9999,
					"system_prompt":                "default-system",
				},
			}},
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "devin-model",
		Payload: []byte(`{"max_tokens":2048}`),
	}
	opts := cliproxyexecutor.Options{}

	callCount := 0
	wrapper := PayloadFinalizer(func(body []byte) []byte {
		callCount++
		original := body
		if !ojson.GetBytesNoCopy(req.Payload, "max_tokens").Exists() {
			original, _ = ojson.DeleteBytes(original, "completion_config.max_tokens")
		}
		return NewPayloadFinalizer(cfg, "devin", "devin-model", "openai", "", original, req, opts)(body)
	})

	doc := ojson.NewDoc([]byte(`{"completion_config":{"max_tokens":2048}}`))
	wrapper.FinalizeDoc(doc)
	if callCount != 1 {
		t.Fatalf("custom PayloadFinalizer callCount = %d, want 1", callCount)
	}
	out := doc.Bytes()
	if got := ojson.GetBytesNoCopy(out, "completion_config.max_tokens").Int(); got != 2048 {
		t.Fatalf("completion_config.max_tokens = %d, want 2048 (preserved from body-derived original): %s", got, out)
	}
	if got := ojson.GetBytesNoCopy(out, "system_prompt").String(); got != "default-system" {
		t.Fatalf("system_prompt = %q, want default-system: %s", got, out)
	}
}

func TestApplyPayloadConfigDocSequentialDefaultObjectAndNestedField(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Default: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "gpt-5"}},
					Params: map[string]any{"metadata": map[string]any{"x": 1}},
				},
				{
					Models: []config.PayloadModelRule{{Name: "gpt-5"}},
					Params: map[string]any{"metadata.x": 2, "metadata.y": 3},
				},
			},
		},
	}
	raw := []byte(`{"model":"gpt-5"}`)
	want := ApplyPayloadConfigWithRoot(cfg, "gpt-5", "openai", "", raw, nil, "", "")

	doc := ojson.NewDoc(raw)
	ApplyPayloadConfigWithRootDoc(cfg, "gpt-5", "openai", "", doc, nil, "", "")
	got := doc.Bytes()

	if ojson.GetBytesNoCopy(got, "metadata.x").Int() != ojson.GetBytesNoCopy(want, "metadata.x").Int() {
		t.Fatalf("doc metadata.x = %d, want %d (got=%s want=%s)",
			ojson.GetBytesNoCopy(got, "metadata.x").Int(),
			ojson.GetBytesNoCopy(want, "metadata.x").Int(),
			got, want)
	}
	if ojson.GetBytesNoCopy(got, "metadata.y").Int() != 3 {
		t.Fatalf("doc metadata.y = %d, want 3: %s", ojson.GetBytesNoCopy(got, "metadata.y").Int(), got)
	}

	arrCfg := &config.Config{
		Payload: config.PayloadConfig{
			Default: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "gpt-5"}},
					Params: map[string]any{"items.2": 2},
				},
				{
					Models: []config.PayloadModelRule{{Name: "gpt-5"}},
					Params: map[string]any{"items.0": 1},
				},
			},
		},
	}
	arrRaw := []byte(`{"model":"gpt-5","items":[]}`)
	arrWant := ApplyPayloadConfigWithRoot(arrCfg, "gpt-5", "openai", "", arrRaw, nil, "", "")
	arrDoc := ojson.NewDoc(arrRaw)
	ApplyPayloadConfigWithRootDoc(arrCfg, "gpt-5", "openai", "", arrDoc, nil, "", "")
	arrGot := arrDoc.Bytes()
	if ojson.GetBytesNoCopy(arrGot, "items.0").Int() != 1 || ojson.GetBytesNoCopy(arrGot, "items.2").Int() != 2 || !bytes.Equal(arrGot, arrWant) {
		t.Fatalf("sparse array default mismatch:\ngot  %s\nwant %s", arrGot, arrWant)
	}

	hugeArrCfg := &config.Config{
		Payload: config.PayloadConfig{
			Default: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "gpt-5"}},
					Params: map[string]any{"items.1000000000": 1},
				},
			},
		},
	}
	hugeArrRaw := []byte(`{"model":"gpt-5","items":[]}`)
	hugeArrDoc := ojson.NewDoc(hugeArrRaw)
	ApplyPayloadConfigWithRootDoc(hugeArrCfg, "gpt-5", "openai", "", hugeArrDoc, nil, "", "")
	if got := string(hugeArrDoc.Bytes()); got != string(hugeArrRaw) {
		t.Fatalf("huge index array default should be ignored without OOM: got %s, want %s", got, hugeArrRaw)
	}
}

func TestFinalizeDocConcurrentWithNewPayloadFinalizer(t *testing.T) {
	builtinFinalizerCodePtr.Store(0)
	cfg := &config.Config{}
	req := cliproxyexecutor.Request{Model: "gpt-4o", Payload: []byte(`{"model":"gpt-4o"}`)}
	opts := cliproxyexecutor.Options{}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			custom := PayloadFinalizer(func(body []byte) []byte { return body })
			doc := ojson.NewDoc([]byte(`{"model":"gpt-4o"}`))
			custom.FinalizeDoc(doc)
		}()
		go func() {
			defer wg.Done()
			fn := NewPayloadFinalizer(cfg, "openai", "gpt-4o", "openai", "", req.Payload, req, opts)
			doc := ojson.NewDoc([]byte(`{"model":"gpt-4o"}`))
			fn.FinalizeDoc(doc)
		}()
	}
	wg.Wait()
}
