// Package util provides logging utilities for secure logging
package util

import (
	"strings"
)

// SensitiveKeyPatterns are keys that should be masked in logs
var SensitiveKeyPatterns = []string{
	"password", "token", "secret", "apikey", "api_key",
	"access_token", "refresh_token", "bearer", "authorization",
	"credential", "private_key", "client_secret",
}

// MaskSensitiveData masks sensitive values in maps
func MaskSensitiveData(data map[string]string) map[string]string {
	if data == nil {
		return nil
	}
	
	result := make(map[string]string, len(data))
	for k, v := range data {
		result[k] = MaskValue(k, v)
	}
	return result
}

// MaskValue masks sensitive values based on key name
func MaskValue(key, value string) string {
	if value == "" {
		return ""
	}
	
	// Check if key is sensitive
	if IsSensitiveKey(key) {
		return MaskString(value)
	}
	return value
}

// IsSensitiveKey checks if a key name suggests sensitive data
func IsSensitiveKey(key string) bool {
	keyLower := strings.ToLower(key)
	for _, pattern := range SensitiveKeyPatterns {
		if strings.Contains(keyLower, pattern) {
			return true
		}
	}
	return false
}

// MaskString masks a value showing first/last 4 chars only
func MaskString(s string) string {
	if len(s) <= 8 {
		return "****"
	}
	if len(s) <= 12 {
		return s[:4] + "****" + s[len(s)-4:]
	}
	return s[:6] + "****" + s[len(s)-4:]
}

// SafeLogField creates a safe log field that masks sensitive data
type SafeLogField struct {
	Key   string
	Value interface{}
}

// String implements fmt.Stringer
func (s SafeLogField) String() string {
	if s.Value == nil {
		return ""
	}
	
	// Convert to string
	var str string
	switch v := s.Value.(type) {
	case string:
		str = v
	default:
		str = "****"
	}
	
	if IsSensitiveKey(s.Key) {
		return s.Key + "=" + MaskString(str)
	}
	return s.Key + "=" + str
}
