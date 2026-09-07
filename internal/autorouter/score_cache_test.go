package autorouter

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// TestScoreCacheHitMiss pins the basic contract: a miss computes, a put makes
// the next get a hit, and the hit returns the identical result.
func TestScoreCacheHitMiss(t *testing.T) {
	c := NewScoreCache(8)
	body := []byte(`{"messages":[{"role":"user","content":"hi there"}]}`)
	key := c.Key(body, "openai", "router-1", "sha256:abc")
	if _, ok := c.Get(key); ok {
		t.Fatal("expected miss on empty cache")
	}
	res := ScoreWithProfileCompiled(body, "openai", nil)
	c.Put(key, res)
	got, ok := c.Get(key)
	if !ok {
		t.Fatal("expected hit after put")
	}
	if got.Score.Total != res.Score.Total || got.EffectiveTier != res.EffectiveTier {
		t.Fatalf("cached result diverged: %+v vs %+v", got, res)
	}
}

// TestScoreCacheKeySeparation pins invalidation-by-hash: the same body under a
// different profile hash (or router or format) must not collide.
func TestScoreCacheKeySeparation(t *testing.T) {
	c := NewScoreCache(8)
	body := []byte(`{"messages":[{"role":"user","content":"hi there"}]}`)
	k1 := c.Key(body, "openai", "router-1", "sha256:abc")
	k2 := c.Key(body, "openai", "router-1", "sha256:def")
	k3 := c.Key(body, "openai", "router-2", "sha256:abc")
	k4 := c.Key(body, "claude", "router-1", "sha256:abc")
	keys := map[string]bool{k1: true, k2: true, k3: true, k4: true}
	if len(keys) != 4 {
		t.Fatalf("expected 4 distinct keys, got %d", len(keys))
	}
}

// TestScoreCacheEviction pins LRU/FIFO eviction: inserting capacity+1 entries
// evicts the oldest.
func TestScoreCacheEviction(t *testing.T) {
	const capacity = 8
	c := NewScoreCache(capacity)
	res := ScoreWithProfileCompiled([]byte(`{"messages":[{"content":"x"}]}`), "openai", nil)
	var firstKey string
	for i := 0; i < capacity+1; i++ {
		key := c.Key([]byte(fmt.Sprintf(`{"n":%d}`, i)), "openai", "r", "h")
		if i == 0 {
			firstKey = key
		}
		c.Put(key, res)
	}
	if _, ok := c.Get(firstKey); ok {
		t.Fatal("expected oldest entry to be evicted")
	}
	if c.len() != capacity {
		t.Fatalf("cache size = %d, want %d", c.len(), capacity)
	}
}

// TestScoreCacheConcurrent exercises mixed get/put under -race.
func TestScoreCacheConcurrent(t *testing.T) {
	c := NewScoreCache(64)
	res := ScoreWithProfileCompiled([]byte(`{"messages":[{"content":"x"}]}`), "openai", nil)
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				key := c.Key([]byte(fmt.Sprintf(`{"n":%d}`, i%100)), "openai", "r", "h")
				if _, ok := c.Get(key); !ok {
					c.Put(key, res)
				}
			}
		}(g)
	}
	wg.Wait()
}

// TestScoreCacheKeyNotReversible guards that keys do not embed raw bodies.
func TestScoreCacheKeyNotReversible(t *testing.T) {
	c := NewScoreCache(8)
	secret := "super-secret-prompt-text-about-payroll"
	key := c.Key([]byte(`{"messages":[{"content":"`+secret+`"}]}`), "openai", "r", "h")
	if strings.Contains(key, secret) {
		t.Fatal("cache key embeds raw body content")
	}
	if len(key) != 64 { // sha256 hex
		t.Fatalf("key length = %d, want 64 (sha256 hex)", len(key))
	}
}
