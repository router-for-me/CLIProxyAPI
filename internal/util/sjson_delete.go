package util

import (
	"github.com/MoeForge/moego/ojson"
)

// DeleteJSONBytesIfExists behaves like ojson.DeleteBytes but returns data
// unchanged when path is absent. ojson.DeleteBytes copies the document at
// least once even when there is nothing to delete (a partial prefix copy is
// built and discarded, then the whole input is copied again before sjson
// returns the original slice), which dominates allocation when callers
// defensively delete alias spellings that are usually missing. path must be a
// nonempty literal dotted path (gjson-compatible escapes like \. are
// supported); sjson's "-1" last-element delete and ":" forced-object-key syntax
// are not supported here. Behavior otherwise matches ojson.DeleteBytes.
func DeleteJSONBytesIfExists(data []byte, path string) ([]byte, error) {
	if !GetGJSONBytesNoCopy(data, path).Exists() {
		return data, nil
	}
	return ojson.DeleteBytes(data, path)
}

// DeleteJSONDocIfExists deletes path from doc only when the path exists.
func DeleteJSONDocIfExists(doc *ojson.Doc, path string) error {
	if doc == nil {
		return nil
	}
	if !doc.Get(path).Exists() {
		return nil
	}
	return doc.Delete(path)
}
