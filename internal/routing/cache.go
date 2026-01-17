package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// RouteCache persists successful model routes to disk for faster subsequent lookups.
type RouteCache struct {
	mu       sync.RWMutex
	filePath string
	entries  map[string]*CacheEntry
	dirty    bool
}

// CacheEntry represents a cached successful route.
type CacheEntry struct {
	// ActualModel is the model name that was successfully used.
	ActualModel string `json:"actual_model"`
	// LastUsed is the timestamp when this route was last successfully used.
	LastUsed time.Time `json:"last_used"`
	// SuccessCount tracks how many times this route succeeded.
	SuccessCount int `json:"success_count"`
}

// cacheFileData is the JSON structure persisted to disk.
type cacheFileData struct {
	Version int                    `json:"version"`
	Routes  map[string]*CacheEntry `json:"routes"`
}

const cacheFileVersion = 1

// NewRouteCache creates a new RouteCache with the given auth directory and cache file name.
func NewRouteCache(authDir, cacheFile string) *RouteCache {
	c := &RouteCache{
		entries: make(map[string]*CacheEntry),
	}
	c.UpdatePath(authDir, cacheFile)
	return c
}

// UpdatePath updates the cache file path and reloads the cache.
func (c *RouteCache) UpdatePath(authDir, cacheFile string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Expand ~ in authDir
	if strings.HasPrefix(authDir, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			authDir = filepath.Join(home, authDir[1:])
		}
	}

	newPath := filepath.Join(authDir, cacheFile)
	if newPath == c.filePath {
		return
	}

	// Save current cache before switching
	if c.dirty && c.filePath != "" {
		c.saveToFileLocked()
	}

	c.filePath = newPath
	c.entries = make(map[string]*CacheEntry)
	c.dirty = false

	// Load from new path
	c.loadFromFileLocked()
}

// Get returns the cached actual model for the given virtual model name.
// Returns empty string if not cached.
func (c *RouteCache) Get(virtualModel string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	key := strings.ToLower(strings.TrimSpace(virtualModel))
	if entry, ok := c.entries[key]; ok {
		return entry.ActualModel
	}
	return ""
}

// Set records a successful route from virtual model to actual model.
func (c *RouteCache) Set(virtualModel, actualModel string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := strings.ToLower(strings.TrimSpace(virtualModel))
	actualModel = strings.TrimSpace(actualModel)

	if key == "" || actualModel == "" {
		return
	}

	entry, exists := c.entries[key]
	if exists {
		entry.ActualModel = actualModel
		entry.LastUsed = time.Now()
		entry.SuccessCount++
	} else {
		c.entries[key] = &CacheEntry{
			ActualModel:  actualModel,
			LastUsed:     time.Now(),
			SuccessCount: 1,
		}
	}
	c.dirty = true

	// Persist to disk asynchronously
	go c.Save()
}

// Save persists the cache to disk.
func (c *RouteCache) Save() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.saveToFileLocked()
}

// saveToFileLocked saves the cache to file. Must be called with c.mu held.
func (c *RouteCache) saveToFileLocked() {
	if c.filePath == "" {
		return
	}

	// Ensure directory exists
	dir := filepath.Dir(c.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Warnf("model routing cache: failed to create directory %s: %v", dir, err)
		return
	}

	data := cacheFileData{
		Version: cacheFileVersion,
		Routes:  c.entries,
	}

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		log.Warnf("model routing cache: failed to marshal cache: %v", err)
		return
	}

	// Write to temp file first, then rename for atomicity
	tmpFile := c.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, jsonData, 0644); err != nil {
		log.Warnf("model routing cache: failed to write temp file: %v", err)
		return
	}

	if err := os.Rename(tmpFile, c.filePath); err != nil {
		log.Warnf("model routing cache: failed to rename temp file: %v", err)
		_ = os.Remove(tmpFile)
		return
	}

	c.dirty = false
	log.Debugf("model routing cache: saved %d entries to %s", len(c.entries), c.filePath)
}

// loadFromFileLocked loads the cache from file. Must be called with c.mu held.
func (c *RouteCache) loadFromFileLocked() {
	if c.filePath == "" {
		return
	}

	jsonData, err := os.ReadFile(c.filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Warnf("model routing cache: failed to read cache file: %v", err)
		}
		return
	}

	var data cacheFileData
	if err := json.Unmarshal(jsonData, &data); err != nil {
		log.Warnf("model routing cache: failed to parse cache file: %v", err)
		return
	}

	// Version check for future compatibility
	if data.Version != cacheFileVersion {
		log.Warnf("model routing cache: unsupported version %d, expected %d", data.Version, cacheFileVersion)
		return
	}

	if data.Routes != nil {
		c.entries = data.Routes
	}

	log.Debugf("model routing cache: loaded %d entries from %s", len(c.entries), c.filePath)
}

// Clear removes all cached entries.
func (c *RouteCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries = make(map[string]*CacheEntry)
	c.dirty = true

	// Remove file
	if c.filePath != "" {
		_ = os.Remove(c.filePath)
	}
}

// Entries returns a copy of all cache entries for inspection.
func (c *RouteCache) Entries() map[string]*CacheEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[string]*CacheEntry, len(c.entries))
	for k, v := range c.entries {
		entryCopy := *v
		result[k] = &entryCopy
	}
	return result
}
