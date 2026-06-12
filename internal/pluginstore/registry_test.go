package pluginstore

import (
	"strings"
	"testing"
)

func TestParseRegistryValidatesRegistry(t *testing.T) {
	t.Parallel()

	registry, errParse := ParseRegistry([]byte(`{
		"schema_version": 1,
		"plugins": [{
			"id": "sample-provider",
			"name": "Sample Provider",
			"description": "Adds sample provider support.",
			"author": "author-name",
			"version": "0.1.0",
			"repository": "https://github.com/author-name/cliproxy-sample-provider-plugin",
			"logo": "https://example.com/logo.png",
			"homepage": "https://github.com/author-name/cliproxy-sample-provider-plugin",
			"license": "MIT",
			"tags": ["provider"]
		}]
	}`))
	if errParse != nil {
		t.Fatalf("ParseRegistry() error = %v", errParse)
	}
	plugin, ok := registry.PluginByID("sample-provider")
	if !ok {
		t.Fatal("PluginByID(sample-provider) missing")
	}
	if plugin.Version != "0.1.0" {
		t.Fatalf("plugin version = %q, want 0.1.0", plugin.Version)
	}
}

func TestParseRegistryNormalizesPluginFields(t *testing.T) {
	t.Parallel()

	registry, errParse := ParseRegistry([]byte(`{
		"schema_version": 1,
		"plugins": [{
			"id": " sample-provider ",
			"name": " Sample Provider ",
			"description": " Adds sample provider support. ",
			"author": " author-name ",
			"version": " 0.1.0 ",
			"repository": " https://github.com/author-name/cliproxy-sample-provider-plugin ",
			"logo": " https://example.com/logo.png ",
			"homepage": " https://github.com/author-name/cliproxy-sample-provider-plugin ",
			"license": " MIT ",
			"tags": [" provider "]
		}]
	}`))
	if errParse != nil {
		t.Fatalf("ParseRegistry() error = %v", errParse)
	}
	plugin, ok := registry.PluginByID("sample-provider")
	if !ok {
		t.Fatal("PluginByID(sample-provider) missing")
	}
	if plugin.ID != "sample-provider" || plugin.Version != "0.1.0" || plugin.Repository != "https://github.com/author-name/cliproxy-sample-provider-plugin" {
		t.Fatalf("plugin not normalized: %#v", plugin)
	}
	if plugin.Name != "Sample Provider" || plugin.Tags[0] != "provider" {
		t.Fatalf("plugin display fields not normalized: %#v", plugin)
	}
}

func TestValidateRegistryAllowsMissingVersion(t *testing.T) {
	t.Parallel()

	registry := Registry{SchemaVersion: 1, Plugins: []Plugin{{
		ID:          "sample-provider",
		Name:        "Sample Provider",
		Description: "Adds sample provider support.",
		Author:      "author-name",
		Repository:  "https://github.com/author-name/cliproxy-sample-provider-plugin",
	}}}
	if errValidate := ValidateRegistry(registry); errValidate != nil {
		t.Fatalf("ValidateRegistry() error = %v, want nil for missing version", errValidate)
	}
}

func TestValidateRegistryRejectsInvalidEntries(t *testing.T) {
	t.Parallel()

	valid := Plugin{
		ID:          "sample-provider",
		Name:        "Sample Provider",
		Description: "Adds sample provider support.",
		Author:      "author-name",
		Version:     "0.1.0",
		Repository:  "https://github.com/author-name/cliproxy-sample-provider-plugin",
	}
	tests := []struct {
		name    string
		mutate  func(*Registry)
		wantErr string
	}{
		{
			name: "schema version",
			mutate: func(registry *Registry) {
				registry.SchemaVersion = 2
			},
			wantErr: "unsupported schema_version",
		},
		{
			name: "missing required field",
			mutate: func(registry *Registry) {
				registry.Plugins[0].Name = ""
			},
			wantErr: "missing required field name",
		},
		{
			name: "duplicate id",
			mutate: func(registry *Registry) {
				registry.Plugins = append(registry.Plugins, valid)
			},
			wantErr: "duplicate plugin id",
		},
		{
			name: "invalid id",
			mutate: func(registry *Registry) {
				registry.Plugins[0].ID = "../sample-provider"
			},
			wantErr: "invalid plugin id",
		},
		{
			name: "v-prefixed version",
			mutate: func(registry *Registry) {
				registry.Plugins[0].Version = "v0.1.0"
			},
			wantErr: "invalid plugin version",
		},
		{
			name: "invalid repository",
			mutate: func(registry *Registry) {
				registry.Plugins[0].Repository = "https://example.com/author/repo"
			},
			wantErr: "repository must be",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registry := Registry{SchemaVersion: 1, Plugins: []Plugin{valid}}
			tt.mutate(&registry)
			errValidate := ValidateRegistry(registry)
			if errValidate == nil {
				t.Fatal("ValidateRegistry() error = nil")
			}
			if !strings.Contains(errValidate.Error(), tt.wantErr) {
				t.Fatalf("ValidateRegistry() error = %v, want substring %q", errValidate, tt.wantErr)
			}
		})
	}
}

func TestGitHubRepositoryPartsRejectsNonRepositoryURLs(t *testing.T) {
	t.Parallel()

	tests := []string{
		"http://github.com/owner/repo",
		"https://github.com/owner",
		"https://github.com/owner/repo/issues",
		"https://github.com/owner/repo.git",
		"https://github.com/owner/repo?tab=readme",
	}
	for _, repository := range tests {
		t.Run(repository, func(t *testing.T) {
			t.Parallel()

			if _, _, errParse := GitHubRepositoryParts(repository); errParse == nil {
				t.Fatalf("GitHubRepositoryParts(%q) error = nil", repository)
			}
		})
	}
}
