package executor

import "testing"

func TestClaudeFable52BackportGates(t *testing.T) {
	for _, model := range []string{"claude-fable-5-2", "claude-fable-5.2", "claude-fable-5-2[1m]"} {
		t.Run(model, func(t *testing.T) {
			if !isClaudeFable52Model(model) {
				t.Fatal("Fable 5.2 model was not recognized")
			}
			if !claudeModelUsesProgressDisplay(model) {
				t.Fatal("Fable 5.2 must use progress display")
			}
			if !claudeModelHasPerTurnEffort(model) || !claudeModelHasPerTurnTiming(model) {
				t.Fatal("Fable 5.2 must enable per-turn effort and timing gates")
			}
			body := []byte(`{"model":"` + model + `","output_config":{"timing":{}}}`)
			if !claudeIncludePerTurnControl(body, nil) || !claudeIncludePerTurnTiming(body, nil) {
				t.Fatal("Fable 5.2 request did not enable per-turn controls")
			}
		})
	}
	if isClaudeFable52Model("claude-fable-5-1") {
		t.Fatal("Fable 5.1 must not match the Fable 5.2 gate")
	}
}
