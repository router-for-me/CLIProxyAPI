package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestV8ExampleLoadsAndRoundTrips(t *testing.T) {
	example, err := os.ReadFile(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	active, err := ParseConfigBytes(example)
	if err != nil {
		t.Fatalf("load active v8 example: %v", err)
	}
	if active.Port != 8317 || len(active.APIKeys) != 3 || active.RequestRetry != 3 || !active.QuotaExceeded.AntigravityCredits {
		t.Fatal("v8 template lost its default configuration")
	}
	if err = ValidateV8Config(example); err != nil {
		t.Fatalf("active template must use the v8 layout: %v", err)
	}
	for _, count := range []int{len(active.GeminiKey), len(active.CodexKey), len(active.ClaudeKey), len(active.VertexCompatAPIKey), len(active.XAIKey), len(active.MetaKey), len(active.InteractionsKey), len(active.OpenAICompatibility)} {
		if count != 0 {
			t.Fatal("placeholder upstream credentials must remain commented")
		}
	}
	if _, changed, errNormalize := NormalizeConfigLayout(example, false); errNormalize != nil || changed {
		t.Fatalf("v8 template unexpectedly needs conflict cleanup: changed=%v error=%v", changed, errNormalize)
	}
	// Validate the provider examples exactly as operators uncomment them.
	text := strings.ReplaceAll(string(example), "\r\n", "\n")
	_, block, found := strings.Cut(text, "# BEGIN API KEY EXAMPLES\n")
	if !found {
		t.Fatal("config.example.yaml is missing the API-key examples")
	}
	block, _, found = strings.Cut(block, "# END API KEY EXAMPLES")
	if !found {
		t.Fatal("config.example.yaml is missing the end of the API-key examples")
	}
	var uncommented strings.Builder
	for _, line := range strings.Split(strings.TrimSuffix(block, "\n"), "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			t.Fatal("placeholder upstream examples must remain commented")
		}
		uncommented.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "#"), " "))
		uncommented.WriteByte('\n')
	}
	data := []byte(text + "\n" + uncommented.String())
	if err = ValidateV8Config(data); err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseConfigBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 8317 || len(cfg.APIKeys) != 3 || len(cfg.GeminiKey) != 3 || len(cfg.CodexKey) != 1 || len(cfg.ClaudeKey) != 2 || len(cfg.VertexCompatAPIKey) != 1 || len(cfg.XAIKey) != 1 || len(cfg.MetaKey) != 1 || len(cfg.InteractionsKey) != 1 || len(cfg.OpenAICompatibility) != 1 {
		t.Fatal("v8 example fields did not reach runtime config")
	}
	if !cfg.QuotaExceeded.AntigravityCredits || cfg.QuotaExceeded.SwitchProject || cfg.QuotaExceeded.SwitchPreviewModel {
		t.Fatal("legacy-only quota examples must remain commented")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = SaveConfigPreserveComments(path, cfg); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	var beforeNode, afterNode yaml.Node
	if err = beforeNode.Encode(cfg); err != nil {
		t.Fatal(err)
	}
	if err = afterNode.Encode(reloaded); err != nil {
		t.Fatal(err)
	}
	if !nodesStructurallyEqual(&beforeNode, &afterNode) {
		t.Fatal("runtime values changed after saving v8 example")
	}
	saved, _ := os.ReadFile(path)
	var node yaml.Node
	if err = yaml.Unmarshal(saved, &node); err != nil {
		t.Fatal(err)
	}
	if groups := yamlPath(node.Content[0], "api-keys.gemini"); groups == nil || len(groups.Content) != 2 || yamlPath(groups.Content[0], "name").Value != "gemini-1" {
		t.Fatal("save lost upstream group identity")
	}
}

