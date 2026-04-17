package helps

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var optimisticJSONOptions = &sjson.Options{
	Optimistic:     true,
	ReplaceInPlace: true,
}

type jsonEditOp uint8

const (
	jsonEditSet jsonEditOp = iota
	jsonEditSetRaw
	jsonEditDelete
)

// JSONEdit represents a single ordered JSON mutation.
type JSONEdit struct {
	path     string
	value    any
	rawValue []byte
	op       jsonEditOp
}

// SetJSONEdit replaces or creates a JSON field using optimistic in-place updates.
func SetJSONEdit(path string, value any) JSONEdit {
	return JSONEdit{path: path, value: value, op: jsonEditSet}
}

// SetRawJSONEdit replaces or creates a JSON field with a raw JSON fragment.
func SetRawJSONEdit(path string, rawValue []byte) JSONEdit {
	return JSONEdit{path: path, rawValue: rawValue, op: jsonEditSetRaw}
}

// DeleteJSONEdit removes a JSON field when it exists.
func DeleteJSONEdit(path string) JSONEdit {
	return JSONEdit{path: path, op: jsonEditDelete}
}

// EditJSONBytes applies ordered JSON edits, keeping the last successful body.
func EditJSONBytes(body []byte, edits ...JSONEdit) []byte {
	out := body
	for _, edit := range edits {
		var (
			updated []byte
			err     error
		)
		switch edit.op {
		case jsonEditSet:
			updated, err = SetJSONBytes(out, edit.path, edit.value)
		case jsonEditSetRaw:
			updated, err = SetRawJSONBytes(out, edit.path, edit.rawValue)
		case jsonEditDelete:
			updated, err = DeleteJSONBytes(out, edit.path)
		default:
			continue
		}
		if err == nil {
			out = updated
		}
	}
	return out
}

// SetJSONBytes updates a JSON field using optimistic in-place replacement when possible.
func SetJSONBytes(body []byte, path string, value any) ([]byte, error) {
	if path == "" {
		return body, nil
	}
	return sjson.SetBytesOptions(body, path, value, optimisticJSONOptions)
}

// SetRawJSONBytes updates a JSON field with raw JSON using optimistic in-place replacement when possible.
func SetRawJSONBytes(body []byte, path string, rawValue []byte) ([]byte, error) {
	if path == "" {
		return body, nil
	}
	return sjson.SetRawBytesOptions(body, path, rawValue, optimisticJSONOptions)
}

// DeleteJSONBytes removes a JSON field when present, skipping no-op deletes.
func DeleteJSONBytes(body []byte, path string) ([]byte, error) {
	if len(body) == 0 || path == "" {
		return body, nil
	}
	if !gjson.GetBytes(body, path).Exists() {
		return body, nil
	}
	return sjson.DeleteBytes(body, path)
}
