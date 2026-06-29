package pluginstore

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallBlocksLoadedWindowsPlugin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		goos        string
		loaded      bool
		wantBlocked bool
	}{
		{name: "windows loaded", goos: "windows", loaded: true, wantBlocked: false},
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

	root := t.TempDir()
	targetDir := filepath.Join(root, "windows", "amd64")
	if errMkdir := os.MkdirAll(targetDir, 0o755); errMkdir != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdir)
	}
	if errWrite := os.WriteFile(filepath.Join(targetDir, "sample-provider-v0.1.0.dll"), []byte("old"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	_, errInstall := InstallArchive(makeZip(t, map[string]string{
		"sample-provider.dll": "library-data",
	}), testPlugin(), InstallOptions{
		PluginsDir:   root,
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
	targetPath := filepath.Join(targetDir, "sample-provider-v0.1.0.dll")
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

func TestInstallArchiveSkipsIdenticalLoadedWindowsPlugin(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	targetDir := filepath.Join(root, "windows", "amd64")
	if errMkdir := os.MkdirAll(targetDir, 0o755); errMkdir != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdir)
	}
	targetPath := filepath.Join(targetDir, "sample-provider-v0.1.0.dll")
	if errWrite := os.WriteFile(targetPath, []byte("same"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	beforeWriteCalled := false

	result, errInstall := InstallArchive(makeZip(t, map[string]string{
		"sample-provider.dll": "same",
	}), testPlugin(), InstallOptions{
		PluginsDir:   root,
		GOOS:         "windows",
		GOARCH:       "amd64",
		PluginLoaded: func() bool { return true },
		BeforeWrite: func() error {
			beforeWriteCalled = true
			return errors.New("before write should not run")
		},
	})
	if errInstall != nil {
		t.Fatalf("InstallArchive() error = %v", errInstall)
	}
	if beforeWriteCalled {
		t.Fatal("BeforeWrite was called for identical artifact")
	}
	if !result.Overwritten {
		t.Fatal("Overwritten = false, want true")
	}
	if !result.Skipped {
		t.Fatal("Skipped = false, want true")
	}
	data, errRead := os.ReadFile(targetPath)
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	if string(data) != "same" {
		t.Fatalf("installed data = %q, want same", data)
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
	wantPath := filepath.Join(root, "darwin", "arm64", "sample-provider-v0.1.0.dylib")
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
	if errWrite := os.WriteFile(filepath.Join(targetDir, "sample-provider-v0.1.0.dylib"), []byte("old"), 0o644); errWrite != nil {
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
	existingPath := filepath.Join(root, runtime.GOOS, runtime.GOARCH, "sample-provider-v0.1.0"+pluginExtension(runtime.GOOS))
	if errMkdir := os.MkdirAll(filepath.Dir(existingPath), 0o755); errMkdir != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdir)
	}
	if errWrite := os.WriteFile(existingPath, []byte("old"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}

	result, errInstall := InstallArchive(makeZip(t, map[string]string{
		"sample-provider" + pluginExtension(runtime.GOOS): "new",
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

func TestInstallUsesLatestReleaseVersion(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	archiveData := makeZip(t, map[string]string{"sample-provider.dylib": "library-data"})
	archiveName := "sample-provider_0.2.0_darwin_arm64.zip"
	checksum := sha256.Sum256(archiveData)
	client := Client{HTTPClient: mapHTTPDoer{
		"https://api.github.com/repos/author-name/cliproxy-sample-provider-plugin/releases/latest": []byte(`{
			"tag_name": "v0.2.0",
			"assets": [
				{"name": "` + archiveName + `", "browser_download_url": "https://downloads.example/` + archiveName + `"},
				{"name": "checksums.txt", "browser_download_url": "https://downloads.example/checksums.txt"}
			]
		}`),
		"https://downloads.example/" + archiveName: archiveData,
		"https://downloads.example/checksums.txt":  []byte(hex.EncodeToString(checksum[:]) + "  " + archiveName + "\n"),
	}}

	result, errInstall := client.Install(context.Background(), testPlugin(), InstallOptions{
		PluginsDir: root,
		GOOS:       "darwin",
		GOARCH:     "arm64",
	})
	if errInstall != nil {
		t.Fatalf("Install() error = %v", errInstall)
	}
	if result.Version != "0.2.0" {
		t.Fatalf("Version = %q, want 0.2.0 from latest release tag", result.Version)
	}
	data, errRead := os.ReadFile(filepath.Join(root, "darwin", "arm64", "sample-provider-v0.2.0.dylib"))
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	if string(data) != "library-data" {
		t.Fatalf("installed data = %q", data)
	}
}

func TestInstallDownloadsReleaseAssetsViaAPIURL(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	archiveData := makeZip(t, map[string]string{"sample-provider.dylib": "library-data"})
	archiveName := "sample-provider_0.2.0_darwin_arm64.zip"
	checksum := sha256.Sum256(archiveData)
	client := Client{HTTPClient: mapHTTPDoer{
		"https://api.github.com/repos/author-name/cliproxy-sample-provider-plugin/releases/latest": []byte(`{
			"tag_name": "v0.2.0",
			"assets": [
				{
					"name": "` + archiveName + `",
					"url": "https://api.github.com/repos/author-name/cliproxy-sample-provider-plugin/releases/assets/1",
					"browser_download_url": "https://downloads.example/missing.zip"
				},
				{
					"name": "checksums.txt",
					"url": "https://api.github.com/repos/author-name/cliproxy-sample-provider-plugin/releases/assets/2",
					"browser_download_url": "https://downloads.example/missing-checksums.txt"
				}
			]
		}`),
		"https://api.github.com/repos/author-name/cliproxy-sample-provider-plugin/releases/assets/1": archiveData,
		"https://api.github.com/repos/author-name/cliproxy-sample-provider-plugin/releases/assets/2": []byte(hex.EncodeToString(checksum[:]) + "  " + archiveName + "\n"),
	}}

	result, errInstall := client.Install(context.Background(), testPlugin(), InstallOptions{
		PluginsDir: root,
		GOOS:       "darwin",
		GOARCH:     "arm64",
	})
	if errInstall != nil {
		t.Fatalf("Install() error = %v", errInstall)
	}
	if result.Version != "0.2.0" {
		t.Fatalf("Version = %q, want 0.2.0 from latest release tag", result.Version)
	}
	data, errRead := os.ReadFile(filepath.Join(root, "darwin", "arm64", "sample-provider-v0.2.0.dylib"))
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	if string(data) != "library-data" {
		t.Fatalf("installed data = %q, want library-data", data)
	}
}

func TestInstallVersionUsesPinnedReleaseTag(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	archiveData := makeZip(t, map[string]string{"sample-provider.so": "library-data"})
	archiveName := "sample-provider_0.3.0_linux_amd64.zip"
	checksum := sha256.Sum256(archiveData)
	client := Client{HTTPClient: mapHTTPDoer{
		"https://api.github.com/repos/author-name/cliproxy-sample-provider-plugin/releases/tags/v0.3.0": []byte(`{
			"tag_name": "v0.3.0",
			"assets": [
				{"name": "` + archiveName + `", "browser_download_url": "https://downloads.example/` + archiveName + `"},
				{"name": "checksums.txt", "browser_download_url": "https://downloads.example/checksums.txt"}
			]
		}`),
		"https://downloads.example/" + archiveName: archiveData,
		"https://downloads.example/checksums.txt":  []byte(hex.EncodeToString(checksum[:]) + "  " + archiveName + "\n"),
	}}

	result, errInstall := client.InstallVersion(context.Background(), testPlugin(), "v0.3.0", "0.3.0", InstallOptions{
		PluginsDir: root,
		GOOS:       "linux",
		GOARCH:     "amd64",
	})
	if errInstall != nil {
		t.Fatalf("InstallVersion() error = %v", errInstall)
	}
	if result.Version != "0.3.0" {
		t.Fatalf("Version = %q, want 0.3.0", result.Version)
	}
	data, errRead := os.ReadFile(filepath.Join(root, "linux", "amd64", "sample-provider-v0.3.0.so"))
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	if string(data) != "library-data" {
		t.Fatalf("installed data = %q", data)
	}
}

func TestInstallManifestResolvesDirectArtifactsFromSource(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	archiveData := makeZip(t, map[string]string{"sample-provider.so": "library-data"})
	checksum := sha256.Sum256(archiveData)
	registryURL := "https://registry.example/registry.json"
	artifactURL := "https://downloads.example/sample-provider_0.4.0_linux_amd64.zip"
	latestArtifactURL := "https://downloads.example/sample-provider_0.5.0_linux_amd64.zip"
	client := Client{HTTPClient: mapHTTPDoer{
		registryURL: []byte(`{
			"schema_version": 2,
			"plugins": [{
				"id": "sample-provider",
				"name": "Sample Provider",
				"description": "Adds sample provider support.",
				"author": "author-name",
				"version": "0.5.0",
				"install": {
					"type": "direct",
					"artifacts": [{
						"goos": "linux",
						"goarch": "amd64",
						"url": "` + latestArtifactURL + `",
						"sha256": "` + hex.EncodeToString(checksum[:]) + `"
					}]
				},
				"versions": [{
					"version": "0.4.0",
					"install": {
						"type": "direct",
						"artifacts": [{
							"goos": "linux",
							"goarch": "amd64",
							"url": "` + artifactURL + `",
							"sha256": "` + hex.EncodeToString(checksum[:]) + `"
						}]
					}
				}]
			}]
		}`),
		artifactURL: archiveData,
	}}

	result, errInstall := client.InstallManifest(context.Background(), Manifest{
		SchemaVersion: SchemaVersionV2,
		ID:            "sample-provider",
		Version:       "0.4.0",
		SourceURL:     registryURL,
		Install:       InstallPlan{Type: InstallTypeDirect},
	}, InstallOptions{
		PluginsDir: root,
		GOOS:       "linux",
		GOARCH:     "amd64",
	})
	if errInstall != nil {
		t.Fatalf("InstallManifest() error = %v", errInstall)
	}
	if result.InstallType != InstallTypeDirect || result.Version != "0.4.0" {
		t.Fatalf("result = %#v, want direct 0.4.0", result)
	}
	data, errRead := os.ReadFile(filepath.Join(root, "linux", "amd64", "sample-provider-v0.4.0.so"))
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	if string(data) != "library-data" {
		t.Fatalf("installed data = %q", data)
	}
}

func TestInstallDirectDownloadsMatchingArtifactWithBearerAuth(t *testing.T) {
	t.Setenv("PLUGIN_STORE_TOKEN", "secret-token")
	root := t.TempDir()
	archiveData := makeZip(t, map[string]string{"sample-provider.so": "library-data"})
	checksum := sha256.Sum256(archiveData)
	artifactURL := "https://downloads.example/private/sample-provider_0.4.0_linux_amd64.zip"
	client := Client{
		HTTPClient: authCheckingHTTPDoer{
			url:           artifactURL,
			wantAuth:      "Bearer secret-token",
			responseBytes: archiveData,
		},
		Auth: []AuthConfig{{
			Match:    "https://downloads.example/private/",
			ApplyTo:  []string{RequestKindArtifact},
			Type:     AuthTypeBearer,
			TokenEnv: "PLUGIN_STORE_TOKEN",
		}},
	}

	plugin := testPlugin()
	plugin.Version = "0.4.0"
	plugin.Install = InstallPlan{
		Type: InstallTypeDirect,
		Artifacts: []Artifact{{
			GOOS:   "linux",
			GOARCH: "amd64",
			URL:    artifactURL,
			SHA256: hex.EncodeToString(checksum[:]),
		}},
	}
	result, errInstall := client.Install(context.Background(), plugin, InstallOptions{
		PluginsDir: root,
		GOOS:       "linux",
		GOARCH:     "amd64",
	})
	if errInstall != nil {
		t.Fatalf("Install() error = %v", errInstall)
	}
	if result.InstallType != InstallTypeDirect || result.Version != "0.4.0" {
		t.Fatalf("result = %#v, want direct 0.4.0", result)
	}
	data, errRead := os.ReadFile(filepath.Join(root, "linux", "amd64", "sample-provider-v0.4.0.so"))
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	if string(data) != "library-data" {
		t.Fatalf("installed data = %q", data)
	}
}

func TestInstallDirectRejectsChecksumMismatch(t *testing.T) {
	t.Parallel()

	archiveData := makeZip(t, map[string]string{"sample-provider.so": "library-data"})
	client := Client{HTTPClient: mapHTTPDoer{
		"https://downloads.example/sample-provider.zip": archiveData,
	}}
	plugin := testPlugin()
	plugin.Version = "0.4.0"
	plugin.Install = InstallPlan{
		Type: InstallTypeDirect,
		Artifacts: []Artifact{{
			GOOS:   "linux",
			GOARCH: "amd64",
			URL:    "https://downloads.example/sample-provider.zip",
			SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		}},
	}
	_, errInstall := client.Install(context.Background(), plugin, InstallOptions{
		PluginsDir: t.TempDir(),
		GOOS:       "linux",
		GOARCH:     "amd64",
	})
	if errInstall == nil {
		t.Fatal("Install() error = nil")
	}
	if !strings.Contains(errInstall.Error(), "checksum mismatch") {
		t.Fatalf("Install() error = %v, want checksum mismatch", errInstall)
	}
}

func TestDownloadArtifactEnforcesDeclaredSizeDuringRead(t *testing.T) {
	t.Parallel()

	body := &trackingReadCloser{data: []byte("0123456789")}
	sum := sha256.Sum256(body.data)
	client := Client{HTTPClient: singleResponseHTTPDoer{body: body}}
	_, errDownload := client.DownloadArtifact(context.Background(), Artifact{
		GOOS:   "linux",
		GOARCH: "amd64",
		URL:    "https://downloads.example/sample-provider.zip",
		SHA256: hex.EncodeToString(sum[:]),
		Size:   4,
	})
	if errDownload == nil {
		t.Fatal("DownloadArtifact() error = nil")
	}
	if !strings.Contains(errDownload.Error(), "maximum allowed size") {
		t.Fatalf("DownloadArtifact() error = %v, want size limit", errDownload)
	}
	if body.offset > 5 {
		t.Fatalf("download read %d bytes, want at most size+1", body.offset)
	}
}

func TestInstallRejectsInvalidLatestReleaseTag(t *testing.T) {
	t.Parallel()

	client := Client{HTTPClient: mapHTTPDoer{
		"https://api.github.com/repos/author-name/cliproxy-sample-provider-plugin/releases/latest": []byte(`{"tag_name": "latest", "assets": []}`),
	}}
	_, errInstall := client.Install(context.Background(), testPlugin(), InstallOptions{
		PluginsDir: t.TempDir(),
		GOOS:       "darwin",
		GOARCH:     "arm64",
	})
	if errInstall == nil {
		t.Fatal("Install() error = nil")
	}
	if !strings.Contains(errInstall.Error(), "invalid release tag") {
		t.Fatalf("Install() error = %v, want invalid release tag", errInstall)
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

type mapHTTPDoer map[string][]byte

func (c mapHTTPDoer) Do(req *http.Request) (*http.Response, error) {
	body, ok := c[req.URL.String()]
	if !ok {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader("not found")),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

type authCheckingHTTPDoer struct {
	url           string
	wantAuth      string
	responseBytes []byte
}

type singleResponseHTTPDoer struct {
	body io.ReadCloser
}

func (c singleResponseHTTPDoer) Do(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       c.body,
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

type trackingReadCloser struct {
	data   []byte
	offset int
}

func (r *trackingReadCloser) Read(p []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}

func (r *trackingReadCloser) Close() error {
	return nil
}

func (c authCheckingHTTPDoer) Do(req *http.Request) (*http.Response, error) {
	if req.URL.String() != c.url {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader("not found")),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	}
	if gotAuth := req.Header.Get("Authorization"); gotAuth != c.wantAuth {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Body:       io.NopCloser(strings.NewReader("bad auth")),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(c.responseBytes)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
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