func TestV8PresencePrecedenceAndCleanup(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		retry     int
		cooling   bool
		keys      int
		unchanged bool
	}{
		{"legacy", "request-retry: 4\ndisable-cooling: true\napi-keys: [old]\n", 4, true, 1, true},
		{"mixed explicit zero", "request-retry: 4\ndisable-cooling: true\napi-keys: [old]\nrouting:\n  retry: {request-retry: 0}\n  cooldown: {disable-cooling: false}\naccess: {api-keys: []}\n", 0, false, 0, false},
		{"version does not force migration", "config-version: 8\nrequest-retry: 4\ndisable-cooling: true\napi-keys: [old]\n", 4, true, 1, true},
		{"partial new block", "request-retry: 4\ndisable-cooling: true\napi-keys: [old]\nrouting: {retry: {max-retry-credentials: 2}}\n", 4, true, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.RequestRetry != tc.retry || cfg.DisableCooling != tc.cooling || len(cfg.APIKeys) != tc.keys {
				t.Fatalf("unexpected effective config: retry=%d cooling=%v keys=%d", cfg.RequestRetry, cfg.DisableCooling, len(cfg.APIKeys))
			}
			saved, _ := os.ReadFile(path)
			if tc.unchanged && string(saved) != tc.raw {
				t.Fatal("legacy config was rewritten")
			}
			if !tc.unchanged {
				var doc yaml.Node
				_ = yaml.Unmarshal(saved, &doc)
				if yamlPath(doc.Content[0], "request-retry") != nil || legacyPath(doc.Content[0], "api-keys") != nil || yamlPath(doc.Content[0], "disable-cooling") != nil {
					t.Fatal("conflicting legacy fields remain")
				}
			}
		})
	}
}

