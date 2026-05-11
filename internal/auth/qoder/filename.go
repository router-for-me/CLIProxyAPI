package qoder

import (
	"fmt"
	"strings"
)

// CredentialFileName returns the filename used to persist Qoder credentials.
// It uses the uid as a suffix to disambiguate accounts.
func CredentialFileName(uid string) string {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return "qoder.json"
	}
	return fmt.Sprintf("qoder-%s.json", uid)
}
