package store

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const seedTestBcryptSecret = "$2a$10$abcdefghijklmnopqrstuuabcdefghijklmnopqrstuvwxyz01234"

// seedCases are local configs and whether seeding must upload them unchanged
// (true) or hashed (false).
var seedCases = map[string]struct {
	content   string
	unchanged bool
}{
	"legacy plaintext":         {content: "remote-management:\n  secret-key: plain-secret\n"},
	"anchored plaintext":       {content: "remote-management: &rm\n  secret-key: plain-secret\nmanagement: *rm\n"},
	"plaintext under both":     {content: "management:\n  secret-key: \"\"\nremote-management:\n  secret-key: plain-secret\n"},
	"hashed":                   {content: "management:\n  secret-key: '" + seedTestBcryptSecret + "'\n", unchanged: true},
	"hashed but invalid model": {content: "management:\n  secret-key: '" + seedTestBcryptSecret + "'\nmodels: [{name: }]\n", unchanged: true},
}

func writeSeedConfig(t *testing.T, content string, perm os.FileMode) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func checkSeeded(t *testing.T, content string, unchanged bool, seeded []byte) {
	t.Helper()
	if unchanged {
		if !bytes.Equal(seeded, []byte(content)) {
			t.Fatalf("seeded %q, want the local config unchanged %q", seeded, content)
		}
		return
	}
	if strings.Contains(string(seeded), "plain-secret") {
		t.Fatalf("seeded a plaintext secret: %q", seeded)
	}
}

// Seeding an empty remote store uploads the local config with every management
// secret hashed, in the file too, so the local and remote copies match; a config
// without a plaintext secret is uploaded as today.
func TestHashLocalConfigForSeeding(t *testing.T) {
	for name, tc := range seedCases {
		t.Run(name, func(t *testing.T) {
			configPath := writeSeedConfig(t, tc.content, 0o600)
			data, ok := hashLocalConfigForSeeding(configPath, "test store")
			if !ok {
				t.Fatal("hashLocalConfigForSeeding() refused to seed")
			}
			checkSeeded(t, tc.content, tc.unchanged, data)
			if local, _ := os.ReadFile(configPath); !bytes.Equal(local, data) {
				t.Fatalf("seed data %q differs from the local file %q", data, local)
			}
		})
	}
}

// A config that may still hold a plaintext secret is not uploaded.
func TestHashLocalConfigForSeedingRefusesPossiblePlaintext(t *testing.T) {
	t.Run("shared anchor keeps the plaintext", func(t *testing.T) {
		configPath := writeSeedConfig(t, "defaults: &d\n  secret-key: plain-secret\nmanagement:\n  <<: *d\n", 0o600)
		if data, ok := hashLocalConfigForSeeding(configPath, "test store"); ok || data != nil {
			t.Fatalf("hashLocalConfigForSeeding() = ok %v, data %q; want nothing to upload", ok, data)
		}
	})
	t.Run("file cannot be written", func(t *testing.T) {
		configPath := writeSeedConfig(t, "remote-management:\n  secret-key: plain-secret\n", 0o400)
		if f, errOpen := os.OpenFile(configPath, os.O_WRONLY, 0); errOpen == nil {
			_ = f.Close()
			t.Skip("file stays writable (running as root)")
		}
		if data, ok := hashLocalConfigForSeeding(configPath, "test store"); ok || data != nil {
			t.Fatalf("hashLocalConfigForSeeding() = ok %v, data %q; want nothing to upload", ok, data)
		}
	})
}

// An object store whose bucket has no config is seeded from the local file.
func TestObjectTokenStoreSeedsBucketWithoutPlaintextSecret(t *testing.T) {
	// The bucket answers every request with 404, so the config is missing.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	client, err := minio.New(strings.TrimPrefix(server.URL, "http://"), &minio.Options{
		Creds:  credentials.NewStaticV4("access", "secret", ""),
		Region: "us-east-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range seedCases {
		t.Run(name, func(t *testing.T) {
			var uploaded [][]byte
			store := &ObjectTokenStore{
				client:     client,
				cfg:        ObjectStoreConfig{Bucket: "bucket"},
				configPath: writeSeedConfig(t, tc.content, 0o600),
				putObjectFn: func(_ context.Context, key string, data []byte, _ string) error {
					if key == objectStoreConfigKey {
						uploaded = append(uploaded, data)
					}
					return nil
				},
			}
			if errSync := store.syncConfigFromBucket(context.Background(), ""); errSync != nil {
				t.Fatalf("syncConfigFromBucket() error = %v", errSync)
			}
			if len(uploaded) != 1 {
				t.Fatalf("config uploads = %d, want 1", len(uploaded))
			}
			checkSeeded(t, tc.content, tc.unchanged, uploaded[0])
		})
	}
}
