package helps

import (
	"encoding/json"
	"sort"

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

// JSONStringField is a JSON string path to overwrite in one document rewrite.
type JSONStringField struct {
	Path  string
	Value string
}

// ReplaceJSONStringFields copies payload once and writes all existing string
// fields in a single splice. Missing paths fall back to sjson.SetBytes.
func ReplaceJSONStringFields(payload []byte, fields []JSONStringField) []byte {
	if len(payload) == 0 || len(fields) == 0 {
		return payload
	}

	type replacement struct {
		index int
		oldN  int
		neu   []byte
	}
	replacements := make([]replacement, 0, len(fields))
	var missing []JSONStringField
	extra := 0
	for _, field := range fields {
		if field.Path == "" {
			continue
		}
		current := gjson.GetBytes(payload, field.Path)
		if !current.Exists() || current.Index <= 0 {
			missing = append(missing, field)
			continue
		}
		encoded, errMarshal := json.Marshal(field.Value)
		if errMarshal != nil {
			missing = append(missing, field)
			continue
		}
		if current.Raw == string(encoded) {
			continue
		}
		replacements = append(replacements, replacement{
			index: current.Index,
			oldN:  len(current.Raw),
			neu:   encoded,
		})
		extra += len(encoded) - len(current.Raw)
	}
	if extra < 0 {
		extra = 0
	}

	out := payload
	if len(replacements) > 0 {
		sort.Slice(replacements, func(i, j int) bool {
			return replacements[i].index < replacements[j].index
		})
		buf := make([]byte, 0, len(payload)+extra)
		last := 0
		for _, item := range replacements {
			if item.index < last || item.index+item.oldN > len(payload) {
				return fallbackSetJSONStringFields(payload, fields)
			}
			buf = append(buf, payload[last:item.index]...)
			buf = append(buf, item.neu...)
			last = item.index + item.oldN
		}
		buf = append(buf, payload[last:]...)
		out = buf
	}
	for _, field := range missing {
		updated, errSet := sjson.SetBytes(out, field.Path, field.Value)
		if errSet != nil {
			continue
		}
		out = updated
	}
	return out
}

func fallbackSetJSONStringFields(payload []byte, fields []JSONStringField) []byte {
	out := payload
	for _, field := range fields {
		updated, errSet := sjson.SetBytes(out, field.Path, field.Value)
		if errSet != nil {
			continue
		}
		out = updated
	}
	return out
}
