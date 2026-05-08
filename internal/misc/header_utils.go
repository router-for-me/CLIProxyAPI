// Package misc provides miscellaneous utility functions for the CLI Proxy API server.
// It includes helper functions for HTTP header manipulation and other common operations
// that don't fit into more specific packages.
package misc

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"
)

const (
	// GeminiCLIVersion is the version string reported in the User-Agent for upstream requests.
	GeminiCLIVersion = "0.31.0"

	// GeminiCLIApiClientHeader is the value for the X-Goog-Api-Client header sent to the Gemini CLI upstream.
	GeminiCLIApiClientHeader = "google-genai-sdk/1.41.0 gl-node/v22.19.0"
)

// geminiCLIOS maps Go runtime OS names to the Node.js-style platform strings used by Gemini CLI.
func geminiCLIOS() string {
	switch runtime.GOOS {
	case "windows":
		return "win32"
	default:
		return runtime.GOOS
	}
}

// geminiCLIArch maps Go runtime architecture names to the Node.js-style arch strings used by Gemini CLI.
func geminiCLIArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "386":
		return "x86"
	default:
		return runtime.GOARCH
	}
}

// GeminiCLIUserAgent returns a User-Agent string that matches the Gemini CLI format.
// The model parameter is included in the UA; pass "" or "unknown" when the model is not applicable.
func GeminiCLIUserAgent(model string) string {
	if model == "" {
		model = "unknown"
	}
	return fmt.Sprintf("GeminiCLI/%s/%s (%s; %s)", GeminiCLIVersion, model, geminiCLIOS(), geminiCLIArch())
}

// ScrubProxyAndFingerprintHeaders removes all headers that could reveal
// proxy infrastructure, client identity, or browser fingerprints from an
// outgoing request. This ensures requests to upstream services look like they
// originate directly from a native client rather than a third-party client
// behind a reverse proxy.
func ScrubProxyAndFingerprintHeaders(req *http.Request) {
	if req == nil {
		return
	}

	// --- Proxy tracing headers ---
	req.Header.Del("X-Forwarded-For")
	req.Header.Del("X-Forwarded-Host")
	req.Header.Del("X-Forwarded-Proto")
	req.Header.Del("X-Forwarded-Port")
	req.Header.Del("X-Real-IP")
	req.Header.Del("Forwarded")
	req.Header.Del("Via")

	// --- Client identity headers ---
	req.Header.Del("X-Title")
	req.Header.Del("X-Stainless-Lang")
	req.Header.Del("X-Stainless-Package-Version")
	req.Header.Del("X-Stainless-Os")
	req.Header.Del("X-Stainless-Arch")
	req.Header.Del("X-Stainless-Runtime")
	req.Header.Del("X-Stainless-Runtime-Version")
	req.Header.Del("Http-Referer")
	req.Header.Del("Referer")

	// --- Browser / Chromium fingerprint headers ---
	// These are sent by Electron-based clients (e.g. CherryStudio) using the
	// Fetch API, but NOT by Node.js https module (which Antigravity uses).
	req.Header.Del("Sec-Ch-Ua")
	req.Header.Del("Sec-Ch-Ua-Mobile")
	req.Header.Del("Sec-Ch-Ua-Platform")
	req.Header.Del("Sec-Fetch-Mode")
	req.Header.Del("Sec-Fetch-Site")
	req.Header.Del("Sec-Fetch-Dest")
	req.Header.Del("Priority")

	// --- Encoding negotiation ---
	// Antigravity (Node.js) sends "gzip, deflate, br" by default;
	// Electron-based clients may add "zstd" which is a fingerprint mismatch.
	req.Header.Del("Accept-Encoding")
}

// EnsureHeader ensures that a header exists in the target header map by checking
// multiple sources in order of priority: source headers, existing target headers,
// and finally the default value. It only sets the header if it's not already present
// and the value is not empty after trimming whitespace.
//
// Parameters:
//   - target: The target header map to modify
//   - source: The source header map to check first (can be nil)
//   - key: The header key to ensure
//   - defaultValue: The default value to use if no other source provides a value
func EnsureHeader(target http.Header, source http.Header, key, defaultValue string) {
	if target == nil {
		return
	}
	if source != nil {
		if val := strings.TrimSpace(source.Get(key)); val != "" {
			target.Set(key, val)
			return
		}
	}
	if strings.TrimSpace(target.Get(key)) != "" {
		return
	}
	if val := strings.TrimSpace(defaultValue); val != "" {
		target.Set(key, val)
	}
}
