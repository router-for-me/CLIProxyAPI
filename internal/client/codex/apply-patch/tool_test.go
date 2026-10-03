package applypatch

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestApplyPatchToolContract(t *testing.T) {
	tool := gjson.Parse(`{"type":"custom","name":"apply_patch","description":"This is a FREEFORM tool, so do not wrap the patch in JSON.","format":{"type":"grammar","syntax":"lark","definition":"start: begin_patch hunk+ end_patch"}}`)
	if !IsCustomTool(tool) {
		t.Fatal("custom apply_patch was not recognized")
	}
	params := gjson.ParseBytes(Parameters())
	if params.Get("properties.input.type").String() != "string" || params.Get("additionalProperties").Bool() {
		t.Fatalf("invalid schema: %s", Parameters())
	}
	description := Description(tool)
	for _, token := range []string{"*** Begin Patch", "*** End Patch", "*** Add File:", "*** Delete File:", "*** Update File:", "@@", "input", "start: begin_patch hunk+ end_patch"} {
		if !strings.Contains(description, token) {
			t.Fatalf("missing format instruction %q", token)
		}
	}
	if strings.Contains(description, "do not wrap the patch in JSON") {
		t.Fatal("freeform wrapper instruction leaked")
	}
	patch := "*** Begin Patch\n*** Add File: a.txt\n+hello\n*** End Patch\n"
	got, errUnwrap := UnwrapInput(WrapInput(patch))
	if errUnwrap != nil || got != patch {
		t.Fatalf("round trip: %q, %v", got, errUnwrap)
	}
}

func TestApplyPatchToolRecognition(t *testing.T) {
	tests := []struct {
		name string
		tool string
		want bool
	}{
		{name: "custom", tool: `{"type":"custom","name":"apply_patch"}`, want: true},
		{name: "trimmed_name", tool: `{"type":"custom","name":" \tapply_patch\n"}`, want: true},
		{name: "ordinary_function", tool: `{"type":"function","name":"apply_patch"}`},
		{name: "nested_function", tool: `{"type":"function","function":{"name":"apply_patch"}}`},
		{name: "other_custom_tool", tool: `{"type":"custom","name":"shell"}`},
		{name: "case_sensitive_name", tool: `{"type":"custom","name":"APPLY_PATCH"}`},
		{name: "qualified_name", tool: `{"type":"custom","name":"functions.apply_patch"}`},
		{name: "exact_type", tool: `{"type":" custom ","name":"apply_patch"}`},
		{name: "missing_type", tool: `{"name":"apply_patch"}`},
		{name: "missing_name", tool: `{"type":"custom"}`},
		{name: "non_string_type", tool: `{"type":true,"name":"apply_patch"}`},
		{name: "non_string_name", tool: `{"type":"custom","name":42}`},
		{name: "null_name", tool: `{"type":"custom","name":null}`},
		{name: "empty_object", tool: `{}`},
		{name: "missing_tool", tool: ``},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsCustomTool(gjson.Parse(test.tool)); got != test.want {
				t.Fatalf("IsCustomTool(%s) = %v, want %v", test.tool, got, test.want)
			}
		})
	}
}

func TestApplyPatchToolParameters(t *testing.T) {
	parameters := Parameters()
	if !gjson.ValidBytes(parameters) {
		t.Fatalf("invalid JSON schema: %s", parameters)
	}
	schema := gjson.ParseBytes(parameters)
	if schema.Get("type").String() != "object" {
		t.Fatalf("schema must describe an object: %s", parameters)
	}
	properties := schema.Get("properties").Map()
	if len(properties) != 1 || properties["input"].Get("type").String() != "string" {
		t.Fatalf("schema must expose only a string input: %s", parameters)
	}
	if properties["input"].Get("description").String() != "The complete apply_patch patch text." {
		t.Fatalf("schema does not describe the complete patch input: %s", parameters)
	}
	required := schema.Get("required").Array()
	if len(required) != 1 || required[0].String() != "input" {
		t.Fatalf("input must be required: %s", parameters)
	}
	additional := schema.Get("additionalProperties")
	if additional.Type != gjson.False {
		t.Fatalf("additional properties must be explicitly forbidden: %s", parameters)
	}
}

