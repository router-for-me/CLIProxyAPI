package management

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginstore"
)

func TestListPluginStoreMergesInstalledStatus(t *testing.T) {
	t.Parallel()

	pluginsDir := writeManagementPluginFile(t, "sample-provider")
	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled: true,
				Dir:     pluginsDir,
				Configs: map[string]config.PluginInstanceConfig{
					"sample-provider": pluginConfigFromYAML(t, "enabled: true\nmode: fast\n"),
				},
			},
		},
		configFilePath:         writeTestConfigFile(t),
		pluginStoreRegistryURL: "https://registry.example/registry.json",
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			"https://registry.example/registry.json": registryJSON(t),
		},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/plugin-store", nil)

	h.ListPluginStore(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body pluginStoreListResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v; body=%s", errDecode, rec.Body.String())
	}
	if !body.PluginsEnabled {
		t.Fatal("plugins_enabled = false, want true")
	}
	if len(body.Plugins) != 1 {
		t.Fatalf("plugins len = %d, want 1", len(body.Plugins))
	}
	entry := body.Plugins[0]
	if !entry.Installed || !entry.Configured || !entry.Enabled {
		t.Fatalf("store entry status = %#v, want installed configured enabled", entry)
	}
	if entry.Registered || entry.EffectiveEnabled {
		t.Fatalf("runtime status = registered %v effective %v, want false false", entry.Registered, entry.EffectiveEnabled)
	}
	if entry.InstalledVersion != "" {
		t.Fatalf("installed_version = %q, want empty for unregistered plugin", entry.InstalledVersion)
	}
	if entry.UpdateAvailable {
		t.Fatal("update_available = true, want false when installed version is unknown")
	}
	if entry.Path == "" {
		t.Fatal("path is empty")
	}
}

func TestPluginStoreDirectManifestPinsRequestedVersionArtifacts(t *testing.T) {
	plugin := pluginstore.Plugin{
		ID: "sample", Name: "Sample", Description: "Sample plugin", Author: "tester", Version: "1.0.0",
		Install: pluginstore.InstallPlan{Type: pluginstore.InstallTypeDirect, Artifacts: []pluginstore.Artifact{{
			GOOS: "linux", GOARCH: "amd64", URL: "https://downloads.example/sample-1.0.0.zip",
			SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Size: 100,
		}}},
		Versions: []pluginstore.Version{{
			Version: "0.9.0",
			Install: pluginstore.InstallPlan{Type: pluginstore.InstallTypeDirect, Artifacts: []pluginstore.Artifact{{
				GOOS: "linux", GOARCH: "amd64", URL: "https://downloads.example/sample-0.9.0.zip",
				SHA256: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", Size: 90,
			}}},
		}},
	}

	manifest, errManifest := pluginStoreDirectManifest(pluginstore.DefaultSource(), plugin, "0.9.0")
	if errManifest != nil {
		t.Fatalf("pluginStoreDirectManifest() error = %v", errManifest)
	}
	if manifest.Version != "0.9.0" || len(manifest.Install.Artifacts) != 1 {
		t.Fatalf("manifest = %#v, want pinned historical version artifact", manifest)
	}
	artifact := manifest.Install.Artifacts[0]
	if artifact.URL != "https://downloads.example/sample-0.9.0.zip" || artifact.Size != 90 {
		t.Fatalf("artifact = %#v, want historical 0.9.0 artifact", artifact)
	}
}

func TestListPluginStoreUsesVersionFromInstalledFilename(t *testing.T) {
	t.Parallel()

	pluginsDir := t.TempDir()
	archDir := filepath.Join(pluginsDir, runtime.GOOS, runtime.GOARCH)
	if errMkdirAll := os.MkdirAll(archDir, 0o755); errMkdirAll != nil {
		t.Fatalf("MkdirAll(%s) error = %v", archDir, errMkdirAll)
	}
	pluginPath := filepath.Join(archDir, "sample-provider-v0.0.1"+managementPluginExtension(runtime.GOOS))
	if errWriteFile := os.WriteFile(pluginPath, []byte("x"), 0o644); errWriteFile != nil {
		t.Fatalf("WriteFile(%s) error = %v", pluginPath, errWriteFile)
	}
	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled: true,
				Dir:     pluginsDir,
			},
		},
		configFilePath:         writeTestConfigFile(t),
		pluginStoreRegistryURL: "https://registry.example/registry.json",
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			"https://registry.example/registry.json": registryJSON(t),
		},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/plugin-store", nil)

	h.ListPluginStore(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body pluginStoreListResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v; body=%s", errDecode, rec.Body.String())
	}
	if len(body.Plugins) != 1 {
		t.Fatalf("plugins len = %d, want 1", len(body.Plugins))
	}
	entry := body.Plugins[0]
	if !entry.Installed || entry.InstalledVersion != "0.0.1" {
		t.Fatalf("store entry status = %#v, want installed version 0.0.1", entry)
	}
	if !entry.UpdateAvailable {
		t.Fatalf("update_available = false, want true for installed 0.0.1 and registry 0.1.0")
	}
}

func TestListPluginStoreUsesConfiguredStoreVersionWhenFilesCoexist(t *testing.T) {
	t.Parallel()

	pluginsDir := t.TempDir()
	archDir := filepath.Join(pluginsDir, runtime.GOOS, runtime.GOARCH)
	if errMkdirAll := os.MkdirAll(archDir, 0o755); errMkdirAll != nil {
		t.Fatalf("MkdirAll(%s) error = %v", archDir, errMkdirAll)
	}
	extension := managementPluginExtension(runtime.GOOS)
	pinnedPath := filepath.Join(archDir, "sample-provider-v0.1.0"+extension)
	newerPath := filepath.Join(archDir, "sample-provider-v0.2.0"+extension)
	for _, path := range []string{pinnedPath, newerPath} {
		if errWriteFile := os.WriteFile(path, []byte("x"), 0o644); errWriteFile != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, errWriteFile)
		}
	}
	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled: true,
				Dir:     pluginsDir,
				Configs: map[string]config.PluginInstanceConfig{
					"sample-provider": pluginConfigFromYAML(t, "enabled: true\nstore:\n  version: 0.1.0\n  release-tag: v0.1.0\n"),
				},
			},
		},
		configFilePath:         writeTestConfigFile(t),
		pluginStoreRegistryURL: "https://registry.example/registry.json",
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			"https://registry.example/registry.json": registryJSON(t),
		},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/plugin-store", nil)

	h.ListPluginStore(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body pluginStoreListResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v; body=%s", errDecode, rec.Body.String())
	}
	if len(body.Plugins) != 1 {
		t.Fatalf("plugins len = %d, want 1", len(body.Plugins))
	}
	entry := body.Plugins[0]
	if !entry.Installed || entry.InstalledVersion != "0.1.0" || entry.Path != pinnedPath {
		t.Fatalf("store entry status = %#v, want pinned version/path %s", entry, pinnedPath)
	}
}

