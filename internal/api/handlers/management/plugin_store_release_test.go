package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginstore"
)

func listPluginStoreForTest(t *testing.T, h *Handler) pluginStoreListResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/plugin-store", nil)
	h.ListPluginStore(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var body pluginStoreListResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatal(errDecode)
	}
	return body
}

func TestListPluginStoreSkipsUninstalledReleases(t *testing.T) {
	t.Parallel()

	registry := pluginstore.Registry{SchemaVersion: pluginstore.SchemaVersion}
	for index := range 100 {
		version := ""
		if index%2 == 0 {
			version = "1.0.0"
		}
		registry.Plugins = append(registry.Plugins, pluginstore.Plugin{
			ID: fmt.Sprintf("plugin-%d", index), Name: "Plugin", Description: "Test plugin", Author: "test",
			Version: version, Repository: fmt.Sprintf("https://github.com/test/plugin-%d", index),
		})
	}
	data, errMarshal := json.Marshal(registry)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	httpClient := &countingPluginStoreHTTPClient{responses: fakePluginStoreHTTPClient{
		pluginstore.DefaultRegistryURL: data,
	}}
	h := &Handler{
		cfg: &config.Config{Plugins: config.PluginsConfig{
			Dir: t.TempDir(),
			// Configuration alone does not mean the plugin is installed.
			Configs: map[string]config.PluginInstanceConfig{"plugin-0": pluginConfigFromYAML(t, "enabled: true\n")},
		}},
		pluginStoreHTTPClient: httpClient,
	}
	for range 2 {
		body := listPluginStoreForTest(t, h)
		if len(body.Plugins) != 100 {
			t.Fatalf("plugins = %d, want 100", len(body.Plugins))
		}
		for index, entry := range body.Plugins {
			if entry.Version != registry.Plugins[index].Version || entry.Installed || entry.UpdateAvailable {
				t.Fatalf("unexpected catalog entry: %#v", entry)
			}
		}
	}
	httpClient.mu.Lock()
	defer httpClient.mu.Unlock()
	if len(httpClient.counts) != 1 || httpClient.counts[pluginstore.DefaultRegistryURL] != 2 {
		t.Fatalf("requests = %v, want registry requests only", httpClient.counts)
	}
}

