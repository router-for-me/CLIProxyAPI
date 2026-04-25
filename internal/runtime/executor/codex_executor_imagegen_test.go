package executor

import (
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestEnsureImageGenerationTool_NoTools(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"draw a cat"}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil)

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
	result := ensureImageGenerationTool(body, "gpt-5.4", nil)

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
	result := ensureImageGenerationTool(body, "gpt-5.4", nil)

	tools := gjson.GetBytes(result, "tools")
	arr := tools.Array()
	if len(arr) != 2 {
		t.Fatalf("expected 2 tools (no duplicate), got %d", len(arr))
	}
	if arr[0].Get("output_format").String() != "webp" {
		t.Fatalf("expected original output_format=webp preserved, got %s", arr[0].Get("output_format").String())
	}
}

func TestEnsureImageGenerationTool_EmptyToolsArray(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil)

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
	result := ensureImageGenerationTool(body, "gpt-5.4", nil)

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
	result := ensureImageGenerationTool(body, "gpt-5.3-codex-spark", nil)

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
	result := ensureImageGenerationTool(body, "gpt-5.4", freeAuth)

	if string(result) != string(body) {
		t.Fatalf("expected body to be unchanged, got %s", string(result))
	}
	if gjson.GetBytes(result, "tools").Exists() {
		t.Fatalf("expected no tools for free codex auth, got %s", gjson.GetBytes(result, "tools").Raw)
	}
}

func TestMaybeEnsureImageGenerationTool_SkipsGenericClient(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"web_search"}]}`)
	result := maybeEnsureImageGenerationTool(body, "gpt-5.4", nil)

	if string(result) != string(body) {
		t.Fatalf("expected generic client body unchanged, got %s", string(result))
	}
	if hasImageGenerationTool(result) {
		t.Fatalf("expected no image_generation tool for generic client, got %s", string(result))
	}
}

func TestMaybeEnsureImageGenerationTool_AddsForImageGenerationToolChoice(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tool_choice":{"type":"image_generation"}}`)
	result := maybeEnsureImageGenerationTool(body, "gpt-5.4", nil)

	if !hasImageGenerationTool(result) {
		t.Fatalf("expected image_generation tool for explicit tool_choice, got %s", string(result))
	}
}

func TestMaybeEnsureImageGenerationTool_AppendsForImageGenerationToolChoice(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"web_search"}],"tool_choice":{"type":"image_generation"}}`)
	result := maybeEnsureImageGenerationTool(body, "gpt-5.4", nil)

	if !hasImageGenerationTool(result) {
		t.Fatalf("expected image_generation tool for explicit tool_choice, got %s", string(result))
	}
	tools := gjson.GetBytes(result, "tools").Array()
	if len(tools) != 2 {
		t.Fatalf("expected web_search plus image_generation tools, got %s", gjson.GetBytes(result, "tools").Raw)
	}
}

func TestMaybeEnsureImageGenerationTool_AddsForAllowedToolsImageGenerationToolChoice(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tool_choice":{"type":"allowed_tools","tools":[{"type":"image_generation"}]}}`)
	result := maybeEnsureImageGenerationTool(body, "gpt-5.4", nil)

	if !hasImageGenerationTool(result) {
		t.Fatalf("expected image_generation tool for allowed_tools tool_choice, got %s", string(result))
	}
}

func TestMaybeEnsureImageGenerationTool_SkipsWhenImageGenerationToolAlreadyPresent(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation","output_format":"webp"}],"tool_choice":{"type":"image_generation"}}`)
	result := maybeEnsureImageGenerationTool(body, "gpt-5.4", nil)

	if string(result) != string(body) {
		t.Fatalf("expected existing image_generation body unchanged, got %s", string(result))
	}
}

func TestMaybeEnsureImageGenerationTool_FreeCodexAuthSkipsEvenWithToolChoice(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tool_choice":{"type":"image_generation"}}`)
	freeAuth := &cliproxyauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{"plan_type": "free"},
	}
	result := maybeEnsureImageGenerationTool(body, "gpt-5.4", freeAuth)

	if string(result) != string(body) {
		t.Fatalf("expected free auth body unchanged, got %s", string(result))
	}
	if hasImageGenerationTool(result) {
		t.Fatalf("expected no image_generation tool for free auth, got %s", string(result))
	}
}