func TestListPluginStoreEscapesRegistryStrings(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled: true,
				Dir:     t.TempDir(),
			},
		},
		configFilePath:         writeTestConfigFile(t),
		pluginStoreRegistryURL: "https://registry.example/registry.json",
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			"https://registry.example/registry.json": []byte(`{
				"schema_version": 1,
				"plugins": [{
					"id": "sample-provider",
					"name": "<script>alert(1)</script>",
					"description": "<img src=x onerror=alert(1)>",
					"author": "\"attacker\"",
					"version": "0.1.0",
					"repository": "https://github.com/author-name/cliproxy-sample-provider-plugin",
					"logo": "<svg onload=alert(1)>",
					"homepage": "https://example.com/?q=<x>",
					"license": "<b>MIT</b>",
					"tags": ["<provider>", "safe & sound"]
				}]
			}`),
		},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/plugin-store", nil)

	h.ListPluginStore(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body pluginStoreListResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v; body=%s", errDecode, rec.Body.String())
	}
	if len(body.Plugins) != 1 {
		t.Fatalf("plugins len = %d, want 1", len(body.Plugins))
	}
	entry := body.Plugins[0]
	if entry.Name != html.EscapeString("<script>alert(1)</script>") ||
		entry.Description != html.EscapeString("<img src=x onerror=alert(1)>") ||
		entry.Author != html.EscapeString(`"attacker"`) ||
		entry.Version != "0.1.0" ||
		entry.Repository != "https://github.com/author-name/cliproxy-sample-provider-plugin" ||
		entry.Logo != html.EscapeString("<svg onload=alert(1)>") ||
		entry.Homepage != html.EscapeString("https://example.com/?q=<x>") ||
		entry.License != html.EscapeString("<b>MIT</b>") {
		t.Fatalf("store entry = %#v, want escaped strings", entry)
	}
	if len(entry.Tags) != 2 ||
		entry.Tags[0] != html.EscapeString("<provider>") ||
		entry.Tags[1] != html.EscapeString("safe & sound") {
		t.Fatalf("tags = %#v, want escaped strings", entry.Tags)
	}
}

func TestListPluginStoreOnlyChecksInstalledSources(t *testing.T) {
	t.Parallel()
	for _, installedCount := range []int{0, 2} {
		t.Run(fmt.Sprintf("installed-%d", installedCount), func(t *testing.T) {
			pluginsDir := t.TempDir()
			archDir := filepath.Join(pluginsDir, runtime.GOOS, runtime.GOARCH)
			if errMkdir := os.MkdirAll(archDir, 0o755); errMkdir != nil {
				t.Fatal(errMkdir)
			}
			communityURL := "https://community.example/registry.json"
			configs := map[string]config.PluginInstanceConfig{}
			var plugins []map[string]any
			responses := fakePluginStoreHTTPClient{}
			for index := 0; index < 57; index++ {
				id := fmt.Sprintf("sample-%d", index)
				repository := fmt.Sprintf("https://github.com/author/plugin-%d", index)
				plugins = append(plugins, map[string]any{"id": id, "name": id, "description": "Test plugin", "author": "author", "version": "0.1.0", "repository": repository})
				responses[fmt.Sprintf("https://api.github.com/repos/author/plugin-%d/releases/latest", index)] = []byte(fmt.Sprintf(`{"tag_name":"v0.2.%d"}`, index))
				if index == 3 || index == 41 {
					configs[id] = pluginConfigWithStoreSource(t, pluginstore.DefaultSourceID, pluginstore.DefaultRegistryURL)
					if installedCount > 0 {
						path := filepath.Join(archDir, id+"-v0.0.1"+managementPluginExtension(runtime.GOOS))
						if errWrite := os.WriteFile(path, []byte("x"), 0o644); errWrite != nil {
							t.Fatal(errWrite)
						}
					}
				}
			}
			registry, errMarshal := json.Marshal(map[string]any{"schema_version": 1, "plugins": plugins})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			responses[pluginstore.DefaultRegistryURL] = registry
			responses[communityURL] = []byte(`{"schema_version":1,"plugins":[{"id":"sample-3","name":"Other source","description":"Test plugin","author":"community","version":"0.9.0","repository":"https://github.com/community/other"}]}`)
			httpClient := &countingPluginStoreHTTPClient{responses: responses}
			h := &Handler{cfg: &config.Config{Plugins: config.PluginsConfig{Enabled: true, Dir: pluginsDir, StoreSources: []string{communityURL}, Configs: configs}}, pluginStoreHTTPClient: httpClient}
			for call := 0; call < 2; call++ {
				body := listPluginStoreForTest(t, h)
				if len(body.Plugins) != 58 {
					t.Fatalf("got %d plugins, want 58", len(body.Plugins))
				}
				for _, entry := range body.Plugins {
					wantVersion := "0.1.0"
					if entry.SourceID != pluginstore.DefaultSourceID {
						wantVersion = "0.9.0"
					} else if installedCount > 0 && (entry.ID == "sample-3" || entry.ID == "sample-41") {
						wantVersion = "0.2." + strings.TrimPrefix(entry.ID, "sample-")
					}
					if entry.Version != wantVersion {
						t.Fatalf("%s: version %s, want %s", entry.StoreID, entry.Version, wantVersion)
					}
				}
			}
			for index := 0; index < 57; index++ {
				want := 0
				if installedCount > 0 && (index == 3 || index == 41) {
					want = 1
				}
				if got := httpClient.count(fmt.Sprintf("https://api.github.com/repos/author/plugin-%d/releases/latest", index)); got != want {
					t.Fatalf("plugin-%d calls = %d, want %d", index, got, want)
				}
			}
			if got := httpClient.count("https://api.github.com/repos/community/other/releases/latest"); got != 0 {
				t.Fatalf("other source queried %d times", got)
			}
		})
	}
}

func listPluginStoreForTest(t *testing.T, h *Handler) pluginStoreListResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/plugin-store", nil)
	h.ListPluginStore(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	var body pluginStoreListResponse
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &body); errDecode != nil {
		t.Fatal(errDecode)
	}
	return body
}

type pluginStoreHTTPDoerFunc func(*http.Request) (*http.Response, error)

func (do pluginStoreHTTPDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return do(request)
}

func TestListPluginStoreRateLimitCache(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		status  int
		limited bool
	}{
		{http.StatusForbidden, true},
		{http.StatusTooManyRequests, true},
		{http.StatusForbidden, false},
	} {
		for _, previousVersion := range []string{"", "0.2.0"} {
			t.Run(fmt.Sprintf("%d/limited-%t/previous-%s", test.status, test.limited, previousVersion), func(t *testing.T) {
				repository := "https://github.com/author-name/cliproxy-sample-provider-plugin"
				retryAt := time.Now().Add(time.Hour).Truncate(time.Second)
				calls := 0
				h := &Handler{cfg: &config.Config{Plugins: config.PluginsConfig{Enabled: true, Dir: writeManagementPluginFile(t, "sample-provider")}}}
				h.pluginReleaseCache = map[string]pluginReleaseCacheEntry{repository: {version: previousVersion, expiresAt: time.Now().Add(-time.Hour)}}
				h.pluginStoreHTTPClient = pluginStoreHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
					if request.URL.String() == pluginstore.DefaultRegistryURL {
						return fakePluginStoreHTTPClient{pluginstore.DefaultRegistryURL: registryJSON(t)}.Do(request)
					}
					calls++
					headers := make(http.Header)
					headers.Set("X-RateLimit-Reset", fmt.Sprint(retryAt.Unix()))
					if test.limited {
						headers.Set("X-RateLimit-Remaining", "0")
					}
					return &http.Response{StatusCode: test.status, Header: headers, Body: io.NopCloser(strings.NewReader("limited"))}, nil
				})
				for call := 0; call < 2; call++ {
					body := listPluginStoreForTest(t, h)
					wantVersion := "0.1.0"
					if previousVersion != "" && test.limited {
						wantVersion = previousVersion
					}
					if len(body.Plugins) != 1 || body.Plugins[0].Version != wantVersion || !body.Plugins[0].Installed {
						t.Fatalf("unexpected entries: %+v", body.Plugins)
					}
				}
				if calls != 1 {
					t.Fatalf("calls = %d, want 1", calls)
				}
				entry := h.pluginReleaseCache[repository]
				if test.limited {
					if !entry.expiresAt.Equal(retryAt) {
						t.Fatalf("expiry = %v, want %v", entry.expiresAt, retryAt)
					}
				} else if entry.expiresAt.After(time.Now().Add(time.Minute)) {
					t.Fatal("ordinary error received long cooldown")
				}
				entry.expiresAt = time.Now().Add(-time.Second)
				h.pluginReleaseCache[repository] = entry
				listPluginStoreForTest(t, h)
				if calls != 2 {
					t.Fatalf("calls after expiry = %d, want 2", calls)
				}
			})
		}
	}
}

