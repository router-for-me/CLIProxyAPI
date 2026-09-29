package registry

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestGPT61SolCatalog(t *testing.T) {
	var catalog staticModelsJSON
	if err := json.Unmarshal(embeddedModelsJSON, &catalog); err != nil {
		t.Fatal(err)
	}
	for name, models := range map[string][]*ModelInfo{
		"team": catalog.CodexTeam, "plus": catalog.CodexPlus, "pro": catalog.CodexPro,
	} {
		t.Run(name, func(t *testing.T) {
			var found *ModelInfo
			for _, model := range models {
				if model.ID == "gpt-6.1-sol" {
					if found != nil {
						t.Fatal("duplicate GPT-6.1 Sol")
					}
					found = model
				}
			}
			if found == nil {
				t.Fatal("GPT-6.1 Sol is missing")
			}
			if found.ContextLength != 272000 || found.MaxCompletionTokens != 128000 {
				t.Fatalf("unexpected context/output limits: %+v", found)
			}
			if found.Thinking == nil || !reflect.DeepEqual(found.Thinking.Levels, []string{"low", "medium", "high", "xhigh", "max"}) {
				t.Fatalf("unexpected reasoning levels: %+v", found.Thinking)
			}
			if !found.SupportConfigurationUpdate {
				t.Fatal("missing configuration update capability")
			}
		})
	}
	for _, model := range catalog.CodexFree {
		if model.ID == "gpt-6.1-sol" {
			t.Fatal("GPT-6.1 Sol must not be inferred available on the free tier")
		}
	}
	var client codexClientModelsPayload
	if err := json.Unmarshal(embeddedCodexClientModelsJSON, &client); err != nil {
		t.Fatal(err)
	}
	for _, model := range client.Models {
		if model["slug"] != "gpt-6.1-sol" {
			continue
		}
		if model["default_reasoning_level"] != "low" || model["context_window"] != float64(272000) || model["max_context_window"] != float64(872000) {
			t.Fatal("unexpected Codex client reasoning/context defaults")
		}
		if model["tool_mode"] != "code_mode_only" || model["multi_agent_version"] != "v2" || model["node_repl_auto_review_required"] != true {
			t.Fatal("missing Codex client tool-mode capabilities")
		}
		return
	}
	t.Fatal("GPT-6.1 Sol Codex client template is missing")
}
