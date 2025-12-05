// Package browser provides cross-platform functionality for opening URLs in the default web browser.
// It abstracts the underlying operating system commands and provides a simple interface.
package browser

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	pkgbrowser "github.com/pkg/browser"
	log "github.com/sirupsen/logrus"
)

// incognitoMode controls whether to open URLs in incognito/private mode.
// This is useful for OAuth flows where you want to use a different account.
var incognitoMode bool

// lastBrowserProcess stores the last opened browser process for cleanup
var lastBrowserProcess *exec.Cmd
var browserMutex sync.Mutex

// SetIncognitoMode enables or disables incognito/private browsing mode.
func SetIncognitoMode(enabled bool) {
	incognitoMode = enabled
}

// IsIncognitoMode returns whether incognito mode is enabled.
func IsIncognitoMode() bool {
	return incognitoMode
}

// CloseBrowser closes the last opened browser process.
func CloseBrowser() error {
	browserMutex.Lock()
	defer browserMutex.Unlock()

	if lastBrowserProcess == nil || lastBrowserProcess.Process == nil {
		return nil
	}
	
	err := lastBrowserProcess.Process.Kill()
	lastBrowserProcess = nil
	return err
}

// OpenURL opens the specified URL in the default web browser.
// It uses the pkg/browser library which provides robust cross-platform support
// for Windows, macOS, and Linux.
// If incognito mode is enabled, it will open in a private/incognito window.
//
// Parameters:
//   - url: The URL to open.
//
// Returns:
//   - An error if the URL cannot be opened, otherwise nil.
func OpenURL(url string) error {
	log.Debugf("Opening URL in browser: %s (incognito=%v)", url, incognitoMode)

	// If incognito mode is enabled, use platform-specific incognito commands
	if incognitoMode {
		log.Debug("Using incognito mode")
		return openURLIncognito(url)
	}

	// Use pkg/browser for cross-platform support
	err := pkgbrowser.OpenURL(url)
	if err == nil {
		log.Debug("Successfully opened URL using pkg/browser library")
		return nil
	}

	log.Debugf("pkg/browser failed: %v, trying platform-specific commands", err)

	// Fallback to platform-specific commands
	return openURLPlatformSpecific(url)
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

// openURLIncognito opens a URL in incognito/private browsing mode.
// It first tries to detect the default browser and use its incognito flag.
// Falls back to a chain of known browsers if detection fails.
//
// Parameters:
//   - url: The URL to open.
//
// Returns:
//   - An error if the URL cannot be opened, otherwise nil.
func openURLIncognito(url string) error {
	// First, try to detect and use the default browser
	if cmd := tryDefaultBrowserIncognito(url); cmd != nil {
		log.Debugf("Using detected default browser: %s %v", cmd.Path, cmd.Args[1:])
		if err := cmd.Start(); err == nil {
			storeBrowserProcess(cmd)
			log.Debug("Successfully opened URL in default browser's incognito mode")
			return nil
		}
		log.Debugf("Failed to start default browser, trying fallback chain")
	}

	// Fallback to known browser chain
	cmd := tryFallbackBrowsersIncognito(url)
	if cmd == nil {
		log.Warn("No browser with incognito support found, falling back to normal mode")
		return openURLPlatformSpecific(url)
	}

	log.Debugf("Running incognito command: %s %v", cmd.Path, cmd.Args[1:])
	err := cmd.Start()
	if err != nil {
		log.Warnf("Failed to open incognito browser: %v, falling back to normal mode", err)
		return openURLPlatformSpecific(url)
	}

	storeBrowserProcess(cmd)
	log.Debug("Successfully opened URL in incognito/private mode")
	return nil
}

// storeBrowserProcess safely stores the browser process for later cleanup.
func storeBrowserProcess(cmd *exec.Cmd) {
	browserMutex.Lock()
	lastBrowserProcess = cmd
	browserMutex.Unlock()
}

// tryDefaultBrowserIncognito attempts to detect the default browser and return
// an exec.Cmd configured with the appropriate incognito flag.
func tryDefaultBrowserIncognito(url string) *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		return tryDefaultBrowserMacOS(url)
	case "windows":
		return tryDefaultBrowserWindows(url)
	case "linux":
		return tryDefaultBrowserLinux(url)
	}
	return nil
}

// tryDefaultBrowserMacOS detects the default browser on macOS.
func tryDefaultBrowserMacOS(url string) *exec.Cmd {
	// Try to get default browser from Launch Services
	out, err := exec.Command("defaults", "read", "com.apple.LaunchServices/com.apple.launchservices.secure", "LSHandlers").Output()
	if err != nil {
		return nil
	}

	output := string(out)
	var browserName string

	// Parse the output to find the http/https handler
	if containsBrowserID(output, "com.google.chrome") {
		browserName = "chrome"
	} else if containsBrowserID(output, "org.mozilla.firefox") {
		browserName = "firefox"
	} else if containsBrowserID(output, "com.apple.safari") {
		browserName = "safari"
	} else if containsBrowserID(output, "com.brave.browser") {
		browserName = "brave"
	} else if containsBrowserID(output, "com.microsoft.edgemac") {
		browserName = "edge"
	}

	return createMacOSIncognitoCmd(browserName, url)
}

