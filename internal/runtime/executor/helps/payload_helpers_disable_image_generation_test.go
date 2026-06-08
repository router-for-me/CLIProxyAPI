package helps

import (
	"net/http"
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
)

func TestApplyPayloadConfigWithRoot_DisableImageGeneration_RemovesToolsEntry(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
	}
	payload := []byte(`{"tools":[{"type":"image_generation","output_format":"png"},{"type":"function","name":"f1"}]}`)

	out := ApplyPayloadConfigWithRoot(cfg, "gpt-5.4", "openai-response", "", payload, nil, "", "")

	tools := gjson.GetBytes(out, "tools")
	if !tools.Exists() || !tools.IsArray() {
		t.Fatalf("expected tools array, got %v", tools.Type)
	}
	arr := tools.Array()
	if len(arr) != 1 {
		t.Fatalf("expected 1 tool after removal, got %d", len(arr))
	}
	if got := arr[0].Get("type").String(); got != "function" {
		t.Fatalf("expected remaining tool type=function, got %q", got)
	}
}

func TestApplyPayloadConfigWithRoot_DisableImageGeneration_RemovesToolsEntryWithRoot(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
	}
	payload := []byte(`{"request":{"tools":[{"type":"image_generation"},{"type":"web_search"}]}}`)

	out := ApplyPayloadConfigWithRoot(cfg, "gpt-5.4", "gemini-cli", "request", payload, nil, "", "")

	tools := gjson.GetBytes(out, "request.tools")
	if !tools.Exists() || !tools.IsArray() {
		t.Fatalf("expected request.tools array, got %v", tools.Type)
	}
	arr := tools.Array()
	if len(arr) != 1 {
		t.Fatalf("expected 1 tool after removal, got %d", len(arr))
	}
	if got := arr[0].Get("type").String(); got != "web_search" {
		t.Fatalf("expected remaining tool type=web_search, got %q", got)
	}
}

func TestApplyPayloadConfigWithRoot_DisableImageGeneration_RemovesToolChoiceByType(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
	}
	payload := []byte(`{"tools":[{"type":"image_generation"},{"type":"function","name":"f1"}],"tool_choice":{"type":"image_generation"}}`)

	out := ApplyPayloadConfigWithRoot(cfg, "gpt-5.4", "openai-response", "", payload, nil, "", "")

	if gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatalf("expected tool_choice to be removed")
	}
}

func TestApplyPayloadConfigWithRoot_DisableImageGeneration_RemovesToolChoiceByNameWithRoot(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
	}
	payload := []byte(`{"request":{"tools":[{"type":"image_generation"},{"type":"web_search"}],"tool_choice":{"type":"tool","name":"image_generation"}}}`)

	out := ApplyPayloadConfigWithRoot(cfg, "gpt-5.4", "gemini-cli", "request", payload, nil, "", "")

	if gjson.GetBytes(out, "request.tool_choice").Exists() {
		t.Fatalf("expected request.tool_choice to be removed")
	}
}

func TestApplyPayloadConfigWithRoot_DisableImageGenerationChat_KeepsImageGenerationOnImagesEndpoints(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationChat},
	}
	payload := []byte(`{"tools":[{"type":"image_generation"},{"type":"function","name":"f1"}],"tool_choice":{"type":"image_generation"}}`)

	out := ApplyPayloadConfigWithRoot(cfg, "gpt-5.4", "openai-response", "", payload, nil, "", "/v1/images/generations")

	tools := gjson.GetBytes(out, "tools")
	if !tools.Exists() || !tools.IsArray() {
		t.Fatalf("expected tools array, got %v", tools.Type)
	}
	arr := tools.Array()
	if len(arr) != 2 {
		t.Fatalf("expected 2 tools (no removal), got %d", len(arr))
	}
	if !gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatalf("expected tool_choice to be kept on images endpoint")
	}
}

