// Package kiro provides custom protocol handler registration for Kiro OAuth.
// This enables the CLI to intercept kiro:// URIs for social authentication (Google/GitHub).
package kiro

import (
	"context"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	// KiroProtocol is the custom URI scheme used by Kiro
	KiroProtocol = "kiro"

	// KiroAuthority is the URI authority for authentication callbacks
	KiroAuthority = "kiro.kiroAgent"

	// KiroAuthPath is the path for successful authentication
	KiroAuthPath = "/authenticate-success"

	// KiroRedirectURI is the full redirect URI for social auth
	KiroRedirectURI = "kiro://kiro.kiroAgent/authenticate-success"

	// DefaultHandlerPort is the default port for the local callback server
	DefaultHandlerPort = 19876

	// HandlerTimeout is how long to wait for the OAuth callback
	HandlerTimeout = 10 * time.Minute
)

// ProtocolHandler manages the custom kiro:// protocol handler for OAuth callbacks.
type ProtocolHandler struct {
	port       int
	server     *http.Server
	listener   net.Listener
	resultChan chan *AuthCallback
	stopChan   chan struct{}
	mu         sync.Mutex
	running    bool
}

// AuthCallback contains the OAuth callback parameters.
type AuthCallback struct {
	Code  string
	State string
	Error string
}

// NewProtocolHandler creates a new protocol handler.
func NewProtocolHandler() *ProtocolHandler {
	return &ProtocolHandler{
		port:       DefaultHandlerPort,
		resultChan: make(chan *AuthCallback, 1),
		stopChan:   make(chan struct{}),
	}
}

// Start starts the local callback server that receives redirects from the protocol handler.
func (h *ProtocolHandler) Start(ctx context.Context) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.running {
		return h.port, nil
	}

	// Drain any stale results from previous runs
	select {
	case <-h.resultChan:
	default:
	}

	// Reset stopChan for reuse - close old channel first to unblock any waiting goroutines
	if h.stopChan != nil {
		select {
		case <-h.stopChan:
			// Already closed
		default:
			close(h.stopChan)
		}
	}
	h.stopChan = make(chan struct{})

	// Try ports in known range (must match handler script port range)
	var listener net.Listener
	var err error
	portRange := []int{DefaultHandlerPort, DefaultHandlerPort + 1, DefaultHandlerPort + 2, DefaultHandlerPort + 3, DefaultHandlerPort + 4}
	
	for _, port := range portRange {
		listener, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			break
		}
		log.Debugf("kiro protocol handler: port %d busy, trying next", port)
	}
	
	if listener == nil {
		return 0, fmt.Errorf("failed to start callback server: all ports %d-%d are busy", DefaultHandlerPort, DefaultHandlerPort+4)
	}

	h.listener = listener
	h.port = listener.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/callback", h.handleCallback)

	h.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		if err := h.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Debugf("kiro protocol handler server error: %v", err)
		}
	}()

	h.running = true
	log.Debugf("kiro protocol handler started on port %d", h.port)

	// Auto-shutdown after context done, timeout, or explicit stop
	// Capture references to prevent race with new Start() calls
	currentStopChan := h.stopChan
	currentServer := h.server
	currentListener := h.listener
	go func() {
		select {
		case <-ctx.Done():
		case <-time.After(HandlerTimeout):
		case <-currentStopChan:
			return // Already stopped, exit goroutine
		}
		// Only stop if this is still the current server/listener instance
		h.mu.Lock()
		if h.server == currentServer && h.listener == currentListener {
			h.mu.Unlock()
			h.Stop()
		} else {
			h.mu.Unlock()
		}
	}()

	return h.port, nil
}

// Stop stops the callback server.
func (h *ProtocolHandler) Stop() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.running {
		return
	}

	// Signal the auto-shutdown goroutine to exit.
	// This select pattern is safe because stopChan is only modified while holding h.mu,
	// and we hold the lock here. The select prevents panic from double-close.
	select {
	case <-h.stopChan:
		// Already closed
	default:
		close(h.stopChan)
	}

	if h.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.server.Shutdown(ctx)
	}

	h.running = false
	log.Debug("kiro protocol handler stopped")
}

// WaitForCallback waits for the OAuth callback and returns the result.
func (h *ProtocolHandler) WaitForCallback(ctx context.Context) (*AuthCallback, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(HandlerTimeout):
		return nil, fmt.Errorf("timeout waiting for OAuth callback")
	case result := <-h.resultChan:
		return result, nil
	}
}

