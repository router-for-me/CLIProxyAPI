package handlers

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestCLISubFallbackChainUsesLiveCatalogOwnership(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	primaryClient := "test-cli-sub-primary"
	cursorClient := "test-cli-sub-cursor"
	droidClient := "test-cli-sub-droid"
	defer reg.UnregisterClient(primaryClient)
	defer reg.UnregisterClient(cursorClient)
	defer reg.UnregisterClient(droidClient)

	reg.RegisterClient(primaryClient, "openai", []*registry.ModelInfo{{ID: "gpt-9-test", OwnedBy: "openai"}})
	reg.RegisterClient(cursorClient, "cursor", []*registry.ModelInfo{{ID: "cursor-gpt-9-test-high", OwnedBy: cliSubscriptionsOwner}})
	reg.RegisterClient(droidClient, "droid", []*registry.ModelInfo{{ID: "droid-gpt-9-test", OwnedBy: cliSubscriptionsOwner}})

	chain := buildCLISubFallbackChain(reg, "gpt-9-test")
	if len(chain) != 2 {
		t.Fatalf("fallback chain length = %d, want 2: %#v", len(chain), chain)
	}
	if chain[0].Model != "cursor-gpt-9-test-high" || chain[1].Model != "droid-gpt-9-test" {
		t.Fatalf("fallback order = %#v, want cursor then droid", chain)
	}

	reg.RegisterClient(primaryClient, "gemini", []*registry.ModelInfo{{ID: "gpt-9-test", OwnedBy: "gemini"}})
	chain = buildCLISubFallbackChain(reg, "gpt-9-test")
	if len(chain) != 2 {
		t.Fatalf("fallback chain after ownership change length = %d, want 2", len(chain))
	}
	if chain[0].PrimaryOwner != "gemini" {
		t.Fatalf("primary owner = %q, want live catalog owner gemini", chain[0].PrimaryOwner)
	}
}

func TestCLISubFallbackChainSupportsDroidOnlyAndGenericVendors(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clients := []string{"test-cli-sub-minimax-primary", "test-cli-sub-minimax-droid", "test-cli-sub-minimax-other"}
	defer func() {
		for _, client := range clients {
			reg.UnregisterClient(client)
		}
	}()

	reg.RegisterClient(clients[0], "opencode-go", []*registry.ModelInfo{{ID: "custom/minimax-m9-test", OwnedBy: "opencode-go"}})
	reg.RegisterClient(clients[1], "droid", []*registry.ModelInfo{{ID: "droid-minimax-m9-test", OwnedBy: cliSubscriptionsOwner}})
	reg.RegisterClient(clients[2], "future-cli", []*registry.ModelInfo{{ID: "minimax-m9-test", OwnedBy: cliSubscriptionsOwner}})

	chain := buildCLISubFallbackChain(reg, "custom/minimax-m9-test")
	if len(chain) != 2 {
		t.Fatalf("fallback chain length = %d, want 2: %#v", len(chain), chain)
	}
	if chain[0].Model != "droid-minimax-m9-test" || chain[1].Model != "minimax-m9-test" {
		t.Fatalf("fallback order = %#v, want droid then generic provider", chain)
	}
}

func TestRewriteFallbackResponseModelPreservesClientID(t *testing.T) {
	payload := []byte(`{"id":"chatcmpl-1","model":"cursor/gpt-9-test-high","usage":{"model":"cursor/gpt-9-test-high"}}`)
	got, upstream := rewriteFallbackResponseModel(payload, "gpt-9-test")
	want := `{"id":"chatcmpl-1","model":"gpt-9-test","usage":{"model":"gpt-9-test"}}`
	if string(got) != want {
		t.Fatalf("rewritten response = %s, want %s", got, want)
	}
	if upstream != "cursor/gpt-9-test-high" {
		t.Fatalf("upstream = %q, want cursor/gpt-9-test-high", upstream)
	}
}

func TestRewriteFallbackStreamFramePreservesClientID(t *testing.T) {
	frame := []byte("data: {\"id\":\"chunk-1\",\"model\":\"droid/minimax-m9-test\"}\n\n")
	got, upstream := rewriteFallbackStreamFrame(frame, "minimax-m9-test")
	want := "data: {\"id\":\"chunk-1\",\"model\":\"minimax-m9-test\"}\n\n"
	if string(got) != want {
		t.Fatalf("rewritten frame = %q, want %q", got, want)
	}
	if upstream != "droid/minimax-m9-test" {
		t.Fatalf("upstream = %q, want droid/minimax-m9-test", upstream)
	}
}
