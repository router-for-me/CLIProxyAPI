package helps

import (
	"sync"
	"sync/atomic"
	"time"
)

const cacheShardCount = 16

type shardedStringMap[V any] struct {
	cleanupCursor atomic.Uint32
	shards        [cacheShardCount]shardedStringMapShard[V]
}

type shardedStringMapShard[V any] struct {
	mu      sync.RWMutex
	entries map[string]V
}

func newShardedStringMap[V any]() *shardedStringMap[V] {
	store := &shardedStringMap[V]{}
	for i := range store.shards {
		store.shards[i].entries = make(map[string]V)
	}
	return store
}

func (m *shardedStringMap[V]) shardForKey(key string) *shardedStringMapShard[V] {
	return &m.shards[stringShardIndex(key)]
}

func (m *shardedStringMap[V]) load(key string) (V, bool) {
	shard := m.shardForKey(key)
	shard.mu.RLock()
	defer shard.mu.RUnlock()

	value, ok := shard.entries[key]
	return value, ok
}

func (m *shardedStringMap[V]) store(key string, value V) {
	shard := m.shardForKey(key)
	shard.mu.Lock()
	shard.entries[key] = value
	shard.mu.Unlock()
}

func (m *shardedStringMap[V]) cleanupNextShard(now time.Time, expired func(V, time.Time) bool) {
	shardIndex := int(m.cleanupCursor.Add(1)-1) % cacheShardCount
	shard := &m.shards[shardIndex]
	shard.mu.Lock()
	for key, value := range shard.entries {
		if expired(value, now) {
			delete(shard.entries, key)
		}
	}
	shard.mu.Unlock()
}

func (m *shardedStringMap[V]) clear() {
	for i := range m.shards {
		shard := &m.shards[i]
		shard.mu.Lock()
		clear(shard.entries)
		shard.mu.Unlock()
	}
}

func stringShardIndex(key string) int {
	const (
		fnvOffset64 = 14695981039346656037
		fnvPrime64  = 1099511628211
	)

	hash := uint64(fnvOffset64)
	for i := 0; i < len(key); i++ {
		hash ^= uint64(key[i])
		hash *= fnvPrime64
	}
	return int(hash % cacheShardCount)
}
