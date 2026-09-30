package upstreamsync

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// recordingProviderStore is a minimal in-memory UpstreamProviderStore that
// captures rows produced by SeedFromArtifacts so a test can feed them back
// through the renderer without a database.
type recordingProviderStore struct {
	rows []store.UpstreamProvider
}

func (r *recordingProviderStore) List(ctx context.Context) ([]store.UpstreamProvider, error) {
	return r.rows, nil
}

func (r *recordingProviderStore) Get(ctx context.Context, id int64) (*store.UpstreamProvider, error) {
	for i := range r.rows {
		if r.rows[i].ID == id {
			p := r.rows[i]
			return &p, nil
		}
	}
	return nil, nil
}

func (r *recordingProviderStore) Create(ctx context.Context, p store.UpstreamProvider) (*store.UpstreamProvider, error) {
	r.rows = append(r.rows, p)
	return &p, nil
}

func (r *recordingProviderStore) Update(ctx context.Context, p store.UpstreamProvider) (*store.UpstreamProvider, error) {
	return &p, nil
}

func (r *recordingProviderStore) Delete(ctx context.Context, id int64) error {
	return nil
}

func (r *recordingProviderStore) Count(ctx context.Context) (int64, error) {
	return int64(len(r.rows)), nil
}

func (r *recordingProviderStore) SetEntryAutoDisabled(ctx context.Context, entryID int64, code string) (bool, error) {
	return false, nil
}

func (r *recordingProviderStore) ReenableExpiredAutoDisabled(ctx context.Context) (int64, error) {
	return 0, nil
}

// TestNeuralwattAPIKeySeedRenderRoundTrip pins that a neuralwatt-api-key row
// seeds from config.NeuralwattKey (CodexKey alias) via providerFromCodexKey
// with the neuralwatt provider type and renders back into cfg.NeuralwattKey
// via RenderConfigWithPools — the same one-item-to-one-row round trip the
// meta-api-key path guarantees. The service-tier field is Neuralwatt-specific
// and must survive the round trip.
func TestNeuralwattAPIKeySeedRenderRoundTrip(t *testing.T) {
	cfg := &config.Config{NeuralwattKey: []config.NeuralwattKey{{
		APIKey:         "neuralwatt-key",
		BaseURL:        "https://api.neuralwatt.com/v1",
		ServiceTier:    "flex",
		Prefix:         "nw/",
		Priority:       3,
		Websockets:     false,
		ProxyURL:       "http://proxy.example",
		ExcludedModels: []string{"neuralwatt-old"},
		Models: []config.CodexModel{
			{Name: "neuralwatt-pro", Alias: "nw-flash"},
		},
	}}}

	st := &recordingProviderStore{}
	if err := SeedFromArtifacts(context.Background(), st, cfg, ""); err != nil {
		t.Fatalf("SeedFromArtifacts() error = %v", err)
	}
	if len(st.rows) != 1 {
		t.Fatalf("seeded %d provider rows, want 1", len(st.rows))
	}
	if st.rows[0].ProviderType != TypeNeuralwattAPIKey {
		t.Fatalf("seeded provider type = %q, want %q", st.rows[0].ProviderType, TypeNeuralwattAPIKey)
	}

	rendered := RenderConfigWithPools(st.rows, nil)
	if len(rendered.NeuralwattKey) != 1 {
		t.Fatalf("rendered %d neuralwatt keys, want 1", len(rendered.NeuralwattKey))
	}
	got := rendered.NeuralwattKey[0]
	if got.APIKey != "neuralwatt-key" || got.BaseURL != "https://api.neuralwatt.com/v1" {
		t.Fatalf("rendered neuralwatt key = %+v", got)
	}
	if got.ServiceTier != "flex" {
		t.Fatalf("rendered service-tier = %q, want flex", got.ServiceTier)
	}
	if got.Prefix != "nw/" || got.Priority != 3 {
		t.Fatalf("rendered prefix/priority = %q/%d, want nw//3", got.Prefix, got.Priority)
	}
	if got.ProxyURL != "http://proxy.example" {
		t.Fatalf("rendered proxy URL = %q", got.ProxyURL)
	}
	if len(got.Models) != 1 || got.Models[0].Name != "neuralwatt-pro" || got.Models[0].Alias != "nw-flash" {
		t.Fatalf("rendered models = %+v", got.Models)
	}
	if len(got.ExcludedModels) != 1 || got.ExcludedModels[0] != "neuralwatt-old" {
		t.Fatalf("rendered excluded models = %+v", got.ExcludedModels)
	}
}
