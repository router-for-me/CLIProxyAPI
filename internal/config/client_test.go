package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestClientCodexEnableApplyPatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want bool
	}{
		{"omitted", "config-version: 8\n", false},
		{"disabled", "client: {codex: {enable-apply-patch: false}}\n", false},
		{"enabled", "client: {codex: {enable-apply-patch: true}}\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if errValidate := ValidateV8Config([]byte(tc.raw)); errValidate != nil {
				t.Fatalf("validate: %v", errValidate)
			}
			cfg, errParse := ParseConfigBytes([]byte(tc.raw))
			if errParse != nil {
				t.Fatalf("parse: %v", errParse)
			}
			if cfg.Client.Codex.EnableApplyPatch != tc.want {
				t.Fatalf("EnableApplyPatch = %t, want %t", cfg.Client.Codex.EnableApplyPatch, tc.want)
			}
			if cfg.OAuthOnlyFields["client.codex.enable-apply-patch"] {
				t.Fatal("client setting must not be OAuth-only")
			}
			var sdkCfg SDKConfig
			if errUnmarshal := yaml.Unmarshal([]byte(tc.raw), &sdkCfg); errUnmarshal != nil {
				t.Fatalf("decode SDK config: %v", errUnmarshal)
			}
			if sdkCfg.Client.Codex.EnableApplyPatch != tc.want {
				t.Fatalf("SDK EnableApplyPatch = %t, want %t", sdkCfg.Client.Codex.EnableApplyPatch, tc.want)
			}
			data, errMarshal := json.Marshal(cfg)
			if errMarshal != nil {
				t.Fatalf("encode JSON: %v", errMarshal)
			}
			var decoded Config
			if errUnmarshal := json.Unmarshal(data, &decoded); errUnmarshal != nil {
				t.Fatalf("decode JSON: %v", errUnmarshal)
			}
			if decoded.Client.Codex.EnableApplyPatch != tc.want {
				t.Fatal("JSON round trip changed client setting")
			}
			migrated, _, errNormalize := NormalizeConfigLayout([]byte(tc.raw), true)
			if errNormalize != nil {
				t.Fatalf("normalize: %v", errNormalize)
			}
			if errValidate := ValidateV8Config(migrated); errValidate != nil {
				t.Fatalf("validate normalized config: %v", errValidate)
			}
			if errUnmarshal := yaml.Unmarshal(migrated, &decoded); errUnmarshal != nil {
				t.Fatalf("decode normalized config: %v", errUnmarshal)
			}
			if decoded.Client.Codex.EnableApplyPatch != tc.want {
				t.Fatal("normalization changed client setting")
			}
		})
	}
	for _, raw := range []string{
		"client: {codex: {enable-apply-patch: invalid}}\n",
		"client: {codex: {unknown: true}}\n",
	} {
		if errValidate := ValidateV8Config([]byte(raw)); errValidate == nil {
			t.Fatalf("accepted invalid config %q", raw)
		}
	}
}

func TestClientCodexEnableApplyPatchSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte("config-version: 8\n"), 0600); errWrite != nil {
		t.Fatalf("write: %v", errWrite)
	}
	cfg := &Config{}
	for _, enabled := range []bool{true, false} {
		cfg.Client.Codex.EnableApplyPatch = enabled
		if errSave := SaveConfigPreserveComments(path, cfg, true); errSave != nil {
			t.Fatalf("save: %v", errSave)
		}
		data, errRead := os.ReadFile(path)
		if errRead != nil {
			t.Fatalf("read: %v", errRead)
		}
		if errValidate := ValidateV8Config(data); errValidate != nil {
			t.Fatalf("validate saved config: %v", errValidate)
		}
		loaded, errLoad := LoadConfig(path)
		if errLoad != nil {
			t.Fatalf("load: %v", errLoad)
		}
		if loaded.Client.Codex.EnableApplyPatch != enabled {
			t.Fatalf("saved EnableApplyPatch = %t, want %t", loaded.Client.Codex.EnableApplyPatch, enabled)
		}
	}
}

