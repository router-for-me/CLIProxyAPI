package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToOpenAI_OutputConfigFormat(t *testing.T) {
	t.Run("json_schema becomes response_format", func(t *testing.T) {
		payload := []byte(`{
			"model": "deepseek-v4-flash",
			"max_tokens": 128,
			"messages": [{"role": "user", "content": "title"}],
			"output_config": {
				"effort": "high",
				"format": {
					"type": "json_schema",
					"schema": {
						"type": "object",
						"properties": {"title": {"type": "string"}},
						"required": ["title"]
					}
				}
			}
		}`)

		out := ConvertClaudeRequestToOpenAI("deepseek-v4-flash", payload, false)
		root := gjson.ParseBytes(out)
		if root.Get("output_config").Exists() {
			t.Fatalf("output_config leaked onto chat body: %s", out)
		}
		if root.Get("reasoning_effort").Exists() {
			t.Fatalf("effort without thinking must not set reasoning_effort: %s", out)
		}
		if got := root.Get("messages.0.content").String(); got != "title" {
			t.Fatalf("messages.0.content = %q, want title", got)
		}
		if got := root.Get("response_format.type").String(); got != "json_schema" {
			t.Fatalf("response_format.type = %q, want json_schema; output=%s", got, out)
		}
		if got := root.Get("response_format.json_schema.name").String(); got != "cli_proxy_structured_output" {
			t.Fatalf("response_format.json_schema.name = %q, want cli_proxy_structured_output", got)
		}
		if got := root.Get("response_format.json_schema.strict"); !got.Exists() || !got.Bool() {
			t.Fatalf("response_format.json_schema.strict = %v, want true; output=%s", got.Value(), out)
		}
		if got := root.Get("response_format.json_schema.description"); got.Exists() {
			t.Fatalf("description should be omitted when absent, got %s", got.Raw)
		}
		if got := root.Get("response_format.json_schema.schema.properties.title.type").String(); got != "string" {
			t.Fatalf("schema.properties.title.type = %q, want string", got)
		}
		if got := root.Get("response_format.json_schema.schema.required.0").String(); got != "title" {
			t.Fatalf("schema.required.0 = %q, want title", got)
		}
	})

	t.Run("custom name strict false and description", func(t *testing.T) {
		payload := []byte(`{
			"model": "gpt-5.4",
			"messages": [{"role": "user", "content": "hello"}],
			"output_config": {
				"format": {
					"type": "json_schema",
					"name": "custom_schema",
					"description": " Structured answer ",
					"strict": false,
					"schema": {"type": "object", "additionalProperties": false}
				}
			}
		}`)

		out := ConvertClaudeRequestToOpenAI("gpt-5.4", payload, true)
		root := gjson.ParseBytes(out)
		if got := root.Get("response_format.json_schema.name").String(); got != "custom_schema" {
			t.Errorf("name = %q, want custom_schema", got)
		}
		if got := root.Get("response_format.json_schema.strict"); !got.Exists() || got.Bool() {
			t.Errorf("strict = %v, want false; output=%s", got.Value(), out)
		}
		if got := root.Get("response_format.json_schema.description").String(); got != "Structured answer" {
			t.Errorf("description = %q, want Structured answer", got)
		}
		if got := root.Get("response_format.json_schema.schema.additionalProperties"); !got.Exists() || got.Bool() {
			t.Errorf("additionalProperties = %v, want false", got.Value())
		}
		if !root.Get("stream").Bool() {
			t.Errorf("stream flag was not preserved; output=%s", out)
		}
	})

	t.Run("compat entry point maps the same format", func(t *testing.T) {
		payload := []byte(`{
			"model": "gpt-5.4",
			"messages": [{"role": "user", "content": "hello"}],
			"output_config": {
				"format": {
					"type": "json_schema",
					"schema": {
						"type": "object",
						"properties": {"answer": {"type": "string"}},
						"required": ["answer"],
						"additionalProperties": false
					}
				}
			}
		}`)
		plain := gjson.GetBytes(ConvertClaudeRequestToOpenAI("gpt-5.4", payload, false), "response_format").Raw
		compat := gjson.GetBytes(ConvertClaudeRequestToOpenAIWithCompat("gpt-5.4", payload, false), "response_format").Raw
		if plain == "" || plain != compat {
			t.Fatalf("compat response_format = %s, want %s", compat, plain)
		}
	})

	t.Run("no format does not invent response_format", func(t *testing.T) {
		payload := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}]}`)
		out := ConvertClaudeRequestToOpenAI("gpt-5.4", payload, false)
		if gjson.GetBytes(out, "response_format").Exists() {
			t.Fatalf("response_format invented: %s", out)
		}
	})

	t.Run("effort only keeps reasoning and skips response_format", func(t *testing.T) {
		payload := []byte(`{
			"model": "gpt-5.4",
			"thinking": {"type": "adaptive"},
			"output_config": {"effort": "high"},
			"messages": [{"role": "user", "content": "hello"}]
		}`)
		out := ConvertClaudeRequestToOpenAI("gpt-5.4", payload, false)
		root := gjson.ParseBytes(out)
		if root.Get("response_format").Exists() {
			t.Fatalf("response_format invented from effort: %s", out)
		}
		if root.Get("output_config").Exists() {
			t.Fatalf("output_config leaked: %s", out)
		}
		if got := root.Get("reasoning_effort").String(); got != "high" {
			t.Fatalf("reasoning_effort = %q, want high", got)
		}
	})

	t.Run("effort and format both survive", func(t *testing.T) {
		payload := []byte(`{
			"model": "gpt-5.4",
			"thinking": {"type": "adaptive"},
			"output_config": {
				"effort": "high",
				"format": {
					"type": "json_schema",
					"schema": {
						"type": "object",
						"properties": {"answer": {"type": "string"}},
						"required": ["answer"],
						"additionalProperties": false
					}
				}
			},
			"messages": [{"role": "user", "content": "hello"}]
		}`)
		out := ConvertClaudeRequestToOpenAI("gpt-5.4", payload, false)
		root := gjson.ParseBytes(out)
		if got := root.Get("reasoning_effort").String(); got != "high" {
			t.Errorf("reasoning_effort = %q, want high", got)
		}
		if got := root.Get("response_format.json_schema.schema.properties.answer.type").String(); got != "string" {
			t.Errorf("schema dropped alongside effort: %s", out)
		}
		if !root.Get("response_format.json_schema.strict").Bool() {
			t.Errorf("strict = false, want true for a fully required schema; output=%s", out)
		}
	})

	t.Run("optional property downgrades strict", func(t *testing.T) {
		payload := []byte(`{
			"model": "gpt-5.4",
			"messages": [{"role": "user", "content": "hello"}],
			"output_config": {
				"format": {
					"type": "json_schema",
					"name": "cli_proxy_structured_output",
					"strict": true,
					"schema": {
						"type": "object",
						"properties": {
							"answer": {"type": "string"},
							"impossible": {"type": "string"}
						},
						"required": ["answer"],
						"additionalProperties": false
					}
				}
			}
		}`)
		out := ConvertClaudeRequestToOpenAI("gpt-5.4", payload, false)
		root := gjson.ParseBytes(out)
		if got := root.Get("response_format.json_schema.strict"); !got.Exists() || got.Bool() {
			t.Errorf("strict = %v, want false for a schema that misses required; output=%s", got.Value(), out)
		}
		if got := root.Get("response_format.json_schema.schema.properties.impossible.type").String(); got != "string" {
			t.Errorf("optional property was dropped: %s", out)
		}
	})

	t.Run("nested optional property downgrades strict", func(t *testing.T) {
		payload := []byte(`{
			"model": "gpt-5.4",
			"messages": [{"role": "user", "content": "hello"}],
			"output_config": {
				"format": {
					"type": "json_schema",
					"schema": {
						"type": "object",
						"properties": {
							"answer": {
								"type": "object",
								"properties": {
									"text": {"type": "string"},
									"note": {"type": "string"}
								},
								"required": ["text"]
							}
						},
						"required": ["answer"],
						"additionalProperties": false
					}
				}
			}
		}`)
		out := ConvertClaudeRequestToOpenAI("gpt-5.4", payload, false)
		if got := gjson.GetBytes(out, "response_format.json_schema.strict"); !got.Exists() || got.Bool() {
			t.Fatalf("strict = %v, want false for a nested optional property; output=%s", got.Value(), out)
		}
	})

	t.Run("json_object maps without a schema", func(t *testing.T) {
		payload := []byte(`{
			"model": "gpt-5.4",
			"messages": [{"role": "user", "content": "hello"}],
			"output_config": {"format": {"type": "json_object"}}
		}`)
		out := ConvertClaudeRequestToOpenAI("gpt-5.4", payload, false)
		root := gjson.ParseBytes(out)
		if got := root.Get("response_format.type").String(); got != "json_object" {
			t.Fatalf("response_format.type = %q, want json_object; output=%s", got, out)
		}
		if root.Get("response_format.json_schema").Exists() {
			t.Fatalf("json_object must not nest json_schema: %s", out)
		}
	})

	t.Run("json_schema without an object schema is not invented", func(t *testing.T) {
		payload := []byte(`{
			"model": "gpt-5.4",
			"messages": [{"role": "user", "content": "hello"}],
			"output_config": {"format": {"type": "json_schema"}}
		}`)
		out := ConvertClaudeRequestToOpenAI("gpt-5.4", payload, false)
		if gjson.GetBytes(out, "response_format").Exists() {
			t.Fatalf("response_format invented without a schema: %s", out)
		}
		if gjson.GetBytes(out, "output_config").Exists() {
			t.Fatalf("output_config leaked: %s", out)
		}
	})
}
