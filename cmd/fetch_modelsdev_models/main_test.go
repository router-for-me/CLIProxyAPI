package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestRewriteModelsJSONPreservesOtherKeys(t *testing.T) {
	raw := []byte("{\n  \"claude\": [{\"id\": \"c\"}],\n  \"opencode\": [{\"id\": \"stale\"}],\n  \"zai\": []\n}\n")
	oc := []*registry.ModelInfo{{ID: "fresh", Object: "model", OwnedBy: "opencode", Type: "opencode"}}
	zai := []*registry.ModelInfo{{ID: "glm-5.3", Object: "model", OwnedBy: "zai", Type: "zai"}}
	updated, err := rewriteModelsJSON(raw, oc, zai)
	if err != nil {
		t.Fatalf("rewriteModelsJSON: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(updated, &doc); err != nil {
		t.Fatalf("updated JSON invalid: %v\n%s", err, updated)
	}
	if string(doc["claude"]) != "[{\"id\": \"c\"}]" && !strings.Contains(string(doc["claude"]), `"c"`) {
		t.Fatalf("claude key not preserved: %s", doc["claude"])
	}
	if !strings.Contains(string(doc["opencode"]), `"fresh"`) {
		t.Fatalf("opencode not replaced: %s", doc["opencode"])
	}
	if !strings.Contains(string(doc["zai"]), `"glm-5.3"`) {
		t.Fatalf("zai not replaced: %s", doc["zai"])
	}
	if !strings.HasSuffix(string(updated), "}\n") {
		t.Fatal("output must end with } + newline")
	}
}

func TestRewriteModelsJSONKeepsKeyOrderAndBytes(t *testing.T) {
	raw := []byte("{\n  \"zebra\": [\n    {\n      \"id\": \"z\"\n    }\n  ],\n  \"opencode\": [],\n  \"apple\": 1\n}\n")
	oc := []*registry.ModelInfo{{ID: "fresh", Object: "model", OwnedBy: "opencode", Type: "opencode"}}
	zai := []*registry.ModelInfo{{ID: "g", Object: "model", OwnedBy: "zai", Type: "zai"}}
	updated, err := rewriteModelsJSON(raw, oc, zai)
	if err != nil {
		t.Fatalf("rewriteModelsJSON: %v", err)
	}
	out := string(updated)
	// Original key order (zebra, opencode, apple) must survive, not sorted.
	zebra := strings.Index(out, `"zebra"`)
	opencode := strings.Index(out, `"opencode"`)
	apple := strings.Index(out, `"apple"`)
	if zebra < 0 || opencode < 0 || apple < 0 {
		t.Fatalf("keys missing:\n%s", out)
	}
	if !(zebra < opencode && opencode < apple) {
		t.Fatalf("key order changed (want zebra,opencode,apple):\n%s", out)
	}
	// Untouched zebra section must be byte-identical to the input span.
	if !strings.Contains(out, "\"zebra\": [\n    {\n      \"id\": \"z\"\n    }\n  ]") {
		t.Fatalf("zebra section reformatted:\n%s", out)
	}
}

func TestRewriteBuiltinFilePreservesOutsideMarkers(t *testing.T) {
	src := "package registry\n\n// Code generated from models.dev (opencode-go provider, fetched 2026-09-22T00:00:00Z).\n// DO NOT EDIT BY HAND — regenerate with: go run ./cmd/fetch_modelsdev_models\n\nvar route = map[string]bool{\"a\": true}\n\nfunc infos() []*ModelInfo {\n// modelsdev:generated:begin\n\told()\n// modelsdev:generated:end\n\treturn models\n}\n"
	models := []*registry.ModelInfo{{
		ID: "glm-5.2", Object: "model", Created: 1781308800, OwnedBy: "opencode", Type: "opencode",
		DisplayName: "GLM-5.2", ContextLength: 1000000, MaxCompletionTokens: 131072,
		Thinking:                 &registry.ThinkingSupport{Levels: []string{"high", "max"}},
		SupportedInputModalities: []string{"text"}, SupportedOutputModalities: []string{"text"},
		SupportedParameters: []string{"tool_choice"},
		ExplicitThinking:    true, ExplicitInputModalities: true,
	}}
	updated, err := rewriteBuiltinFile(src, "opencode-go", models, "2026-09-22T00:00:00Z")
	if err != nil {
		t.Fatalf("rewriteBuiltinFile: %v", err)
	}
	if !strings.Contains(updated, `var route = map[string]bool{"a": true}`) {
		t.Fatal("hand-written route map not preserved")
	}
	if strings.Contains(updated, "old()") {
		t.Fatal("stale generated body not replaced")
	}
	if !strings.Contains(updated, `ID: "glm-5.2"`) || !strings.Contains(updated, `ContextLength: 1000000`) {
		t.Fatalf("entry not rendered:\n%s", updated)
	}
	if !strings.Contains(updated, `Levels: []string{"high", "max"}`) {
		t.Fatal("thinking levels not rendered")
	}
	if !strings.Contains(updated, "ExplicitThinking: true") || !strings.Contains(updated, "ExplicitInputModalities: true") {
		t.Fatal("explicit flags not rendered")
	}
	if _, err := rewriteBuiltinFile("no markers", "x", models, "s"); err == nil {
		t.Fatal("expected error when markers missing")
	}
	noHeader := "package registry\n\nfunc infos() []*ModelInfo {\n// modelsdev:generated:begin\n\told()\n// modelsdev:generated:end\n\treturn models\n}\n"
	if _, err := rewriteBuiltinFile(noHeader, "x", models, "s"); err == nil {
		t.Fatal("expected error when generated header lines missing")
	}
}

func TestRewriteBuiltinFileStampStable(t *testing.T) {
	models := []*registry.ModelInfo{{
		ID: "glm-5.2", Object: "model", OwnedBy: "opencode", Type: "opencode", DisplayName: "GLM-5.2",
	}}
	src := "package registry\n\n// Code generated from models.dev (opencode-go provider, fetched 2026-09-22T00:00:00Z).\n// DO NOT EDIT BY HAND — regenerate with: go run ./cmd/fetch_modelsdev_models\n\nfunc infos() []*ModelInfo {\n// modelsdev:generated:begin\n\told()\n// modelsdev:generated:end\n\treturn models\n}\n"
	first, err := rewriteBuiltinFile(src, "opencode-go", models, "2026-09-22T00:00:00Z")
	if err != nil {
		t.Fatalf("rewriteBuiltinFile: %v", err)
	}
	// A second pass reusing the checked-in stamp must be byte-identical,
	// otherwise --check could never report "current".
	second, err := rewriteBuiltinFile(first, "opencode-go", models, extractFetchStamp(first))
	if err != nil {
		t.Fatalf("rewriteBuiltinFile: %v", err)
	}
	if first != second {
		t.Fatalf("rewrite not idempotent:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	if got := extractFetchStamp(first); got != "2026-09-22T00:00:00Z" {
		t.Fatalf("extractFetchStamp = %q", got)
	}
	if got := extractFetchStamp("no stamp here"); got != "" {
		t.Fatalf("extractFetchStamp without header = %q, want empty", got)
	}
}

func TestCheckSectionsNonEmpty(t *testing.T) {
	full := registry.ModelsDevSections{
		Opencode:    []*registry.ModelInfo{{ID: "a"}},
		Zai:         []*registry.ModelInfo{{ID: "b"}},
		HasOpencode: true,
		HasZai:      true,
	}
	if err := checkSectionsNonEmpty(full, false); err != nil {
		t.Fatalf("full sections: %v", err)
	}
	// Missing key refused.
	missing := full
	missing.HasZai = false
	missing.Zai = nil
	if err := checkSectionsNonEmpty(missing, false); err == nil {
		t.Fatal("expected refusal on missing zai section")
	}
	// Present-but-empty refused.
	empty := full
	empty.Opencode = nil
	if err := checkSectionsNonEmpty(empty, false); err == nil {
		t.Fatal("expected refusal on empty opencode section")
	}
	// --allow-empty overrides both.
	if err := checkSectionsNonEmpty(empty, true); err != nil {
		t.Fatalf("allow-empty: %v", err)
	}
	if err := checkSectionsNonEmpty(missing, true); err != nil {
		t.Fatalf("allow-empty: %v", err)
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/snap.json"
	writeFileAtomic(path, []byte(`{"a":1}`))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != `{"a":1}` {
		t.Fatalf("content = %q", data)
	}
	leftovers, err := filepath.Glob(dir + "/*.tmp-*")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("tmp leftovers: %v", leftovers)
	}
}

func TestFetchCatalogRejectsOversize(t *testing.T) {
	big := bytes.Repeat([]byte("x"), registry.MaxModelsDevSize+100)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(big)
	}))
	t.Cleanup(server.Close)
	if _, err := fetchCatalog(server.URL); err == nil {
		t.Fatal("expected error for oversize catalog")
	}
}

