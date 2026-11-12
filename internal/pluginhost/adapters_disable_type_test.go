package pluginhost

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func TestPluginThinkingSupportCarriesDisableTypeBothWays(t *testing.T) {
	plugin := &pluginapi.ThinkingSupport{ZeroAllowed: true, Levels: []string{"low", "high"}, DisableType: "between_tools"}
	reg := pluginThinkingSupportToRegistryThinkingSupport(plugin)
	if reg.DisableType != "between_tools" {
		t.Fatalf("plugin -> registry DisableType = %q, want between_tools", reg.DisableType)
	}
	if back := registryThinkingSupportToPluginThinkingSupport(reg); back.DisableType != "between_tools" {
		t.Fatalf("registry -> plugin DisableType = %q, want between_tools", back.DisableType)
	}
	if got := registryModelInfoToPluginModelInfo(&registry.ModelInfo{ID: "m", Thinking: reg}); got.Thinking == nil || got.Thinking.DisableType != "between_tools" {
		t.Fatalf("registry model -> plugin model DisableType = %+v, want between_tools", got.Thinking)
	}
}

func TestPluginThinkingSupportDisableTypeJSONCompatibility(t *testing.T) {
	raw, err := json.Marshal(pluginapi.ThinkingSupport{Levels: []string{"low"}, DisableType: "between_tools"})
	if err != nil {
		t.Fatal(err)
	}
	var decoded pluginapi.ThinkingSupport
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.DisableType != "between_tools" {
		t.Fatalf("round trip = %+v, err %v", decoded, err)
	}
	var old pluginapi.ThinkingSupport
	if err := json.Unmarshal([]byte(`{"Levels":["low"],"ZeroAllowed":true}`), &old); err != nil || old.DisableType != "" {
		t.Fatalf("older plugin payload = %+v, err %v", old, err)
	}
}

func TestPluginRegisteredSonnet55TurnsThinkingOffWithBetweenTools(t *testing.T) {
	info := pluginModelInfoToRegistryModelInfo(pluginapi.ModelInfo{
		ID: "claude-sonnet-5-5", Type: "claude",
		Thinking: &pluginapi.ThinkingSupport{ZeroAllowed: true, Levels: []string{"low", "medium", "high"}, DisableType: "between_tools"},
	})
	body := []byte(`{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"}}`)
	out, err := thinking.ApplyThinkingWithModelInfo(body, nil, "claude-sonnet-5-5", "claude", "claude", "claude", info)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(out, "thinking").Raw; got != `{"type":"between_tools"}` {
		t.Fatalf("thinking = %s, want between_tools; body=%s", got, out)
	}
}
