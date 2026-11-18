package xai

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type credentialListStore struct {
	records []*coreauth.Auth
}

func (s *credentialListStore) List(context.Context) ([]*coreauth.Auth, error) {
	return s.records, nil
}

func (*credentialListStore) Save(context.Context, *coreauth.Auth) (string, error) {
	panic("unexpected Save call")
}

func (*credentialListStore) Delete(context.Context, string) error {
	panic("unexpected Delete call")
}

func TestCredentialFileNameSeparatesCollidingEmails(t *testing.T) {
	plus := CredentialFileName("a+b@example.com", "sub-first")
	hyphen := CredentialFileName("a-b@example.com", "sub-second")
	if plus == hyphen {
		t.Fatalf("CredentialFileName(a+b@example.com) = %q collides with CredentialFileName(a-b@example.com)", plus)
	}
	for _, name := range []string{plus, hyphen} {
		if !strings.HasPrefix(name, "xai-") || !strings.HasSuffix(name, ".json") || !filepath.IsLocal(name) {
			t.Fatalf("CredentialFileName() = %q, want local xai-*.json filename", name)
		}
	}
}

func TestCredentialFileNameStableForSameAccount(t *testing.T) {
	first := CredentialFileName("User+Work@example.com", "sub-1")
	second := CredentialFileName(" user+work@example.com ", "sub-1")
	if first != second {
		t.Fatalf("re-login changed account file: %q != %q", first, second)
	}
}

func TestCredentialFileNameStableWhenSubjectAvailabilityChanges(t *testing.T) {
	withoutSubject := CredentialFileName("user@example.com", "")
	withSubject := CredentialFileName("user@example.com", "sub-1")
	if withoutSubject != withSubject {
		t.Fatalf("subject availability changed account file: %q != %q", withoutSubject, withSubject)
	}
}

func TestCredentialFileNameSeparatesSubjects(t *testing.T) {
	if got := CredentialFileName("", "sub one"); got == CredentialFileName("", "sub-one") {
		t.Fatalf("subject-only filenames collide: %q", got)
	}
}

func TestCredentialFileNameLongEmailFitsFilesystem(t *testing.T) {
	name := CredentialFileName(strings.Repeat("a", 300)+"@example.com", "sub-1")
	if len(name) > 255 || !filepath.IsLocal(name) {
		t.Fatalf("filename %q (len %d) cannot be saved on common filesystems", name, len(name))
	}
	shortTail := CredentialFileName(strings.Repeat("a", 300)+"b@example.com", "sub-1")
	if name == shortTail {
		t.Fatalf("long emails differing past truncation point collide: %q", name)
	}
}