func TestLatestPluginVersionCacheStartsAfterRequest(t *testing.T) {
	t.Parallel()
	for _, success := range []bool{false, true} {
		t.Run(fmt.Sprintf("success-%t", success), func(t *testing.T) {
			var completedAt time.Time
			client := pluginstore.Client{HTTPClient: pluginStoreHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
				completedAt = time.Now()
				if !success {
					return nil, fmt.Errorf("simulated network failure")
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"tag_name":"v0.2.0"}`))}, nil
			})}
			h := &Handler{}
			plugin := pluginstore.Plugin{ID: "sample", Repository: "https://github.com/author/sample"}
			h.latestPluginVersion(context.Background(), client, plugin)
			ttl := pluginReleaseFailureCacheTTL
			if success {
				ttl = pluginReleaseCacheTTL
			}
			if expiry := h.pluginReleaseCache[plugin.Repository].expiresAt; expiry.Before(completedAt.Add(ttl)) {
				t.Fatalf("expiry %v precedes completion plus TTL %v", expiry, completedAt.Add(ttl))
			}
		})
	}
}

func TestLatestPluginVersionRateLimitPreservesConcurrentRefresh(t *testing.T) {
	t.Parallel()
	repository := "https://github.com/author/sample"
	plugin := pluginstore.Plugin{ID: "sample", Repository: repository}
	retryAt := time.Now().Add(time.Hour).Truncate(time.Second)
	h := &Handler{pluginReleaseCache: map[string]pluginReleaseCacheEntry{
		repository: {version: "0.1.0", expiresAt: time.Now().Add(-time.Second)},
	}}
	started := make(chan struct{})
	releaseLimited := make(chan struct{})
	limitedResult := make(chan string, 1)
	limitedClient := pluginstore.Client{HTTPClient: pluginStoreHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-releaseLimited
		headers := make(http.Header)
		headers.Set("Retry-After", retryAt.Format(http.TimeFormat))
		return &http.Response{StatusCode: http.StatusForbidden, Header: headers, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	go func() {
		limitedResult <- h.latestPluginVersion(context.Background(), limitedClient, plugin)
	}()
	<-started
	successClient := pluginstore.Client{HTTPClient: pluginStoreHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"tag_name":"v0.2.0"}`))}, nil
	})}
	successVersion := h.latestPluginVersion(context.Background(), successClient, plugin)
	close(releaseLimited)
	limitedVersion := <-limitedResult
	if successVersion != "0.2.0" || limitedVersion != "0.2.0" {
		t.Fatalf("versions: success = %q, limited = %q; want 0.2.0", successVersion, limitedVersion)
	}
	entry := h.pluginReleaseCache[repository]
	if entry.version != "0.2.0" || !entry.expiresAt.Equal(retryAt) {
		t.Fatalf("cache = %+v, want version 0.2.0 until %v", entry, retryAt)
	}
	if version := h.latestPluginVersion(context.Background(), limitedClient, plugin); version != "0.2.0" {
		t.Fatalf("cached version = %q, want 0.2.0", version)
	}
}

