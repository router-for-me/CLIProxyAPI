package executor

import (
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"testing"
)

func TestResolveClaudeWirePolicy_RelaxedSystemPromptPrecedence(t *testing.T) {
	enabled := true
	disabled := false
	tests := []struct {
		name        string
		auth        *cliproxyauth.Auth
		cloak       *config.CloakConfig
		wantCloak   bool
		wantStrict  bool
		wantRelaxed bool
	}{
		{name: "default disabled", auth: &cliproxyauth.Auth{}},
		{name: "metadata bool enabled", auth: &cliproxyauth.Auth{Metadata: map[string]any{"cloak_relaxed_system_prompt": true}}, wantCloak: true, wantRelaxed: true},
		{name: "metadata string enabled", auth: &cliproxyauth.Auth{Metadata: map[string]any{"cloak_relaxed_system_prompt": "true"}}, wantCloak: true, wantRelaxed: true},
		{name: "attribute disabled overrides metadata", auth: &cliproxyauth.Auth{Attributes: map[string]string{"cloak_relaxed_system_prompt": "false"}, Metadata: map[string]any{"cloak_relaxed_system_prompt": true}}, wantCloak: true},
		{name: "key config overrides metadata", auth: &cliproxyauth.Auth{Metadata: map[string]any{"cloak_relaxed_system_prompt": false}}, cloak: &config.CloakConfig{RelaxedSystemPrompt: &enabled}, wantCloak: true, wantRelaxed: true},
		{name: "key config explicit false", auth: &cliproxyauth.Auth{Metadata: map[string]any{"cloak_relaxed_system_prompt": true}}, cloak: &config.CloakConfig{RelaxedSystemPrompt: &disabled}, wantCloak: true},
		{name: "key strict mode wins", auth: &cliproxyauth.Auth{Metadata: map[string]any{"cloak_relaxed_system_prompt": true}}, cloak: &config.CloakConfig{StrictMode: true}, wantCloak: true, wantStrict: true},
		{name: "boolean metadata strict mode wins", auth: &cliproxyauth.Auth{Metadata: map[string]any{"cloak_strict_mode": true, "cloak_relaxed_system_prompt": true}}, wantCloak: true, wantStrict: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{}
			if test.cloak != nil {
				test.auth.Attributes = map[string]string{"api_key": "key-123"}
				cfg.ClaudeKey = []config.ClaudeKey{{APIKey: "key-123", Cloak: test.cloak}}
			}
			policy, settings := resolveClaudeWirePolicy(cfg, test.auth, "key-123", false)
			if policy.Cloak != test.wantCloak {
				t.Fatalf("Cloak = %v, want %v", policy.Cloak, test.wantCloak)
			}
			if settings.strictMode != test.wantStrict {
				t.Fatalf("strictMode = %v, want %v", settings.strictMode, test.wantStrict)
			}
			if settings.relaxedSystemPrompt != test.wantRelaxed {
				t.Fatalf("relaxedSystemPrompt = %v, want %v", settings.relaxedSystemPrompt, test.wantRelaxed)
			}
		})
	}
}

func TestResolveClaudeWirePolicy_RelaxedSystemPromptUsesConfigIdentity(t *testing.T) {
	disabled := false
	enabled := true
	baseURL := "https://shared-relay.example"
	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{
		{
			BaseURL: baseURL,
			Headers: map[string]string{"X-Relay-Account": "first"},
			Cloak:   &config.CloakConfig{RelaxedSystemPrompt: &disabled},
		},
		{
			BaseURL: baseURL,
			Headers: map[string]string{"X-Relay-Account": "second"},
			Cloak:   &config.CloakConfig{RelaxedSystemPrompt: &enabled},
		},
	}}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"source":                 "config:claude[second]",
		"config_index":           "1",
		"base_url":               baseURL,
		"header:X-Relay-Account": "second",
	}}

	policy, settings := resolveClaudeWirePolicy(cfg, auth, "", false)
	if !policy.Cloak {
		t.Fatal("Cloak = false, want true for the selected base-url-only config entry")
	}
	if !settings.relaxedSystemPrompt {
		t.Fatal("relaxedSystemPrompt = false, want the config_index-selected value true")
	}
}

