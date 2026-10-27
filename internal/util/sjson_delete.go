package util

import (
	"github.com/tidwall/sjson"
)

// DeleteJSONBytesIfExists behaves like sjson.DeleteBytes but returns data
// unchanged when path is absent. sjson.DeleteBytes copies the document at
// least once even when there is nothing to delete (a partial prefix copy is
// built and discarded, then the whole input is copied again before sjson
// returns the original slice), which dominates allocation when callers
// defensively delete alias spellings that are usually missing. path must be a
// nonempty literal dotted path (gjson-compatible escapes like \. are
// supported); sjson's "-1" last-element delete and ":" forced-object-key syntax
// are not supported here. Behavior otherwise matches sjson.DeleteBytes.
func DeleteJSONBytesIfExists(data []byte, path string) ([]byte, error) {
	if !GetGJSONBytesNoCopy(data, path).Exists() {
		return data, nil
	}
	return sjson.DeleteBytes(data, path)
}
