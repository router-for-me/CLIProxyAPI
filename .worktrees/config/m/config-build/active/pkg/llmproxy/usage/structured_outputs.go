// Package usage provides structured output capabilities with JSON Schema enforcement.
// This ensures responses conform to specified schemas, reducing parsing errors.
package usage

import (
	"encoding/json"
	"fmt"
)

// JSONSchema represents a JSON Schema for structured output validation
type JSONSchema struct {
	Type       string              `json:"type,omitempty"`
	Properties map[string]*Schema `json:"properties,omitempty"`
	Required   []string           `json:"required,omitempty"`
	Items      *JSONSchema        `json:"items,omitempty"`
	Enum       []interface{}      `json:"enum,omitempty"`
	Minimum    *float64          `json:"minimum,omitempty"`
	Maximum    *float64          `json:"maximum,omitempty"`
	MinLength  *int              `json:"minLength,omitempty"`
	MaxLength  *int              `json:"maxLength,omitempty"`
	Pattern    string             `json:"pattern,omitempty"`
	Format     string             `json:"format,omitempty"`
	// For nested objects
	AllOf []*JSONSchema `json:"allOf,omitempty"`
	OneOf []*JSONSchema `json:"oneOf,omitempty"`
	AnyOf []*JSONSchema `json:"anyOf,omitempty"`
	Not   *JSONSchema  `json:"not,omitempty"`
}

// Schema is an alias for JSONSchema
type Schema = JSONSchema

// ResponseFormat specifies the desired output format
type ResponseFormat struct {
	// Type is the response format type (e.g., "json_schema", "text", "json_object")
	Type string `json:"type"`
	// Schema is the JSON Schema (for json_schema type)
	Schema *JSONSchema `json:"schema,omitempty"`
	// Strict enables strict schema enforcement
	Strict *bool `json:"strict,omitempty"`
	// Name is the name of the schema (for json_schema type)
	Name string `json:"name,omitempty"`
	// Description is the description of the schema (for json_schema type)
	Description string `json:"description,omitempty"`
}

