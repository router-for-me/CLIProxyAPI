package samplekeys

import "strings"

// IsClientAPIKey reports whether key is one of the documented sample client keys.
func IsClientAPIKey(key string) bool {
	switch strings.TrimSpace(key) {
	case "your-api-key-1", "your-api-key-2", "your-api-key-3":
		return true
	}
	return false
}

// ContainsClientAPIKey reports whether keys includes any documented sample client key.
func ContainsClientAPIKey(keys []string) bool {
	for _, key := range keys {
		if IsClientAPIKey(key) {
			return true
		}
	}
	return false
}
