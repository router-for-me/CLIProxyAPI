//go:build !darwin && !linux && !windows

package zai

// zcodeOsVersion has no platform source on other targets; the header is
// omitted, matching the official client when the value is not normalizable.
func zcodeOsVersion() string {
	return ""
}
