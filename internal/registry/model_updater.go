package registry

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	modelsFetchTimeout    = 30 * time.Second
	modelsRefreshInterval = 3 * time.Hour

	// Startup retry backoff for the initial remote catalog fetch. The first
	// fetch runs concurrently with process startup, so a transport that is
	// not ready yet (e.g. a local proxy that starts after login) can make it
	// fail; without retries the registry silently falls back to the embedded
	// catalog, which may be outdated and miss newly released models until
	// the next periodic tick. Retries start at 10s, double with ±20% jitter,
	// cap at 5 minutes, and never give up: once the first fetch succeeds the
	// regular modelsRefreshInterval ticker takes over.
	startupRetryBaseDelay = 10 * time.Second
	startupRetryMaxDelay  = 5 * time.Minute
)

// startupSleep waits for d or until ctx is cancelled. It is a package
// variable so tests can run the startup retry loop without real delays.
var startupSleep = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// fetchModelsFromRemoteFn is the fetch entry used by the startup retry loop.
// Tests replace it to simulate transient remote failures.
var fetchModelsFromRemoteFn = fetchModelsFromRemote

var modelsURLs = []string{
	"https://raw.githubusercontent.com/router-for-me/models/refs/heads/main/models.json",
	"https://models.router-for.me/models.json",
}

//go:embed models/models.json
var embeddedModelsJSON []byte

type modelStore struct {
	mu   sync.RWMutex
	data *staticModelsJSON
}

var modelsCatalogStore = &modelStore{}

var updaterOnce sync.Once

// ModelRefreshCallback is invoked when startup or periodic model refresh detects changes.
// changedProviders contains the provider names whose model definitions changed.
type ModelRefreshCallback func(changedProviders []string)

var (
	refreshCallbackMu     sync.Mutex
	refreshCallback       ModelRefreshCallback
	pendingRefreshChanges []string
)

// SetModelRefreshCallback registers a callback that is invoked when startup or
// periodic model refresh detects changes. Only one callback is supported;
// subsequent calls replace the previous callback.
func SetModelRefreshCallback(cb ModelRefreshCallback) {
	refreshCallbackMu.Lock()
	refreshCallback = cb
	var pending []string
	if cb != nil && len(pendingRefreshChanges) > 0 {
		pending = append([]string(nil), pendingRefreshChanges...)
		pendingRefreshChanges = nil
	}
	refreshCallbackMu.Unlock()

	if cb != nil && len(pending) > 0 {
		cb(pending)
	}
}

func init() {
	// Load embedded data as fallback on startup.
	if err := loadModelsFromBytes(embeddedModelsJSON, "embed"); err != nil {
		log.Warnf("registry: failed to parse embedded models.json (embedded catalog may be incomplete or invalid; continuing startup and will rely on remote model refresh): %v", err)
	}
}

// StartModelsUpdater starts a background updater that fetches models
// immediately on startup and then refreshes the model catalog every 3 hours.
// Safe to call multiple times; only one updater will run.
func StartModelsUpdater(ctx context.Context) {
	updaterOnce.Do(func() {
		go runModelsUpdater(ctx)
	})
}

func runModelsUpdater(ctx context.Context) {
	runStartupRefreshWithBackoff(ctx)
	periodicRefresh(ctx)
}

// runStartupRefreshWithBackoff performs the initial remote catalog fetch and,
// on failure, retries with exponential backoff (10s doubling with jitter,
// capped at 5 minutes) until it succeeds or ctx is cancelled. Every failed
// attempt is logged at error level so a degraded startup is visible in file
// logs. Periodic refresh remains responsible for all subsequent updates.
func runStartupRefreshWithBackoff(ctx context.Context) {
	delay := startupRetryBaseDelay
	for attempt := 1; ; attempt++ {
		if tryStartupRefresh(ctx) {
			if attempt > 1 {
				log.WithFields(log.Fields{
					"attempts": attempt,
				}).Info("startup model refresh succeeded after retries")
			}
			return
		}
		log.WithFields(log.Fields{
			"attempt":     attempt,
			"retry_delay": delay.String(),
		}).Error("startup model catalog fetch failed; retrying")
		if errSleep := startupSleep(ctx, jitteredDelay(delay)); errSleep != nil {
			// Context cancelled (shutdown): periodic refresh will not run either.
			log.WithField("reason", errSleep.Error()).Info("startup model refresh retry loop stopped")
			return
		}
		delay = min(delay*2, startupRetryMaxDelay)
	}
}