func TestV8KeyInheritance(t *testing.T) {
	for _, provider := range []string{"gemini", "interactions", "vertex", "codex", "claude", "xai", "meta"} {
		t.Run(provider, func(t *testing.T) {
			raw := "request-retry: 9\napi-keys:\n  " + provider + ":\n" + `    - name: shared
      base-url: https://example.invalid
      priority: 7
      prefix: group
      proxy-url: direct
      headers: {X-Group: yes}
      models: [{name: model, alias: alias}]
      excluded-models: [blocked]
      disable-cooling: true
      request-retry: 3
      keys:
        - api-key: inherited
          priority: null
          headers: null
          disable-cooling: null
          request-retry: null
        - api-key: overridden
          weight: 0
          priority: 0
          prefix: ''
          proxy-url: ''
          headers: {}
          models: []
          excluded-models: []
          disable-cooling: false
          request-retry: 0
        - api-key: global-retry
          request-retry: -1
`
			cfg, err := ParseConfigBytes([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			var effective yaml.Node
			if err = effective.Encode(cfg); err != nil {
				t.Fatal(err)
			}
			var old string
			for _, family := range v8KeyFamilies {
				if family.current == provider {
					old = family.old
				}
			}
			keys := yamlPath(&effective, old)
			if keys == nil || len(keys.Content) != 3 {
				t.Fatal("group did not expand to three keys")
			}
			first, second, third := keys.Content[0], keys.Content[1], keys.Content[2]
			if yamlPath(first, "priority").Value != "7" || yamlPath(first, "request-retry").Value != "3" || yamlPath(first, "disable-cooling").Value != "true" || yamlPath(first, "headers") == nil {
				t.Fatal("null did not inherit")
			}
			if yamlPath(second, "request-retry").Value != "0" || yamlPath(second, "disable-cooling").Value != "false" || yamlPath(second, "weight").Value != "0" || yamlPath(third, "request-retry").Value != "-1" {
				t.Fatal("explicit overrides lost")
			}
			for _, name := range []string{"headers", "models", "excluded-models"} {
				if node := yamlPath(second, name); node != nil && len(node.Content) != 0 {
					t.Fatalf("empty %s inherited group value", name)
				}
			}
		})
	}
}

func TestV8MigrationPreservesLegacySemantics(t *testing.T) {
	raw := []byte(`host: 127.0.0.1
port: 8317
api-keys: [client]
request-retry: 0
ws-auth: false
quota-exceeded: {switch-project: true, switch-preview-model: true, antigravity-credits: true}
codex-api-key:
  - api-key: a
    base-url: https://example.invalid
    headers: {X-Test: first}
    request-retry: 0
    disable-cooling: false
  - api-key: b
    base-url: https://example.invalid
    headers: {X-Test: second}
    request-retry: -1
openai-compatibility:
  - name: compatible
    base-url: https://example.invalid
    api-key-entries: [{api-key: a, weight: 0}, {api-key: b, proxy-url: direct}]
`)
	before, err := ParseConfigBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	migrated, _, err := NormalizeConfigLayout(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateV8Config(migrated); err != nil {
		t.Fatal(err)
	}
	after, err := ParseConfigBytes(migrated)
	if err != nil {
		t.Fatal(err)
	}
	// Scope metadata is intentionally added when fields move under oauth.providers.
	after.OAuthOnlyFields = nil
	if !reflect.DeepEqual(before, after) {
		t.Fatal("migration changed effective config")
	}
	var doc yaml.Node
	_ = yaml.Unmarshal(migrated, &doc)
	groups := yamlPath(doc.Content[0], "api-keys.codex")
	if len(groups.Content) != 2 {
		t.Fatal("different shared settings were combined")
	}
	if !after.QuotaExceeded.SwitchProject || !after.QuotaExceeded.SwitchPreviewModel {
		t.Fatal("legacy-only options lost")
	}
}

func TestV8MigrationPreservesEmptyLegacyContainers(t *testing.T) {
	for _, section := range []configPath{
		{"tls", "server.tls"}, {"remote-management", "management"},
		{"pprof", "observability.pprof"}, {"discovery", "server.discovery"},
		{"discovery.interfaces", "server.discovery.interfaces"},
		{"credential-concurrency", "credentials.concurrency"}, {"credential-in-flight", "credentials.in-flight"},
		{"streaming", "requests.streaming"}, {"payload", "requests.payload"},
		{"codex", "oauth.providers.codex"}, {"codex.live-media-relay", "oauth.providers.codex.live-media-relay"},
		{"codex-header-defaults", "oauth.providers.codex.header-defaults"},
		{"claude", "oauth.providers.claude"}, {"claude-code", "oauth.providers.claude.claude-code"},
		{"claude-header-defaults", "oauth.providers.claude.header-defaults"},
		{"antigravity", "oauth.providers.antigravity"}, {"antigravity.connection-pool", "oauth.providers.antigravity.connection-pool"},
		{"xai", "oauth.providers.xai"}, {"devin", "oauth.providers.devin"},
	} {
		for _, empty := range []string{"{}", "null"} {
			t.Run(section.old+"/"+empty, func(t *testing.T) {
				var doc, value yaml.Node
				if err := yaml.Unmarshal([]byte("port: 8317\nplugins: {configs: {sample: {enabled: false, options: {}}}}\n"), &doc); err != nil {
					t.Fatal(err)
				}
				if err := yaml.Unmarshal([]byte(empty), &value); err != nil {
					t.Fatal(err)
				}
				value.Content[0].LineComment = "Keep this empty block comment"
				setYAMLPath(doc.Content[0], section.old, value.Content[0])
				raw, err := yaml.Marshal(&doc)
				if err != nil {
					t.Fatal(err)
				}
				before, err := ParseConfigBytes(raw)
				if err != nil {
					t.Fatal(err)
				}
				if unchanged, changed, errNormalize := NormalizeConfigLayout(raw, false); errNormalize != nil || changed || string(unchanged) != string(raw) {
					t.Fatalf("legacy-only load changed the file: %v", errNormalize)
				}
				migrated, _, err := NormalizeConfigLayout(raw, true)
				if err != nil {
					t.Fatal(err)
				}
				if err = ValidateV8Config(migrated); err != nil {
					t.Fatalf("migration left an invalid block: %v", err)
				}
				after, err := ParseConfigBytes(migrated)
				if err != nil {
					t.Fatal(err)
				}
				var beforeNode, afterNode yaml.Node
				if err = beforeNode.Encode((*legacyConfig)(before)); err != nil {
					t.Fatal(err)
				}
				if err = afterNode.Encode((*legacyConfig)(after)); err != nil {
					t.Fatal(err)
				}
				if !nodesStructurallyEqual(&beforeNode, &afterNode) {
					t.Fatal("migration changed effective defaults or plugin options")
				}
				if err = yaml.Unmarshal(migrated, &doc); err != nil {
					t.Fatal(err)
				}
				moved := yamlPath(doc.Content[0], section.current)
				if yamlPath(doc.Content[0], section.old) != nil || moved == nil || moved.Kind != yaml.MappingNode || len(moved.Content) != 0 {
					t.Fatal("empty legacy container was not moved to its v8 path")
				}
				if !strings.Contains(string(migrated), "Keep this empty block comment") {
					t.Fatal("migration dropped the empty block comment")
				}
			})
		}
	}
}

func TestV8EmptyLegacyContainersKeepNewValues(t *testing.T) {
	raw := []byte(`port: 8317
tls: null
codex: {identity-confuse: true, live-media-relay: {}}
server: {tls: {enable: true, cert: server.crt, key: server.key}}
oauth: {providers: {codex: {live-media-relay: {max-sessions: 12}}}}
`)
	for _, migrate := range []bool{false, true} {
		data, _, err := NormalizeConfigLayout(raw, migrate)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := ParseConfigBytes(data)
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.TLS.Enable || cfg.TLS.Cert != "server.crt" || cfg.TLS.Key != "server.key" || cfg.Codex.LiveMediaRelay.MaxSessions != 12 || !cfg.Codex.IdentityConfuse {
			t.Fatal("empty legacy block overwrote new values or a non-empty sibling")
		}
		var doc yaml.Node
		if err = yaml.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		if yamlPath(doc.Content[0], "tls") != nil || yamlPath(doc.Content[0], "codex.live-media-relay") != nil {
			t.Fatal("conflicting empty legacy blocks were not removed")
		}
		if !migrate && yamlPath(doc.Content[0], "codex.identity-confuse") == nil {
			t.Fatal("conflict cleanup migrated a non-conflicting legacy sibling")
		}
	}
}

func TestV8PrivateIPAliasPrecedenceAndMigration(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      bool
	}{
		{"allow", "codex: {live-media-relay: {allow-private-remote-ips: true}}\n", false},
		{"deny", "codex: {live-media-relay: {allow-private-remote-ips: false}}\n", true},
		{"new wins", "codex: {live-media-relay: {allow-private-remote-ips: false}}\noauth: {providers: {codex: {live-media-relay: {disable-private-remote-ips: false}}}}\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseConfigBytes([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Codex.LiveMediaRelay.DisablePrivateRemoteIPs != tc.want {
				t.Fatal("wrong private-IP policy")
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err = os.WriteFile(path, []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			if err = SaveConfigPreserveComments(path, cfg, true); err != nil {
				t.Fatal(err)
			}
			after, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if after.Codex.LiveMediaRelay.DisablePrivateRemoteIPs != tc.want {
				t.Fatal("migration inverted the policy")
			}
			data, err := os.ReadFile(path)
			if err != nil || strings.Contains(string(data), "allow-private-remote-ips") {
				t.Fatalf("legacy alias remained: %v", err)
			}
		})
	}
}

func TestV8SaveLegacyAdapterAndManualFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := "config-version: 8\nrouting: {retry: {request-retry: 1}}\noauth: {providers: {aistudio: {ws-auth: true}}}\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RequestRetry, cfg.WebsocketAuth = 0, false
	if err = SaveConfigPreserveComments(path, cfg); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.RequestRetry != 0 || reloaded.WebsocketAuth {
		t.Fatal("legacy API write was shadowed by stale v8 values")
	}
	if err = os.WriteFile(path, []byte("config-version: 8\nrequest-retry: 5\nws-auth: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reloaded, err = LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.RequestRetry != 5 || !reloaded.WebsocketAuth {
		t.Fatal("manual legacy fallback failed")
	}
}

func TestV8RejectsInvalidGroups(t *testing.T) {
	for _, raw := range []string{
		"api-keys: {codex: [{name: a, keys: [{api-key: a, weight: 1.5}]}]}",
		"api-keys: {codex: [{name: a, keys: [{api-key: a, base-url: https://invalid}]}]}",
		"api-keys: {codex: [{name: a, keys: {api-key: a}}]}",
		"server: true", "config-version: 9", "server: {port: bad}",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseConfigBytes([]byte(raw)); err == nil {
				t.Fatal("accepted invalid v8 config")
			}
		})
	}
}