func TestResolveClaudeKeyConfigRejectsStaleConfigIndex(t *testing.T) {
	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{
		{
			APIKey:   "shared-key",
			BaseURL:  "https://shared.example",
			Prefix:   "current",
			ProxyURL: "https://current-proxy.example",
		},
		{
			APIKey:   "shared-key",
			BaseURL:  "https://shared.example",
			Prefix:   "other",
			ProxyURL: "https://other-proxy.example",
		},
	}}
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"source":       "config:claude[stale]",
			"config_index": "1",
			"api_key":      "shared-key",
			"base_url":     "https://shared.example",
		},
		Prefix:   "current",
		ProxyURL: "https://current-proxy.example",
	}

	if got := resolveClaudeKeyConfig(cfg, auth); got != &cfg.ClaudeKey[0] {
		t.Fatalf("resolveClaudeKeyConfig() = %p, want current credential %p", got, &cfg.ClaudeKey[0])
	}
}

func TestResolveClaudeKeyConfigRejectsStaleConfigIndexWithDifferentHeaders(t *testing.T) {
	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{
		{
			APIKey:  "shared-key",
			BaseURL: "https://shared.example",
			Headers: map[string]string{"X-Relay-Account": "current"},
		},
		{
			APIKey:  "shared-key",
			BaseURL: "https://shared.example",
			Headers: map[string]string{"X-Relay-Account": "other"},
		},
	}}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"source":                 "config:claude[stale]",
		"config_index":           "1",
		"api_key":                "shared-key",
		"base_url":               "https://shared.example",
		"header:X-Relay-Account": "current",
	}}

	if got := resolveClaudeKeyConfig(cfg, auth); got != &cfg.ClaudeKey[0] {
		t.Fatalf("resolveClaudeKeyConfig() = %p, want current credential %p", got, &cfg.ClaudeKey[0])
	}
}

func TestResolveClaudeKeyConfigDoesNotFallbackAcrossExplicitBaseURL(t *testing.T) {
	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{{
		APIKey:  "shared-key",
		BaseURL: "https://configured.example",
	}}}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "shared-key",
		"base_url": "https://auth.example",
	}}

	if got := resolveClaudeKeyConfig(cfg, auth); got != nil {
		t.Fatalf("resolveClaudeKeyConfig() = %p, want nil for an explicit base URL mismatch", got)
	}
}

func TestResolveClaudeKeyConfigMatchesHeaderNamesAcrossReload(t *testing.T) {
	for _, test := range []struct {
		name       string
		authName   string
		configName string
	}{
		{name: "lowercase auth", authName: "x-relay-account", configName: "X-Relay-Account"},
		{name: "lowercase config", authName: "X-Relay-Account", configName: "x-relay-account"},
		{name: "mixed case", authName: "x-ReLaY-aCcOuNt", configName: "X-rElAy-AcCoUnT"},
	} {
		for _, configIndex := range []string{"0", "1"} {
			t.Run(test.name+"/index="+configIndex, func(t *testing.T) {
				cfg := &config.Config{ClaudeKey: []config.ClaudeKey{
					{
						APIKey:  "shared-key",
						BaseURL: "https://shared.example",
						Headers: map[string]string{test.configName: "SECOND"},
					},
					{
						APIKey:  "shared-key",
						BaseURL: "https://shared.example",
						Headers: map[string]string{test.configName: "second"},
					},
				}}
				auth := &cliproxyauth.Auth{Attributes: map[string]string{
					"source":                  "config:claude[reloaded]",
					"config_index":            configIndex,
					"api_key":                 "shared-key",
					"base_url":                "https://shared.example",
					"header:" + test.authName: "second",
				}}

				if got := resolveClaudeKeyConfig(cfg, auth); got != &cfg.ClaudeKey[1] {
					t.Fatalf("resolveClaudeKeyConfig() = %p, want matching header value credential %p", got, &cfg.ClaudeKey[1])
				}
				if got := cfg.ClaudeKey[1].Headers[test.configName]; got != "second" {
					t.Fatalf("configuration header changed: got %q, want second", got)
				}
			})
		}
	}
}

func TestResolveClaudeKeyConfigKeepsKeylessCredentialsSeparate(t *testing.T) {
	for _, configIndex := range []string{"", "0", "1"} {
		t.Run("config index="+configIndex, func(t *testing.T) {
			cfg := &config.Config{ClaudeKey: []config.ClaudeKey{
				{APIKey: "shared-key", BaseURL: "https://shared.example"},
				{BaseURL: "https://shared.example"},
			}}
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"source":       "config:claude[keyless]",
				"config_index": configIndex,
				"base_url":     "https://shared.example",
			}}
			if got := resolveClaudeKeyConfig(cfg, auth); got != &cfg.ClaudeKey[1] {
				t.Fatalf("resolveClaudeKeyConfig() = %p, want keyless credential %p", got, &cfg.ClaudeKey[1])
			}
			cfg.ClaudeKey = cfg.ClaudeKey[:1]
			if got := resolveClaudeKeyConfig(cfg, auth); got != nil {
				t.Fatalf("resolveClaudeKeyConfig() = %p, want nil without a matching keyless entry", got)
			}
		})
	}
}