func TestApplyPatchToolParametersAreIndependent(t *testing.T) {
	first := Parameters()
	second := Parameters()
	first[0] = '['
	if second[0] != '{' || !gjson.ValidBytes(second) {
		t.Fatalf("mutating one schema changed another: %s", second)
	}
	third := Parameters()
	if third[0] != '{' || !gjson.ValidBytes(third) {
		t.Fatalf("mutating one schema changed later calls: %s", third)
	}
}

func TestApplyPatchToolDescriptionRules(t *testing.T) {
	tests := []struct {
		name string
		tool string
	}{
		{name: "no_description_or_grammar", tool: `{"type":"custom","name":"apply_patch"}`},
		{name: "empty_description_and_grammar", tool: `{"description":"","format":{"definition":""}}`},
		{name: "format_without_definition", tool: `{"description":"Edit files.","format":{"type":"grammar","syntax":"lark"}}`},
		{name: "grammar_without_environment", tool: `{"format":{"definition":"start: begin_patch hunk+ end_patch"}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			description := Description(gjson.Parse(test.tool))
			for _, instruction := range []string{
				"Call this function with a JSON object whose input field contains the complete patch text.",
				"Use the Codex apply_patch format, not a conventional git unified diff.",
				"Start with *** Begin Patch and end with *** End Patch.",
				"Use *** Add File: path, *** Delete File: path, or *** Update File: path.",
				"Every added-file content line starts with +.",
				"For updates, use @@; context lines start with one space, removed lines with -, and added lines with +.",
				"Use *** Move to: path for a rename and *** End of File when required by the patch grammar.",
				"Example input:\n*** Begin Patch\n*** Update File: src/main.go\n@@\n-old\n+new\n*** End Patch",
			} {
				if !strings.Contains(description, instruction) {
					t.Errorf("missing instruction %q in %q", instruction, description)
				}
			}
			if strings.Contains(description, "*** Environment ID:") {
				t.Fatalf("environment syntax invented without an environment grammar: %q", description)
			}
		})
	}
}

func TestApplyPatchToolDescriptionPreservesNonWrapperInstructions(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		retained []string
	}{
		{
			name:     "non_wrapper_description",
			tool:     `{"description":"Use apply_patch to edit files.\n  Keep context lines intact.\nDo not run the patch on the proxy."}`,
			retained: []string{"Use apply_patch to edit files.\n  Keep context lines intact.\nDo not run the patch on the proxy."},
		},
		{
			name:     "wrapper_between_instructions",
			tool:     `{"description":"Edit files only. This is a FREEFORM tool, so do not wrap the patch in JSON. Preserve line endings.\n  Keep indentation."}`,
			retained: []string{"Edit files only.", "Preserve line endings.\n  Keep indentation."},
		},
		{
			name: "wrapper_only",
			tool: `{"description":"This is a FREEFORM tool, so do not wrap the patch in JSON."}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tool := gjson.Parse(test.tool)
			description := Description(tool)
			if strings.Contains(description, "This is a FREEFORM tool") || strings.Contains(description, "do not wrap the patch in JSON") {
				t.Fatalf("freeform wrapper instruction leaked: %q", description)
			}
			for _, retained := range test.retained {
				if !strings.Contains(description, retained) {
					t.Errorf("lost non-wrapper instruction %q in %q", retained, description)
				}
			}
			if tool.Raw != test.tool {
				t.Fatalf("description generation mutated the original tool: %s", tool.Raw)
			}
		})
	}
}

func TestApplyPatchToolDescriptionPreservesGrammar(t *testing.T) {
	tests := []struct {
		name        string
		tool        string
		wantGrammar string
	}{
		{
			name:        "whitespace_and_line_endings",
			tool:        `{"format":{"definition":" \nstart: begin_patch hunk+ end_patch\r\n  begin_patch: \"*** Begin Patch\"\n\n"}}`,
			wantGrammar: " \nstart: begin_patch hunk+ end_patch\r\n  begin_patch: \"*** Begin Patch\"\n\n",
		},
		{
			name:        "wrapper_text_inside_grammar_is_not_rewritten",
			tool:        `{"description":"This is a FREEFORM tool, so do not wrap the patch in JSON.","format":{"definition":"// This is a FREEFORM tool, so do not wrap the patch in JSON.\nstart: begin_patch hunk+ end_patch"}}`,
			wantGrammar: "// This is a FREEFORM tool, so do not wrap the patch in JSON.\nstart: begin_patch hunk+ end_patch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			description := Description(gjson.Parse(test.tool))
			if !strings.HasSuffix(description, test.wantGrammar) {
				t.Fatalf("original grammar was not appended verbatim: %q", description)
			}
			beforeGrammar := strings.TrimSuffix(description, test.wantGrammar)
			if strings.Contains(beforeGrammar, "do not wrap the patch in JSON") {
				t.Fatalf("wrapper text leaked outside the original grammar: %q", beforeGrammar)
			}
		})
	}
}

