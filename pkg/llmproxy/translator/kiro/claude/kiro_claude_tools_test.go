package claude

import "testing"

func TestProcessToolUseEvent_PreservesBooleanFields(t *testing.T) {
	processedIDs := map[string]bool{}

	event := map[string]interface{}{
		"toolUseEvent": map[string]interface{}{
			"toolUseId": "toolu_1",
			"name":      "sequentialthinking",
			"input": map[string]interface{}{
				"thought":           "step 1",
				"nextThoughtNeeded": false,
			},
			"stop": true,
		},
	}

	toolUses, state := ProcessToolUseEvent(event, nil, processedIDs)
	if state != nil {
		t.Fatalf("expected nil state after stop event, got %+v", state)
	}
	if len(toolUses) != 1 {
		t.Fatalf("expected 1 tool use, got %d", len(toolUses))
	}

	next, ok := toolUses[0].Input["nextThoughtNeeded"].(bool)
	if !ok {
		t.Fatalf("expected nextThoughtNeeded to be bool, got %#v", toolUses[0].Input["nextThoughtNeeded"])
	}
	if next {
		t.Fatalf("expected nextThoughtNeeded=false, got true")
	}
}

func TestProcessToolUseEvent_PreservesBooleanFieldsFromFragments(t *testing.T) {
	processedIDs := map[string]bool{}

	start := map[string]interface{}{
		"toolUseEvent": map[string]interface{}{
			"toolUseId": "toolu_2",
			"name":      "sequentialthinking",
			"input":     "{\"thought\":\"step 1\",",
			"stop":      false,
		},
	}

	_, state := ProcessToolUseEvent(start, nil, processedIDs)
	if state == nil {
		t.Fatalf("expected in-progress state after first fragment")
	}

	stop := map[string]interface{}{
		"toolUseEvent": map[string]interface{}{
			"toolUseId": "toolu_2",
			"name":      "sequentialthinking",
			"input":     "\"nextThoughtNeeded\":false}",
			"stop":      true,
		},
	}

	toolUses, state := ProcessToolUseEvent(stop, state, processedIDs)
	if state != nil {
		t.Fatalf("expected nil state after completion, got %+v", state)
	}
	if len(toolUses) != 1 {
		t.Fatalf("expected 1 tool use, got %d", len(toolUses))
	}

	next, ok := toolUses[0].Input["nextThoughtNeeded"].(bool)
	if !ok {
		t.Fatalf("expected nextThoughtNeeded to be bool, got %#v", toolUses[0].Input["nextThoughtNeeded"])
	}
	if next {
		t.Fatalf("expected nextThoughtNeeded=false, got true")
	}
}
