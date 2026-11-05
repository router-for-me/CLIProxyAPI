package registry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestParseOpenRouterModelLimits(t *testing.T) {
	got, err := parseOpenRouterModelLimits([]byte(`{"data":[
		{"id":"anthropic/claude-haiku-5.5","context_length":1000000,"top_provider":{"context_length":1000000,"max_completion_tokens":128000}},
		{"id":"OpenAI/GPT-6.1-Sol","context_length":0,"top_provider":{"context_length":1050000,"max_completion_tokens":128000}},
		{"id":"empty/model","context_length":0,"top_provider":{}},
		{"context_length":5}]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got["anthropic/claude-haiku-5.5"] != (OpenRouterModelLimits{ContextLength: 1000000, MaxCompletionTokens: 128000}) {
		t.Errorf("haiku = %+v", got["anthropic/claude-haiku-5.5"])
	}
	if got["openai/gpt-6.1-sol"] != (OpenRouterModelLimits{ContextLength: 1050000, MaxCompletionTokens: 128000}) {
		t.Errorf("gpt (top_provider fallback, lower-cased id) = %+v", got["openai/gpt-6.1-sol"])
	}
	if _, ok := got["empty/model"]; ok || len(got) != 2 {
		t.Errorf("unexpected entries: %+v", got)
	}
	if _, err := parseOpenRouterModelLimits([]byte(`{"nope":1}`)); err == nil {
		t.Error("want an error without a data array")
	}
}

func TestOpenRouterCatalogFetchesInBackgroundAndNotifies(t *testing.T) {
	fetched := make(chan struct{}, 4)
	catalog := &openRouterCatalog{fetch: func(context.Context) (map[string]OpenRouterModelLimits, error) {
		fetched <- struct{}{}
		return map[string]OpenRouterModelLimits{"anthropic/claude-haiku-5.5": {ContextLength: 1000000, MaxCompletionTokens: 128000}}, nil
	}}
	notified := make(chan []string, 1)
	SetModelRefreshCallback(func(providers []string) { notified <- providers })
	defer SetModelRefreshCallback(nil)

	if _, ok := catalog.lookup("openrouter", "anthropic/claude-haiku-5.5"); ok {
		t.Fatal("first lookup must not block on the network")
	}
	select {
	case <-fetched:
	case <-time.After(2 * time.Second):
		t.Fatal("no background fetch")
	}
	select {
	case providers := <-notified:
		if len(providers) != 1 || providers[0] != "openrouter" {
			t.Fatalf("notified %v, want [openrouter]", providers)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no refresh notification")
	}
	limits, ok := catalog.lookup("openrouter", "Anthropic/Claude-Haiku-5.5")
	if !ok || limits.MaxCompletionTokens != 128000 {
		t.Fatalf("lookup after fetch = %+v, %v", limits, ok)
	}
	select {
	case <-fetched:
		t.Fatal("fresh catalog must not refetch")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestOpenRouterCatalogBacksOffAfterFailure(t *testing.T) {
	calls := make(chan struct{}, 4)
	catalog := &openRouterCatalog{fetch: func(context.Context) (map[string]OpenRouterModelLimits, error) {
		calls <- struct{}{}
		return nil, errors.New("offline")
	}}
	catalog.lookup("openrouter", "x")
	<-calls
	time.Sleep(50 * time.Millisecond)
	catalog.lookup("openrouter", "x")
	select {
	case <-calls:
		t.Fatal("retried immediately after a failure")
	case <-time.After(100 * time.Millisecond):
	}
}
