// Package common provides shared constants and utilities for Kiro translator.
package common

// GetString safely extracts a string from a map.
// Returns empty string if the key doesn't exist or the value is not a string.
func GetString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// GetStringValue is an alias for GetString for backward compatibility.
func GetStringValue(m map[string]interface{}, key string) string {
	return GetString(m, key)
}