// jitteredDelay spreads the retry delay by ±20% so a fleet of restarts (or
// multiple local instances) does not hammer the catalog mirrors in sync.
func jitteredDelay(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	spread := d / 5
	if spread <= 0 {
		return d
	}
	return d - spread + time.Duration(rand.Int64N(int64(2*spread)+1))
}

func periodicRefresh(ctx context.Context) {
	ticker := time.NewTicker(modelsRefreshInterval)
	defer ticker.Stop()
	log.Infof("periodic model refresh started (interval=%s)", modelsRefreshInterval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tryPeriodicRefresh(ctx)
		}
	}
}

// tryPeriodicRefresh fetches models from remote, compares with the current
// catalog, and notifies the registered callback if any provider changed.
// A failed periodic attempt keeps the current data; the next tick retries,
// so no backoff loop is needed here.
func tryPeriodicRefresh(ctx context.Context) {
	if !tryRefreshModels(ctx, "periodic model refresh") {
		log.WithField("next_retry_after", modelsRefreshInterval.String()).Error("periodic model refresh failed: fetch failed from all URLs; keeping current catalog until next tick")
	}
}

// tryStartupRefresh runs one startup fetch attempt. It reports whether the
// remote catalog was fetched and applied.
func tryStartupRefresh(ctx context.Context) bool {
	return tryRefreshModels(ctx, "startup model refresh")
}

// tryRefreshModels fetches models from remote and swaps the store on success.
// It returns true when a remote catalog was fetched (regardless of whether
// it differed from the current data), false when all URLs failed.
func tryRefreshModels(ctx context.Context, label string) bool {
	oldData := getModels()

	parsed, url := fetchModelsFromRemoteFn(ctx)
	if parsed == nil {
		log.Warnf("%s: fetch failed from all URLs, keeping current data", label)
		return false
	}

	// Detect changes before updating store.
	changed := detectChangedProviders(oldData, parsed)

	// Update store with new data regardless.
	modelsCatalogStore.mu.Lock()
	modelsCatalogStore.data = parsed
	modelsCatalogStore.mu.Unlock()

	if len(changed) == 0 {
		log.Infof("%s completed from %s, no changes detected", label, url)
		return true
	}

	log.Infof("%s completed from %s, changes detected for providers: %v", label, url, changed)
	notifyModelRefresh(changed)
	return true
}

// fetchModelsFromRemote tries all remote URLs and returns the parsed model catalog
// along with the URL it was fetched from. Returns (nil, "") if all fetches fail.
func fetchModelsFromRemote(ctx context.Context) (*staticModelsJSON, string) {
	client := &http.Client{Timeout: modelsFetchTimeout}
	for _, url := range modelsURLs {
		reqCtx, cancel := context.WithTimeout(ctx, modelsFetchTimeout)
		req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
		if err != nil {
			cancel()
			log.Debugf("models fetch request creation failed for %s: %v", url, err)
			continue
		}

		resp, err := client.Do(req)
		if err != nil {
			cancel()
			log.Debugf("models fetch failed from %s: %v", url, err)
			continue
		}

		if resp.StatusCode != 200 {
			resp.Body.Close()
			cancel()
			log.Debugf("models fetch returned %d from %s", resp.StatusCode, url)
			continue
		}

		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()

		if err != nil {
			log.Debugf("models fetch read error from %s: %v", url, err)
			continue
		}

		var parsed staticModelsJSON
		if err := json.Unmarshal(data, &parsed); err != nil {
			log.Warnf("models parse failed from %s: %v", url, err)
			continue
		}
		if err := validateModelsCatalog(&parsed); err != nil {
			log.Warnf("models validate failed from %s: %v", url, err)
			continue
		}

		return &parsed, url
	}
	return nil, ""
}