func TestApplyPayloadConfigWithRoot_DisableImageGeneration_PayloadOverrideCanRestoreImageGeneration(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
		Payload: config.PayloadConfig{
			OverrideRaw: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gpt-5.4", Protocol: "openai-response"},
					},
					Params: map[string]any{
						"tools":       `[{"type":"image_generation"},{"type":"function","name":"f1"}]`,
						"tool_choice": `{"type":"image_generation"}`,
					},
				},
			},
		},
	}
	payload := []byte(`{"tools":[{"type":"image_generation"},{"type":"function","name":"f1"}],"tool_choice":{"type":"image_generation"}}`)

	out := ApplyPayloadConfigWithRoot(cfg, "gpt-5.4", "openai-response", "", payload, nil, "", "")

	tools := gjson.GetBytes(out, "tools")
	if !tools.Exists() || !tools.IsArray() {
		t.Fatalf("expected tools array, got %v", tools.Type)
	}
	arr := tools.Array()
	if len(arr) != 2 {
		t.Fatalf("expected 2 tools after payload override, got %d", len(arr))
	}
	if got := arr[0].Get("type").String(); got != "image_generation" {
		t.Fatalf("expected first tool type=image_generation, got %q", got)
	}
	if !gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatalf("expected tool_choice to be restored by payload override")
	}
}

func TestApplyPayloadConfigWithRequest_HeaderGateRequiresWildcardMatch(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{
							Name:     "gpt-*",
							Protocol: "openai",
							Headers: map[string]string{
								"X-Client-Tier": "tenant-*-region-*",
							},
						},
					},
					Params: map[string]any{
						"metadata.enabled": true,
					},
				},
			},
		},
	}
	payload := []byte(`{"model":"gpt-5.4"}`)
	headers := http.Header{}
	headers.Set("X-Client-Tier", "tenant-alpha-region-us")

	out := ApplyPayloadConfigWithRequest(cfg, "gpt-5.4", "openai", "responses", "", payload, nil, "", "", headers)
	if !gjson.GetBytes(out, "metadata.enabled").Bool() {
		t.Fatalf("expected header-matched payload rule to apply, payload=%s", string(out))
	}

	headers.Set("X-Client-Tier", "tenant-alpha")
	out = ApplyPayloadConfigWithRequest(cfg, "gpt-5.4", "openai", "responses", "", payload, nil, "", "", headers)
	if gjson.GetBytes(out, "metadata.enabled").Exists() {
		t.Fatalf("expected header-mismatched payload rule to be skipped, payload=%s", string(out))
	}
}

func TestApplyPayloadConfigWithRequest_FromProtocolGateUsesSourceProtocol(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gpt-*", Protocol: "openai", FromProtocol: "responses"},
					},
					Params: map[string]any{
						"metadata.source": "responses",
					},
				},
				{
					Models: []config.PayloadModelRule{
						{Name: "gpt-*", Protocol: "openai", FromProtocol: "openai"},
					},
					Params: map[string]any{
						"metadata.source": "openai",
					},
				},
			},
		},
	}
	payload := []byte(`{"model":"gpt-5.4"}`)

	out := ApplyPayloadConfigWithRequest(cfg, "gpt-5.4", "openai", "openai-response", "", payload, nil, "", "", nil)
	if got := gjson.GetBytes(out, "metadata.source").String(); got != "responses" {
		t.Fatalf("metadata.source = %q, want responses; payload=%s", got, string(out))
	}

	out = ApplyPayloadConfigWithRequest(cfg, "gpt-5.4", "openai", "openai", "", payload, nil, "", "", nil)
	if got := gjson.GetBytes(out, "metadata.source").String(); got != "openai" {
		t.Fatalf("metadata.source = %q, want openai; payload=%s", got, string(out))
	}
}