func TestV8ValidationRejectsLegacyWriteLayout(t *testing.T) {
	for _, raw := range []string{
		"debug: true", "server: {port: 8317}\nport: 8318", "api-keys: [client]",
		"codex-api-key: []", "codex: {}", "quota-exceeded: {antigravity-credits: true}",
		"<<: {debug: true}",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseConfigBytes([]byte(raw)); err != nil {
				t.Fatalf("legacy file compatibility failed: %v", err)
			}
			if err := ValidateV8Config([]byte(raw)); err == nil {
				t.Fatal("v8 API accepted the legacy write layout")
			}
		})
	}
}

func TestV8SecretHashUsesNewPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("management: {secret-key: test-secret}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(path)
	if !looksLikeBcrypt(cfg.RemoteManagement.SecretKey) || strings.Contains(string(saved), "test-secret") || strings.Contains(string(saved), "remote-management") {
		t.Fatal("secret was not hashed at the v8 path")
	}
}

func TestV8LegacyClientKeysDoNotOverwriteUpstreamGroups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("api-keys: {codex: [{name: upstream, base-url: 'https://example.invalid', keys: [{api-key: upstream-key}]}]}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.APIKeys = []string{"client-key"}
	if err = SaveConfigPreserveComments(path, cfg); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.APIKeys) != 1 || cfg.APIKeys[0] != "client-key" || len(cfg.CodexKey) != 1 {
		t.Fatal("client/upstream key collision lost credentials")
	}
}

