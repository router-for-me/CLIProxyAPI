package responses

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

type ValidationError struct {
	Message string
	Param   string
}

func (e *ValidationError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

var codexResponsesOutputTextRoles = map[string]struct{}{
	"assistant": {},
}

func ValidateCodexResponsesInput(rawJSON []byte) *ValidationError {
	input := gjson.GetBytes(rawJSON, "input")
	if !input.Exists() || input.Type == gjson.String || !input.IsArray() {
		return nil
	}
	items := input.Array()
	for i, item := range items {
		if item.Get("type").String() != "message" {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
		if role == "" {
			role = "user"
		}
		if _, ok := codexResponsesOutputTextRoles[role]; ok {
			continue
		}
		content := item.Get("content")
		if !content.Exists() || content.Type == gjson.String || !content.IsArray() {
			continue
		}
		for j, part := range content.Array() {
			if part.Get("type").String() != "output_text" {
				continue
			}
			return &ValidationError{
				Message: "Invalid value: 'output_text'. Supported values are: 'input_text', 'input_image', 'input_file', and 'scoped_content'.",
				Param:   fmt.Sprintf("input[%d].content[%d]", i, j),
			}
		}
	}
	return nil
}
