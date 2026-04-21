// Package misc provides miscellaneous utility functions for the CLI Proxy API server.
// It includes helper functions for HTTP header manipulation and other common operations
// that don't fit into more specific packages.
package misc

import (
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
)

const (
	// GeminiCLIVersion is the version string reported in the User-Agent for upstream requests.
	GeminiCLIVersion = "0.31.0"

	// GeminiCLIApiClientHeader is the value for the X-Goog-Api-Client header sent to the Gemini CLI upstream.
	GeminiCLIApiClientHeader = "google-genai-sdk/1.41.0 gl-node/v22.19.0"

	// CodexCLIVersion is the upstream-compatible Codex CLI version embedded in
	// the User-Agent. Keep in sync with the upstream release the proxy aims to
	// mimic; picking a slightly-behind release minimizes the risk of upstream
	// rejecting unknown clients while still matching a real CLI build.
	CodexCLIVersion = "0.118.0-alpha.4"

	// CodexDefaultOriginator mirrors codex-rs's DEFAULT_ORIGINATOR. Changing this
	// value will affect both the "Originator" header and the User-Agent token
	// emitted when neither a caller- nor config-supplied override is present.
	CodexDefaultOriginator = "codex_cli_rs"

	// CodexCLIOriginator is kept as the public default Originator name because
	// the executor code already consumes this symbol directly.
	CodexCLIOriginator = CodexDefaultOriginator

	// CodexOriginatorEnvVar is the env var name codex-rs itself honours. We
	// accept the same variable so operators can point the proxy at internal
	// originator values (e.g. "codex_vscode") without touching YAML.
	CodexOriginatorEnvVar = "CODEX_INTERNAL_ORIGINATOR_OVERRIDE"

	// CodexResidencyEnvVar lets operators set the residency header without
	// editing config.
	CodexResidencyEnvVar = "CODEX_INTERNAL_RESIDENCY_OVERRIDE"

	// CodexResidencyHeader is the residency hint header upstream honours.
	CodexResidencyHeader = "x-openai-internal-codex-residency"

	// CodexSubagentHeader is the sub-agent hint header upstream honours.
	CodexSubagentHeader      = "x-openai-subagent"
	codexCLIFallbackTerminal = "xterm-256color"
)

var (
	codexCLIOSOnce      sync.Once
	codexCLIOSCached    string
	codexTerminalOnce   sync.Once
	codexTerminalCached string
	codexUserAgentCache sync.Map

	// CodexCLIUserAgent is the default Codex CLI-style fingerprint used when no client-specific
	// User-Agent is available during login or execution.
	CodexCLIUserAgent = DefaultCodexCLIUserAgent()
)

// BuildCodexUserAgent renders a Codex CLI User-Agent using the given build
// version and the current runtime's OS / architecture / terminal. It never
// panics and always returns a syntactically valid User-Agent string.
func BuildCodexUserAgent(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		version = CodexCLIVersion
	}
	return fmt.Sprintf(
		"%s/%s (%s; %s) %s",
		CodexCLIOriginator,
		version,
		codexCLIOS(),
		codexCLIArch(),
		codexTerminal(),
	)
}

// codexOSDescriptor maps runtime.GOOS to the human-readable OS token codex-rs
// emits via os_info.os_type(). We intentionally omit OS version (which codex-rs
// includes as "Mac OS 14.6.1" for example) because there is no portable way to
// obtain it without CGO or shelling out, and an inaccurate version is worse
// than none: the proxy should not misrepresent the host kernel.
func codexOSDescriptor() string {
	switch runtime.GOOS {
	case "darwin":
		return "Mac OS"
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	case "freebsd", "netbsd", "openbsd", "dragonfly":
		// Capitalize to match os_info convention, e.g. "FreeBSD".
		if len(runtime.GOOS) == 0 {
			return "Unknown"
		}
		return strings.ToUpper(runtime.GOOS[:1]) + runtime.GOOS[1:]
	default:
		return runtime.GOOS
	}
}

// codexArchDescriptor returns the architecture token using the same naming
// convention as os_info / uname: "x86_64", "arm64", etc.
func codexArchDescriptor() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "386":
		return "i686"
	case "arm64":
		return "arm64"
	case "arm":
		return "armv7l"
	default:
		return runtime.GOARCH
	}
}

// codexTerminalDescriptor approximates codex-rs's terminal_detection::user_agent().
// It prefers TERM_PROGRAM (set by iTerm2, vscode, Apple Terminal, ...) in
// "name/version" form when TERM_PROGRAM_VERSION is also present, otherwise
// falls back to $TERM (e.g. "xterm-256color"). Returns "unknown" if neither is
// available.
func codexTerminalDescriptor() string {
	program := strings.TrimSpace(os.Getenv("TERM_PROGRAM"))
	version := strings.TrimSpace(os.Getenv("TERM_PROGRAM_VERSION"))
	if program != "" {
		token := sanitizeTerminalToken(program)
		if version != "" {
			token = token + "/" + sanitizeTerminalToken(version)
		}
		return token
	}
	if term := strings.TrimSpace(os.Getenv("TERM")); term != "" {
		return sanitizeTerminalToken(term)
	}
	return "unknown"
}

// sanitizeTerminalToken strips characters that would produce an invalid HTTP
// header value. Whitespace and non-printable bytes are replaced with '_', which
// matches codex-rs's sanitize_user_agent fallback.
func sanitizeTerminalToken(in string) string {
	if in == "" {
		return ""
	}
	b := make([]byte, 0, len(in))
	for i := 0; i < len(in); i++ {
		c := in[i]
		if c < 0x20 || c == 0x7f || c == ' ' || c == '\t' {
			b = append(b, '_')
			continue
		}
		b = append(b, c)
	}
	return string(b)
}

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

