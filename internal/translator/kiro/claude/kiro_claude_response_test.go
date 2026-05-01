package claude

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

func TestBuildClaudeResponseIncludesKiroCacheUsage(t *testing.T) {
	out := BuildClaudeResponse("hello", nil, "claude-sonnet-4", usage.Detail{
		InputTokens:              10,
		OutputTokens:             2,
		CacheReadInputTokens:     7,
		CacheCreationInputTokens: 3,
	}, "end_turn")

	if got := gjson.GetBytes(out, "usage.cache_read_input_tokens").Int(); got != 7 {
		t.Fatalf("cache_read_input_tokens = %d, want 7", got)
	}
	if got := gjson.GetBytes(out, "usage.cache_creation_input_tokens").Int(); got != 3 {
		t.Fatalf("cache_creation_input_tokens = %d, want 3", got)
	}
}

func TestBuildClaudeMessageDeltaEventIncludesKiroCacheUsage(t *testing.T) {
	out := BuildClaudeMessageDeltaEvent("end_turn", usage.Detail{
		InputTokens:              10,
		OutputTokens:             2,
		CacheReadInputTokens:     7,
		CacheCreationInputTokens: 3,
	})

	payload, ok := strings.CutPrefix(string(out), "event: message_delta\ndata: ")
	if !ok {
		t.Fatalf("expected SSE data payload, got %s", string(out))
	}
	if got := gjson.Get(payload, "usage.cache_read_input_tokens").Int(); got != 7 {
		t.Fatalf("cache_read_input_tokens = %d, want 7", got)
	}
	if got := gjson.Get(payload, "usage.cache_creation_input_tokens").Int(); got != 3 {
		t.Fatalf("cache_creation_input_tokens = %d, want 3", got)
	}
}
