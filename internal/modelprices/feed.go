// Package modelprices keeps a local copy of a public model price list (LiteLLM
// format) so request costs can be estimated for models without a configured price.
package modelprices

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/httpfetch"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
)

const (
	// maxFeedSize bounds the downloaded price list (LiteLLM's is a few MB).
	maxFeedSize = 32 << 20
	// fetchTimeout bounds a background price-list download; it never applies to
	// proxied model traffic.
	fetchTimeout = 60 * time.Second
	// retryAfterError delays the next attempt after a failed download.
	retryAfterError = time.Hour
	checkInterval   = 10 * time.Minute
)

// Price holds USD prices per one million tokens.
type Price struct {
	Input       float64 `json:"input"`
	Output      float64 `json:"output"`
	CachedInput float64 `json:"cached_input,omitempty"`
	CacheWrite  float64 `json:"cache_write,omitempty"`
}

// Status describes the feed for the management panel.
type Status struct {
	Enabled   bool      `json:"enabled"`
	URL       string    `json:"url"`
	Models    int       `json:"models"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	LastError string    `json:"last_error,omitempty"`
}

type cacheFile struct {
	URL       string           `json:"url"`
	FetchedAt time.Time        `json:"fetched_at"`
	Prices    map[string]Price `json:"prices"`
}

// Feed downloads, caches and serves model prices.
type Feed struct {
	mu        sync.RWMutex
	prices    map[string]Price
	url       string
	fetchedAt time.Time
	lastErr   string
	nextTry   time.Time
	cachePath string
	cfgFn     func() *config.Config
	listeners []func()
	now       func() time.Time
	fetch     func(ctx context.Context, url string, cfg *config.Config) ([]byte, error)
	startOnce sync.Once
	stop      chan struct{}
}

var defaultFeed = New()

// Default returns the process-wide feed.
func Default() *Feed { return defaultFeed }

// New returns an empty feed.
func New() *Feed {
	return &Feed{now: time.Now, fetch: download, stop: make(chan struct{})}
}

// Init loads the on-disk cache and starts background refreshes.
func (f *Feed) Init(cachePath string, cfgFn func() *config.Config) {
	f.mu.Lock()
	f.cachePath = cachePath
	f.cfgFn = cfgFn
	f.mu.Unlock()
	f.loadCache()
	f.startOnce.Do(func() { go f.loop() })
}

// OnUpdate registers fn to run after prices are loaded or refreshed.
func (f *Feed) OnUpdate(fn func()) {
	f.mu.Lock()
	f.listeners = append(f.listeners, fn)
	loaded := len(f.prices) > 0
	f.mu.Unlock()
	if loaded {
		fn()
	}
}

// Close stops background refreshes.
func (f *Feed) Close() {
	select {
	case <-f.stop:
	default:
		close(f.stop)
	}
}

func (f *Feed) config() *config.Config {
	f.mu.RLock()
	cfgFn := f.cfgFn
	f.mu.RUnlock()
	if cfgFn == nil {
		return nil
	}
	return cfgFn()
}

func (f *Feed) settings() config.ModelPriceFeed {
	if cfg := f.config(); cfg != nil {
		return cfg.ModelPriceFeed
	}
	return config.ModelPriceFeed{}
}

// Lookup returns the price of the first model name the feed knows.
func (f *Feed) Lookup(models ...string) (Price, bool) {
	if f.settings().Disabled {
		return Price{}, false
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, model := range models {
		for _, name := range candidateNames(model) {
			if p, ok := f.prices[name]; ok {
				return p, true
			}
		}
	}
	return Price{}, false
}

// Status reports the feed's state.
func (f *Feed) Status() Status {
	s := f.settings()
	f.mu.RLock()
	defer f.mu.RUnlock()
	return Status{
		Enabled:   !s.Disabled,
		URL:       s.EffectiveURL(),
		Models:    len(f.prices),
		UpdatedAt: f.fetchedAt,
		LastError: f.lastErr,
	}
}

func (f *Feed) loop() {
	f.refreshIfDue(context.Background())
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			f.refreshIfDue(context.Background())
		case <-f.stop:
			return
		}
	}
}

func (f *Feed) refreshIfDue(ctx context.Context) {
	s := f.settings()
	if s.Disabled {
		return
	}
	url := s.EffectiveURL()
	now := f.now()
	f.mu.RLock()
	due := len(f.prices) == 0 || f.url != url || now.Sub(f.fetchedAt) >= s.RefreshInterval()
	waiting := now.Before(f.nextTry)
	f.mu.RUnlock()
	if !due || waiting {
		return
	}
	if err := f.Refresh(ctx); err != nil {
		log.WithError(err).Warn("model prices: failed to refresh price list")
	}
}

// Refresh downloads the price list now.
func (f *Feed) Refresh(ctx context.Context) error {
	cfg := f.config()
	url := f.settings().EffectiveURL()
	data, err := f.fetch(ctx, url, cfg)
	var prices map[string]Price
	if err == nil {
		prices, err = Parse(data)
	}
	if err == nil && len(prices) == 0 {
		err = fmt.Errorf("price list from %s has no usable models", url)
	}
	now := f.now()
	f.mu.Lock()
	if err != nil {
		f.lastErr = err.Error()
		f.nextTry = now.Add(retryAfterError)
		f.mu.Unlock()
		return err
	}
	f.prices, f.url, f.fetchedAt, f.lastErr, f.nextTry = prices, url, now, "", time.Time{}
	listeners := append([]func(){}, f.listeners...)
	f.mu.Unlock()
	f.saveCache()
	log.Infof("model prices: loaded %d models from %s", len(prices), url)
	for _, fn := range listeners {
		fn()
	}
	return nil
}

func download(ctx context.Context, url string, cfg *config.Config) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	client := &http.Client{}
	proxyURL := ""
	if cfg != nil {
		proxyURL = cfg.ProxyURL
	}
	util.SetProxy(&sdkconfig.SDKConfig{ProxyURL: strings.TrimSpace(proxyURL)}, client)
	return httpfetch.GetBytes(ctx, client, url, map[string]string{"User-Agent": "cli-proxy-api-price-feed"}, maxFeedSize)
}

func (f *Feed) loadCache() {
	f.mu.RLock()
	path := f.cachePath
	f.mu.RUnlock()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.WithError(err).Warn("model prices: failed to read cached price list")
		}
		return
	}
	var c cacheFile
	if err = json.Unmarshal(data, &c); err != nil || len(c.Prices) == 0 {
		log.WithError(err).Warn("model prices: ignoring unreadable cached price list")
		return
	}
	f.mu.Lock()
	f.prices, f.url, f.fetchedAt = c.Prices, c.URL, c.FetchedAt
	listeners := append([]func(){}, f.listeners...)
	f.mu.Unlock()
	for _, fn := range listeners {
		fn()
	}
}

func (f *Feed) saveCache() {
	f.mu.RLock()
	path := f.cachePath
	c := cacheFile{URL: f.url, FetchedAt: f.fetchedAt, Prices: f.prices}
	data, err := json.Marshal(c)
	f.mu.RUnlock()
	if path == "" {
		return
	}
	if err == nil {
		tmp := path + ".tmp"
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			if err = os.WriteFile(tmp, data, 0o600); err == nil {
				err = os.Rename(tmp, path)
			}
		}
	}
	if err != nil {
		log.WithError(err).Warn("model prices: failed to cache price list")
	}
}

// liteLLMEntry is the subset of a LiteLLM price entry the feed uses (USD per token).
type liteLLMEntry struct {
	Input      *float64 `json:"input_cost_per_token"`
	Output     *float64 `json:"output_cost_per_token"`
	CacheRead  *float64 `json:"cache_read_input_token_cost"`
	CacheWrite *float64 `json:"cache_creation_input_token_cost"`
	Provider   string   `json:"litellm_provider"`
	Mode       string   `json:"mode"`
}

// preferredProviders win when several provider-prefixed entries share a model name.
var preferredProviders = map[string]int{
	"openai": 1, "anthropic": 1, "gemini": 1, "xai": 1, "deepseek": 1, "mistral": 1,
	"vertex_ai-language-models": 2, "vertex_ai": 2, "azure": 3, "bedrock": 4,
}

// Parse converts a LiteLLM price JSON into per-model prices keyed by lowercase
// model name without provider prefix. Unprefixed entries win over prefixed ones,
// and first-party providers over resellers.
func Parse(data []byte) (map[string]Price, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse price list: %w", err)
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	type ranked struct {
		price Price
		rank  int
	}
	best := make(map[string]ranked)
	for _, key := range keys {
		var e liteLLMEntry
		if json.Unmarshal(raw[key], &e) != nil || e.Input == nil || e.Output == nil {
			continue
		}
		switch e.Mode {
		case "", "chat", "completion", "responses":
		default:
			continue
		}
		lower := strings.ToLower(strings.TrimSpace(key))
		name := lower
		rank := 0
		if i := strings.LastIndex(lower, "/"); i >= 0 {
			name = lower[i+1:]
			rank = 5
			if r, ok := preferredProviders[strings.ToLower(e.Provider)]; ok {
				rank = r
			}
		}
		if name == "" || name == "sample_spec" {
			continue
		}
		p := Price{Input: *e.Input * 1e6, Output: *e.Output * 1e6}
		if e.CacheRead != nil {
			p.CachedInput = *e.CacheRead * 1e6
		}
		if e.CacheWrite != nil {
			p.CacheWrite = *e.CacheWrite * 1e6
		}
		if cur, ok := best[name]; !ok || rank < cur.rank {
			best[name] = ranked{price: p, rank: rank}
		}
	}
	out := make(map[string]Price, len(best))
	for name, r := range best {
		out[name] = r.price
	}
	return out, nil
}

// candidateNames returns lookup keys for a model name: as is and without a provider prefix.
func candidateNames(model string) []string {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return nil
	}
	out := []string{m}
	if i := strings.LastIndex(m, "/"); i >= 0 && i < len(m)-1 {
		out = append(out, m[i+1:])
	}
	return out
}
