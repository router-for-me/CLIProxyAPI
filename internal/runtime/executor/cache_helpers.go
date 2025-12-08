package executor

import (
	"sync"
	"time"
)

type codexCache struct {
	ID     string
	Expire time.Time
}

var (
	codexCacheMap   = map[string]codexCache{}
	codexCacheMutex sync.RWMutex
)

// getCodexCache safely retrieves a cache entry
func getCodexCache(key string) (codexCache, bool) {
	codexCacheMutex.RLock()
	defer codexCacheMutex.RUnlock()
	cache, ok := codexCacheMap[key]
	return cache, ok
}

// setCodexCache safely sets a cache entry
func setCodexCache(key string, cache codexCache) {
	codexCacheMutex.Lock()
	defer codexCacheMutex.Unlock()
	codexCacheMap[key] = cache
}

// deleteCodexCache safely deletes a cache entry
func deleteCodexCache(key string) {
	codexCacheMutex.Lock()
	defer codexCacheMutex.Unlock()
	delete(codexCacheMap, key)
}
