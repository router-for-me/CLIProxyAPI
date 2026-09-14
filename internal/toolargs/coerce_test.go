package toolargs

import (
	"encoding/json"
	"reflect"
	"testing"
)

func schemaOf(t *testing.T, raw string) any {
	t.Helper()
	var s any
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("bad schema fixture: %v", err)
	}
	return s
}

func TestCoerceIntegralFloatsExactFieldSample(t *testing.T) {
	// The exact field case: Codex's strict router rejected `14380.0` for an i32 param.
	schema := schemaOf(t, `{"type":"object","properties":{"session_id":{"type":"integer"},"yield_time_ms":{"type":"integer"}}}`)
	got, coerced := CoerceIntegralFloats(`{"session_id":1,"yield_time_ms":14380.0}`, schema)
	if got != `{"session_id":1,"yield_time_ms":14380}` {
		t.Fatalf("got %s", got)
	}
	if !reflect.DeepEqual(coerced, []string{"yield_time_ms"}) {
		t.Fatalf("coerced = %v", coerced)
	}
}

func TestCoerceIntegralFloatsNestedObjectsAndArrays(t *testing.T) {
	schema := schemaOf(t, `{"type":"object","properties":{
		"opts":{"type":"object","properties":{"retries":{"type":"integer"},"label":{"type":"string"}}},
		"ids":{"type":"array","items":{"type":"integer"}},
		"matrix":{"type":"array","items":{"type":"array","items":{"type":"integer"}}}}}`)
	in := `{"opts":{"retries":3.0,"label":"x"},"ids":[1.0,2,3.0],"matrix":[[4.0],[5]]}`
	got, coerced := CoerceIntegralFloats(in, schema)
	if got != `{"opts":{"retries":3,"label":"x"},"ids":[1,2,3],"matrix":[[4],[5]]}` {
		t.Fatalf("got %s", got)
	}
	want := []string{"opts.retries", "ids[0]", "ids[2]", "matrix[0][0]"}
	if !reflect.DeepEqual(coerced, want) {
		t.Fatalf("coerced = %v, want %v", coerced, want)
	}
}

func TestCoerceIntegralFloatsNumberTypedParamUntouched(t *testing.T) {
	schema := schemaOf(t, `{"type":"object","properties":{"ratio":{"type":"number"}}}`)
	got, coerced := CoerceIntegralFloats(`{"ratio":14380.0}`, schema)
	if got != `{"ratio":14380.0}` || len(coerced) != 0 {
		t.Fatalf("number param must stay: %s %v", got, coerced)
	}
}

func TestCoerceIntegralFloatsNonIntegralPassesThrough(t *testing.T) {
	// A genuinely fractional value for an integer param is NOT ours to fix; let the client reject.
	schema := schemaOf(t, `{"type":"object","properties":{"n":{"type":"integer"}}}`)
	got, coerced := CoerceIntegralFloats(`{"n":14380.5}`, schema)
	if got != `{"n":14380.5}` || len(coerced) != 0 {
		t.Fatalf("non-integral must pass through: %s %v", got, coerced)
	}
}

func TestCoerceIntegralFloatsPreservesUnrelatedBytesAndBigInts(t *testing.T) {
	// No float round-trip of the whole document: the big integer, key order, and whitespace
	// must survive byte-for-byte; only the target node changes.
	schema := schemaOf(t, `{"type":"object","properties":{"big":{"type":"integer"},"n":{"type":"integer"},"s":{"type":"string"}}}`)
	in := `{ "big" : 12345678901234567890123456789 , "s" : "14380.0" , "n" : 2.0 }`
	got, coerced := CoerceIntegralFloats(in, schema)
	if got != `{ "big" : 12345678901234567890123456789 , "s" : "14380.0" , "n" : 2 }` {
		t.Fatalf("got %s", got)
	}
	if !reflect.DeepEqual(coerced, []string{"n"}) {
		t.Fatalf("coerced = %v", coerced)
	}
}

