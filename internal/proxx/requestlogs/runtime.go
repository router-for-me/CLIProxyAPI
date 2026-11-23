package requestlogs

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// pollInterval is how often the directory is polled while a live stream is open. Polling is
// used instead of fsnotify because the kqueue backend (macOS, BSD) opens one descriptor per
// file in a watched directory, which a large log directory would exhaust.
const pollInterval = time.Second

// retentionEvery is how many poll ticks pass between periodic refresh + retention runs (60s).
const retentionEvery = 60

var (
	defaultStore = NewStore()
	runtimeMu    sync.Mutex
	runtimeState struct {
		dir     string
		started bool
		cancel  context.CancelFunc
		done    chan struct{}
	}
)

// Default returns the process-wide store.
func Default() *Store { return defaultStore }

// ResolveDir returns the request log directory the same way the request logger does
// (internal/api/server_options.go defaultRequestLoggerFactory): the log directory, joined with
// the config file's directory when relative.
func ResolveDir(cfg *config.Config, configPath string) string {
	dir := logging.ResolveLogDirectory(cfg)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(filepath.Dir(configPath), dir)
	}
	return dir
}

// fileLoggingEnabled mirrors when the request logger writes files: request-log on, not forced
// off by commercial mode, and not forwarding to CLIProxyAPIHome.
func fileLoggingEnabled(cfg *config.Config) bool {
	return cfg.RequestLog && !cfg.CommercialMode && !cfg.Home.Enabled
}

// Configure applies proxx.request-logs and the request-log flag, registers the usage join plugin,
// starts the poll/retention loop bound to ctx and runs a stat refresh (which enforces retention).
// The directory and the persisted index path are resolved once, like the request logger, which is
// built once at startup. The index is loaded by the first refresh; meta parsing waits for the first
// viewer use.
func Configure(ctx context.Context, cfg *config.Config, configPath string) {
	if cfg == nil {
		return
	}
	runtimeMu.Lock()
	if runtimeState.dir == "" {
		runtimeState.dir = ResolveDir(cfg, configPath)
		defaultStore.setIndexFile(ResolveIndexFile(configPath))
	}
	dir := runtimeState.dir
	defaultStore.Configure(dir, cfg.Proxx.RequestLogs.MaxTotalSizeBytes(), fileLoggingEnabled(cfg))
	usage.RegisterNamedPlugin(UsagePluginName, defaultStore.usage)
	if ctx != nil && !runtimeState.started {
		loopCtx, cancel := context.WithCancel(ctx)
		runtimeState.started = true
		runtimeState.cancel = cancel
		runtimeState.done = make(chan struct{})
		go pollLoop(loopCtx, runtimeState.done)
	}
	runtimeMu.Unlock()
	go defaultStore.Refresh()
}

func pollLoop(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	tick := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick++
			if tick%retentionEvery == 0 || defaultStore.hasSubscribers() {
				defaultStore.Refresh()
			}
		}
	}
}

// Shutdown stops the poll loop and the meta worker, then saves the index if it changed.
func Shutdown() {
	runtimeMu.Lock()
	cancel, done := runtimeState.cancel, runtimeState.done
	runtimeState.started = false
	runtimeState.cancel = nil
	runtimeState.done = nil
	runtimeMu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	defaultStore.stopIndexing()
	defaultStore.saveIndex(true)
}