func TestV8AliasesAndMergeKeys(t *testing.T) {
	raw := []byte(`routing: &routing
  strategy: round-robin
  retry: {request-retry: 0}
codex-api-key:
  - &key
    api-key: first
    base-url: https://example.invalid
    request-retry: 0
  - <<: *key
    api-key: second
api-keys:
  gemini:
    - &upstream
      name: first
      base-url: https://example.invalid
      request-retry: 2
      keys: [{api-key: one}]
    - <<: *upstream
      name: second
      keys: [{api-key: two, request-retry: 0}]
`)
	before, err := ParseConfigBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	migrated, _, err := NormalizeConfigLayout(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	after, err := ParseConfigBytes(migrated)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || len(after.CodexKey) != 2 || len(after.GeminiKey) != 2 {
		t.Fatal("alias migration changed effective credentials")
	}
	if *after.GeminiKey[0].RequestRetry != 2 || *after.GeminiKey[1].RequestRetry != 0 {
		t.Fatal("merge override precedence changed")
	}
}

func TestV8LegacyAddsExplicitZeroRetryOverride(t *testing.T) {
	for _, raw := range []string{
		"request-retry: 3\napi-keys: {codex: [{name: upstream, base-url: 'https://example.invalid', keys: [{api-key: upstream-key}]}]}\n",
		"request-retry: 3\ncodex-api-key: [{base-url: 'https://example.invalid', api-key: upstream-key}]\n",
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		cfg.CodexKey[0].RequestRetry = new(0)
		if err = SaveConfigPreserveComments(path, cfg); err != nil {
			t.Fatal(err)
		}
		cfg, err = LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.CodexKey[0].RequestRetry == nil || *cfg.CodexKey[0].RequestRetry != 0 {
			t.Fatal("new zero retry override was discarded")
		}
	}
}

func TestV8LegacyNullRouting(t *testing.T) {
	for _, raw := range []string{
		"port: 8317\nrouting: null\n",
		"port: 8317\nrouting: ~\n",
		"port: 8317\nrouting:\n",
		"port: 8317\nrouting: null\nrequest-retry: 3\n",
		"server: {port: 8317}\nrouting: null\n",
	} {
		t.Run(strings.TrimSpace(raw), func(t *testing.T) {
			cfg, err := ParseConfigBytes([]byte(raw))
			if err != nil {
				t.Fatalf("parse legacy null routing: %v", err)
			}
			if cfg.Port != 8317 || !reflect.DeepEqual(cfg.Routing, RoutingConfig{}) {
				t.Fatal("null routing did not preserve default routing")
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err = os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != raw {
				t.Fatalf("loading legacy null routing rewrote the file: %v", err)
			}
			if err = SaveConfigPreserveComments(path, loaded, true); err != nil {
				t.Fatalf("migrate null routing: %v", err)
			}
			reloaded, err := LoadConfig(path)
			if err != nil || !reflect.DeepEqual(loaded.Routing, reloaded.Routing) || loaded.RequestRetry != reloaded.RequestRetry {
				t.Fatalf("migrating null routing changed effective settings: %v", err)
			}
		})
	}
	for _, raw := range []string{"routing: false", "routing: []", "routing: {retry: false}", "server: null"} {
		if _, err := ParseConfigBytes([]byte(raw)); err == nil {
			t.Errorf("accepted invalid container: %s", raw)
		}
	}
}

func TestV8SecretHashResolvesReferences(t *testing.T) {
	const secret = "test-management-reference-secret"
	for _, tc := range []struct {
		name        string
		raw         string
		path        string
		allowRemote bool
	}{
		{"v8 alias", "defaults: &management\n  secret-key: " + secret + "\n  allow-remote: true\nmanagement: *management\nother: *management\n", "management", true},
		{"v8 merge", "defaults: &management\n  secret-key: " + secret + "\n  allow-remote: true\nmanagement: {<<: *management, allow-remote: false}\nother: *management\n", "management", false},
		{"v8 scalar alias", "password: &password " + secret + "\nmanagement: {secret-key: *password, allow-remote: true}\n", "management", true},
		{"v8 root merge", "defaults: &root\n  management: {secret-key: " + secret + ", allow-remote: true}\n<<: *root\n", "management", true},
		{"v8 wins legacy", "remote-management: {secret-key: stale-secret, allow-remote: false}\ndefaults: &management {secret-key: " + secret + ", allow-remote: true}\nmanagement: *management\n", "management", true},
		{"legacy alias", "defaults: &management\n  secret-key: " + secret + "\n  allow-remote: true\nremote-management: *management\nother: *management\n", "remote-management", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			raw := tc.raw + "# Keep this comment\nport: 8317\n"
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var doc yaml.Node
			if err = yaml.Unmarshal(saved, &doc); err != nil {
				t.Fatal(err)
			}
			effective := expandConfigAliases(doc.Content[0])
			stored := yamlPath(effective, tc.path+".secret-key")
			if stored == nil || stored.Value != cfg.RemoteManagement.SecretKey || !looksLikeBcrypt(stored.Value) {
				t.Fatal("effective management secret was not hashed at the correct path")
			}
			if cfg.RemoteManagement.AllowRemote != tc.allowRemote || cfg.Port != 8317 || !strings.Contains(string(saved), "# Keep this comment") {
				t.Fatal("hashing changed unrelated settings or comments")
			}
			if other := yamlPath(effective, "other.secret-key"); other != nil && other.Value != secret {
				t.Fatal("hashing mutated another use of the shared anchor")
			}
			if tc.path == "management" && yamlPath(effective, "remote-management.secret-key") != nil {
				t.Fatal("hashing left a conflicting legacy secret")
			}
			reloaded, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			second, err := os.ReadFile(path)
			if err != nil || string(second) != string(saved) || reloaded.RemoteManagement.SecretKey != cfg.RemoteManagement.SecretKey || reloaded.RemoteManagement.AllowRemote != tc.allowRemote {
				t.Fatalf("second load changed the persisted hash or settings: %v", err)
			}
		})
	}
}
