package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestNormalizeAntigravityGeminiFunctionResponseRolesMatchesLegacy(t *testing.T) {
	fixtures := [][]byte{
		[]byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"read","args":{}}},{"functionCall":{"id":"call-2","name":"write","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"id":"call-2","name":"write","response":{"ok":2}}},{"functionResponse":{"id":"call-1","name":"read","response":{"ok":1}}}]}]}}`),
		[]byte("{\r\n  \"request\" : {\r\n    \"contents\" : [\r\n      {\"role\":\"model\",\"parts\":[{\"functionCall\":{\"id\":\"a\",\"name\":\"one\"}},{\"functionCall\":{\"id\":\"b\",\"name\":\"two\"}}]},\r\n      {\"role\" : \"user\", \"parts\" : [ { \"functionResponse\" : {\"id\":\"a\",\"name\":\"one\"} }, { \"functionResponse\" : {\"id\":\"b\",\"name\":\"two\"} } ]}\r\n    ]\r\n  }\r\n}"),
		[]byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"a","name":"actual"}}]},{"parts":[{"functionResponse":{"id":"a","name":"unknown"}}]}]}}`),
		[]byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"a","name":"actual"}}]},{"role":"user","parts":[{"functionResponse":{"id":"a","name":"","response":{"ok":true}}}]}]}}`),
		[]byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"a","name":"actual"}}]},{"role":"user","parts":[{"functionResponse":{"id":"a","response":{"ok":true}}}]}]}}`),
		[]byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"a","name":"one"}}]},{"role":"user","role":"model","parts":[{"functionResponse":{"id":"a","name":"one"}}]}]}}`),
		[]byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"a","name":"one"}}]},{"role":"user","parts":[{"functionResponse":{"id":"a","name":"one"}}],"parts":[{"text":"duplicate"}]}]}}`),
		[]byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"a","name":"one"}}]},{"role":"user","parts":[{"functionResponse":{"id":"a","name":"one"}}]}],"contents":[{"role":"user","parts":[]}]}}`),
		[]byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"name":"one"}},{"functionCall":{"name":"one"}}]},{"role":" Model ","parts":[{"functionResponse":{"name":"one"}},{"functionResponse":{"name":"one"}}]}]}}`),
		[]byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"a","name":"one"}}]},{"role":"user","parts":[]},{"role":"user","parts":[{"functionResponse":{"id":"a","name":"one"}}]}]}}`),
		[]byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"a","name":"one"}}]},{"role":"user","parts":[{"functionResponse":{"id":"a","name":"one"}}]}]`),
		[]byte(`{"prefix":broken,"request":{"contents":[{"role":"user","parts":[{"functionResponse":{"id":"a","name":"one"}}]}]}}`),
	}

	randomSource := rand.New(rand.NewSource(0xA617A5))
	for range 1_000 {
		fixtures = append(fixtures, randomAntigravityFunctionHistory(randomSource))
	}

	changed := 0
	unchanged := 0
	for index, fixture := range fixtures {
		want := legacyNormalizeAntigravityGeminiFunctionResponseRoles(fixture)
		got := normalizeAntigravityGeminiFunctionResponseRoles(fixture)
		if !bytes.Equal(got, want) {
			t.Fatalf("case %d differs: input_bytes=%d got_bytes=%d want_bytes=%d", index, len(fixture), len(got), len(want))
		}
		if bytes.Equal(fixture, want) {
			unchanged++
		} else {
			changed++
		}
		if again := normalizeAntigravityGeminiFunctionResponseRoles(got); !bytes.Equal(again, got) {
			t.Fatalf("case %d is not idempotent", index)
		}
	}
	if changed == 0 || unchanged == 0 {
		t.Fatalf("degenerate fixtures: changed=%d unchanged=%d", changed, unchanged)
	}
}

func randomAntigravityFunctionHistory(randomSource *rand.Rand) []byte {
	contents := make([]any, 0)
	groupCount := 1 + randomSource.Intn(6)
	for groupIndex := range groupCount {
		partCount := 1 + randomSource.Intn(4)
		calls := make([]any, 0, partCount)
		responses := make([]any, 0, partCount)
		for partIndex := range partCount {
			id := fmt.Sprintf("call-%d-%d", groupIndex, partIndex)
			if randomSource.Intn(5) == 0 {
				id = ""
			}
			name := fmt.Sprintf("tool-%d", partIndex)
			call := map[string]any{"name": name, "args": map[string]any{"value": partIndex}}
			response := map[string]any{"name": name, "response": map[string]any{"value": partIndex}}
			if id != "" {
				call["id"] = id
				response["id"] = id
			}
			if id != "" {
				switch randomSource.Intn(10) {
				case 0:
					response["name"] = "unknown"
				case 1:
					response["name"] = ""
				case 2:
					delete(response, "name")
				}
			}
			calls = append(calls, map[string]any{"functionCall": call})
			responses = append(responses, map[string]any{"functionResponse": response})
		}
		contents = append(contents, map[string]any{"role": "model", "parts": calls})
		if randomSource.Intn(10) == 0 {
			contents = append(contents, map[string]any{"role": "user", "parts": []any{}})
		}
		permutation := randomSource.Perm(len(responses))
		orderedResponses := make([]any, 0, len(responses))
		for _, responseIndex := range permutation {
			orderedResponses = append(orderedResponses, responses[responseIndex])
		}
		responseContent := map[string]any{"parts": orderedResponses}
		switch randomSource.Intn(4) {
		case 0:
			responseContent["role"] = "model"
		case 1:
			responseContent["role"] = "user"
		case 2:
			responseContent["role"] = " Model "
		}
		contents = append(contents, responseContent)
	}
	payload, errMarshal := json.Marshal(map[string]any{"request": map[string]any{"contents": contents}})
	if errMarshal != nil {
		panic(errMarshal)
	}
	return payload
}

func TestRepairAntigravityGeminiFunctionResponseNamesMatchesLegacy(t *testing.T) {
	fixtures := [][]byte{
		[]byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"read"}}]},{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"unknown","response":{"ok":true}}}]}]}}`),
		[]byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"read"}}]},{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"","response":{"ok":true}}}]}]}}`),
		[]byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"read"}}]},{"role":"user","parts":[{"functionResponse":{"id":"call-1","response":{"ok":true}}}]}]}}`),
		[]byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"read"}}]},{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"read","response":{"ok":true}}}]}]}}`),
		[]byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"read"}}]},{"role":"user","parts":[{"functionResponse":{"name":"unknown","response":{"ok":true}}}]}]}}`),
		[]byte(`{"request":{"contents":[{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"unknown","response":{"ok":true}}}]}]}}`),
		[]byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"read"}},{"functionCall":{"id":"call-2","name":"write"}}]},{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"unknown"}},{"functionResponse":{"id":"call-2","name":""}}]}]}}`),
		[]byte("{\r\n  \"request\" : {\r\n    \"contents\" : [\r\n      {\"role\":\"model\",\"parts\":[{\"functionCall\":{\"id\":\"a\",\"name\":\"one\"}}]},\r\n      {\"role\" : \"user\", \"parts\" : [ { \"functionResponse\" : {\"id\":\"a\",\"name\":\"unknown\"} } ]}\r\n    ]\r\n  }\r\n}"),
		[]byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"a","name":"unknown"}}]},{"role":"user","parts":[{"functionResponse":{"id":"a","name":"unknown"}}]}]}}`),
		[]byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"a","name":"one"}}]},{"role":"user","parts":[{"functionResponse":{"id":"a","name":"one"}}]}]`),
	}

	randomSource := rand.New(rand.NewSource(0xA617A5))
	for range 1_000 {
		fixtures = append(fixtures, randomAntigravityFunctionHistory(randomSource))
	}

	changed := 0
	unchanged := 0
	for index, fixture := range fixtures {
		want := legacyRepairAntigravityGeminiFunctionResponseNames(fixture)
		got := repairAntigravityGeminiFunctionResponseNames(fixture)
		if !bytes.Equal(got, want) {
			t.Fatalf("case %d differs: input_bytes=%d got_bytes=%d want_bytes=%d got=%s want=%s", index, len(fixture), len(got), len(want), got, want)
		}
		if bytes.Equal(fixture, want) {
			unchanged++
		} else {
			changed++
		}
		if again := repairAntigravityGeminiFunctionResponseNames(got); !bytes.Equal(again, got) {
			t.Fatalf("case %d is not idempotent", index)
		}
	}
	if changed == 0 || unchanged == 0 {
		t.Fatalf("degenerate fixtures: changed=%d unchanged=%d", changed, unchanged)
	}
}

func TestRepairAntigravityGeminiFunctionResponseNames(t *testing.T) {
	tests := []struct {
		name          string
		payload       string
		wantName      map[string]string
		wantUnchanged bool
	}{
		{
			name:     "empty name repaired",
			payload:  `{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"read"}}]},{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"","response":{"ok":true}}}]}]}}`,
			wantName: map[string]string{"request.contents.1.parts.0.functionResponse.name": "read"},
		},
		{
			name:     "unknown name repaired",
			payload:  `{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"read"}}]},{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"unknown","response":{"ok":true}}}]}]}}`,
			wantName: map[string]string{"request.contents.1.parts.0.functionResponse.name": "read"},
		},
		{
			name:     "missing name field repaired",
			payload:  `{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"read"}}]},{"role":"user","parts":[{"functionResponse":{"id":"call-1","response":{"ok":true}}}]}]}}`,
			wantName: map[string]string{"request.contents.1.parts.0.functionResponse.name": "read"},
		},
		{
			name:          "good name left alone",
			payload:       `{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"read"}}]},{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"already_good","response":{"ok":true}}}]}]}}`,
			wantName:      map[string]string{"request.contents.1.parts.0.functionResponse.name": "already_good"},
			wantUnchanged: true,
		},
		{
			name:          "missing id skipped",
			payload:       `{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"read"}}]},{"role":"user","parts":[{"functionResponse":{"name":"unknown","response":{"ok":true}}}]}]}}`,
			wantName:      map[string]string{"request.contents.1.parts.0.functionResponse.name": "unknown"},
			wantUnchanged: true,
		},
		{
			name:    "multiple repairs in one request",
			payload: `{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"read"}},{"functionCall":{"id":"call-2","name":"write"}}]},{"role":"user","parts":[{"functionResponse":{"id":"call-2","name":"unknown"}},{"functionResponse":{"id":"call-1","name":""}}]}]}}`,
			wantName: map[string]string{
				"request.contents.1.parts.0.functionResponse.name": "write",
				"request.contents.1.parts.1.functionResponse.name": "read",
			},
		},
		{
			name:          "no-op when map empty",
			payload:       `{"request":{"contents":[{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"unknown","response":{"ok":true}}}]}]}}`,
			wantName:      map[string]string{"request.contents.0.parts.0.functionResponse.name": "unknown"},
			wantUnchanged: true,
		},
		{
			name:          "functionCall name unknown is not mapped",
			payload:       `{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"unknown"}}]},{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"unknown"}}]}]}}`,
			wantName:      map[string]string{"request.contents.1.parts.0.functionResponse.name": "unknown"},
			wantUnchanged: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := []byte(test.payload)
			got := repairAntigravityGeminiFunctionResponseNames(payload)
			wantLegacy := legacyRepairAntigravityGeminiFunctionResponseNames(payload)
			if !bytes.Equal(got, wantLegacy) {
				t.Fatalf("differs from legacy oracle: got=%s want=%s", got, wantLegacy)
			}
			if test.wantUnchanged && !bytes.Equal(got, payload) {
				t.Fatalf("payload changed: got=%s", got)
			}
			for path, wantName := range test.wantName {
				if gotName := gjson.GetBytes(got, path).String(); gotName != wantName {
					t.Fatalf("%s = %q, want %q; output=%s", path, gotName, wantName, got)
				}
			}
		})
	}
}

func TestApplyAntigravityIndexedEditsFallsBackWhenOffsetsInvalid(t *testing.T) {
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"unknown"}}]}]}}`)
	replacement := []byte(`{"functionResponse":{"id":"call-1","name":"read"}}`)
	edits := []antigravityContentEdit{{
		path:        "request.contents.0.parts.0",
		start:       -1,
		end:         -1,
		replacement: replacement,
	}}
	want, errSet := sjson.SetRawBytes(payload, "request.contents.0.parts.0", replacement)
	if errSet != nil {
		t.Fatal(errSet)
	}
	got := applyAntigravityIndexedEdits(payload, edits, false)
	if !bytes.Equal(got, want) {
		t.Fatalf("fallback differs: got=%s want=%s", got, want)
	}
}

