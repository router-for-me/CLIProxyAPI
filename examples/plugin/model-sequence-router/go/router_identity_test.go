package main

import (
	"testing"
)

// TestRouteHoldsSequenceWhenPromptCacheKeyAppears verifies that a credential-affinity
// identifier appearing mid-conversation cannot restart that conversation's cursor.
func TestRouteHoldsSequenceWhenPromptCacheKeyAppears(t *testing.T) {
	runtime := newTestRuntime(t)

	opening := runtime.route(responsesRoute(t, "conv-alpha", "", 1))
	if opening.Target != "codex" {
		t.Fatalf("opening turn = %#v, want the first sequence position", opening)
	}
	runtime.route(responsesRoute(t, "conv-alpha", "cache-lane", 2))
	runtime.route(responsesRoute(t, "conv-alpha", "cache-lane", 3))

	fourth := runtime.route(responsesRoute(t, "conv-alpha", "cache-lane", 4))
	if fourth.Target != "claude" {
		t.Fatalf("sequence restarted when the prompt cache key appeared: %#v", fourth)
	}
}

// TestRouteKeepsConversationsSharingOneCacheLaneApart verifies that one cache
// affinity lane cannot merge the cursors of two conversations.
func TestRouteKeepsConversationsSharingOneCacheLaneApart(t *testing.T) {
	runtime := newTestRuntime(t)
	for turn := 1; turn <= 3; turn++ {
		runtime.route(responsesRoute(t, "conv-alpha", "shared-lane", turn))
	}

	other := runtime.route(responsesRoute(t, "conv-beta", "shared-lane", 1))
	if other.Target != "codex" {
		t.Fatalf("second conversation inherited the first conversation's position: %#v", other)
	}
}