// detectChangedProviders compares two model catalogs and returns provider names
// whose model definitions differ. Gemini changes affect both Gemini protocols,
// while Codex tiers (free/team/plus/pro) are grouped under one "codex" provider.
func detectChangedProviders(oldData, newData *staticModelsJSON) []string {
	if oldData == nil || newData == nil {
		return nil
	}

	type section struct {
		provider string
		oldList  []*ModelInfo
		newList  []*ModelInfo
	}

	sections := []section{
		{"claude", oldData.Claude, newData.Claude},
		{"gemini", oldData.Gemini, newData.Gemini},
		{"gemini-interactions", oldData.Gemini, newData.Gemini},
		{"vertex", oldData.Vertex, newData.Vertex},
		{"aistudio", oldData.AIStudio, newData.AIStudio},
		{"codex", oldData.CodexFree, newData.CodexFree},
		{"codex", oldData.CodexTeam, newData.CodexTeam},
		{"codex", oldData.CodexPlus, newData.CodexPlus},
		{"codex", oldData.CodexPro, newData.CodexPro},
		{"kimi", oldData.Kimi, newData.Kimi},
		{"antigravity", oldData.Antigravity, newData.Antigravity},
		{"xai", oldData.XAI, newData.XAI},
	}

	seen := make(map[string]bool, len(sections))
	var changed []string
	for _, s := range sections {
		if seen[s.provider] {
			continue
		}
		if modelSectionChanged(s.oldList, s.newList) {
			changed = append(changed, s.provider)
			seen[s.provider] = true
		}
	}
	return changed
}

// modelSectionChanged reports whether two model slices differ.
func modelSectionChanged(a, b []*ModelInfo) bool {
	if len(a) != len(b) {
		return true
	}
	if len(a) == 0 {
		return false
	}
	aj, err1 := json.Marshal(a)
	bj, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return true
	}
	return string(aj) != string(bj)
}

func notifyModelRefresh(changedProviders []string) {
	if len(changedProviders) == 0 {
		return
	}

	refreshCallbackMu.Lock()
	cb := refreshCallback
	if cb == nil {
		pendingRefreshChanges = mergeProviderNames(pendingRefreshChanges, changedProviders)
		refreshCallbackMu.Unlock()
		return
	}
	refreshCallbackMu.Unlock()
	cb(changedProviders)
}

func mergeProviderNames(existing, incoming []string) []string {
	if len(incoming) == 0 {
		return existing
	}
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	merged := make([]string, 0, len(existing)+len(incoming))
	for _, provider := range existing {
		name := strings.ToLower(strings.TrimSpace(provider))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		merged = append(merged, name)
	}
	for _, provider := range incoming {
		name := strings.ToLower(strings.TrimSpace(provider))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		merged = append(merged, name)
	}
	return merged
}

func loadModelsFromBytes(data []byte, source string) error {
	var parsed staticModelsJSON
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("%s: decode models catalog: %w", source, err)
	}
	if err := validateModelsCatalog(&parsed); err != nil {
		return fmt.Errorf("%s: validate models catalog: %w", source, err)
	}

	modelsCatalogStore.mu.Lock()
	modelsCatalogStore.data = &parsed
	modelsCatalogStore.mu.Unlock()
	return nil
}

func getModels() *staticModelsJSON {
	modelsCatalogStore.mu.RLock()
	defer modelsCatalogStore.mu.RUnlock()
	return modelsCatalogStore.data
}

func validateModelsCatalog(data *staticModelsJSON) error {
	if data == nil {
		return fmt.Errorf("catalog is nil")
	}

	requiredSections := []struct {
		name   string
		models []*ModelInfo
	}{
		{name: "claude", models: data.Claude},
		{name: "gemini", models: data.Gemini},
		{name: "vertex", models: data.Vertex},
		{name: "aistudio", models: data.AIStudio},
		{name: "codex-free", models: data.CodexFree},
		{name: "codex-team", models: data.CodexTeam},
		{name: "codex-plus", models: data.CodexPlus},
		{name: "codex-pro", models: data.CodexPro},
		{name: "kimi", models: data.Kimi},
		{name: "antigravity", models: data.Antigravity},
		{name: "xai", models: data.XAI},
	}

	for _, section := range requiredSections {
		if err := validateModelSection(section.name, section.models); err != nil {
			return err
		}
	}
	return nil
}

func validateModelSection(section string, models []*ModelInfo) error {
	if len(models) == 0 {
		log.Warnf("models catalog: %s section is empty, continuing without those model definitions", section)
		return nil
	}

	seen := make(map[string]struct{}, len(models))
	for i, model := range models {
		if model == nil {
			return fmt.Errorf("%s[%d] is null", section, i)
		}
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			return fmt.Errorf("%s[%d] has empty id", section, i)
		}
		if _, exists := seen[modelID]; exists {
			return fmt.Errorf("%s contains duplicate model id %q", section, modelID)
		}
		seen[modelID] = struct{}{}
	}
	return nil
}
