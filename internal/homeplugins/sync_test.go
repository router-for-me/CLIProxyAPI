package homeplugins

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkpluginstore "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginstore"
	"gopkg.in/yaml.v3"
)

type fakePluginRuntime struct {
	busy     bool
	unloaded []string
}

func (r *fakePluginRuntime) PluginBusy(id string) bool {
	return r.busy
}

func (r *fakePluginRuntime) UnloadPlugin(id string) bool {
	r.unloaded = append(r.unloaded, id)
	r.busy = false
	return true
}

func TestSyncPlatformInstallsManifestArtifact(t *testing.T) {
	root := t.TempDir()
	archiveData := makeZip(t, map[string]string{"sample.dll": "library-data"})
	archiveName := "sample_0.2.0_windows_amd64.zip"
	checksum := sha256.Sum256(archiveData)
	httpClient := mapHTTPDoer{
		"https://api.github.com/repos/owner/sample-plugin/releases/tags/v0.2.0": []byte(`{
			"tag_name": "v0.2.0",
			"assets": [
				{"name": "` + archiveName + `", "browser_download_url": "https://downloads.example/` + archiveName + `"},
				{"name": "checksums.txt", "browser_download_url": "https://downloads.example/checksums.txt"}
			]
		}`),
		"https://downloads.example/" + archiveName: archiveData,
		"https://downloads.example/checksums.txt":  []byte(hex.EncodeToString(checksum[:]) + "  " + archiveName + "\n"),
	}
	restore := replacePluginStoreClientForTest(httpClient)
	defer restore()

	if errSync := SyncPlatform(context.Background(), syncTestConfig(t, root), nil, Platform{GOOS: "windows", GOARCH: "amd64"}); errSync != nil {
		t.Fatalf("SyncPlatform() error = %v", errSync)
	}
	target := filepath.Join(root, "windows", "amd64", "sample.dll")
	got, errRead := os.ReadFile(target)
	if errRead != nil {
		t.Fatalf("read target: %v", errRead)
	}
	if string(got) != "library-data" {
		t.Fatalf("target data = %q, want library-data", string(got))
	}
}

func TestSyncPlatformSkipsIdenticalBusyPlugin(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, "windows", "amd64")
	if errMkdir := os.MkdirAll(targetDir, 0o755); errMkdir != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdir)
	}
	target := filepath.Join(targetDir, "sample.dll")
	if errWrite := os.WriteFile(target, []byte("library-data"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	archiveData := makeZip(t, map[string]string{"sample.dll": "library-data"})
	archiveName := "sample_0.2.0_windows_amd64.zip"
	checksum := sha256.Sum256(archiveData)
	httpClient := mapHTTPDoer{
		"https://api.github.com/repos/owner/sample-plugin/releases/tags/v0.2.0": []byte(`{
			"tag_name": "v0.2.0",
			"assets": [
				{"name": "` + archiveName + `", "browser_download_url": "https://downloads.example/` + archiveName + `"},
				{"name": "checksums.txt", "browser_download_url": "https://downloads.example/checksums.txt"}
			]
		}`),
		"https://downloads.example/" + archiveName: archiveData,
		"https://downloads.example/checksums.txt":  []byte(hex.EncodeToString(checksum[:]) + "  " + archiveName + "\n"),
	}
	restore := replacePluginStoreClientForTest(httpClient)
	defer restore()

	runtime := &fakePluginRuntime{busy: true}
	if errSync := SyncPlatform(context.Background(), syncTestConfig(t, root), runtime, Platform{GOOS: "windows", GOARCH: "amd64"}); errSync != nil {
		t.Fatalf("SyncPlatform() error = %v", errSync)
	}
	if len(runtime.unloaded) != 0 {
		t.Fatalf("UnloadPlugin() calls = %v, want none", runtime.unloaded)
	}
	got, errRead := os.ReadFile(target)
	if errRead != nil {
		t.Fatalf("read target: %v", errRead)
	}
	if string(got) != "library-data" {
		t.Fatalf("target data = %q, want library-data", string(got))
	}
}

func TestSyncPlatformSkipsConfigWithoutManifest(t *testing.T) {
	restore := replacePluginStoreClientForTest(mapHTTPDoer{})
	defer restore()

	cfg := &config.Config{
		Home: config.HomeConfig{Enabled: true},
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     t.TempDir(),
			Configs: map[string]config.PluginInstanceConfig{
				"sample": pluginConfigFromYAML(t, `enabled: true`),
			},
		},
	}
	if errSync := SyncPlatform(context.Background(), cfg, nil, Platform{GOOS: "linux", GOARCH: "amd64"}); errSync != nil {
		t.Fatalf("SyncPlatform() error = %v", errSync)
	}
}

func TestSyncPlatformRejectsInvalidManifest(t *testing.T) {
	cfg := &config.Config{
		Home: config.HomeConfig{Enabled: true},
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     t.TempDir(),
			Configs: map[string]config.PluginInstanceConfig{
				"sample": pluginConfigFromYAML(t, `
enabled: true
store:
  id: sample
`),
			},
		},
	}
	if errSync := SyncPlatform(context.Background(), cfg, nil, Platform{GOOS: "linux", GOARCH: "amd64"}); errSync == nil {
		t.Fatal("SyncPlatform() error = nil, want invalid manifest")
	}
}

func syncTestConfig(t *testing.T, root string) *config.Config {
	t.Helper()
	return &config.Config{
		Home: config.HomeConfig{Enabled: true},
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     root,
			Configs: map[string]config.PluginInstanceConfig{
				"sample": pluginConfigFromYAML(t, `
enabled: true
store:
  id: sample
  name: Sample
  description: Adds sample support.
  author: owner
  version: 0.2.0
  release-tag: v0.2.0
  repository: https://github.com/owner/sample-plugin
`),
			},
		},
	}
}

func pluginConfigFromYAML(t *testing.T, text string) config.PluginInstanceConfig {
	t.Helper()
	var item config.PluginInstanceConfig
	if errUnmarshal := yaml.Unmarshal([]byte(text), &item); errUnmarshal != nil {
		t.Fatalf("unmarshal plugin config: %v", errUnmarshal)
	}
	return item
}

func replacePluginStoreClientForTest(httpClient sdkpluginstore.HTTPDoer) func() {
	previous := newPluginStoreClient
	newPluginStoreClient = func(cfg *config.Config) sdkpluginstore.Client {
		return sdkpluginstore.NewClient(httpClient, "")
	}
	return func() {
		newPluginStoreClient = previous
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
