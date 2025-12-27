package util

import (
	"bytes"
	"encoding/json"
	"strings"
)

var sensitiveJSONKeys = map[string]struct{}{
	"access_token":        {},
	"api_key":             {},
	"api-key":             {},
	"apikey":              {},
	"authorization":       {},
	"client_secret":       {},
	"clientsecret":        {},
	"id_token":            {},
	"password":            {},
	"refresh_token":       {},
	"secret":              {},
	"secret_access_key":   {},
	"session_token":       {},
	"token":               {},
	"x-api-key":           {},
	"x-goog-api-key":      {},
	"x-goog-vertex-token": {},
}

// MaskSensitiveJSON redacts common credential fields in a JSON request/response body.
// If the payload is not valid JSON, the original bytes are returned unchanged.
func MaskSensitiveJSON(body []byte) []byte {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return body
	}

	var value any
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return body
	}

	masked := maskSensitiveJSONValue(value)
	out, err := json.MarshalIndent(masked, "", "  ")
	if err != nil {
		return body
	}
	return out
}

func maskSensitiveJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, v := range typed {
			if isSensitiveJSONKey(key) {
				out[key] = "***"
				continue
			}
			out[key] = maskSensitiveJSONValue(v)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			out[i] = maskSensitiveJSONValue(typed[i])
		}
		return out
	default:
		return value
	}
}

func isSensitiveJSONKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	if normalized == "" {
		return false
	}
	if _, ok := sensitiveJSONKeys[normalized]; ok {
		return true
	}
	if strings.HasSuffix(normalized, "_secret") {
		return true
	}
	return false
}
