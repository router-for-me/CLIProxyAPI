package antigravity

import (
	"fmt"
	"strings"
)

// CredentialFileName returns the filename used to persist Antigravity credentials.
// It uses the email as a suffix to disambiguate accounts.
func CredentialFileName(email string) string {
	email = strings.TrimSpace(email)
	if email == "" {
		return "antigravity.json"
	}
	return fmt.Sprintf("antigravity-%s.json", email)
}
