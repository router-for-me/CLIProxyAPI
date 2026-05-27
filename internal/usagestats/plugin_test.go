package usagestats

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// mockStoreForPlugin tracks append calls.
type mockStoreForPlugin struct {
	appendCount atomic.Int32
	appendErr   error
	lastEvent   Event
}

func (m *mockStoreForPlugin) EnsureSchema(_ context.Context) error { return nil }
func (m *mockStoreForPlugin) Append(_ context.Context, event Event) error {
	m.lastEvent = event
	m.appendCount.Add(1)
	return m.appendErr
}
func (m *mockStoreForPlugin) Summary(_ context.Context, _ Query) (*SummaryResult, error) {
	return nil, nil
}
func (m *mockStoreForPlugin) Close() error { return nil }

func TestPlugin_HandleUsage_Appends(t *testing.T) {
	store := &mockStoreForPlugin{}
	pm := NewPriceMatcher([]ModelPrice{
		{Provider: "openai", Model: "gpt-4.1-mini", InputCostPerToken: 0.0000004, OutputCostPerToken: 0.0000016},
	})
	plugin := NewPlugin(store, pm)

	record := coreusage.Record{
		Provider: "openai",
		Model:    "gpt-4.1-mini",
		Detail: coreusage.Detail{
			InputTokens:  100,
			OutputTokens: 50,
		},
	}
	plugin.HandleUsage(context.Background(), record)

	if store.appendCount.Load() != 1 {
		t.Errorf("append count = %d, want 1", store.appendCount.Load())
	}
	if store.lastEvent.Provider != "openai" {
		t.Errorf("provider = %q, want openai", store.lastEvent.Provider)
	}
	if !store.lastEvent.CostKnown {
		t.Error("cost_known = false, want true")
	}
}

func TestPlugin_HandleUsage_Disabled(t *testing.T) {
	store := &mockStoreForPlugin{}
	plugin := NewPlugin(nil, nil) // nil store means disabled

	record := coreusage.Record{
		Provider: "openai",
		Model:    "gpt-4.1-mini",
	}
	plugin.HandleUsage(context.Background(), record)

	if store.appendCount.Load() != 0 {
		t.Errorf("append count = %d, want 0 for disabled plugin", store.appendCount.Load())
	}
}

func TestPlugin_HandleUsage_StoreError_NoPanic(t *testing.T) {
	store := &mockStoreForPlugin{
		appendErr: fmt.Errorf("db error"),
	}
	plugin := NewPlugin(store, nil)

	record := coreusage.Record{
		Provider: "openai",
		Model:    "gpt-4.1-mini",
	}
	// Must not panic.
	plugin.HandleUsage(context.Background(), record)

	if store.appendCount.Load() != 1 {
		t.Errorf("append count = %d, want 1 (error does not prevent attempt)", store.appendCount.Load())
	}
}

func TestPlugin_SetEnabled(t *testing.T) {
	store := &mockStoreForPlugin{}
	plugin := NewPlugin(store, nil)

	if !plugin.Enabled() {
		t.Error("expected enabled with non-nil store")
	}

	plugin.SetEnabled(false)
	if plugin.Enabled() {
		t.Error("expected disabled after SetEnabled(false)")
	}

	// Disabled plugin should not append.
	record := coreusage.Record{Provider: "openai", Model: "gpt-4.1-mini"}
	plugin.HandleUsage(context.Background(), record)
	if store.appendCount.Load() != 0 {
		t.Errorf("append count = %d, want 0 when disabled", store.appendCount.Load())
	}
}

func TestPlugin_SetStore(t *testing.T) {
	plugin := NewPlugin(nil, nil) // starts disabled

	if plugin.Enabled() {
		t.Error("expected disabled with nil store")
	}

	store := &mockStoreForPlugin{}
	plugin.SetStore(store)
	if !plugin.Enabled() {
		t.Error("expected enabled after SetStore")
	}

	plugin.SetStore(nil)
	if plugin.Enabled() {
		t.Error("expected disabled after SetStore(nil)")
	}
}

func TestPlugin_NilReceiver(t *testing.T) {
	var plugin *Plugin
	// All methods should be safe on nil.
	plugin.HandleUsage(context.Background(), coreusage.Record{})
	plugin.SetEnabled(true)
	plugin.SetStore(nil)
	plugin.SetMatcher(nil)
	if plugin.Enabled() {
		t.Error("nil plugin should not be enabled")
	}
	if plugin.Store() != nil {
		t.Error("nil plugin store should be nil")
	}
}