func TestResolveClaudeKeyConfigPreservesCompleteIdentityAcrossReload(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*config.ClaudeKey)
	}{
		{name: "API key casing", change: func(entry *config.ClaudeKey) { entry.APIKey = "SHARED-key" }},
		{name: "endpoint path casing", change: func(entry *config.ClaudeKey) { entry.BaseURL = "https://shared.example/Relay" }},
		{name: "prefix casing", change: func(entry *config.ClaudeKey) { entry.Prefix = "Route" }},
		{name: "proxy password casing", change: func(entry *config.ClaudeKey) { entry.ProxyURL = "https://user:Secret@proxy.example" }},
		{name: "header value casing", change: func(entry *config.ClaudeKey) { entry.Headers = map[string]string{"X-Relay-Account": "SECOND"} }},
	} {
		for _, configIndex := range []string{"", "0", "1"} {
			t.Run(test.name+"/index="+configIndex, func(t *testing.T) {
				entry := config.ClaudeKey{
					APIKey: "shared-key", BaseURL: "https://shared.example/relay",
					Prefix: "route", ProxyURL: "https://user:secret@proxy.example",
					Headers: map[string]string{"X-Relay-Account": "second"},
				}
				cfg := &config.Config{ClaudeKey: []config.ClaudeKey{entry, entry}}
				test.change(&cfg.ClaudeKey[0])
				auth := &cliproxyauth.Auth{
					Prefix: "route", ProxyURL: "https://user:secret@proxy.example",
					Attributes: map[string]string{
						"source": "config:claude[reloaded]", "config_index": configIndex,
						"api_key": "shared-key", "base_url": "https://shared.example/relay",
						"header:X-Relay-Account": "second",
					},
				}
				if got := resolveClaudeKeyConfig(cfg, auth); got != &cfg.ClaudeKey[1] {
					t.Fatalf("resolveClaudeKeyConfig() = %p, want original credential %p", got, &cfg.ClaudeKey[1])
				}
				cfg.ClaudeKey = cfg.ClaudeKey[:1]
				if got := resolveClaudeKeyConfig(cfg, auth); got != nil {
					t.Fatalf("resolveClaudeKeyConfig() = %p, want nil after the original identity was removed", got)
				}
			})
		}
	}
}

func TestResolveClaudeKeyConfigLimitsLegacyFallbackToUnscopedAuths(t *testing.T) {
	for _, test := range []struct {
		name    string
		source  string
		baseURL string
		prefix  string
		proxy   string
		header  string
		noKey   bool
		want    bool
	}{
		{name: "legacy key-only", want: true},
		{name: "legacy explicit matching endpoint", baseURL: "https://gateway.example", want: true},
		{name: "legacy explicit other endpoint", baseURL: "https://other.example"},
		{name: "config default endpoint stays distinct", source: "config:claude[current]"},
		{name: "config empty identity stays distinct", source: "config:claude[current]", baseURL: "https://gateway.example"},
		{name: "file explicit prefix", source: "file:claude", baseURL: "https://gateway.example", prefix: "other"},
		{name: "file explicit proxy", source: "file:claude", baseURL: "https://gateway.example", proxy: "https://other-proxy.example"},
		{name: "file explicit headers", source: "file:claude", baseURL: "https://gateway.example", header: "other"},
		{name: "file partial identity", source: "file:claude", baseURL: "https://gateway.example", prefix: "route"},
		{name: "file complete identity", source: "file:claude", baseURL: "https://gateway.example", prefix: "route", proxy: "https://proxy.example", header: "second", want: true},
		{name: "no key or endpoint", noKey: true},
		{name: "keyless cannot use keyed entry", baseURL: "https://gateway.example", noKey: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{ClaudeKey: []config.ClaudeKey{{
				APIKey: "shared-key", BaseURL: "https://gateway.example",
				Prefix: "route", ProxyURL: "https://proxy.example",
				Headers: map[string]string{"X-Relay-Account": "second"},
			}}}
			auth := &cliproxyauth.Auth{
				Prefix: test.prefix, ProxyURL: test.proxy,
				Attributes: map[string]string{
					"source": test.source, "config_index": "0", "base_url": test.baseURL,
				},
			}
			if !test.noKey {
				auth.Attributes["api_key"] = "shared-key"
			}
			if test.header != "" {
				auth.Attributes["header:X-Relay-Account"] = test.header
			}
			got := resolveClaudeKeyConfig(cfg, auth)
			if (got == &cfg.ClaudeKey[0]) != test.want {
				t.Fatalf("resolveClaudeKeyConfig() = %p, want matching entry: %t", got, test.want)
			}
		})
	}
}

