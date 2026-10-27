// Package browser provides cross-platform functionality for opening URLs in the default web browser.
// It abstracts the underlying operating system commands and provides a simple interface.
package browser

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"github.com/skratchdot/open-golang/open"
)

// Launcher opens an OAuth authorization URL with an alternate browser backend.
// Implementations must honor ctx cancellation.
type Launcher interface {
	// Launch opens opts.AuthURL and blocks until the login finishes, fails, or
	// the context is done.
	Launch(ctx context.Context, opts LaunchOptions) error
}

// oauthSessionProxyContextKey carries the OAuth session proxy URL to the launcher.
type oauthSessionProxyContextKey struct{}

// oauthProviderContextKey carries the OAuth provider name to the launcher.
type oauthProviderContextKey struct{}

// WithOAuthProxyURL attaches an OAuth session proxy URL to ctx.
// It is used to keep the login egress IP identical to the runtime egress IP.
func WithOAuthProxyURL(ctx context.Context, proxyURL string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, oauthSessionProxyContextKey{}, strings.TrimSpace(proxyURL))
}

// OAuthProxyURLFromContext returns the OAuth session proxy URL stored in ctx.
func OAuthProxyURLFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(oauthSessionProxyContextKey{}).(string)
	return strings.TrimSpace(value)
}

// WithOAuthProvider attaches the OAuth provider name to ctx so launchers can
// derive stable profile identifiers.
func WithOAuthProvider(ctx context.Context, provider string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, oauthProviderContextKey{}, strings.TrimSpace(provider))
}

// OAuthProviderFromContext returns the OAuth provider name stored in ctx.
func OAuthProviderFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(oauthProviderContextKey{}).(string)
	return strings.TrimSpace(value)
}

var (
	launcherMu sync.RWMutex
	launcher   Launcher

	openRunner   = open.Run
	platformOpen = openURLPlatformSpecific
)

// SetLauncher installs the OAuth browser launcher. Passing nil clears it.
func SetLauncher(next Launcher) {
	launcherMu.Lock()
	launcher = next
	launcherMu.Unlock()
}

// ClearLauncher removes any installed launcher.
func ClearLauncher() {
	SetLauncher(nil)
}

// HasLauncher reports whether an alternate OAuth browser launcher is installed.
func HasLauncher() bool {
	launcherMu.RLock()
	defer launcherMu.RUnlock()
	return launcher != nil
}

func currentLauncher() Launcher {
	launcherMu.RLock()
	defer launcherMu.RUnlock()
	return launcher
}

// OpenURL opens the specified URL in the default web browser.
// When an alternate launcher is installed it is tried first; a launcher failure
// logs a warning and falls back to the system browser so login always remains
// possible.
func OpenURL(url string) error {
	return OpenURLWithContext(context.Background(), url)
}

// OpenURLWithContext opens the specified URL, preferring an installed launcher.
// The context may carry the OAuth session proxy URL and provider name.
func OpenURLWithContext(ctx context.Context, url string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if current := currentLauncher(); current != nil {
		provider := OAuthProviderFromContext(ctx)
		proxyURL := OAuthProxyURLFromContext(ctx)
		opts := LaunchOptions{
			AuthURL:     url,
			ProxyURL:    proxyURL,
			ProfileID:   oauthProfileIDForContext(provider, proxyURL),
			DeviceMode:  "",
			Headless:    false,
			UserDataDir: "",
		}
		if errLaunch := current.Launch(ctx, opts); errLaunch != nil {
			log.Warnf("alternate OAuth browser launch failed, falling back to system browser: %v", errLaunch)
		} else {
			return nil
		}
	}
	return openSystemURL(url)
}

func openSystemURL(url string) error {
	fmt.Printf("Attempting to open URL in browser: %s\n", url)

	// Try using the open-golang library first
	err := openRunner(url)
	if err == nil {
		log.Debug("Successfully opened URL using open-golang library")
		return nil
	}

	log.Debugf("open-golang failed: %v, trying platform-specific commands", err)

	// Fallback to platform-specific commands
	return platformOpen(url)
}