func TestFindMatchingLegacyCredential(t *testing.T) {
	const collidingLegacyFileName = "xai-a-b@example.com.json"

	tests := []struct {
		name          string
		targetEmail   string
		targetSubject string
		legacyEmail   string
		legacySubject string
		// legacyFileName overrides the default colliding legacy filename so a
		// fixture can carry the exact predecessor name it claims to exercise.
		legacyFileName string
		wantMatch      bool
	}{
		{
			name:           "same account migrates from legacy filename",
			targetEmail:    "a+b@example.com",
			targetSubject:  "sub-first",
			legacyEmail:    "a+b@example.com",
			legacySubject:  "sub-first",
			legacyFileName: collidingLegacyFileName,
			wantMatch:      true,
		},
		{
			name:           "colliding account does not claim the other account file",
			targetEmail:    "a-b@example.com",
			targetSubject:  "sub-second",
			legacyEmail:    "a+b@example.com",
			legacySubject:  "sub-first",
			legacyFileName: collidingLegacyFileName,
		},
		{
			name:           "conflicting subject blocks migration",
			targetEmail:    "a+b@example.com",
			targetSubject:  "sub-other",
			legacyEmail:    "a+b@example.com",
			legacySubject:  "sub-first",
			legacyFileName: collidingLegacyFileName,
		},
		{
			name:           "conflicting email blocks migration",
			targetEmail:    "a-b@example.com",
			targetSubject:  "sub-shared",
			legacyEmail:    "a+b@example.com",
			legacySubject:  "sub-shared",
			legacyFileName: collidingLegacyFileName,
		},
		{
			name:           "matching email with missing stored subject migrates",
			targetEmail:    "a+b@example.com",
			targetSubject:  "sub-first",
			legacyEmail:    "a+b@example.com",
			legacyFileName: collidingLegacyFileName,
			wantMatch:      true,
		},
		{
			name:           "legacy file without stored identity stays unclaimed",
			targetEmail:    "a+b@example.com",
			targetSubject:  "sub-first",
			legacyFileName: collidingLegacyFileName,
		},
		{
			name:           "disjoint identity fields stay unclaimed",
			targetEmail:    "a+b@example.com",
			legacySubject:  "sub-first",
			legacyFileName: "xai-sub-first.json",
		},
		{
			name:           "matching subject migrates from pre-hash subject filename",
			targetSubject:  "sub-first",
			legacySubject:  "sub-first",
			legacyFileName: "xai-sub-first.json",
			wantMatch:      true,
		},
		{
			name:           "pre-hash subject filename differing only by case stays unclaimed",
			targetSubject:  "abc",
			legacySubject:  "ABC",
			legacyFileName: "xai-ABC.json",
		},
		{
			name:          "email gained later migrates hashed subject-only predecessor",
			targetEmail:   "a+b@example.com",
			targetSubject: "sub-first",
			legacySubject: "sub-first",
			wantMatch:     true,
		},
		{
			name:           "email gained later migrates pre-hash subject-only predecessor",
			targetEmail:    "a+b@example.com",
			targetSubject:  "sub-first",
			legacySubject:  "sub-first",
			legacyFileName: "xai-sub-first.json",
			wantMatch:      true,
		},
		{
			name:          "email lost later selects email-hashed predecessor",
			targetSubject: "sub-first",
			legacyEmail:   "a+b@example.com",
			legacySubject: "sub-first",
			wantMatch:     true,
		},
		{
			name:          "canonical destination file is not its own predecessor",
			targetEmail:   "a+b@example.com",
			targetSubject: "sub-first",
			legacyEmail:   "a+b@example.com",
			legacySubject: "sub-first",
			// Default fixture name is the hashed name, identical to the
			// target's own destination on an ordinary re-login.
		},
		{
			name:          "custom-named credential is never claimed",
			targetEmail:   "a+b@example.com",
			targetSubject: "sub-first",
			legacyEmail:   "a+b@example.com",
			legacySubject: "sub-first",
			// Same stored identity, but the file was renamed or imported;
			// migration must not merge or delete a file it does not own.
			legacyFileName: "my-xai.json",
		},
		{
			name:          "subject-named file with backfilled email stays reconcilable",
			targetEmail:   "a+b@example.com",
			targetSubject: "sub-first",
			legacyEmail:   "a+b@example.com",
			legacySubject: "sub-first",
			// Hashed subject-only name; the stored identity gained the email
			// when an earlier migration merged it into this file.
			legacyFileName: CredentialFileName("", "sub-first"),
			wantMatch:      true,
		},
		{
			name:           "pre-hash subject-named file with backfilled email stays reconcilable",
			targetEmail:    "a+b@example.com",
			targetSubject:  "sub-first",
			legacyEmail:    "a+b@example.com",
			legacySubject:  "sub-first",
			legacyFileName: "xai-sub-first.json",
			wantMatch:      true,
		},
		{
			name:          "subject-only file named by foreign identity stays unclaimed",
			targetEmail:   "a+b@example.com",
			targetSubject: "sub-first",
			legacyEmail:   "a+b@example.com",
			legacySubject: "sub-first",
			// The filename was derived from a different subject than the stored
			// identity, so the file is not one the login flow produced for it.
			legacyFileName: CredentialFileName("", "sub-other"),
		},
		{
			name:           "nested credential is never claimed",
			targetEmail:    "a+b@example.com",
			targetSubject:  "sub-first",
			legacyEmail:    "a+b@example.com",
			legacySubject:  "sub-first",
			legacyFileName: "team/" + collidingLegacyFileName,
		},
		{
			name:          "unsanitizable subject still reconciles by identity",
			targetSubject: "+++",
			legacyEmail:   "a+b@example.com",
			legacySubject: "+++",
			wantMatch:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			targetFileName := CredentialFileName(tt.targetEmail, tt.targetSubject)
			target := &coreauth.Auth{
				ID:       targetFileName,
				FileName: targetFileName,
				Provider: "xai",
				Metadata: map[string]any{"email": tt.targetEmail, "sub": tt.targetSubject},
			}
			legacyFileName := tt.legacyFileName
			if legacyFileName == "" {
				// Default to the predecessor filename the stored identity itself
				// would have produced; fixtures that need the ambiguous
				// colliding name set legacyFileName explicitly.
				legacyFileName = CredentialFileName(tt.legacyEmail, tt.legacySubject)
			}
			legacy := &coreauth.Auth{
				ID:       legacyFileName,
				FileName: legacyFileName,
				Provider: "xai",
				Metadata: map[string]any{"email": tt.legacyEmail, "sub": tt.legacySubject},
			}
			got, err := FindMatchingLegacyCredential(context.Background(), &credentialListStore{records: []*coreauth.Auth{legacy}}, target)
			if err != nil {
				t.Fatalf("FindMatchingLegacyCredential() error = %v", err)
			}
			if (got != nil) != tt.wantMatch {
				t.Fatalf("FindMatchingLegacyCredential() match = %v, want %v", got != nil, tt.wantMatch)
			}
		})
	}
}
