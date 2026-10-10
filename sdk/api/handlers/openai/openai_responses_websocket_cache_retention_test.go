package openai

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDefaultWebsocketToolCachesExpireIdleSessions(t *testing.T) {
	for name, defaults := range map[string]*websocketToolOutputCache{
		"output": defaultWebsocketToolOutputCache,
		"call":   defaultWebsocketToolCallCache,
	} {
		t.Run(name, func(t *testing.T) {
			cache := newWebsocketToolOutputCache(defaults.ttl, defaults.maxPerSession)
			item := json.RawMessage(`{"type":"function_call_output","call_id":"call-1","output":"synthetic"}`)
			cache.record("idle", "call-1", item)
			cache.record("active", "call-1", item)
			// Leave the session present: no connection-release deletion occurs.
			cache.mu.Lock()
			cache.sessions["idle"].lastSeen = time.Now().Add(-time.Hour - time.Minute)
			cache.mu.Unlock()
			// An ordinary access must reclaim idle state, even with a live connection.
			cache.record("heartbeat", "call-2", item)
			if _, ok := cache.get("idle", "call-1"); ok {
				t.Error("session idle for more than one hour still retains its tool item")
			}
			if got, ok := cache.get("active", "call-1"); !ok || string(got) != string(item) {
				t.Fatal("recent replay item was not preserved")
			}
			// Returned bytes remain independent of the cache's stored copy.
			got, _ := cache.get("active", "call-1")
			got[0] = 'x'
			if again, _ := cache.get("active", "call-1"); string(again) != string(item) {
				t.Fatal("caller mutation changed the retained item")
			}
		})
	}
}

func TestDefaultWebsocketToolCachesKeepRecentReplay(t *testing.T) {
	for name, defaults := range map[string]*websocketToolOutputCache{
		"output": defaultWebsocketToolOutputCache,
		"call":   defaultWebsocketToolCallCache,
	} {
		t.Run(name, func(t *testing.T) {
			cache := newWebsocketToolOutputCache(defaults.ttl, defaults.maxPerSession)
			item := json.RawMessage(`{"call_id":"call-1"}`)
			cache.record("active", "call-1", item)
			cache.mu.Lock()
			cache.sessions["active"].lastSeen = time.Now().Add(-59 * time.Minute)
			cache.mu.Unlock()
			if _, ok := cache.get("active", "call-1"); !ok {
				t.Fatal("replay within the one-hour idle window was lost")
			}
			cache.mu.Lock()
			// A successful get refreshes the session's existing idle timeout.
			cache.cleanupLocked(time.Now().Add(59 * time.Minute))
			_, present := cache.sessions["active"]
			cache.mu.Unlock()
			if !present {
				t.Fatal("active replay did not refresh the idle timeout")
			}
		})
	}
}

// This reproduces retention in the real cache implementation under continuous
// arrivals of sessions whose connections remain open after their last tool turn.
// Explicit timestamps replace a day of waiting; no timers or credentials are used.
func TestDefaultWebsocketToolCachesBoundIdleGrowth(t *testing.T) {
	for name, defaults := range map[string]*websocketToolOutputCache{
		"output": defaultWebsocketToolOutputCache,
		"call":   defaultWebsocketToolCallCache,
	} {
		t.Run(name, func(t *testing.T) {
			cache := newWebsocketToolOutputCache(defaults.ttl, defaults.maxPerSession)
			payload := strings.Repeat("s", 256*1024)
			var item json.RawMessage
			if name == "output" {
				item = json.RawMessage(fmt.Sprintf(`{"type":"function_call_output","call_id":"synthetic","output":%q}`, payload))
			} else {
				item = json.RawMessage(fmt.Sprintf(`{"type":"function_call","call_id":"synthetic","name":"synthetic","arguments":%q}`, payload))
			}
			const sessionsPerStep = 2
			const itemsPerSession = 4
			const steps = 48
			// At the exact TTL boundary the existing cleanup keeps that session.
			maxBytes := 3 * sessionsPerStep * itemsPerSession * len(item)
			anchor := time.Now()
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			retained := 0
			for step := 0; step < steps; step++ {
				now := anchor.Add(time.Duration(step) * 30 * time.Minute)
				for session := 0; session < sessionsPerStep; session++ {
					key := fmt.Sprintf("synthetic-%d-%d", step, session)
					for call := 0; call < itemsPerSession; call++ {
						cache.record(key, fmt.Sprintf("call-%d", call), item)
					}
					cache.mu.Lock()
					cache.sessions[key].lastSeen = now
					cache.mu.Unlock()
				}
				cache.mu.Lock()
				cache.cleanupLocked(now)
				retained = 0
				for _, session := range cache.sessions {
					for _, output := range session.outputs {
						retained += len(output)
					}
				}
				liveSessions := len(cache.sessions)
				cache.mu.Unlock()
				if step == 0 || (step+1)%16 == 0 {
					t.Logf("simulated_hours=%.1f retained_sessions=%d retained_payload_bytes=%d", float64(step)/2, liveSessions, retained)
				}
			}
			runtime.GC()
			runtime.ReadMemStats(&after)
			runtime.KeepAlive(cache)
			t.Logf("post_gc_heap_delta_bytes=%d", int64(after.HeapAlloc)-int64(before.HeapAlloc))
			// Assert exact retained payload bytes; Go allocator/GC measurements
			// are supporting evidence, not a portable RSS assertion.
			if retained > maxBytes {
				t.Errorf("retained %d payload bytes after idle-session arrivals; want <= %d", retained, maxBytes)
			}
		})
	}
}
