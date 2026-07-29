package auth

import "strings"

// authBucket returns the credential's bucket tag, or "" when unbucketed.
// Attributes take precedence over raw auth-file metadata, mirroring authWeight.
func authBucket(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if raw, ok := auth.Attributes[AttributeBucket]; ok {
		if bucket := strings.TrimSpace(raw); bucket != "" {
			return bucket
		}
	}
	if raw, ok := auth.Metadata[AttributeBucket]; ok {
		if value, okString := raw.(string); okString {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
