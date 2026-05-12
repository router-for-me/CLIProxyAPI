package executor

import (
	"context"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestVertexCacheCreateBodyUsesStablePromptFieldsAndPrefix(t *testing.T) {
	body := []byte(`{
		"model":"gemini-3-flash-preview",
		"system_instruction":{"parts":[{"text":"system"}]},
		"tools":[{"functionDeclarations":[{"name":"Read"}]}],
		"toolConfig":{"functionCallingConfig":{"mode":"AUTO"}},
		"contents":[
			{"role":"user","parts":[{"text":"stable"}]},
			{"role":"model","parts":[{"text":"old"}]},
			{"role":"user","parts":[{"text":"dynamic"}]}
		]
	}`)
	original := []byte(`{
		"messages":[
			{"role":"user","content":[{"type":"text","text":"stable","cache_control":{"type":"ephemeral"}}]},
			{"role":"assistant","content":[{"type":"text","text":"old"}]},
			{"role":"user","content":[{"type":"text","text":"dynamic"}]}
		]
	}`)

	plan, ok := vertexCacheCreateBody("test-project", "global", "gemini-3-flash-preview", body, original, sdktranslator.FromString("claude"))
	if !ok {
		t.Fatal("vertexCacheCreateBody returned ok=false")
	}
	if got := gjson.GetBytes(plan.createBody, "model").String(); got != "projects/test-project/locations/global/publishers/google/models/gemini-3-flash-preview" {
		t.Fatalf("model = %q", got)
	}
	if !gjson.GetBytes(plan.createBody, "systemInstruction").Exists() {
		t.Fatalf("systemInstruction missing: %s", string(plan.createBody))
	}
	if !gjson.GetBytes(plan.createBody, "tools").Exists() {
		t.Fatalf("tools missing: %s", string(plan.createBody))
	}
	if got := len(gjson.GetBytes(plan.createBody, "contents").Array()); got != 1 {
		t.Fatalf("cached contents = %d, want 1; body=%s", got, string(plan.createBody))
	}
	if got := len(gjson.GetBytes(plan.requestBody, "contents").Array()); got != 2 {
		t.Fatalf("request contents = %d, want 2; body=%s", got, string(plan.requestBody))
	}
	for _, path := range []string{"system_instruction", "systemInstruction", "tools", "toolConfig"} {
		if gjson.GetBytes(plan.requestBody, path).Exists() {
			t.Fatalf("%s should be removed from cached request: %s", path, string(plan.requestBody))
		}
	}
}

func TestVertexCacheCreateBodyAcceptsCamelSystemInstruction(t *testing.T) {
	body := []byte(`{
		"systemInstruction":{"parts":[{"text":"system"}]},
		"contents":[{"role":"user","parts":[{"text":"dynamic"}]}]
	}`)

	plan, ok := vertexCacheCreateBody("test-project", "global", "gemini-3-flash-preview", body, nil, sdktranslator.FromString("openai"))
	if !ok {
		t.Fatal("vertexCacheCreateBody returned ok=false")
	}
	if !gjson.GetBytes(plan.createBody, "systemInstruction").Exists() {
		t.Fatalf("systemInstruction missing: %s", string(plan.createBody))
	}
	if gjson.GetBytes(plan.requestBody, "systemInstruction").Exists() {
		t.Fatalf("systemInstruction should be removed: %s", string(plan.requestBody))
	}
}

func TestVertexCacheCreateBodyFallsBackToStablePrefixForNonClaude(t *testing.T) {
	body := []byte(`{
		"contents":[
			{"role":"user","parts":[{"text":"stable"}]},
			{"role":"model","parts":[{"text":"old"}]},
			{"role":"user","parts":[{"text":"dynamic"}]}
		]
	}`)

	plan, ok := vertexCacheCreateBody("test-project", "global", "gemini-3-flash-preview", body, nil, sdktranslator.FromString("openai"))
	if !ok {
		t.Fatal("vertexCacheCreateBody returned ok=false")
	}
	if got := len(gjson.GetBytes(plan.createBody, "contents").Array()); got != 2 {
		t.Fatalf("cached contents = %d, want 2; body=%s", got, string(plan.createBody))
	}
	if got := len(gjson.GetBytes(plan.requestBody, "contents").Array()); got != 1 {
		t.Fatalf("request contents = %d, want 1; body=%s", got, string(plan.requestBody))
	}
	if got := gjson.GetBytes(plan.requestBody, "contents.0.parts.0.text").String(); got != "dynamic" {
		t.Fatalf("request first content text = %q, want dynamic; body=%s", got, string(plan.requestBody))
	}
}

func TestVertexCacheCreateBodyUsesClaudeAutoCacheControlPrefix(t *testing.T) {
	body := []byte(`{
		"contents":[
			{"role":"user","parts":[{"text":"stable"}]},
			{"role":"model","parts":[{"text":"old"}]},
			{"role":"user","parts":[{"text":"dynamic"}]}
		]
	}`)
	original := []byte(`{
		"messages":[
			{"role":"user","content":"stable"},
			{"role":"assistant","content":"old"},
			{"role":"user","content":"dynamic"}
		]
	}`)

	plan, ok := vertexCacheCreateBody("test-project", "global", "gemini-3-flash-preview", body, original, sdktranslator.FromString("claude"))
	if !ok {
		t.Fatal("vertexCacheCreateBody returned ok=false")
	}
	if got := len(gjson.GetBytes(plan.createBody, "contents").Array()); got != 1 {
		t.Fatalf("cached contents = %d, want 1; body=%s", got, string(plan.createBody))
	}
	if got := len(gjson.GetBytes(plan.requestBody, "contents").Array()); got != 2 {
		t.Fatalf("request contents = %d, want 2; body=%s", got, string(plan.requestBody))
	}
}

func TestMaybeApplyNativePromptCacheUsesTrimmedRequestBodyOnHit(t *testing.T) {
	body := []byte(`{
		"system_instruction":{"parts":[{"text":"system"}]},
		"tools":[{"functionDeclarations":[{"name":"Read"}]}],
		"toolConfig":{"functionCallingConfig":{"mode":"AUTO"}},
		"contents":[
			{"role":"user","parts":[{"text":"stable"}]},
			{"role":"model","parts":[{"text":"old"}]},
			{"role":"user","parts":[{"text":"dynamic"}]}
		]
	}`)
	original := []byte(`{
		"messages":[
			{"role":"user","content":[{"type":"text","text":"stable","cache_control":{"type":"ephemeral"}}]},
			{"role":"assistant","content":[{"type":"text","text":"old"}]},
			{"role":"user","content":[{"type":"text","text":"dynamic"}]}
		]
	}`)
	auth := &cliproxyauth.Auth{ID: "auth-1"}
	plan, ok := vertexCacheCreateBody("test-project", "global", "gemini-3-flash-preview", body, original, sdktranslator.FromString("claude"))
	if !ok {
		t.Fatal("vertexCacheCreateBody returned ok=false")
	}
	key := vertexPromptCacheKey(auth, "test-project", "global", "gemini-3-flash-preview", plan.createBody)
	vertexPromptCaches.Store(key, vertexPromptCacheEntry{
		name:      "projects/test-project/locations/global/cachedContents/cache-1",
		expiresAt: time.Now().Add(time.Minute),
	})
	t.Cleanup(func() { vertexPromptCaches.Delete(key) })

	out := NewGeminiVertexExecutor(nil).maybeApplyNativePromptCache(
		context.Background(),
		auth,
		body,
		original,
		sdktranslator.FromString("claude"),
		"test-project",
		"global",
		"gemini-3-flash-preview",
		"",
	)
	if got := gjson.GetBytes(out, "cachedContent").String(); got != "projects/test-project/locations/global/cachedContents/cache-1" {
		t.Fatalf("cachedContent = %q", got)
	}
	if got := len(gjson.GetBytes(out, "contents").Array()); got != 2 {
		t.Fatalf("request contents = %d, want 2; body=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "contents.0.parts.0.text").String(); got != "old" {
		t.Fatalf("request first content text = %q, want old; body=%s", got, string(out))
	}
	for _, path := range []string{"system_instruction", "systemInstruction", "tools", "toolConfig"} {
		if gjson.GetBytes(out, path).Exists() {
			t.Fatalf("%s should be removed from cached request: %s", path, string(out))
		}
	}
}

func TestApplyVertexCachedContentRemovesCachedPromptFields(t *testing.T) {
	body := []byte(`{
		"systemInstruction":{"parts":[{"text":"system"}]},
		"system_instruction":{"parts":[{"text":"legacy"}]},
		"tools":[{"functionDeclarations":[{"name":"Read"}]}],
		"toolConfig":{"functionCallingConfig":{"mode":"AUTO"}},
		"contents":[{"role":"user","parts":[{"text":"dynamic"}]}]
	}`)

	out := applyVertexCachedContent(body, "projects/123/locations/global/cachedContents/cache-1")
	if got := gjson.GetBytes(out, "cachedContent").String(); got != "projects/123/locations/global/cachedContents/cache-1" {
		t.Fatalf("cachedContent = %q", got)
	}
	for _, path := range []string{"system_instruction", "systemInstruction", "tools", "toolConfig"} {
		if gjson.GetBytes(out, path).Exists() {
			t.Fatalf("%s should be removed: %s", path, string(out))
		}
	}
	if !gjson.GetBytes(out, "contents").Exists() {
		t.Fatalf("contents should remain: %s", string(out))
	}
}

func TestLoadVertexPromptCacheNegativeEntryDoesNotReturnName(t *testing.T) {
	key := "test-negative"
	vertexPromptCaches.Store(key, vertexPromptCacheEntry{retryAt: time.Now().Add(time.Minute)})
	t.Cleanup(func() { vertexPromptCaches.Delete(key) })

	name, ok := loadVertexPromptCacheName(key, time.Now())
	if !ok {
		t.Fatal("expected negative cache entry to be found")
	}
	if name != "" {
		t.Fatalf("name = %q, want empty for negative cache entry", name)
	}
}

func TestVertexPromptCacheKeyIncludesAuthID(t *testing.T) {
	body := []byte(`{"ttl":"3600s","model":"m"}`)
	a := &cliproxyauth.Auth{ID: "a"}
	b := &cliproxyauth.Auth{ID: "b"}
	if vertexPromptCacheKey(a, "p", "global", "m", body) == vertexPromptCacheKey(b, "p", "global", "m", body) {
		t.Fatal("cache key should differ across auth IDs")
	}
}
