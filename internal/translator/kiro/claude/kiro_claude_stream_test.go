package claude

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestBuildClaudeMessageDeltaEventIncludesCacheUsageFields(t *testing.T) {
	event := BuildClaudeMessageDeltaEvent("end_turn", usage.Detail{
		InputTokens:         290,
		OutputTokens:        1,
		CacheReadTokens:     3822,
		CacheCreationTokens: 17,
	})

	var payload map[string]interface{}
	if err := json.Unmarshal(claudeSSEData(t, event), &payload); err != nil {
		t.Fatalf("unmarshal message_delta: %v", err)
	}
	usagePayload, ok := payload["usage"].(map[string]interface{})
	if !ok {
		t.Fatalf("usage payload missing or wrong type: %#v", payload["usage"])
	}
	assertJSONNumber(t, usagePayload, "input_tokens", 290)
	assertJSONNumber(t, usagePayload, "output_tokens", 1)
	assertJSONNumber(t, usagePayload, "cache_read_input_tokens", 3822)
	assertJSONNumber(t, usagePayload, "cache_creation_input_tokens", 17)
}

func claudeSSEData(t *testing.T, event []byte) []byte {
	t.Helper()
	for _, line := range strings.Split(string(event), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			return []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	t.Fatalf("event has no data line: %s", string(event))
	return nil
}
