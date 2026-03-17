package common

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// DefaultSafetySettings returns the default Gemini safety configuration we attach to requests.
func DefaultSafetySettings() []map[string]string {
	return []map[string]string{
		{
			"category":  "HARM_CATEGORY_HARASSMENT",
			"threshold": "OFF",
		},
		{
			"category":  "HARM_CATEGORY_HATE_SPEECH",
			"threshold": "OFF",
		},
		{
			"category":  "HARM_CATEGORY_SEXUALLY_EXPLICIT",
			"threshold": "OFF",
		},
		{
			"category":  "HARM_CATEGORY_DANGEROUS_CONTENT",
			"threshold": "OFF",
		},
		{
			"category":  "HARM_CATEGORY_CIVIC_INTEGRITY",
			"threshold": "BLOCK_NONE",
		},
	}
}

// AttachDefaultSafetySettings ensures the default safety settings are present when absent.
// The caller must provide the target JSON path (e.g. "safetySettings" or "request.safetySettings").
func AttachDefaultSafetySettings(rawJSON []byte, path string) []byte {
	if gjson.GetBytes(rawJSON, path).Exists() {
		return rawJSON
	}

	out, err := sjson.SetBytes(rawJSON, path, DefaultSafetySettings())
	if err != nil {
		return rawJSON
	}

	return out
}
