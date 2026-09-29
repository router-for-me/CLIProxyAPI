package common

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInMemoryRateLimiterAllocatesTimestampsOnDemand(t *testing.T) {
	var limiter InMemoryRateLimiter
	limiter.Init(0)

	require.True(t, limiter.Request("client", 1_000_000, 60))

	entry := limiter.store["client"]
	require.NotNil(t, entry)
	assert.Equal(t, 1, entry.requests.length)
	assert.Same(t, entry.requests.head, entry.requests.tail)
	assert.Nil(t, entry.requests.head.next)

	limiter.deleteExpiredEntries(time.Now().Add(time.Hour))
	assert.Contains(t, limiter.store, "client")
}

func TestInMemoryRateLimiterEnforcesWindowBoundary(t *testing.T) {
	var queue rateLimitQueue
	queue.append(100)
	queue.append(100)

	queue.removeExpired(159, 60)
	assert.Equal(t, 2, queue.length)

	queue.removeExpired(160, 60)
	assert.Zero(t, queue.length)
	assert.Nil(t, queue.head)
	assert.Nil(t, queue.tail)
}

func TestInMemoryRateLimiterRemovesAllExpiredRequestsPerCall(t *testing.T) {
	var queue rateLimitQueue
	for range 5 {
		queue.append(100)
	}
	queue.append(110)

	queue.removeExpired(110, 10)
	assert.Equal(t, 1, queue.length)
	require.NotNil(t, queue.head)
	assert.EqualValues(t, 110, queue.head.timestamp)
	assert.Same(t, queue.head, queue.tail)
}

func TestInMemoryRateLimiterKeepsNonExpiredRequests(t *testing.T) {
	var queue rateLimitQueue
	for range 3 {
		queue.append(100)
	}

	queue.removeExpired(109, 10)
	assert.Equal(t, 3, queue.length)
	assert.NotNil(t, queue.head)
	assert.NotNil(t, queue.tail)
}

func TestInMemoryRateLimiterEvictsOnlyExpiredLRUTail(t *testing.T) {
	var limiter InMemoryRateLimiter
	limiter.Init(0)
	limiter.expirationDuration = 10 * time.Second

	require.True(t, limiter.Request("active", 1, 100))
	require.True(t, limiter.Request("idle", 1, 100))
	assert.False(t, limiter.Request("active", 1, 100))

	assert.Equal(t, "active", limiter.lru.Front().Value.(*rateLimitEntry).key)
	assert.Equal(t, "idle", limiter.lru.Back().Value.(*rateLimitEntry).key)

	idleEntry := limiter.store["idle"]
	now := time.Now()
	idleEntry.lastActive = now.Add(-10 * time.Second)
	limiter.store["active"].lastActive = now
	limiter.deleteExpiredEntries(now)

	assert.NotContains(t, limiter.store, "idle")
	assert.Contains(t, limiter.store, "active")
	assert.Equal(t, 1, limiter.lru.Len())
	assert.Nil(t, idleEntry.element)
	assert.Nil(t, idleEntry.requests.head)
	assert.Nil(t, idleEntry.requests.tail)
	assert.Zero(t, idleEntry.requests.length)
}

func TestInMemoryRateLimiterCleanupTicksEvictExpiredKeys(t *testing.T) {
	var limiter InMemoryRateLimiter
	limiter.Init(0)
	limiter.expirationDuration = 10 * time.Second
	require.True(t, limiter.Request("idle", 1, 100))

	now := time.Now()
	limiter.store["idle"].lastActive = now.Add(-10 * time.Second)
	ticks := make(chan time.Time, 1)
	done := make(chan struct{})
	go func() {
		limiter.clearExpiredItems(ticks)
		close(done)
	}()

	ticks <- now
	close(ticks)
	<-done

	assert.Empty(t, limiter.store)
	assert.Zero(t, limiter.lru.Len())
}

func TestInMemoryRateLimiterInitKeepsFirstConfiguration(t *testing.T) {
	var limiter InMemoryRateLimiter
	limiter.Init(time.Hour)
	limiter.Init(0)

	assert.Equal(t, time.Hour, limiter.expirationDuration)
	require.True(t, limiter.Request("client", 1, 60))
	limiter.deleteExpiredEntries(time.Now().Add(time.Hour))
	assert.NotContains(t, limiter.store, "client")
}

func TestInMemoryRateLimiterConcurrentInitializationAndRequests(t *testing.T) {
	var limiter InMemoryRateLimiter
	var allowed atomic.Int64
	var waitGroup sync.WaitGroup

	for range 100 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			limiter.Init(0)
			if limiter.Request("client", 10, 60) {
				allowed.Add(1)
			}
		}()
	}

	waitGroup.Wait()
	assert.EqualValues(t, 10, allowed.Load())
	assert.Len(t, limiter.store, 1)
	assert.Equal(t, 10, limiter.store["client"].requests.length)
}
