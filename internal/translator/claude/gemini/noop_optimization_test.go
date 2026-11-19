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

func TestLowercaseClaudeToolSchemaTypesPreservesNonStringType(t *testing.T) {
	input := []byte(`{"input_schema":{"type":123,"properties":{"type":{"type":"STRING","enum":["a","b"]}}}}`)

	output := lowercaseClaudeToolSchemaTypes(input)

	if got := ojson.GetBytesNoCopy(output, "input_schema.type"); got.Type != ojson.Number || got.Int() != 123 {
		t.Fatalf("input_schema.type = %s (type=%v), want number 123", got.Raw, got.Type)
	}
	if prop := ojson.GetBytesNoCopy(output, "input_schema.properties.type"); !prop.IsObject() {
		t.Fatalf("input_schema.properties.type = %s, want object", prop.Raw)
	}
	if got := ojson.GetBytesNoCopy(output, "input_schema.properties.type.type").String(); got != "string" {
		t.Fatalf("input_schema.properties.type.type = %q, want string", got)
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
