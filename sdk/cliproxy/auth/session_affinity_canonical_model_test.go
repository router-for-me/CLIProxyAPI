package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// Thinking-suffix variants of the same route model (e.g. "claude-3" and
// "claude-3(high)") share one credential pool and one cooldown state, so a
// session bound through one variant must stay bound when a later request in
// the same session arrives with another variant.
func TestSessionAffinitySelector_ThinkingSuffixSharesSessionBinding(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelector(&RoundRobinSelector{})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-a"},
		{ID: "auth-b"},
	}
	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_suffix-share"}}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}

	first, err := selector.Pick(context.Background(), "claude", "claude-3(high)", opts, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}

	for _, model := range []string{"claude-3", "claude-3(low)", "claude-3(high)"} {
		got, errPick := selector.Pick(context.Background(), "claude", model, opts, auths)
		if errPick != nil {
			t.Fatalf("Pick(%q) error = %v", model, errPick)
		}
		if got.ID != first.ID {
			t.Fatalf("Pick(%q) auth.ID = %q, want sticky auth %q (thinking-suffix variants must share the session binding)", model, got.ID, first.ID)
		}
	}
}

// A failure reported with the base model name must release a binding that was
// created through a thinking-suffixed variant of the same route model.
func TestSessionAffinityOnResult_ThinkingSuffixReleasesBinding(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelector(&RoundRobinSelector{})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-a"},
		{ID: "auth-b"},
	}
	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_suffix-release"}}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}

	first, err := selector.Pick(context.Background(), "claude", "claude-3(high)", opts, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}

	// The next request of the same session arrives without the suffix and fails.
	second, errPick := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
	if errPick != nil {
		t.Fatalf("Pick() error = %v", errPick)
	}
	if second.ID != first.ID {
		t.Fatalf("Pick() auth.ID = %q, want sticky auth %q before failure", second.ID, first.ID)
	}
	selector.OnResult(Result{
		AuthID:   second.ID,
		Provider: "claude",
		Model:    "claude-3",
		Success:  false,
		Error:    &Error{Code: "upstream_error", Message: "upstream failed"},
		Options:  opts,
	})

	// The binding is released, so the round-robin fallback must move on.
	third, errPick := selector.Pick(context.Background(), "claude", "claude-3(high)", opts, auths)
	if errPick != nil {
		t.Fatalf("Pick() after failure error = %v", errPick)
	}
	if third.ID == first.ID {
		t.Fatalf("Pick() after failure auth.ID = %q, want reselection after the binding was released", third.ID)
	}
}

// Parenthesized names that the thinking parsers do not recognize (e.g. a
// configured alias like "claude-3(custom)") are distinct models, not thinking
// variants, and must not be collapsed into the base model's session binding.
func TestSessionAffinitySelector_UnrecognizedSuffixKeepsSeparateBinding(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelector(&RoundRobinSelector{})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-a"},
		{ID: "auth-b"},
	}
	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_suffix-distinct"}}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}

	first, err := selector.Pick(context.Background(), "claude", "claude-3(custom)", opts, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}

	// The unrecognized suffix must not collapse onto the base model's binding:
	// the base model's first pick is a cache miss and round-robin moves on.
	second, errPick := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
	if errPick != nil {
		t.Fatalf("Pick() error = %v", errPick)
	}
	if second.ID == first.ID {
		t.Fatalf("Pick() auth.ID = %q, want a separate binding for an unrecognized suffix", second.ID)
	}

	// Sanity: a recognized thinking level still shares the base binding.
	third, errPick := selector.Pick(context.Background(), "claude", "claude-3(high)", opts, auths)
	if errPick != nil {
		t.Fatalf("Pick() error = %v", errPick)
	}
	if third.ID != second.ID {
		t.Fatalf("Pick() auth.ID = %q, want sticky auth %q for a recognized thinking variant", third.ID, second.ID)
	}
}
