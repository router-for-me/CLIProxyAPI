package codex

import (
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// CredentialFileName returns the filename used to persist Codex OAuth credentials.
// When planType is available (e.g. "plus", "team"), it is appended after the email
// as a suffix to disambiguate subscriptions.
func CredentialFileName(email, planType, hashAccountID string, includeProviderPrefix bool) string {
	email = strings.TrimSpace(email)
	plan := normalizePlanTypeForFilename(planType)

	prefix := ""
	if includeProviderPrefix {
		prefix = "codex"
	}

	if plan == "" {
		return fmt.Sprintf("%s-%s.json", prefix, email)
	} else if plan == "team" {
		return fmt.Sprintf("%s-%s-%s-%s.json", prefix, hashAccountID, email, plan)
	}
	return fmt.Sprintf("%s-%s-%s.json", prefix, email, plan)
}

func normalizePlanTypeForFilename(planType string) string {
	planType = strings.TrimSpace(planType)
	if planType == "" {
		return ""
	}

	parts := strings.FieldsFunc(planType, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(parts) == 0 {
		return ""
	}

	for i, part := range parts {
		parts[i] = titleToken(part)
	}
	return strings.Join(parts, "-")
}

func titleToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	return cases.Title(language.English).String(token)
}
