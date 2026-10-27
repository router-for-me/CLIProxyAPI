package browser

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const (
	oauthStatusPrefix   = "OAUTH_STATUS:"
	defaultLaunchLimit  = 5 * time.Minute
	oauthStatusComplete = "completed"
	oauthStatusFailed   = "failed"
	oauthStatusClosed   = "closed"
)

// LaunchOptions describes a single KunKun OAuth browser launch.
type LaunchOptions struct {
	// AuthURL is the OAuth authorization URL to open.
	AuthURL string
	// ProxyURL is the resolved egress proxy. Empty means direct ("-" on the CLI).
	ProxyURL string
	// ProfileID identifies the browser profile. Empty generates a random one.
	ProfileID string
	// DeviceMode selects KunKun's device fingerprint mode.
	DeviceMode string
	// Headless asks KunKun to run without a visible window.
	Headless bool
	// UserDataDir overrides the per-profile browser data directory.
	UserDataDir string
	// CallbackPrefix tells KunKun when to auto-finish. Empty derives it from AuthURL.
	CallbackPrefix string
}

// KunKunDriver launches the KunKun fingerprint browser for OAuth logins.
type KunKunDriver struct {
	cfg config.KunKunBrowserConfig
}

// NewKunKunDriver constructs a driver for the supplied configuration.
func NewKunKunDriver(cfg config.KunKunBrowserConfig) *KunKunDriver {
	cfg.Normalize()
	return &KunKunDriver{cfg: cfg}
}

// Launch runs KunKun and waits for the OAuth login to finish.
func (d *KunKunDriver) Launch(ctx context.Context, opts LaunchOptions) error {
	if d == nil {
		return fmt.Errorf("kunkun oauth browser driver is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	mode := config.NormalizeKunKunDriverMode(d.cfg.DriverMode)
	switch mode {
	case config.KunKunDriverModeCLI:
	case config.KunKunDriverModeService:
		return fmt.Errorf("kunkun-browser: driver mode %q is not implemented", mode)
	default:
		return fmt.Errorf("kunkun-browser: unsupported driver mode %q", d.cfg.DriverMode)
	}

	bin, args, userDataDir, errArgs := d.launchArguments(opts)
	if errArgs != nil {
		return errArgs
	}
	if errMkdir := os.MkdirAll(userDataDir, 0o700); errMkdir != nil {
		return fmt.Errorf("create kunkun profile directory %q: %w", userDataDir, errMkdir)
	}

	timeout := d.launchTimeout()
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, bin, args...)
	cmd.Dir = strings.TrimSpace(d.cfg.ProjectRoot)
	configureCommandProcessGroup(cmd)
	cmd.Cancel = func() error { return killCommandProcessGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second

	stdout, errStdout := cmd.StdoutPipe()
	if errStdout != nil {
		return fmt.Errorf("open kunkun oauth browser stdout: %w", errStdout)
	}
	stderr := &tailBuffer{limit: 8 << 10}
	cmd.Stderr = stderr

	proxyForLog := strings.TrimSpace(opts.ProxyURL)
	log.WithFields(log.Fields{
		"project_root": cmd.Dir,
		"profile_id":   opts.ProfileID,
		"device_mode":  deviceModeForLaunch(d.cfg.DefaultDeviceMode, opts.DeviceMode),
		"headless":     opts.Headless || d.cfg.Headless,
		"proxy":        proxyutil.Redact(proxyForLog),
		"timeout":      timeout.String(),
	}).Info("launching KunKun OAuth browser")

	if errStart := cmd.Start(); errStart != nil {
		return fmt.Errorf("start kunkun oauth browser: %w", errStart)
	}

	status, errScan := scanOAuthStatus(stdout)
	waitErr := cmd.Wait()

	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("kunkun oauth browser timed out after %s", timeout)
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return fmt.Errorf("kunkun oauth browser canceled: %w", errCtx)
	}
	if errScan != nil {
		return fmt.Errorf("read kunkun oauth browser status: %w", errScan)
	}

	exitCode := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return fmt.Errorf("wait for kunkun oauth browser: %w", waitErr)
		}
	}

	switch status {
	case oauthStatusComplete:
		return nil
	case oauthStatusClosed:
		return fmt.Errorf("kunkun oauth browser was closed before authorization completed")
	case oauthStatusFailed:
		return fmt.Errorf("kunkun oauth browser reported authorization failure")
	}

	if exitCode != 0 {
		detail := strings.TrimSpace(stderr.String())
		if len(proxyForLog) > 0 && proxyForLog != "-" {
			detail = strings.ReplaceAll(detail, proxyForLog, proxyutil.Redact(proxyForLog))
		}
		if detail != "" {
			return fmt.Errorf("kunkun oauth browser exited with code %d: %s", exitCode, detail)
		}
		return fmt.Errorf("kunkun oauth browser exited with code %d", exitCode)
	}
	return nil
}