func TestCoerceIntegralFloatsUndeterminedSchemaPassesThrough(t *testing.T) {
	cases := map[string]string{
		"anyOf":       `{"type":"object","properties":{"n":{"anyOf":[{"type":"integer"},{"type":"string"}]}}}`,
		"oneOf":       `{"type":"object","properties":{"n":{"oneOf":[{"type":"integer"}]}}}`,
		"allOf":       `{"type":"object","properties":{"n":{"allOf":[{"type":"integer"}]}}}`,
		"ref":         `{"type":"object","properties":{"n":{"$ref":"#/$defs/int"}},"$defs":{"int":{"type":"integer"}}}`,
		"rootRef":     `{"$ref":"#/$defs/root","$defs":{"root":{"type":"object","properties":{"n":{"type":"integer"}}}}}`,
		"intOrNumber": `{"type":"object","properties":{"n":{"type":["integer","number"]}}}`,
		"missingProp": `{"type":"object","properties":{"other":{"type":"integer"}}}`,
		"noSchema":    `null`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			got, coerced := CoerceIntegralFloats(`{"n":2.0}`, schemaOf(t, raw))
			if got != `{"n":2.0}` || len(coerced) != 0 {
				t.Fatalf("must pass through unchanged: %s %v", got, coerced)
			}
		})
	}
}

func TestCoerceIntegralFloatsNullableIntegerAndExponentAndNegative(t *testing.T) {
	schema := schemaOf(t, `{"type":"object","properties":{"a":{"type":["integer","null"]},"b":{"type":"integer"},"c":{"type":"integer"}}}`)
	got, coerced := CoerceIntegralFloats(`{"a":7.0,"b":1.438e4,"c":-5.0}`, schema)
	if got != `{"a":7,"b":14380,"c":-5}` {
		t.Fatalf("got %s", got)
	}
	if !reflect.DeepEqual(coerced, []string{"a", "b", "c"}) {
		t.Fatalf("coerced = %v", coerced)
	}
}

func TestCoerceIntegralFloatsAlreadyIntegerIsNoop(t *testing.T) {
	schema := schemaOf(t, `{"type":"object","properties":{"n":{"type":"integer"}}}`)
	got, coerced := CoerceIntegralFloats(`{"n":14380}`, schema)
	if got != `{"n":14380}` || len(coerced) != 0 {
		t.Fatalf("integer literal must be a no-op: %s %v", got, coerced)
	}
}

func TestCoerceIntegralFloatsMalformedArgumentsUnchanged(t *testing.T) {
	schema := schemaOf(t, `{"type":"object","properties":{"n":{"type":"integer"}}}`)
	for _, in := range []string{``, `{"n":2.0`, `not json`, `{"n":2.0} trailing`} {
		got, coerced := CoerceIntegralFloats(in, schema)
		if got != in || len(coerced) != 0 {
			t.Fatalf("malformed %q must be unchanged: %s %v", in, got, coerced)
		}
	}
}

func TestFindToolParamsChatAndResponsesShapes(t *testing.T) {
	chat := []byte(`{"tools":[{"type":"function","function":{"name":"wait","parameters":{"type":"object","properties":{"ms":{"type":"integer"}}}}}]}`)
	if p, ok := FindToolParams(chat, "wait"); !ok || p == nil {
		t.Fatalf("chat-shape tool not found")
	}
	responses := []byte(`{"tools":[{"type":"function","name":"wait","parameters":{"type":"object","properties":{"ms":{"type":"integer"}}}}]}`)
	if p, ok := FindToolParams(responses, "wait"); !ok || p == nil {
		t.Fatalf("responses-shape tool not found")
	}
	if _, ok := FindToolParams(chat, "other"); ok {
		t.Fatalf("missing tool must not resolve")
	}
}

func TestFindToolParamsAmbiguousOrSchemalessFailsClosed(t *testing.T) {
	dup := []byte(`{"tools":[{"type":"function","function":{"name":"wait","parameters":{"type":"object"}}},{"type":"function","function":{"name":"wait","parameters":{"type":"object"}}}]}`)
	if _, ok := FindToolParams(dup, "wait"); ok {
		t.Fatalf("duplicate tool names must not resolve (cannot pick a schema)")
	}
	noParams := []byte(`{"tools":[{"type":"function","function":{"name":"wait"}}]}`)
	if _, ok := FindToolParams(noParams, "wait"); ok {
		t.Fatalf("tool without parameters must not resolve")
	}
	if _, ok := FindToolParams([]byte(`{}`), "wait"); ok {
		t.Fatalf("no tools must not resolve")
	}
	if _, ok := FindToolParams([]byte(`{"tools":[]}`), ""); ok {
		t.Fatalf("empty name must not resolve")
	}
}
