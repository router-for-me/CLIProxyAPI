package diff

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestBuildConfigChangeDetails(t *testing.T) {
	oldCfg := &config.Config{
		Port:    8080,
		AuthDir: "/tmp/auth-old",
		GeminiKey: []config.GeminiKey{
			{APIKey: "old", BaseURL: "http://old", ExcludedModels: []string{"old-model"}},
		},
		AmpCode: config.AmpCode{
			UpstreamURL:                   "http://old-upstream",
			ModelMappings:                 []config.AmpModelMapping{{From: "from-old", To: "to-old"}},
			RestrictManagementToLocalhost: false,
		},
		RemoteManagement: config.RemoteManagement{
			AllowRemote:           false,
			SecretKey:             "old",
			DisableControlPanel:   false,
			PanelGitHubRepository: "repo-old",
		},
		OAuthExcludedModels: map[string][]string{
			"providerA": {"m1"},
		},
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name: "compat-a",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "k1"},
				},
				Models: []config.OpenAICompatibilityModel{{Name: "m1"}},
			},
		},
	}

	newCfg := &config.Config{
		Port:    9090,
		AuthDir: "/tmp/auth-new",
		GeminiKey: []config.GeminiKey{
			{APIKey: "old", BaseURL: "http://old", ExcludedModels: []string{"old-model", "extra"}},
		},
		AmpCode: config.AmpCode{
			UpstreamURL:                   "http://new-upstream",
			RestrictManagementToLocalhost: true,
			ModelMappings: []config.AmpModelMapping{
				{From: "from-old", To: "to-old"},
				{From: "from-new", To: "to-new"},
			},
		},
		RemoteManagement: config.RemoteManagement{
			AllowRemote:           true,
			SecretKey:             "new",
			DisableControlPanel:   true,
			PanelGitHubRepository: "repo-new",
		},
		OAuthExcludedModels: map[string][]string{
			"providerA": {"m1", "m2"},
			"providerB": {"x"},
		},
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name: "compat-a",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "k1"},
				},
				Models: []config.OpenAICompatibilityModel{{Name: "m1"}, {Name: "m2"}},
			},
			{
				Name: "compat-b",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "k2"},
				},
			},
		},
	}

	details := BuildConfigChangeDetails(oldCfg, newCfg)

	expectContains(t, details, "port: 8080 -> 9090")
	expectContains(t, details, "auth-dir: /tmp/auth-old -> /tmp/auth-new")
	expectContains(t, details, "gemini[0].excluded-models: updated (1 -> 2 entries)")
	expectContains(t, details, "ampcode.upstream-url: http://old-upstream -> http://new-upstream")
	expectContains(t, details, "ampcode.model-mappings: updated (1 -> 2 entries)")
	expectContains(t, details, "remote-management.allow-remote: false -> true")
	expectContains(t, details, "remote-management.secret-key: updated")
	expectContains(t, details, "oauth-excluded-models[providera]: updated (1 -> 2 entries)")
	expectContains(t, details, "oauth-excluded-models[providerb]: added (1 entries)")
	expectContains(t, details, "openai-compatibility:")
	expectContains(t, details, "  provider added: compat-b (api-keys=1, models=0)")
	expectContains(t, details, "  provider updated: compat-a (models 1 -> 2)")
}

func TestBuildConfigChangeDetails_NoChanges(t *testing.T) {
	cfg := &config.Config{
		Port: 8080,
	}
	if details := BuildConfigChangeDetails(cfg, cfg); len(details) != 0 {
		t.Fatalf("expected no change entries, got %v", details)
	}
}

