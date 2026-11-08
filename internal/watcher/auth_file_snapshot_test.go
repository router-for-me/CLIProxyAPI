package watcher

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestSnapshotAuthFileRelativeAuthDir(t *testing.T) {
	// Keep the fixture on the working directory's volume without changing cwd.
	authDir, errMkdirTemp := os.MkdirTemp(".", "snapshot-relative-authdir-")
	if errMkdirTemp != nil {
		t.Fatal(errMkdirTemp)
	}
	t.Cleanup(func() {
		if errRemoveAll := os.RemoveAll(authDir); errRemoveAll != nil {
			t.Errorf("remove auth directory: %v", errRemoveAll)
		}
	})
	absoluteAuthDir, errAbs := filepath.Abs(authDir)
	if errAbs != nil {
		t.Fatal(errAbs)
	}
	absolutePath := filepath.Join(absoluteAuthDir, "account.json")
	if errWriteFile := os.WriteFile(absolutePath, []byte(`{"type":"codex"}`), 0o600); errWriteFile != nil {
		t.Fatal(errWriteFile)
	}
	w := &Watcher{authDir: authDir, config: &config.Config{AuthDir: authDir}}
	for _, path := range []string{absolutePath, filepath.Join(authDir, "account.json")} {
		auths, errSnapshotAuthFile := w.SnapshotAuthFile(path)
		if errSnapshotAuthFile != nil {
			t.Fatal(errSnapshotAuthFile)
		}
		if len(auths) != 1 {
			t.Fatalf("snapshot path=%q: got %d auths, want 1", path, len(auths))
		}
		if auths[0].ID != "account.json" || auths[0].Attributes[coreauth.AttributePath] != absolutePath {
			t.Fatalf("snapshot path=%q: ID=%q, source=%q; want account.json from %q", path, auths[0].ID, auths[0].Attributes[coreauth.AttributePath], absolutePath)
		}
	}
}

func TestSnapshotAuthFile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		data    string
		parser  synthesizer.PluginAuthParser
		wantID  string
		wantErr bool
		missing bool
	}{
		{name: "builtin_relative_id", data: `{"type":"codex"}`, wantID: "account.json"},
		{name: "plugin_id", data: `{"type":"plugin","ids":["b"]}`, parser: &fileDeleteTestParser{}, wantID: "b"},
		{name: "empty", data: ""},
		{name: "malformed_json", data: `{"type":`, wantErr: true},
		{name: "empty_plugin_result", data: `{"type":"plugin","ids":[]}`, parser: &fileDeleteTestParser{}},
		{name: "plugin_error_no_builtin_fallback", data: `{"type":"codex"}`, parser: &fileDeleteTestParser{err: fmt.Errorf("simulated parser failure")}, wantErr: true},
		{name: "missing_file", missing: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "account.json")
			if !tc.missing {
				if errWriteFile := os.WriteFile(path, []byte(tc.data), 0o600); errWriteFile != nil {
					t.Fatal(errWriteFile)
				}
			}
			// Another file must never contribute IDs to the single-file snapshot.
			otherPath := filepath.Join(dir, "other.json")
			if errWriteFile := os.WriteFile(otherPath, []byte(`{"type":"plugin","ids":["a"]}`), 0o600); errWriteFile != nil {
				t.Fatal(errWriteFile)
			}
			w := &Watcher{authDir: dir, config: &config.Config{AuthDir: dir}, pluginAuthParser: tc.parser}
			auths, errSnapshotAuthFile := w.SnapshotAuthFile(path)
			if (errSnapshotAuthFile != nil) != tc.wantErr {
				t.Fatalf("snapshot error=%v, want error=%t", errSnapshotAuthFile, tc.wantErr)
			}
			if tc.wantID == "" {
				if len(auths) != 0 {
					t.Fatalf("expected no reliable auths, got %+v", auths)
				}
				return
			}
			if len(auths) != 1 || auths[0].ID != tc.wantID || auths[0].Attributes[coreauth.AttributePath] != path {
				t.Fatalf("snapshot=%+v, want only ID %q from %q", auths, tc.wantID, path)
			}
		})
	}
}