func TestLatestPluginVersionPreservesConcurrentCooldown(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"success", "failure", "shorter-limit"} {
		t.Run(outcome, func(t *testing.T) {
			repository := "https://github.com/author/sample"
			plugin := pluginstore.Plugin{ID: "sample", Repository: repository}
			retryAt := time.Now().Add(time.Hour).Truncate(time.Second)
			h := &Handler{pluginReleaseCache: map[string]pluginReleaseCacheEntry{
				repository: {version: "0.1.0", expiresAt: time.Now().Add(-time.Second)},
			}}
			started := make(chan struct{})
			finish := make(chan struct{})
			result := make(chan string, 1)
			laterClient := pluginstore.Client{HTTPClient: pluginStoreHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
				close(started)
				<-finish
				switch outcome {
				case "failure":
					return nil, fmt.Errorf("simulated network failure")
				case "shorter-limit":
					headers := make(http.Header)
					headers.Set("Retry-After", "120")
					return &http.Response{StatusCode: http.StatusTooManyRequests, Header: headers, Body: io.NopCloser(strings.NewReader(""))}, nil
				default:
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"tag_name":"v0.2.0"}`))}, nil
				}
			})}
			go func() {
				result <- h.latestPluginVersion(context.Background(), laterClient, plugin)
			}()
			<-started
			limitedClient := pluginstore.Client{HTTPClient: pluginStoreHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
				headers := make(http.Header)
				headers.Set("Retry-After", retryAt.Format(http.TimeFormat))
				return &http.Response{StatusCode: http.StatusForbidden, Header: headers, Body: io.NopCloser(strings.NewReader(""))}, nil
			})}
			h.latestPluginVersion(context.Background(), limitedClient, plugin)
			close(finish)
			version := <-result
			wantVersion := "0.1.0"
			if outcome == "success" {
				wantVersion = "0.2.0"
			}
			entry := h.pluginReleaseCache[repository]
			if version != wantVersion || entry.version != wantVersion || !entry.expiresAt.Equal(retryAt) || !entry.retryAt.Equal(retryAt) {
				t.Fatalf("version = %q, cache = %+v; want %s until %v", version, entry, wantVersion, retryAt)
			}
			if cached := h.latestPluginVersion(context.Background(), laterClient, plugin); cached != wantVersion {
				t.Fatalf("cached version = %q, want %q", cached, wantVersion)
			}
		})
	}
}

func TestListPluginStoreShowsLatestReleaseVersionAndCaches(t *testing.T) {
	t.Parallel()

	httpClient := &countingPluginStoreHTTPClient{responses: fakePluginStoreHTTPClient{
		"https://registry.example/registry.json": registryJSON(t),
		"https://api.github.com/repos/author-name/cliproxy-sample-provider-plugin/releases/latest": []byte(`{
			"tag_name": "v0.2.0",
			"assets": []
		}`),
	}}
	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled: true,
				Dir:     writeManagementPluginFile(t, "sample-provider"),
			},
		},
		configFilePath:         writeTestConfigFile(t),
		pluginStoreRegistryURL: "https://registry.example/registry.json",
		pluginStoreHTTPClient:  httpClient,
	}

	listOnce := func() pluginStoreListResponse {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/plugin-store", nil)
		h.ListPluginStore(c)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		var body pluginStoreListResponse
		if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
			t.Fatalf("Unmarshal() error = %v; body=%s", errDecode, rec.Body.String())
		}
		return body
	}

	for call := 0; call < 2; call++ {
		body := listOnce()
		if len(body.Plugins) != 1 {
			t.Fatalf("plugins len = %d, want 1", len(body.Plugins))
		}
		if body.Plugins[0].Version != "0.2.0" {
			t.Fatalf("version = %q, want 0.2.0 from latest release tag", body.Plugins[0].Version)
		}
	}
	releaseCalls := httpClient.count("https://api.github.com/repos/author-name/cliproxy-sample-provider-plugin/releases/latest")
	if releaseCalls != 1 {
		t.Fatalf("latest release fetched %d times, want 1 (cached)", releaseCalls)
	}
}

func TestListPluginStoreFallsBackToRegistryVersion(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled: true,
				Dir:     t.TempDir(),
			},
		},
		configFilePath:         writeTestConfigFile(t),
		pluginStoreRegistryURL: "https://registry.example/registry.json",
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			"https://registry.example/registry.json": registryJSON(t),
		},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/plugin-store", nil)

	h.ListPluginStore(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body pluginStoreListResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v; body=%s", errDecode, rec.Body.String())
	}
	if len(body.Plugins) != 1 {
		t.Fatalf("plugins len = %d, want 1", len(body.Plugins))
	}
	if body.Plugins[0].Version != "0.1.0" {
		t.Fatalf("version = %q, want registry fallback 0.1.0", body.Plugins[0].Version)
	}
}

func TestListPluginStoreIncludesThirdPartySources(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled:      true,
				Dir:          t.TempDir(),
				StoreSources: []string{"https://community.example/registry.json"},
			},
		},
		configFilePath: writeTestConfigFile(t),
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			pluginstore.DefaultRegistryURL: registryJSON(t),
			"https://community.example/registry.json": []byte(`{
				"schema_version": 1,
				"plugins": [{
					"id": "third-provider",
					"name": "Third Provider",
					"description": "Adds third-party provider support.",
					"author": "community",
					"version": "0.3.0",
					"repository": "https://github.com/community/cliproxy-third-provider-plugin"
				}]
			}`),
		},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/plugin-store", nil)

	h.ListPluginStore(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body pluginStoreListResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v; body=%s", errDecode, rec.Body.String())
	}
	if len(body.Sources) != 2 {
		t.Fatalf("sources len = %d, want 2: %#v", len(body.Sources), body.Sources)
	}
	if len(body.Plugins) != 2 {
		t.Fatalf("plugins len = %d, want 2: %#v", len(body.Plugins), body.Plugins)
	}
	byID := map[string]pluginStoreListEntry{}
	for _, entry := range body.Plugins {
		byID[entry.ID] = entry
	}
	if byID["sample-provider"].SourceID != pluginstore.DefaultSourceID {
		t.Fatalf("official source id = %q, want %q", byID["sample-provider"].SourceID, pluginstore.DefaultSourceID)
	}
	third := byID["third-provider"]
	communitySourceID := pluginstore.SourceID("https://community.example/registry.json")
	if third.StoreID != communitySourceID+"/third-provider" || third.SourceID != communitySourceID || third.SourceName != "community.example" || third.SourceURL != "https://community.example/registry.json" {
		t.Fatalf("third-party source fields = %#v", third)
	}
}

func TestListPluginStoreMatchesInstalledStatusToManifestSource(t *testing.T) {
	t.Parallel()

	pluginsDir := t.TempDir()
	archDir := filepath.Join(pluginsDir, runtime.GOOS, runtime.GOARCH)
	if errMkdirAll := os.MkdirAll(archDir, 0o755); errMkdirAll != nil {
		t.Fatalf("MkdirAll(%s) error = %v", archDir, errMkdirAll)
	}
	pluginPath := filepath.Join(archDir, "sample-provider-v0.0.1"+managementPluginExtension(runtime.GOOS))
	if errWriteFile := os.WriteFile(pluginPath, []byte("x"), 0o644); errWriteFile != nil {
		t.Fatalf("WriteFile(%s) error = %v", pluginPath, errWriteFile)
	}

	communityURL := "https://community.example/registry.json"
	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled:      true,
				Dir:          pluginsDir,
				StoreSources: []string{communityURL},
				Configs: map[string]config.PluginInstanceConfig{
					"sample-provider": pluginConfigWithStoreSource(t, pluginstore.DefaultSourceID, pluginstore.DefaultRegistryURL),
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			pluginstore.DefaultRegistryURL: registryJSON(t),
			communityURL:                   thirdPartySampleRegistryJSON(t),
		},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/plugin-store", nil)
	h.ListPluginStore(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		Plugins []struct {
			SourceID            string `json:"source_id"`
			InstalledSourceID   string `json:"installed_source_id"`
			InstallSourceStatus string `json:"install_source_status"`
			UpdateAvailable     bool   `json:"update_available"`
		} `json:"plugins"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v; body=%s", errDecode, rec.Body.String())
	}
	if len(body.Plugins) != 2 {
		t.Fatalf("plugins len = %d, want 2", len(body.Plugins))
	}
	entries := make(map[string]struct {
		InstalledSourceID   string
		InstallSourceStatus string
		UpdateAvailable     bool
	}, len(body.Plugins))
	for _, entry := range body.Plugins {
		entries[entry.SourceID] = struct {
			InstalledSourceID   string
			InstallSourceStatus string
			UpdateAvailable     bool
		}{entry.InstalledSourceID, entry.InstallSourceStatus, entry.UpdateAvailable}
	}
	official := entries[pluginstore.DefaultSourceID]
	if official.InstalledSourceID != pluginstore.DefaultSourceID || official.InstallSourceStatus != "matched" || !official.UpdateAvailable {
		t.Fatalf("official entry = %#v, want matched update", official)
	}
	communitySourceID := pluginstore.SourceID(communityURL)
	community := entries[communitySourceID]
	if community.InstalledSourceID != pluginstore.DefaultSourceID || community.InstallSourceStatus != "different" || community.UpdateAvailable {
		t.Fatalf("community entry = %#v, want different source without update", community)
	}
}

func TestInstallPluginFromStoreRejectsImplicitSourceSwitch(t *testing.T) {
	t.Parallel()

	communityURL := "https://community.example/registry.json"
	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled:      true,
				Dir:          writeManagementPluginFile(t, "sample-provider"),
				StoreSources: []string{communityURL},
				Configs: map[string]config.PluginInstanceConfig{
					"sample-provider": pluginConfigWithStoreSource(t, pluginstore.DefaultSourceID, pluginstore.DefaultRegistryURL),
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			pluginstore.DefaultRegistryURL: registryJSON(t),
			communityURL:                   thirdPartySampleRegistryJSON(t),
		},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "sample-provider"}}
	communitySourceID := pluginstore.SourceID(communityURL)
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/plugin-store/sample-provider/install?source="+communitySourceID, nil)
	h.InstallPluginFromStore(c)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "plugin_store_source_conflict") || !strings.Contains(rec.Body.String(), pluginstore.DefaultSourceID) {
		t.Fatalf("body = %s, want source conflict with installed source", rec.Body.String())
	}
}

func TestInstallPluginFromStoreRejectsUnknownManagedSource(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled: true,
				Dir:     writeManagementPluginFile(t, "sample-provider"),
				Configs: map[string]config.PluginInstanceConfig{
					"sample-provider": pluginConfigWithStoreSource(t, "", ""),
				},
			},
		},
		configFilePath:         writeTestConfigFile(t),
		pluginStoreRegistryURL: "https://registry.example/registry.json",
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			"https://registry.example/registry.json": registryJSON(t),
		},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "sample-provider"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/plugin-store/sample-provider/install", nil)
	h.InstallPluginFromStore(c)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "plugin_store_installed_source_unknown") {
		t.Fatalf("body = %s, want unknown installed source error", rec.Body.String())
	}
}

