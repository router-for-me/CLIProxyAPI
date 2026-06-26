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
	"runtime"
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

type fakePluginLoadInspector map[string]bool

func (r *fakePluginRuntime) PluginBusy(id string) bool {
	return r.busy
}

func (r *fakePluginRuntime) UnloadPlugin(id string) bool {
	r.unloaded = append(r.unloaded, id)
	r.busy = false
	return true
}

func (i fakePluginLoadInspector) PluginRegistered(id string) bool {
	return i[id]
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
	target := pluginTestPath(root, "windows", "amd64", "sample", "0.2.0")
	got, errRead := os.ReadFile(target)
	if errRead != nil {
		t.Fatalf("read target: %v", errRead)
	}
	if string(got) != "library-data" {
		t.Fatalf("target data = %q, want library-data", string(got))
	}
}

func TestSyncPlatformWithReportRecordsSuccessfulInstall(t *testing.T) {
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

	report, errSync := SyncPlatformWithReport(context.Background(), syncTestConfig(t, root), nil, Platform{GOOS: "windows", GOARCH: "amd64"})
	if errSync != nil {
		t.Fatalf("SyncPlatformWithReport() error = %v", errSync)
	}
	if !report.OK || report.Status != pluginTaskStatusOK || report.Phase != pluginTaskPhaseInstall {
		t.Fatalf("report status = %+v, want successful install phase", report)
	}
	if len(report.Plugins) != 1 {
		t.Fatalf("report plugins len = %d, want 1", len(report.Plugins))
	}
	plugin := report.Plugins[0]
	if plugin.ID != "sample" || plugin.InstallStatus != pluginInstallStatusInstalled || plugin.Version != "0.2.0" {
		t.Fatalf("plugin report = %+v, want installed sample 0.2.0", plugin)
	}
	if wantPath := pluginTestPath(root, "windows", "amd64", "sample", "0.2.0"); plugin.Path != wantPath {
		t.Fatalf("plugin path = %q, want %q", plugin.Path, wantPath)
	}
}

func TestSyncPlatformWithReportRecordsSkippedIdenticalArtifact(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, "windows", "amd64")
	if errMkdir := os.MkdirAll(targetDir, 0o755); errMkdir != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdir)
	}
	target := filepath.Join(targetDir, "sample-v0.2.0.dll")
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

	report, errSync := SyncPlatformWithReport(context.Background(), syncTestConfig(t, root), nil, Platform{GOOS: "windows", GOARCH: "amd64"})
	if errSync != nil {
		t.Fatalf("SyncPlatformWithReport() error = %v", errSync)
	}
	if !report.OK || len(report.Plugins) != 1 {
		t.Fatalf("report = %+v, want one successful skipped plugin", report)
	}
	plugin := report.Plugins[0]
	if plugin.ID != "sample" || plugin.InstallStatus != pluginInstallStatusSkipped || !plugin.Skipped {
		t.Fatalf("plugin report = %+v, want skipped identical sample", plugin)
	}
	if plugin.Path != target {
		t.Fatalf("plugin path = %q, want %q", plugin.Path, target)
	}
}

func TestSyncPlatformSkipsIdenticalBusyPlugin(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, "windows", "amd64")
	if errMkdir := os.MkdirAll(targetDir, 0o755); errMkdir != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdir)
	}
	target := filepath.Join(targetDir, "sample-v0.2.0.dll")
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

func TestSyncPlatformWithReportRecordsInvalidManifest(t *testing.T) {
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
	report, errSync := SyncPlatformWithReport(context.Background(), cfg, nil, Platform{GOOS: "linux", GOARCH: "amd64"})
	if errSync == nil {
		t.Fatal("SyncPlatformWithReport() error = nil, want invalid manifest")
	}
	if report.OK || report.Status != pluginTaskStatusError || len(report.Plugins) != 1 {
		t.Fatalf("report = %+v, want one failed plugin", report)
	}
	if report.Plugins[0].ID != "sample" || report.Plugins[0].InstallStatus != pluginInstallStatusFailed || !strings.Contains(report.Plugins[0].Error, "invalid store manifest") {
		t.Fatalf("plugin report = %+v, want invalid manifest failure", report.Plugins[0])
	}
}

func TestMarkLoadResultsFailsWhenInstalledPluginDidNotLoad(t *testing.T) {
	report := SyncReport{
		Status:  pluginTaskStatusOK,
		OK:      true,
		Phase:   pluginTaskPhaseInstall,
		Plugins: []PluginInstallStatus{{ID: "sample", InstallStatus: pluginInstallStatusInstalled}},
	}

	errLoad := MarkLoadResults(&report, fakePluginLoadInspector{})
	if errLoad == nil {
		t.Fatal("MarkLoadResults() error = nil, want load failure")
	}
	if report.OK || report.Status != pluginTaskStatusError || report.Phase != pluginTaskPhaseLoad {
		t.Fatalf("report = %+v, want failed load phase", report)
	}
	if report.Plugins[0].LoadStatus != pluginLoadStatusFailed || !strings.Contains(report.Plugins[0].Error, "installed but not loaded") {
		t.Fatalf("plugin report = %+v, want load failure", report.Plugins[0])
	}
}