func TestApplyPayloadConfigWithRequest_PayloadConditionsNarrowRule(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{
							Name: "gpt-*",
							Match: []map[string]any{
								{"metadata.client": "codex"},
								{"tools.#(type==\"web_search\").enabled": true},
							},
							NotMatch: []map[string]any{
								{"metadata.mode": "dev"},
							},
							Exist: []string{
								"tools.#(type==\"web_search\").type",
							},
							NotExist: []string{
								"metadata.missing",
								"metadata.null_value",
							},
						},
					},
					Params: map[string]any{
						"metadata.applied": true,
					},
				},
			},
		},
	}
	payload := []byte(`{"model":"gpt-5.4","metadata":{"client":"codex","mode":"prod","null_value":null},"tools":[{"type":"function"},{"type":"web_search","enabled":true}]}`)

	out := ApplyPayloadConfigWithRequest(cfg, "gpt-5.4", "openai", "responses", "", payload, nil, "", "", nil)
	if !gjson.GetBytes(out, "metadata.applied").Bool() {
		t.Fatalf("expected payload condition-matched rule to apply, payload=%s", string(out))
	}
}

func TestApplyPayloadConfigWithRequest_PayloadConditionsSkipRule(t *testing.T) {
	testCases := []struct {
		name  string
		model config.PayloadModelRule
	}{
		{
			name: "match mismatch",
			model: config.PayloadModelRule{
				Name:  "gpt-*",
				Match: []map[string]any{{"metadata.client": "codex"}},
			},
		},
		{
			name: "not-match matched",
			model: config.PayloadModelRule{
				Name:     "gpt-*",
				NotMatch: []map[string]any{{"metadata.mode": "dev"}},
			},
		},
		{
			name: "exist missing",
			model: config.PayloadModelRule{
				Name:  "gpt-*",
				Exist: []string{"metadata.missing"},
			},
		},
		{
			name: "exist null",
			model: config.PayloadModelRule{
				Name:  "gpt-*",
				Exist: []string{"metadata.null_value"},
			},
		},
		{
			name: "not-exist present",
			model: config.PayloadModelRule{
				Name:     "gpt-*",
				NotExist: []string{"metadata.client"},
			},
		},
	}
	payload := []byte(`{"model":"gpt-5.4","metadata":{"client":"other","mode":"dev","null_value":null}}`)

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				Payload: config.PayloadConfig{
					Override: []config.PayloadRule{
						{
							Models: []config.PayloadModelRule{tc.model},
							Params: map[string]any{
								"metadata.applied": true,
							},
						},
					},
				},
			}

			out := ApplyPayloadConfigWithRequest(cfg, "gpt-5.4", "openai", "responses", "", payload, nil, "", "", nil)
			if gjson.GetBytes(out, "metadata.applied").Exists() {
				t.Fatalf("expected payload condition-mismatched rule to be skipped, payload=%s", string(out))
			}
		})
	}
}

