package util

import (
	"unsafe"

	"github.com/tidwall/gjson"
)

// GetGJSONBytesNoCopy returns a GJSON result that may reference data directly.
// Callers must not retain the result or mutate data while using it.
func GetGJSONBytesNoCopy(data []byte, path string) gjson.Result {
	if len(data) == 0 {
		return gjson.Result{}
	}
	return gjson.Get(unsafe.String(unsafe.SliceData(data), len(data)), path)
}
