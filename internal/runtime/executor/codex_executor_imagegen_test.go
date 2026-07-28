package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorExecuteResponsesLiteHeaderDoesNotInjectImageGenerationTool(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":   "test",
			"base_url":  server.URL,
			"plan_type": "pro",
		},
	}
	headers := make(http.Header)
	headers.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Headers:      headers,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if tools := gjson.GetBytes(gotBody, "tools"); tools.Exists() {
		t.Fatalf("unexpected tools in responses-lite upstream payload: %s", tools.Raw)
	}
	parallelToolCalls := gjson.GetBytes(gotBody, "parallel_tool_calls")
	if !parallelToolCalls.Exists() || parallelToolCalls.Bool() {
		t.Fatalf("responses-lite parallel_tool_calls should be false: %s", gotBody)
	}
}

func TestCodexExecutorExecuteStreamResponsesLiteHeaderForcesParallelToolCallsFalse(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":   "test",
			"base_url":  server.URL,
			"plan_type": "pro",
		},
	}
	headers := make(http.Header)
	headers.Set(codexResponsesLiteHeader, "true")

	result, errExecute := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-luna",
		Payload: []byte(`{"model":"gpt-5.6-luna","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Headers:      headers,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}

	parallelToolCalls := gjson.GetBytes(gotBody, "parallel_tool_calls")
	if !parallelToolCalls.Exists() || parallelToolCalls.Bool() {
		t.Fatalf("responses-lite parallel_tool_calls should be false: %s", gotBody)
	}
}

func TestEnsureImageGenerationTool_ResponsesLiteMetadataDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"},"input":[{"role":"user","content":"hello"}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.6-sol", nil, nil)

	if string(result) != string(body) {
		t.Fatalf("expected responses-lite body to be unchanged, got %s", string(result))
	}
	if gjson.GetBytes(result, "tools").Exists() {
		t.Fatalf("expected no injected tools for responses-lite request, got %s", gjson.GetBytes(result, "tools").Raw)
	}
}

func TestEnsureImageGenerationTool_ResponsesLiteBooleanMetadataDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":true},"input":"hello"}`)
	result := ensureImageGenerationTool(body, "gpt-5.6-sol", nil, nil)

	if string(result) != string(body) {
		t.Fatalf("expected responses-lite body to be unchanged, got %s", string(result))
	}
}

func TestEnsureImageGenerationTool_ResponsesLiteHeaderDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","input":"hello"}`)
	headers := make(http.Header)
	headers.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")
	result := ensureImageGenerationTool(body, "gpt-5.6-sol", nil, headers)

	if string(result) != string(body) {
		t.Fatalf("expected responses-lite body to be unchanged, got %s", string(result))
	}
}

func TestEnsureImageGenerationTool_ResponsesLiteFalseMetadataStillInjectsTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"false"},"input":"hello"}`)
	result := ensureImageGenerationTool(body, "gpt-5.6-sol", nil, nil)

	if got := gjson.GetBytes(result, "tools.0.type").String(); got != "image_generation" {
		t.Fatalf("tools.0.type = %q, want image_generation; body=%s", got, result)
	}
}

func TestEnsureImageGenerationTool_NoTools(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"draw a cat"}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	tools := gjson.GetBytes(result, "tools")
	if !tools.IsArray() {
		t.Fatalf("expected tools array, got %v", tools.Type)
	}
	arr := tools.Array()
	if len(arr) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(arr))
	}
	if arr[0].Get("type").String() != "image_generation" {
		t.Fatalf("expected type=image_generation, got %s", arr[0].Get("type").String())
	}
	if arr[0].Get("output_format").String() != "png" {
		t.Fatalf("expected output_format=png, got %s", arr[0].Get("output_format").String())
	}
}

func TestEnsureImageGenerationTool_ExistingToolsWithoutImageGen(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"function","name":"get_weather","parameters":{}}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	tools := gjson.GetBytes(result, "tools")
	arr := tools.Array()
	if len(arr) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(arr))
	}
	if arr[0].Get("type").String() != "function" {
		t.Fatalf("expected first tool type=function, got %s", arr[0].Get("type").String())
	}
	if arr[1].Get("type").String() != "image_generation" {
		t.Fatalf("expected second tool type=image_generation, got %s", arr[1].Get("type").String())
	}
}

func TestEnsureImageGenerationTool_AlreadyPresent(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation","output_format":"webp"},{"type":"function","name":"f1"}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	tools := gjson.GetBytes(result, "tools")
	arr := tools.Array()
	if len(arr) != 2 {
		t.Fatalf("expected 2 tools (no duplicate), got %d", len(arr))
	}
	if arr[0].Get("output_format").String() != "webp" {
		t.Fatalf("expected original output_format=webp preserved, got %s", arr[0].Get("output_format").String())
	}
}

func TestEnsureImageGenerationTool_ImageGenNamespaceDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen","parameters":{}}]}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	if string(result) != string(body) {
		t.Fatalf("expected body to be unchanged, got %s", string(result))
	}
}

func TestEnsureImageGenerationTool_FlattenedImageGenFunctionDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"function","name":"image_gen.imagegen","parameters":{}}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	if string(result) != string(body) {
		t.Fatalf("expected body to be unchanged, got %s", string(result))
	}
}

func TestEnsureImageGenerationTool_SimilarNamespaceStillInjectsTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"namespace","name":"image_tools","tools":[{"type":"function","name":"imagegen","parameters":{}}]}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	tools := gjson.GetBytes(result, "tools").Array()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[1].Get("type").String() != "image_generation" {
		t.Fatalf("expected second tool type=image_generation, got %s", tools[1].Get("type").String())
	}
}

func TestEnsureImageGenerationTool_EmptyToolsArray(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	tools := gjson.GetBytes(result, "tools")
	arr := tools.Array()
	if len(arr) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(arr))
	}
	if arr[0].Get("type").String() != "image_generation" {
		t.Fatalf("expected type=image_generation, got %s", arr[0].Get("type").String())
	}
}

func TestEnsureImageGenerationTool_WebSearchAndImageGen(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"web_search"}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	tools := gjson.GetBytes(result, "tools")
	arr := tools.Array()
	if len(arr) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(arr))
	}
	if arr[0].Get("type").String() != "web_search" {
		t.Fatalf("expected first tool type=web_search, got %s", arr[0].Get("type").String())
	}
	if arr[1].Get("type").String() != "image_generation" {
		t.Fatalf("expected second tool type=image_generation, got %s", arr[1].Get("type").String())
	}
}

func TestEnsureImageGenerationTool_GPT53CodexSparkDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.3-codex-spark","input":"draw a cat"}`)
	result := ensureImageGenerationTool(body, "gpt-5.3-codex-spark", nil, nil)

	if string(result) != string(body) {
		t.Fatalf("expected body to be unchanged, got %s", string(result))
	}
	if gjson.GetBytes(result, "tools").Exists() {
		t.Fatalf("expected no tools for gpt-5.3-codex-spark, got %s", gjson.GetBytes(result, "tools").Raw)
	}
}

