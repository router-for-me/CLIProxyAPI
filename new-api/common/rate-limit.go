package common

import (
	"container/list"
	"sync"
	"time"
)

// rateLimitRequest stores one accepted request in a key's sliding window.
type rateLimitRequest struct {
	next      *rateLimitRequest
	timestamp int64
}

// rateLimitQueue keeps accepted requests ordered from oldest to newest.
type rateLimitQueue struct {
	head   *rateLimitRequest
	tail   *rateLimitRequest
	length int
}

// append adds an accepted request without preallocating for the configured limit.
func (q *rateLimitQueue) append(timestamp int64) {
	request := &rateLimitRequest{timestamp: timestamp}
	if q.tail == nil {
		q.head = request
		q.tail = request
	} else {
		q.tail.next = request
		q.tail = request
	}
	q.length++
}

// removeExpired releases every expired request at the front of the queue.
func (q *rateLimitQueue) removeExpired(now int64, duration int64) {
	if q.head == nil || now-q.head.timestamp < duration {
		return
	}

	// Requests are time ordered, so an expired tail means the whole queue expired.
	if now-q.tail.timestamp >= duration {
		q.clear()
		return
	}

	for now-q.head.timestamp >= duration {
		expired := q.head
		q.head = expired.next
		expired.next = nil
		q.length--
	}
}

// clear releases the whole request chain when its key expires.
func (q *rateLimitQueue) clear() {
	q.head = nil
	q.tail = nil
	q.length = 0
}

// rateLimitEntry is both a rate-limit bucket and a node in the key-level LRU.
type rateLimitEntry struct {
	lastActive time.Time
	element    *list.Element
	requests   rateLimitQueue
	key        string
}

// InMemoryRateLimiter implements a sliding-window limiter with idle-key eviction.
// Reservations are tracked apart from the LRU so an in-flight request survives
// its key being evicted while it is still running.
type InMemoryRateLimiter struct {
	store              map[string]*rateLimitEntry
	lru                *list.List
	reservations       map[string]int
	mutex              sync.Mutex
	expirationDuration time.Duration
}

// Init initializes the limiter once. Repeated calls leave the first configuration unchanged.
func (l *InMemoryRateLimiter) Init(expirationDuration time.Duration) {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	if l.store != nil {
		return
	}

	l.store = make(map[string]*rateLimitEntry)
	l.lru = list.New()
	l.reservations = make(map[string]int)
	l.expirationDuration = expirationDuration
	if expirationDuration > 0 {
		go l.clearExpiredItems(time.NewTicker(expirationDuration).C)
	}
}

// clearExpiredItems periodically removes expired entries from the LRU tail.
func (l *InMemoryRateLimiter) clearExpiredItems(ticks <-chan time.Time) {
	for now := range ticks {
		l.deleteExpiredEntries(now)
	}
}

// deleteExpiredEntries walks only the oldest LRU entries and stops at the first active key.
func (l *InMemoryRateLimiter) deleteExpiredEntries(now time.Time) {
	if l.expirationDuration <= 0 {
		return
	}

	l.mutex.Lock()
	defer l.mutex.Unlock()

	for {
		oldest := l.lru.Back()
		if oldest == nil {
			return
		}

		entry := oldest.Value.(*rateLimitEntry)
		if now.Sub(entry.lastActive) < l.expirationDuration {
			return
		}

		delete(l.store, entry.key)
		l.lru.Remove(oldest)
		entry.element = nil
		entry.requests.clear()
	}
}

// touchEntry returns the key's bucket, creating it on demand, and marks it as
// the most recently active key. The caller must hold the mutex.
func (l *InMemoryRateLimiter) touchEntry(key string, now time.Time) *rateLimitEntry {
	entry, ok := l.store[key]
	if !ok {
		entry = &rateLimitEntry{
			key:        key,
			lastActive: now,
		}
		entry.element = l.lru.PushFront(entry)
		l.store[key] = entry
		return entry
	}
	entry.lastActive = now
	l.lru.MoveToFront(entry.element)
	return entry
}

// Request parameter duration's unit is seconds
func (l *InMemoryRateLimiter) Request(key string, maxRequestNum int, duration int64) bool {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	now := time.Now()
	entry := l.touchEntry(key, now)
	entry.requests.removeExpired(now.Unix(), duration)

	allowed := entry.requests.length < maxRequestNum
	if allowed {
		entry.requests.append(now.Unix())
	}

	return allowed
}

// RateLimitReservation counts an active request towards admission until its
// outcome is known. Failed requests release the slot without consuming quota.
type RateLimitReservation struct {
	limiter *InMemoryRateLimiter
	key     string
	once    sync.Once
}

// Reserve admits a request when the key's accepted requests plus its in-flight
// reservations stay below maxRequests. It returns nil when the key is saturated.
func (l *InMemoryRateLimiter) Reserve(key string, maxRequests int, duration int64) *RateLimitReservation {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	now := time.Now()
	entry := l.touchEntry(key, now)
	entry.requests.removeExpired(now.Unix(), duration)
	if entry.requests.length+l.reservations[key] >= maxRequests {
		return nil
	}
	l.reservations[key]++
	return &RateLimitReservation{limiter: l, key: key}
}

// Complete releases the reservation. A successful outcome records the request
// in the key's sliding window; a failed one only frees the slot.
func (r *RateLimitReservation) Complete(success bool) {
	if r == nil {
		return
	}
	r.once.Do(func() {
		l := r.limiter
		l.mutex.Lock()
		defer l.mutex.Unlock()
		l.reservations[r.key]--
		if l.reservations[r.key] == 0 {
			delete(l.reservations, r.key)
		}
		if success {
			now := time.Now()
			l.touchEntry(r.key, now).requests.append(now.Unix())
		}
	})
}