// GetPort returns the port the handler is listening on.
func (h *ProtocolHandler) GetPort() int {
	return h.port
}

// handleCallback processes the OAuth callback from the protocol handler script.
func (h *ProtocolHandler) handleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	errParam := r.URL.Query().Get("error")

	result := &AuthCallback{
		Code:  code,
		State: state,
		Error: errParam,
	}

	// Send result
	select {
	case h.resultChan <- result:
	default:
		// Channel full, ignore duplicate callbacks
	}

	// Send success response
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if errParam != "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>Login Failed</title></head>
<body>
<h1>Login Failed</h1>
<p>Error: %s</p>
<p>You can close this window.</p>
</body>
</html>`, html.EscapeString(errParam))
	} else {
		fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head><title>Login Successful</title></head>
<body>
<h1>Login Successful!</h1>
<p>You can close this window and return to the terminal.</p>
<script>window.close();</script>
</body>
</html>`)
	}
}

// IsProtocolHandlerInstalled checks if the kiro:// protocol handler is installed.
func IsProtocolHandlerInstalled() bool {
	switch runtime.GOOS {
	case "linux":
		return isLinuxHandlerInstalled()
	case "windows":
		return isWindowsHandlerInstalled()
	case "darwin":
		return isDarwinHandlerInstalled()
	default:
		return false
	}
}

// InstallProtocolHandler installs the kiro:// protocol handler for the current platform.
func InstallProtocolHandler(handlerPort int) error {
	switch runtime.GOOS {
	case "linux":
		return installLinuxHandler(handlerPort)
	case "windows":
		return installWindowsHandler(handlerPort)
	case "darwin":
		return installDarwinHandler(handlerPort)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

// UninstallProtocolHandler removes the kiro:// protocol handler.
func UninstallProtocolHandler() error {
	switch runtime.GOOS {
	case "linux":
		return uninstallLinuxHandler()
	case "windows":
		return uninstallWindowsHandler()
	case "darwin":
		return uninstallDarwinHandler()
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

// --- Linux Implementation ---

func getLinuxDesktopPath() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".local", "share", "applications", "kiro-oauth-handler.desktop")
}

func getLinuxHandlerScriptPath() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".local", "bin", "kiro-oauth-handler")
}

func isLinuxHandlerInstalled() bool {
	desktopPath := getLinuxDesktopPath()
	_, err := os.Stat(desktopPath)
	return err == nil
}

func installLinuxHandler(handlerPort int) error {
	// Create directories
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	binDir := filepath.Join(homeDir, ".local", "bin")
	appDir := filepath.Join(homeDir, ".local", "share", "applications")

	if err := os.MkdirAll(binDir, 0755); err != nil {
		return fmt.Errorf("failed to create bin directory: %w", err)
	}
	if err := os.MkdirAll(appDir, 0755); err != nil {
		return fmt.Errorf("failed to create applications directory: %w", err)
	}

	// Create handler script - tries multiple ports to handle dynamic port allocation
	scriptPath := getLinuxHandlerScriptPath()
	scriptContent := fmt.Sprintf(`#!/bin/bash
# Kiro OAuth Protocol Handler
# Handles kiro:// URIs - tries CLI first, then forwards to Kiro IDE

URL="$1"

# Check curl availability
if ! command -v curl &> /dev/null; then
    echo "Error: curl is required for Kiro OAuth handler" >&2
    exit 1
fi

# Extract code and state from URL
[[ "$URL" =~ code=([^&]+) ]] && CODE="${BASH_REMATCH[1]}"
[[ "$URL" =~ state=([^&]+) ]] && STATE="${BASH_REMATCH[1]}"
[[ "$URL" =~ error=([^&]+) ]] && ERROR="${BASH_REMATCH[1]}"

# Try CLI proxy on multiple possible ports (default + dynamic range)
CLI_OK=0
for PORT in %d %d %d %d %d; do
    if [ -n "$ERROR" ]; then
        curl -sf --connect-timeout 1 "http://127.0.0.1:$PORT/oauth/callback?error=$ERROR" && CLI_OK=1 && break
    elif [ -n "$CODE" ] && [ -n "$STATE" ]; then
        curl -sf --connect-timeout 1 "http://127.0.0.1:$PORT/oauth/callback?code=$CODE&state=$STATE" && CLI_OK=1 && break
    fi
done

# If CLI not available, forward to Kiro IDE
if [ $CLI_OK -eq 0 ] && [ -x "/usr/share/kiro/kiro" ]; then
    /usr/share/kiro/kiro --open-url "$URL" &
fi
`, handlerPort, handlerPort+1, handlerPort+2, handlerPort+3, handlerPort+4)

	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		return fmt.Errorf("failed to write handler script: %w", err)
	}

	// Create .desktop file
	desktopPath := getLinuxDesktopPath()
	desktopContent := fmt.Sprintf(`[Desktop Entry]
Name=Kiro OAuth Handler
Comment=Handle kiro:// protocol for CLI Proxy API authentication
Exec=%s %%u
Type=Application
Terminal=false
NoDisplay=true
MimeType=x-scheme-handler/kiro;
Categories=Utility;
`, scriptPath)

	if err := os.WriteFile(desktopPath, []byte(desktopContent), 0644); err != nil {
		return fmt.Errorf("failed to write desktop file: %w", err)
	}

	// Register handler with xdg-mime
	cmd := exec.Command("xdg-mime", "default", "kiro-oauth-handler.desktop", "x-scheme-handler/kiro")
	if err := cmd.Run(); err != nil {
		log.Warnf("xdg-mime registration failed (may need manual setup): %v", err)
	}

	// Update desktop database
	cmd = exec.Command("update-desktop-database", appDir)
	_ = cmd.Run() // Ignore errors, not critical

	log.Info("Kiro protocol handler installed for Linux")
	return nil
}