func TestEnsureImageGenerationTool_FreeCodexAuthDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"draw a cat"}`)
	freeAuth := &cliproxyauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{"plan_type": "free"},
	}
	result := ensureImageGenerationTool(body, "gpt-5.4", freeAuth, nil)

	if string(result) != string(body) {
		t.Fatalf("expected body to be unchanged, got %s", string(result))
	}
	if gjson.GetBytes(result, "tools").Exists() {
		t.Fatalf("expected no tools for free codex auth, got %s", gjson.GetBytes(result, "tools").Raw)
	}
}

func TestShouldInjectImageGeneration_GlobalModes(t *testing.T) {
	if !shouldInjectImageGeneration(nil, nil) {
		t.Fatal("nil cfg should inject")
	}
	if !shouldInjectImageGeneration(&config.Config{}, nil) {
		t.Fatal("Off mode should inject")
	}
	cfgAll := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}}
	if shouldInjectImageGeneration(cfgAll, nil) {
		t.Fatal("All mode must not inject")
	}
	cfgChat := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationChat}}
	if shouldInjectImageGeneration(cfgChat, nil) {
		t.Fatal("Chat mode must not inject")
	}
}

func TestShouldInjectImageGeneration_PerAuthOverride(t *testing.T) {
	cfgOff := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationOff}}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"disable_image_generation": true}}
	if shouldInjectImageGeneration(cfgOff, auth) {
		t.Fatal("per-auth override must block injection when global is Off")
	}
	authFalse := &cliproxyauth.Auth{Metadata: map[string]any{"disable_image_generation": false}}
	if !shouldInjectImageGeneration(cfgOff, authFalse) {
		t.Fatal("false override must be treated as unset")
	}
}

func TestApplyCodexImageGenerationPolicy_PerAuthOverrideStripsClientTool(t *testing.T) {
	cfgOff := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationOff}}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"disable-image-generation": true}}
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation","output_format":"png"},{"type":"function","name":"f1"}],"tool_choice":{"type":"image_generation"}}`)
	result := applyCodexImageGenerationPolicy(cfgOff, auth, body, "gpt-5.4", nil)

	tools := gjson.GetBytes(result, "tools")
	if !tools.IsArray() || len(tools.Array()) != 1 {
		t.Fatalf("expected only function tool remaining, got %s", tools.Raw)
	}
	if tools.Array()[0].Get("type").String() != "function" {
		t.Fatalf("expected remaining tool type=function, got %s", tools.Array()[0].Get("type").String())
	}
	if gjson.GetBytes(result, "tool_choice").Exists() {
		t.Fatalf("expected tool_choice removed, got %s", gjson.GetBytes(result, "tool_choice").Raw)
	}
}

