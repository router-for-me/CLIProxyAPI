package pluginstore

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
)

func TestInstallBlocksLoadedWindowsPlugin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		goos        string
		loaded      bool
		wantBlocked bool
	}{
		{name: "windows loaded", goos: "windows", loaded: true, wantBlocked: true},
		{name: "windows not loaded", goos: "windows", loaded: false, wantBlocked: false},
		{name: "linux loaded", goos: "linux", loaded: true, wantBlocked: false},
		{name: "darwin loaded", goos: "darwin", loaded: true, wantBlocked: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, errInstall := Client{HTTPClient: failingHTTPDoer{}}.Install(context.Background(), testPlugin(), InstallOptions{
				PluginsDir:   t.TempDir(),
				GOOS:         tt.goos,
				GOARCH:       "amd64",
				PluginLoaded: func() bool { return tt.loaded },
			})
			if errInstall == nil {
				t.Fatal("Install() error = nil")
			}
			if gotBlocked := errors.Is(errInstall, ErrLoadedPluginLocked); gotBlocked != tt.wantBlocked {
				t.Fatalf("Install() error = %v, blocked = %v, want %v", errInstall, gotBlocked, tt.wantBlocked)
			}
		})
	}
}

func TestInstallArchiveBlocksLoadedWindowsPluginBeforeWrite(t *testing.T) {
	t.Parallel()

	_, errInstall := InstallArchive(makeZip(t, map[string]string{
		"sample-provider.dll": "library-data",
	}), testPlugin(), InstallOptions{
		PluginsDir:   t.TempDir(),
		GOOS:         "windows",
		GOARCH:       "amd64",
		PluginLoaded: func() bool { return true },
	})
	if !errors.Is(errInstall, ErrLoadedPluginLocked) {
		t.Fatalf("InstallArchive() error = %v, want ErrLoadedPluginLocked", errInstall)
	}
}

