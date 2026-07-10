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

func TestParseRegistryNormalizesPluginLocales(t *testing.T) {
	t.Parallel()

	registry, errParse := ParseRegistry([]byte(`{
		"schema_version": 1,
		"plugins": [{
			"id": "sample-provider",
			"name": "Sample Provider",
			"description": "Adds sample provider support.",
			"author": "author-name",
			"repository": "https://github.com/author-name/cliproxy-sample-provider-plugin",
			"locales": {
				" zh-CN ": {
					"name": " 示例插件 ",
					"description": " 增加示例提供商支持。 ",
					"author": " 作者 ",
					"tags": [" 工具 "]
				}
			}
		}]
	}`))
	if errParse != nil {
		t.Fatalf("ParseRegistry() error = %v", errParse)
	}
	plugin, ok := registry.PluginByID("sample-provider")
	if !ok {
		t.Fatal("PluginByID(sample-provider) missing")
	}
	localized, ok := plugin.Locales["zh-cn"]
	if !ok {
		t.Fatalf("plugin locales = %#v, want zh-cn", plugin.Locales)
	}
	if localized.Name != "示例插件" || localized.Description != "增加示例提供商支持。" || localized.Author != "作者" || localized.Tags[0] != "工具" {
		t.Fatalf("localized plugin = %#v, want trimmed locale fields", localized)
	}
}

func TestParseRegistryNormalizesDuplicatePluginLocaleKeysDeterministically(t *testing.T) {
	t.Parallel()

	registry, errParse := ParseRegistry([]byte(`{
		"schema_version": 1,
		"plugins": [{
			"id": "sample-provider",
			"name": "Sample Provider",
			"description": "Adds sample provider support.",
			"author": "author-name",
			"repository": "https://github.com/author-name/cliproxy-sample-provider-plugin",
			"locales": {
				"ZH-cn": {"name": "uppercase"},
				"zh-cn": {"name": "lower"},
				"zh-CN": {"name": "canonical"}
			}
		}]
	}`))
	if errParse != nil {
		t.Fatalf("ParseRegistry() error = %v", errParse)
	}
	plugin, ok := registry.PluginByID("sample-provider")
	if !ok {
		t.Fatal("PluginByID(sample-provider) missing")
	}
	if len(plugin.Locales) != 1 || plugin.Locales["zh-cn"].Name != "canonical" {
		t.Fatalf("plugin locales = %#v, want deterministic canonical value", plugin.Locales)
	}
}