// oauthProfileIDForContext returns a stable sandbox profile id when the OAuth
// session carries both a provider and a proxy. Without them (for example the CLI
// login paths, which do not have a session-scoped proxy) the legacy random
// provider-prefixed identifier is kept.
func oauthProfileIDForContext(provider, proxyURL string) string {
	if strings.TrimSpace(provider) != "" && strings.TrimSpace(proxyURL) != "" {
		return DeriveKunKunProfileID(provider, proxyURL)
	}
	return newOAuthProfileID(provider)
}

// newOAuthProfileID builds a random profile identifier with a provider prefix.
func newOAuthProfileID(provider string) string {
	prefix := sanitizeProfilePrefix(provider)
	if prefix == "" {
		prefix = "oauth"
	}
	return prefix + "-" + uuid.NewString()
}

func sanitizeProfilePrefix(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
		if b.Len() >= 32 {
			break
		}
	}
	return strings.Trim(b.String(), "-_")
}

// openURLPlatformSpecific is a helper function that opens a URL using OS-specific commands.
// This serves as a fallback mechanism for OpenURL.
//
// Parameters:
//   - url: The URL to open.
//
// Returns:
//   - An error if the URL cannot be opened, otherwise nil.
func openURLPlatformSpecific(url string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin": // macOS
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "linux":
		// Try common Linux browsers in order of preference
		browsers := []string{"xdg-open", "x-www-browser", "www-browser", "firefox", "chromium", "google-chrome"}
		for _, browser := range browsers {
			if _, err := exec.LookPath(browser); err == nil {
				cmd = exec.Command(browser, url)
				break
			}
		}
		if cmd == nil {
			return fmt.Errorf("no suitable browser found on Linux system")
		}
	default:
		return fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}

	log.Debugf("Running command: %s %v", cmd.Path, cmd.Args[1:])
	err := cmd.Start()
	if err != nil {
		return fmt.Errorf("failed to start browser command: %w", err)
	}

	log.Debug("Successfully opened URL using platform-specific command")
	return nil
}

// IsAvailable checks if the system has a command available to open a web browser.
// It verifies the presence of necessary commands for the current operating system.
//
// Returns:
//   - true if a browser can be opened, false otherwise.
func IsAvailable() bool {
	// An installed alternate launcher does not need a system browser.
	if HasLauncher() {
		return true
	}

	// First check if open-golang can work
	testErr := openRunner("about:blank")
	if testErr == nil {
		return true
	}

	// Check platform-specific commands
	switch runtime.GOOS {
	case "darwin":
		_, err := exec.LookPath("open")
		return err == nil
	case "windows":
		_, err := exec.LookPath("rundll32")
		return err == nil
	case "linux":
		browsers := []string{"xdg-open", "x-www-browser", "www-browser", "firefox", "chromium", "google-chrome"}
		for _, browser := range browsers {
			if _, err := exec.LookPath(browser); err == nil {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// GetPlatformInfo returns a map containing details about the current platform's
// browser opening capabilities, including the OS, architecture, and available commands.
//
// Returns:
//   - A map with platform-specific browser support information.
func GetPlatformInfo() map[string]interface{} {
	info := map[string]interface{}{
		"os":        runtime.GOOS,
		"arch":      runtime.GOARCH,
		"available": IsAvailable(),
	}

	switch runtime.GOOS {
	case "darwin":
		info["default_command"] = "open"
	case "windows":
		info["default_command"] = "rundll32"
	case "linux":
		browsers := []string{"xdg-open", "x-www-browser", "www-browser", "firefox", "chromium", "google-chrome"}
		var availableBrowsers []string
		for _, browser := range browsers {
			if _, err := exec.LookPath(browser); err == nil {
				availableBrowsers = append(availableBrowsers, browser)
			}
		}
		info["available_browsers"] = availableBrowsers
		if len(availableBrowsers) > 0 {
			info["default_command"] = availableBrowsers[0]
		}
	}

	return info
}
