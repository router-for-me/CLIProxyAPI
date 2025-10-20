package executor

import (
	"sync"
	"time"
)

type codexCache struct {
	ID     string
	Expire time.Time
}

var codexCacheMap sync.Map

func loadOrCreateCache(key string, ttl time.Duration, factory func() string) codexCache {
	if value, ok := codexCacheMap.Load(key); ok {
		cache := value.(codexCache)
		if cache.Expire.After(time.Now()) {
			return cache
		}
	}

	cache := codexCache{
		ID:     factory(),
		Expire: time.Now().Add(ttl),
	}
	codexCacheMap.Store(key, cache)
	return cache
}
