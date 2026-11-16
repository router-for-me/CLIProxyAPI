package usagestats

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

// PluginName is the usage plugin registration name.
const PluginName = "proxx-usage"

// DefaultFileName is the snapshot file name used next to the config file.
const DefaultFileName = "proxx-usage.json"

// FlushInterval is how often a changed store is snapshotted to disk.
const FlushInterval = time.Minute

var (
	defaultStore = NewStore(DefaultRecentLimit)
	runtimeMu    sync.Mutex
	runtimeState struct {
		file    string
		loaded  bool
		started bool
		cancel  context.CancelFunc
		done    chan struct{}
	}
)

// Default returns the process-wide store.
func Default() *Store { return defaultStore }

// ResolveFile returns the snapshot path for cfg: an absolute or config-relative File, or
// proxx-usage.json next to the config file.
func ResolveFile(cfg config.ProxxUsageConfig, configPath string) string {
	file := strings.TrimSpace(cfg.File)
	base := "."
	if configPath != "" {
		base = filepath.Dir(configPath)
	}
	if file == "" {
		return filepath.Join(base, DefaultFileName)
	}
	if filepath.IsAbs(file) {
		return file
	}
	return filepath.Join(base, file)
}

// Configure applies proxx.usage: registers the usage plugin, loads the snapshot once,
// and starts the periodic flush loop bound to ctx. Safe to call on every reload.
func Configure(ctx context.Context, cfg config.ProxxUsageConfig, configPath string) {
	store := defaultStore
	store.SetEnabled(!cfg.Disabled)
	store.SetRecentLimit(cfg.RecentLimit)
	file := ResolveFile(cfg, configPath)

	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	if !runtimeState.loaded {
		if err := store.Load(file); err != nil {
			log.WithError(err).Warn("proxx: failed to load usage snapshot")
		}
		runtimeState.loaded = true
	}
	runtimeState.file = file
	usage.RegisterNamedPlugin(PluginName, store)
	if ctx == nil || runtimeState.started {
		return
	}
	loopCtx, cancel := context.WithCancel(ctx)
	runtimeState.started = true
	runtimeState.cancel = cancel
	runtimeState.done = make(chan struct{})
	go flushLoop(loopCtx, runtimeState.done)
}

func currentFile() string {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	return runtimeState.file
}

func flushLoop(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := defaultStore.Save(currentFile(), false); err != nil {
				log.WithError(err).Warn("proxx: failed to save usage snapshot")
			}
		}
	}
}

// Shutdown stops the flush loop and writes a final snapshot.
func Shutdown() {
	runtimeMu.Lock()
	cancel, done := runtimeState.cancel, runtimeState.done
	runtimeState.started = false
	runtimeState.cancel = nil
	runtimeState.done = nil
	file := runtimeState.file
	runtimeMu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	if err := defaultStore.Save(file, false); err != nil {
		log.WithError(err).Warn("proxx: failed to save usage snapshot")
	}
}
