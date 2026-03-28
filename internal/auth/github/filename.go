package github

import (
	"fmt"
	"strings"
)

// CredentialFileName returns the filename used to persist GitHub Copilot credentials.
// Uses the GitHub login (username) as a suffix to disambiguate accounts.
func CredentialFileName(login string) string {
	login = strings.TrimSpace(login)
	if login == "" {
		return "github-copilot.json"
	}
	return fmt.Sprintf("github-copilot-%s.json", login)
}
