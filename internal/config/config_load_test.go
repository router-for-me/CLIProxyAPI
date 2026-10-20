package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

const uploadTestPlaintextSecret = "upload-plaintext-secret"

func writeUploadTestConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte(content), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}
	return path
}

func readUploadTestConfig(t *testing.T, path string) string {
	t.Helper()
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read config: %v", errRead)
	}
	return string(data)
}

func uploadTestBcrypt(t *testing.T) string {
	t.Helper()
	hashed, errHash := bcrypt.GenerateFromPassword([]byte(uploadTestPlaintextSecret), bcrypt.MinCost)
	if errHash != nil {
		t.Fatalf("bcrypt.GenerateFromPassword() error = %v", errHash)
	}
	return string(hashed)
}

// uploadTestSecret returns the secret-key stored under section and whether it exists.
func uploadTestSecret(t *testing.T, data, section string) (string, bool) {
	t.Helper()
	var doc map[string]any
	if errParse := yaml.Unmarshal([]byte(data), &doc); errParse != nil {
		t.Fatalf("parse config: %v", errParse)
	}
	management, _ := doc[section].(map[string]any)
	secret, found := management["secret-key"].(string)
	return secret, found
}

func assertUploadTestHashed(t *testing.T, data, section string) {
	t.Helper()
	secret, _ := uploadTestSecret(t, data, section)
	if errCompare := bcrypt.CompareHashAndPassword([]byte(secret), []byte(uploadTestPlaintextSecret)); errCompare != nil {
		t.Fatalf("%s.secret-key %q is not the bcrypt hash of the plaintext secret:\n%s", section, secret, data)
	}
}

func TestPrepareConfigFileForUploadHashesPlaintextSecret(t *testing.T) {
	for _, tc := range []struct {
		name    string
		section string
		other   string
	}{
		{name: "legacy layout", section: "remote-management", other: "management"},
		{name: "v8 layout", section: "management", other: "remote-management"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeUploadTestConfig(t, "# operator note\nport: 8317\n"+tc.section+":\n  secret-key: "+uploadTestPlaintextSecret+"\n")

			if errPrepare := PrepareConfigFileForUpload(path); errPrepare != nil {
				t.Fatalf("PrepareConfigFileForUpload() error = %v", errPrepare)
			}

			saved := readUploadTestConfig(t, path)
			if strings.Contains(saved, uploadTestPlaintextSecret) {
				t.Fatalf("config still holds the plaintext secret:\n%s", saved)
			}
			if !strings.Contains(saved, "# operator note") || !strings.Contains(saved, "port: 8317") {
				t.Fatalf("hashing dropped unrelated content:\n%s", saved)
			}
			assertUploadTestHashed(t, saved, tc.section)
			if _, found := uploadTestSecret(t, saved, tc.other); found {
				t.Fatalf("hashing wrote a secret under %s:\n%s", tc.other, saved)
			}
		})
	}
}

