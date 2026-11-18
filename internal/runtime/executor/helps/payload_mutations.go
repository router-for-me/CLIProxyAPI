package helps

import (
	"github.com/MoeForge/moego/ojson"
)

// SetStringIfDifferent updates path only when its value is not already the
// canonical JSON string. Values with another JSON type are still normalized.
func SetStringIfDifferent(payload []byte, path, value string) []byte {
	current := ojson.GetBytesNoCopy(payload, path)
	if current.Type == ojson.String && current.String() == value {
		return payload
	}
	updated, errSet := ojson.SetBytes(payload, path, value)
	if errSet != nil {
		return payload
	}
	return updated
}

// SetStringIfDifferentDoc updates path on doc in place only when its value is not
// already the canonical JSON string. Values with another JSON type are still normalized.
func SetStringIfDifferentDoc(doc *ojson.Doc, path, value string) bool {
	if doc == nil {
		return false
	}
	current := doc.Get(path)
	if current.Type == ojson.String && current.String() == value {
		return false
	}
	return doc.Set(path, value) == nil
}

// SetBoolIfDifferent updates path only when its value is not already the
// canonical JSON boolean. Values with another JSON type are still normalized.
func SetBoolIfDifferent(payload []byte, path string, value bool) []byte {
	current := ojson.GetBytesNoCopy(payload, path)
	if (value && current.Type == ojson.True) || (!value && current.Type == ojson.False) {
		return payload
	}
	updated, errSet := ojson.SetBytes(payload, path, value)
	if errSet != nil {
		return payload
	}
	return updated
}

// SetBoolIfDifferentDoc updates path on doc in place only when its value is not
// already the canonical JSON boolean. Values with another JSON type are still normalized.
func SetBoolIfDifferentDoc(doc *ojson.Doc, path string, value bool) bool {
	if doc == nil {
		return false
	}
	current := doc.Get(path)
	if (value && current.Type == ojson.True) || (!value && current.Type == ojson.False) {
		return false
	}
	return doc.Set(path, value) == nil
}

// SetRawIfDifferent updates path only when the existing raw JSON is identical.
func SetRawIfDifferent(payload []byte, path string, value []byte) []byte {
	current := ojson.GetBytesNoCopy(payload, path)
	if current.Exists() && len(current.Indexes) == 0 && current.Raw == string(value) {
		return payload
	}
	updated, errSet := ojson.SetRawBytes(payload, path, value)
	if errSet != nil {
		return payload
	}
	return updated
}

// SetRawIfDifferentDoc updates path on doc in place only when the existing raw JSON differs.
func SetRawIfDifferentDoc(doc *ojson.Doc, path string, value []byte) bool {
	if doc == nil {
		return false
	}
	current := doc.Get(path)
	if current.Exists() && len(current.Indexes) == 0 && current.Raw == string(value) {
		return false
	}
	return doc.SetRawBytes(path, value) == nil
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