func uninstallLinuxHandler() error {
	desktopPath := getLinuxDesktopPath()
	scriptPath := getLinuxHandlerScriptPath()

	if err := os.Remove(desktopPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove desktop file: %w", err)
	}
	if err := os.Remove(scriptPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove handler script: %w", err)
	}

	log.Info("Kiro protocol handler uninstalled")
	return nil
}

// --- Windows Implementation ---

func isWindowsHandlerInstalled() bool {
	// Check registry key existence
	cmd := exec.Command("reg", "query", `HKCU\Software\Classes\kiro`, "/ve")
	return cmd.Run() == nil
}

func installWindowsHandler(handlerPort int) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	// Create handler script (PowerShell)
	scriptDir := filepath.Join(homeDir, ".cliproxyapi")
	if err := os.MkdirAll(scriptDir, 0755); err != nil {
		return fmt.Errorf("failed to create script directory: %w", err)
	}

	scriptPath := filepath.Join(scriptDir, "kiro-oauth-handler.ps1")
	scriptContent := fmt.Sprintf(`# Kiro OAuth Protocol Handler for Windows
param([string]$url)

# Load required assembly for HttpUtility
Add-Type -AssemblyName System.Web

# Parse URL parameters
$uri = [System.Uri]$url
$query = [System.Web.HttpUtility]::ParseQueryString($uri.Query)
$code = $query["code"]
$state = $query["state"]
$errorParam = $query["error"]

# Try multiple ports (default + dynamic range)
$ports = @(%d, %d, %d, %d, %d)
$success = $false

foreach ($port in $ports) {
    if ($success) { break }
    $callbackUrl = "http://127.0.0.1:$port/oauth/callback"
    try {
        if ($errorParam) {
            $fullUrl = $callbackUrl + "?error=" + $errorParam
            Invoke-WebRequest -Uri $fullUrl -UseBasicParsing -TimeoutSec 1 -ErrorAction Stop | Out-Null
            $success = $true
        } elseif ($code -and $state) {
            $fullUrl = $callbackUrl + "?code=" + $code + "&state=" + $state
            Invoke-WebRequest -Uri $fullUrl -UseBasicParsing -TimeoutSec 1 -ErrorAction Stop | Out-Null
            $success = $true
        }
    } catch {
        # Try next port
    }
}
`, handlerPort, handlerPort+1, handlerPort+2, handlerPort+3, handlerPort+4)

	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0644); err != nil {
		return fmt.Errorf("failed to write handler script: %w", err)
	}

	// Create batch wrapper
	batchPath := filepath.Join(scriptDir, "kiro-oauth-handler.bat")
	batchContent := fmt.Sprintf("@echo off\npowershell -ExecutionPolicy Bypass -File \"%s\" %%1\n", scriptPath)

	if err := os.WriteFile(batchPath, []byte(batchContent), 0644); err != nil {
		return fmt.Errorf("failed to write batch wrapper: %w", err)
	}

	// Register in Windows registry
	commands := [][]string{
		{"reg", "add", `HKCU\Software\Classes\kiro`, "/ve", "/d", "URL:Kiro Protocol", "/f"},
		{"reg", "add", `HKCU\Software\Classes\kiro`, "/v", "URL Protocol", "/d", "", "/f"},
		{"reg", "add", `HKCU\Software\Classes\kiro\shell`, "/f"},
		{"reg", "add", `HKCU\Software\Classes\kiro\shell\open`, "/f"},
		{"reg", "add", `HKCU\Software\Classes\kiro\shell\open\command`, "/ve", "/d", fmt.Sprintf("\"%s\" \"%%1\"", batchPath), "/f"},
	}

	for _, args := range commands {
		cmd := exec.Command(args[0], args[1:]...)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to run registry command: %w", err)
		}
	}

	log.Info("Kiro protocol handler installed for Windows")
	return nil
}

