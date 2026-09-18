package management

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginstore"
	log "github.com/sirupsen/logrus"
)

const (
	pluginReleaseCacheTTL        = time.Hour
	pluginReleaseFailureCacheTTL = 30 * time.Second
	pluginReleaseConcurrency     = 2
)

type pluginReleaseCacheEntry struct {
	version     string
	nextCheckAt time.Time
}

type pluginReleaseLookup struct {
	done     chan struct{}
	version  string
	canceled bool
}

// pluginReleaseCache shares both in-flight lookups and the concurrency budget
// across all catalog requests handled by this management server.
type pluginReleaseCache struct {
	mu       sync.Mutex
	entries  map[string]pluginReleaseCacheEntry
	inflight map[string]*pluginReleaseLookup
	slots    chan struct{}
	nowFunc  func() time.Time
}

func (cache *pluginReleaseCache) now() time.Time {
	if cache.nowFunc != nil {
		return cache.nowFunc()
	}
	return time.Now()
}

func pluginReleaseKey(client pluginstore.Client, plugin pluginstore.Plugin) string {
	if pluginstore.PluginInstallType(plugin) != pluginstore.InstallTypeGitHubRelease || plugin.Repository == "" {
		return ""
	}
	key, errKey := client.LatestReleaseCacheKey(plugin)
	if errKey != nil {
		return ""
	}
	return key
}

// cached returns the last successful result without causing network activity.
func (cache *pluginReleaseCache) cached(client pluginstore.Client, plugin pluginstore.Plugin) string {
	key := pluginReleaseKey(client, plugin)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.entries[key].version
}

// latestPluginVersions preserves catalog order, including skipped placeholders.
func (h *Handler) latestPluginVersions(ctx context.Context, client pluginstore.Client, plugins []pluginstore.Plugin) []string {
	versions := make([]string, len(plugins))
	var wg sync.WaitGroup
	for index, plugin := range plugins {
		if pluginReleaseKey(client, plugin) == "" {
			continue
		}
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			versions[index] = h.latestPluginVersion(ctx, client, plugins[index])
		}(index)
	}
	wg.Wait()
	return versions
}

func (h *Handler) latestPluginVersion(ctx context.Context, client pluginstore.Client, plugin pluginstore.Plugin) string {
	if pluginstore.PluginInstallType(plugin) != pluginstore.InstallTypeGitHubRelease {
		return ""
	}
	client, key, errPrepare := client.PrepareLatestRelease(plugin)
	if errPrepare != nil {
		return ""
	}
	cache := &h.pluginReleases
	var entry pluginReleaseCacheEntry
	var lookup *pluginReleaseLookup
	for {
		cache.mu.Lock()
		entry = cache.entries[key]
		if cache.now().Before(entry.nextCheckAt) || ctx.Err() != nil {
			cache.mu.Unlock()
			return entry.version
		}
		if pending := cache.inflight[key]; pending != nil {
			cache.mu.Unlock()
			select {
			case <-pending.done:
				if pending.canceled && ctx.Err() == nil {
					// A live waiter takes ownership after a canceled leader. All
					// other waiters coalesce behind the replacement lookup.
					continue
				}
				return pending.version
			case <-ctx.Done():
				return entry.version
			}
		}
		if cache.entries == nil {
			cache.entries = make(map[string]pluginReleaseCacheEntry)
		}
		if cache.inflight == nil {
			cache.inflight = make(map[string]*pluginReleaseLookup)
			cache.slots = make(chan struct{}, pluginReleaseConcurrency)
		}
		lookup = &pluginReleaseLookup{done: make(chan struct{})}
		cache.inflight[key] = lookup
		cache.mu.Unlock()
		break
	}

	// Every listing uses the same slots. Canceled waiters do not consume a slot
	// or install a negative cache entry.
	select {
	case cache.slots <- struct{}{}:
		if ctx.Err() == nil {
			release, errRelease := client.FetchLatestRelease(ctx, plugin)
			version := ""
			if errRelease == nil {
				version, errRelease = pluginstore.ReleaseVersion(release)
			}
			if ctx.Err() == nil {
				ttl := pluginReleaseFailureCacheTTL
				if errRelease != nil {
					log.WithError(errRelease).WithField("plugin_id", plugin.ID).Warn("pluginstore: failed to fetch latest release")
				} else {
					entry.version = version
					ttl = pluginReleaseCacheTTL
				}
				// Start the TTL when the lookup finishes, not when it was queued.
				entry.nextCheckAt = cache.now().Add(ttl)
				var rateLimit *pluginstore.RateLimitError
				if errors.As(errRelease, &rateLimit) {
					entry.nextCheckAt = rateLimit.RetryAt
				}
			}
		}
		<-cache.slots
	case <-ctx.Done():
	}

	cache.mu.Lock()
	cache.entries[key] = entry
	lookup.version = entry.version
	lookup.canceled = ctx.Err() != nil
	delete(cache.inflight, key)
	close(lookup.done)
	cache.mu.Unlock()
	return entry.version
}
