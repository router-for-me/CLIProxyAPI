package cline

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetchAvailableModelsAuthHeaderAndPath(t *testing.T) {
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"moonshotai/kimi-k3","name":"kimi-k3","description":"Kimi K3","tags":["NEW"]}]`))
	}))
	defer server.Close()

	auth := newTestClineAuth("https://unused.example", server.URL+"/api/v1")
	models, err := auth.FetchAvailableModels(context.Background(), "cline-test-token")
	if err != nil {
		t.Fatalf("FetchAvailableModels() error = %v", err)
	}
	if gotPath != "/api/v1/ai/cline/models" {
		t.Fatalf("path = %q, want /api/v1/ai/cline/models", gotPath)
	}
	if gotAuth != "Bearer cline-test-token" {
		t.Fatalf("Authorization = %q, want Bearer cline-test-token", gotAuth)
	}
	if len(models) != 1 || models[0].ID != "moonshotai/kimi-k3" || models[0].Name != "kimi-k3" || len(models[0].Tags) != 1 || models[0].Tags[0] != "NEW" {
		t.Fatalf("unexpected models: %+v", models)
	}
}

func TestParseClineModelsBareArray(t *testing.T) {
	models, err := ParseClineModels([]byte(`[
		{"id":"anthropic/claude-sonnet-4-6","name":"claude-sonnet-4-6","description":"Sonnet"},
		{"id":"","name":"skip-me"},
		{"id":"google/gemini-2.5-pro"}
	]`))
	if err != nil {
		t.Fatalf("ParseClineModels() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(models), models)
	}
	if models[0].ID != "anthropic/claude-sonnet-4-6" || models[1].ID != "google/gemini-2.5-pro" {
		t.Fatalf("unexpected ids: %+v", models)
	}
}

func TestParseClineModelsEnvelope(t *testing.T) {
	models, err := ParseClineModels([]byte(`{"success":true,"data":[{"id":"cline-pass/glm-5.3","name":"glm-5.3"}]}`))
	if err != nil {
		t.Fatalf("ParseClineModels() error = %v", err)
	}
	if len(models) != 1 || models[0].ID != "cline-pass/glm-5.3" {
		t.Fatalf("unexpected models: %+v", models)
	}
}

func TestParseClineModelsBucketed(t *testing.T) {
	models, err := ParseClineModels([]byte(`{
		"recommended":[{"id":"moonshotai/kimi-k3","name":"kimi-k3","tags":["NEW"]}],
		"free":[{"id":"cline-free/glm-air","name":"glm-air"}],
		"clinePass":[{"id":"cline-pass/glm-5.3"},{"id":"moonshotai/kimi-k3","name":"kimi-k3-duplicate"}],
		"clineCloud":[{"id":"cline-cloud/cc"}]
	}`))
	if err != nil {
		t.Fatalf("ParseClineModels() error = %v", err)
	}
	if len(models) != 4 {
		t.Fatalf("len = %d, want 4 (buckets merged, duplicates dropped): %+v", len(models), models)
	}
	ids := make(map[string]bool, 4)
	for _, m := range models {
		ids[m.ID] = true
	}
	for _, want := range []string{"moonshotai/kimi-k3", "cline-free/glm-air", "cline-pass/glm-5.3", "cline-cloud/cc"} {
		if !ids[want] {
			t.Fatalf("missing %s in %+v", want, models)
		}
	}
}

func TestParseClineModelsErrorPaths(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"empty", nil},
		{"invalid json", []byte("not-json")},
		{"scalar", []byte("42")},
	} {
		if _, err := ParseClineModels(tc.body); err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid token"}`))
	}))
	defer server.Close()
	auth := newTestClineAuth("https://unused.example", server.URL+"/api/v1")
	if _, err := auth.FetchAvailableModels(context.Background(), "bad-token"); err == nil || !strings.Contains(err.Error(), "status 401") {
		t.Fatalf("FetchAvailableModels() error = %v, want status 401", err)
	}

	if _, err := auth.FetchAvailableModels(context.Background(), "  "); err == nil {
		t.Fatal("FetchAvailableModels() error = nil, want access token required")
	}
}

func TestModelsFromMetadata(t *testing.T) {
	stored := []ClineModelInfo{{ID: "moonshotai/kimi-k3", Name: "kimi-k3", Tags: []string{"NEW"}}}

	// In-memory typed form (same process as the fetch).
	fromTyped := ModelsFromMetadata(map[string]any{ModelsMetadataKey: stored})
	if len(fromTyped) != 1 || fromTyped[0].ID != "moonshotai/kimi-k3" || fromTyped[0].Tags[0] != "NEW" {
		t.Fatalf("typed metadata decode failed: %+v", fromTyped)
	}

	// JSON round-trip form (file reload).
	jsonForm := []any{map[string]any{"id": "anthropic/claude-sonnet-4-6", "name": "sonnet"}}
	fromJSON := ModelsFromMetadata(map[string]any{ModelsMetadataKey: jsonForm})
	if len(fromJSON) != 1 || fromJSON[0].ID != "anthropic/claude-sonnet-4-6" {
		t.Fatalf("json metadata decode failed: %+v", fromJSON)
	}

	if ModelsFromMetadata(nil) != nil || ModelsFromMetadata(map[string]any{}) != nil {
		t.Fatal("missing key must return nil")
	}
}

func TestDetectAvailableModelsOnce(t *testing.T) {
	var calls atomic.Int32
	var wg sync.WaitGroup
	afreshKey := fmt.Sprintf("detect-once-%d", time.Now().UnixNano())
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := DetectAvailableModelsOnce(afreshKey, func() error {
				calls.Add(1)
				return nil
			}); err != nil {
				t.Errorf("DetectAvailableModelsOnce() error = %v", err)
			}
		}()
	}
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("fn executed %d times, want 1", got)
	}

	// A different key must run a fresh detection.
	if err := DetectAvailableModelsOnce(afreshKey+"-2", func() error {
		calls.Add(1)
		return nil
	}); err != nil {
		t.Fatalf("DetectAvailableModelsOnce(second key) error = %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("fn executed %d times across two keys, want 2", got)
	}
}
