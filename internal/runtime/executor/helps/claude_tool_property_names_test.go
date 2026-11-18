package helps

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestClaudePropertyKeyAccepted(t *testing.T) {
	for name, want := range map[string]bool{
		"state":                 true,
		"user.name":             true,
		"a-b_c":                 true,
		strings.Repeat("x", 64): true,
		strings.Repeat("x", 65): false,
		"":                      false,
		"not[labels]":           false,
		"$schema":               false,
		"a b":                   false,
		"名":                     false,
	} {
		if got := claudePropertyKeyAccepted(name); got != want {
			t.Errorf("claudePropertyKeyAccepted(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestNormalizeClaudeToolPropertyNamesDocEmbedsRejectedSchemas(t *testing.T) {
	rejectedSchema := `{"type":"object","properties":{"not[labels]":{"type":"string"},"state":{"type":"string"}},"required":["not[labels]"]}`
	nestedSchema := `{"type":"object","$defs":{"f":{"type":"object","properties":{"$schema":{"type":"string"}}}},"properties":{"filter":{"$ref":"#/$defs/f"}}}`
	inlineSchema := `{"type":"object","properties":{"items":{"type":"array","items":{"type":"object","properties":{"id[]":{"type":"string"}}}}}}`
	propertyNamedDefault := `{"type":"object","properties":{"default":{"type":"object","properties":{"a b":{"type":"string"}}}}}`
	instanceOnly := `{"type":"object","properties":{"q":{"type":"object","default":{"properties":{"a b":1}},"examples":[{"properties":{"c d":2}}],"const":{"properties":{"e f":3}},"enum":[{"properties":{"g h":4}}]}}}`
	validSchema := `{"type":"object","properties":{"user.name":{"type":"string"}}}`
	body := `{"tools":[` +
		`{"name":"list_merge_requests","description":"List merge requests.","strict":true,"cache_control":{"type":"ephemeral"},"input_schema":` + rejectedSchema + `,"input_examples":[{"not[labels]":"bug"}]},` +
		`{"name":"nested","input_schema":` + nestedSchema + `},` +
		`{"name":"property_named_default","description":"Default.","input_schema":` + propertyNamedDefault + `},` +
		`{"name":"instance_only","description":"Data.","input_schema":` + instanceOnly + `},` +
		`{"name":"valid","description":"Valid.","strict":true,"input_schema":` + validSchema + `},` +
		`{"type":"web_search_20250305","name":"web_search","max_uses":1}],` +
		`"messages":[` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"list_merge_requests","input":{"not[labels]":"bug"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"[]"}]},` +
		`{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"inline_tool","description":"Inline.","input_schema":` + inlineSchema + `}}}]}]}`
	doc := ojson.NewDocString(body)
	NormalizeClaudeToolPropertyNamesDoc(doc)
	out := doc.Bytes()

	fallbacks := map[string]string{
		"tools.0":                              "List merge requests.\n\n" + claudeToolSchemaFallbackPrefix + rejectedSchema,
		"tools.1":                              claudeToolSchemaFallbackPrefix + nestedSchema,
		"tools.2":                              "Default.\n\n" + claudeToolSchemaFallbackPrefix + propertyNamedDefault,
		"messages.2.content.0.tool.definition": "Inline.\n\n" + claudeToolSchemaFallbackPrefix + inlineSchema,
	}
	for path, description := range fallbacks {
		if got := ojson.GetBytes(out, path+".input_schema").Raw; got != claudeToolSchemaFallbackSchema {
			t.Errorf("%s.input_schema = %s, want %s", path, got, claudeToolSchemaFallbackSchema)
		}
		if got := ojson.GetBytes(out, path+".description").String(); got != description {
			t.Errorf("%s.description = %q, want %q", path, got, description)
		}
		if ojson.GetBytes(out, path+".strict").Exists() {
			t.Errorf("%s.strict must be removed", path)
		}
	}
	unchanged := map[string]string{
		"tools.0.cache_control":      `{"type":"ephemeral"}`,
		"tools.0.input_examples":     `[{"not[labels]":"bug"}]`,
		"tools.3":                    `{"name":"instance_only","description":"Data.","input_schema":` + instanceOnly + `}`,
		"tools.4":                    `{"name":"valid","description":"Valid.","strict":true,"input_schema":` + validSchema + `}`,
		"tools.5":                    `{"type":"web_search_20250305","name":"web_search","max_uses":1}`,
		"messages.0.content.0.input": `{"not[labels]":"bug"}`,
	}
	for path, want := range unchanged {
		if got := ojson.GetBytes(out, path).Raw; got != want {
			t.Errorf("%s = %s, want %s", path, got, want)
		}
	}
}

func TestNormalizeClaudeToolPropertyNamesDocLeavesAcceptedRequestsUnchanged(t *testing.T) {
	body := `{"tools":[{"name":"t","description":"d","input_schema":{"type":"object","$defs":{"a[b]":{"type":"string"}},"patternProperties":{"^x\\[":{}},"properties":{"file.xml":{"$ref":"#/$defs/a[b]"}}}}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"t","input":{"x[1]":2}}]}]}`
	doc := ojson.NewDocString(body)
	NormalizeClaudeToolPropertyNamesDoc(doc)
	if got := doc.String(); got != body {
		t.Fatalf("body changed:\n got: %s\nwant: %s", got, body)
	}
}

func TestNormalizeClaudeToolPropertyNamesDocFallsBackForDeepSchemas(t *testing.T) {
	depth := claudeToolSchemaMaxInspectDepth + 10
	schema := strings.Repeat(`{"not":`, depth) + `{}` + strings.Repeat(`}`, depth)
	doc := ojson.NewDocString(`{"tools":[{"name":"deep","input_schema":` + schema + `}]}`)
	NormalizeClaudeToolPropertyNamesDoc(doc)
	if got := doc.Get("tools.0.input_schema").Raw; got != claudeToolSchemaFallbackSchema {
		t.Fatalf("deep input_schema = %.80s, want the fallback", got)
	}
	if !strings.HasSuffix(doc.Get("tools.0.description").String(), schema) {
		t.Fatal("deep schema must be embedded verbatim in the description")
	}
}

// Keyword names are only interpreted at schema positions; named definitions,
// dependent schemas, patterns and properties are classified by their role.
func TestClaudeSchemaHasRejectedPropertyKeyFollowsSchemaPositions(t *testing.T) {
	rejected := `{"type":"object","properties":{"not[labels]":{"type":"string"}}}`
	accepted := `{"type":"object","properties":{"value":{"type":"string"}}}`
	cases := map[string]struct {
		schema string
		want   bool
	}{
		"defs named default":                {schema: `{"$defs":{"default":` + rejected + `},"properties":{"f":{"$ref":"#/$defs/default"}}}`, want: true},
		"definitions named enum":            {schema: `{"definitions":{"enum":` + rejected + `}}`, want: true},
		"dependent schema named const":      {schema: `{"dependentSchemas":{"const":` + rejected + `}}`, want: true},
		"dependencies schema named const":   {schema: `{"dependencies":{"const":` + rejected + `,"a":["b"]}}`, want: true},
		"pattern named examples":            {schema: `{"patternProperties":{"examples":` + rejected + `}}`, want: true},
		"property named default":            {schema: `{"properties":{"default":` + rejected + `}}`, want: true},
		"property named properties":         {schema: `{"properties":{"properties":` + rejected + `}}`, want: true},
		"nested applicators":                {schema: `{"anyOf":[{"items":[{"not":{"if":{"then":{"contains":` + rejected + `}}}}]}]}`, want: true},
		"tuple items and prefix items":      {schema: `{"prefixItems":[true,{"additionalProperties":` + rejected + `}]}`, want: true},
		"defs named properties":             {schema: `{"$defs":{"text":{"type":"string"},"properties":{"$ref":"#/$defs/text"}},"properties":{"value":{"$ref":"#/$defs/properties"}},"additionalProperties":false}`, want: false},
		"property named properties valid":   {schema: `{"properties":{"properties":` + accepted + `}}`, want: false},
		"defs with rejected definition key": {schema: `{"$defs":{"a[b]":` + accepted + `}}`, want: false},
		"data keywords":                     {schema: `{"default":{"properties":{"a b":1}},"const":{"properties":{"c d":2}},"enum":[{"properties":{"e f":3}}],"examples":[{"properties":{"g h":4}}]}`, want: false},
		"unknown annotation":                {schema: `{"x-ui":{"properties":{"a b":{}}}}`, want: false},
	}
	for name, tc := range cases {
		if got := claudeSchemaHasRejectedPropertyKey(ojson.Parse(tc.schema), 0); got != tc.want {
			t.Errorf("%s: claudeSchemaHasRejectedPropertyKey = %v, want %v", name, got, tc.want)
		}
	}
}
