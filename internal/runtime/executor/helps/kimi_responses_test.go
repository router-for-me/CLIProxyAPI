package helps

import (
	"bytes"
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