// DefaultCodexCLIUserAgent returns the fallback Codex CLI-style User-Agent used by
// the proxy when the downstream request does not provide one.
func DefaultCodexCLIUserAgent() string {
	return CodexCLIUserAgentWithOriginator(CodexCLIOriginator)
}

// CodexCLIUserAgentWithOriginator returns the fallback Codex-style User-Agent for the
// provided Originator value.
func CodexCLIUserAgentWithOriginator(originator string) string {
	originator = codexNormalizedOriginator(originator)
	if cached, ok := codexUserAgentCache.Load(originator); ok {
		return cached.(string)
	}
	userAgent := fmt.Sprintf(
		"%s/%s (%s; %s) %s",
		originator,
		CodexCLIVersion,
		codexCLIOS(),
		codexCLIArch(),
		codexTerminal(),
	)
	cached, _ := codexUserAgentCache.LoadOrStore(originator, userAgent)
	return cached.(string)
}

func codexNormalizedOriginator(originator string) string {
	if trimmed := strings.TrimSpace(originator); trimmed != "" {
		return trimmed
	}
	return CodexCLIOriginator
}

func codexCLIOS() string {
	codexCLIOSOnce.Do(func() {
		switch runtime.GOOS {
		case "linux":
			if distro := codexLinuxOSDescriptor(os.ReadFile); distro != "" {
				codexCLIOSCached = distro
				return
			}
			codexCLIOSCached = "Linux"
		case "darwin":
			codexCLIOSCached = "Mac OS"
		case "windows":
			codexCLIOSCached = "Windows"
		default:
			codexCLIOSCached = runtime.GOOS
		}
	})
	return codexCLIOSCached
}

func codexCLIArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "386":
		return "x86"
	default:
		return runtime.GOARCH
	}
}

func codexTerminal() string {
	codexTerminalOnce.Do(func() {
		codexTerminalCached = codexTerminalFromEnv(func(key string) string {
			return os.Getenv(key)
		})
	})
	return codexTerminalCached
}

func codexTerminalFromEnv(getenv func(string) string) string {
	if getenv == nil {
		return codexCLIFallbackTerminal
	}

	if termProgram := strings.TrimSpace(getenv("TERM_PROGRAM")); termProgram != "" {
		version := strings.TrimSpace(getenv("TERM_PROGRAM_VERSION"))
		if version == "" && strings.EqualFold(termProgram, "VTE") {
			version = strings.TrimSpace(getenv("VTE_VERSION"))
		}
		return codexSanitizeTerminalToken(codexFormatTerminalToken(termProgram, version))
	}

	if vteVersion := strings.TrimSpace(getenv("VTE_VERSION")); vteVersion != "" {
		return codexSanitizeTerminalToken(codexFormatTerminalToken("VTE", vteVersion))
	}
	if term := strings.TrimSpace(getenv("TERM")); term != "" {
		return codexSanitizeTerminalToken(term)
	}
	return codexCLIFallbackTerminal
}

func codexFormatTerminalToken(name string, version string) string {
	name = strings.TrimSpace(name)
	version = strings.TrimSpace(version)
	if name == "" {
		return codexCLIFallbackTerminal
	}
	if version == "" {
		return name
	}
	return name + "/" + version
}

func codexSanitizeTerminalToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return codexCLIFallbackTerminal
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.', r == '/':
			return r
		default:
			return '_'
		}
	}, value)
}

func codexLinuxOSDescriptor(readFile func(string) ([]byte, error)) string {
	if readFile == nil {
		return ""
	}
	data, err := readFile("/etc/os-release")
	if err != nil {
		return ""
	}

	values := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		values[key] = strings.Trim(value, `"'`)
	}

	name := strings.TrimSpace(values["NAME"])
	versionID := strings.TrimSpace(values["VERSION_ID"])
	prettyName := strings.TrimSpace(values["PRETTY_NAME"])
	switch {
	case name != "" && versionID != "":
		return name + " " + versionID
	case prettyName != "":
		return prettyName
	case name != "":
		return name
	default:
		return ""
	}
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

// ResolveCodexOriginator returns the effective originator string, honouring
// (in decreasing priority): an explicit config-provided value, the
// CODEX_INTERNAL_ORIGINATOR_OVERRIDE environment variable, and finally the
// built-in DEFAULT_ORIGINATOR. Invalid values (non-ASCII or illegal header
// characters) are discarded and the function falls back to the default so we
// never emit a malformed header.
//
// This matches the precedence implemented in codex-rs
// default_client::get_originator_value.
func ResolveCodexOriginator(configured string) string {
	if v := strings.TrimSpace(configured); v != "" && isValidHeaderValue(v) {
		return v
	}
	if v := strings.TrimSpace(os.Getenv(CodexOriginatorEnvVar)); v != "" && isValidHeaderValue(v) {
		return v
	}
	return CodexDefaultOriginator
}

// ResolveCodexResidency returns the residency header value following the same
// precedence as ResolveCodexOriginator. Unlike originator, an empty result is a
// valid outcome — the caller should not set the header in that case, which
// mirrors codex-rs's behaviour of only emitting the residency header when a
// value is configured.
func ResolveCodexResidency(configured string) string {
	if v := strings.TrimSpace(configured); v != "" && isValidHeaderValue(v) {
		return v
	}
	if v := strings.TrimSpace(os.Getenv(CodexResidencyEnvVar)); v != "" && isValidHeaderValue(v) {
		return v
	}
	return ""
}

// isValidHeaderValue returns true when s would be accepted by
// net/http as a header value. We keep the check permissive (RFC 7230 VCHAR
// plus SP and HTAB) because codex-rs also sanitizes only obviously-bad bytes.
func isValidHeaderValue(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\t' {
			continue
		}
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
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