func TestApplyPayloadConfigWithRequest_JSHandlerHooks(t *testing.T) {
	// 创建临时测试用 JS 脚本目录
	temp_dir := t.TempDir()

	// 脚本 1：正常修改 payload 并通过 return ctx 返回修改结果
	normal_js_path := temp_dir + "/normal.js"
	normal_js_content := `
		function on_before_request(ctx) {
			let req = JSON.parse(ctx.body);
			req.model = "gemini-2.5-pro-modified";
			req.temperature = 0.5;
			ctx.body = JSON.stringify(req);
			return ctx;
		}
	`
	if err := os.WriteFile(normal_js_path, []byte(normal_js_content), 0644); err != nil {
		t.Fatalf("写入测试脚本失败: %v", err)
	}

	// 脚本 2：死循环脚本（用于测试超时熔断）
	loop_js_path := temp_dir + "/loop.js"
	loop_js_content := `
		function on_before_request(ctx) {
			while(true) {}
		}
	`
	if err := os.WriteFile(loop_js_path, []byte(loop_js_content), 0644); err != nil {
		t.Fatalf("写入测试脚本失败: %v", err)
	}

	// 脚本 3：语法错误脚本（用于测试错误捕获与降级）
	syntax_js_path := temp_dir + "/syntax.js"
	syntax_js_content := `
		function on_before_request(ctx) {
			let req = ; // 语法错误
			return ctx;
		}
	`
	if err := os.WriteFile(syntax_js_path, []byte(syntax_js_content), 0644); err != nil {
		t.Fatalf("写入测试脚本失败: %v", err)
	}

	// 1. 验证正常情况下的修改
	cfg_normal := &config.Config{
		Payload: config.PayloadConfig{
			JSHandler: []config.JSHandlerRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gemini-*", Protocol: "gemini"},
					},
					Params: []string{normal_js_path},
				},
			},
		},
	}
	payload := []byte(`{"model":"gemini-2.5-pro","temperature":1.0}`)
	out_normal := ApplyPayloadConfigWithRequest(cfg_normal, "gemini-2.5-pro", "gemini", "responses", "", payload, nil, "", "", nil)

	if got := gjson.GetBytes(out_normal, "model").String(); got != "gemini-2.5-pro-modified" {
		t.Fatalf("预期 model 被 JS 修改为 gemini-2.5-pro-modified, 实际为: %q", got)
	}
	if got := gjson.GetBytes(out_normal, "temperature").Num; got != 0.5 {
		t.Fatalf("预期 temperature 被 JS 修改为 0.5, 实际为: %f", got)
	}

	// 2. 验证 JS 语法错误时能降级处理，保留原 Payload
	cfg_syntax := &config.Config{
		Payload: config.PayloadConfig{
			JSHandler: []config.JSHandlerRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gemini-*", Protocol: "gemini"},
					},
					Params: []string{syntax_js_path},
				},
			},
		},
	}
	out_syntax := ApplyPayloadConfigWithRequest(cfg_syntax, "gemini-2.5-pro", "gemini", "responses", "", payload, nil, "", "", nil)
	if got := gjson.GetBytes(out_syntax, "model").String(); got != "gemini-2.5-pro" {
		t.Fatalf("预期语法错误时降级并保留原始 model = gemini-2.5-pro, 实际为: %q", got)
	}

	// 3. 验证 JS 死循环时触发 1 秒超时中断降级，且保留原 Payload
	cfg_loop := &config.Config{
		Payload: config.PayloadConfig{
			JSHandler: []config.JSHandlerRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gemini-*", Protocol: "gemini"},
					},
					Params: []string{loop_js_path},
				},
			},
		},
	}

	out_loop := ApplyPayloadConfigWithRequest(cfg_loop, "gemini-2.5-pro", "gemini", "responses", "", payload, nil, "", "", nil)
	if got := gjson.GetBytes(out_loop, "model").String(); got != "gemini-2.5-pro" {
		t.Fatalf("预期死循环超时中断后降级并保留原始 model = gemini-2.5-pro, 实际为: %q", got)
	}
}

