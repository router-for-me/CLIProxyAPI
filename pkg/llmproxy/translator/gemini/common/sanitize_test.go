package common

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAIFunctionSchemaForGemini_StrictAddsClosedObject(t *testing.T) {
	params := gjson.Parse(`{
		"type":"object",
		"$id":"urn:test",
		"properties":{"name":{"type":"string"}},
		"patternProperties":{"^x-":{"type":"string"}}
	}`)

	got := NormalizeOpenAIFunctionSchemaForGemini(params, true)
	res := gjson.Parse(got)

	if res.Get("$id").Exists() {
		t.Fatalf("expected $id to be removed")
	}
	if res.Get("patternProperties").Exists() {
		t.Fatalf("expected patternProperties to be removed")
	}
	if res.Get("type").String() != "OBJECT" {
		t.Fatalf("expected root type OBJECT, got %q", res.Get("type").String())
	}
	if !res.Get("properties.name").Exists() {
		t.Fatalf("expected properties.name to exist")
	}
	if !res.Get("additionalProperties").Exists() || res.Get("additionalProperties").Bool() {
		t.Fatalf("expected additionalProperties=false when strict=true")
	}
}

func TestNormalizeOpenAIFunctionSchemaForGemini_EmptySchemaDefaults(t *testing.T) {
	got := NormalizeOpenAIFunctionSchemaForGemini(gjson.Result{}, false)
	res := gjson.Parse(got)

	if res.Get("type").String() != "OBJECT" {
		t.Fatalf("expected root type OBJECT, got %q", res.Get("type").String())
	}
	if !res.Get("properties").IsObject() {
		t.Fatalf("expected properties object to exist")
	}
	if res.Get("additionalProperties").Exists() {
		t.Fatalf("did not expect additionalProperties for non-strict schema")
	}
}

func TestNormalizeOpenAIFunctionSchemaForGemini_CleansNullableAndTypeArrays(t *testing.T) {
	params := gjson.Parse(`{
		"type":"object",
		"properties":{
			"query":{"type":"string"},
			"limit":{"type":["integer","null"],"nullable":true}
		},
		"required":["query","limit"]
	}`)

	got := NormalizeOpenAIFunctionSchemaForGemini(params, false)
	res := gjson.Parse(got)

	if res.Get("properties.limit.nullable").Exists() {
		t.Fatalf("expected nullable to be removed from limit schema")
	}
	if res.Get("properties.limit.type").IsArray() {
		t.Fatalf("expected limit.type array to be flattened, got %s", res.Get("properties.limit.type").Raw)
	}

	required := res.Get("required").Array()
	for _, field := range required {
		if field.String() == "limit" {
			t.Fatalf("expected nullable field limit to be removed from required list")
		}
	}
}
