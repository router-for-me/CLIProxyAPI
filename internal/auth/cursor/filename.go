package cursor

import (
	"fmt"
	"strings"
)

// CredentialFileName returns the filename used to persist Cursor credentials.
// It uses the label as a suffix to disambiguate multiple accounts.
func CredentialFileName(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return "cursor.json"
	}
	return fmt.Sprintf("cursor-%s.json", label)
}
