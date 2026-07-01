package config

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"gopkg.in/yaml.v3"
)

func TestCloneForRuntimeNil(t *testing.T) {
	var cfg *Config
	if got := cfg.CloneForRuntime(); got != nil {
		t.Fatalf("CloneForRuntime() = %#v, want nil", got)
	}
}

func TestCloneForRuntimeDeepCopiesConfig(t *testing.T) {
	cfg := sampleCloneRuntimeConfig()

	clone := cfg.CloneForRuntime()
	if clone == nil {
		t.Fatal("CloneForRuntime() = nil")
	}
	if clone == cfg {
		t.Fatal("CloneForRuntime() returned original pointer")
	}

	mutateOriginalConfig(cfg)

	if clone.Home.Host != "home.local" {
		t.Fatalf("clone.Home.Host = %q, want home.local", clone.Home.Host)
	}
	if clone.APIKeys[0] != "client-key" {
		t.Fatalf("clone.APIKeys[0] = %q, want client-key", clone.APIKeys[0])
	}
	if clone.OAuthExcludedModels["codex"][0] != "hidden-model" {
		t.Fatalf("clone.OAuthExcludedModels[codex][0] = %q, want hidden-model", clone.OAuthExcludedModels["codex"][0])
	}
	if clone.OAuthModelAlias["codex"][0].Alias != "client-model" {
		t.Fatalf("clone.OAuthModelAlias[codex][0].Alias = %q, want client-model", clone.OAuthModelAlias["codex"][0].Alias)
	}
	if got := pluginRawScalar(t, clone.Plugins.Configs["sample"].Raw, "mode"); got != "first" {
		t.Fatalf("clone plugin raw mode = %q, want first", got)
	}
	if clone.OpenAICompatibility[0].Models[0].Thinking.Levels[0] != "low" {
		t.Fatalf("clone thinking level = %q, want low", clone.OpenAICompatibility[0].Models[0].Thinking.Levels[0])
	}
	if got := clone.Payload.Default[0].Params["object"].(map[string]any)["key"]; got != "value" {
		t.Fatalf("clone payload object key = %#v, want value", got)
	}

	clone.APIKeys[0] = "clone-client-key"
	clone.OAuthExcludedModels["codex"][0] = "clone-hidden-model"
	clone.OAuthModelAlias["codex"][0].Alias = "clone-client-model"
	clone.OpenAICompatibility[0].Models[0].Thinking.Levels[0] = "clone-low"
	clone.Payload.Default[0].Params["object"].(map[string]any)["key"] = "clone-value"
	plugin := clone.Plugins.Configs["sample"]
	setPluginRawScalar(t, &plugin.Raw, "mode", "third")
	clone.Plugins.Configs["sample"] = plugin

	if cfg.APIKeys[0] != "mutated-client-key" {
		t.Fatalf("cfg.APIKeys[0] = %q, want mutated-client-key", cfg.APIKeys[0])
	}
	if cfg.OAuthExcludedModels["codex"][0] != "mutated-hidden-model" {
		t.Fatalf("cfg.OAuthExcludedModels[codex][0] = %q, want mutated-hidden-model", cfg.OAuthExcludedModels["codex"][0])
	}
	if cfg.OAuthModelAlias["codex"][0].Alias != "mutated-client-model" {
		t.Fatalf("cfg.OAuthModelAlias[codex][0].Alias = %q, want mutated-client-model", cfg.OAuthModelAlias["codex"][0].Alias)
	}
	if got := pluginRawScalar(t, cfg.Plugins.Configs["sample"].Raw, "mode"); got != "second" {
		t.Fatalf("cfg plugin raw mode = %q, want second", got)
	}
	if cfg.OpenAICompatibility[0].Models[0].Thinking.Levels[0] != "mutated-low" {
		t.Fatalf("cfg thinking level = %q, want mutated-low", cfg.OpenAICompatibility[0].Models[0].Thinking.Levels[0])
	}
	if got := cfg.Payload.Default[0].Params["object"].(map[string]any)["key"]; got != "mutated-value" {
		t.Fatalf("cfg payload object key = %#v, want mutated-value", got)
	}
}