// Keys only name settings, so a secret spelled like a key must not block the upload.
func TestPrepareConfigFileForUploadIgnoresKeysSpelledLikeSecret(t *testing.T) {
	hashed := uploadTestBcrypt(t)
	for _, tc := range []struct {
		name    string
		content string
		check   func(t *testing.T, saved string)
	}{
		{
			name:    "v8 secret spelled like its section",
			content: "management:\n  secret-key: management\n",
			check: func(t *testing.T, saved string) {
				secret, _ := uploadTestSecret(t, saved, "management")
				if errCompare := bcrypt.CompareHashAndPassword([]byte(secret), []byte("management")); errCompare != nil {
					t.Fatalf("management.secret-key %q is not the bcrypt hash of the secret:\n%s", secret, saved)
				}
			},
		},
		{
			name:    "overridden legacy secret spelled like the v8 section",
			content: "remote-management:\n  secret-key: management\nmanagement:\n  secret-key: '" + hashed + "'\n",
			check: func(t *testing.T, saved string) {
				if secret, _ := uploadTestSecret(t, saved, "management"); secret != hashed {
					t.Fatalf("management.secret-key = %q, want the existing hash kept:\n%s", secret, saved)
				}
				if _, found := uploadTestSecret(t, saved, "remote-management"); found {
					t.Fatalf("conflicting legacy secret kept:\n%s", saved)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeUploadTestConfig(t, tc.content)

			if errPrepare := PrepareConfigFileForUpload(path); errPrepare != nil {
				t.Fatalf("PrepareConfigFileForUpload() error = %v", errPrepare)
			}

			tc.check(t, readUploadTestConfig(t, path))
		})
	}
}

// A YAML null elsewhere is not the string secret "null", so it must not block the upload.
func TestPrepareConfigFileForUploadIgnoresYAMLNullBesideStringNullSecret(t *testing.T) {
	path := writeUploadTestConfig(t, "proxy-url: null\nmanagement:\n  secret-key: 'null'\n")

	if errPrepare := PrepareConfigFileForUpload(path); errPrepare != nil {
		t.Fatalf("PrepareConfigFileForUpload() error = %v", errPrepare)
	}

	saved := readUploadTestConfig(t, path)
	secret, _ := uploadTestSecret(t, saved, "management")
	if errCompare := bcrypt.CompareHashAndPassword([]byte(secret), []byte("null")); errCompare != nil {
		t.Fatalf("management.secret-key %q is not the bcrypt hash of the string secret:\n%s", secret, saved)
	}
}

// A partially migrated config can keep a legacy plaintext secret next to the v8 key.
// The startup load drops that conflicting legacy field, so it must not be uploaded.
func TestPrepareConfigFileForUploadDropsConflictingLegacySecret(t *testing.T) {
	hashed := uploadTestBcrypt(t)
	for _, tc := range []struct {
		name     string
		v8Secret string
	}{
		{name: "duplicated plaintext", v8Secret: uploadTestPlaintextSecret},
		{name: "stale legacy beside hashed v8", v8Secret: "'" + hashed + "'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeUploadTestConfig(t, "remote-management:\n  secret-key: "+uploadTestPlaintextSecret+"\nmanagement:\n  secret-key: "+tc.v8Secret+"\n")

			if errPrepare := PrepareConfigFileForUpload(path); errPrepare != nil {
				t.Fatalf("PrepareConfigFileForUpload() error = %v", errPrepare)
			}

			saved := readUploadTestConfig(t, path)
			if strings.Contains(saved, uploadTestPlaintextSecret) {
				t.Fatalf("config still holds the plaintext secret:\n%s", saved)
			}
			assertUploadTestHashed(t, saved, "management")
			if _, found := uploadTestSecret(t, saved, "remote-management"); found {
				t.Fatalf("conflicting legacy secret kept:\n%s", saved)
			}
		})
	}
}

func TestPrepareConfigFileForUploadLeavesSettledConfigUntouched(t *testing.T) {
	hashed := uploadTestBcrypt(t)
	shippedTemplate, errTemplate := os.ReadFile(filepath.Join("..", "..", "config.example.yaml"))
	if errTemplate != nil {
		t.Fatalf("read config.example.yaml: %v", errTemplate)
	}
	for name, content := range map[string]string{
		"hashed legacy secret": "# operator note\nremote-management:\n  secret-key: '" + hashed + "'\n",
		"hashed v8 secret":     "# operator note\nmanagement:\n  secret-key: '" + hashed + "'\n",
		"no secret":            "# operator note\nport: 8317\n",
		"null v8 secret":       "# operator note\nmanagement:\n  secret-key: null\n",
		"tilde legacy secret":  "# operator note\nremote-management:\n  secret-key: ~\n",
		"empty file":           "",
		"shipped template":     string(shippedTemplate),
	} {
		t.Run(name, func(t *testing.T) {
			path := writeUploadTestConfig(t, content)

			if errPrepare := PrepareConfigFileForUpload(path); errPrepare != nil {
				t.Fatalf("PrepareConfigFileForUpload() error = %v", errPrepare)
			}

			if saved := readUploadTestConfig(t, path); saved != content {
				t.Fatalf("config rewritten to %q, want it untouched", saved)
			}
		})
	}
}

