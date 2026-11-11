package gemini

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

func TestNormalizeClaudeToolSchemaPreservesCanonicalSchema(t *testing.T) {
	input := []byte(`{"type":"object","properties":{"value":{"type":"string"}},"additionalProperties":false,"$schema":"http://json-schema.org/draft-07/schema#"}`)

	output := normalizeClaudeToolSchema(util.ParseGJSONBytesNoCopy(input))

	if string(output) != string(input) {
		t.Fatalf("canonical schema changed:\n got: %s\nwant: %s", output, input)
	}
}

func TestNormalizeClaudeToolSchemaCorrectsWrongTypes(t *testing.T) {
	input := []byte(`{"type":"object","additionalProperties":"false","$schema":123}`)

	output := normalizeClaudeToolSchema(util.ParseGJSONBytesNoCopy(input))

	if additionalProperties := ojson.GetBytesNoCopy(output, "additionalProperties"); additionalProperties.Type != ojson.False {
		t.Fatalf("additionalProperties = %s, want false", additionalProperties.Raw)
	}
	if schema := ojson.GetBytesNoCopy(output, "$schema"); schema.Type != ojson.String || schema.String() != "http://json-schema.org/draft-07/schema#" {
		t.Fatalf("$schema = %s, want canonical string", schema.Raw)
	}
}

func TestLowercaseClaudeToolSchemaTypesReusesLowercaseSchema(t *testing.T) {
	input := []byte(`{"name":"lookup","input_schema":{"type":"object","properties":{"value":{"type":"string"}}}}`)

	output := lowercaseClaudeToolSchemaTypes(input)

	if &output[0] != &input[0] {
		t.Fatal("lowercase schema types caused a payload copy")
	}
}

func TestLowercaseClaudeToolSchemaTypesNormalizesNonStringType(t *testing.T) {
	input := []byte(`{"input_schema":{"type":123}}`)

	output := lowercaseClaudeToolSchemaTypes(input)

	if got := ojson.GetBytesNoCopy(output, "input_schema.type"); got.Type != ojson.String || got.String() != "123" {
		t.Fatalf("input_schema.type = %s, want string 123", got.Raw)
	}
}

func TestLowercaseClaudeToolSchemaTypesNormalizesUppercaseTypes(t *testing.T) {
	input := []byte(`{"input_schema":{"type":"OBJECT","properties":{"value":{"type":"STRING"}}}}`)

	output := lowercaseClaudeToolSchemaTypes(input)

	if got := ojson.GetBytesNoCopy(output, "input_schema.type").String(); got != "object" {
		t.Fatalf("input_schema.type = %q, want object", got)
	}
	if got := ojson.GetBytesNoCopy(output, "input_schema.properties.value.type").String(); got != "string" {
		t.Fatalf("nested type = %q, want string", got)
	}
}