// launchArguments builds the frozen cross-repository command contract.
func (d *KunKunDriver) launchArguments(opts LaunchOptions) (string, []string, string, error) {
	authURL := strings.TrimSpace(opts.AuthURL)
	if authURL == "" {
		return "", nil, "", fmt.Errorf("kunkun oauth browser requires an authorization URL")
	}

	bin := strings.TrimSpace(d.cfg.PythonBin)
	if bin == "" {
		bin = config.DefaultKunKunPythonBin
	}

	args := make([]string, 0, 16)
	if isUVBinary(bin) {
		args = append(args, "run", "python")
	}
	args = append(args, "main.py", "--oauth-launch", "--url", authURL)

	proxy := strings.TrimSpace(opts.ProxyURL)
	if proxy == "" {
		proxy = "-"
	}
	args = append(args, "--proxy", proxy)

	profileID := strings.TrimSpace(opts.ProfileID)
	if profileID == "" {
		profileID = "oauth-" + uuid.NewString()
	}
	args = append(args, "--profile-id", profileID)

	args = append(args, "--device-mode", deviceModeForLaunch(d.cfg.DefaultDeviceMode, opts.DeviceMode))

	userDataDir := strings.TrimSpace(opts.UserDataDir)
	if userDataDir == "" {
		profilesDir := strings.TrimSpace(d.cfg.ProfilesDir)
		if profilesDir == "" {
			profilesDir = config.DefaultKunKunProfilesDir
		}
		userDataDir = filepath.Join(profilesDir, profileID)
	}
	absoluteDir, errAbs := filepath.Abs(userDataDir)
	if errAbs != nil {
		return "", nil, "", fmt.Errorf("resolve kunkun profile directory %q: %w", userDataDir, errAbs)
	}
	args = append(args, "--user-data-dir", absoluteDir)

	if opts.Headless || d.cfg.Headless {
		args = append(args, "--headless")
	}

	callbackPrefix := strings.TrimSpace(opts.CallbackPrefix)
	if callbackPrefix == "" {
		callbackPrefix = callbackPrefixFromAuthURL(authURL)
	}
	if callbackPrefix != "" {
		args = append(args, "--callback-prefix", callbackPrefix)
	}

	return bin, args, absoluteDir, nil
}

func (d *KunKunDriver) launchTimeout() time.Duration {
	if d != nil && d.cfg.LaunchTimeoutSeconds > 0 {
		return time.Duration(d.cfg.LaunchTimeoutSeconds) * time.Second
	}
	return defaultLaunchLimit
}

func deviceModeForLaunch(configured, requested string) string {
	if strings.TrimSpace(requested) != "" {
		return config.NormalizeKunKunDeviceMode(requested)
	}
	return config.NormalizeKunKunDeviceMode(configured)
}

func isUVBinary(bin string) bool {
	base := strings.TrimSuffix(filepath.Base(bin), ".exe")
	return strings.EqualFold(base, "uv")
}

// callbackPrefixFromAuthURL extracts redirect_uri from an authorization URL.
// KunKun auto-finishes when the page URL matches this prefix.
func callbackPrefixFromAuthURL(authURL string) string {
	parsed, errParse := url.Parse(strings.TrimSpace(authURL))
	if errParse != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Query().Get("redirect_uri"))
}

// scanOAuthStatus reads stdout line by line and returns the last status line.
func scanOAuthStatus(stdout io.Reader) (string, error) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	status := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, oauthStatusPrefix) {
			continue
		}
		switch value := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, oauthStatusPrefix))); value {
		case oauthStatusComplete, oauthStatusFailed, oauthStatusClosed:
			status = value
		}
	}
	return status, scanner.Err()
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	limit int
	data  []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	if b.limit > 0 && len(b.data) > b.limit {
		b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	if b == nil {
		return ""
	}
	return string(b.data)
}
