package session

import (
	"strings"

	"github.com/tidwall/gjson"
)

// sessionFieldIndex indexes object fields once for the fixed, dotted session
// paths used by this package. Large message and image values remain unmodified
// slices of the original JSON; they are never decoded or copied into a map.
// The index is local to extraction and must not be retained across requests.
type sessionFieldIndex struct {
	gjson.Result
	fields   map[string]gjson.Result
	children map[string]sessionFieldIndex
}

func indexSessionFields(root gjson.Result) sessionFieldIndex {
	idx := sessionFieldIndex{Result: root}
	if !root.IsObject() {
		return idx
	}
	idx.fields = make(map[string]gjson.Result)
	idx.children = make(map[string]sessionFieldIndex)
	duplicate := false
	root.ForEach(func(key, value gjson.Result) bool {
		if _, exists := idx.fields[key.Str]; exists {
			duplicate = true
			return false
		}
		idx.fields[key.Str] = value
		return true
	})
	// Duplicate object keys have path-dependent GJSON lookup semantics. Keep
	// the original reader for that uncommon case instead of choosing a winner.
	if duplicate {
		idx.fields = nil
		idx.children = nil
	}
	return idx
}

func (idx sessionFieldIndex) Get(path string) gjson.Result {
	if idx.fields == nil {
		return idx.Result.Get(path)
	}
	key, rest, nested := strings.Cut(path, ".")
	value, exists := idx.fields[key]
	if !exists || !nested {
		return value
	}
	child, cached := idx.children[key]
	if !cached {
		child = indexSessionFields(value)
		idx.children[key] = child
	}
	return child.Get(rest)
}