func TestApplyJSAfterResponse_Hooks(t *testing.T) {
	// 创建临时测试用 JS 脚本目录
	temp_dir := t.TempDir()

	// 脚本 1：正常修改响应载荷（非流式）
	normal_resp_js_path := temp_dir + "/normal_resp.js"
	normal_resp_js_content := `
		function on_after_response(ctx) {
			let resp = JSON.parse(ctx.body);
			resp.modified = true;
			resp.req_body = ctx.req.body; // 测试能否正常获取关联 of 请求上下文
			ctx.body = JSON.stringify(resp);
			ctx.headers["X-Test-Resp-Header"] = "test-value";
			return ctx;
		}
	`
	if err := os.WriteFile(normal_resp_js_path, []byte(normal_resp_js_content), 0644); err != nil {
		t.Fatalf("写入测试脚本失败: %v", err)
	}

	// 脚本 2：流式响应修改分块
	stream_resp_js_path := temp_dir + "/stream_resp.js"
	stream_resp_js_content := `
		function on_after_response(ctx) {
			if (ctx.body !== null) {
				throw new Error("流式响应中 body 应该为 null");
			}
			// 如果是第二次调用，前一个历史分块应该已经被修改为 secured 并且可以通过 history_chunks 索引到
			if (ctx.history_chunks && ctx.history_chunks.length > 0) {
				var prev = ctx.history_chunks[0];
				if (prev.indexOf("secured") !== -1) {
					ctx.headers["X-Stream-History-Check"] = "passed";
				}
			}
			
			if (ctx.chunk.includes("sensitive")) {
				ctx.chunk = ctx.chunk.replace("sensitive", "secured");
			}
			ctx.headers["X-Stream-Resp-Header"] = "stream-value";
			return ctx;
		}
	`
	if err := os.WriteFile(stream_resp_js_path, []byte(stream_resp_js_content), 0644); err != nil {
		t.Fatalf("写入测试脚本失败: %v", err)
	}

	// 1. 验证常规响应（非流式）处理
	cfg_normal := &config.Config{
		Payload: config.PayloadConfig{
			JSHandler: []config.JSHandlerRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gpt-*", Protocol: "openai"},
					},
					Params: []string{normal_resp_js_path},
				},
			},
		},
	}

	req_body := []byte(`{"prompt": "hello"}`)
	resp_body := []byte(`{"choices": [{"text": "hi"}]}`)

	mock_resp_headers := make(http.Header)
	out_normal, _ := ApplyJSAfterResponse(cfg_normal, "test-req-id", "gpt-4", "gpt-4", "openai", nil, req_body, resp_body, mock_resp_headers)

	if got := gjson.GetBytes(out_normal, "modified").Bool(); !got {
		t.Fatalf("预期 modified 属性为 true, 实际未被修改")
	}
	if got := gjson.GetBytes(out_normal, "req_body").String(); got != `{"prompt": "hello"}` {
		t.Fatalf("预期 req_body 匹配为 %q, 实际为 %q", `{"prompt": "hello"}`, got)
	}
	if got := mock_resp_headers.Get("X-Test-Resp-Header"); got != "test-value" {
		t.Fatalf("预期响应头 X-Test-Resp-Header 被修改为 %q, 实际为 %q", "test-value", got)
	}

	// 2. 验证流式分块响应处理
	cfg_stream := &config.Config{
		Payload: config.PayloadConfig{
			JSHandler: []config.JSHandlerRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gpt-*", Protocol: "openai"},
					},
					Params: []string{stream_resp_js_path},
				},
			},
		},
	}

	chunk1 := []byte("hello stream sensitive world")
	chunk2 := []byte("keep it safe")

	mock_stream_headers := make(http.Header)

	// 第一次调用，已累积 body 还是空的
	out_chunk1, _ := ApplyJSAfterResponseStream(cfg_stream, "test-req-id-stream", "gpt-4", "gpt-4", "openai", nil, req_body, nil, chunk1, mock_stream_headers)
	if string(out_chunk1) != "hello stream secured world" {
		t.Fatalf("预期分块 1 的 sensitive 被替换为 secured，实际为: %q", string(out_chunk1))
	}

	// 模拟追加历史记录
	history := []string{string(out_chunk1)}

	// 第二次调用，传入被修改后的历史分块切片
	out_chunk2, _ := ApplyJSAfterResponseStream(cfg_stream, "test-req-id-stream", "gpt-4", "gpt-4", "openai", nil, req_body, history, chunk2, mock_stream_headers)
	if string(out_chunk2) != "keep it safe" {
		t.Fatalf("预期分块 2 无敏感词不修改，实际为: %q", string(out_chunk2))
	}

	if got := mock_stream_headers.Get("X-Stream-History-Check"); got != "passed" {
		t.Fatalf("预期通过了历史分块检测得到 %q, 实际为 %q", "passed", got)
	}

	if got := mock_stream_headers.Get("X-Stream-Resp-Header"); got != "stream-value" {
		t.Fatalf("预期响应头 X-Stream-Resp-Header 被修改为 %q, 实际为 %q", "stream-value", got)
	}
}
