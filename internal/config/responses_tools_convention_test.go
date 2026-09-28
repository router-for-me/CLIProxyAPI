package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/responsestools"
)

const partialRouteYAML = "requests:\n" +
	"  responses-tools:\n" +
	"    enabled: true\n" +
	"    routes:\n" +
	"      - match:\n" +
	"          provider: meta\n" +
	"          auth-kind: api-key\n" +
	"          upstream-model: muse-spark-1.3-contributor\n" +
	"          upstream-format: codex\n" +
	"          base-url: https://example.invalid/v1\n" +
	"        client-search: bridge\n"

func partialRouteConfig(t *testing.T) (*Config, responsestools.Route) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(partialRouteYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg, responsestools.Route{
		Provider:       "meta",
		AuthKind:       "api-key",
		UpstreamModel:  "muse-spark-1.3-contributor",
		UpstreamFormat: "codex",
		BaseURL:        "https://example.invalid/v1",
	}
}

// A route that overrides one strategy must keep the rest of its provider's
// convention. Materializing a config-wide default for the omitted strategies
// used to erase the difference between "omitted" and "set to that value", and
// the route then lost the portable surface its upstream needs.
func TestPartialRouteInheritsProviderConvention(t *testing.T) {
	cfg, route := partialRouteConfig(t)
	policy, err := cfg.ResponsesTools.Compile()
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	effective, err := responsestools.EffectivePolicy(policy, route)
	if err != nil {
		t.Fatalf("effective policy: %v", err)
	}
	if effective.ClientSearch != responsestools.ClientSearchBridge {
		t.Errorf("client-search = %q, want the rule's %q", effective.ClientSearch, responsestools.ClientSearchBridge)
	}
	if effective.CustomTools != responsestools.CustomToolsFunction {
		t.Errorf("custom-tools = %q, want the convention's %q", effective.CustomTools, responsestools.CustomToolsFunction)
	}
	if effective.CustomGrammar != responsestools.CustomGrammarDescribe {
		t.Errorf("custom-grammar = %q, want the convention's %q", effective.CustomGrammar, responsestools.CustomGrammarDescribe)
	}
	if !effective.Schema.CompletesSearchSchemas() {
		t.Error("schema completion was not inherited from the convention")
	}
}

// The end-to-end consequence: an upstream that refuses type: custom must not
// receive one, so the custom declaration has to be folded before the wire.
func TestPartialRouteFoldsCustomDeclarationOnTheWire(t *testing.T) {
	cfg, route := partialRouteConfig(t)
	policy, err := cfg.ResponsesTools.Compile()
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	effective, err := responsestools.EffectivePolicy(policy, route)
	if err != nil {
		t.Fatalf("effective policy: %v", err)
	}
	body := []byte(`{"model":"m","tools":[{"type":"custom","name":"apply_patch","description":"Apply a patch."}],"input":[]}`)
	prepared, err := responsestools.Prepare(body, effective, policy.Limits, responsestools.NewLimiter(policy.Limits))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	out := string(prepared.Body)
	if strings.Contains(out, `"type":"custom"`) {
		t.Fatalf("custom declaration reached the wire unfolded: %s", out)
	}
	if prepared.Attempt == nil {
		t.Fatal("request was passed through without adaptation")
	}
	var decoded struct {
		Tools []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(prepared.Body, &decoded); err != nil {
		t.Fatalf("decode prepared body: %v", err)
	}
	if len(decoded.Tools) != 1 || decoded.Tools[0].Type != "function" {
		t.Fatalf("custom tool not folded into a function: %+v", decoded.Tools)
	}
}