func TestListPluginStoreIncludesDirectMetadataAndAuth(t *testing.T) {
	t.Setenv("PLUGIN_STORE_TOKEN", "secret-token")

	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled: true,
				Dir:     t.TempDir(),
				StoreAuth: []pluginstore.AuthConfig{{
					Match:    "https://registry.example/",
					ApplyTo:  []string{pluginstore.RequestKindRegistry},
					Type:     pluginstore.AuthTypeBearer,
					TokenEnv: "PLUGIN_STORE_TOKEN",
				}},
			},
		},
		configFilePath:         writeTestConfigFile(t),
		pluginStoreRegistryURL: "https://registry.example/registry.json",
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			"https://registry.example/registry.json": directRegistryJSON("https://downloads.example/sample-provider.zip", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"),
		},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/plugin-store", nil)

	h.ListPluginStore(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body pluginStoreListResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v; body=%s", errDecode, rec.Body.String())
	}
	if len(body.Plugins) != 1 {
		t.Fatalf("plugins len = %d, want 1", len(body.Plugins))
	}
	entry := body.Plugins[0]
	if entry.InstallType != pluginstore.InstallTypeDirect || !entry.AuthRequired || !entry.AuthConfigured {
		t.Fatalf("direct metadata = %#v, want direct auth metadata", entry)
	}
	if !pluginStorePlatformsContain(entry.Platforms, "linux", "amd64") {
		t.Fatalf("platforms = %#v, want linux/amd64", entry.Platforms)
	}
}

func TestListPluginStoreReportsVersionArtifactAuth(t *testing.T) {
	t.Setenv("PLUGIN_STORE_TOKEN", "secret-token")

	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled: true,
				Dir:     t.TempDir(),
				StoreAuth: []pluginstore.AuthConfig{{
					Match:    "https://versioned.example/",
					ApplyTo:  []string{pluginstore.RequestKindArtifact},
					Type:     pluginstore.AuthTypeBearer,
					TokenEnv: "PLUGIN_STORE_TOKEN",
				}},
			},
		},
		configFilePath:         writeTestConfigFile(t),
		pluginStoreRegistryURL: "https://registry.example/registry.json",
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			"https://registry.example/registry.json": directRegistryJSONWithVersionArtifact(
				"https://downloads.example/sample-provider.zip",
				"https://versioned.example/sample-provider-0.3.0.zip",
				"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			),
		},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/plugin-store", nil)

	h.ListPluginStore(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body pluginStoreListResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v; body=%s", errDecode, rec.Body.String())
	}
	if len(body.Plugins) != 1 {
		t.Fatalf("plugins len = %d, want 1", len(body.Plugins))
	}
	if !body.Plugins[0].AuthConfigured {
		t.Fatalf("auth_configured = false, want true for version artifact auth")
	}
}

func TestListPluginStoreReportsGitHubMetadataAuth(t *testing.T) {
	t.Setenv("PLUGIN_STORE_TOKEN", "secret-token")

	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled: true,
				Dir:     t.TempDir(),
				StoreAuth: []pluginstore.AuthConfig{{
					Match:    "https://api.github.com/repos/author-name/cliproxy-sample-provider-plugin/releases/",
					ApplyTo:  []string{pluginstore.RequestKindMetadata},
					Type:     pluginstore.AuthTypeBearer,
					TokenEnv: "PLUGIN_STORE_TOKEN",
				}},
			},
		},
		configFilePath:         writeTestConfigFile(t),
		pluginStoreRegistryURL: "https://registry.example/registry.json",
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			"https://registry.example/registry.json": registryJSON(t),
		},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/plugin-store", nil)

	h.ListPluginStore(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body pluginStoreListResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v; body=%s", errDecode, rec.Body.String())
	}
	if len(body.Plugins) != 1 {
		t.Fatalf("plugins len = %d, want 1", len(body.Plugins))
	}
	if !body.Plugins[0].AuthConfigured {
		t.Fatalf("auth_configured = false, want true for GitHub metadata auth")
	}
}

func TestInstallPluginFromStoreRejectsUnresolvedPluginsDir(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Chdir(workspace)

	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Dir:     "~/.cli-proxy-api/plugins",
				Configs: map[string]config.PluginInstanceConfig{},
			},
		},
		pluginStoreRegistryURL: "https://registry.example/registry.json",
		pluginStoreHTTPClient:  fakePluginStoreHTTPClient{},
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "sample-provider"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/plugin-store/sample-provider/install", nil)

	h.InstallPluginFromStore(c)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	var body map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v; body=%s", errDecode, rec.Body.String())
	}
	if body["error"] != "plugin_directory_invalid" {
		t.Fatalf("error = %#v, want plugin_directory_invalid", body["error"])
	}
	if _, errStat := os.Stat(filepath.Join(workspace, "~")); !os.IsNotExist(errStat) {
		t.Fatalf("literal tilde directory stat error = %v, want not exist", errStat)
	}
}