func TestCloneForRuntimeDoesNotShareReferenceFields(t *testing.T) {
	cfg := sampleCloneRuntimeConfig()
	clone := cfg.CloneForRuntime()

	assertNoSharedRuntimeReferences(t, reflect.ValueOf(cfg), reflect.ValueOf(clone), "Config")
}

func sampleCloneRuntimeConfig() *Config {
	cacheStrict := true
	bypassStrict := false
	pluginEnabled := false
	cacheUserID := true

	return &Config{
		SDKConfig: SDKConfig{
			APIKeys: []string{"client-key"},
			Streaming: StreamingConfig{
				KeepAliveSeconds: 3,
				BootstrapRetries: 2,
			},
		},
		Home: HomeConfig{
			Enabled: true,
			Host:    "home.local",
			Port:    8081,
			TLS: HomeTLSConfig{
				Enable:              true,
				ServerName:          "home.local",
				CACert:              "ca",
				ClientCert:          "cert",
				ClientKey:           "key",
				UseTargetServerName: true,
			},
		},
		Plugins: PluginsConfig{
			Enabled:      true,
			Dir:          "plugins",
			StoreSources: []string{"https://plugins.example/store.json"},
			Configs: map[string]PluginInstanceConfig{
				"sample": {
					Enabled:  &pluginEnabled,
					Priority: 10,
					Raw:      samplePluginRawNode("first"),
				},
			},
		},
		AntigravitySignatureCacheEnabled: &cacheStrict,
		AntigravitySignatureBypassStrict: &bypassStrict,
		GeminiKey: []GeminiKey{{
			APIKey:         "gemini-key",
			Models:         []GeminiModel{{Name: "gemini-upstream", Alias: "gemini-upstream-alias"}},
			Headers:        map[string]string{"X-Gemini": "one"},
			ExcludedModels: []string{"gemini-hidden"},
		}},
		CodexKey: []CodexKey{{
			APIKey:         "codex-key",
			Models:         []CodexModel{{Name: "codex-upstream", Alias: "codex-client"}},
			Headers:        map[string]string{"X-Codex": "one"},
			ExcludedModels: []string{"codex-hidden-key"},
		}},
		ClaudeKey: []ClaudeKey{{
			APIKey:         "claude-key",
			Models:         []ClaudeModel{{Name: "claude-upstream", Alias: "claude-client"}},
			Headers:        map[string]string{"X-Claude": "one"},
			ExcludedModels: []string{"claude-hidden"},
			Cloak: &CloakConfig{
				SensitiveWords: []string{"secret"},
				CacheUserID:    &cacheUserID,
			},
		}},
		OpenAICompatibility: []OpenAICompatibility{{
			Name:          "compat",
			APIKeyEntries: []OpenAICompatibilityAPIKey{{APIKey: "compat-key", ProxyURL: "http://proxy.local"}},
			Models: []OpenAICompatibilityModel{{
				Name:     "compat-upstream",
				Alias:    "compat-client",
				Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high"}},
			}},
			Headers: map[string]string{"X-Compat": "one"},
		}},
		VertexCompatAPIKey: []VertexCompatKey{{
			APIKey:         "vertex-key",
			Headers:        map[string]string{"X-Vertex": "one"},
			Models:         []VertexCompatModel{{Name: "vertex-upstream", Alias: "vertex-client"}},
			ExcludedModels: []string{"vertex-hidden"},
		}},
		OAuthExcludedModels: map[string][]string{
			"codex": {"hidden-model"},
		},
		OAuthModelAlias: map[string][]OAuthModelAlias{
			"codex": {{Name: "upstream-model", Alias: "client-model", Fork: true}},
		},
		Payload: PayloadConfig{
			Default: []PayloadRule{{
				Models: []PayloadModelRule{{
					Name:    "model-*",
					Headers: map[string]string{"X-Tier": "gold"},
					Match:   []map[string]any{{"tier": "gold"}},
					Exist:   []string{"$.messages"},
				}},
				Params: map[string]any{
					"object": map[string]any{"key": "value"},
					"array":  []any{"first", map[string]any{"nested": "value"}},
				},
			}},
			Filter: []PayloadFilterRule{{
				Models: []PayloadModelRule{{Name: "model-*"}},
				Params: []string{"$.secret"},
			}},
		},
	}
}