// containsBrowserID checks if the LaunchServices output contains a browser ID.
func containsBrowserID(output, bundleID string) bool {
	return strings.Contains(output, bundleID)
}

// createMacOSIncognitoCmd creates the appropriate incognito command for macOS browsers.
func createMacOSIncognitoCmd(browserName, url string) *exec.Cmd {
	switch browserName {
	case "chrome":
		// Try direct path first
		chromePath := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
		if _, err := exec.LookPath(chromePath); err == nil {
			return exec.Command(chromePath, "--incognito", url)
		}
		return exec.Command("open", "-na", "Google Chrome", "--args", "--incognito", url)
	case "firefox":
		return exec.Command("open", "-na", "Firefox", "--args", "--private-window", url)
	case "safari":
		// Safari doesn't have CLI incognito, try AppleScript
		return tryAppleScriptSafariPrivate(url)
	case "brave":
		return exec.Command("open", "-na", "Brave Browser", "--args", "--incognito", url)
	case "edge":
		return exec.Command("open", "-na", "Microsoft Edge", "--args", "--inprivate", url)
	}
	return nil
}

// tryAppleScriptSafariPrivate attempts to open Safari in private browsing mode using AppleScript.
func tryAppleScriptSafariPrivate(url string) *exec.Cmd {
	// AppleScript to open a new private window in Safari
	script := fmt.Sprintf(`
		tell application "Safari"
			activate
			tell application "System Events"
				keystroke "n" using {command down, shift down}
				delay 0.5
			end tell
			set URL of document 1 to "%s"
		end tell
	`, url)

	cmd := exec.Command("osascript", "-e", script)
	// Test if this approach works by checking if Safari is available
	if _, err := exec.LookPath("/Applications/Safari.app/Contents/MacOS/Safari"); err != nil {
		log.Debug("Safari not found, AppleScript private window not available")
		return nil
	}
	log.Debug("Attempting Safari private window via AppleScript")
	return cmd
}

// tryDefaultBrowserWindows detects the default browser on Windows via registry.
func tryDefaultBrowserWindows(url string) *exec.Cmd {
	// Query registry for default browser
	out, err := exec.Command("reg", "query",
		`HKEY_CURRENT_USER\Software\Microsoft\Windows\Shell\Associations\UrlAssociations\http\UserChoice`,
		"/v", "ProgId").Output()
	if err != nil {
		return nil
	}

	output := string(out)
	var browserName string

	// Map ProgId to browser name
	if strings.Contains(output, "ChromeHTML") {
		browserName = "chrome"
	} else if strings.Contains(output, "FirefoxURL") {
		browserName = "firefox"
	} else if strings.Contains(output, "MSEdgeHTM") {
		browserName = "edge"
	} else if strings.Contains(output, "BraveHTML") {
		browserName = "brave"
	}

	return createWindowsIncognitoCmd(browserName, url)
}

// createWindowsIncognitoCmd creates the appropriate incognito command for Windows browsers.
func createWindowsIncognitoCmd(browserName, url string) *exec.Cmd {
	switch browserName {
	case "chrome":
		paths := []string{
			"chrome",
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		}
		for _, p := range paths {
			if _, err := exec.LookPath(p); err == nil {
				return exec.Command(p, "--incognito", url)
			}
		}
	case "firefox":
		if path, err := exec.LookPath("firefox"); err == nil {
			return exec.Command(path, "--private-window", url)
		}
	case "edge":
		paths := []string{
			"msedge",
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		}
		for _, p := range paths {
			if _, err := exec.LookPath(p); err == nil {
				return exec.Command(p, "--inprivate", url)
			}
		}
	case "brave":
		paths := []string{
			`C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe`,
			`C:\Program Files (x86)\BraveSoftware\Brave-Browser\Application\brave.exe`,
		}
		for _, p := range paths {
			if _, err := exec.LookPath(p); err == nil {
				return exec.Command(p, "--incognito", url)
			}
		}
	}
	return nil
}

// tryDefaultBrowserLinux detects the default browser on Linux using xdg-settings.
func tryDefaultBrowserLinux(url string) *exec.Cmd {
	out, err := exec.Command("xdg-settings", "get", "default-web-browser").Output()
	if err != nil {
		return nil
	}

	desktop := string(out)
	var browserName string

	// Map .desktop file to browser name
	if strings.Contains(desktop, "google-chrome") || strings.Contains(desktop, "chrome") {
		browserName = "chrome"
	} else if strings.Contains(desktop, "firefox") {
		browserName = "firefox"
	} else if strings.Contains(desktop, "chromium") {
		browserName = "chromium"
	} else if strings.Contains(desktop, "brave") {
		browserName = "brave"
	} else if strings.Contains(desktop, "microsoft-edge") || strings.Contains(desktop, "msedge") {
		browserName = "edge"
	}

	return createLinuxIncognitoCmd(browserName, url)
}