func TestParseRegistryCanonicalPluginLocaleScoreSubtags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		locales   string
		localeKey string
		wantName  string
	}{
		{
			name:      "numeric region",
			locales:   `"en-419": {"name": "latin america"}, "en_419": {"name": "underscore"}`,
			localeKey: "en-419",
			wantName:  "latin america",
		},
		{
			name:      "script subtag",
			locales:   `"zh-Hant": {"name": "traditional"}, "zh-hant": {"name": "lower script"}`,
			localeKey: "zh-hant",
			wantName:  "traditional",
		},
		{
			name:      "alpha three region",
			locales:   `"en-USA": {"name": "upper region"}, "en-usa": {"name": "lower region"}`,
			localeKey: "en-usa",
			wantName:  "upper region",
		},
		{
			name:      "variant subtag",
			locales:   `"sl-rozaj": {"name": "lower variant"}, "sl-ROZAJ": {"name": "upper variant"}`,
			localeKey: "sl-rozaj",
			wantName:  "lower variant",
		},
		{
			name:      "private subtag",
			locales:   `"en-x-private": {"name": "lower private"}, "en-X-private": {"name": "upper private"}`,
			localeKey: "en-x-private",
			wantName:  "lower private",
		},
		{
			name:      "dash beats underscore",
			locales:   `"zh-CN": {"name": "dash"}, "zh_CN": {"name": "underscore"}`,
			localeKey: "zh-cn",
			wantName:  "dash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registry, errParse := ParseRegistry([]byte(`{
				"schema_version": 1,
				"plugins": [{
					"id": "sample-provider",
					"name": "Sample Provider",
					"description": "Adds sample provider support.",
					"author": "author-name",
					"repository": "https://github.com/author-name/cliproxy-sample-provider-plugin",
					"locales": {` + tt.locales + `}
				}]
			}`))
			if errParse != nil {
				t.Fatalf("ParseRegistry() error = %v", errParse)
			}
			plugin, ok := registry.PluginByID("sample-provider")
			if !ok {
				t.Fatal("PluginByID(sample-provider) missing")
			}
			if len(plugin.Locales) != 1 || plugin.Locales[tt.localeKey].Name != tt.wantName {
				t.Fatalf("plugin locales = %#v, want %s => %q", plugin.Locales, tt.localeKey, tt.wantName)
			}
		})
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

func TestParseRegistrySupportsDirectInstall(t *testing.T) {
	t.Parallel()

	registry, errParse := ParseRegistry([]byte(`{
		"schema_version": 2,
		"plugins": [{
			"id": "sample-provider",
			"name": "Sample Provider",
			"description": "Adds sample provider support.",
			"author": "author-name",
			"version": "0.2.0",
			"auth_required": true,
			"install": {
				"type": "direct",
				"artifacts": [{
					"goos": "windows",
					"goarch": "x64",
					"url": "https://downloads.example/sample-provider.zip",
					"sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
				}]
			},
			"versions": [{
				"version": "0.1.0",
				"install": {
					"type": "direct",
					"artifacts": [{
						"goos": "linux",
						"goarch": "aarch64",
						"url": "https://downloads.example/sample-provider-0.1.0.zip",
						"sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
					}]
				}
			}]
		}]
	}`))
	if errParse != nil {
		t.Fatalf("ParseRegistry() error = %v", errParse)
	}
	plugin, ok := registry.PluginByID("sample-provider")
	if !ok {
		t.Fatal("PluginByID(sample-provider) missing")
	}
	if PluginInstallType(plugin) != InstallTypeDirect {
		t.Fatalf("install type = %q, want direct", PluginInstallType(plugin))
	}
	if !plugin.AuthRequired {
		t.Fatal("AuthRequired = false, want true")
	}
	if len(plugin.Versions) != 1 || plugin.Versions[0].Version != "0.1.0" {
		t.Fatalf("versions = %#v, want normalized 0.1.0 entry", plugin.Versions)
	}
	platforms := PluginPlatforms(plugin)
	if len(platforms) != 2 ||
		platforms[0].GOOS != "windows" || platforms[0].GOARCH != "amd64" ||
		platforms[1].GOOS != "linux" || platforms[1].GOARCH != "arm64" {
		t.Fatalf("platforms = %#v, want normalized windows/amd64 and linux/arm64", platforms)
	}
	artifacts := PluginArtifacts(plugin)
	if len(artifacts) != 2 ||
		artifacts[0].GOOS != "windows" || artifacts[0].GOARCH != "amd64" ||
		artifacts[1].GOOS != "linux" || artifacts[1].GOARCH != "arm64" {
		t.Fatalf("artifacts = %#v, want normalized top-level and version artifacts", artifacts)
	}
}

func TestValidateRegistryRejectsInvalidDirectInstall(t *testing.T) {
	t.Parallel()

	registry := Registry{SchemaVersion: SchemaVersionV2, Plugins: []Plugin{{
		ID:          "sample-provider",
		Name:        "Sample Provider",
		Description: "Adds sample provider support.",
		Author:      "author-name",
		Version:     "0.2.0",
		Install: InstallPlan{
			Type: InstallTypeDirect,
			Artifacts: []Artifact{{
				GOOS:   "linux",
				GOARCH: "amd64",
				URL:    "https://downloads.example/sample.zip?token=secret",
				SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			}},
		},
	}}}
	errValidate := ValidateRegistry(registry)
	if errValidate == nil {
		t.Fatal("ValidateRegistry() error = nil")
	}
	if !strings.Contains(errValidate.Error(), "sensitive query") {
		t.Fatalf("ValidateRegistry() error = %v, want sensitive query", errValidate)
	}
}

func TestValidateRegistryRejectsDirectInstallInSchemaV1(t *testing.T) {
	t.Parallel()

	registry := Registry{SchemaVersion: SchemaVersion, Plugins: []Plugin{{
		ID:          "sample-provider",
		Name:        "Sample Provider",
		Description: "Adds sample provider support.",
		Author:      "author-name",
		Version:     "0.2.0",
		Install: InstallPlan{
			Type: InstallTypeDirect,
			Artifacts: []Artifact{{
				GOOS:   "linux",
				GOARCH: "amd64",
				URL:    "https://downloads.example/sample.zip",
				SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			}},
		},
	}}}
	errValidate := ValidateRegistry(registry)
	if errValidate == nil {
		t.Fatal("ValidateRegistry() error = nil")
	}
	if !strings.Contains(errValidate.Error(), "schema_version 2") {
		t.Fatalf("ValidateRegistry() error = %v, want schema_version 2", errValidate)
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
				registry.SchemaVersion = 3
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

func TestNormalizeSourcesAppendsURLsToDefaultSource(t *testing.T) {
	t.Parallel()

	sources, errNormalize := NormalizeSources([]string{" https://community.example/registry.json "})
	if errNormalize != nil {
		t.Fatalf("NormalizeSources() error = %v", errNormalize)
	}
	if len(sources) != 2 {
		t.Fatalf("sources len = %d, want 2", len(sources))
	}
	if sources[0].ID != DefaultSourceID || sources[0].URL != DefaultRegistryURL {
		t.Fatalf("default source = %#v", sources[0])
	}
	if sources[1].ID != SourceID("https://community.example/registry.json") ||
		sources[1].Name != "community.example" ||
		sources[1].URL != "https://community.example/registry.json" {
		t.Fatalf("third-party source = %#v", sources[1])
	}
}

func TestNormalizeSourcesSkipsDuplicates(t *testing.T) {
	t.Parallel()

	sources, errNormalize := NormalizeSources([]string{
		DefaultRegistryURL,
		"https://community.example/registry.json",
		"https://community.example/registry.json",
	})
	if errNormalize != nil {
		t.Fatalf("NormalizeSources() error = %v", errNormalize)
	}
	if len(sources) != 2 {
		t.Fatalf("sources len = %d, want 2: %#v", len(sources), sources)
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
