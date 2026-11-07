package store

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/authpublication"
)

func TestObjectStoreBootstrapReconcilesSymlinkPublication(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(fmt.Sprintf("confirmed=%t", confirmed), func(t *testing.T) {
			root := t.TempDir()
			authDir := filepath.Join(root, "auths")
			realDir := filepath.Join(root, "real-auths")
			if err := os.MkdirAll(realDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(realDir, authDir); err != nil {
				if runtime.GOOS == "windows" {
					t.Skipf("symlink unavailable: %v", err)
				}
				t.Fatal(err)
			}
			committed := []byte(`{"type":"codex","access_token":"newer-remote"}`)
			configData := []byte("port: 8317\n")
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var data []byte
				switch r.URL.Path {
				case "/credentials/":
					if r.Method == http.MethodHead {
						return
					}
					if r.Method != http.MethodGet || r.URL.Query().Get("prefix") != "auths/" {
						t.Errorf("unexpected bucket request: %s %s", r.Method, r.URL)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					data = []byte(fmt.Sprintf(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>credentials</Name><Prefix>auths/</Prefix><IsTruncated>false</IsTruncated><Contents><Key>auths/account.json</Key><LastModified>2026-01-01T00:00:00Z</LastModified><ETag>"remote"</ETag><Size>%d</Size></Contents></ListBucketResult>`, len(committed)))
					w.Header().Set("Content-Type", "application/xml")
				case "/credentials/config/config.yaml":
					data = configData
				case "/credentials/auths/account.json":
					data = committed
				default:
					t.Errorf("unexpected startup request: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("Last-Modified", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(http.TimeFormat))
				w.Header().Set("ETag", `"remote"`)
				w.Header().Set("Content-Length", strconv.Itoa(len(data)))
				if r.Method != http.MethodHead {
					_, _ = w.Write(data)
				}
			}))
			defer remote.Close()
			s, err := NewObjectTokenStore(ObjectStoreConfig{Endpoint: strings.TrimPrefix(remote.URL, "http://"), Bucket: "credentials",
				AccessKey: "test-key", SecretKey: "test-secret", Region: "us-east-1", LocalRoot: root, PathStyle: true})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(authDir, "account.json")
			pending, err := authpublication.Prepare(path, []byte(`{"type":"codex","access_token":"old-overlay"}`))
			if err != nil {
				t.Fatal(err)
			}
			if confirmed {
				if errCommit := pending.Commit(); errCommit != nil {
					t.Fatal(errCommit)
				}
			}
			outside := filepath.Join(t.TempDir(), "outside.json")
			outsidePending, err := authpublication.Prepare(outside, []byte(`{"type":"codex","access_token":"unrelated"}`))
			if err != nil {
				t.Fatal(err)
			}
			if errCommit := outsidePending.Commit(); errCommit != nil {
				t.Fatal(errCommit)
			}
			if errSymlink := os.Symlink(filepath.Dir(outside), filepath.Join(authDir, "external")); errSymlink != nil {
				t.Fatal(errSymlink)
			}
			if errBootstrap := s.Bootstrap(context.Background(), ""); errBootstrap != nil {
				t.Fatal(errBootstrap)
			}
			if !authpublication.HasPending(outside) {
				t.Fatal("reconciliation followed a nested symlink and removed an unrelated publication")
			}
			if mirror, errRead := os.ReadFile(path); errRead != nil || !bytes.Equal(mirror, committed) {
				t.Fatalf("startup did not load the actual backend credential: %q, %v", mirror, errRead)
			}
			if effective, errRead := authpublication.ReadFile(path); errRead != nil || !bytes.Equal(effective, committed) || authpublication.HasPending(path) {
				t.Fatalf("startup retained a stale publication: %q, %v", effective, errRead)
			}
			if errWrite := os.WriteFile(path, []byte(`{"type":"codex","access_token":"edited"}`), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			if effective, errRead := authpublication.ReadFile(path); errRead != nil || !bytes.Contains(effective, []byte("edited")) {
				t.Fatalf("startup reconciliation still blocks ordinary edits: %q, %v", effective, errRead)
			}
		})
	}
}