// createLinuxIncognitoCmd creates the appropriate incognito command for Linux browsers.
func createLinuxIncognitoCmd(browserName, url string) *exec.Cmd {
	switch browserName {
	case "chrome":
		paths := []string{"google-chrome", "google-chrome-stable"}
		for _, p := range paths {
			if path, err := exec.LookPath(p); err == nil {
				return exec.Command(path, "--incognito", url)
			}
		}
	case "firefox":
		paths := []string{"firefox", "firefox-esr"}
		for _, p := range paths {
			if path, err := exec.LookPath(p); err == nil {
				return exec.Command(path, "--private-window", url)
			}
		}
	case "chromium":
		paths := []string{"chromium", "chromium-browser"}
		for _, p := range paths {
			if path, err := exec.LookPath(p); err == nil {
				return exec.Command(path, "--incognito", url)
			}
		}
	case "brave":
		if path, err := exec.LookPath("brave-browser"); err == nil {
			return exec.Command(path, "--incognito", url)
		}
	case "edge":
		if path, err := exec.LookPath("microsoft-edge"); err == nil {
			return exec.Command(path, "--inprivate", url)
		}
	}
	return nil
}

// tryFallbackBrowsersIncognito tries a chain of known browsers as fallback.
func tryFallbackBrowsersIncognito(url string) *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		return tryFallbackBrowsersMacOS(url)
	case "windows":
		return tryFallbackBrowsersWindows(url)
	case "linux":
		return tryFallbackBrowsersLinuxChain(url)
	}
	return nil
}

// tryFallbackBrowsersMacOS tries known browsers on macOS.
func tryFallbackBrowsersMacOS(url string) *exec.Cmd {
	// Try Chrome
	chromePath := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	if _, err := exec.LookPath(chromePath); err == nil {
		return exec.Command(chromePath, "--incognito", url)
	}
	// Try Firefox
	if _, err := exec.LookPath("/Applications/Firefox.app/Contents/MacOS/firefox"); err == nil {
		return exec.Command("open", "-na", "Firefox", "--args", "--private-window", url)
	}
	// Try Brave
	if _, err := exec.LookPath("/Applications/Brave Browser.app/Contents/MacOS/Brave Browser"); err == nil {
		return exec.Command("open", "-na", "Brave Browser", "--args", "--incognito", url)
	}
	// Try Edge
	if _, err := exec.LookPath("/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge"); err == nil {
		return exec.Command("open", "-na", "Microsoft Edge", "--args", "--inprivate", url)
	}
	// Last resort: try Safari with AppleScript
	if cmd := tryAppleScriptSafariPrivate(url); cmd != nil {
		log.Info("Using Safari with AppleScript for private browsing (may require accessibility permissions)")
		return cmd
	}
	return nil
}

// tryFallbackBrowsersWindows tries known browsers on Windows.
func tryFallbackBrowsersWindows(url string) *exec.Cmd {
	// Chrome
	chromePaths := []string{
		"chrome",
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	}
	for _, p := range chromePaths {
		if _, err := exec.LookPath(p); err == nil {
			return exec.Command(p, "--incognito", url)
		}
	}
	// Firefox
	if path, err := exec.LookPath("firefox"); err == nil {
		return exec.Command(path, "--private-window", url)
	}
	// Edge (usually available on Windows 10+)
	edgePaths := []string{
		"msedge",
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
	}
	for _, p := range edgePaths {
		if _, err := exec.LookPath(p); err == nil {
			return exec.Command(p, "--inprivate", url)
		}
	}
	return nil
}

// tryFallbackBrowsersLinuxChain tries known browsers on Linux.
func tryFallbackBrowsersLinuxChain(url string) *exec.Cmd {
	type browserConfig struct {
		name string
		flag string
	}
	browsers := []browserConfig{
		{"google-chrome", "--incognito"},
		{"google-chrome-stable", "--incognito"},
		{"chromium", "--incognito"},
		{"chromium-browser", "--incognito"},
		{"firefox", "--private-window"},
		{"firefox-esr", "--private-window"},
		{"brave-browser", "--incognito"},
		{"microsoft-edge", "--inprivate"},
	}
	for _, b := range browsers {
		if path, err := exec.LookPath(b.name); err == nil {
			return exec.Command(path, b.flag, url)
		}
	}
	return nil
}

// IsAvailable checks if the system has a command available to open a web browser.
// It verifies the presence of necessary commands for the current operating system.
//
// Returns:
//   - true if a browser can be opened, false otherwise.
func IsAvailable() bool {
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