func TestBuildConfigChangeDetails_GeminiVertexHeadersAndForceMappings(t *testing.T) {
	oldCfg := &config.Config{
		GeminiKey: []config.GeminiKey{
			{APIKey: "g1", Headers: map[string]string{"H": "1"}, ExcludedModels: []string{"a"}},
		},
		VertexCompatAPIKey: []config.VertexCompatKey{
			{APIKey: "v1", BaseURL: "http://v-old", Models: []config.VertexCompatModel{{Name: "m1"}}},
		},
		AmpCode: config.AmpCode{
			ModelMappings:      []config.AmpModelMapping{{From: "a", To: "b"}},
			ForceModelMappings: false,
		},
	}
	newCfg := &config.Config{
		GeminiKey: []config.GeminiKey{
			{APIKey: "g1", Headers: map[string]string{"H": "2"}, ExcludedModels: []string{"a", "b"}},
		},
		VertexCompatAPIKey: []config.VertexCompatKey{
			{APIKey: "v1", BaseURL: "http://v-new", Models: []config.VertexCompatModel{{Name: "m1"}, {Name: "m2"}}},
		},
		AmpCode: config.AmpCode{
			ModelMappings:      []config.AmpModelMapping{{From: "a", To: "c"}},
			ForceModelMappings: true,
		},
	}

	details := BuildConfigChangeDetails(oldCfg, newCfg)
	expectContains(t, details, "gemini[0].headers: updated")
	expectContains(t, details, "gemini[0].excluded-models: updated (1 -> 2 entries)")
	expectContains(t, details, "ampcode.model-mappings: updated (1 -> 1 entries)")
	expectContains(t, details, "ampcode.force-model-mappings: false -> true")
}

func TestBuildConfigChangeDetails_ModelPrefixes(t *testing.T) {
	oldCfg := &config.Config{
		GeminiKey: []config.GeminiKey{
			{APIKey: "g1", Prefix: "old-g", BaseURL: "http://g", ProxyURL: "http://gp"},
		},
		ClaudeKey: []config.ClaudeKey{
			{APIKey: "c1", Prefix: "old-c", BaseURL: "http://c", ProxyURL: "http://cp"},
		},
		CodexKey: []config.CodexKey{
			{APIKey: "x1", Prefix: "old-x", BaseURL: "http://x", ProxyURL: "http://xp"},
		},
		VertexCompatAPIKey: []config.VertexCompatKey{
			{APIKey: "v1", Prefix: "old-v", BaseURL: "http://v", ProxyURL: "http://vp"},
		},
	}
	newCfg := &config.Config{
		GeminiKey: []config.GeminiKey{
			{APIKey: "g1", Prefix: "new-g", BaseURL: "http://g", ProxyURL: "http://gp"},
		},
		ClaudeKey: []config.ClaudeKey{
			{APIKey: "c1", Prefix: "new-c", BaseURL: "http://c", ProxyURL: "http://cp"},
		},
		CodexKey: []config.CodexKey{
			{APIKey: "x1", Prefix: "new-x", BaseURL: "http://x", ProxyURL: "http://xp"},
		},
		VertexCompatAPIKey: []config.VertexCompatKey{
			{APIKey: "v1", Prefix: "new-v", BaseURL: "http://v", ProxyURL: "http://vp"},
		},
	}

	changes := BuildConfigChangeDetails(oldCfg, newCfg)
	expectContains(t, changes, "gemini[0].prefix: old-g -> new-g")
	expectContains(t, changes, "claude[0].prefix: old-c -> new-c")
	expectContains(t, changes, "codex[0].prefix: old-x -> new-x")
	expectContains(t, changes, "vertex[0].prefix: old-v -> new-v")
}

func TestBuildConfigChangeDetails_NilSafe(t *testing.T) {
	if details := BuildConfigChangeDetails(nil, &config.Config{}); len(details) != 0 {
		t.Fatalf("expected empty change list when old nil, got %v", details)
	}
	if details := BuildConfigChangeDetails(&config.Config{}, nil); len(details) != 0 {
		t.Fatalf("expected empty change list when new nil, got %v", details)
	}
}

