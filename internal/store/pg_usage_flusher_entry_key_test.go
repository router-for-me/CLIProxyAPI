package store

import (
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// TestUsageFlusherToEventCarriesEntryProviderKey guards the provider-budget
// attribution chain at the flusher boundary: the record's EntryProviderKey
// (stamped by the usage reporter from the serving auth) must be copied onto
// the persisted UsageEvent / UsageError. Without this mapping the
// entry_provider_key column stays empty and the provider-budget query reads
// zero spend for every entry.
//
// toEvent / toError tolerate nil store / nil apiKeyStore, so no live Postgres
// is needed to exercise the field plumbing.
func TestUsageFlusherToEventCarriesEntryProviderKey(t *testing.T) {
	f := NewUsageFlusher(nil, nil, nil, DefaultFlusherConfig())
	ctx := cancelableTestCtx(t)

	measured := coreusage.Record{
		Provider: "openai-compatible-akf-zhipu", Model: "glm-5.2",
		APIKey: "sk-test", AuthType: "api_key", Source: "test",
		EntryProviderKey: "openai-compatible-akf-zhipu:key-91",
		RequestedAt:      now(),
		Detail:           coreusage.Detail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
	}
	event, _, _, ok := f.toEvent(ctx, measured)
	if !ok {
		t.Fatal("toEvent returned ok=false for a valid record")
	}
	if event.EntryProviderKey != "openai-compatible-akf-zhipu:key-91" {
		t.Fatalf("UsageEvent.EntryProviderKey = %q; want %q", event.EntryProviderKey, "openai-compatible-akf-zhipu:key-91")
	}

	// Records without the identity (legacy YAML entries, oauth channels) must
	// persist an empty value, not a fabricated one.
	plain := coreusage.Record{
		Provider: "claude", Model: "claude-opus",
		APIKey: "sk-test", AuthType: "api_key", Source: "test",
		RequestedAt: now(),
		Detail:      coreusage.Detail{InputTokens: 1, TotalTokens: 1},
	}
	event2, _, _, ok := f.toEvent(ctx, plain)
	if !ok {
		t.Fatal("toEvent returned ok=false for the plain record")
	}
	if event2.EntryProviderKey != "" {
		t.Fatalf("UsageEvent.EntryProviderKey = %q; want empty for record without entry identity", event2.EntryProviderKey)
	}
}

func TestUsageFlusherToErrorCarriesEntryProviderKey(t *testing.T) {
	f := NewUsageFlusher(nil, nil, nil, DefaultFlusherConfig())
	ctx := cancelableTestCtx(t)

	failed := coreusage.Record{
		Provider: "openai-compatible-akf-zhipu", Model: "glm-5.2",
		APIKey: "sk-test", AuthType: "api_key", Source: "test",
		EntryProviderKey: "openai-compatible-akf-zhipu:key-91",
		RequestedAt:      now(),
		Detail:           coreusage.Detail{InputTokens: 3, TotalTokens: 3},
		Failed:           true,
		Fail:             coreusage.Failure{StatusCode: 502, Body: "upstream error"},
		Generate:         coreusage.GenerateFlag(true),
	}
	usageError, _, _, ok := f.toError(ctx, failed)
	if !ok {
		t.Fatal("toError returned ok=false for a valid failure record")
	}
	if usageError.EntryProviderKey != "openai-compatible-akf-zhipu:key-91" {
		t.Fatalf("UsageError.EntryProviderKey = %q; want %q", usageError.EntryProviderKey, "openai-compatible-akf-zhipu:key-91")
	}
}
