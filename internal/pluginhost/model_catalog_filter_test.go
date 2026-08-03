package pluginhost

import (
	"context"
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type modelCatalogFilterFunc func(context.Context, pluginapi.ModelCatalogFilterRequest) (pluginapi.ModelCatalogFilterResponse, error)

func (fn modelCatalogFilterFunc) FilterModelCatalog(ctx context.Context, req pluginapi.ModelCatalogFilterRequest) (pluginapi.ModelCatalogFilterResponse, error) {
	return fn(ctx, req)
}

func TestHostChainsModelCatalogFilters(t *testing.T) {
	host := newHostWithRecords(
		capabilityRecord{
			id:       "second",
			priority: 1,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ModelCatalogFilter: modelCatalogFilterFunc(func(_ context.Context, req pluginapi.ModelCatalogFilterRequest) (pluginapi.ModelCatalogFilterResponse, error) {
					if len(req.Models) != 2 {
						t.Fatalf("second filter received %d models, want 2", len(req.Models))
					}
					return pluginapi.ModelCatalogFilterResponse{Handled: true, Models: req.Models[:1]}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "first",
			priority: 10,
			meta:     pluginapi.Metadata{Name: "First"},
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ModelCatalogFilter: modelCatalogFilterFunc(func(_ context.Context, req pluginapi.ModelCatalogFilterRequest) (pluginapi.ModelCatalogFilterResponse, error) {
					if req.PluginID != "first" || req.Plugin.Name != "First" {
						t.Fatalf("plugin context = %q %#v", req.PluginID, req.Plugin)
					}
					return pluginapi.ModelCatalogFilterResponse{Handled: true, Models: req.Models[:2]}, nil
				}),
			}},
		},
	)

	resp, err := host.FilterModelCatalog(context.Background(), pluginapi.ModelCatalogFilterRequest{
		Models: []map[string]any{{"id": "a"}, {"id": "b"}, {"id": "c"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Handled || len(resp.Models) != 1 || resp.Models[0]["id"] != "a" {
		t.Fatalf("FilterModelCatalog() = %#v", resp)
	}
}

func TestHostModelCatalogFilterFailsClosed(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "broken",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ModelCatalogFilter: modelCatalogFilterFunc(func(context.Context, pluginapi.ModelCatalogFilterRequest) (pluginapi.ModelCatalogFilterResponse, error) {
				return pluginapi.ModelCatalogFilterResponse{}, errors.New("policy unavailable")
			}),
		}},
	})

	if _, err := host.FilterModelCatalog(context.Background(), pluginapi.ModelCatalogFilterRequest{
		Models: []map[string]any{{"id": "secret"}},
	}); err == nil {
		t.Fatal("filter failure must not fall back to the unfiltered catalog")
	}
}
