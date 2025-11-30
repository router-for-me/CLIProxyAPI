 package executor
 
 import (
 	"testing"
 	"time"
 
 	"github.com/tidwall/gjson"
 )
 
 func TestGeminiReasoningCache_CacheAndInject(t *testing.T) {
 	cache := newGeminiReasoningCache()
 
 	// Cache reasoning from a streaming delta
 	delta := `{"choices":[{"delta":{"tool_calls":[{"id":"call_123"}],"reasoning_opaque":"opaque_data","reasoning_text":"thinking..."}}]}`
 	cache.CacheReasoning([]byte(delta))
 
 	// Verify it was cached
 	if cache.cache["call_123"] == nil {
 		t.Fatal("reasoning was not cached")
 	}
 	if cache.cache["call_123"].Opaque != "opaque_data" {
 		t.Errorf("Opaque = %q, want %q", cache.cache["call_123"].Opaque, "opaque_data")
 	}
 	if cache.cache["call_123"].Text != "thinking..." {
 		t.Errorf("Text = %q, want %q", cache.cache["call_123"].Text, "thinking...")
 	}
 
 	// Inject into a request body
 	body := `{"messages":[{"role":"assistant","tool_calls":[{"id":"call_123"}]}]}`
 	result := cache.InjectReasoning([]byte(body))
 
 	// Verify injection
 	if !gjson.GetBytes(result, "messages.0.reasoning_opaque").Exists() {
 		t.Error("reasoning_opaque was not injected")
 	}
 	if gjson.GetBytes(result, "messages.0.reasoning_opaque").String() != "opaque_data" {
 		t.Errorf("injected reasoning_opaque = %q, want %q", gjson.GetBytes(result, "messages.0.reasoning_opaque").String(), "opaque_data")
 	}
 }
 
 func TestGeminiReasoningCache_TextAppends(t *testing.T) {
 	cache := newGeminiReasoningCache()
 
 	// Simulate streaming chunks
 	chunk1 := `{"choices":[{"delta":{"tool_calls":[{"id":"call_456"}],"reasoning_text":"Hello "}}]}`
 	chunk2 := `{"choices":[{"delta":{"tool_calls":[{"id":"call_456"}],"reasoning_text":"World"}}]}`
 
 	cache.CacheReasoning([]byte(chunk1))
 	cache.CacheReasoning([]byte(chunk2))
 
 	if cache.cache["call_456"].Text != "Hello World" {
 		t.Errorf("Text = %q, want %q", cache.cache["call_456"].Text, "Hello World")
 	}
 }
 
 func TestGeminiReasoningCache_NoInjectWhenAlreadyPresent(t *testing.T) {
 	cache := newGeminiReasoningCache()
 
 	delta := `{"choices":[{"delta":{"tool_calls":[{"id":"call_789"}],"reasoning_opaque":"cached"}}]}`
 	cache.CacheReasoning([]byte(delta))
 
 	// Body already has reasoning_opaque
 	body := `{"messages":[{"role":"assistant","tool_calls":[{"id":"call_789"}],"reasoning_opaque":"existing"}]}`
 	result := cache.InjectReasoning([]byte(body))
 
 	// Should keep existing value
 	if gjson.GetBytes(result, "messages.0.reasoning_opaque").String() != "existing" {
 		t.Error("existing reasoning_opaque was overwritten")
 	}
 }
 
 func TestGeminiReasoningCache_TTLExpiry(t *testing.T) {
 	cache := newGeminiReasoningCache()
 
 	// Manually insert expired entry
 	cache.cache["expired_call"] = &geminiReasoning{
 		Opaque:    "old_data",
 		createdAt: time.Now().Add(-31 * time.Minute), // expired
 	}
 
 	body := `{"messages":[{"role":"assistant","tool_calls":[{"id":"expired_call"}]}]}`
 	result := cache.InjectReasoning([]byte(body))
 
 	// Should not inject expired reasoning
 	if gjson.GetBytes(result, "messages.0.reasoning_opaque").Exists() {
 		t.Error("expired reasoning was injected")
 	}
 }
 
 func TestEvictCopilotGeminiReasoningCache(t *testing.T) {
 	// Setup shared cache
 	testAuthID := "test-auth-evict"
 	cache := getSharedGeminiReasoningCache(testAuthID)
 	cache.cache["test"] = &geminiReasoning{Opaque: "data", createdAt: time.Now()}
 
 	// Evict
 	EvictCopilotGeminiReasoningCache(testAuthID)
 
 	// Get again - should be fresh
 	newCache := getSharedGeminiReasoningCache(testAuthID)
 	if len(newCache.cache) != 0 {
 		t.Error("cache was not evicted")
 	}
 }
 
 func TestGetSharedGeminiReasoningCache_EmptyAuthID(t *testing.T) {
 	cache1 := getSharedGeminiReasoningCache("")
 	cache2 := getSharedGeminiReasoningCache("")
 
 	// Empty authID should return new cache each time
 	if cache1 == cache2 {
 		t.Error("empty authID should return new cache instance each time")
 	}
 }
 
 func TestGetSharedGeminiReasoningCache_SameAuthID(t *testing.T) {
 	testAuthID := "test-auth-same"
 	cache1 := getSharedGeminiReasoningCache(testAuthID)
 	cache2 := getSharedGeminiReasoningCache(testAuthID)
 
 	if cache1 != cache2 {
 		t.Error("same authID should return same cache instance")
 	}
 
 	// Cleanup
 	EvictCopilotGeminiReasoningCache(testAuthID)
 }