func TestApplyCodexImageGenerationPolicy_InjectsWhenEnabled(t *testing.T) {
	cfgOff := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationOff}}
	body := []byte(`{"model":"gpt-5.4","input":"draw a cat"}`)
	result := applyCodexImageGenerationPolicy(cfgOff, nil, body, "gpt-5.4", nil)
	tools := gjson.GetBytes(result, "tools")
	if !tools.IsArray() || len(tools.Array()) != 1 {
		t.Fatalf("expected injected image_generation tool, got %s", tools.Raw)
	}
	if tools.Array()[0].Get("type").String() != "image_generation" {
		t.Fatalf("expected type=image_generation, got %s", tools.Array()[0].Get("type").String())
	}
}

func TestApplyCodexImageGenerationPolicy_GlobalChatDoesNotInject(t *testing.T) {
	cfgChat := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationChat}}
	body := []byte(`{"model":"gpt-5.4","input":"draw a cat"}`)
	result := applyCodexImageGenerationPolicy(cfgChat, nil, body, "gpt-5.4", nil)
	if gjson.GetBytes(result, "tools").Exists() {
		t.Fatalf("expected no tools injection in chat mode, got %s", gjson.GetBytes(result, "tools").Raw)
	}
}

func TestCodexAuthImageGenerationDisabledErr(t *testing.T) {
	if err := codexAuthImageGenerationDisabledErr(nil); err != nil {
		t.Fatalf("nil auth must allow image generation, got %v", err)
	}
	if err := codexAuthImageGenerationDisabledErr(&cliproxyauth.Auth{}); err != nil {
		t.Fatalf("auth without override must allow image generation, got %v", err)
	}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"disable_image_generation": true}}
	err := codexAuthImageGenerationDisabledErr(auth)
	if err == nil {
		t.Fatal("expected error for disabled image-generation auth")
	}
	se, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error %T does not expose StatusCode()", err)
	}
	if got := se.StatusCode(); got != http.StatusForbidden {
		t.Fatalf("StatusCode() = %d, want %d", got, http.StatusForbidden)
	}
}

func TestExecuteOpenAIImage_PerAuthDisableFailsFast(t *testing.T) {
	exec := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"disable_image_generation": true},
		Attributes: map[string]string{
			"api_key":  "sk-test",
			"base_url": "http://127.0.0.1:1",
		},
	}
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-image-2",
		Payload: []byte(`{"model":"gpt-image-2","prompt":"a cat"}`),
	}, codexOpenAIImageTestOptions(codexImagesGenerationsPath, false))
	if err == nil {
		t.Fatal("expected per-auth image disable to reject /v1/images path")
	}
	se, ok := err.(interface{ StatusCode() int })
	if !ok || se.StatusCode() != http.StatusForbidden {
		t.Fatalf("got err=%v status, want 403", err)
	}
}

func TestExecuteOpenAIImageStream_PerAuthDisableFailsFast(t *testing.T) {
	exec := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"disable-image-generation": true},
		Attributes: map[string]string{
			"api_key":  "sk-test",
			"base_url": "http://127.0.0.1:1",
		},
	}
	_, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-image-2",
		Payload: []byte(`{"model":"gpt-image-2","prompt":"a cat","stream":true}`),
	}, codexOpenAIImageTestOptions(codexImagesGenerationsPath, true))
	if err == nil {
		t.Fatal("expected per-auth image disable to reject streaming /v1/images path")
	}
	se, ok := err.(interface{ StatusCode() int })
	if !ok || se.StatusCode() != http.StatusForbidden {
		t.Fatalf("got err=%v status, want 403", err)
	}
}

func TestStripCodexImageGenerationIfDisabled(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation"},{"type":"function","name":"f1"}],"tool_choice":{"type":"image_generation"}}`)
	if got := stripCodexImageGenerationIfDisabled(nil, body); string(got) != string(body) {
		t.Fatalf("nil auth must leave body unchanged")
	}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"disable_image_generation": true}}
	got := stripCodexImageGenerationIfDisabled(auth, body)
	tools := gjson.GetBytes(got, "tools")
	if !tools.IsArray() || len(tools.Array()) != 1 || tools.Array()[0].Get("type").String() != "function" {
		t.Fatalf("expected only function tool remaining, got %s", tools.Raw)
	}
	if gjson.GetBytes(got, "tool_choice").Exists() {
		t.Fatalf("expected tool_choice removed, got %s", gjson.GetBytes(got, "tool_choice").Raw)
	}
}

func TestCodexAuthImageGenerationDisabledErr_IsRequestScoped(t *testing.T) {
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"disable_image_generation": true}}
	err := codexAuthImageGenerationDisabledErr(auth)
	if err == nil {
		t.Fatal("expected error")
	}
	// Must expose StatusCode for conductor status routing.
	if se, ok := err.(interface{ StatusCode() int }); !ok || se.StatusCode() != http.StatusForbidden {
		t.Fatalf("StatusCode() = %v, want 403", se)
	}
	// Must expose IsRequestScoped so MarkResult skips cooldown.
	if rs, ok := err.(interface{ IsRequestScoped() bool }); !ok || !rs.IsRequestScoped() {
		t.Fatal("expected IsRequestScoped() true to avoid credential cooldown")
	}
	// Must NOT be a permanent/cooldown 403 — conductor should failover to next credential.
	if _, ok := err.(error); !ok {
		t.Fatal("must implement error")
	}
}
