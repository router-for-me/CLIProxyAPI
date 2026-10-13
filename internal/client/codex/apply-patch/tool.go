package applypatch

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/tidwall/gjson"
)

const parametersJSON = `{"type":"object","properties":{"input":{"type":"string","description":"The complete apply_patch patch text."}},"required":["input"],"additionalProperties":false}`

const patchInstructions = `Call this function with a JSON object whose input field contains the complete patch text.
Use the Codex apply_patch format, not a conventional git unified diff.
Start with *** Begin Patch and end with *** End Patch.
Use *** Add File: path, *** Delete File: path, or *** Update File: path.
Every added-file content line starts with +.
For updates, use @@; context lines start with one space, removed lines with -, and added lines with +.
Use *** Move to: path for a rename and *** End of File when required by the patch grammar.
Example input:
*** Begin Patch
*** Update File: src/main.go
@@
-old
+new
*** End Patch`

// IsCustomTool reports whether the declaration is the custom apply_patch tool.
func IsCustomTool(tool gjson.Result) bool {
	return tool.Get("type").String() == "custom" && strings.TrimSpace(tool.Get("name").String()) == "apply_patch"
}

// Parameters returns an independent copy of the patch input schema.
func Parameters() []byte {
	return []byte(parametersJSON)
}

// Description explains the JSON wrapper and preserves the original patch grammar.
func Description(tool gjson.Result) string {
	original := strings.ReplaceAll(tool.Get("description").String(), "This is a FREEFORM tool, so do not wrap the patch in JSON.", "")
	var description strings.Builder
	if strings.TrimSpace(original) != "" {
		description.WriteString(original)
		description.WriteString("\n\n")
	}
	description.WriteString(patchInstructions)
	grammar := tool.Get("format.definition").String()
	if grammar != "" {
		if strings.Contains(grammar, "*** Environment ID:") {
			description.WriteString("\n\nUse *** Environment ID: as specified by the patch grammar.")
		}
		description.WriteString("\n\nOriginal patch grammar:\n")
		description.WriteString(grammar)
	}
	return description.String()
}

// WrapInput encodes the complete patch text as function arguments.
func WrapInput(input string) string {
	body, _ := json.Marshal(struct {
		Input string `json:"input"`
	}{Input: input})
	return string(body)
}

// UnwrapInput accepts only a JSON object containing one string field named input.
func UnwrapInput(arguments string) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	token, errToken := decoder.Token()
	if errToken != nil {
		return "", fmt.Errorf("decode apply_patch arguments object: %w", errToken)
	}
	if token != json.Delim('{') {
		return "", fmt.Errorf("apply_patch arguments must be a JSON object")
	}

	token, errToken = decoder.Token()
	if errToken != nil {
		return "", fmt.Errorf("decode apply_patch input key: %w", errToken)
	}
	if token != "input" {
		return "", fmt.Errorf("apply_patch arguments must contain the input field")
	}

	token, errToken = decoder.Token()
	if errToken != nil {
		return "", fmt.Errorf("decode apply_patch input value: %w", errToken)
	}
	input, ok := token.(string)
	if !ok {
		return "", fmt.Errorf("apply_patch input must be a string")
	}

	token, errToken = decoder.Token()
	if errToken != nil {
		return "", fmt.Errorf("decode apply_patch arguments closing brace: %w", errToken)
	}
	if token != json.Delim('}') {
		return "", fmt.Errorf("apply_patch arguments must contain only one input field")
	}

	_, errToken = decoder.Token()
	if errToken != io.EOF {
		if errToken != nil {
			return "", fmt.Errorf("decode apply_patch arguments end: %w", errToken)
		}
		return "", fmt.Errorf("apply_patch arguments must not contain trailing JSON")
	}
	return input, nil
}

// EscapeInputFragment encodes patch text for use inside a JSON string without quotes.
func EscapeInputFragment(fragment string) string {
	encoded, _ := json.Marshal(fragment)
	return string(encoded[1 : len(encoded)-1])
}
