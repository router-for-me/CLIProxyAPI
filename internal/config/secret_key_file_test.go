package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

const testBcryptSecret = "$2a$10$abcdefghijklmnopqrstuuabcdefghijklmnopqrstuvwxyz01234"

// secretKeysAt returns the secret at each path of data that has one.
func secretKeysAt(t *testing.T, data []byte) map[string]string {
	t.Helper()
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatalf("parse %q: %v", data, err)
	}
	secrets := make(map[string]string)
	if len(root.Content) == 0 {
		return secrets
	}
	doc := expandConfigAliases(root.Content[0])
	for _, path := range secretKeyPaths {
		if secret := yamlPath(doc, path); secret != nil {
			secrets[path] = secret.Value
		}
	}
	return secrets
}

// The file must leave the machine with no plaintext secret at any path the loader
// can read one from, whatever the file's layout.
func TestHashSecretKeysInFileHashesEveryPlaintextSecret(t *testing.T) {
	for name, content := range map[string]string{
		"legacy path": "remote-management:\n  secret-key: plain-secret\n",
		"v8 path":     "management:\n  secret-key: plain-secret\n",
		// The loader reads the alias; the anchored copy must be hashed too.
		"anchor and alias": "remote-management: &rm\n  secret-key: plain-secret\nmanagement: *rm\n",
		// The loader reads the empty v8 secret; the legacy plaintext still leaves.
		"both paths": "management:\n  secret-key: \"\"\nremote-management:\n  secret-key: plain-secret\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("# operator note\nport: 8317\n"+content), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := HashSecretKeysInFile(path)
			if err != nil {
				t.Fatalf("HashSecretKeysInFile() error = %v", err)
			}
			saved, _ := os.ReadFile(path)
			if !bytes.Equal(got, saved) {
				t.Fatalf("returned %q, but the file holds %q", got, saved)
			}
			if strings.Contains(string(saved), "plain-secret") || !strings.Contains(string(saved), "# operator note") {
				t.Fatalf("file after hashing = %q, want every secret hashed and the comment kept", saved)
			}
			secrets := secretKeysAt(t, saved)
			if len(secrets) == 0 {
				t.Fatalf("no secret left in %q", saved)
			}
			for secretPath, secret := range secrets {
				if secret != "" && bcrypt.CompareHashAndPassword([]byte(secret), []byte("plain-secret")) != nil {
					t.Fatalf("%s = %q, want the bcrypt hash of the plaintext", secretPath, secret)
				}
			}
		})
	}
}

// Without a plaintext secret the file is uploaded exactly as today, even one the
// full config parser rejects.
func TestHashSecretKeysInFileLeavesFilesWithoutPlaintextUntouched(t *testing.T) {
	for name, content := range map[string]string{
		"hashed secret":            "management:\n  secret-key: '" + testBcryptSecret + "'\n",
		"no secret":                "port: 8317\n",
		"empty file":               "",
		"hashed but invalid model": "management:\n  secret-key: '" + testBcryptSecret + "'\nmodels: [{name: }]\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := HashSecretKeysInFile(path)
			if err != nil {
				t.Fatalf("HashSecretKeysInFile() error = %v", err)
			}
			saved, _ := os.ReadFile(path)
			if !bytes.Equal(saved, []byte(content)) || !bytes.Equal(got, []byte(content)) {
				t.Fatalf("file %q, returned %q; want both untouched", saved, got)
			}
		})
	}
}

// A file that cannot be parsed or written, whose secret is not a string, or that
// keeps a hashed secret's plaintext elsewhere may still carry the plaintext, so
// the caller must hear about it.
func TestHashSecretKeysInFileReportsWhenPlaintextMayRemain(t *testing.T) {
	for name, content := range map[string]string{
		"not yaml":          "management: [secret-key: plain-secret\n",
		"secret not string": "management:\n  secret-key: {value: plain-secret}\n",
		// Hashing the merged secret leaves the anchor, which other settings may use.
		"merged from a shared anchor": "defaults: &d\n  secret-key: plain-secret\nmanagement:\n  <<: *d\n",
		"aliased by another setting":  "defaults: &d\n  secret-key: plain-secret\nmanagement: *d\nother: *d\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := HashSecretKeysInFile(path); err == nil {
				t.Fatal("HashSecretKeysInFile() reported success")
			}
		})
	}

	t.Run("read-only file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		content := []byte("management:\n  secret-key: plain-secret\n")
		if err := os.WriteFile(path, content, 0o400); err != nil {
			t.Fatal(err)
		}
		if f, errOpen := os.OpenFile(path, os.O_WRONLY, 0); errOpen == nil {
			_ = f.Close()
			t.Skip("file stays writable (running as root)")
		}
		if _, err := HashSecretKeysInFile(path); err == nil {
			t.Fatal("HashSecretKeysInFile() reported success for a file it could not write")
		}
		if saved, _ := os.ReadFile(path); !bytes.Equal(saved, content) {
			t.Fatalf("read-only file changed to %q", saved)
		}
	})
}

// A startup load hashes the secrets the same way and keeps the hash it wrote, so
// the parsed config and the file agree.
func TestLoadConfigKeepsTheHashItWritesBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("remote-management: &rm\n  secret-key: plain-secret\nmanagement: *rm\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	saved, _ := os.ReadFile(path)
	if strings.Contains(string(saved), "plain-secret") {
		t.Fatalf("plaintext secret left in the file: %q", saved)
	}
	if secrets := secretKeysAt(t, saved); cfg.RemoteManagement.SecretKey != secrets["management.secret-key"] {
		t.Fatalf("parsed secret %q differs from the file's %q", cfg.RemoteManagement.SecretKey, secrets["management.secret-key"])
	}
	if bcrypt.CompareHashAndPassword([]byte(cfg.RemoteManagement.SecretKey), []byte("plain-secret")) != nil {
		t.Fatalf("parsed secret %q is not the bcrypt hash of the plaintext", cfg.RemoteManagement.SecretKey)
	}
}