func TestMarkLoadResultsPreservesInstallFailure(t *testing.T) {
	report := SyncReport{
		Status:  pluginTaskStatusError,
		OK:      false,
		Phase:   pluginTaskPhaseInstall,
		Plugins: []PluginInstallStatus{{ID: "sample", InstallStatus: pluginInstallStatusFailed, Error: "install boom"}},
	}

	errLoad := MarkLoadResults(&report, fakePluginLoadInspector{"sample": true})
	if errLoad == nil {
		t.Fatal("MarkLoadResults() error = nil, want install failure to remain fatal")
	}
	if report.OK || report.Status != pluginTaskStatusError {
		t.Fatalf("report = %+v, want failed status", report)
	}
	if report.Plugins[0].LoadStatus != pluginInstallStatusSkipped {
		t.Fatalf("load status = %q, want skipped", report.Plugins[0].LoadStatus)
	}
}

func TestDeleteWithReportRemovesCurrentPlatformPlugin(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, runtime.GOOS, runtime.GOARCH)
	if errMkdir := os.MkdirAll(targetDir, 0o755); errMkdir != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdir)
	}
	target := filepath.Join(targetDir, "sample"+pluginExtension(runtime.GOOS))
	if errWrite := os.WriteFile(target, []byte("library-data"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	runtimeHost := &fakePluginRuntime{busy: true}

	report := DeleteWithReport(context.Background(), syncTestConfig(t, root), runtimeHost, 42, "sample")
	if !report.OK || report.TaskID != 42 || report.Task != pluginDeleteTaskName || report.Phase != pluginTaskPhaseDelete {
		t.Fatalf("report = %+v, want successful delete task", report)
	}
	if len(runtimeHost.unloaded) != 1 || runtimeHost.unloaded[0] != "sample" {
		t.Fatalf("UnloadPlugin calls = %v, want sample", runtimeHost.unloaded)
	}
	if len(report.Plugins) != 1 || report.Plugins[0].InstallStatus != pluginInstallStatusDeleted || report.Plugins[0].Path != target {
		t.Fatalf("plugin report = %+v, want deleted target", report.Plugins)
	}
	if _, errStat := os.Stat(target); !os.IsNotExist(errStat) {
		t.Fatalf("target stat error = %v, want not exist", errStat)
	}
}

func TestDeleteWithReportRemovesAllCurrentPlatformPluginVersions(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, runtime.GOOS, runtime.GOARCH)
	if errMkdir := os.MkdirAll(targetDir, 0o755); errMkdir != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdir)
	}
	extension := pluginExtension(runtime.GOOS)
	olderTarget := filepath.Join(targetDir, "sample-v0.2.0"+extension)
	newerTarget := filepath.Join(targetDir, "sample-v0.3.0"+extension)
	otherTarget := filepath.Join(targetDir, "other-v0.3.0"+extension)
	for _, target := range []string{olderTarget, newerTarget, otherTarget} {
		if errWrite := os.WriteFile(target, []byte("library-data"), 0o644); errWrite != nil {
			t.Fatalf("WriteFile(%s) error = %v", target, errWrite)
		}
	}
	runtimeHost := &fakePluginRuntime{busy: true}

	report := DeleteWithReport(context.Background(), syncTestConfig(t, root), runtimeHost, 43, "sample")
	if !report.OK {
		t.Fatalf("report = %+v, want successful delete task", report)
	}
	if len(runtimeHost.unloaded) != 1 || runtimeHost.unloaded[0] != "sample" {
		t.Fatalf("UnloadPlugin calls = %v, want sample", runtimeHost.unloaded)
	}
	if len(report.Plugins) != 1 || report.Plugins[0].InstallStatus != pluginInstallStatusDeleted || report.Plugins[0].Path != newerTarget {
		t.Fatalf("plugin report = %+v, want deleted representative target %s", report.Plugins, newerTarget)
	}
	for _, target := range []string{olderTarget, newerTarget} {
		if _, errStat := os.Stat(target); !os.IsNotExist(errStat) {
			t.Fatalf("target %s stat error = %v, want not exist", target, errStat)
		}
	}
	if _, errStat := os.Stat(otherTarget); errStat != nil {
		t.Fatalf("other plugin stat error = %v, want retained", errStat)
	}
}

func TestDeleteWithReportMissingPluginIsSuccess(t *testing.T) {
	report := DeleteWithReport(context.Background(), syncTestConfig(t, t.TempDir()), nil, 7, "missing")
	if !report.OK || report.Status != pluginTaskStatusOK {
		t.Fatalf("report = %+v, want missing plugin delete success", report)
	}
	if len(report.Plugins) != 1 || report.Plugins[0].InstallStatus != pluginInstallStatusMissing {
		t.Fatalf("plugin report = %+v, want missing status", report.Plugins)
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

func pluginTestPath(root string, goos string, goarch string, id string, version string) string {
	name := strings.TrimSpace(id)
	version = strings.TrimSpace(version)
	if version != "" {
		name += "-v" + version
	}
	return filepath.Join(root, goos, goarch, name+pluginExtension(goos))
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
