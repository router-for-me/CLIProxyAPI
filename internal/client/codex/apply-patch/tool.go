package applypatch

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
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
func IsCustomTool(tool ojson.Result) bool {
	return tool.Get("type").String() == "custom" && strings.TrimSpace(tool.Get("name").String()) == "apply_patch"
}

// Parameters returns an independent copy of the patch input schema.
func Parameters() []byte {
	return []byte(parametersJSON)
}

// Description explains the JSON wrapper and preserves the original patch grammar.
func Description(tool ojson.Result) string {
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

	valueOffset := decoder.InputOffset()
	token, errToken = decoder.Token()
	if errToken != nil {
		if errString := inputStringError(arguments[valueOffset:]); errString != nil {
			return "", errString
		}
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

// Malformed input strings get the streaming decoder's messages; Go's own JSON error text differs between releases.
var (
	errInputControlCharacter = errors.New("invalid control character in apply_patch input")
	errInputUnicodeEscape    = errors.New("invalid Unicode escape in apply_patch input")
	errInputEscape           = errors.New("invalid escape in apply_patch input")
)

// inputStringError names the first malformed character in the string value at the start of rest
// (after the input key); nil when the value is not a string or the string is well formed.
func inputStringError(rest string) error {
	rest = strings.TrimLeft(rest, " \t\n\r:")
	if !strings.HasPrefix(rest, `"`) {
		return nil
	}
	for i := 1; i < len(rest); i++ {
		c := rest[i]
		switch {
		case c == '"':
			return nil
		case c < 0x20:
			return errInputControlCharacter
		case c != '\\':
			continue
		}
		i++
		if i >= len(rest) {
			return nil
		}
		switch rest[i] {
		case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		case 'u':
			if i+4 >= len(rest) || !isHex4(rest[i+1:i+5]) {
				return errInputUnicodeEscape
			}
			i += 4
		default:
			return errInputEscape
		}
	}
	return nil
}

func isHex4(s string) bool {
	for _, c := range []byte(s) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
