package registry

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestParseOpenRouterModels(t *testing.T) {
	got, err := parseOpenRouterModels([]byte(`{"data":[
		{"id":"anthropic/claude-haiku-5.5","context_length":1000000,"top_provider":{"context_length":1000000,"max_completion_tokens":128000},
		 "reasoning":{"mandatory":false,"supported_efforts":["max","XHigh","high","medium","low","none","high"],"default_effort":"medium"}},
		{"id":"OpenAI/GPT-6.1-Sol","context_length":0,"top_provider":{"context_length":1050000,"max_completion_tokens":128000}},
		{"id":"vendor/efforts-only","context_length":0,"top_provider":{},"reasoning":{"supported_efforts":["high","none"]}},
		{"id":"empty/model","context_length":0,"top_provider":{},"reasoning":{"mandatory":true}},
		{"context_length":5}]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	haiku := got["anthropic/claude-haiku-5.5"]
	if haiku.ContextLength != 1000000 || haiku.MaxCompletionTokens != 128000 ||
		!slices.Equal(haiku.ReasoningEfforts, []string{"max", "xhigh", "high", "medium", "low", "none"}) {
		t.Errorf("haiku = %+v (efforts lower-cased, duplicates dropped)", haiku)
	}
	sol := got["openai/gpt-6.1-sol"]
	if sol.ContextLength != 1050000 || sol.MaxCompletionTokens != 128000 || len(sol.ReasoningEfforts) != 0 {
		t.Errorf("gpt (top_provider fallback, lower-cased id, no efforts) = %+v", sol)
	}
	if only := got["vendor/efforts-only"]; !slices.Equal(only.ReasoningEfforts, []string{"high", "none"}) {
		t.Errorf("a model with only published efforts must be kept: %+v", only)
	}
	if _, ok := got["empty/model"]; ok || len(got) != 3 {
		t.Errorf("unexpected entries: %+v", got)
	}
	if _, err := parseOpenRouterModels([]byte(`{"nope":1}`)); err == nil {
		t.Error("want an error without a data array")
	}
}

func TestOpenRouterLookupReturnsACopyOfTheEfforts(t *testing.T) {
	restore := SetOpenRouterModelsForTest(map[string]OpenRouterModel{
		"Anthropic/Claude-Opus-5.5": {ReasoningEfforts: []string{"max", "high"}},
	})
	defer restore()
	first, ok := LookupOpenRouterModel("openrouter", "anthropic/claude-opus-5.5")
	if !ok {
		t.Fatal("model not found")
	}
	first.ReasoningEfforts[0] = "changed"
	if second, _ := LookupOpenRouterModel("openrouter", "anthropic/claude-opus-5.5"); second.ReasoningEfforts[0] != "max" {
		t.Fatalf("a caller changed the catalog: %v", second.ReasoningEfforts)
	}
}

// testOpenRouterCatalog returns a catalog that may always fetch, with a settable clock and a channel that receives
// once per completed refresh.
func testOpenRouterCatalog(fetch func(context.Context) (map[string]OpenRouterModel, error)) (*openRouterCatalog, *time.Time, chan struct{}) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	refreshed := make(chan struct{}, 8)
	catalog := &openRouterCatalog{
		fetch:     fetch,
		remote:    func() (context.Context, bool) { return context.Background(), true },
		now:       func() time.Time { return now },
		refreshed: func() { refreshed <- struct{}{} },
	}
	return catalog, &now, refreshed
}

func waitRefreshed(t *testing.T, refreshed <-chan struct{}) {
	t.Helper()
	select {
	case <-refreshed:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not complete")
	}
}

func (c *openRouterCatalog) isFetching() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fetching
}

func TestOpenRouterCatalogFetchesInBackgroundAndNotifies(t *testing.T) {
	calls := 0
	catalog, now, refreshed := testOpenRouterCatalog(func(context.Context) (map[string]OpenRouterModel, error) {
		calls++
		return map[string]OpenRouterModel{"anthropic/claude-haiku-5.5": {ContextLength: 1000000, MaxCompletionTokens: 128000}}, nil
	})
	notified := make(chan []string, 1)
	SetModelRefreshCallback(func(providers []string) { notified <- providers })
	defer SetModelRefreshCallback(nil)

	if _, ok := catalog.lookup("openrouter", "anthropic/claude-haiku-5.5"); ok {
		t.Fatal("first lookup must not block on the network")
	}
	waitRefreshed(t, refreshed)
	select {
	case providers := <-notified:
		if len(providers) != 1 || providers[0] != "openrouter" {
			t.Fatalf("notified %v, want [openrouter]", providers)
		}
	default:
		t.Fatal("no refresh notification")
	}
	published, ok := catalog.lookup("openrouter", "Anthropic/Claude-Haiku-5.5")
	if !ok || published.MaxCompletionTokens != 128000 {
		t.Fatalf("lookup after fetch = %+v, %v", published, ok)
	}
	if catalog.isFetching() || calls != 1 {
		t.Fatalf("a fresh catalog must not refetch (fetching=%v, calls=%d)", catalog.isFetching(), calls)
	}

	*now = now.Add(ModelsRefreshInterval + time.Second)
	catalog.lookup("openrouter", "anthropic/claude-haiku-5.5")
	waitRefreshed(t, refreshed)
	if calls != 2 {
		t.Fatalf("a stale catalog must refetch once, calls=%d", calls)
	}
}

func TestOpenRouterCatalogBacksOffAfterFailure(t *testing.T) {
	calls := 0
	catalog, now, refreshed := testOpenRouterCatalog(func(context.Context) (map[string]OpenRouterModel, error) {
		calls++
		return nil, errors.New("offline")
	})
	catalog.lookup("openrouter", "x")
	waitRefreshed(t, refreshed)

	*now = now.Add(30 * time.Second)
	catalog.lookup("openrouter", "x")
	if catalog.isFetching() || calls != 1 {
		t.Fatalf("retried within the back-off (fetching=%v, calls=%d)", catalog.isFetching(), calls)
	}

	*now = now.Add(31 * time.Second)
	catalog.lookup("openrouter", "x")
	waitRefreshed(t, refreshed)
	if calls != 2 {
		t.Fatalf("no retry after the back-off, calls=%d", calls)
	}
}

func TestOpenRouterCatalogHonorsLocalModelPolicy(t *testing.T) {
	catalogRuntime.Lock()
	oldLocal := catalogRuntime.local
	catalogRuntime.local = true
	catalogRuntime.Unlock()
	defer func() {
		catalogRuntime.Lock()
		catalogRuntime.local = oldLocal
		catalogRuntime.Unlock()
	}()

	calls := 0
	catalog := &openRouterCatalog{
		fetch: func(context.Context) (map[string]OpenRouterModel, error) {
			calls++
			return nil, nil
		},
		remote: remoteCatalogPolicy,
	}
	if _, ok := catalog.lookup("openrouter", "anthropic/claude-haiku-5.5"); ok {
		t.Fatal("no model without a catalog")
	}
	if catalog.isFetching() || calls != 0 {
		t.Fatalf("--local-model must not fetch OpenRouter's catalog (fetching=%v, calls=%d)", catalog.isFetching(), calls)
	}
}
