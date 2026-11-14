package util

import (
	"unsafe"

	ojson "github.com/MoeForge/moego/ojson"
)

// GetGJSONBytesNoCopy returns a GJSON result that may reference data directly.
// Callers must not retain the result or mutate data while using it.
func GetGJSONBytesNoCopy(data []byte, path string) ojson.Result {
	if len(data) == 0 {
		return ojson.Result{}
	}
	return ojson.GetBytesNoCopy(data, path)
}

// ParseGJSONBytesNoCopy parses data into a GJSON result that references data
// directly. gjson.ParseBytes copies the whole document, which is prohibitive
// for multi-megabyte payloads. Callers must not retain the result or mutate
// data while using it.
func ParseGJSONBytesNoCopy(data []byte) ojson.Result {
	if len(data) == 0 {
		return ojson.Result{}
	}
	return ojson.Parse(unsafe.String(unsafe.SliceData(data), len(data)))
}