func TestApplyPatchToolDescriptionEnvironmentGrammar(t *testing.T) {
	tool := gjson.Parse(`{"format":{"definition":"start: begin_patch environment_id? hunk+ end_patch\nenvironment_id: \"*** Environment ID:\" /[^\\n]+/\n"}}`)
	description := Description(tool)
	grammar := "start: begin_patch environment_id? hunk+ end_patch\nenvironment_id: \"*** Environment ID:\" /[^\\n]+/\n"
	if !strings.HasSuffix(description, grammar) {
		t.Fatalf("environment grammar changed: %q", description)
	}
	instructions := strings.TrimSuffix(description, grammar)
	if !strings.Contains(instructions, "*** Environment ID:") {
		t.Fatalf("environment syntax was not explained before its grammar: %q", instructions)
	}
}

func TestApplyPatchToolInputEncoding(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantJSON    string
		wantEscaped string
	}{
		{name: "empty", input: "", wantJSON: `{"input":""}`, wantEscaped: ``},
		{name: "plain", input: "patch", wantJSON: `{"input":"patch"}`, wantEscaped: `patch`},
		{name: "newlines_and_leading_spaces", input: "  context\n+added\r\n\tline\n ", wantJSON: `{"input":"  context\n+added\r\n\tline\n "}`, wantEscaped: `  context\n+added\r\n\tline\n `},
		{name: "quotes_and_backslashes", input: "\"C:\\file\"", wantJSON: `{"input":"\"C:\\file\""}`, wantEscaped: `\"C:\\file\"`},
		{name: "control_characters", input: "\x00\b\f\r\t", wantJSON: `{"input":"\u0000\b\f\r\t"}`, wantEscaped: `\u0000\b\f\r\t`},
		{name: "unicode", input: "补丁🙂", wantJSON: `{"input":"补丁🙂"}`, wantEscaped: `补丁🙂`},
		{name: "json_sensitive_characters", input: "<>&\u2028\u2029", wantJSON: `{"input":"\u003c\u003e\u0026\u2028\u2029"}`, wantEscaped: `\u003c\u003e\u0026\u2028\u2029`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := WrapInput(test.input); got != test.wantJSON {
				t.Errorf("WrapInput(%q) = %q, want %q", test.input, got, test.wantJSON)
			}
			if got := EscapeInputFragment(test.input); got != test.wantEscaped {
				t.Errorf("EscapeInputFragment(%q) = %q, want %q", test.input, got, test.wantEscaped)
			}
			got, errUnwrap := UnwrapInput(WrapInput(test.input))
			if errUnwrap != nil || got != test.input {
				t.Fatalf("round trip = %q, %v, want %q", got, errUnwrap, test.input)
			}
		})
	}
}

func TestApplyPatchToolEscapeInputFragmentsCompose(t *testing.T) {
	fragments := []string{"*** Begin Patch\n", "  context\n", "-\"old\"\r\n", "+C:\\new\n", "+补丁🙂\n", "*** End Patch\n"}
	var encoded strings.Builder
	for _, fragment := range fragments {
		encoded.WriteString(EscapeInputFragment(fragment))
	}
	var decoded struct {
		Input string `json:"input"`
	}
	if errUnmarshal := json.Unmarshal([]byte(`{"input":"`+encoded.String()+`"}`), &decoded); errUnmarshal != nil {
		t.Fatalf("escaped fragments did not form valid JSON: %v", errUnmarshal)
	}
	want := "*** Begin Patch\n  context\n-\"old\"\r\n+C:\\new\n+补丁🙂\n*** End Patch\n"
	if decoded.Input != want {
		t.Fatalf("escaped fragments changed patch text: got %q, want %q", decoded.Input, want)
	}
}

