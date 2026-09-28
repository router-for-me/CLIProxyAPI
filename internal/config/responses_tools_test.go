package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/responsestools"
	"gopkg.in/yaml.v3"
)

func TestResponsesToolsDefaultsFollowConvention(t *testing.T) {
	cfg := &Config{}
	cfg.NormalizeResponsesToolsConfig()
	if !cfg.ResponsesTools.FeatureEnabled() {
		t.Fatalf("convention must stay enabled without configuration")
	}
	limits := cfg.ResponsesTools.Limits
	defaults := responsestools.DefaultLimits()
	if limits.MaxActiveToolBytes != defaults.MaxActiveToolBytes ||
		limits.MaxActiveAttempts != defaults.MaxActiveAttempts ||
		limits.MaxStateBytes != defaults.MaxStateBytes ||
		limits.MaxAttemptBytes != defaults.MaxAttemptBytes ||
		limits.MaxSchemaExpansionBytes != defaults.MaxSchemaExpansionBytes ||
		limits.MaxSchemaExpansionNodes != defaults.MaxSchemaExpansionNodes ||
		limits.MaxDepth != defaults.MaxDepth {
		t.Fatalf("limits not defaulted: %+v", limits)
	}
	if err := cfg.ValidateResponsesToolsConfig(); err != nil {
		t.Fatalf("empty policy must validate: %v", err)
	}
	policy, err := cfg.ResponsesTools.Compile()
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !policy.Enabled || len(policy.Routes) != 0 {
		t.Fatalf("convention policy must be enabled with no explicit routes")
	}
	off := false
	cfg.ResponsesTools.Enabled = &off
	if cfg.ResponsesTools.FeatureEnabled() {
		t.Fatalf("explicit false must disable the emergency gate")
	}
}

func TestResponsesToolsInvalidEnumRejected(t *testing.T) {
	cfg := &Config{}
	cfg.ResponsesTools.Enabled = boolPointer(true)
	cfg.ResponsesTools.Routes = []ResponsesToolsRoute{{
		Match:        ResponsesToolsMatch{Provider: "codex", AuthKind: "oauth", UpstreamModel: "m", UpstreamFormat: "codex"},
		ClientSearch: "sometimes",
		CustomTools:  "inherit",
	}}
	cfg.NormalizeResponsesToolsConfig()
	if err := cfg.ValidateResponsesToolsConfig(); err == nil {
		t.Fatalf("expected enum rejection")
	}
}

func TestResponsesToolsOverlapRejected(t *testing.T) {
	match := ResponsesToolsMatch{Provider: "codex", AuthKind: "oauth", UpstreamModel: "m", UpstreamFormat: "codex"}
	cfg := &Config{}
	cfg.ResponsesTools.Enabled = boolPointer(true)
	cfg.ResponsesTools.Routes = []ResponsesToolsRoute{
		{Match: match, ClientSearch: "bridge", CustomTools: "function"},
		{Match: match, ClientSearch: "bridge", CustomTools: "strip"},
	}
	cfg.NormalizeResponsesToolsConfig()
	if err := cfg.ValidateResponsesToolsConfig(); err == nil {
		t.Fatalf("expected overlap rejection")
	}
}

func TestResponsesToolsYAMLRoundTrip(t *testing.T) {
	document := "responses-tools:\n" +
		"  enabled: true\n" +
		"  limits:\n" +
		"    max-active-tool-bytes: 100\n" +
		"  routes:\n" +
		"    - match:\n" +
		"        provider: codex\n" +
		"        auth-kind: oauth\n" +
		"        upstream-model: m-x\n" +
		"        upstream-format: codex\n" +
		"      client-search: bridge\n" +
		"      custom-tools: function\n"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.ResponsesTools.FeatureEnabled() {
		t.Fatalf("enabled lost")
	}
	if len(cfg.ResponsesTools.Routes) != 1 {
		t.Fatalf("routes lost")
	}
	route := cfg.ResponsesTools.Routes[0]
	if route.ClientSearch != "bridge" || route.CustomTools != "function" {
		t.Fatalf("strategies lost: %+v", route)
	}
	if route.CustomGrammar != "" || route.Schema.LocalRefs != "" {
		t.Fatalf("omitted strategies must stay omitted, not defaulted: %+v", route)
	}
	cloned := cfg.CloneForRuntime()
	if len(cloned.ResponsesTools.Routes) != 1 || !cloned.ResponsesTools.FeatureEnabled() {
		t.Fatalf("clone lost responses-tools")
	}
	cloned.ResponsesTools.Routes[0].ClientSearch = "native"
	if cfg.ResponsesTools.Routes[0].ClientSearch != "bridge" {
		t.Fatalf("clone shares route memory")
	}
}

func boolPointer(value bool) *bool {
	return &value
}

func TestResponsesToolsLegacyRootKeyMigratesUnderV8(t *testing.T) {
	document := "host: \"127.0.0.1\"\n" +
		"port: 8317\n" +
		"responses-tools:\n" +
		"  enabled: true\n" +
		"  limits:\n" +
		"    max-active-tool-bytes: 100\n"

	migrated, _, err := NormalizeConfigLayout([]byte(document), true)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err = ValidateV8Config(migrated); err != nil {
		t.Fatalf("migrated layout is not valid v8: %v", err)
	}

	var doc yaml.Node
	if err = yaml.Unmarshal(migrated, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// The legacy spelling must land on the nested path, not be retained as a
	// comment or dropped: v8 rejects the root-level key on write.
	if node := yamlPath(doc.Content[0], "requests.responses-tools.enabled"); node == nil {
		t.Fatalf("requests.responses-tools.enabled missing after migration:\n%s", migrated)
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err = os.WriteFile(path, migrated, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.ResponsesTools.FeatureEnabled() {
		t.Fatalf("enabled lost across migration")
	}
}
