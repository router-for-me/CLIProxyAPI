package helps

import (
	"bytes"
	"strings"
	"testing"

	kimiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kimi"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestResolveKimiResponsesURL(t *testing.T) {
	tests := []struct {
		name string
		auth *cliproxyauth.Auth
		want string
	}{
		{
			name: "nil auth",
			auth: nil,
			want: kimiauth.KimiAPIBaseURL + "/v1/responses",
		},
		{
			name: "empty attributes",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{}},
			want: kimiauth.KimiAPIBaseURL + "/v1/responses",
		},
		{
			name: "base_url without v1",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.com/coding"}},
			want: "https://api.kimi.com/coding/v1/responses",
		},
		{
			name: "base_url with trailing slash",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.com/coding/"}},
			want: "https://api.kimi.com/coding/v1/responses",
		},
		{
			name: "base_url with v1",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.com/coding/v1"}},
			want: "https://api.kimi.com/coding/v1/responses",
		},
		{
			name: "base_url with v1 and trailing slash",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.com/coding/v1/"}},
			want: "https://api.kimi.com/coding/v1/responses",
		},
		{
			name: "kimi.ai auth by provider",
			auth: &cliproxyauth.Auth{Provider: "kimi-ai"},
			want: kimiauth.KimiAIAPIBaseURL + "/v1/responses",
		},
		{
			name: "kimi.ai auth by domain attribute",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"domain": "kimi.ai"}},
			want: kimiauth.KimiAIAPIBaseURL + "/v1/responses",
		},
		{
			name: "kimi.ai base_url in attributes",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.ai/coding"}},
			want: "https://api.kimi.ai/coding/v1/responses",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveKimiResponsesURL(tt.auth)
			if got != tt.want {
				t.Fatalf("ResolveKimiResponsesURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveKimiChatURL(t *testing.T) {
	tests := []struct {
		name string
		auth *cliproxyauth.Auth
		want string
	}{
		{
			name: "nil auth",
			auth: nil,
			want: kimiauth.KimiAPIBaseURL + "/v1/chat/completions",
		},
		{
			name: "base_url without v1",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.ai/coding"}},
			want: "https://api.kimi.ai/coding/v1/chat/completions",
		},
		{
			name: "base_url with trailing v1",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.ai/coding/v1"}},
			want: "https://api.kimi.ai/coding/v1/chat/completions",
		},
		{
			name: "base_url with trailing slash after v1",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.ai/coding/v1/"}},
			want: "https://api.kimi.ai/coding/v1/chat/completions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveKimiChatURL(tt.auth)
			if got != tt.want {
				t.Fatalf("ResolveKimiChatURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveKimiClaudeBaseURL(t *testing.T) {
	tests := []struct {
		name string
		auth *cliproxyauth.Auth
		want string
	}{
		{
			name: "nil auth",
			auth: nil,
			want: kimiauth.KimiAPIBaseURL,
		},
		{
			name: "base_url with v1",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.ai/coding/v1"}},
			want: "https://api.kimi.ai/coding",
		},
		{
			name: "base_url without v1",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.ai/coding"}},
			want: "https://api.kimi.ai/coding",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveKimiClaudeBaseURL(tt.auth)
			if got != tt.want {
				t.Fatalf("ResolveKimiClaudeBaseURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeKimiResponsesInput(t *testing.T) {
	t.Run("interleaved developer message between parallel outputs", func(t *testing.T) {
		inputJSON := []byte(`{
			"model": "kimi-k3",
			"input": [
				{"type":"function_call","call_id":"view_image:31","name":"view_image","arguments":"{}"},
				{"type":"function_call","call_id":"view_image:32","name":"view_image","arguments":"{}"},
				{"type":"function_call_output","call_id":"view_image:31","output":"ok31"},
				{"type":"message","role":"developer","content":[{"type":"input_text","text":"keep following instructions"}]},
				{"type":"function_call_output","call_id":"view_image:32","output":"ok32"}
			]
		}`)

		normalized, errNormalize := NormalizeKimiResponsesInput(inputJSON)
		if errNormalize != nil {
			t.Fatalf("NormalizeKimiResponsesInput error = %v", errNormalize)
		}

		items := gjson.GetBytes(normalized, "input").Array()
		if len(items) != 5 {
			t.Fatalf("expected 5 items, got %d: %s", len(items), string(normalized))
		}

		if got := items[0].Get("call_id").String(); got != "view_image:31" {
			t.Errorf("items[0].call_id = %q, want view_image:31", got)
		}
		if got := items[1].Get("call_id").String(); got != "view_image:32" {
			t.Errorf("items[1].call_id = %q, want view_image:32", got)
		}
		if got := items[2].Get("type").String(); got != "function_call_output" || items[2].Get("call_id").String() != "view_image:31" {
			t.Errorf("items[2] = %s, want function_call_output view_image:31", items[2].Raw)
		}
		if got := items[3].Get("type").String(); got != "function_call_output" || items[3].Get("call_id").String() != "view_image:32" {
			t.Errorf("items[3] = %s, want function_call_output view_image:32", items[3].Raw)
		}
		if got := items[4].Get("type").String(); got != "message" || items[4].Get("role").String() != "developer" {
			t.Errorf("items[4] = %s, want developer message", items[4].Raw)
		}
	})

	t.Run("interleaved non-tool message before all outputs", func(t *testing.T) {
		inputJSON := []byte(`{
			"model": "kimi-k3",
			"input": [
				{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},
				{"type":"function_call","call_id":"call_2","name":"lookup","arguments":"{}"},
				{"type":"message","role":"user","content":[{"type":"input_text","text":"status reminder"}]},
				{"type":"function_call_output","call_id":"call_1","output":"res1"},
				{"type":"function_call_output","call_id":"call_2","output":"res2"}
			]
		}`)

		normalized, errNormalize := NormalizeKimiResponsesInput(inputJSON)
		if errNormalize != nil {
			t.Fatalf("NormalizeKimiResponsesInput error = %v", errNormalize)
		}

		items := gjson.GetBytes(normalized, "input").Array()
		if len(items) != 5 {
			t.Fatalf("expected 5 items, got %d: %s", len(items), string(normalized))
		}

		if items[2].Get("type").String() != "function_call_output" || items[2].Get("call_id").String() != "call_1" {
			t.Errorf("items[2] = %s, want function_call_output call_1", items[2].Raw)
		}
		if items[3].Get("type").String() != "function_call_output" || items[3].Get("call_id").String() != "call_2" {
			t.Errorf("items[3] = %s, want function_call_output call_2", items[3].Raw)
		}
		if items[4].Get("type").String() != "message" || items[4].Get("role").String() != "user" {
			t.Errorf("items[4] = %s, want user message", items[4].Raw)
		}
	})

	t.Run("contiguous outputs remain byte identical", func(t *testing.T) {
		inputJSON := []byte(`{"model":"kimi-k3","input":[{"type":"function_call","call_id":"c1","name":"t"},{"type":"function_call_output","call_id":"c1","output":"ok"},{"type":"message","role":"developer"}]}`)
		normalized, errNormalize := NormalizeKimiResponsesInput(inputJSON)
		if errNormalize != nil {
			t.Fatalf("NormalizeKimiResponsesInput error = %v", errNormalize)
		}
		if !bytes.Equal(normalized, inputJSON) {
			t.Fatalf("expected byte identical return for already contiguous input, got: %s", string(normalized))
		}
	})

	t.Run("missing tool output remains untouched", func(t *testing.T) {
		inputJSON := []byte(`{"model":"kimi-k3","input":[{"type":"function_call","call_id":"c1","name":"t"},{"type":"function_call","call_id":"c2","name":"t"},{"type":"function_call_output","call_id":"c1","output":"ok"},{"type":"message","role":"developer"}]}`)
		normalized, errNormalize := NormalizeKimiResponsesInput(inputJSON)
		if errNormalize != nil {
			t.Fatalf("NormalizeKimiResponsesInput error = %v", errNormalize)
		}
		if !bytes.Equal(normalized, inputJSON) {
			t.Fatalf("expected byte identical return when tool output is missing, got: %s", string(normalized))
		}
	})

	t.Run("no tool calls in input remains untouched", func(t *testing.T) {
		inputJSON := []byte(`{"model":"kimi-k3","input":[{"type":"message","role":"user","content":"hi"}]}`)
		normalized, errNormalize := NormalizeKimiResponsesInput(inputJSON)
		if errNormalize != nil {
			t.Fatalf("NormalizeKimiResponsesInput error = %v", errNormalize)
		}
		if !bytes.Equal(normalized, inputJSON) {
			t.Fatalf("expected byte identical return when no tool calls exist, got: %s", string(normalized))
		}
	})

	t.Run("multiple tool batches across history", func(t *testing.T) {
		inputJSON := []byte(`{
			"model": "kimi-k3",
			"input": [
				{"type":"function_call","call_id":"b1_c1","name":"t1","arguments":"{}"},
				{"type":"function_call","call_id":"b1_c2","name":"t2","arguments":"{}"},
				{"type":"message","role":"developer","content":[{"type":"input_text","text":"dev1"}]},
				{"type":"function_call_output","call_id":"b1_c1","output":"out1"},
				{"type":"function_call_output","call_id":"b1_c2","output":"out2"},
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done batch 1"}]},
				{"type":"function_call","call_id":"b2_c1","name":"t3","arguments":"{}"},
				{"type":"message","role":"developer","content":[{"type":"input_text","text":"dev2"}]},
				{"type":"function_call_output","call_id":"b2_c1","output":"out3"}
			]
		}`)

		normalized, errNormalize := NormalizeKimiResponsesInput(inputJSON)
		if errNormalize != nil {
			t.Fatalf("NormalizeKimiResponsesInput error = %v", errNormalize)
		}

		items := gjson.GetBytes(normalized, "input").Array()
		if len(items) != 9 {
			t.Fatalf("expected 9 items, got %d: %s", len(items), string(normalized))
		}

		// Batch 1: b1_c1, b1_c2 calls -> b1_c1 out, b1_c2 out -> dev1
		if items[2].Get("call_id").String() != "b1_c1" || items[2].Get("type").String() != "function_call_output" {
			t.Errorf("items[2] = %s, want b1_c1 output", items[2].Raw)
		}
		if items[3].Get("call_id").String() != "b1_c2" || items[3].Get("type").String() != "function_call_output" {
			t.Errorf("items[3] = %s, want b1_c2 output", items[3].Raw)
		}
		if items[4].Get("role").String() != "developer" {
			t.Errorf("items[4] = %s, want dev1", items[4].Raw)
		}
		if items[5].Get("role").String() != "assistant" {
			t.Errorf("items[5] = %s, want assistant", items[5].Raw)
		}
		// Batch 2: b2_c1 call -> b2_c1 out -> dev2
		if items[6].Get("call_id").String() != "b2_c1" || items[6].Get("type").String() != "function_call" {
			t.Errorf("items[6] = %s, want b2_c1 call", items[6].Raw)
		}
		if items[7].Get("call_id").String() != "b2_c1" || items[7].Get("type").String() != "function_call_output" {
			t.Errorf("items[7] = %s, want b2_c1 output", items[7].Raw)
		}
		if items[8].Get("role").String() != "developer" {
			t.Errorf("items[8] = %s, want dev2", items[8].Raw)
		}
	})

	t.Run("parallel contiguous outputs remain byte identical", func(t *testing.T) {
		inputJSON := []byte(`{"model":"kimi-k3","input":[{"type":"function_call","call_id":"c1","name":"t1"},{"type":"function_call","call_id":"c2","name":"t2"},{"type":"function_call_output","call_id":"c1","output":"o1"},{"type":"function_call_output","call_id":"c2","output":"o2"},{"type":"message","role":"developer"}]}`)
		normalized, errNormalize := NormalizeKimiResponsesInput(inputJSON)
		if errNormalize != nil {
			t.Fatalf("NormalizeKimiResponsesInput error = %v", errNormalize)
		}
		if !bytes.Equal(normalized, inputJSON) {
			t.Fatalf("expected byte identical return for parallel contiguous outputs, got: %s", string(normalized))
		}
	})

	t.Run("idempotence: normalizing twice yields same result", func(t *testing.T) {
		inputJSON := []byte(`{
			"model": "kimi-k3",
			"input": [
				{"type":"function_call","call_id":"c1","name":"t1"},
				{"type":"function_call","call_id":"c2","name":"t2"},
				{"type":"function_call_output","call_id":"c1","output":"o1"},
				{"type":"message","role":"developer","content":"note"},
				{"type":"function_call_output","call_id":"c2","output":"o2"}
			]
		}`)
		firstPass, errFirst := NormalizeKimiResponsesInput(inputJSON)
		if errFirst != nil {
			t.Fatalf("first pass error = %v", errFirst)
		}
		secondPass, errSecond := NormalizeKimiResponsesInput(firstPass)
		if errSecond != nil {
			t.Fatalf("second pass error = %v", errSecond)
		}
		if !bytes.Equal(firstPass, secondPass) {
			t.Fatalf("normalization is not idempotent: first=%s, second=%s", string(firstPass), string(secondPass))
		}
	})

	t.Run("custom tool call outputs supported", func(t *testing.T) {
		inputJSON := []byte(`{
			"model": "kimi-k3",
			"input": [
				{"type":"custom_tool_call","call_id":"ctc_1","name":"custom_lookup"},
				{"type":"message","role":"developer","content":"note"},
				{"type":"custom_tool_call_output","call_id":"ctc_1","output":"lookup_result"}
			]
		}`)
		normalized, errNormalize := NormalizeKimiResponsesInput(inputJSON)
		if errNormalize != nil {
			t.Fatalf("NormalizeKimiResponsesInput error = %v", errNormalize)
		}
		items := gjson.GetBytes(normalized, "input").Array()
		if len(items) != 3 {
			t.Fatalf("expected 3 items, got %d", len(items))
		}
		if items[1].Get("type").String() != "custom_tool_call_output" || items[1].Get("call_id").String() != "ctc_1" {
			t.Errorf("items[1] = %s, want custom_tool_call_output ctc_1", items[1].Raw)
		}
		if items[2].Get("type").String() != "message" || items[2].Get("role").String() != "developer" {
			t.Errorf("items[2] = %s, want developer message", items[2].Raw)
		}
	})
}

func TestNormalizeKimiResponsesCustomTools(t *testing.T) {
	body := []byte(`{
		"tools":[
			{"type":"custom","name":"exec","description":"run a command","format":{"type":"text"}},
			{"type":"custom","name":"apply_patch"},
			{"type":"function","name":"read","parameters":{}},
			{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"wait"}]}
		],
		"input":[
			{"type":"additional_tools","tools":[{"type":"custom","name":"view_image"},{"type":"custom","name":"exec"},{"type":"namespace","name":"functions","children":[{"type":"custom","name":"request_user_input"}]}]},
			{"type":"custom_tool_call","id":"ctc_exec","call_id":"c1","name":"exec","input":"pwd"},
			{"type":"custom_tool_call_output","id":"ctco_exec","call_id":"c1","output":"ok"},
			{"type":"custom_tool_call","id":"ctc_patch","call_id":"p1","name":"apply_patch","input":"patch"}
		],
		"tool_choice":{"type":"custom","name":"exec"}
	}`)

	normalized, errNormalize := NormalizeKimiResponsesCustomTools(body)
	if errNormalize != nil {
		t.Fatalf("NormalizeKimiResponsesCustomTools error = %v", errNormalize)
	}
	if got := gjson.GetBytes(normalized, "tools.0.type").String(); got != "function" {
		t.Fatalf("exec declaration type = %q, want function: %s", got, normalized)
	}
	if got := gjson.GetBytes(normalized, "tools.0.parameters.properties.input.type").String(); got != "string" {
		t.Fatalf("exec input type = %q, want string", got)
	}
	if gjson.GetBytes(normalized, "tools.0.format").Exists() {
		t.Fatal("exec format was not removed")
	}
	if got := gjson.GetBytes(normalized, "tools.1.type").String(); got != "function" {
		t.Fatalf("apply_patch declaration type = %q, want function", got)
	}
	if got := gjson.GetBytes(normalized, "tools.3.name").String(); got != "functions__wait" {
		t.Fatalf("namespaced tool name = %q, want functions__wait", got)
	}
	if got := gjson.GetBytes(normalized, "tools.3.type").String(); got != "function" {
		t.Fatalf("namespaced custom declaration type = %q, want function", got)
	}
	if got := gjson.GetBytes(normalized, "tools.4.name").String(); got != "view_image" {
		t.Fatalf("additional tool name = %q, want view_image", got)
	}
	if got := gjson.GetBytes(normalized, "tools.5.name").String(); got != "functions__request_user_input" {
		t.Fatalf("message namespace tool name = %q, want functions__request_user_input", got)
	}
	if hasKimiAdditionalTools(normalized) {
		t.Fatalf("additional_tools metadata was not removed from Kimi input: %s", normalized)
	}
	if got := gjson.GetBytes(normalized, "input.0.type").String(); got != "function_call" {
		t.Fatalf("custom history type = %q, want function_call", got)
	}
	if got := gjson.GetBytes(normalized, "input.0.id").String(); got != "fc_exec" {
		t.Fatalf("custom history id = %q, want fc_exec", got)
	}
	if got := gjson.GetBytes(normalized, "input.0.arguments").String(); got != `{"input":"pwd"}` {
		t.Fatalf("custom history arguments = %q", got)
	}
	if got := gjson.GetBytes(normalized, "input.1.type").String(); got != "function_call_output" {
		t.Fatalf("custom output type = %q, want function_call_output", got)
	}
	if got := gjson.GetBytes(normalized, "input.1.call_id").String(); got != "c1" {
		t.Fatalf("custom output call_id = %q, want c1", got)
	}
	if gjson.GetBytes(normalized, "input.1.id").Exists() {
		t.Fatalf("custom output id should be omitted from Kimi request: %s", normalized)
	}
	if got := gjson.GetBytes(normalized, "input.2.type").String(); got != "function_call" {
		t.Fatalf("apply_patch history type = %q, want function_call", got)
	}
	if got := gjson.GetBytes(normalized, "tool_choice.type").String(); got != "function" {
		t.Fatalf("tool choice type = %q, want function", got)
	}
}

func TestNormalizeKimiResponsesCustomToolsDeduplicatesMessageTools(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"function","name":"request_user_input","parameters":{"type":"object"}}],
		"input":[
			{"type":"additional_tools","tools":[{"type":"namespace","name":"functions","children":[
				{"type":"function","name":"request_user_input","parameters":{"type":"object"}},
				{"type":"function","name":"request_user_input_async","parameters":{"type":"object"}}
			]}]}
		]
	}`)

	normalized, errNormalize := NormalizeKimiResponsesCustomTools(body)
	if errNormalize != nil {
		t.Fatalf("NormalizeKimiResponsesCustomTools error = %v", errNormalize)
	}
	if got := len(gjson.GetBytes(normalized, "tools").Array()); got != 3 {
		t.Fatalf("flattened tools = %d, want direct and two namespaced functions", got)
	}
	if got := gjson.GetBytes(normalized, "tools.1.name").String(); got != "functions__request_user_input" {
		t.Fatalf("flattened namespace tool name = %q", got)
	}
	if got := gjson.GetBytes(normalized, "tools.2.name").String(); got != "functions__request_user_input_async" {
		t.Fatalf("flattened second namespace tool name = %q", got)
	}
	if hasKimiAdditionalTools(normalized) {
		t.Fatalf("additional_tools record was not folded into tools: %s", normalized)
	}
}

func TestNormalizeKimiResponsesCustomToolsDropsEmptyNamespaces(t *testing.T) {
	body := []byte(`{
		"tools":[
			{"type":"namespace","name":"clock","tools":[]},
			{"type":"function","name":"exec","parameters":{"type":"object"}}
		],
		"input":[
			{"type":"additional_tools","tools":[
				{"type":"namespace","name":"collaboration","children":[]},
				{"type":"namespace","name":"functions","children":[
					{"type":"custom","name":"read"}
				]}
			]}
		]
	}`)

	normalized, errNormalize := NormalizeKimiResponsesCustomTools(body)
	if errNormalize != nil {
		t.Fatalf("NormalizeKimiResponsesCustomTools error = %v", errNormalize)
	}
	if got := len(gjson.GetBytes(normalized, "tools").Array()); got != 2 {
		t.Fatalf("flattened tools = %d, want direct function and namespace child", got)
	}
	if got := gjson.GetBytes(normalized, "tools.1.name").String(); got != "functions__read" {
		t.Fatalf("flattened non-empty namespace child = %q", got)
	}
	if hasKimiAdditionalTools(normalized) {
		t.Fatalf("additional_tools record was not removed: %s", normalized)
	}
}

func hasKimiAdditionalTools(body []byte) bool {
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() == "additional_tools" {
			return true
		}
	}
	return false
}

func TestNormalizeKimiResponsesCustomToolsDropsMalformedMessageNamespace(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"custom","name":"exec"}],
		"input":[
			{"type":"message","role":"user","content":"run"},
			{"type":"additional_tools","tools":[
				{"type":"custom","name":"read"},
				{"type":"namespace","name":"functions"}
			]}
		]
	}`)

	normalized, errNormalize := NormalizeKimiResponsesCustomTools(body)
	if errNormalize != nil {
		t.Fatalf("NormalizeKimiResponsesCustomTools error = %v", errNormalize)
	}
	messageTools := gjson.GetBytes(normalized, "tools").Array()
	if len(messageTools) != 2 || messageTools[1].Get("name").String() != "read" {
		t.Fatalf("flattened tools contain a malformed namespace: %s", gjson.GetBytes(normalized, "tools").Raw)
	}
}

func TestNormalizeKimiResponsesCustomToolsFoldsDuplicateFunctionNamespaces(t *testing.T) {
	body := []byte(`{
		"model":"kimi-k3.1-pro",
		"tools":[{"type":"function","name":"list_mcp_resources"}],
		"input":[
			{"type":"additional_tools","tools":[{"type":"namespace","name":"functions","tools":[
				{"type":"custom","name":"exec"},
				{"type":"function","name":"wait"}
			]}]},
			{"type":"additional_tools","tools":[{"type":"namespace","name":"functions","children":[
				{"type":"custom","name":"exec"}
			]}]}
		]
	}`)

	normalized, errNormalize := NormalizeKimiResponsesCustomTools(body)
	if errNormalize != nil {
		t.Fatalf("NormalizeKimiResponsesCustomTools error = %v", errNormalize)
	}
	if hasKimiAdditionalTools(normalized) {
		t.Fatalf("additional_tools remained in normalized request: %s", normalized)
	}
	tools := gjson.GetBytes(normalized, "tools").Array()
	var execCount int
	for _, tool := range tools {
		if tool.Get("name").String() == "functions__exec" {
			execCount++
		}
	}
	if execCount != 1 {
		t.Fatalf("functions__exec count = %d, want 1: %s", execCount, normalized)
	}
}

func TestKimiResponsesCustomToolStateRestoresNonStreamAndStream(t *testing.T) {
	original := []byte(`{"tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec"}]},{"type":"function","name":"read"}]}`)
	state := NewKimiResponsesCustomToolState(original)
	response := []byte(`{"id":"r1","output":[{"id":"fc_1","type":"function_call","status":"completed","call_id":"c1","name":"functions__exec","arguments":"{\"input\":\"pwd\"}"},{"id":"fc_2","type":"function_call","call_id":"c2","name":"read","arguments":"{}"}]}`)
	restored, errRestore := state.TransformNonStream(response)
	if errRestore != nil {
		t.Fatalf("TransformNonStream error = %v", errRestore)
	}
	if got := gjson.GetBytes(restored, "output.0.type").String(); got != "custom_tool_call" {
		t.Fatalf("restored exec tool type = %q, want custom_tool_call", got)
	}
	if got := gjson.GetBytes(restored, "output.0.id").String(); got != "ctc_1" {
		t.Fatalf("restored exec custom id = %q, want ctc_1", got)
	}
	if got := gjson.GetBytes(restored, "output.0.name").String(); got != "exec" {
		t.Fatalf("restored exec wire name = %q, want exec", got)
	}
	if got := gjson.GetBytes(restored, "output.0.namespace").String(); got != "functions" {
		t.Fatalf("exec namespace = %q, want functions: %s", got, restored)
	}
	if got := gjson.GetBytes(restored, "output.0.input").String(); got != "pwd" {
		t.Fatalf("restored exec input = %q, want pwd", got)
	}
	if gjson.GetBytes(restored, "output.0.arguments").Exists() {
		t.Fatalf("exec function arguments leaked into custom response: %s", restored)
	}
	if got := gjson.GetBytes(restored, "output.1.type").String(); got != "function_call" {
		t.Fatalf("ordinary function type = %q, want function_call", got)
	}

	streamState := NewKimiResponsesCustomToolState(original)
	var streamed []byte
	frames := [][]byte{
		[]byte("event: response.output_item.added"),
		[]byte(`data: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"c1","name":"functions__exec","arguments":""}}`),
		[]byte("event: response.function_call_arguments.delta"),
		[]byte(`data: {"type":"response.function_call_arguments.delta","sequence_number":2,"item_id":"fc_1","delta":"{\"input\":\"pwd\"}"}`),
		[]byte("event: response.function_call_arguments.done"),
		[]byte(`data: {"type":"response.function_call_arguments.done","sequence_number":3,"item_id":"fc_1","arguments":"{\"input\":\"pwd\"}"}`),
		[]byte("event: response.output_item.done"),
		[]byte(`data: {"type":"response.output_item.done","sequence_number":4,"output_index":0,"item":{"id":"fc_1","type":"function_call","status":"completed","call_id":"c1","name":"functions__exec","arguments":"{\"input\":\"pwd\"}"}}`),
	}
	for _, frame := range frames {
		converted, errStream := streamState.Stream(frame)
		if errStream != nil {
			t.Fatalf("Stream(%s) error = %v", frame, errStream)
		}
		streamed = append(streamed, bytes.Join(converted, nil)...)
	}
	if !strings.Contains(string(streamed), "response.custom_tool_call_input.done") {
		t.Fatalf("exec custom input completion event is missing: %s", streamed)
	}
	if strings.Contains(string(streamed), "response.function_call_arguments") {
		t.Fatalf("exec function arguments events leaked into custom response: %s", streamed)
	}
	if !strings.Contains(string(streamed), `"id":"ctc_1"`) || !strings.Contains(string(streamed), `"item_id":"ctc_1"`) || !strings.Contains(string(streamed), `"name":"exec"`) || !strings.Contains(string(streamed), `"namespace":"functions"`) || strings.Contains(string(streamed), `"arguments":"{\"input\":\"pwd\"}"`) {
		t.Fatalf("exec custom stream identity/name/input missing: %s", streamed)
	}
}

func TestNormalizeKimiResponsesCustomToolsRepairsMissingCallID(t *testing.T) {
	body := []byte(`{"tools":[{"type":"custom","name":"exec"}],"input":[{"type":"custom_tool_call","id":"ctc_7","name":"exec","input":"pwd"},{"type":"custom_tool_call_output","output":"/tmp"}]}`)
	normalized, errNormalize := NormalizeKimiResponsesCustomTools(body)
	if errNormalize != nil {
		t.Fatalf("NormalizeKimiResponsesCustomTools error = %v", errNormalize)
	}
	callID := gjson.GetBytes(normalized, "input.0.call_id").String()
	if callID == "" || gjson.GetBytes(normalized, "input.1.call_id").String() != callID {
		t.Fatalf("missing call_id was not repaired consistently: %s", normalized)
	}
	if gjson.GetBytes(normalized, "input.1.type").String() != "function_call_output" {
		t.Fatalf("missing custom output type conversion: %s", normalized)
	}

	ambiguous := []byte(`{"input":[{"type":"function_call","call_id":"a","name":"a"},{"type":"function_call","call_id":"b","name":"b"},{"type":"function_call_output","output":"x"}]}`)
	if _, errNormalize = NormalizeKimiResponsesCustomTools(ambiguous); errNormalize == nil {
		t.Fatal("ambiguous missing call_id unexpectedly normalized")
	}
}

func TestKimiResponsesRestoresOrdinaryFunctionsNamespace(t *testing.T) {
	original := []byte(`{"tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec"},{"type":"function","name":"wait"}]}]}`)
	response := []byte(`{"output":[{"type":"function_call","id":"fc_wait","call_id":"call_wait","name":"functions__wait","arguments":"{\"cell_id\":\"cell_1\"}"}]}`)
	state := NewKimiResponsesCustomToolState(original)
	restored, errRestore := state.TransformNonStream(response)
	if errRestore != nil {
		t.Fatal(errRestore)
	}
	item := gjson.GetBytes(restored, "output.0")
	if item.Get("type").String() != "function_call" || item.Get("name").String() != "wait" || item.Get("namespace").String() != "functions" || item.Get("arguments").String() != `{"cell_id":"cell_1"}` {
		t.Fatalf("ordinary wait tool identity or arguments changed: %s", restored)
	}
	frames, errStream := NewKimiResponsesCustomToolState(original).Stream([]byte(`data: {"type":"response.output_item.done","output_index":0,"item":` + gjson.GetBytes(response, "output.0").Raw + `}`))
	if errStream != nil {
		t.Fatal(errStream)
	}
	stream := string(bytes.Join(frames, nil))
	if !strings.Contains(stream, `"name":"wait"`) || !strings.Contains(stream, `"namespace":"functions"`) || strings.Contains(stream, `custom_tool_call`) {
		t.Fatalf("ordinary wait stream did not restore its declaration: %s", stream)
	}
}
