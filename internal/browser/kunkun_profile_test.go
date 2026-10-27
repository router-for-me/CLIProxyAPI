package browser

import (
	"context"
	"strings"
	"testing"
)

func TestDeriveKunKunProfileIDIsDeterministic(t *testing.T) {
	const proxyURL = "http://user:secret@127.0.0.1:8080"

	first := DeriveKunKunProfileID("claude", proxyURL)
	second := DeriveKunKunProfileID("claude", proxyURL)
	if first != second {
		t.Fatalf("DeriveKunKunProfileID() = %q then %q, want identical values", first, second)
	}
	if !strings.HasPrefix(first, "claude-kunkun-") {
		t.Fatalf("DeriveKunKunProfileID() = %q, want claude-kunkun- prefix", first)
	}
	const hashLength = 12
	if got := len(first) - len("claude-kunkun-"); got != hashLength {
		t.Fatalf("hash segment length = %d, want %d (%q)", got, hashLength, first)
	}
}

func TestDeriveKunKunProfileIDChangesWithProviderOrProxy(t *testing.T) {
	const proxyURL = "http://127.0.0.1:8080"
	base := DeriveKunKunProfileID("claude", proxyURL)

	if other := DeriveKunKunProfileID("codex", proxyURL); other == base {
		t.Fatalf("provider change produced the same id %q", base)
	}
	if other := DeriveKunKunProfileID("claude", "http://127.0.0.1:8081"); other == base {
		t.Fatalf("proxy change produced the same id %q", base)
	}
}

func TestDeriveKunKunProfileIDCanonicalizesAnthropic(t *testing.T) {
	const proxyURL = "http://127.0.0.1:8080"
	anthropic := DeriveKunKunProfileID("anthropic", proxyURL)
	claude := DeriveKunKunProfileID("claude", proxyURL)
	if anthropic != claude {
		t.Fatalf("anthropic id = %q, claude id = %q, want the same sandbox", anthropic, claude)
	}
}

func TestDeriveKunKunProfileIDWithoutProxyIsRandom(t *testing.T) {
	first := DeriveKunKunProfileID("claude", "")
	second := DeriveKunKunProfileID("claude", "")
	if first == second {
		t.Fatalf("DeriveKunKunProfileID() without proxy = %q twice, want unique values", first)
	}
	if !strings.HasPrefix(first, "claude-") {
		t.Fatalf("DeriveKunKunProfileID() = %q, want claude- prefix", first)
	}
	if !strings.HasPrefix(DeriveKunKunProfileID("", ""), "oauth-") {
		t.Fatal("empty provider should fall back to oauth- prefix")
	}
}

func TestOpenURLWithContextDerivesProfileIDFromSessionContext(t *testing.T) {
	clearLauncherAfterTest(t)
	withFakeSystemOpen(t, nil, nil)

	var received LaunchOptions
	SetLauncher(launcherFunc(func(_ context.Context, opts LaunchOptions) error {
		received = opts
		return nil
	}))

	const proxyURL = "http://user:secret@proxy.example:8080"
	ctx := WithOAuthProvider(context.Background(), "claude")
	ctx = WithOAuthProxyURL(ctx, proxyURL)

	if errOpen := OpenURLWithContext(ctx, "https://auth.example.com/x?state=1"); errOpen != nil {
		t.Fatalf("OpenURLWithContext() error = %v, want nil", errOpen)
	}
	want := DeriveKunKunProfileID("claude", proxyURL)
	if received.ProfileID != want {
		t.Fatalf("ProfileID = %q, want derived %q", received.ProfileID, want)
	}
}

func TestOpenURLWithContextWithoutSessionContextUsesRandomProfileID(t *testing.T) {
	clearLauncherAfterTest(t)
	withFakeSystemOpen(t, nil, nil)

	seen := make([]string, 0, 2)
	SetLauncher(launcherFunc(func(_ context.Context, opts LaunchOptions) error {
		seen = append(seen, opts.ProfileID)
		return nil
	}))

	for i := 0; i < 2; i++ {
		if errOpen := OpenURLWithContext(context.Background(), "https://auth.example.com/x"); errOpen != nil {
			t.Fatalf("OpenURLWithContext() error = %v, want nil", errOpen)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("launcher calls = %d, want 2", len(seen))
	}
	if seen[0] == seen[1] {
		t.Fatalf("profile IDs = %q, want unique values without session context", seen[0])
	}
	if !strings.HasPrefix(seen[0], "oauth-") {
		t.Fatalf("ProfileID = %q, want oauth- prefix", seen[0])
	}
}