func TestResolveClaudeWirePolicy_RelaxedHonorsSharedUpstreamDefaults(t *testing.T) {
	for _, test := range []struct {
		name      string
		cloak     string
		attrMode  string
		wantCloak bool
		wantRelax bool
	}{
		{name: "shared disable", cloak: "relaxed-system-prompt: true", wantRelax: true},
		{name: "key enables cloak", cloak: "mode: auto, relaxed-system-prompt: true", wantCloak: true, wantRelax: true},
		{name: "key disables relaxed", cloak: "mode: auto, relaxed-system-prompt: false", wantCloak: true},
		{name: "strict wins", cloak: "mode: auto, strict-mode: true, relaxed-system-prompt: true", wantCloak: true},
		{name: "metadata enables cloak", cloak: "relaxed-system-prompt: true", attrMode: "always", wantCloak: true, wantRelax: true},
		{name: "key overrides metadata mode", cloak: "mode: never, relaxed-system-prompt: true", attrMode: "always", wantRelax: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, errConfig := config.ParseConfigBytes([]byte(fmt.Sprintf(`upstream:
  claude:
    disable-claude-cloak-mode: true
    header-defaults: {timezone: Asia/Singapore}
api-keys:
  claude:
    - name: primary
      keys:
        - api-key: shared-default-key
          fingerprint-profile: claude-code-cli
          cloak: {%s}
`, test.cloak)))
			if errConfig != nil {
				t.Fatal(errConfig)
			}
			auth := &cliproxyauth.Auth{
				Attributes: map[string]string{"api_key": "shared-default-key", "cloak_mode": test.attrMode},
				Metadata:   map[string]any{"cloak_relaxed_system_prompt": true},
			}
			for _, snapshot := range []*config.Config{cfg, cfg.CloneForRuntime()} {
				if scoped := snapshot.ForAPIKey(); !scoped.DisableClaudeCloakMode || scoped.ClaudeHeaderDefaults.Timezone != "Asia/Singapore" {
					t.Fatal("shared Claude defaults must remain available to API-key credentials")
				}
				policy, settings := resolveClaudeWirePolicy(snapshot, auth, "shared-default-key", false)
				if policy.Cloak != test.wantCloak || settings.relaxedSystemPrompt != test.wantRelax {
					t.Fatalf("cloak/relaxed = %t/%t, want %t/%t", policy.Cloak, settings.relaxedSystemPrompt, test.wantCloak, test.wantRelax)
				}
				if native, _ := resolveClaudeWirePolicy(snapshot, auth, "shared-default-key", true); native.Cloak {
					t.Fatal("confirmed native Claude Code must remain a passthrough client")
				}
			}
		})
	}
}

func TestResolveClaudeKeyConfigPreservesLegacyRuntimeEndpoint(t *testing.T) {
	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{{APIKey: "legacy-key"}}}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "legacy-key", "base_url": "https://runtime.example"}}
	if got := resolveClaudeKeyConfig(cfg, auth); got != &cfg.ClaudeKey[0] {
		t.Fatal("legacy runtime endpoint must match a key with no configured endpoint")
	}
	auth.Attributes["api_key"] = "LEGACY-KEY"
	if got := resolveClaudeKeyConfig(cfg, auth); got != &cfg.ClaudeKey[0] {
		t.Fatal("legacy unscoped key matching must retain its existing comparison")
	}
	auth.Attributes["api_key"] = "legacy-key"
	auth.Attributes["source"] = "config:claude[current]"
	if got := resolveClaudeKeyConfig(cfg, auth); got != nil {
		t.Fatal("config credentials must match their complete endpoint identity")
	}
	delete(auth.Attributes, "source")
	cfg.ClaudeKey[0].BaseURL = "https://other.example"
	if got := resolveClaudeKeyConfig(cfg, auth); got != nil {
		t.Fatal("different explicit endpoints must not match")
	}
}