func TestInstallArchivePreparesLoadedWindowsPluginBeforeWrite(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	targetDir := filepath.Join(root, "windows", "amd64")
	if errMkdir := os.MkdirAll(targetDir, 0o755); errMkdir != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdir)
	}
	targetPath := filepath.Join(targetDir, "sample-provider.dll")
	if errWrite := os.WriteFile(targetPath, []byte("old"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	loaded := true
	prepared := false

	result, errInstall := InstallArchive(makeZip(t, map[string]string{
		"sample-provider.dll": "new",
	}), testPlugin(), InstallOptions{
		PluginsDir:   root,
		GOOS:         "windows",
		GOARCH:       "amd64",
		PluginLoaded: func() bool { return loaded },
		BeforeWrite: func() error {
			prepared = true
			loaded = false
			return nil
		},
	})
	if errInstall != nil {
		t.Fatalf("InstallArchive() error = %v", errInstall)
	}
	if !prepared {
		t.Fatal("BeforeWrite was not called")
	}
	if !result.Overwritten {
		t.Fatal("Overwritten = false, want true")
	}
	data, errRead := os.ReadFile(targetPath)
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	if string(data) != "new" {
		t.Fatalf("installed data = %q, want new", data)
	}
}

func TestInstallArchiveWritesPlatformPlugin(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	result, errInstall := InstallArchive(makeZip(t, map[string]string{
		"README.md":             "ignored",
		"sample-provider.dylib": "library-data",
	}), testPlugin(), InstallOptions{PluginsDir: root, GOOS: "darwin", GOARCH: "arm64"})
	if errInstall != nil {
		t.Fatalf("InstallArchive() error = %v", errInstall)
	}
	wantPath := filepath.Join(root, "darwin", "arm64", "sample-provider.dylib")
	if result.Path != wantPath {
		t.Fatalf("Path = %q, want %q", result.Path, wantPath)
	}
	data, errRead := os.ReadFile(wantPath)
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	if string(data) != "library-data" {
		t.Fatalf("installed data = %q", data)
	}
}

func TestInstallArchiveReportsOverwrite(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	targetDir := filepath.Join(root, "darwin", "arm64")
	if errMkdir := os.MkdirAll(targetDir, 0o755); errMkdir != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdir)
	}
	if errWrite := os.WriteFile(filepath.Join(targetDir, "sample-provider.dylib"), []byte("old"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	result, errInstall := InstallArchive(makeZip(t, map[string]string{
		"sample-provider.dylib": "new",
	}), testPlugin(), InstallOptions{PluginsDir: root, GOOS: "darwin", GOARCH: "arm64"})
	if errInstall != nil {
		t.Fatalf("InstallArchive() error = %v", errInstall)
	}
	if !result.Overwritten {
		t.Fatal("Overwritten = false, want true")
	}
}

func TestInstallArchiveOverwritesRuntimeSelectedPlugin(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	existingPath := filepath.Join(root, "sample-provider"+pluginhost.PluginExtension(runtime.GOOS))
	if errWrite := os.WriteFile(existingPath, []byte("old"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}

	result, errInstall := InstallArchive(makeZip(t, map[string]string{
		"sample-provider" + pluginhost.PluginExtension(runtime.GOOS): "new",
	}), testPlugin(), InstallOptions{PluginsDir: root, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH})
	if errInstall != nil {
		t.Fatalf("InstallArchive() error = %v", errInstall)
	}
	if result.Path != existingPath {
		t.Fatalf("Path = %q, want selected runtime plugin %q", result.Path, existingPath)
	}
	if !result.Overwritten {
		t.Fatal("Overwritten = false, want true")
	}
	data, errRead := os.ReadFile(existingPath)
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	if string(data) != "new" {
		t.Fatalf("installed data = %q, want new", data)
	}
}

func TestInstallArchiveRejectsUnsafeArchives(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		files   map[string]string
		wantErr string
	}{
		{
			name:    "zip slip",
			files:   map[string]string{"../sample-provider.dylib": "library"},
			wantErr: "escapes archive root",
		},
		{
			name:    "absolute path",
			files:   map[string]string{"/sample-provider.dylib": "library"},
			wantErr: "is absolute",
		},
		{
			name:    "nested target",
			files:   map[string]string{"nested/sample-provider.dylib": "library"},
			wantErr: "zip root",
		},
		{
			name:    "extension mismatch",
			files:   map[string]string{"sample-provider.so": "library"},
			wantErr: "sample-provider.dylib",
		},
		{
			name:    "filename mismatch",
			files:   map[string]string{"other.dylib": "library"},
			wantErr: "sample-provider.dylib",
		},
		{
			name:    "missing target",
			files:   map[string]string{"README.md": "library"},
			wantErr: "does not contain",
		},
		{
			name: "multiple targets",
			files: map[string]string{
				"sample-provider.dylib": "library",
				"copy.dylib":            "library",
			},
			wantErr: "sample-provider.dylib",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, errInstall := InstallArchive(makeZip(t, tt.files), testPlugin(), InstallOptions{PluginsDir: t.TempDir(), GOOS: "darwin", GOARCH: "arm64"})
			if errInstall == nil {
				t.Fatal("InstallArchive() error = nil")
			}
			if !strings.Contains(errInstall.Error(), tt.wantErr) {
				t.Fatalf("InstallArchive() error = %v, want substring %q", errInstall, tt.wantErr)
			}
		})
	}
}

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range files {
		file, errCreate := writer.Create(name)
		if errCreate != nil {
			t.Fatalf("Create(%s) error = %v", name, errCreate)
		}
		if _, errWrite := file.Write([]byte(content)); errWrite != nil {
			t.Fatalf("Write(%s) error = %v", name, errWrite)
		}
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}
	return buffer.Bytes()
}

type failingHTTPDoer struct{}

func (failingHTTPDoer) Do(*http.Request) (*http.Response, error) {
	return nil, errors.New("network unavailable")
}

func testPlugin() Plugin {
	return Plugin{
		ID:          "sample-provider",
		Name:        "Sample Provider",
		Description: "Adds sample provider support.",
		Author:      "author-name",
		Version:     "0.1.0",
		Repository:  "https://github.com/author-name/cliproxy-sample-provider-plugin",
	}
}
