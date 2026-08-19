package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func withIsolatedCursorModelsCache(t *testing.T) {
	t.Helper()
	cursorModelsCacheMu.Lock()
	previous := cursorModelsCache
	cursorModelsCache = make(map[string][]*registry.ModelInfo)
	cursorModelsCacheMu.Unlock()
	t.Cleanup(func() {
		cursorModelsCacheMu.Lock()
		cursorModelsCache = previous
		cursorModelsCacheMu.Unlock()
	})
}

func cursorCacheTestModel(id string) *registry.ModelInfo {
	return &registry.ModelInfo{
		ID:                         id,
		Object:                     "model",
		Created:                    123,
		OwnedBy:                    "cursor",
		Type:                       cursorAuthType,
		DisplayName:                "Live " + id,
		Name:                       "name-" + id,
		Version:                    "v1",
		Description:                "live Cursor model",
		InputTokenLimit:            100,
		OutputTokenLimit:           50,
		SupportedGenerationMethods: []string{"generateContent"},
		ContextLength:              200,
		MaxCompletionTokens:        100,
		SupportedParameters:        []string{"temperature"},
		SupportedEndpoints:         []string{"/v1/chat/completions"},
		SupportedInputModalities:   []string{"TEXT", "IMAGE"},
		SupportedOutputModalities:  []string{"TEXT"},
		SupportsWebSearch:          true,
		Thinking:                   &registry.ThinkingSupport{Min: 1, Max: 2, ZeroAllowed: true, DynamicAllowed: true, Levels: []string{"low", "high"}},
		Config:                     &registry.ModelConfig{OverrideHeader: map[string]string{"x-cursor-test": id}},
		UserDefined:                true,
	}
}

func TestFetchCursorModelsColdStartUsesFallback(t *testing.T) {
	withIsolatedCursorModelsCache(t)

	got := FetchCursorModels(context.Background(), &cliproxyauth.Auth{ID: "cold-start"}, nil)
	want := GetCursorFallbackModels()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cold-start models = %+v, want hardcoded fallback %+v", got, want)
	}
}

func TestFetchCursorModelsNilAuthUsesFallback(t *testing.T) {
	withIsolatedCursorModelsCache(t)

	got := FetchCursorModels(context.Background(), nil, nil)
	if !reflect.DeepEqual(got, GetCursorFallbackModels()) {
		t.Fatalf("nil auth models = %+v, want hardcoded fallback", got)
	}
}

func TestFetchCursorModelsSuccessfulFetchReplacesLastGoodSnapshot(t *testing.T) {
	withIsolatedCursorModelsCache(t)

	const authID = "auth-1"
	cacheCursorModels(authID, []*registry.ModelInfo{cursorCacheTestModel("old-live-model")})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer access-token" {
			t.Errorf("Authorization = %q, want bearer access token", request.Header.Get("Authorization"))
		}
		if _, err := response.Write(cursorModelsResponse("new-live-model", "New Live Model")); err != nil {
			t.Errorf("write models response: %v", err)
		}
	}))
	defer server.Close()

	got := fetchCursorModels(context.Background(), authID, "access-token", server.Client(), server.URL)
	if len(got) != 1 || got[0] == nil || got[0].ID != "new-live-model" {
		t.Fatalf("successful fetch models = %+v, want new live model", got)
	}

	got[0].DisplayName = "mutated caller model"
	cached := cursorModelsOrFallback(authID)
	if len(cached) != 1 || cached[0] == nil || cached[0].ID != "new-live-model" || cached[0].DisplayName != "New Live Model" {
		t.Fatalf("successful fetch did not replace an isolated last-good snapshot: %+v", cached)
	}
}

func TestFetchCursorModelsRepeatedFailuresReturnLastGoodSnapshot(t *testing.T) {
	withIsolatedCursorModelsCache(t)

	const authID = "auth-with-last-good"
	var failures atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if failures.Load() {
			response.WriteHeader(http.StatusBadGateway)
			return
		}
		if _, err := response.Write(cursorModelsResponse("live-model", "Live Model")); err != nil {
			t.Errorf("write models response: %v", err)
		}
	}))
	defer server.Close()

	first := fetchCursorModels(context.Background(), authID, "access-token", server.Client(), server.URL)
	if len(first) != 1 || first[0] == nil || first[0].ID != "live-model" {
		t.Fatalf("successful fetch models = %+v, want live model", first)
	}

	failures.Store(true)
	for attempt := 0; attempt < 4; attempt++ {
		got := fetchCursorModels(context.Background(), authID, "access-token", server.Client(), server.URL)
		if len(got) != 1 || got[0] == nil || got[0].ID != "live-model" {
			t.Fatalf("failure %d returned %+v, want the last-good live model", attempt+1, got)
		}
		got[0].DisplayName = "mutated failure result"
	}

	cached := cursorModelsOrFallback(authID)
	if len(cached) != 1 || cached[0] == nil || cached[0].ID != "live-model" || cached[0].DisplayName != "Live Model" {
		t.Fatalf("repeated failures changed the last-good snapshot: %+v", cached)
	}
}

