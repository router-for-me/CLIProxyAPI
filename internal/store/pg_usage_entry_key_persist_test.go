package store

import (
	"fmt"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// TestUsageFlusherPersistsEntryProviderKey is the Postgres round-trip guard
// for the provider-budget attribution chain: the record's EntryProviderKey
// must survive flusher -> toEvent -> BatchInsertEvents -> usage_events row.
// Without the schema migration (entry_provider_key column) and the INSERT
// binding, the value silently lands as empty and the provider-budget query
// reads zero spend. Skips when PGSTORE_TEST_DSN is unset.
func TestUsageFlusherPersistsEntryProviderKey(t *testing.T) {
	store := newTestPostgresStore(t, "flusher_entry_key")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)
	apiKeys := NewAPIKeyStore(store)
	flusher := NewUsageFlusher(us, apiKeys, nil, FlusherConfig{QueueCap: 10, FlushInterval: time.Hour, FlushBatchSize: 5})
	if err := flusher.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(flusher.Stop)

	flusher.HandleUsage(ctx, coreusage.Record{
		Provider: "openai-compatible-akf-zhipu", Model: "glm-5.2", Alias: "glm-5.2",
		AuthType: "api_key", Source: "test",
		EntryProviderKey: "openai-compatible-akf-zhipu:key-91",
		RequestedAt:      time.Now().UTC(),
		Detail:           coreusage.Detail{InputTokens: 100, OutputTokens: 100, TotalTokens: 200},
	})
	flusher.Stop()

	var entryKey string
	err := us.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT entry_provider_key FROM %s ORDER BY id DESC LIMIT 1`, us.eventsTable,
	)).Scan(&entryKey)
	if err != nil {
		t.Fatalf("query persisted entry_provider_key: %v", err)
	}
	if entryKey != "openai-compatible-akf-zhipu:key-91" {
		t.Errorf("entry_provider_key = %q; want %q", entryKey, "openai-compatible-akf-zhipu:key-91")
	}
}

// TestInsertErrorPersistsEntryProviderKey mirrors the events round-trip for
// the failures table: InsertError must bind entry_provider_key so failed
// attempts stay attributable to the same upstream entry. Skips when
// PGSTORE_TEST_DSN is unset.
func TestInsertErrorPersistsEntryProviderKey(t *testing.T) {
	store := newTestPostgresStore(t, "flusher_entry_key_err")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)

	err := us.InsertError(ctx, UsageError{
		Provider:         "openai-compatible-akf-zhipu",
		Model:            "glm-5.2",
		EntryProviderKey: "openai-compatible-akf-zhipu:key-91",
		FailStatusCode:   502,
		ErrorMessage:     "upstream error",
		RequestedAt:      time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("InsertError: %v", err)
	}

	var entryKey string
	if err := us.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT entry_provider_key FROM %s ORDER BY id DESC LIMIT 1`, us.errorsTable,
	)).Scan(&entryKey); err != nil {
		t.Fatalf("query persisted entry_provider_key: %v", err)
	}
	if entryKey != "openai-compatible-akf-zhipu:key-91" {
		t.Errorf("entry_provider_key = %q; want %q", entryKey, "openai-compatible-akf-zhipu:key-91")
	}
}