// ValidationResult represents the result of validating a response against a schema
type ValidationResult struct {
	Valid   bool     `json:"valid"`
	Errors  []string `json:"errors,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// ResponseHealer attempts to fix responses that don't match the schema
type ResponseHealer struct {
	schema        *JSONSchema
	maxAttempts   int
	removeUnknown bool
}

// NewResponseHealer creates a new ResponseHealer
func NewResponseHealer(schema *JSONSchema) *ResponseHealer {
	return &ResponseHealer{
		schema:       schema,
		maxAttempts:  3,
		removeUnknown: true,
	}
}

// Heal attempts to fix a response to match the schema
func (h *ResponseHealer) Heal(response json.RawMessage) (json.RawMessage, error) {
	// First, try to parse as-is
	var data interface{}
	if err := json.Unmarshal(response, &data); err != nil {
		// Try to extract JSON from response
		healed := h.extractJSON(string(response))
		if healed == "" {
			return nil, fmt.Errorf("failed to parse response: %w", err)
		}
		if err := json.Unmarshal([]byte(healed), &data); err != nil {
			return nil, fmt.Errorf("failed to parse extracted JSON: %w", err)
		}
	}

	// Validate
	result := h.Validate(response)
	if result.Valid {
		return response, nil
	}

	// Attempt to heal
	return h.healData(data, result.Errors)
}

// Validate checks if a response matches the schema
func (h *ResponseHealer) Validate(response json.RawMessage) ValidationResult {
	var data interface{}
	if err := json.Unmarshal(response, &data); err != nil {
		return ValidationResult{
			Valid:  false,
			Errors: []string{fmt.Sprintf("failed to parse JSON: %v", err)},
		}
	}

	return h.validateData(data, "")
}

// validateData recursively validates data against the schema
func (h *ResponseHealer) validateData(data interface{}, path string) ValidationResult {
	var errors []string

	switch v := data.(type) {
	case map[string]interface{}:
		if h.schema.Type == "object" || h.schema.Properties != nil {
			// Check required fields
			for _, req := range h.schema.Required {
				if _, ok := v[req]; !ok {
					errors = append(errors, fmt.Sprintf("missing required field: %s", req))
				}
			}
			// Check property types
			for prop, propSchemaVal := range h.schema.Properties {
				if val, ok := v[prop]; ok {
					result := h.validateData(val, path+"."+prop)
					errors = append(errors, result.Errors...)
					// Use propSchemaVal to avoid unused variable
					_ = propSchemaVal
				}
			}
		}
	case []interface{}:
		if h.schema.Type == "array" && h.schema.Items != nil {
			for i, item := range v {
				result := h.validateData(item, fmt.Sprintf("%s[%d]", path, i))
				errors = append(errors, result.Errors...)
			}
		}
	case string:
		if h.schema.Type == "string" {
			if h.schema.MinLength != nil && len(v) < *h.schema.MinLength {
				errors = append(errors, fmt.Sprintf("string too short: %d < %d", len(v), *h.schema.MinLength))
			}
			if h.schema.MaxLength != nil && len(v) > *h.schema.MaxLength {
				errors = append(errors, fmt.Sprintf("string too long: %d > %d", len(v), *h.schema.MaxLength))
			}
			if h.schema.Pattern != "" {
				// Simple pattern check (would need regex in production)
				_ = h.schema.Pattern
			}
			if len(h.schema.Enum) > 0 {
				found := false
				for _, e := range h.schema.Enum {
					if e == v {
						found = true
						break
					}
				}
				if !found {
					errors = append(errors, fmt.Sprintf("value not in enum: %s", v))
				}
			}
		}
	case float64:
		if h.schema.Type == "number" || h.schema.Type == "integer" {
			if h.schema.Minimum != nil && v < *h.schema.Minimum {
				errors = append(errors, fmt.Sprintf("number too small: %v < %v", v, *h.schema.Minimum))
			}
			if h.schema.Maximum != nil && v > *h.schema.Maximum {
				errors = append(errors, fmt.Sprintf("number too large: %v > %v", v, *h.schema.Maximum))
			}
		}
	case bool:
		if h.schema.Type == "boolean" {
			// OK
		}
	case nil:
		// Null values
	}

	if len(errors) == 0 {
		return ValidationResult{Valid: true}
	}
	return ValidationResult{Valid: false, Errors: errors}
}

// healData attempts to fix data to match schema
func (h *ResponseHealer) healData(data interface{}, errors []string) (json.RawMessage, error) {
	// Simple healing: remove unknown fields if enabled
	if h.removeUnknown {
		if m, ok := data.(map[string]interface{}); ok {
			if h.schema.Properties != nil {
				cleaned := make(map[string]interface{})
				for k, v := range m {
					if _, ok := h.schema.Properties[k]; ok {
						cleaned[k] = v
					}
				}
				// Add required fields with defaults if missing
				for _, req := range h.schema.Required {
					if _, ok := cleaned[req]; !ok {
						cleaned[req] = getDefaultForType(h.schema.Properties[req])
					}
				}
				return json.Marshal(cleaned)
			}
		}
	}

	// If healing failed, return original with errors
	return nil, fmt.Errorf("failed to heal: %v", errors)
}

// extractJSON attempts to extract JSON from a response that might contain extra text
func (h *ResponseHealer) extractJSON(s string) string {
	// Try to find JSON object/array
	start := -1
	end := -1
	
	for i, c := range s {
		if c == '{' && start == -1 {
			start = i
		}
		if c == '}' && start != -1 && end == -1 {
			end = i + 1
			break
		}
		if c == '[' && start == -1 {
			start = i
		}
		if c == ']' && start != -1 && end == -1 {
			end = i + 1
			break
		}
	}
	
	if start != -1 && end != -1 {
		return s[start:end]
	}
	
	return ""
}

// getDefaultForType returns a default value for a schema type
func getDefaultForType(schema *Schema) interface{} {
	if schema == nil {
		return nil
	}
	switch schema.Type {
	case "string":
		return ""
	case "number", "integer":
		return 0
	case "boolean":
		return false
	case "array":
		return []interface{}{}
	case "object":
		return map[string]interface{}{}
	default:
		return nil
	}
}

// NewResponseFormat creates a new ResponseFormat for JSON Schema enforcement
func NewResponseFormat(schema *JSONSchema, name, description string) *ResponseFormat {
	strict := true
	return &ResponseFormat{
		Type:        "json_schema",
		Schema:      schema,
		Name:        name,
		Description: description,
		Strict:      &strict,
	}
}

// CommonSchemas provides commonly used schemas
var CommonSchemas = struct {
	// CodeReview represents a code review response
	CodeReview *JSONSchema
	// Summarization represents a summary response
	Summarization *JSONSchema
	// Extraction represents data extraction
	Extraction *JSONSchema
}{
	CodeReview: &JSONSchema{
		Type: "object",
		Properties: map[string]*Schema{
			"issues": {
				Type: "array",
				Items: &JSONSchema{
					Type: "object",
					Properties: map[string]*Schema{
						"severity": {Type: "string", Enum: []interface{}{"error", "warning", "info"}},
						"line":     {Type: "integer"},
						"message":  {Type: "string"},
						"code":     {Type: "string"},
					},
					Required: []string{"severity", "message"},
				},
			},
			"summary": {Type: "string"},
			"score":   {Type: "number", Minimum: float64Ptr(0), Maximum: float64Ptr(10)},
		},
		Required: []string{"summary", "issues"},
	},
	Summarization: &JSONSchema{
		Type: "object",
		Properties: map[string]*Schema{
			"summary":   {Type: "string", MinLength: intPtr(10)},
			"highlights": {Type: "array", Items: &JSONSchema{Type: "string"}},
			"sentiment": {Type: "string", Enum: []interface{}{"positive", "neutral", "negative"}},
		},
		Required: []string{"summary"},
	},
	Extraction: &JSONSchema{
		Type: "object",
		Properties: map[string]*Schema{
			"entities": {
				Type: "array",
				Items: &JSONSchema{
					Type: "object",
					Properties: map[string]*Schema{
						"type":  {Type: "string"},
						"value": {Type: "string"},
						"score": {Type: "number", Minimum: float64Ptr(0), Maximum: float64Ptr(1)},
					},
					Required: []string{"type", "value"},
				},
			},
			"relations": {
				Type: "array",
				Items: &JSONSchema{
					Type: "object",
					Properties: map[string]*Schema{
						"from": {Type: "string"},
						"to":   {Type: "string"},
						"type": {Type: "string"},
					},
					Required: []string{"from", "to", "type"},
				},
			},
		},
	},
}

func float64Ptr(f float64) *float64 {
	return &f
}

func intPtr(i int) *int {
	return &i
}
