package browser

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type launcherFunc func(ctx context.Context, opts LaunchOptions) error

func (f launcherFunc) Launch(ctx context.Context, opts LaunchOptions) error {
	return f(ctx, opts)
}

func withFakeSystemOpen(t *testing.T, openErr, platformErr error) (openCalls *int, platformCalls *int) {
	t.Helper()
	previousOpen := openRunner
	previousPlatform := platformOpen
	openCount := 0
	platformCount := 0
	openRunner = func(string) error {
		openCount++
		return openErr
	}
	platformOpen = func(string) error {
		platformCount++
		return platformErr
	}
	t.Cleanup(func() {
		openRunner = previousOpen
		platformOpen = previousPlatform
	})
	return &openCount, &platformCount
}

func clearLauncherAfterTest(t *testing.T) {
	t.Helper()
	previous := currentLauncher()
	t.Cleanup(func() { SetLauncher(previous) })
	SetLauncher(nil)
}

func TestOpenURLWithContextPrefersLauncher(t *testing.T) {
	clearLauncherAfterTest(t)
	openCalls, platformCalls := withFakeSystemOpen(t, nil, nil)

	var received LaunchOptions
	SetLauncher(launcherFunc(func(_ context.Context, opts LaunchOptions) error {
		received = opts
		return nil
	}))

	ctx := WithOAuthProxyURL(context.Background(), "http://user:secret@proxy.example:8080")
	ctx = WithOAuthProvider(ctx, "Claude")
	errOpen := OpenURLWithContext(ctx, "https://auth.example.com/x?state=1")
	if errOpen != nil {
		t.Fatalf("OpenURLWithContext() error = %v, want nil", errOpen)
	}
	if *openCalls != 0 || *platformCalls != 0 {
		t.Fatalf("system browser was used (open=%d platform=%d), want launcher only", *openCalls, *platformCalls)
	}
	if received.AuthURL != "https://auth.example.com/x?state=1" {
		t.Fatalf("AuthURL = %q", received.AuthURL)
	}
	if received.ProxyURL != "http://user:secret@proxy.example:8080" {
		t.Fatalf("ProxyURL = %q, want session proxy", received.ProxyURL)
	}
	if !strings.HasPrefix(received.ProfileID, "claude-") {
		t.Fatalf("ProfileID = %q, want claude- prefix", received.ProfileID)
	}
}

func TestOpenURLWithContextFallsBackWhenLauncherFails(t *testing.T) {
	clearLauncherAfterTest(t)
	openCalls, platformCalls := withFakeSystemOpen(t, nil, nil)

	SetLauncher(launcherFunc(func(context.Context, LaunchOptions) error {
		return errors.New("kunkun unavailable")
	}))

	if errOpen := OpenURLWithContext(context.Background(), "https://auth.example.com/x"); errOpen != nil {
		t.Fatalf("OpenURLWithContext() error = %v, want fallback success", errOpen)
	}
	if *openCalls != 1 {
		t.Fatalf("open.Run calls = %d, want 1", *openCalls)
	}
	if *platformCalls != 0 {
		t.Fatalf("platform fallback calls = %d, want 0", *platformCalls)
	}
}

func TestOpenURLWithContextFallsBackToPlatformCommand(t *testing.T) {
	clearLauncherAfterTest(t)
	openCalls, platformCalls := withFakeSystemOpen(t, errors.New("no handler"), nil)

	SetLauncher(launcherFunc(func(context.Context, LaunchOptions) error {
		return errors.New("kunkun unavailable")
	}))

	if errOpen := OpenURLWithContext(context.Background(), "https://auth.example.com/x"); errOpen != nil {
		t.Fatalf("OpenURLWithContext() error = %v, want platform fallback success", errOpen)
	}
	if *openCalls != 1 {
		t.Fatalf("open.Run calls = %d, want 1", *openCalls)
	}
	if *platformCalls != 1 {
		t.Fatalf("platform fallback calls = %d, want 1", *platformCalls)
	}
}

func TestOpenURLWithoutLauncherUsesSystemBrowser(t *testing.T) {
	clearLauncherAfterTest(t)
	openCalls, platformCalls := withFakeSystemOpen(t, nil, nil)

	if errOpen := OpenURL("https://auth.example.com/x"); errOpen != nil {
		t.Fatalf("OpenURL() error = %v, want nil", errOpen)
	}
	if *openCalls != 1 || *platformCalls != 0 {
		t.Fatalf("open.Run calls = %d, platform = %d, want 1/0", *openCalls, *platformCalls)
	}
}

func TestOpenURLReturnsSystemBrowserError(t *testing.T) {
	clearLauncherAfterTest(t)
	withFakeSystemOpen(t, errors.New("open failed"), errors.New("platform failed"))

	SetLauncher(launcherFunc(func(context.Context, LaunchOptions) error {
		return errors.New("kunkun unavailable")
	}))

	errOpen := OpenURL("https://auth.example.com/x")
	if errOpen == nil || !strings.Contains(errOpen.Error(), "platform failed") {
		t.Fatalf("OpenURL() error = %v, want platform failure", errOpen)
	}
}

func TestIsAvailableWithLauncher(t *testing.T) {
	clearLauncherAfterTest(t)
	withFakeSystemOpen(t, errors.New("no browser"), errors.New("no browser"))

	SetLauncher(launcherFunc(func(context.Context, LaunchOptions) error { return nil }))
	if !IsAvailable() {
		t.Fatal("IsAvailable() = false with launcher, want true")
	}
}

func TestNewOAuthProfileID(t *testing.T) {
	first := newOAuthProfileID("Claude")
	second := newOAuthProfileID("Claude")
	if !strings.HasPrefix(first, "claude-") || !strings.HasPrefix(second, "claude-") {
		t.Fatalf("profile IDs = %q, %q, want claude- prefix", first, second)
	}
	if first == second {
		t.Fatalf("profile IDs = %q, want unique values", first)
	}
	if !strings.HasPrefix(newOAuthProfileID(""), "oauth-") {
		t.Fatal("empty provider should fall back to oauth- prefix")
	}
}