func TestOpencodeRouteFor(t *testing.T) {
	cases := []struct {
		id   string
		npm  string
		want string
	}{
		{"gpt-6-luna", "@ai-sdk/openai", "responses"},
		{"grok-4.7", "@ai-sdk/openai", "responses"},
		{"minimax-m2.5", "@ai-sdk/anthropic", "anthropic"},
		{"qwen3.8-flash", "@ai-sdk/anthropic", "anthropic"},
		{"glm-5.3", "", "chat"},
		{"mimo-v2.6-flash", "", "chat"},
		// Pin wins over an absent hint: gateway-verified /responses
		// (omp behavior.kdl, verified 2026-08-08).
		{"deepseek-v4-flash", "", "responses"},
		// Pin wins over even a conflicting hint.
		{"deepseek-v4-flash", "@ai-sdk/anthropic", "responses"},
		// Unknown future npm values fall back to the safe chat lane.
		{"some-future-model", "@ai-sdk/vertex", "chat"},
	}
	for _, tc := range cases {
		if got := opencodeRouteFor(tc.id, tc.npm); got != tc.want {
			t.Errorf("opencodeRouteFor(%q, %q) = %q, want %q", tc.id, tc.npm, got, tc.want)
		}
	}
}

func TestExtractOpencodeNPM(t *testing.T) {
	data := []byte(`{
		"opencode-go": {
			"api": "https://opencode.ai/zen/go/v1",
			"models": {
				"gpt-6-luna": {"id": "gpt-6-luna", "provider": {"npm": "@ai-sdk/openai"}},
				"glm-5.3": {"id": "glm-5.3"}
			}
		},
		"zai-coding-plan": {
			"models": {
				"glm-5.3-flash": {"id": "glm-5.3-flash", "provider": {"npm": "@ai-sdk/anthropic"}}
			}
		}
	}`)
	got := extractOpencodeNPM(data)
	want := map[string]string{"gpt-6-luna": "@ai-sdk/openai"}
	if len(got) != len(want) {
		t.Fatalf("extractOpencodeNPM = %v, want %v", got, want)
	}
	for id, npm := range want {
		if got[id] != npm {
			t.Fatalf("npm[%q] = %q, want %q", id, got[id], npm)
		}
	}
}

