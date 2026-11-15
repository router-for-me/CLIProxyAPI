package codex

import (
	"fmt"
	"strings"
	"unicode"
)

// CredentialFileName returns the filename used to persist Codex OAuth credentials.
// The account hash is included when available to keep accounts with the same email
// and plan distinct. The legacy email-based format remains the fallback.
//
// The email and the account hash are untrusted: the email comes from the "email"
// claim of a caller-supplied Codex ID token. Both are sanitized here so the result
// is always a bare file name that cannot escape the credentials directory when it
// is joined onto it.
func CredentialFileName(email, planType, hashAccountID string, includeProviderPrefix bool) string {
	email = sanitizeFileNameComponent(email)
	plan := normalizePlanTypeForFilename(planType)
	hashAccountID = sanitizeFileNameComponent(hashAccountID)

	prefix := ""
	if includeProviderPrefix {
		prefix = "codex"
	}

	if hashAccountID != "" {
		if plan == "" {
			return fmt.Sprintf("%s-%s-%s.json", prefix, hashAccountID, email)
		}
		return fmt.Sprintf("%s-%s-%s-%s.json", prefix, hashAccountID, email, plan)
	}
	if plan == "" {
		return fmt.Sprintf("%s-%s.json", prefix, email)
	}
	return fmt.Sprintf("%s-%s-%s.json", prefix, email, plan)
}

// windowsForbiddenFileNameChars are the characters that are legal in a file name
// on other systems but make the create fail on Windows. Path separators and
// colons are already replaced by CredentialFileName.
const windowsForbiddenFileNameChars = `<>"|?*`

// PlatformCredentialFileName returns the name the Codex import and the
// maintained CLI login write for an account: CredentialFileName, plus, on
// Windows only, the characters in windowsForbiddenFileNameChars become "_" so
// the file can be created. Both flows call it, so an import and a later login of
// one account resolve to one credential file. The deprecated /v0/management
// flow keeps CredentialFileName. goos is a parameter so the Windows rule can be
// tested anywhere; production callers pass runtime.GOOS.
func PlatformCredentialFileName(email, planType, hashAccountID, goos string) string {
	name := CredentialFileName(email, planType, hashAccountID, true)
	if goos != "windows" {
		return name
	}
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(windowsForbiddenFileNameChars, r) {
			return '_'
		}
		return r
	}, name)
}

// sanitizeFileNameComponent makes an untrusted string safe to embed in a file
// name: path separators, colons (the NTFS stream separator) and control
// characters become "_". Every other character is kept, so a credential saved
// before this check keeps its file name.
func sanitizeFileNameComponent(value string) string {
	value = strings.TrimSpace(value)
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || unicode.IsControl(r) {
			return '_'
		}
		return r
	}, value)
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
		parts[i] = strings.ToLower(strings.TrimSpace(part))
	}
	return strings.Join(parts, "-")
}
