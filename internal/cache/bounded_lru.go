package cache

import (
	"container/list"
	"sync"
)

type boundedLRUEntry[K comparable, V any] struct {
	key   K
	value V
}

// BoundedLRU stores at most capacity values and evicts the least recently used
// value when a new key crosses the bound. The optional eviction callback runs
// after the cache lock is released.
type BoundedLRU[K comparable, V any] struct {
	mu       sync.Mutex
	capacity int
	entries  map[K]*list.Element
	order    *list.List
	onEvict  func(K, V)
}

func NewBoundedLRU[K comparable, V any](capacity int, onEvict func(K, V)) *BoundedLRU[K, V] {
	if capacity < 1 {
		capacity = 1
	}
	return &BoundedLRU[K, V]{
		capacity: capacity,
		entries:  make(map[K]*list.Element, capacity),
		order:    list.New(),
		onEvict:  onEvict,
	}
}

// GetOrAdd returns the cached value or creates and stores one while holding the
// cache lock. The create function must not call back into this cache.
func (cache *BoundedLRU[K, V]) GetOrAdd(key K, create func() V) V {
	cache.mu.Lock()
	if element, ok := cache.entries[key]; ok {
		cache.order.MoveToFront(element)
		value := element.Value.(boundedLRUEntry[K, V]).value
		cache.mu.Unlock()
		return value
	}

	value := create()
	element := cache.order.PushFront(boundedLRUEntry[K, V]{key: key, value: value})
	cache.entries[key] = element

	var evicted boundedLRUEntry[K, V]
	didEvict := false
	if cache.order.Len() > cache.capacity {
		oldest := cache.order.Back()
		evicted = oldest.Value.(boundedLRUEntry[K, V])
		delete(cache.entries, evicted.key)
		cache.order.Remove(oldest)
		didEvict = true
	}
	cache.mu.Unlock()

	if didEvict && cache.onEvict != nil {
		cache.onEvict(evicted.key, evicted.value)
	}
	return value
}

func (cache *BoundedLRU[K, V]) Get(key K) (V, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if element, ok := cache.entries[key]; ok {
		cache.order.MoveToFront(element)
		return element.Value.(boundedLRUEntry[K, V]).value, true
	}
	var zero V
	return zero, false
}

func (cache *BoundedLRU[K, V]) Len() int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return len(cache.entries)
}