func TestBuildConfigChangeDetails_SecretsAndCounts(t *testing.T) {
	oldCfg := &config.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{"a"},
		},
		AmpCode: config.AmpCode{
			UpstreamAPIKey: "",
		},
		RemoteManagement: config.RemoteManagement{
			SecretKey: "",
		},
	}
	newCfg := &config.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{"a", "b", "c"},
		},
		AmpCode: config.AmpCode{
			UpstreamAPIKey: "new-key",
		},
		RemoteManagement: config.RemoteManagement{
			SecretKey: "new-secret",
		},
	}

	details := BuildConfigChangeDetails(oldCfg, newCfg)
	expectContains(t, details, "api-keys count: 1 -> 3")
	expectContains(t, details, "ampcode.upstream-api-key: added")
	expectContains(t, details, "remote-management.secret-key: created")
}

func TestBuildConfigChangeDetails_FlagsAndKeys(t *testing.T) {
	oldCfg := &config.Config{
		Port:                   1000,
		AuthDir:                "/old",
		Debug:                  false,
		LoggingToFile:          false,
		UsageStatisticsEnabled: false,
		DisableCooling:         false,
		RequestRetry:           1,
		MaxRetryInterval:       1,
		WebsocketAuth:          false,
		QuotaExceeded:          config.QuotaExceeded{SwitchProject: false, SwitchPreviewModel: false},
		ClaudeKey:              []config.ClaudeKey{{APIKey: "c1"}},
		CodexKey:               []config.CodexKey{{APIKey: "x1"}},
		AmpCode:                config.AmpCode{UpstreamAPIKey: "keep", RestrictManagementToLocalhost: false},
		RemoteManagement:       config.RemoteManagement{DisableControlPanel: false, PanelGitHubRepository: "old/repo", SecretKey: "keep"},
		SDKConfig: sdkconfig.SDKConfig{
			RequestLog:       false,
			ProxyURL:         "http://old-proxy",
			APIKeys:          []string{"key-1"},
			ForceModelPrefix: false,
		},
	}
	newCfg := &config.Config{
		Port:                   2000,
		AuthDir:                "/new",
		Debug:                  true,
		LoggingToFile:          true,
		UsageStatisticsEnabled: true,
		DisableCooling:         true,
		RequestRetry:           2,
		MaxRetryInterval:       3,
		WebsocketAuth:          true,
		QuotaExceeded:          config.QuotaExceeded{SwitchProject: true, SwitchPreviewModel: true},
		ClaudeKey: []config.ClaudeKey{
			{APIKey: "c1", BaseURL: "http://new", ProxyURL: "http://p", Headers: map[string]string{"H": "1"}, ExcludedModels: []string{"a"}},
			{APIKey: "c2"},
		},
		CodexKey: []config.CodexKey{
			{APIKey: "x1", BaseURL: "http://x", ProxyURL: "http://px", Headers: map[string]string{"H": "2"}, ExcludedModels: []string{"b"}},
			{APIKey: "x2"},
		},
		AmpCode: config.AmpCode{
			UpstreamAPIKey:                "",
			RestrictManagementToLocalhost: true,
			ModelMappings:                 []config.AmpModelMapping{{From: "a", To: "b"}},
		},
		RemoteManagement: config.RemoteManagement{
			DisableControlPanel:   true,
			PanelGitHubRepository: "new/repo",
			SecretKey:             "",
		},
		SDKConfig: sdkconfig.SDKConfig{
			RequestLog:       true,
			ProxyURL:         "http://new-proxy",
			APIKeys:          []string{" key-1 ", "key-2"},
			ForceModelPrefix: true,
		},
	}

	details := BuildConfigChangeDetails(oldCfg, newCfg)
	expectContains(t, details, "debug: false -> true")
	expectContains(t, details, "logging-to-file: false -> true")
	expectContains(t, details, "usage-statistics-enabled: false -> true")
	expectContains(t, details, "disable-cooling: false -> true")
	expectContains(t, details, "request-log: false -> true")
	expectContains(t, details, "request-retry: 1 -> 2")
	expectContains(t, details, "max-retry-interval: 1 -> 3")
	expectContains(t, details, "proxy-url: http://old-proxy -> http://new-proxy")
	expectContains(t, details, "ws-auth: false -> true")
	expectContains(t, details, "force-model-prefix: false -> true")
	expectContains(t, details, "quota-exceeded.switch-project: false -> true")
	expectContains(t, details, "quota-exceeded.switch-preview-model: false -> true")
	expectContains(t, details, "api-keys count: 1 -> 2")
	expectContains(t, details, "claude-api-key count: 1 -> 2")
	expectContains(t, details, "codex-api-key count: 1 -> 2")
	expectContains(t, details, "ampcode.restrict-management-to-localhost: false -> true")
	expectContains(t, details, "ampcode.upstream-api-key: removed")
	expectContains(t, details, "remote-management.disable-control-panel: false -> true")
	expectContains(t, details, "remote-management.panel-github-repository: old/repo -> new/repo")
	expectContains(t, details, "remote-management.secret-key: deleted")
}