func TestInstallPluginFromStoreWritesFileAndEnablesConfig(t *testing.T) {
	workspace := t.TempDir()
	homeDir := filepath.Join(workspace, "home")
	if errMkdir := os.MkdirAll(homeDir, 0o755); errMkdir != nil {
		t.Fatalf("MkdirAll(%s) error = %v", homeDir, errMkdir)
	}
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)
	t.Chdir(workspace)

	cfg, errParse := config.ParseConfigBytes([]byte(`
plugins:
  enabled: false
  dir: "~/.cli-proxy-api/plugins"
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	cfg.Plugins.Configs["sample-provider"] = pluginConfigFromYAML(t, "enabled: false\nmode: fast\n")
	pluginsDir := filepath.Join(homeDir, ".cli-proxy-api", "plugins")
	archiveData := makeManagementPluginStoreZip(t, "sample-provider"+managementPluginExtension(runtime.GOOS), "library-data")
	archiveName := "sample-provider_0.1.0_" + runtime.GOOS + "_" + runtime.GOARCH + ".zip"
	checksum := sha256.Sum256(archiveData)
	h := &Handler{
		cfg:                    cfg,
		configFilePath:         writeTestConfigFile(t),
		pluginStoreRegistryURL: "https://registry.example/registry.json",
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			"https://registry.example/registry.json": registryJSON(t),
			"https://api.github.com/repos/author-name/cliproxy-sample-provider-plugin/releases/latest": []byte(`{
				"tag_name": "v0.1.0",
				"assets": [
					{"name": "` + archiveName + `", "browser_download_url": "https://downloads.example/` + archiveName + `"},
					{"name": "checksums.txt", "browser_download_url": "https://downloads.example/checksums.txt"}
				]
			}`),
			"https://downloads.example/" + archiveName: archiveData,
			"https://downloads.example/checksums.txt":  []byte(hex.EncodeToString(checksum[:]) + "  " + archiveName + "\n"),
		},
	}
	reloads, reloadDone := captureConfigReload(h)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "sample-provider"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/plugin-store/sample-provider/install", nil)

	h.InstallPluginFromStore(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	cfgSnapshot := waitForAsyncReload(t, reloads)
	waitForReloadDone(t, reloadDone)
	if cfgSnapshot == h.cfg {
		t.Fatalf("reload config = handler config %p, want independent snapshot", h.cfg)
	}
	var body pluginInstallResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v; body=%s", errDecode, rec.Body.String())
	}
	if body.Status != "installed" || body.ID != "sample-provider" || body.Version != "0.1.0" {
		t.Fatalf("install response = %#v", body)
	}
	if body.PluginsEnabled {
		t.Fatal("plugins_enabled = true, want false")
	}
	if body.RestartRequired {
		t.Fatal("restart_required = true, want false")
	}
	targetPath := filepath.Join(pluginsDir, runtime.GOOS, runtime.GOARCH, "sample-provider-v0.1.0"+managementPluginExtension(runtime.GOOS))
	data, errRead := os.ReadFile(targetPath)
	if errRead != nil {
		t.Fatalf("ReadFile(%s) error = %v", targetPath, errRead)
	}
	if string(data) != "library-data" {
		t.Fatalf("installed file = %q, want library-data", data)
	}
	item := h.cfg.Plugins.Configs["sample-provider"]
	if item.Enabled == nil || !*item.Enabled {
		t.Fatalf("plugin enabled = %#v, want true", item.Enabled)
	}
	snapshotItem := cfgSnapshot.Plugins.Configs["sample-provider"]
	if snapshotItem.Enabled == nil || !*snapshotItem.Enabled {
		t.Fatalf("snapshot plugin enabled = %#v, want true", snapshotItem.Enabled)
	}
	if h.cfg.Plugins.Enabled {
		t.Fatal("global plugins.enabled changed to true")
	}
	if cfgSnapshot.Plugins.Enabled {
		t.Fatal("snapshot global plugins.enabled changed to true")
	}
	raw := marshalPluginRaw(t, item)
	if !strings.Contains(raw, "mode: fast") {
		t.Fatalf("plugin raw config lost custom field:\n%s", raw)
	}
	manifest := pluginStoreManifestFromConfig(t, item)
	if manifest.InstallType() != pluginstore.InstallTypeGitHubRelease || manifest.ReleaseTag != "v0.1.0" || manifest.Version != "0.1.0" {
		t.Fatalf("store manifest = %#v, want github-release v0.1.0", manifest)
	}
	if raw := marshalPluginRaw(t, snapshotItem); !strings.Contains(raw, "mode: fast") {
		t.Fatalf("snapshot plugin raw config lost custom field:\n%s", raw)
	}
}

func TestInstallPluginFromStoreInstallsDirectArtifact(t *testing.T) {
	t.Parallel()

	pluginsDir := t.TempDir()
	archiveData := makeManagementPluginStoreZip(t, "sample-provider"+managementPluginExtension(runtime.GOOS), "direct-library-data")
	checksum := sha256.Sum256(archiveData)
	artifactURL := "https://downloads.example/sample-provider.zip"
	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled: false,
				Dir:     pluginsDir,
			},
		},
		configFilePath:         writeTestConfigFile(t),
		pluginStoreRegistryURL: "https://registry.example/registry.json",
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			"https://registry.example/registry.json": directRegistryJSON(artifactURL, hex.EncodeToString(checksum[:])),
			artifactURL:                              archiveData,
		},
	}
	reloads, reloadDone := captureConfigReload(h)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "sample-provider"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/plugin-store/sample-provider/install", nil)

	h.InstallPluginFromStore(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	waitForAsyncReload(t, reloads)
	waitForReloadDone(t, reloadDone)
	var body pluginInstallResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v; body=%s", errDecode, rec.Body.String())
	}
	if body.InstallType != pluginstore.InstallTypeDirect || body.Version != "0.4.0" {
		t.Fatalf("install response = %#v, want direct 0.4.0", body)
	}
	targetPath := filepath.Join(pluginsDir, runtime.GOOS, runtime.GOARCH, "sample-provider-v0.4.0"+managementPluginExtension(runtime.GOOS))
	data, errRead := os.ReadFile(targetPath)
	if errRead != nil {
		t.Fatalf("ReadFile(%s) error = %v", targetPath, errRead)
	}
	if string(data) != "direct-library-data" {
		t.Fatalf("installed file = %q, want direct-library-data", data)
	}
	manifest := pluginStoreManifestFromConfig(t, h.cfg.Plugins.Configs["sample-provider"])
	if manifest.SchemaVersion != pluginstore.SchemaVersionV2 || manifest.InstallType() != pluginstore.InstallTypeDirect || manifest.Version != "0.4.0" {
		t.Fatalf("store manifest = %#v, want direct schema v2 0.4.0", manifest)
	}
	if manifest.SourceURL != "https://registry.example/registry.json" || len(manifest.Install.Artifacts) == 0 {
		t.Fatalf("store manifest source/artifacts = %q/%d, want source URL with pinned artifacts", manifest.SourceURL, len(manifest.Install.Artifacts))
	}
	if raw := marshalPluginRaw(t, h.cfg.Plugins.Configs["sample-provider"]); !strings.Contains(raw, "artifacts:") {
		t.Fatalf("direct store manifest should persist pinned artifacts:\n%s", raw)
	}
}

func TestInstallPluginFromStoreHonorsDirectQueryVersion(t *testing.T) {
	t.Parallel()

	pluginsDir := t.TempDir()
	archiveData := makeManagementPluginStoreZip(t, "sample-provider"+managementPluginExtension(runtime.GOOS), "direct-history-data")
	checksum := sha256.Sum256(archiveData)
	topArtifactURL := "https://downloads.example/sample-provider-0.4.0.zip"
	versionArtifactURL := "https://downloads.example/sample-provider-0.3.0.zip"
	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled: false,
				Dir:     pluginsDir,
			},
		},
		configFilePath:         writeTestConfigFile(t),
		pluginStoreRegistryURL: "https://registry.example/registry.json",
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			"https://registry.example/registry.json": directRegistryJSONWithVersionArtifact(topArtifactURL, versionArtifactURL, hex.EncodeToString(checksum[:])),
			versionArtifactURL:                       archiveData,
		},
	}
	reloads, reloadDone := captureConfigReload(h)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "sample-provider"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/plugin-store/sample-provider/install?version=0.3.0", nil)

	h.InstallPluginFromStore(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	waitForAsyncReload(t, reloads)
	waitForReloadDone(t, reloadDone)
	var body pluginInstallResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v; body=%s", errDecode, rec.Body.String())
	}
	if body.InstallType != pluginstore.InstallTypeDirect || body.Version != "0.3.0" {
		t.Fatalf("install response = %#v, want direct 0.3.0", body)
	}
	targetPath := filepath.Join(pluginsDir, runtime.GOOS, runtime.GOARCH, "sample-provider-v0.3.0"+managementPluginExtension(runtime.GOOS))
	data, errRead := os.ReadFile(targetPath)
	if errRead != nil {
		t.Fatalf("ReadFile(%s) error = %v", targetPath, errRead)
	}
	if string(data) != "direct-history-data" {
		t.Fatalf("installed file = %q, want direct-history-data", data)
	}
	manifest := pluginStoreManifestFromConfig(t, h.cfg.Plugins.Configs["sample-provider"])
	if manifest.Version != "0.3.0" || manifest.InstallType() != pluginstore.InstallTypeDirect || len(manifest.Install.Artifacts) != 1 {
		t.Fatalf("store manifest = %#v, want pinned direct 0.3.0", manifest)
	}
	if manifest.Install.Artifacts[0].URL != versionArtifactURL {
		t.Fatalf("store manifest artifact = %#v, want requested version URL", manifest.Install.Artifacts[0])
	}
}

func TestInstallPluginFromStoreUsesRequestedThirdPartySource(t *testing.T) {
	t.Parallel()

	pluginsDir := t.TempDir()
	archiveData := makeManagementPluginStoreZip(t, "sample-provider"+managementPluginExtension(runtime.GOOS), "third-party-library-data")
	archiveName := "sample-provider_0.3.0_" + runtime.GOOS + "_" + runtime.GOARCH + ".zip"
	checksum := sha256.Sum256(archiveData)
	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled:      false,
				Dir:          pluginsDir,
				StoreSources: []string{"https://community.example/registry.json"},
			},
		},
		configFilePath: writeTestConfigFile(t),
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			pluginstore.DefaultRegistryURL:            registryJSON(t),
			"https://community.example/registry.json": thirdPartySampleRegistryJSON(t),
			"https://api.github.com/repos/community/cliproxy-sample-provider-plugin/releases/latest": []byte(`{
				"tag_name": "v0.3.0",
				"assets": [
					{"name": "` + archiveName + `", "browser_download_url": "https://downloads.example/` + archiveName + `"},
					{"name": "checksums.txt", "browser_download_url": "https://downloads.example/checksums.txt"}
				]
			}`),
			"https://downloads.example/" + archiveName: archiveData,
			"https://downloads.example/checksums.txt":  []byte(hex.EncodeToString(checksum[:]) + "  " + archiveName + "\n"),
		},
	}
	reloads, reloadDone := captureConfigReload(h)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "sample-provider"}}
	communitySourceID := pluginstore.SourceID("https://community.example/registry.json")
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/plugin-store/sample-provider/install?source="+communitySourceID, nil)

	h.InstallPluginFromStore(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	cfgSnapshot := waitForAsyncReload(t, reloads)
	waitForReloadDone(t, reloadDone)
	if cfgSnapshot == h.cfg {
		t.Fatalf("reload config = handler config %p, want independent snapshot", h.cfg)
	}
	var body pluginInstallResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v; body=%s", errDecode, rec.Body.String())
	}
	if body.SourceID != communitySourceID || body.Version != "0.3.0" {
		t.Fatalf("install response = %#v, want community source version 0.3.0", body)
	}
	targetPath := filepath.Join(pluginsDir, runtime.GOOS, runtime.GOARCH, "sample-provider-v0.3.0"+managementPluginExtension(runtime.GOOS))
	data, errRead := os.ReadFile(targetPath)
	if errRead != nil {
		t.Fatalf("ReadFile(%s) error = %v", targetPath, errRead)
	}
	if string(data) != "third-party-library-data" {
		t.Fatalf("installed file = %q, want third-party-library-data", data)
	}
	snapshotItem := cfgSnapshot.Plugins.Configs["sample-provider"]
	if snapshotItem.Enabled == nil || !*snapshotItem.Enabled {
		t.Fatalf("snapshot plugin enabled = %#v, want true", snapshotItem.Enabled)
	}
}

func TestInstallPluginFromStoreRequiresSourceForDuplicateIDs(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled:      false,
				Dir:          t.TempDir(),
				StoreSources: []string{"https://community.example/registry.json"},
			},
		},
		configFilePath: writeTestConfigFile(t),
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			pluginstore.DefaultRegistryURL:            registryJSON(t),
			"https://community.example/registry.json": thirdPartySampleRegistryJSON(t),
		},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "sample-provider"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/plugin-store/sample-provider/install", nil)

	h.InstallPluginFromStore(c)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "plugin_store_source_required") {
		t.Fatalf("body = %s, want source required error", rec.Body.String())
	}
}

func TestInstallPluginFromStoreOverwritesFilePreservesConfigAndReloads(t *testing.T) {
	t.Parallel()

	pluginsDir := t.TempDir()
	existingPath := filepath.Join(pluginsDir, runtime.GOOS, runtime.GOARCH, "sample-provider-v0.1.0"+managementPluginExtension(runtime.GOOS))
	if errMkdir := os.MkdirAll(filepath.Dir(existingPath), 0o755); errMkdir != nil {
		t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(existingPath), errMkdir)
	}
	if errWrite := os.WriteFile(existingPath, []byte("old-library-data"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile(%s) error = %v", existingPath, errWrite)
	}
	archiveData := makeManagementPluginStoreZip(t, "sample-provider"+managementPluginExtension(runtime.GOOS), "new-library-data")
	archiveName := "sample-provider_0.1.0_" + runtime.GOOS + "_" + runtime.GOARCH + ".zip"
	checksum := sha256.Sum256(archiveData)
	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled: true,
				Dir:     pluginsDir,
				Configs: map[string]config.PluginInstanceConfig{
					"sample-provider": pluginConfigFromYAML(t, "enabled: false\npriority: 5\nmode: fast\nextra: keep\n"),
				},
			},
		},
		configFilePath:         writeTestConfigFile(t),
		pluginStoreRegistryURL: "https://registry.example/registry.json",
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			"https://registry.example/registry.json": registryJSON(t),
			"https://api.github.com/repos/author-name/cliproxy-sample-provider-plugin/releases/latest": []byte(`{
				"tag_name": "v0.1.0",
				"assets": [
					{"name": "` + archiveName + `", "browser_download_url": "https://downloads.example/` + archiveName + `"},
					{"name": "checksums.txt", "browser_download_url": "https://downloads.example/checksums.txt"}
				]
			}`),
			"https://downloads.example/" + archiveName: archiveData,
			"https://downloads.example/checksums.txt":  []byte(hex.EncodeToString(checksum[:]) + "  " + archiveName + "\n"),
		},
	}
	reloads, reloadDone := captureConfigReload(h)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "sample-provider"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/plugin-store/sample-provider/install", nil)

	h.InstallPluginFromStore(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	cfgSnapshot := waitForAsyncReload(t, reloads)
	waitForReloadDone(t, reloadDone)
	if cfgSnapshot == h.cfg {
		t.Fatalf("reload config = handler config %p, want independent snapshot", h.cfg)
	}
	data, errRead := os.ReadFile(existingPath)
	if errRead != nil {
		t.Fatalf("ReadFile(%s) error = %v", existingPath, errRead)
	}
	if string(data) != "new-library-data" {
		t.Fatalf("installed file = %q, want new-library-data", data)
	}
	item := h.cfg.Plugins.Configs["sample-provider"]
	if item.Enabled == nil || !*item.Enabled {
		t.Fatalf("plugin enabled = %#v, want true", item.Enabled)
	}
	snapshotItem := cfgSnapshot.Plugins.Configs["sample-provider"]
	if snapshotItem.Enabled == nil || !*snapshotItem.Enabled {
		t.Fatalf("snapshot plugin enabled = %#v, want true", snapshotItem.Enabled)
	}
	if item.Priority != 5 {
		t.Fatalf("plugin priority = %d, want 5", item.Priority)
	}
	if snapshotItem.Priority != 5 {
		t.Fatalf("snapshot plugin priority = %d, want 5", snapshotItem.Priority)
	}
	raw := marshalPluginRaw(t, item)
	if !strings.Contains(raw, "mode: fast") || !strings.Contains(raw, "extra: keep") {
		t.Fatalf("plugin raw config lost custom fields:\n%s", raw)
	}
	if raw := marshalPluginRaw(t, snapshotItem); !strings.Contains(raw, "mode: fast") || !strings.Contains(raw, "extra: keep") {
		t.Fatalf("snapshot plugin raw config lost custom fields:\n%s", raw)
	}
}

func TestEnablePluginConfigLockedPreservesExistingFields(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled: false,
				Configs: map[string]config.PluginInstanceConfig{
					"sample-provider": pluginConfigFromYAML(t, "enabled: false\npriority: 5\nmode: fast\n"),
				},
			},
		},
	}

	if errEnable := h.enablePluginConfigLocked("sample-provider", testStoreManifest()); errEnable != nil {
		t.Fatalf("enablePluginConfigLocked() error = %v", errEnable)
	}
	if h.cfg.Plugins.Enabled {
		t.Fatal("global Plugins.Enabled changed to true")
	}
	item := h.cfg.Plugins.Configs["sample-provider"]
	if item.Enabled == nil || !*item.Enabled {
		t.Fatalf("plugin enabled = %#v, want true", item.Enabled)
	}
	if item.Priority != 5 {
		t.Fatalf("plugin priority = %d, want 5", item.Priority)
	}
	raw := marshalPluginRaw(t, item)
	if !strings.Contains(raw, "mode: fast") || !strings.Contains(raw, "store:") {
		t.Fatalf("plugin raw config lost custom field:\n%s", raw)
	}
}

func TestEnablePluginConfigLockedCreatesMissingConfig(t *testing.T) {
	t.Parallel()

	h := &Handler{cfg: &config.Config{}}
	if errEnable := h.enablePluginConfigLocked("sample-provider", testStoreManifest()); errEnable != nil {
		t.Fatalf("enablePluginConfigLocked() error = %v", errEnable)
	}
	item := h.cfg.Plugins.Configs["sample-provider"]
	if item.Enabled == nil || !*item.Enabled {
		t.Fatalf("plugin enabled = %#v, want true", item.Enabled)
	}
	manifest := pluginStoreManifestFromConfig(t, item)
	if manifest.ID != "sample-provider" || manifest.ReleaseTag != "v0.1.0" {
		t.Fatalf("store manifest = %#v, want sample-provider v0.1.0", manifest)
	}
}

type fakePluginStoreHTTPClient map[string][]byte

func (c fakePluginStoreHTTPClient) Do(req *http.Request) (*http.Response, error) {
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

type countingPluginStoreHTTPClient struct {
	responses fakePluginStoreHTTPClient
	mu        sync.Mutex
	counts    map[string]int
}

func (c *countingPluginStoreHTTPClient) Do(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	if c.counts == nil {
		c.counts = make(map[string]int)
	}
	c.counts[req.URL.String()]++
	c.mu.Unlock()
	return c.responses.Do(req)
}

func (c *countingPluginStoreHTTPClient) count(url string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[url]
}

func registryJSON(t *testing.T) []byte {
	t.Helper()

	return []byte(`{
		"schema_version": 1,
		"plugins": [{
			"id": "sample-provider",
			"name": "Sample Provider",
			"description": "Adds sample provider support.",
			"author": "author-name",
			"version": "0.1.0",
			"repository": "https://github.com/author-name/cliproxy-sample-provider-plugin",
			"tags": ["provider"]
		}]
	}`)
}

func thirdPartySampleRegistryJSON(t *testing.T) []byte {
	t.Helper()

	return []byte(`{
		"schema_version": 1,
		"plugins": [{
			"id": "sample-provider",
			"name": "Sample Provider Community Build",
			"description": "Adds sample provider support from a third-party source.",
			"author": "community",
			"version": "0.3.0",
			"repository": "https://github.com/community/cliproxy-sample-provider-plugin"
		}]
	}`)
}

func directRegistryJSON(artifactURL string, checksum string) []byte {
	return []byte(`{
		"schema_version": 2,
		"plugins": [{
			"id": "sample-provider",
			"name": "Sample Provider",
			"description": "Adds sample provider support.",
			"author": "author-name",
			"version": "0.4.0",
			"auth_required": true,
			"install": {
				"type": "direct",
				"artifacts": [{
					"goos": "` + runtime.GOOS + `",
					"goarch": "` + runtime.GOARCH + `",
					"url": "` + artifactURL + `",
					"sha256": "` + checksum + `"
				}, {
					"goos": "linux",
					"goarch": "amd64",
					"url": "` + artifactURL + `",
					"sha256": "` + checksum + `"
				}]
			}
		}]
	}`)
}

func directRegistryJSONWithVersionArtifact(artifactURL string, versionArtifactURL string, checksum string) []byte {
	return []byte(`{
		"schema_version": 2,
		"plugins": [{
			"id": "sample-provider",
			"name": "Sample Provider",
			"description": "Adds sample provider support.",
			"author": "author-name",
			"version": "0.4.0",
			"auth_required": true,
			"install": {
				"type": "direct",
				"artifacts": [{
					"goos": "` + runtime.GOOS + `",
					"goarch": "` + runtime.GOARCH + `",
					"url": "` + artifactURL + `",
					"sha256": "` + checksum + `"
				}]
			},
			"versions": [{
				"version": "0.3.0",
				"install": {
					"type": "direct",
					"artifacts": [{
						"goos": "` + runtime.GOOS + `",
						"goarch": "` + runtime.GOARCH + `",
						"url": "` + versionArtifactURL + `",
						"sha256": "` + checksum + `"
					}]
				}
			}]
		}]
	}`)
}

func testStoreManifest() pluginstore.Manifest {
	return pluginstore.Manifest{
		ID:          "sample-provider",
		Name:        "Sample Provider",
		Description: "Adds sample provider support.",
		Author:      "author-name",
		Version:     "0.1.0",
		ReleaseTag:  "v0.1.0",
		Repository:  "https://github.com/author-name/cliproxy-sample-provider-plugin",
		Install:     pluginstore.InstallPlan{Type: pluginstore.InstallTypeGitHubRelease},
	}
}

func pluginConfigWithStoreSource(t *testing.T, sourceID string, sourceURL string) config.PluginInstanceConfig {
	t.Helper()
	sourceFields := ""
	if sourceID != "" {
		sourceFields += "  source-id: " + sourceID + "\n"
	}
	if sourceURL != "" {
		sourceFields += "  source-url: " + sourceURL + "\n"
	}
	return pluginConfigFromYAML(t, "enabled: true\nstore:\n  schema-version: 1\n  id: sample-provider\n  version: 0.0.1\n  release-tag: v0.0.1\n  repository: https://github.com/author-name/cliproxy-sample-provider-plugin\n"+sourceFields+"  install:\n    type: github-release\n")
}

func pluginStoreManifestFromConfig(t *testing.T, item config.PluginInstanceConfig) pluginstore.Manifest {
	t.Helper()

	node := pluginConfigNode(item)
	for index := 0; index+1 < len(node.Content); index += 2 {
		key := node.Content[index]
		value := node.Content[index+1]
		if key == nil || key.Value != "store" {
			continue
		}
		var manifest pluginstore.Manifest
		if errDecode := value.Decode(&manifest); errDecode != nil {
			t.Fatalf("decode store manifest: %v", errDecode)
		}
		if errValidate := manifest.Validate(); errValidate != nil {
			t.Fatalf("store manifest Validate() error = %v; manifest=%#v", errValidate, manifest)
		}
		return manifest
	}
	t.Fatalf("plugin config missing store manifest:\n%s", marshalPluginRaw(t, item))
	return pluginstore.Manifest{}
}

func pluginStorePlatformsContain(platforms []pluginStorePlatform, goos string, goarch string) bool {
	for _, platform := range platforms {
		if platform.GOOS == goos && platform.GOARCH == goarch {
			return true
		}
	}
	return false
}

func makeManagementPluginStoreZip(t *testing.T, name string, content string) []byte {
	t.Helper()

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, errCreate := writer.Create(name)
	if errCreate != nil {
		t.Fatalf("Create(%s) error = %v", name, errCreate)
	}
	if _, errWrite := file.Write([]byte(content)); errWrite != nil {
		t.Fatalf("Write(%s) error = %v", name, errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}
	return buffer.Bytes()
}
