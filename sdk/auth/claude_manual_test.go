package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestSplitManualCode(t *testing.T) {
	for _, tc := range []struct {
		in, code, state string
		ok              bool
	}{
		{"abc#def", "abc", "def", true},
		{"  abc#def \n", "abc", "def", true},
		{"abc\n#def", "abc", "def", true}, // a wrapped terminal line
		{"abc", "", "", false},
		{"#def", "", "", false},
		{"abc#", "", "", false},
		{"", "", "", false},
	} {
		code, state, err := splitManualCode(tc.in)
		if (err == nil) != tc.ok || code != tc.code || state != tc.state {
			t.Errorf("splitManualCode(%q) = %q, %q, %v", tc.in, code, state, err)
		}
	}
}

func TestClaudeManualLoginPrintsManualURLAndRejectsAnotherLoginsCode(t *testing.T) {
	var asked string
	prompt := func(label string) (string, error) { asked = label; return "somecode#not-our-state", nil }
	record, err := NewClaudeAuthenticator().Login(context.Background(), &config.Config{}, &LoginOptions{ManualCode: true, Prompt: prompt})
	if record != nil || err == nil || !strings.Contains(err.Error(), "different login") {
		t.Fatalf("a code carrying another state must be refused before any token exchange: %v, %v", record, err)
	}
	if !strings.Contains(asked, "code") {
		t.Fatalf("prompt = %q", asked)
	}
}

func TestClaudeManualLoginNeedsAPrompt(t *testing.T) {
	if _, err := NewClaudeAuthenticator().Login(context.Background(), &config.Config{}, &LoginOptions{ManualCode: true}); err == nil {
		t.Fatal("a manual login with nothing to read the code from must fail")
	}
}

func TestClaudeManualLoginStopsWhenCancelledWhilePrompting(t *testing.T) {
	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) })
	prompt := func(string) (string, error) { <-blocked; return "", nil } // stdin that never answers
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := NewClaudeAuthenticator().Login(ctx, &config.Config{}, &LoginOptions{ManualCode: true, Prompt: prompt})
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled login stayed blocked on the prompt")
	}
}
