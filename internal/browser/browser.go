// Package browser provides cross-platform functionality for opening URLs in the default web browser.
// It abstracts the underlying operating system commands and provides a simple interface.
package browser

import (
	"fmt"
	"net/url"
	"os/exec"
	"runtime"

	log "github.com/sirupsen/logrus"
	"github.com/skratchdot/open-golang/open"
)

// OpenURL opens the specified URL in the default web browser.
// It validates the URL scheme and uses a platform-agnostic library to open it.
//
// Parameters:
//   - rawURL: The URL to open.
//
// Returns:
//   - An error if the URL cannot be opened, otherwise nil.
func OpenURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("invalid URL: only http and https schemes are allowed")
	}

	fmt.Printf("Attempting to open URL in browser: %s\n", parsed.String())

	if err := open.Run(parsed.String()); err != nil {
		log.Debugf("open-golang failed: %v", err)
		return fmt.Errorf("failed to open URL in browser: %w", err)
	}

	log.Debug("Successfully opened URL using open-golang library")
	return nil
}

// IsAvailable checks if the system has a command available to open a web browser.
// It verifies the presence of necessary commands for the current operating system.
//
// Returns:
//   - true if a browser can be opened, false otherwise.
func IsAvailable() bool {
	// First check if open-golang can work
	testErr := open.Run("about:blank")
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