func TestBuildConfigChangeDetails_AllBranches(t *testing.T) {
	oldCfg := &config.Config{
		Port:                   1,
		AuthDir:                "/a",
		Debug:                  false,
		LoggingToFile:          false,
		UsageStatisticsEnabled: false,
		DisableCooling:         false,
		RequestRetry:           1,
		MaxRetryInterval:       1,
		WebsocketAuth:          false,
		QuotaExceeded:          config.QuotaExceeded{SwitchProject: false, SwitchPreviewModel: false},
		GeminiKey: []config.GeminiKey{
			{APIKey: "g-old", BaseURL: "http://g-old", ProxyURL: "http://gp-old", Headers: map[string]string{"A": "1"}},
		},
		ClaudeKey: []config.ClaudeKey{
			{APIKey: "c-old", BaseURL: "http://c-old", ProxyURL: "http://cp-old", Headers: map[string]string{"H": "1"}, ExcludedModels: []string{"x"}},
		},
		CodexKey: []config.CodexKey{
			{APIKey: "x-old", BaseURL: "http://x-old", ProxyURL: "http://xp-old", Headers: map[string]string{"H": "1"}, ExcludedModels: []string{"x"}},
		},
		VertexCompatAPIKey: []config.VertexCompatKey{
			{APIKey: "v-old", BaseURL: "http://v-old", ProxyURL: "http://vp-old", Headers: map[string]string{"H": "1"}, Models: []config.VertexCompatModel{{Name: "m1"}}},
		},
		AmpCode: config.AmpCode{
			UpstreamURL:                   "http://amp-old",
			UpstreamAPIKey:                "old-key",
			RestrictManagementToLocalhost: false,
			ModelMappings:                 []config.AmpModelMapping{{From: "a", To: "b"}},
			ForceModelMappings:            false,
		},
		RemoteManagement: config.RemoteManagement{
			AllowRemote:           false,
			DisableControlPanel:   false,
			PanelGitHubRepository: "old/repo",
			SecretKey:             "old",
		},
		SDKConfig: sdkconfig.SDKConfig{
			RequestLog: false,
			ProxyURL:   "http://old-proxy",
			APIKeys:    []string{" keyA "},
		},
		OAuthExcludedModels: map[string][]string{"p1": {"a"}},
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name: "prov-old",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "k1"},
				},
				Models: []config.OpenAICompatibilityModel{{Name: "m1"}},
			},
		},
	}
	newCfg := &config.Config{
		Port:                   2,
		AuthDir:                "/b",
		Debug:                  true,
		LoggingToFile:          true,
		UsageStatisticsEnabled: true,
		DisableCooling:         true,
		RequestRetry:           2,
		MaxRetryInterval:       3,
		WebsocketAuth:          true,
		QuotaExceeded:          config.QuotaExceeded{SwitchProject: true, SwitchPreviewModel: true},
		GeminiKey: []config.GeminiKey{
			{APIKey: "g-new", BaseURL: "http://g-new", ProxyURL: "http://gp-new", Headers: map[string]string{"A": "2"}, ExcludedModels: []string{"x", "y"}},
		},
		ClaudeKey: []config.ClaudeKey{
			{APIKey: "c-new", BaseURL: "http://c-new", ProxyURL: "http://cp-new", Headers: map[string]string{"H": "2"}, ExcludedModels: []string{"x", "y"}},
		},
		CodexKey: []config.CodexKey{
			{APIKey: "x-new", BaseURL: "http://x-new", ProxyURL: "http://xp-new", Headers: map[string]string{"H": "2"}, ExcludedModels: []string{"x", "y"}},
		},
		VertexCompatAPIKey: []config.VertexCompatKey{
			{APIKey: "v-new", BaseURL: "http://v-new", ProxyURL: "http://vp-new", Headers: map[string]string{"H": "2"}, Models: []config.VertexCompatModel{{Name: "m1"}, {Name: "m2"}}},
		},
		AmpCode: config.AmpCode{
			UpstreamURL:                   "http://amp-new",
			UpstreamAPIKey:                "",
			RestrictManagementToLocalhost: true,
			ModelMappings:                 []config.AmpModelMapping{{From: "a", To: "c"}},
			ForceModelMappings:            true,
		},
		RemoteManagement: config.RemoteManagement{
			AllowRemote:           true,
			DisableControlPanel:   true,
			PanelGitHubRepository: "new/repo",
			SecretKey:             "",
		},
		SDKConfig: sdkconfig.SDKConfig{
			RequestLog: true,
			ProxyURL:   "http://new-proxy",
			APIKeys:    []string{"keyB"},
		},
		OAuthExcludedModels: map[string][]string{"p1": {"b", "c"}, "p2": {"d"}},
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name: "prov-old",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "k1"},
					{APIKey: "k2"},
				},
				Models: []config.OpenAICompatibilityModel{{Name: "m1"}, {Name: "m2"}},
			},
			{
				Name:          "prov-new",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "k3"}},
			},
		},
	}

	changes := BuildConfigChangeDetails(oldCfg, newCfg)
	expectContains(t, changes, "port: 1 -> 2")
	expectContains(t, changes, "auth-dir: /a -> /b")
	expectContains(t, changes, "debug: false -> true")
	expectContains(t, changes, "logging-to-file: false -> true")
	expectContains(t, changes, "usage-statistics-enabled: false -> true")
	expectContains(t, changes, "disable-cooling: false -> true")
	expectContains(t, changes, "request-retry: 1 -> 2")
	expectContains(t, changes, "max-retry-interval: 1 -> 3")
	expectContains(t, changes, "proxy-url: http://old-proxy -> http://new-proxy")
	expectContains(t, changes, "ws-auth: false -> true")
	expectContains(t, changes, "quota-exceeded.switch-project: false -> true")
	expectContains(t, changes, "quota-exceeded.switch-preview-model: false -> true")
	expectContains(t, changes, "api-keys: values updated (count unchanged, redacted)")
	expectContains(t, changes, "gemini[0].base-url: http://g-old -> http://g-new")
	expectContains(t, changes, "gemini[0].proxy-url: http://gp-old -> http://gp-new")
	expectContains(t, changes, "gemini[0].api-key: updated")
	expectContains(t, changes, "gemini[0].headers: updated")
	expectContains(t, changes, "gemini[0].excluded-models: updated (0 -> 2 entries)")
	expectContains(t, changes, "claude[0].base-url: http://c-old -> http://c-new")
	expectContains(t, changes, "claude[0].proxy-url: http://cp-old -> http://cp-new")
	expectContains(t, changes, "claude[0].api-key: updated")
	expectContains(t, changes, "claude[0].headers: updated")
	expectContains(t, changes, "claude[0].excluded-models: updated (1 -> 2 entries)")
	expectContains(t, changes, "codex[0].base-url: http://x-old -> http://x-new")
	expectContains(t, changes, "codex[0].proxy-url: http://xp-old -> http://xp-new")
	expectContains(t, changes, "codex[0].api-key: updated")
	expectContains(t, changes, "codex[0].headers: updated")
	expectContains(t, changes, "codex[0].excluded-models: updated (1 -> 2 entries)")
	expectContains(t, changes, "vertex[0].base-url: http://v-old -> http://v-new")
	expectContains(t, changes, "vertex[0].proxy-url: http://vp-old -> http://vp-new")
	expectContains(t, changes, "vertex[0].api-key: updated")
	expectContains(t, changes, "vertex[0].models: updated (1 -> 2 entries)")
	expectContains(t, changes, "vertex[0].headers: updated")
	expectContains(t, changes, "ampcode.upstream-url: http://amp-old -> http://amp-new")
	expectContains(t, changes, "ampcode.upstream-api-key: removed")
	expectContains(t, changes, "ampcode.restrict-management-to-localhost: false -> true")
	expectContains(t, changes, "ampcode.model-mappings: updated (1 -> 1 entries)")
	expectContains(t, changes, "ampcode.force-model-mappings: false -> true")
	expectContains(t, changes, "oauth-excluded-models[p1]: updated (1 -> 2 entries)")
	expectContains(t, changes, "oauth-excluded-models[p2]: added (1 entries)")
	expectContains(t, changes, "remote-management.allow-remote: false -> true")
	expectContains(t, changes, "remote-management.disable-control-panel: false -> true")
	expectContains(t, changes, "remote-management.panel-github-repository: old/repo -> new/repo")
	expectContains(t, changes, "remote-management.secret-key: deleted")
	expectContains(t, changes, "openai-compatibility:")
}

func TestFormatProxyURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: "<none>"},
		{name: "invalid", in: "http://[::1", want: "<redacted>"},
		{name: "fullURLRedactsUserinfoAndPath", in: "http://user:pass@example.com:8080/path?x=1#frag", want: "http://example.com:8080"},
		{name: "socks5RedactsUserinfoAndPath", in: "socks5://user:pass@192.168.1.1:1080/path?x=1", want: "socks5://192.168.1.1:1080"},
		{name: "socks5HostPort", in: "socks5://proxy.example.com:1080/", want: "socks5://proxy.example.com:1080"},
		{name: "hostPortNoScheme", in: "example.com:1234/path?x=1", want: "example.com:1234"},
		{name: "relativePathRedacted", in: "/just/path", want: "<redacted>"},
		{name: "schemeAndHost", in: "https://example.com", want: "https://example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatProxyURL(tt.in); got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestBuildConfigChangeDetails_SecretAndUpstreamUpdates(t *testing.T) {
	oldCfg := &config.Config{
		AmpCode: config.AmpCode{
			UpstreamAPIKey: "old",
		},
		RemoteManagement: config.RemoteManagement{
			SecretKey: "old",
		},
	}
	newCfg := &config.Config{
		AmpCode: config.AmpCode{
			UpstreamAPIKey: "new",
		},
		RemoteManagement: config.RemoteManagement{
			SecretKey: "new",
		},
	}

	changes := BuildConfigChangeDetails(oldCfg, newCfg)
	expectContains(t, changes, "ampcode.upstream-api-key: updated")
	expectContains(t, changes, "remote-management.secret-key: updated")
}

func TestBuildConfigChangeDetails_CountBranches(t *testing.T) {
	oldCfg := &config.Config{}
	newCfg := &config.Config{
		GeminiKey: []config.GeminiKey{{APIKey: "g"}},
		ClaudeKey: []config.ClaudeKey{{APIKey: "c"}},
		CodexKey:  []config.CodexKey{{APIKey: "x"}},
		VertexCompatAPIKey: []config.VertexCompatKey{
			{APIKey: "v", BaseURL: "http://v"},
		},
	}

	changes := BuildConfigChangeDetails(oldCfg, newCfg)
	expectContains(t, changes, "gemini-api-key count: 0 -> 1")
	expectContains(t, changes, "claude-api-key count: 0 -> 1")
	expectContains(t, changes, "codex-api-key count: 0 -> 1")
	expectContains(t, changes, "vertex-api-key count: 0 -> 1")
}

func TestTrimStrings(t *testing.T) {
	out := trimStrings([]string{" a ", "b", "  c"})
	if len(out) != 3 || out[0] != "a" || out[1] != "b" || out[2] != "c" {
		t.Fatalf("unexpected trimmed strings: %v", out)
	}
}