func uninstallWindowsHandler() error {
	// Remove registry keys
	cmd := exec.Command("reg", "delete", `HKCU\Software\Classes\kiro`, "/f")
	if err := cmd.Run(); err != nil {
		log.Warnf("failed to remove registry key: %v", err)
	}

	// Remove scripts
	homeDir, _ := os.UserHomeDir()
	scriptDir := filepath.Join(homeDir, ".cliproxyapi")
	_ = os.Remove(filepath.Join(scriptDir, "kiro-oauth-handler.ps1"))
	_ = os.Remove(filepath.Join(scriptDir, "kiro-oauth-handler.bat"))

	log.Info("Kiro protocol handler uninstalled")
	return nil
}

// --- macOS Implementation ---

func getDarwinAppPath() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, "Applications", "KiroOAuthHandler.app")
}

func isDarwinHandlerInstalled() bool {
	appPath := getDarwinAppPath()
	_, err := os.Stat(appPath)
	return err == nil
}

func installDarwinHandler(handlerPort int) error {
	// Create app bundle structure
	appPath := getDarwinAppPath()
	contentsPath := filepath.Join(appPath, "Contents")
	macOSPath := filepath.Join(contentsPath, "MacOS")

	if err := os.MkdirAll(macOSPath, 0755); err != nil {
		return fmt.Errorf("failed to create app bundle: %w", err)
	}

	// Create Info.plist
	plistPath := filepath.Join(contentsPath, "Info.plist")
	plistContent := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleIdentifier</key>
    <string>com.cliproxyapi.kiro-oauth-handler</string>
    <key>CFBundleName</key>
    <string>KiroOAuthHandler</string>
    <key>CFBundleExecutable</key>
    <string>kiro-oauth-handler</string>
    <key>CFBundleVersion</key>
    <string>1.0</string>
    <key>CFBundleURLTypes</key>
    <array>
        <dict>
            <key>CFBundleURLName</key>
            <string>Kiro Protocol</string>
            <key>CFBundleURLSchemes</key>
            <array>
                <string>kiro</string>
            </array>
        </dict>
    </array>
    <key>LSBackgroundOnly</key>
    <true/>
</dict>
</plist>`

	if err := os.WriteFile(plistPath, []byte(plistContent), 0644); err != nil {
		return fmt.Errorf("failed to write Info.plist: %w", err)
	}

	// Create executable script - tries multiple ports to handle dynamic port allocation
	execPath := filepath.Join(macOSPath, "kiro-oauth-handler")
	execContent := fmt.Sprintf(`#!/bin/bash
# Kiro OAuth Protocol Handler for macOS

URL="$1"

# Check curl availability (should always exist on macOS)
if [ ! -x /usr/bin/curl ]; then
    echo "Error: curl is required for Kiro OAuth handler" >&2
    exit 1
fi

# Extract code and state from URL
[[ "$URL" =~ code=([^&]+) ]] && CODE="${BASH_REMATCH[1]}"
[[ "$URL" =~ state=([^&]+) ]] && STATE="${BASH_REMATCH[1]}"
[[ "$URL" =~ error=([^&]+) ]] && ERROR="${BASH_REMATCH[1]}"

# Try multiple ports (default + dynamic range)
for PORT in %d %d %d %d %d; do
    if [ -n "$ERROR" ]; then
        /usr/bin/curl -sf --connect-timeout 1 "http://127.0.0.1:$PORT/oauth/callback?error=$ERROR" && exit 0
    elif [ -n "$CODE" ] && [ -n "$STATE" ]; then
        /usr/bin/curl -sf --connect-timeout 1 "http://127.0.0.1:$PORT/oauth/callback?code=$CODE&state=$STATE" && exit 0
    fi
done
`, handlerPort, handlerPort+1, handlerPort+2, handlerPort+3, handlerPort+4)

	if err := os.WriteFile(execPath, []byte(execContent), 0755); err != nil {
		return fmt.Errorf("failed to write executable: %w", err)
	}

	// Register the app with Launch Services
	cmd := exec.Command("/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister",
		"-f", appPath)
	if err := cmd.Run(); err != nil {
		log.Warnf("lsregister failed (handler may still work): %v", err)
	}

	log.Info("Kiro protocol handler installed for macOS")
	return nil
}

func uninstallDarwinHandler() error {
	appPath := getDarwinAppPath()

	// Unregister from Launch Services
	cmd := exec.Command("/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister",
		"-u", appPath)
	_ = cmd.Run()

	// Remove app bundle
	if err := os.RemoveAll(appPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove app bundle: %w", err)
	}

	log.Info("Kiro protocol handler uninstalled")
	return nil
}

// ParseKiroURI parses a kiro:// URI and extracts the callback parameters.
func ParseKiroURI(rawURI string) (*AuthCallback, error) {
	u, err := url.Parse(rawURI)
	if err != nil {
		return nil, fmt.Errorf("invalid URI: %w", err)
	}

	if u.Scheme != KiroProtocol {
		return nil, fmt.Errorf("invalid scheme: expected %s, got %s", KiroProtocol, u.Scheme)
	}

	if u.Host != KiroAuthority {
		return nil, fmt.Errorf("invalid authority: expected %s, got %s", KiroAuthority, u.Host)
	}

	query := u.Query()
	return &AuthCallback{
		Code:  query.Get("code"),
		State: query.Get("state"),
		Error: query.Get("error"),
	}, nil
}

// GetHandlerInstructions returns platform-specific instructions for manual handler setup.
func GetHandlerInstructions() string {
	switch runtime.GOOS {
	case "linux":
		return `To manually set up the Kiro protocol handler on Linux:

1. Create ~/.local/share/applications/kiro-oauth-handler.desktop:
   [Desktop Entry]
   Name=Kiro OAuth Handler
   Exec=~/.local/bin/kiro-oauth-handler %u
   Type=Application
   Terminal=false
   MimeType=x-scheme-handler/kiro;

2. Create ~/.local/bin/kiro-oauth-handler (make it executable):
   #!/bin/bash
   URL="$1"
   # ... (see generated script for full content)

3. Run: xdg-mime default kiro-oauth-handler.desktop x-scheme-handler/kiro`

	case "windows":
		return `To manually set up the Kiro protocol handler on Windows:

1. Open Registry Editor (regedit.exe)
2. Create key: HKEY_CURRENT_USER\Software\Classes\kiro
3. Set default value to: URL:Kiro Protocol
4. Create string value "URL Protocol" with empty data
5. Create subkey: shell\open\command
6. Set default value to: "C:\path\to\handler.bat" "%1"`

	case "darwin":
		return `To manually set up the Kiro protocol handler on macOS:

1. Create ~/Applications/KiroOAuthHandler.app bundle
2. Add Info.plist with CFBundleURLTypes containing "kiro" scheme
3. Create executable in Contents/MacOS/
4. Run: /System/Library/.../lsregister -f ~/Applications/KiroOAuthHandler.app`

	default:
		return "Protocol handler setup is not supported on this platform."
	}
}

// SetupProtocolHandlerIfNeeded checks and installs the protocol handler if needed.
func SetupProtocolHandlerIfNeeded(handlerPort int) error {
	if IsProtocolHandlerInstalled() {
		log.Debug("Kiro protocol handler already installed")
		return nil
	}

	fmt.Println("\n╔══════════════════════════════════════════════════════════╗")
	fmt.Println("║       Kiro Protocol Handler Setup Required                 ║")
	fmt.Println("╚══════════════════════════════════════════════════════════╝")
	fmt.Println("\nTo enable Google/GitHub login, we need to install a protocol handler.")
	fmt.Println("This allows your browser to redirect back to the CLI after authentication.")
	fmt.Println("\nInstalling protocol handler...")

	if err := InstallProtocolHandler(handlerPort); err != nil {
		fmt.Printf("\n⚠ Automatic installation failed: %v\n", err)
		fmt.Println("\nManual setup instructions:")
		fmt.Println(strings.Repeat("-", 60))
		fmt.Println(GetHandlerInstructions())
		return err
	}

	fmt.Println("\n✓ Protocol handler installed successfully!")
	return nil
}