func mutateOriginalConfig(cfg *Config) {
	cfg.Home.Host = "mutated-home.local"
	cfg.APIKeys[0] = "mutated-client-key"
	cfg.OAuthExcludedModels["codex"][0] = "mutated-hidden-model"
	cfg.OAuthModelAlias["codex"][0].Alias = "mutated-client-model"
	cfg.OpenAICompatibility[0].Models[0].Thinking.Levels[0] = "mutated-low"
	cfg.Payload.Default[0].Params["object"].(map[string]any)["key"] = "mutated-value"
	plugin := cfg.Plugins.Configs["sample"]
	setPluginRawScalar(nil, &plugin.Raw, "mode", "second")
	cfg.Plugins.Configs["sample"] = plugin
}

func samplePluginRawNode(mode string) yaml.Node {
	modeValue := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: mode, Anchor: "modeAnchor"}
	return yaml.Node{
		Kind: yaml.MappingNode,
		Tag:  "!!map",
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "enabled"},
			{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "mode"},
			modeValue,
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "mode-alias"},
			{Kind: yaml.AliasNode, Alias: modeValue},
		},
	}
}

func pluginRawScalar(t *testing.T, node yaml.Node, key string) string {
	t.Helper()
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i] != nil && node.Content[i].Value == key && node.Content[i+1] != nil {
			return node.Content[i+1].Value
		}
	}
	t.Fatalf("raw plugin node missing key %q", key)
	return ""
}

func setPluginRawScalar(t *testing.T, node *yaml.Node, key, value string) {
	if t != nil {
		t.Helper()
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i] != nil && node.Content[i].Value == key && node.Content[i+1] != nil {
			node.Content[i+1].Value = value
			return
		}
	}
	if t != nil {
		t.Fatalf("raw plugin node missing key %q", key)
	}
}

func assertNoSharedRuntimeReferences(t *testing.T, original, clone reflect.Value, path string) {
	t.Helper()
	if !original.IsValid() || !clone.IsValid() {
		return
	}
	if original.Kind() == reflect.Interface {
		if original.IsNil() || clone.IsNil() {
			return
		}
		assertNoSharedRuntimeReferences(t, original.Elem(), clone.Elem(), path)
		return
	}
	if original.Kind() != clone.Kind() {
		t.Fatalf("%s kind mismatch: %s != %s", path, original.Kind(), clone.Kind())
	}

	switch original.Kind() {
	case reflect.Pointer:
		if original.IsNil() || clone.IsNil() {
			return
		}
		if original.Pointer() == clone.Pointer() {
			t.Fatalf("%s shares pointer %x", path, original.Pointer())
		}
		assertNoSharedRuntimeReferences(t, original.Elem(), clone.Elem(), path+"->"+original.Type().Elem().String())
	case reflect.Map:
		if original.IsNil() || clone.IsNil() {
			return
		}
		if original.Pointer() == clone.Pointer() {
			t.Fatalf("%s shares map pointer %x", path, original.Pointer())
		}
		iter := original.MapRange()
		for iter.Next() {
			key := iter.Key()
			assertNoSharedRuntimeReferences(t, iter.Value(), clone.MapIndex(key), path+"["+keyForPath(key)+"]")
		}
	case reflect.Slice:
		if original.IsNil() || clone.IsNil() {
			return
		}
		if original.Pointer() == clone.Pointer() {
			t.Fatalf("%s shares slice pointer %x", path, original.Pointer())
		}
		for i := 0; i < original.Len(); i++ {
			assertNoSharedRuntimeReferences(t, original.Index(i), clone.Index(i), path+"[]")
		}
	case reflect.Struct:
		for i := 0; i < original.NumField(); i++ {
			field := original.Type().Field(i)
			assertNoSharedRuntimeReferences(t, original.Field(i), clone.Field(i), path+"."+field.Name)
		}
	}
}

func keyForPath(key reflect.Value) string {
	if key.Kind() == reflect.String {
		return key.String()
	}
	return key.Type().String()
}