func TestListPluginStoreSkipsInstalledDirectRelease(t *testing.T) {
	t.Parallel()

	httpClient := &countingPluginStoreHTTPClient{responses: fakePluginStoreHTTPClient{
		pluginstore.DefaultRegistryURL: directRegistryJSON("https://downloads.example/plugin.zip", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"),
	}}
	h := &Handler{
		cfg:                   &config.Config{Plugins: config.PluginsConfig{Dir: writeManagementPluginFile(t, "sample-provider")}},
		pluginStoreHTTPClient: httpClient,
	}
	body := listPluginStoreForTest(t, h)
	if len(body.Plugins) != 1 || !body.Plugins[0].Installed || body.Plugins[0].Version != "0.4.0" {
		t.Fatalf("unexpected direct catalog entry: %#v", body.Plugins)
	}
	httpClient.mu.Lock()
	defer httpClient.mu.Unlock()
	if len(httpClient.counts) != 1 {
		t.Fatalf("requests = %v, want registry request only", httpClient.counts)
	}
}

type pluginReleaseDoerFunc func(*http.Request) (*http.Response, error)

func (f pluginReleaseDoerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

func pluginReleaseResponse(tag string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"tag_name":"` + tag + `"}`))}
}

func TestPluginReleaseCacheRetainsSuccessAndUsesCompletionTime(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h := &Handler{pluginReleases: pluginReleaseCache{nowFunc: func() time.Time { return now }}}
	plugin := pluginstore.Plugin{Repository: "https://github.com/test/plugin"}
	calls := 0
	client := pluginstore.Client{HTTPClient: pluginReleaseDoerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		now = now.Add(time.Minute)
		switch calls {
		case 1:
			return pluginReleaseResponse("v1.0.0"), nil
		case 2:
			return nil, errors.New("offline")
		case 3:
			return pluginReleaseResponse("invalid-tag"), nil
		default:
			return pluginReleaseResponse("v2.0.0"), nil
		}
	})}
	check := func(want string, wantCalls int) {
		t.Helper()
		if got := h.latestPluginVersion(context.Background(), client, plugin); got != want || calls != wantCalls {
			t.Fatalf("version=%q calls=%d, want %q/%d", got, calls, want, wantCalls)
		}
	}
	check("1.0.0", 1)
	now = now.Add(pluginReleaseCacheTTL - time.Second)
	check("1.0.0", 1)
	now = now.Add(time.Second)
	check("1.0.0", 2)
	check("1.0.0", 2)
	now = now.Add(pluginReleaseFailureCacheTTL)
	check("1.0.0", 3)
	now = now.Add(pluginReleaseFailureCacheTTL)
	check("2.0.0", 4)
}

// Done reports that a caller reached a cancelable wait, without relying on sleeps.
type pluginReleaseWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (ctx *pluginReleaseWaitContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
}

func TestPluginReleaseCacheCoalescesConcurrentLookups(t *testing.T) {
	t.Parallel()
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	client := pluginstore.Client{HTTPClient: pluginReleaseDoerFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return pluginReleaseResponse("v1.0.0"), nil
	})}
	h := &Handler{}
	plugin := pluginstore.Plugin{Repository: "https://github.com/test/plugin"}
	results := make(chan string, 21)
	go func() { results <- h.latestPluginVersion(context.Background(), client, plugin) }()
	<-started
	for range 20 {
		ctx := &pluginReleaseWaitContext{Context: context.Background(), waiting: make(chan struct{})}
		go func() { results <- h.latestPluginVersion(ctx, client, plugin) }()
		<-ctx.waiting
	}
	close(release)
	for range 21 {
		if got := <-results; got != "1.0.0" {
			t.Fatalf("version = %q", got)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func TestPluginReleaseCacheCanceledLeaderHandsOffToFollower(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	var calls atomic.Int32
	client := pluginstore.Client{HTTPClient: pluginReleaseDoerFunc(func(req *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		return pluginReleaseResponse("v1.0.0"), nil
	})}
	h := &Handler{}
	plugin := pluginstore.Plugin{Repository: "https://github.com/test/plugin"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	leaderResult := make(chan string, 1)
	go func() { leaderResult <- h.latestPluginVersion(ctx, client, plugin) }()
	<-started
	waitCtx := &pluginReleaseWaitContext{Context: context.Background(), waiting: make(chan struct{})}
	followerResult := make(chan string, 1)
	go func() { followerResult <- h.latestPluginVersion(waitCtx, client, plugin) }()
	<-waitCtx.waiting
	cancel()
	if got := <-leaderResult; got != "" {
		t.Fatalf("canceled leader version = %q", got)
	}
	if got := <-followerResult; got != "1.0.0" || calls.Load() != 2 {
		t.Fatalf("handoff version=%q calls=%d, want 1.0.0 and canceled + successful request", got, calls.Load())
	}
}

func TestPluginReleaseCacheCanceledFollowerDoesNotCancelLeader(t *testing.T) {
	t.Parallel()
	started, release := make(chan struct{}), make(chan struct{})
	client := pluginstore.Client{HTTPClient: pluginReleaseDoerFunc(func(*http.Request) (*http.Response, error) {
		close(started)
		<-release
		return pluginReleaseResponse("v1.0.0"), nil
	})}
	h := &Handler{}
	plugin := pluginstore.Plugin{Repository: "https://github.com/test/plugin"}
	leaderResult := make(chan string, 1)
	go func() { leaderResult <- h.latestPluginVersion(context.Background(), client, plugin) }()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waitCtx := &pluginReleaseWaitContext{Context: ctx, waiting: make(chan struct{})}
	followerResult := make(chan string, 1)
	go func() { followerResult <- h.latestPluginVersion(waitCtx, client, plugin) }()
	<-waitCtx.waiting
	cancel()
	if got := <-followerResult; got != "" {
		t.Fatalf("canceled follower version = %q", got)
	}
	close(release)
	if got := <-leaderResult; got != "1.0.0" {
		t.Fatalf("leader version = %q", got)
	}
}

func TestPluginReleaseCacheSharesConcurrencyAndCancelsQueuedLookup(t *testing.T) {
	t.Parallel()
	started, release := make(chan struct{}, 10), make(chan struct{})
	var calls atomic.Int32
	client := pluginstore.Client{HTTPClient: pluginReleaseDoerFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return pluginReleaseResponse("v1.0.0"), nil
	})}
	h := &Handler{}
	results := make(chan string, pluginReleaseConcurrency)
	for index := range pluginReleaseConcurrency {
		go func() {
			results <- h.latestPluginVersion(context.Background(), client, pluginstore.Plugin{Repository: fmt.Sprintf("https://github.com/test/plugin-%d", index)})
		}()
	}
	for range pluginReleaseConcurrency {
		<-started
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waitCtx := &pluginReleaseWaitContext{Context: ctx, waiting: make(chan struct{})}
	queuedResult := make(chan string, 1)
	plugin := pluginstore.Plugin{Repository: "https://github.com/test/queued"}
	go func() { queuedResult <- h.latestPluginVersion(waitCtx, client, plugin) }()
	<-waitCtx.waiting
	cancel()
	if got := <-queuedResult; got != "" || calls.Load() != pluginReleaseConcurrency {
		t.Fatalf("queued version=%q calls=%d", got, calls.Load())
	}
	close(release)
	for range pluginReleaseConcurrency {
		<-results
	}
	if got := h.latestPluginVersion(context.Background(), client, plugin); got != "1.0.0" || calls.Load() != pluginReleaseConcurrency+1 {
		t.Fatalf("canceled lookup was negatively cached: version=%q calls=%d", got, calls.Load())
	}
}

func TestPluginReleaseCacheSeparatesCredentialsAndEgress(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := pluginstore.Client{HTTPClient: pluginReleaseDoerFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return pluginReleaseResponse("v1.0.0"), nil
	})}
	h := &Handler{}
	plugin := pluginstore.Plugin{Repository: "https://github.com/test/plugin"}
	for _, token := range []string{"", "token-a", "token-b", "token-a"} {
		client.ResolvedAuth = nil
		if token != "" {
			client.ResolvedAuth = []pluginstore.ResolvedAuthConfig{{Match: "https://api.github.com/", Type: pluginstore.AuthTypeGitHubToken, Token: pluginstore.Secret(token)}}
			client.ResolvedAuthExpiresAt = time.Now().Add(time.Hour)
		}
		if got := h.latestPluginVersion(context.Background(), client, plugin); got != "1.0.0" {
			t.Fatalf("version = %q", got)
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
	client.NetworkScope = "other-proxy"
	h.latestPluginVersion(context.Background(), client, plugin)
	if calls.Load() != 4 {
		t.Fatalf("calls after proxy change = %d, want 4", calls.Load())
	}
	for key := range h.pluginReleases.entries {
		if strings.Contains(key, "token-") {
			t.Fatalf("cache key contains credential: %q", key)
		}
	}
}

func TestPluginReleaseChecksStopQueuedRequestsDuringCooldown(t *testing.T) {
	t.Parallel()
	started, release := make(chan struct{}, 10), make(chan struct{})
	var calls atomic.Int32
	reset := time.Now().Add(time.Hour).Truncate(time.Second)
	h := &Handler{pluginStoreRateLimiter: &pluginstore.GitHubRateLimiter{}}
	h.pluginStoreHTTPClient = pluginReleaseDoerFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		headers := make(http.Header)
		headers.Set("X-RateLimit-Remaining", "0")
		headers.Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		return &http.Response{StatusCode: 403, Header: headers, Body: io.NopCloser(strings.NewReader("limited"))}, nil
	})
	client := h.newPluginStoreClient("", "", nil)
	plugins := make([]pluginstore.Plugin, 10)
	h.pluginReleases.entries = make(map[string]pluginReleaseCacheEntry)
	for index := range plugins {
		plugins[index] = pluginstore.Plugin{ID: fmt.Sprintf("plugin-%d", index), Repository: fmt.Sprintf("https://github.com/owner/plugin-%d", index)}
		h.pluginReleases.entries[pluginReleaseKey(client, plugins[index])] = pluginReleaseCacheEntry{version: "1.0.0"}
	}
	result := make(chan []string, 1)
	go func() { result <- h.latestPluginVersions(context.Background(), client, plugins) }()
	for range pluginReleaseConcurrency {
		<-started
	}
	close(release)
	for _, version := range <-result {
		if version != "1.0.0" {
			t.Fatalf("cooldown discarded previous version: %q", version)
		}
	}
	if calls.Load() != pluginReleaseConcurrency {
		t.Fatalf("requests=%d, want only the %d already in flight", calls.Load(), pluginReleaseConcurrency)
	}
	for _, entry := range h.pluginReleases.entries {
		if !entry.nextCheckAt.Equal(reset) {
			t.Fatalf("next check=%s, want GitHub reset %s", entry.nextCheckAt, reset)
		}
	}
}

func TestPluginStoreListingCooldownAlsoBlocksInstallation(t *testing.T) {
	t.Parallel()
	var apiCalls atomic.Int32
	h := &Handler{
		cfg:                    &config.Config{Plugins: config.PluginsConfig{Dir: writeManagementPluginFile(t, "sample-provider")}},
		pluginStoreRateLimiter: &pluginstore.GitHubRateLimiter{},
		pluginStoreHTTPClient: pluginReleaseDoerFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host != "api.github.com" {
				return fakePluginStoreHTTPClient{pluginstore.DefaultRegistryURL: registryJSON(t)}.Do(req)
			}
			apiCalls.Add(1)
			headers := make(http.Header)
			headers.Set("Retry-After", "3600")
			return &http.Response{StatusCode: 429, Header: headers, Body: io.NopCloser(strings.NewReader("limited"))}, nil
		}),
	}
	body := listPluginStoreForTest(t, h)
	if len(body.Plugins) != 1 || body.Plugins[0].Version != "0.1.0" {
		t.Fatalf("listing failed to fall back during cooldown: %#v", body)
	}
	for _, query := range []string{"", "?version=0.2.0"} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Params = gin.Params{{Key: "id", Value: "sample-provider"}}
		c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/plugin-store/sample-provider/install"+query, nil)
		h.InstallPluginFromStore(c)
		if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "plugin_store_rate_limited") || !strings.Contains(rec.Body.String(), "retry_at") {
			t.Fatalf("install status=%d body=%s", rec.Code, rec.Body.String())
		}
		retryAfter, errRetry := strconv.Atoi(rec.Header().Get("Retry-After"))
		if errRetry != nil || retryAfter <= 0 {
			t.Fatalf("Retry-After=%q", rec.Header().Get("Retry-After"))
		}
	}
	if apiCalls.Load() != 1 {
		t.Fatalf("cooldown installation made more requests: %d", apiCalls.Load())
	}
}

func TestPluginStoreInstallationPropagatesRegistryRateLimit(t *testing.T) {
	t.Parallel()
	h := &Handler{
		cfg:                    &config.Config{Plugins: config.PluginsConfig{Dir: t.TempDir()}},
		pluginStoreRegistryURL: "https://api.github.com/repos/owner/store/contents/registry.json",
		pluginStoreRateLimiter: &pluginstore.GitHubRateLimiter{},
		pluginStoreHTTPClient: pluginReleaseDoerFunc(func(*http.Request) (*http.Response, error) {
			headers := make(http.Header)
			headers.Set("Retry-After", "3600")
			return &http.Response{StatusCode: 429, Header: headers, Body: io.NopCloser(strings.NewReader("limited"))}, nil
		}),
	}
	for _, query := range []string{"", "?source=official"} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Params = gin.Params{{Key: "id", Value: "sample-provider"}}
		c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/plugin-store/sample-provider/install"+query, nil)
		h.InstallPluginFromStore(c)
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
			t.Fatalf("install status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
}

func TestPluginStoreTagInstallationStopsOnRateLimitError(t *testing.T) {
	t.Parallel()
	calls := 0
	client := pluginstore.Client{HTTPClient: pluginReleaseDoerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, &pluginstore.RateLimitError{StatusCode: 429, RetryAt: time.Now().Add(time.Hour)}
	})}
	_, errInstall := installPluginStoreGitHubRelease(context.Background(), client, pluginstore.Plugin{
		ID: "sample-provider", Name: "Sample", Description: "Test plugin", Author: "owner", Repository: "https://github.com/owner/repo",
	}, "1.0.0", pluginstore.InstallOptions{PluginsDir: t.TempDir(), GOOS: "linux", GOARCH: "amd64"})
	var rateLimit *pluginstore.RateLimitError
	if !errors.As(errInstall, &rateLimit) || calls != 1 {
		t.Fatalf("error=%v calls=%d, want immediate rate-limit failure without alternate tag", errInstall, calls)
	}
}

func TestListPluginStoreUsesCachedUninstalledVersionWithoutRefresh(t *testing.T) {
	t.Parallel()
	httpClient := &countingPluginStoreHTTPClient{responses: fakePluginStoreHTTPClient{pluginstore.DefaultRegistryURL: registryJSON(t)}}
	h := &Handler{cfg: &config.Config{Plugins: config.PluginsConfig{Dir: t.TempDir()}}, pluginStoreHTTPClient: httpClient}
	plugin := pluginstore.Plugin{Repository: "https://github.com/author-name/cliproxy-sample-provider-plugin"}
	key := pluginReleaseKey(h.newPluginStoreClient("", "", nil), plugin)
	h.pluginReleases.entries = map[string]pluginReleaseCacheEntry{key: {version: "2.0.0"}}
	body := listPluginStoreForTest(t, h)
	if body.Plugins[0].Version != "2.0.0" || len(httpClient.counts) != 1 {
		t.Fatalf("plugins=%#v requests=%v", body.Plugins, httpClient.counts)
	}
}