func TestRewriteRoutesSection(t *testing.T) {
	src := "package registry\n\n" + routesBegin + "\nvar stale = true\n" + routesEnd + "\n\nfunc keep() {}\n"
	updated, err := rewriteRoutesSection(src, "var fresh = true", true)
	if err != nil {
		t.Fatalf("rewriteRoutesSection: %v", err)
	}
	if !strings.Contains(updated, "var fresh = true") || strings.Contains(updated, "var stale = true") {
		t.Fatalf("routes section not replaced:\n%s", updated)
	}
	if !strings.Contains(updated, "func keep() {}") {
		t.Fatalf("content outside markers not preserved:\n%s", updated)
	}
	if _, err := rewriteRoutesSection("no markers", "x", true); err == nil {
		t.Fatal("expected error when markers missing and required")
	}
	unchanged, err := rewriteRoutesSection("no markers", "x", false)
	if err != nil || unchanged != "no markers" {
		t.Fatalf("optional markers must leave src untouched: %q, %v", unchanged, err)
	}
}

func TestRenderOpencodeRoutesDeterministic(t *testing.T) {
	routes := map[string]string{
		"glm-5.3":     "chat",
		"zeta-model":  "responses",
		"alpha-model": "responses",
		"mid-model":   "anthropic",
		"beta-model":  "anthropic",
	}
	out := renderOpencodeRoutes(routes)
	anthropicIdx := strings.Index(out, "opencodeAnthropicRouteModels")
	responsesIdx := strings.Index(out, "opencodeResponsesRouteModels")
	if anthropicIdx < 0 || responsesIdx < 0 || anthropicIdx > responsesIdx {
		t.Fatalf("map order wrong:\n%s", out)
	}
	// Chat-route ids must never appear in either map.
	if strings.Contains(out, "glm-5.3") {
		t.Fatalf("chat model leaked into route maps:\n%s", out)
	}
	alphaIdx := strings.Index(out, `"alpha-model"`)
	zetaIdx := strings.Index(out, `"zeta-model"`)
	if alphaIdx > zetaIdx {
		t.Fatalf("responses ids not sorted:\n%s", out)
	}
	if out != renderOpencodeRoutes(routes) {
		t.Fatal("render not deterministic")
	}
}
