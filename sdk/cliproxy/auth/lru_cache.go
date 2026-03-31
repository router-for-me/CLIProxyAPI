package auth

import "sync"

const defaultStickyLRUSize = 1024

// lruCache is a thread-safe LRU cache mapping string keys to string values.
type lruCache struct {
	mu       sync.Mutex
	capacity int
	items    map[string]*lruEntry
	head     *lruEntry // most recently used
	tail     *lruEntry // least recently used
}

type lruEntry struct {
	key   string
	value string
	prev  *lruEntry
	next  *lruEntry
}

func newLRUCache(capacity int) *lruCache {
	if capacity <= 0 {
		capacity = defaultStickyLRUSize
	}
	return &lruCache{
		capacity: capacity,
		items:    make(map[string]*lruEntry, capacity),
	}
}

// Get returns the value for key and whether it was found. Accessed entries are moved to front.
func (c *lruCache) Get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if !ok {
		return "", false
	}
	c.moveToFront(entry)
	return entry.value, true
}

// Put inserts or updates a key-value pair. If at capacity, the least recently used entry is evicted.
func (c *lruCache) Put(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.items[key]; ok {
		entry.value = value
		c.moveToFront(entry)
		return
	}
	entry := &lruEntry{key: key, value: value}
	c.items[key] = entry
	c.pushFront(entry)
	if len(c.items) > c.capacity {
		c.evictTail()
	}
}

// Remove deletes the entry for the given key.
func (c *lruCache) Remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if !ok {
		return
	}
	c.unlink(entry)
	delete(c.items, key)
}

// Len returns the number of entries in the cache.
func (c *lruCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

func (c *lruCache) moveToFront(entry *lruEntry) {
	if c.head == entry {
		return
	}
	c.unlink(entry)
	c.pushFront(entry)
}

func (c *lruCache) pushFront(entry *lruEntry) {
	entry.prev = nil
	entry.next = c.head
	if c.head != nil {
		c.head.prev = entry
	}
	c.head = entry
	if c.tail == nil {
		c.tail = entry
	}
}

func (c *lruCache) unlink(entry *lruEntry) {
	if entry.prev != nil {
		entry.prev.next = entry.next
	} else {
		c.head = entry.next
	}
	if entry.next != nil {
		entry.next.prev = entry.prev
	} else {
		c.tail = entry.prev
	}
	entry.prev = nil
	entry.next = nil
}

func (c *lruCache) evictTail() {
	if c.tail == nil {
		return
	}
	entry := c.tail
	c.unlink(entry)
	delete(c.items, entry.key)
}
