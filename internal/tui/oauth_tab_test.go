package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

func TestShouldAcceptOAuthPollFiltersStaleMessages(t *testing.T) {
	msg := oauthPollMsg{state: "state-a", generation: 1, done: true, message: "ok"}

	if shouldAcceptOAuthPoll(msg, "state-a", 2, oauthRemote) {
		t.Fatal("accepted poll with stale generation")
	}
	if shouldAcceptOAuthPoll(msg, "state-b", 1, oauthRemote) {
		t.Fatal("accepted poll with mismatched state")
	}
	if shouldAcceptOAuthPoll(msg, "state-a", 1, oauthIdle) {
		t.Fatal("accepted poll while not in remote state")
	}
	if !shouldAcceptOAuthPoll(msg, "state-a", 1, oauthRemote) {
		t.Fatal("rejected valid poll message")
	}
}

func TestShouldAcceptOAuthStartFiltersStaleMessages(t *testing.T) {
	msg := oauthStartMsg{state: "state-a", generation: 1, url: "https://example.com"}
	if shouldAcceptOAuthStart(msg, 2) {
		t.Fatal("accepted start with stale generation")
	}
	if !shouldAcceptOAuthStart(msg, 1) {
		t.Fatal("rejected valid start message")
	}
}

func TestShouldFailOAuthStatusPoll(t *testing.T) {
	if shouldFailOAuthStatusPoll(4, 5) {
		t.Fatal("failed too early on transient errors")
	}
	if !shouldFailOAuthStatusPoll(5, 5) {
		t.Fatal("did not fail after max consecutive errors")
	}
	if !shouldFailOAuthStatusPoll(1, 0) {
		t.Fatal("maxErrors<=0 should fail on first error")
	}
}

func TestOAuthTabUpdateIgnoresStalePollMsg(t *testing.T) {
	m := newOAuthTabModel(nil)
	m.state = oauthRemote
	m.authState = "state-current"
	m.pollGeneration = 2
	m.ready = true
	m.viewport = viewport.New(80, 24)
	m.viewport.SetContent(m.renderContent())

	updated, cmd := m.Update(oauthPollMsg{
		state:      "state-old",
		generation: 1,
		done:       true,
		message:    "should be ignored",
	})
	if cmd != nil {
		t.Fatal("expected no command for stale poll")
	}
	if updated.state != oauthRemote {
		t.Fatalf("state = %v, want oauthRemote", updated.state)
	}
	if updated.message != "" {
		t.Fatalf("message changed by stale poll: %q", updated.message)
	}
}

func TestOAuthTabUpdateAcceptsCurrentPollMsg(t *testing.T) {
	m := newOAuthTabModel(nil)
	m.state = oauthRemote
	m.authState = "state-current"
	m.pollGeneration = 3
	m.ready = true
	m.viewport = viewport.New(80, 24)
	m.viewport.SetContent(m.renderContent())

	updated, _ := m.Update(oauthPollMsg{
		state:      "state-current",
		generation: 3,
		done:       true,
		message:    "Authentication successful",
	})
	if updated.state != oauthSuccess {
		t.Fatalf("state = %v, want oauthSuccess", updated.state)
	}
}

func TestOAuthTabEscRemoteIncrementsGenerationAndClearsState(t *testing.T) {
	m := newOAuthTabModel(nil)
	m.state = oauthRemote
	m.authState = "state-to-cancel"
	m.authURL = "https://example.com"
	m.deviceFlow = true
	m.pollGeneration = 4
	m.ready = true
	m.viewport = viewport.New(80, 24)
	m.viewport.SetContent(m.renderContent())

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if updated.state != oauthIdle {
		t.Fatalf("state = %v, want oauthIdle", updated.state)
	}
	if updated.pollGeneration != 5 {
		t.Fatalf("pollGeneration = %d, want 5", updated.pollGeneration)
	}
	if updated.authState != "" || updated.authURL != "" || updated.deviceFlow {
		t.Fatalf("remote fields not cleared: state=%q url=%q device=%v", updated.authState, updated.authURL, updated.deviceFlow)
	}
	// client is nil, so cancel command should be nil
	if cmd != nil {
		t.Fatal("expected nil cancel command when client is nil")
	}
}

func TestOAuthTabEscWithActiveCallbackInputCancelsRemoteSession(t *testing.T) {
	m := newOAuthTabModel(nil)
	m.state = oauthRemote
	m.authState = "state-to-cancel"
	m.authURL = "https://example.com"
	m.deviceFlow = false
	m.inputActive = true
	m.callbackInput.Focus()
	m.callbackInput.SetValue("https://callback.example/?code=abc&state=state-to-cancel")
	m.pollGeneration = 7
	m.ready = true
	m.viewport = viewport.New(80, 24)
	m.viewport.SetContent(m.renderContent())

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if updated.state != oauthIdle {
		t.Fatalf("state = %v, want oauthIdle", updated.state)
	}
	if updated.pollGeneration != 8 {
		t.Fatalf("pollGeneration = %d, want 8", updated.pollGeneration)
	}
	if updated.inputActive {
		t.Fatal("inputActive still true after esc cancel")
	}
	if updated.callbackInput.Value() != "" {
		t.Fatalf("callback input not cleared: %q", updated.callbackInput.Value())
	}
	if updated.authState != "" || updated.authURL != "" {
		t.Fatalf("remote fields not cleared: state=%q url=%q", updated.authState, updated.authURL)
	}
	// client is nil, so cancel command should be nil
	if cmd != nil {
		t.Fatal("expected nil cancel command when client is nil")
	}
}

func TestOAuthTabStaleStartIsIgnored(t *testing.T) {
	m := newOAuthTabModel(nil)
	m.state = oauthIdle
	m.pollGeneration = 2
	m.ready = true
	m.viewport = viewport.New(80, 24)
	m.viewport.SetContent(m.renderContent())

	updated, cmd := m.Update(oauthStartMsg{
		url:        "https://example.com",
		state:      "stale-state",
		generation: 1,
	})
	if updated.state != oauthIdle {
		t.Fatalf("state = %v, want oauthIdle after stale start", updated.state)
	}
	// client is nil in this unit test; cancel is skipped but state remains idle.
	if cmd != nil {
		t.Fatal("expected nil cancel command when client is nil")
	}
	if updated.authState != "" {
		t.Fatalf("stale start should not set authState, got %q", updated.authState)
	}
}
