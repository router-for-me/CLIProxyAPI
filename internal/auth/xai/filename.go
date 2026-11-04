package xai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// CredentialFileName returns the filename used to persist xAI OAuth credentials.
// It keeps a readable account name and appends a hash of the original identity
// so distinct emails that sanitize identically cannot overwrite each other.
func CredentialFileName(email, subject string) string {
	cleanEmail := strings.TrimSpace(email)
	if cleanEmail != "" {
		// Emails are case-insensitive, so normalize case to keep the same
		// account on the same file regardless of the case returned upstream.
		// The hash covers the normalized email only so the filename stays
		// stable even when a later login stops returning a subject.
		normalizedEmail := strings.ToLower(cleanEmail)
		sanitized := sanitizeFileSegment(normalizedEmail)
		if sanitized == "" {
			sanitized = "account"
		}
		// Leave room for the prefix, identity hash and extension on common filesystems.
		if len(sanitized) > 120 {
			sanitized = sanitized[:120]
		}
		digest := sha256.Sum256([]byte("email\x00" + normalizedEmail))
		return fmt.Sprintf("xai-%s-%s.json", sanitized, hex.EncodeToString(digest[:8]))
	}
	cleanSubject := strings.TrimSpace(subject)
	if cleanSubject != "" {
		sanitized := sanitizeFileSegment(cleanSubject)
		if sanitized == "" {
			sanitized = "account"
		}
		if len(sanitized) > 120 {
			sanitized = sanitized[:120]
		}
		digest := sha256.Sum256([]byte("subject\x00" + cleanSubject))
		return fmt.Sprintf("xai-%s-%s.json", sanitized, hex.EncodeToString(digest[:8]))
	}
	return fmt.Sprintf("xai-%d.json", time.Now().UnixMilli())
}

func sanitizeFileSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '@' || r == '.' || r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// legacyCredentialFileName reconstructs the pre-hash filename format so a
// credential saved before the hash suffix was introduced can be migrated.
func legacyCredentialFileName(email, subject string) string {
	if sanitized := sanitizeFileSegment(email); sanitized != "" {
		return fmt.Sprintf("xai-%s.json", sanitized)
	}
	if sanitized := sanitizeFileSegment(subject); sanitized != "" {
		return fmt.Sprintf("xai-%s.json", sanitized)
	}
	return ""
}

// FindMatchingLegacyCredential locates a prior filename for the same xAI
// identity as target. A candidate's recognized names are derived from every
// subset of its stored identity, covering the pre-hash format as well as
// hashed names from logins that saw only the email or only the subject.
// The target's own canonical filename is never a predecessor, so an ordinary
// re-login does not match the destination file itself. A legacy filename
// alone is ambiguous because distinct accounts could sanitize to it, so a
// candidate is only returned when its stored identity affirmatively matches
// the target identity.
func FindMatchingLegacyCredential(ctx context.Context, store coreauth.Store, target *coreauth.Auth) (*coreauth.Auth, error) {
	if store == nil || !isHashedCredentialTarget(target) {
		return nil, nil
	}

	records, errList := store.List(ctx)
	if errList != nil {
		if errors.Is(errList, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list xAI credentials for legacy migration: %w", errList)
	}

	targetEmail := metadataString(target.Metadata, "email")
	targetSubject := metadataString(target.Metadata, "sub")
	// A nonempty identity always yields a hashed filename even when the
	// pre-hash sanitization would have produced an empty segment, so do not
	// require a legacy name here; isHashedCredentialTarget already verified
	// that at least one identity field is present.
	targetFileName := strings.TrimSpace(target.FileName)
	if targetFileName == "" {
		targetFileName = strings.TrimSpace(target.ID)
	}
	targetBaseName := filepath.Base(targetFileName)

	for _, candidate := range records {
		if candidate == nil || !strings.EqualFold(strings.TrimSpace(candidate.Provider), "xai") {
			continue
		}
		if !identityMatches(targetEmail, targetSubject, candidate) {
			continue
		}
		candidateFileName := strings.TrimSpace(candidate.FileName)
		if candidateFileName == "" {
			candidateFileName = strings.TrimSpace(candidate.ID)
		}
		candidateBaseName := filepath.Base(candidateFileName)
		// Only root-level generated files participate in migration. Login
		// always writes flat names, and a nested relative ID would not
		// resolve against the auth directory on cleanup.
		if candidateBaseName != candidateFileName {
			continue
		}
		// Never match the destination file itself: callers delete the returned
		// predecessor after saving, which would erase the fresh credential.
		if strings.EqualFold(candidateBaseName, targetBaseName) {
			continue
		}
		candidateEmail := metadataString(candidate.Metadata, "email")
		candidateSubject := metadataString(candidate.Metadata, "sub")
		if isRecognizedPredecessor(candidateBaseName, candidateEmail, candidateSubject) {
			return candidate, nil
		}
	}

	return nil, nil
}

// isRecognizedPredecessor reports whether baseName is a filename the login
// flow itself would have produced from some subset of the stored identity:
// the pre-hash name, or a hashed name derived from either identity field.
// A file named when only the subject was known stays recognizable after a
// later login backfills the email into its stored metadata.
func isRecognizedPredecessor(baseName, email, subject string) bool {
	if email == "" && subject == "" {
		return false
	}
	if strings.EqualFold(baseName, CredentialFileName(email, subject)) {
		return true
	}
	if email != "" && strings.EqualFold(baseName, CredentialFileName(email, "")) {
		return true
	}
	if subject != "" && strings.EqualFold(baseName, CredentialFileName("", subject)) {
		return true
	}
	if legacyName := legacyCredentialFileName(email, subject); legacyName != "" && strings.EqualFold(baseName, legacyName) {
		return true
	}
	if subject != "" {
		if legacyName := legacyCredentialFileName("", subject); legacyName != "" && strings.EqualFold(baseName, legacyName) {
			return true
		}
	}
	return false
}

func isHashedCredentialTarget(target *coreauth.Auth) bool {
	if target == nil || !strings.EqualFold(strings.TrimSpace(target.Provider), "xai") {
		return false
	}
	email := metadataString(target.Metadata, "email")
	subject := metadataString(target.Metadata, "sub")
	if email == "" && subject == "" {
		return false
	}
	targetFileName := strings.TrimSpace(target.FileName)
	if targetFileName == "" {
		targetFileName = strings.TrimSpace(target.ID)
	}
	return strings.EqualFold(filepath.Base(targetFileName), CredentialFileName(email, subject))
}

// identityMatches reports whether a candidate credential is provably the
// same account as the target: at least one stored identity field must match
// affirmatively and no stored identity field may conflict. Emails compare
// case-insensitively; subjects are case-sensitive identifiers and must match
// exactly.
func identityMatches(targetEmail, targetSubject string, candidate *coreauth.Auth) bool {
	candidateEmail := metadataString(candidate.Metadata, "email")
	candidateSubject := metadataString(candidate.Metadata, "sub")
	emailMatches := targetEmail != "" && candidateEmail != "" && strings.EqualFold(targetEmail, candidateEmail)
	emailConflicts := targetEmail != "" && candidateEmail != "" && !strings.EqualFold(targetEmail, candidateEmail)
	subjectMatches := targetSubject != "" && candidateSubject != "" && targetSubject == candidateSubject
	subjectConflicts := targetSubject != "" && candidateSubject != "" && targetSubject != candidateSubject
	return (emailMatches || subjectMatches) && !emailConflicts && !subjectConflicts
}

func metadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}