// A config whose secret cannot be hashed into the file still holds the plaintext, so
// callers must hear about it and not upload the file.
func TestPrepareConfigFileForUploadReportsSecretItCannotHash(t *testing.T) {
	for _, tc := range []struct {
		name     string
		content  string
		readOnly bool
	}{
		{name: "unparsable", content: "remote-management:\n  secret-key: " + uploadTestPlaintextSecret + "\nport: [8317\n"},
		{name: "secret too long for bcrypt", content: "remote-management:\n  secret-key: " + strings.Repeat("s", 73) + "\n"},
		{name: "read-only file", content: "remote-management:\n  secret-key: " + uploadTestPlaintextSecret + "\n", readOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeUploadTestConfig(t, tc.content)
			if tc.readOnly {
				if errChmod := os.Chmod(path, 0o400); errChmod != nil {
					t.Fatalf("chmod config: %v", errChmod)
				}
				t.Cleanup(func() {
					if errChmod := os.Chmod(path, 0o600); errChmod != nil {
						t.Errorf("restore config permissions: %v", errChmod)
					}
				})
				if file, errOpen := os.OpenFile(path, os.O_WRONLY, 0); errOpen == nil {
					if errClose := file.Close(); errClose != nil {
						t.Errorf("close config: %v", errClose)
					}
					t.Skip("read-only file stays writable for this user")
				}
			}

			if errPrepare := PrepareConfigFileForUpload(path); errPrepare == nil {
				t.Fatal("PrepareConfigFileForUpload() reported success for a secret it could not hash into the file")
			}

			if saved := readUploadTestConfig(t, path); saved != tc.content {
				t.Fatalf("config rewritten to %q, want it untouched", saved)
			}
		})
	}
}

// Hashing replaces only the effective key. A YAML anchor keeps the plaintext at its
// source, and rewriting the source could change other settings sharing it, so the file
// must be refused and left as it was; otherwise a later call would find the key hashed
// and accept the file with the plaintext still at the anchor.
func TestPrepareConfigFileForUploadRefusesPlaintextKeptByReferences(t *testing.T) {
	anchoredLegacy := "defaults: &legacy\n  secret-key: " + uploadTestPlaintextSecret + "\nremote-management: *legacy\nmanagement:\n  secret-key: "
	for name, content := range map[string]string{
		"mapping anchor": "defaults: &management\n  secret-key: " + uploadTestPlaintextSecret + "\nmanagement: *management\n",
		"scalar anchor":  "password: &password " + uploadTestPlaintextSecret + "\nmanagement:\n  secret-key: *password\n",
		"merge key":      "defaults: &management\n  secret-key: " + uploadTestPlaintextSecret + "\nmanagement:\n  <<: *management\n  allow-remote: true\n",
		"shared anchor":  "management: &management\n  secret-key: " + uploadTestPlaintextSecret + "\nother: *management\n",
		// The overridden legacy secret is dropped from remote-management, but its anchor
		// still holds the plaintext.
		"anchored legacy beside plaintext v8": anchoredLegacy + "current-plaintext-secret\n",
		"anchored legacy beside hashed v8":    anchoredLegacy + "'" + uploadTestBcrypt(t) + "'\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := writeUploadTestConfig(t, content)

			for attempt := 1; attempt <= 2; attempt++ {
				if errPrepare := PrepareConfigFileForUpload(path); errPrepare == nil {
					t.Fatalf("attempt %d: PrepareConfigFileForUpload() reported success, but the file still holds the plaintext secret:\n%s", attempt, readUploadTestConfig(t, path))
				}
			}

			if saved := readUploadTestConfig(t, path); saved != content {
				t.Fatalf("refused config rewritten to %q, want it untouched", saved)
			}
		})
	}
}