func TestApplyPatchToolUnwrapInput(t *testing.T) {
	tests := []struct {
		name      string
		arguments string
		want      string
		wantError bool
	}{
		{name: "empty_input", arguments: `{"input":""}`, want: ""},
		{name: "plain_input", arguments: `{"input":"patch"}`, want: "patch"},
		{name: "whitespace_in_input", arguments: `{"input":"  context\n\tline\r\n\n "}`, want: "  context\n\tline\r\n\n "},
		{name: "json_whitespace", arguments: " \n\t{ \"input\" : \"patch\" }\r\n ", want: "patch"},
		{name: "escaped_field_name", arguments: `{"\u0069nput":"patch"}`, want: "patch"},
		{name: "unicode_input", arguments: `{"input":"补丁🙂"}`, want: "补丁🙂"},
		{name: "empty_arguments", arguments: "", wantError: true},
		{name: "whitespace_arguments", arguments: " \r\n\t ", wantError: true},
		{name: "missing_input", arguments: `{}`, wantError: true},
		{name: "wrong_field", arguments: `{"patch":"text"}`, wantError: true},
		{name: "case_sensitive_field", arguments: `{"Input":"text"}`, wantError: true},
		{name: "null_input", arguments: `{"input":null}`, wantError: true},
		{name: "boolean_input", arguments: `{"input":true}`, wantError: true},
		{name: "number_input", arguments: `{"input":1}`, wantError: true},
		{name: "array_input", arguments: `{"input":[]}`, wantError: true},
		{name: "object_input", arguments: `{"input":{}}`, wantError: true},
		{name: "extra_field_after_input", arguments: `{"input":"patch","other":"value"}`, wantError: true},
		{name: "extra_field_before_input", arguments: `{"other":"value","input":"patch"}`, wantError: true},
		{name: "duplicate_input", arguments: `{"input":"first","input":"second"}`, wantError: true},
		{name: "duplicate_identical_input", arguments: `{"input":"patch","input":"patch"}`, wantError: true},
		{name: "duplicate_escaped_input", arguments: `{"input":"first","\u0069nput":"second"}`, wantError: true},
		{name: "trailing_object", arguments: `{"input":"patch"}{"input":"second"}`, wantError: true},
		{name: "trailing_array", arguments: `{"input":"patch"} []`, wantError: true},
		{name: "trailing_null", arguments: `{"input":"patch"} null`, wantError: true},
		{name: "trailing_boolean", arguments: `{"input":"patch"} true`, wantError: true},
		{name: "trailing_garbage", arguments: `{"input":"patch"} invalid`, wantError: true},
		{name: "top_level_array", arguments: `["patch"]`, wantError: true},
		{name: "top_level_string", arguments: `"patch"`, wantError: true},
		{name: "top_level_null", arguments: `null`, wantError: true},
		{name: "top_level_number", arguments: `1`, wantError: true},
		{name: "top_level_boolean", arguments: `false`, wantError: true},
		{name: "raw_patch", arguments: "*** Begin Patch\n*** End Patch\n", wantError: true},
		{name: "missing_closing_brace", arguments: `{"input":"patch"`, wantError: true},
		{name: "missing_colon", arguments: `{"input" "patch"}`, wantError: true},
		{name: "missing_value", arguments: `{"input":}`, wantError: true},
		{name: "unterminated_string", arguments: `{"input":"patch}`, wantError: true},
		{name: "trailing_comma", arguments: `{"input":"patch",}`, wantError: true},
		{name: "unquoted_field", arguments: `{input:"patch"}`, wantError: true},
		{name: "unescaped_newline", arguments: "{\"input\":\"line\nline\"}", wantError: true},
		{name: "invalid_escape", arguments: `{"input":"\x"}`, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, errUnwrap := UnwrapInput(test.arguments)
			if test.wantError {
				if errUnwrap == nil {
					t.Fatalf("UnwrapInput(%q) accepted an invalid wrapper: %q", test.arguments, got)
				}
				if got != "" {
					t.Fatalf("invalid wrapper returned input or fallback text: %q", got)
				}
				return
			}
			if errUnwrap != nil || got != test.want {
				t.Fatalf("UnwrapInput(%q) = %q, %v, want %q", test.arguments, got, errUnwrap, test.want)
			}
		})
	}
}
