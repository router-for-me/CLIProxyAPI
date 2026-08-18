package helps

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// SetStringIfDifferent updates path only when its value is not already the
// canonical JSON string. Values with another JSON type are still normalized.
func SetStringIfDifferent(payload []byte, path, value string) []byte {
	current := gjson.GetBytes(payload, path)
	if current.Type == gjson.String && current.String() == value {
		return payload
	}
	updated, errSet := sjson.SetBytes(payload, path, value)
	if errSet != nil {
		return payload
	}
	return updated
}

// SetBoolIfDifferent updates path only when its value is not already the
// canonical JSON boolean. Values with another JSON type are still normalized.
func SetBoolIfDifferent(payload []byte, path string, value bool) []byte {
	current := gjson.GetBytes(payload, path)
	if (value && current.Type == gjson.True) || (!value && current.Type == gjson.False) {
		return payload
	}
	updated, errSet := sjson.SetBytes(payload, path, value)
	if errSet != nil {
		return payload
	}
	return updated
}

// SetRawIfDifferent updates path only when the existing raw JSON is identical.
func SetRawIfDifferent(payload []byte, path string, value []byte) []byte {
	current := gjson.GetBytes(payload, path)
	if current.Exists() && len(current.Indexes) == 0 && current.Raw == string(value) {
		return payload
	}
	updated, errSet := sjson.SetRawBytes(payload, path, value)
	if errSet != nil {
		return payload
	}
	return updated
}

// JoinRawJSONArray joins validated raw JSON array items without re-encoding them.
func JoinRawJSONArray(items [][]byte) []byte {
	size := len(items) + 1
	for _, item := range items {
		size += len(item)
	}
	out := make([]byte, 0, size)
	out = append(out, '[')
	for index, item := range items {
		if index > 0 {
			out = append(out, ',')
		}
		out = append(out, item...)
	}
	return append(out, ']')
}

// JoinRawJSONStrings joins raw JSON array items held as strings.
func JoinRawJSONStrings(items []string) []byte {
	size := len(items) + 1
	for _, item := range items {
		size += len(item)
	}
	out := make([]byte, 0, size)
	out = append(out, '[')
	for index, item := range items {
		if index > 0 {
			out = append(out, ',')
		}
		out = append(out, item...)
	}
	return append(out, ']')
}
