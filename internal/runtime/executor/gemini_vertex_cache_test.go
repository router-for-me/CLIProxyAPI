package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestVertexCacheCreateBodyUsesStablePromptFields(t *testing.T) {
	body := []byte(`{
		"model":"gemini-3-flash-preview",
		"system_instruction":{"parts":[{"text":"system"}]},
		"tools":[{"functionDeclarations":[{"name":"Read"}]}],
		"toolConfig":{"functionCallingConfig":{"mode":"AUTO"}},
		"contents":[{"role":"user","parts":[{"text":"dynamic"}]}]
	}`)

	createBody, ok := vertexCacheCreateBody("test-project", "global", "gemini-3-flash-preview", body)
	if !ok {
		t.Fatal("vertexCacheCreateBody returned ok=false")
	}
	if got := gjson.GetBytes(createBody, "model").String(); got != "projects/test-project/locations/global/publishers/google/models/gemini-3-flash-preview" {
		t.Fatalf("model = %q", got)
	}
	if !gjson.GetBytes(createBody, "systemInstruction").Exists() {
		t.Fatalf("systemInstruction missing: %s", string(createBody))
	}
	if !gjson.GetBytes(createBody, "tools").Exists() {
		t.Fatalf("tools missing: %s", string(createBody))
	}
	if gjson.GetBytes(createBody, "contents").Exists() {
		t.Fatalf("contents should not be cached: %s", string(createBody))
	}
}

func TestApplyVertexCachedContentRemovesCachedPromptFields(t *testing.T) {
	body := []byte(`{
		"system_instruction":{"parts":[{"text":"system"}]},
		"tools":[{"functionDeclarations":[{"name":"Read"}]}],
		"toolConfig":{"functionCallingConfig":{"mode":"AUTO"}},
		"contents":[{"role":"user","parts":[{"text":"dynamic"}]}]
	}`)

	out := applyVertexCachedContent(body, "projects/123/locations/global/cachedContents/cache-1")
	if got := gjson.GetBytes(out, "cachedContent").String(); got != "projects/123/locations/global/cachedContents/cache-1" {
		t.Fatalf("cachedContent = %q", got)
	}
	for _, path := range []string{"system_instruction", "tools", "toolConfig"} {
		if gjson.GetBytes(out, path).Exists() {
			t.Fatalf("%s should be removed: %s", path, string(out))
		}
	}
	if !gjson.GetBytes(out, "contents").Exists() {
		t.Fatalf("contents should remain: %s", string(out))
	}
}
