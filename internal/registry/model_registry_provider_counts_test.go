package registry

import "testing"

func TestGetModelProviderCountsGroupsRegisteredClientsByProvider(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("claude-a", "claude", []*ModelInfo{{ID: "shared-model", OwnedBy: "anthropic"}})
	r.RegisterClient("claude-b", "claude", []*ModelInfo{{ID: "shared-model", OwnedBy: "anthropic"}})
	r.RegisterClient("devin-a", "devin", []*ModelInfo{
		{ID: "shared-model", OwnedBy: "anthropic"},
		{ID: "devin-only", OwnedBy: "devin"},
	})
	r.RegisterClient("compat-a", "openai-compatible-acme", []*ModelInfo{{ID: "compat-model", OwnedBy: "openai-compatible-acme"}})

	got := r.GetModelProviderCounts()

	want := []ModelProviderCounts{
		{ID: "compat-model", OwnedBy: "openai-compatible-acme", Providers: []ProviderClientCount{{Provider: "openai-compatible-acme", Clients: 1}}},
		{ID: "devin-only", OwnedBy: "devin", Providers: []ProviderClientCount{{Provider: "devin", Clients: 1}}},
		{ID: "shared-model", OwnedBy: "anthropic", Providers: []ProviderClientCount{
			{Provider: "claude", Clients: 2},
			{Provider: "devin", Clients: 1},
		}},
	}
	if len(got) != len(want) {
		t.Fatalf("models = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].OwnedBy != want[i].OwnedBy {
			t.Fatalf("model[%d] = %+v, want %+v", i, got[i], want[i])
		}
		if len(got[i].Providers) != len(want[i].Providers) {
			t.Fatalf("model[%d] providers = %+v, want %+v", i, got[i].Providers, want[i].Providers)
		}
		for j := range want[i].Providers {
			if got[i].Providers[j] != want[i].Providers[j] {
				t.Fatalf("model[%d] provider[%d] = %+v, want %+v", i, j, got[i].Providers[j], want[i].Providers[j])
			}
		}
	}
}

func TestGetModelProviderCountsTracksUnregister(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("claude-a", "claude", []*ModelInfo{{ID: "shared-model"}})
	r.RegisterClient("devin-a", "devin", []*ModelInfo{{ID: "shared-model"}})
	r.UnregisterClient("devin-a")

	got := r.GetModelProviderCounts()
	if len(got) != 1 || len(got[0].Providers) != 1 || got[0].Providers[0] != (ProviderClientCount{Provider: "claude", Clients: 1}) {
		t.Fatalf("after unregister = %+v, want shared-model served by claude only", got)
	}

	r.UnregisterClient("claude-a")
	if got = r.GetModelProviderCounts(); len(got) != 0 {
		t.Fatalf("after unregistering all clients = %+v, want empty", got)
	}
}

func TestGetModelProviderCountsIgnoresQuotaAndSuspension(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("claude-a", "claude", []*ModelInfo{{ID: "shared-model"}})
	r.RegisterClient("claude-b", "claude", []*ModelInfo{{ID: "shared-model"}})
	r.SetModelQuotaExceeded("claude-a", "shared-model")
	r.SuspendClientModel("claude-b", "shared-model", "test")

	got := r.GetModelProviderCounts()
	if len(got) != 1 || len(got[0].Providers) != 1 || got[0].Providers[0] != (ProviderClientCount{Provider: "claude", Clients: 2}) {
		t.Fatalf("counts = %+v, want claude with 2 registered clients", got)
	}
}

func TestGetModelProviderCountsCountsAliasPoolClientOnce(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("compat-a", "openai-compatible-acme", []*ModelInfo{
		{ID: "pool-model"}, {ID: "pool-model"}, {ID: "pool-model"},
	})
	r.RegisterClient("claude-a", "claude", []*ModelInfo{{ID: "pool-model"}})
	r.RegisterClient("claude-b", "claude", []*ModelInfo{{ID: "pool-model"}})

	got := r.GetModelProviderCounts()
	want := []ProviderClientCount{
		{Provider: "claude", Clients: 2},
		{Provider: "openai-compatible-acme", Clients: 1},
	}
	if len(got) != 1 || len(got[0].Providers) != len(want) {
		t.Fatalf("counts = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[0].Providers[i] != want[i] {
			t.Fatalf("provider[%d] = %+v, want %+v", i, got[0].Providers[i], want[i])
		}
	}
}

func TestGetModelProviderCountsReturnsCopy(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("claude-a", "claude", []*ModelInfo{{ID: "shared-model"}})

	got := r.GetModelProviderCounts()
	got[0].Providers[0].Clients = 99

	again := r.GetModelProviderCounts()
	if again[0].Providers[0].Clients != 1 {
		t.Fatalf("registry state changed through returned slice: %+v", again)
	}
}