func TestApplyAntigravityContentEditsWithSJSONFallback(t *testing.T) {
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"read"}}]}]}}`)
	replacement := []byte(`{"role":"model","parts":[{"functionResponse":{"id":"call-1","name":"read"}}]}`)
	edits := []antigravityContentEdit{{
		index:       0,
		start:       -1,
		end:         -1,
		replacement: replacement,
	}}
	want, errSet := sjson.SetRawBytes(payload, "request.contents.0", replacement)
	if errSet != nil {
		t.Fatal(errSet)
	}
	got := applyAntigravityContentEditsWithSJSON(payload, edits)
	if !bytes.Equal(got, want) {
		t.Fatalf("fallback differs: got=%s want=%s", got, want)
	}
}

func TestAntigravityProvenanceScansMatchLegacyArraySemantics(t *testing.T) {
	reservedID := util.GeminiClaudeToolUseID("native-call", "read", `{}`)
	if reservedID == "" {
		t.Fatal("failed to build reserved provenance ID")
	}

	fixtures := [][]byte{
		[]byte(`{"request":{"contents":[{"parts":{"functionCall":{"id":"` + reservedID + `"}}}]}}`),
		[]byte(`{"request":{"contents":[{"parts":[{"functionCall":{"id":"` + reservedID + `"}}]}]}}`),
		[]byte(`{"request":{"contents":[{"parts":null}]}}`),
		[]byte(`{"request":{"contents":[{}]}}`),
		[]byte(`{"request":{"contents":[{"parts":"scalar"}]}}`),
	}
	for fixtureIndex, fixture := range fixtures {
		wantCount := legacyAntigravityCountClaudeToolProvenanceIDs(fixture)
		if gotCount := antigravityCountClaudeToolProvenanceIDs(fixture); gotCount != wantCount {
			t.Errorf("case %d count = %d, want legacy %d", fixtureIndex, gotCount, wantCount)
		}
		wantFound := legacyAntigravityPayloadHasClaudeToolProvenanceID(fixture)
		if gotFound := antigravityPayloadHasClaudeToolProvenanceID(fixture); gotFound != wantFound {
			t.Errorf("case %d found = %t, want legacy %t", fixtureIndex, gotFound, wantFound)
		}
	}
}

func TestSanitizeAntigravityRequestSchemasMatchesLegacy(t *testing.T) {
	fixtures := []string{
		sanitizeTestPayload,
		"{\r\n  \"request\" : {\r\n    \"contents\" : [{\"role\":\"user\",\"parts\":[{\"text\":\"keep formatting\"}]}],\r\n    \"tools\" : [{\"functionDeclarations\":[{\"name\":\"t\",\"parametersJsonSchema\":{\"type\":\"object\",\"title\":\"drop\",\"properties\":{\"x\":{\"type\":\"string\",\"minLength\":1}}}}]}]\r\n  }\r\n}",
		`{"request":{"tools":[{"functionDeclarations":[{"name":"t","parameters":{"type":"object","$id":"drop"},"parametersJsonSchema":{"type":"object","properties":{"x":{"type":"string"}}}}]}]}}`,
		`{"request":{"tools":[{"function_declarations":[{"name":"t","parameters_json_schema":{"type":"object","title":"drop","properties":{"x":{"type":"string"}}},"responseJsonSchema":{"type":"object","$comment":"drop"}}]}]}}`,
		`{"request":{"generationConfig":{"responseSchema":{"type":"object","$id":"drop-a"},"response_schema":{"type":"object","$id":"drop-b"}},"generation_config":{"responseJsonSchema":{"type":"object","$comment":"drop-c"}}}}`,
		`{"request":{"tools":[{"functionDeclarations":[{"name":"t","parameters":{"type":"object","title":"drop"}}]}],"tools":[{"functionDeclarations":[{"name":"duplicate","parameters":{"type":"object","title":"drop-too"}}]}]}}`,
		`{"request":{"generationConfig":{"responseSchema":{"type":"object","$id":"first"},"responseSchema":{"type":"object","$id":"second"}}}}`,
		`{"request":{"tools":[{"functionDeclarations":[{"name":"t","parameters":{"type":"object","title":"drop"}}]}]`,
		`{"prefix":broken,"request":{"tools":[{"functionDeclarations":[{"name":"t","parameters":{"type":"object","title":"drop"}}]}]}}`,
	}

	randomSource := rand.New(rand.NewSource(0x5C4E6A))
	for range 600 {
		fixtures = append(fixtures, randomAntigravitySchemaRequest(randomSource))
	}

	changed := 0
	for fixtureIndex, fixture := range fixtures {
		for _, useAntigravitySchema := range []bool{false, true} {
			want := legacySanitizeAntigravityRequestSchemas(fixture, useAntigravitySchema)
			got := sanitizeAntigravityRequestSchemas(fixture, useAntigravitySchema)
			if got != want {
				t.Fatalf("case %d antigravity=%t differs: input_bytes=%d got_bytes=%d want_bytes=%d", fixtureIndex, useAntigravitySchema, len(fixture), len(got), len(want))
			}
			if got != fixture {
				changed++
			}
		}
	}
	if changed == 0 {
		t.Fatal("degenerate schema fixtures: no rewrite occurred")
	}
}

func randomAntigravitySchemaRequest(randomSource *rand.Rand) string {
	declarationContainer := "functionDeclarations"
	if randomSource.Intn(2) == 0 {
		declarationContainer = "function_declarations"
	}
	declarationCount := 1 + randomSource.Intn(4)
	declarations := make([]any, 0, declarationCount)
	for declarationIndex := range declarationCount {
		schema := map[string]any{
			"type":  "object",
			"title": fmt.Sprintf("drop-%d", declarationIndex),
			"properties": map[string]any{
				"value": map[string]any{"type": "string", "minLength": 1 + randomSource.Intn(4)},
			},
		}
		if randomSource.Intn(3) == 0 {
			schema["required"] = []string{"value"}
		}
		key := antigravityDeclarationSchemaKeys[randomSource.Intn(len(antigravityDeclarationSchemaKeys))]
		declarations = append(declarations, map[string]any{
			"name": fmt.Sprintf("tool-%d", declarationIndex),
			key:    schema,
		})
	}
	request := map[string]any{
		"contents": []any{map[string]any{
			"role": "model",
			"parts": []any{map[string]any{"functionCall": map[string]any{
				"name": "history",
				"args": map[string]any{"title": "keep", "format": "keep"},
			}}},
		}},
		"tools": []any{map[string]any{declarationContainer: declarations}},
	}
	if randomSource.Intn(2) == 0 {
		generationContainer := "generationConfig"
		if randomSource.Intn(2) == 0 {
			generationContainer = "generation_config"
		}
		generationKey := antigravityGenerationSchemaKeys[randomSource.Intn(len(antigravityGenerationSchemaKeys))]
		request[generationContainer] = map[string]any{
			generationKey: map[string]any{
				"type": "object",
				"$id":  "drop",
				"properties": map[string]any{
					"result": map[string]any{"type": "string"},
				},
			},
		}
	}
	payload, errMarshal := json.Marshal(map[string]any{"request": request})
	if errMarshal != nil {
		panic(errMarshal)
	}
	return string(payload)
}

var antigravityNameRepairBenchmarkOutput []byte

func BenchmarkRepairAntigravityGeminiFunctionResponseNames(b *testing.B) {
	payload := syntheticAntigravityNameRepairBenchmarkPayload(1<<20, 32)
	b.Run("legacy", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(payload)))
		for b.Loop() {
			antigravityNameRepairBenchmarkOutput = legacyRepairAntigravityGeminiFunctionResponseNames(payload)
		}
	})
	b.Run("batched", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(payload)))
		for b.Loop() {
			antigravityNameRepairBenchmarkOutput = repairAntigravityGeminiFunctionResponseNames(payload)
		}
	})
}

func syntheticAntigravityNameRepairBenchmarkPayload(inlineBytes, turns int) []byte {
	var payload strings.Builder
	payload.Grow(inlineBytes + turns*320)
	payload.WriteString(`{"request":{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"application/octet-stream","data":"`)
	payload.WriteString(strings.Repeat("a", inlineBytes))
	payload.WriteString(`"}}]}`)
	for turn := range turns {
		fmt.Fprintf(
			&payload,
			`,{"role":"model","parts":[{"functionCall":{"id":"call-%d","name":"lookup","args":{"turn":%d}}}]}`,
			turn,
			turn,
		)
		fmt.Fprintf(
			&payload,
			`,{"role":"user","parts":[{"functionResponse":{"id":"call-%d","name":"unknown","response":{"result":"ok"}}}]}`,
			turn,
		)
	}
	payload.WriteString(`]}}`)
	return []byte(payload.String())
}