func TestCursorModelsCacheReturnsMutationIsolatedCopies(t *testing.T) {
	withIsolatedCursorModelsCache(t)

	cacheCursorModels("auth-1", []*registry.ModelInfo{cursorCacheTestModel("live-model")})
	first := cursorModelsOrFallback("auth-1")
	mutateCursorCacheTestModel(first[0])

	got := cursorModelsOrFallback("auth-1")
	want := cursorCacheTestModel("live-model")
	if !reflect.DeepEqual(got, []*registry.ModelInfo{want}) {
		t.Fatalf("cached snapshot was mutated through a returned copy: got %+v, want %+v", got, []*registry.ModelInfo{want})
	}
}

func TestCacheCursorModelsStoresWriterDeepSnapshot(t *testing.T) {
	withIsolatedCursorModelsCache(t)

	source := cursorCacheTestModel("live-model")
	cacheCursorModels("auth-1", []*registry.ModelInfo{source})
	mutateCursorCacheTestModel(source)

	got := cursorModelsOrFallback("auth-1")
	want := cursorCacheTestModel("live-model")
	if !reflect.DeepEqual(got, []*registry.ModelInfo{want}) {
		t.Fatalf("cached snapshot was mutated through the source: got %+v, want %+v", got, []*registry.ModelInfo{want})
	}
}

func TestCursorModelsCacheConcurrentAuthIsolation(t *testing.T) {
	withIsolatedCursorModelsCache(t)

	const authCount = 8
	const updatesPerAuth = 64
	for authIndex := 0; authIndex < authCount; authIndex++ {
		authID := fmt.Sprintf("auth-%d", authIndex)
		cacheCursorModels(authID, []*registry.ModelInfo{cursorCacheTestModel(authID + "-seed")})
	}

	var wg sync.WaitGroup
	var once sync.Once
	var failure string
	recordFailure := func(format string, args ...any) {
		once.Do(func() {
			failure = fmt.Sprintf(format, args...)
		})
	}

	for authIndex := 0; authIndex < authCount; authIndex++ {
		authID := fmt.Sprintf("auth-%d", authIndex)

		wg.Add(1)
		go func(authID string) {
			defer wg.Done()
			for update := 0; update < updatesPerAuth; update++ {
				cacheCursorModels(authID, []*registry.ModelInfo{cursorCacheTestModel(fmt.Sprintf("%s-live-%d", authID, update))})
			}
		}(authID)

		wg.Add(1)
		go func(authID string) {
			defer wg.Done()
			for read := 0; read < updatesPerAuth; read++ {
				models := cursorModelsOrFallback(authID)
				if len(models) != 1 || models[0] == nil || !strings.HasPrefix(models[0].ID, authID+"-") {
					recordFailure("auth %s read %+v", authID, models)
					return
				}
				mutateCursorCacheTestModel(models[0])
			}
		}(authID)
	}

	wg.Wait()
	if failure != "" {
		t.Fatal(failure)
	}

	for authIndex := 0; authIndex < authCount; authIndex++ {
		authID := fmt.Sprintf("auth-%d", authIndex)
		wantID := fmt.Sprintf("%s-live-%d", authID, updatesPerAuth-1)
		got := cursorModelsOrFallback(authID)
		if len(got) != 1 || got[0] == nil || got[0].ID != wantID {
			t.Fatalf("auth %s final snapshot = %+v, want %q", authID, got, wantID)
		}
	}
}

func mutateCursorCacheTestModel(model *registry.ModelInfo) {
	model.ID = "mutated"
	model.DisplayName = "mutated"
	model.SupportedGenerationMethods[0] = "mutated"
	model.SupportedParameters[0] = "mutated"
	model.SupportedEndpoints[0] = "mutated"
	model.SupportedInputModalities[0] = "mutated"
	model.SupportedOutputModalities[0] = "mutated"
	model.Thinking.Levels[0] = "mutated"
	model.Config.OverrideHeader["x-cursor-test"] = "mutated"
}

func cursorModelsResponse(modelID, displayName string) []byte {
	entry := make([]byte, 0, len(modelID)+len(displayName)+4)
	entry = append(entry, 0x0a, byte(len(modelID)))
	entry = append(entry, modelID...)
	entry = append(entry, 0x22, byte(len(displayName)))
	entry = append(entry, displayName...)

	response := make([]byte, 0, len(entry)+2)
	response = append(response, 0x0a, byte(len(entry)))
	response = append(response, entry...)
	return response
}