func TestClientCodexWebSearchResultCache(t *testing.T) {
	for _, tc := range []struct {
		name      string
		raw       string
		want      CodexWebSearchResultCacheConfig
		wantTTL   time.Duration
		wantLimit int
	}{
		{
			name:      "omitted",
			raw:       "config-version: 8\n",
			want:      CodexWebSearchResultCacheConfig{},
			wantTTL:   DefaultCodexWebSearchResultCacheTTL,
			wantLimit: DefaultCodexWebSearchResultCacheLimit,
		},
		{
			name:      "enabled with integer seconds ttl",
			raw:       "client: {codex: {web-search-result-cache: {enabled: true, ttl: 1800, limit: 64}}}\n",
			want:      CodexWebSearchResultCacheConfig{Enabled: true, TTL: "1800", Limit: 64},
			wantTTL:   30 * time.Minute,
			wantLimit: 64,
		},
		{
			name:      "enabled with 30m ttl",
			raw:       "client: {codex: {web-search-result-cache: {enabled: true, ttl: 30m, limit: 128}}}\n",
			want:      CodexWebSearchResultCacheConfig{Enabled: true, TTL: "30m", Limit: 128},
			wantTTL:   30 * time.Minute,
			wantLimit: 128,
		},
		{
			name:      "disabled with 24h ttl",
			raw:       "client: {codex: {web-search-result-cache: {enabled: false, ttl: 24h, limit: 1024}}}\n",
			want:      CodexWebSearchResultCacheConfig{Enabled: false, TTL: "24h", Limit: 1024},
			wantTTL:   24 * time.Hour,
			wantLimit: 1024,
		},
		{
			name:      "null fields fall back to defaults",
			raw:       "client: {codex: {web-search-result-cache: {enabled: null, ttl: null, limit: null}}}\n",
			want:      CodexWebSearchResultCacheConfig{},
			wantTTL:   DefaultCodexWebSearchResultCacheTTL,
			wantLimit: DefaultCodexWebSearchResultCacheLimit,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if errValidate := ValidateV8Config([]byte(tc.raw)); errValidate != nil {
				t.Fatalf("validate: %v", errValidate)
			}
			cfg, errParse := ParseConfigBytes([]byte(tc.raw))
			if errParse != nil {
				t.Fatalf("parse: %v", errParse)
			}
			got := cfg.Client.Codex.WebSearchResultCache
			if got != tc.want {
				t.Fatalf("WebSearchResultCache = %+v, want %+v", got, tc.want)
			}
			if got.EffectiveTTL() != tc.wantTTL {
				t.Fatalf("EffectiveTTL() = %v, want %v", got.EffectiveTTL(), tc.wantTTL)
			}
			if got.EffectiveLimit() != tc.wantLimit {
				t.Fatalf("EffectiveLimit() = %d, want %d", got.EffectiveLimit(), tc.wantLimit)
			}
			var sdkCfg SDKConfig
			if errUnmarshal := yaml.Unmarshal([]byte(tc.raw), &sdkCfg); errUnmarshal != nil {
				t.Fatalf("decode SDK config: %v", errUnmarshal)
			}
			if sdkCfg.Client.Codex.WebSearchResultCache != tc.want {
				t.Fatalf("SDK WebSearchResultCache = %+v, want %+v", sdkCfg.Client.Codex.WebSearchResultCache, tc.want)
			}
			data, errMarshal := json.Marshal(cfg)
			if errMarshal != nil {
				t.Fatalf("encode JSON: %v", errMarshal)
			}
			var decoded Config
			if errUnmarshal := json.Unmarshal(data, &decoded); errUnmarshal != nil {
				t.Fatalf("decode JSON: %v", errUnmarshal)
			}
			if decoded.Client.Codex.WebSearchResultCache != tc.want {
				t.Fatalf("JSON round trip WebSearchResultCache = %+v, want %+v", decoded.Client.Codex.WebSearchResultCache, tc.want)
			}
			migrated, _, errNormalize := NormalizeConfigLayout([]byte(tc.raw), true)
			if errNormalize != nil {
				t.Fatalf("normalize: %v", errNormalize)
			}
			if errValidate := ValidateV8Config(migrated); errValidate != nil {
				t.Fatalf("validate normalized config: %v", errValidate)
			}
			if errUnmarshal := yaml.Unmarshal(migrated, &decoded); errUnmarshal != nil {
				t.Fatalf("decode normalized config: %v", errUnmarshal)
			}
			if decoded.Client.Codex.WebSearchResultCache != tc.want {
				t.Fatalf("normalized WebSearchResultCache = %+v, want %+v", decoded.Client.Codex.WebSearchResultCache, tc.want)
			}
		})
	}

	var fromJSONNumber Config
	if errUnmarshal := json.Unmarshal([]byte(`{"client":{"codex":{"web-search-result-cache":{"enabled":true,"ttl":1800,"limit":256}}}}`), &fromJSONNumber); errUnmarshal != nil {
		t.Fatalf("unmarshal JSON number ttl: %v", errUnmarshal)
	}
	if fromJSONNumber.Client.Codex.WebSearchResultCache.EffectiveTTL() != 30*time.Minute || fromJSONNumber.Client.Codex.WebSearchResultCache.EffectiveLimit() != 256 {
		t.Fatalf("JSON number ttl config = %+v", fromJSONNumber.Client.Codex.WebSearchResultCache)
	}

	for _, raw := range []string{
		"client: {codex: {web-search-result-cache: false}}\n",
		"client: {codex: {web-search-result-cache: {unknown: true}}}\n",
		"client: {codex: {web-search-result-cache: {enabled: invalid}}}\n",
		"client: {codex: {web-search-result-cache: {ttl: invalid}}}\n",
		"client: {codex: {web-search-result-cache: {ttl: -30m}}}\n",
		"client: {codex: {web-search-result-cache: {ttl: -10}}}\n",
		"client: {codex: {web-search-result-cache: {ttl: true}}}\n",
		"client: {codex: {web-search-result-cache: {limit: -1}}}\n",
		"client: {codex: {web-search-result-cache: {limit: invalid}}}\n",
	} {
		if errValidate := ValidateV8Config([]byte(raw)); errValidate == nil {
			t.Fatalf("accepted invalid config %q", raw)
		}
		if _, errParse := ParseConfigBytes([]byte(raw)); errParse == nil {
			t.Fatalf("ParseConfigBytes accepted invalid config %q", raw)
		}
	}
}

func TestClientCodexWebSearchResultCacheSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte("config-version: 8\n"), 0600); errWrite != nil {
		t.Fatalf("write: %v", errWrite)
	}
	cfg := &Config{}
	for _, want := range []CodexWebSearchResultCacheConfig{
		{Enabled: true, TTL: "30m", Limit: 128},
		{Enabled: false, TTL: "24h", Limit: 1024},
		{Enabled: false, TTL: "", Limit: 0},
	} {
		cfg.Client.Codex.WebSearchResultCache = want
		if errSave := SaveConfigPreserveComments(path, cfg, true); errSave != nil {
			t.Fatalf("save: %v", errSave)
		}
		data, errRead := os.ReadFile(path)
		if errRead != nil {
			t.Fatalf("read: %v", errRead)
		}
		if errValidate := ValidateV8Config(data); errValidate != nil {
			t.Fatalf("validate saved config: %v", errValidate)
		}
		loaded, errLoad := LoadConfig(path)
		if errLoad != nil {
			t.Fatalf("load: %v", errLoad)
		}
		if loaded.Client.Codex.WebSearchResultCache != want {
			t.Fatalf("saved WebSearchResultCache = %+v, want %+v", loaded.Client.Codex.WebSearchResultCache, want)
		}
	}
}
