package registry

import "testing"

func TestGetAvailableModelInfosPreservesMetadataAndAvailability(t *testing.T) {
	modelRegistry := newTestModelRegistry()
	modelRegistry.RegisterClient("openai-client", "openai", []*ModelInfo{
		{ID: "z-model", DisplayName: "Z Model", ContextLength: 1000},
	})
	modelRegistry.RegisterClient("claude-client", "claude", []*ModelInfo{
		{ID: "a-model", DisplayName: "A Model", ContextLength: 2000, Thinking: &ThinkingSupport{Levels: []string{"low", "high"}}},
	})
	modelRegistry.RegisterClient("xai-client", "xai", []*ModelInfo{{ID: "x-model"}})
	modelRegistry.RegisterClient("suspended-client", "xai", []*ModelInfo{{ID: "hidden-model"}})
	modelRegistry.SuspendClientModel("suspended-client", "hidden-model", "manual")

	models := modelRegistry.GetAvailableModelInfos()
	if len(models) != 3 {
		t.Fatalf("available model count = %d, want 3", len(models))
	}
	if models[0].ID != "a-model" || models[1].ID != "x-model" || models[2].ID != "z-model" {
		t.Fatalf("model order = [%s, %s, %s], want [a-model, x-model, z-model]", models[0].ID, models[1].ID, models[2].ID)
	}
	if models[0].Thinking == nil || len(models[0].Thinking.Levels) != 2 || models[0].Thinking.Levels[1] != "high" {
		t.Fatalf("thinking metadata = %#v", models[0].Thinking)
	}
	for _, model := range models {
		if model.ID == "hidden-model" {
			t.Fatalf("suspended model returned: %#v", model)
		}
	}

	models[0].Thinking.Levels[0] = "mutated"
	fresh := modelRegistry.GetAvailableModelInfos()
	if fresh[0].Thinking.Levels[0] != "low" {
		t.Fatalf("snapshot was not cloned: %#v", fresh[0].Thinking.Levels)
	}
}

func TestGetAvailableModelInfosHonorsQuotaAndSuspensionAvailability(t *testing.T) {
	tests := []struct {
		name               string
		clientCount        int
		quotaExceeded      bool
		quotaSuspended     bool
		manualSuspended    bool
		wantModelAvailable bool
	}{
		{
			name:               "quota cooldown remains listed",
			quotaExceeded:      true,
			wantModelAvailable: true,
		},
		{
			name:               "quota suspension reason remains listed",
			quotaSuspended:     true,
			wantModelAvailable: true,
		},
		{
			name:               "quota and non-quota suspensions are hidden",
			clientCount:        2,
			quotaExceeded:      true,
			quotaSuspended:     true,
			manualSuspended:    true,
			wantModelAvailable: false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			const modelID = "shared-model"
			modelRegistry := newTestModelRegistry()
			modelRegistry.RegisterClient("quota-client", "openai", []*ModelInfo{{ID: modelID}})
			if testCase.clientCount > 1 {
				modelRegistry.RegisterClient("manual-client", "openai", []*ModelInfo{{ID: modelID}})
			}
			if testCase.quotaExceeded {
				modelRegistry.SetModelQuotaExceeded("quota-client", modelID)
			}
			if testCase.quotaSuspended {
				modelRegistry.SuspendClientModel("quota-client", modelID, "quota")
			}
			if testCase.manualSuspended {
				modelRegistry.SuspendClientModel("manual-client", modelID, "manual")
			}

			infos := modelRegistry.GetAvailableModelInfos()
			gotInfoAvailable := len(infos) == 1 && infos[0] != nil && infos[0].ID == modelID
			if gotInfoAvailable != testCase.wantModelAvailable {
				t.Fatalf("GetAvailableModelInfos() available = %v, want %v; models = %#v", gotInfoAvailable, testCase.wantModelAvailable, infos)
			}

			models := modelRegistry.GetAvailableModels("openai")
			gotListAvailable := len(models) == 1 && models[0]["id"] == modelID
			if gotListAvailable != testCase.wantModelAvailable {
				t.Fatalf("GetAvailableModels() available = %v, want %v; models = %#v", gotListAvailable, testCase.wantModelAvailable, models)
			}
		})
	}
}
