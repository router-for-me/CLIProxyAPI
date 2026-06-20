package helps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const (
	defaultCommandAuthTimeout         = 5 * time.Second
	defaultCommandAuthRefreshInterval = 5 * time.Minute
	commandAuthOutputLimit            = 64 * 1024
)

// ShouldPrepareCommandAuth reports whether a command-backed auth needs a fresh token.
func ShouldPrepareCommandAuth(auth *cliproxyauth.Auth) bool {
	if !cliproxyauth.IsCommandAuth(auth) {
		return false
	}
	if strings.TrimSpace(commandAuthAccessToken(auth)) == "" {
		return true
	}
	if auth.NextRefreshAfter.IsZero() {
		return true
	}
	return !time.Now().Before(auth.NextRefreshAfter)
}

// PrepareCommandAuth executes the configured command and stores the bearer token in auth metadata.
func PrepareCommandAuth(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if !cliproxyauth.IsCommandAuth(auth) {
		return auth, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	attrs := auth.Attributes
	command := strings.TrimSpace(attrs[cliproxyauth.AttrAuthCommand])
	if command == "" {
		return auth, fmt.Errorf("command auth: command is empty")
	}
	args, errArgs := commandAuthArgs(attrs[cliproxyauth.AttrAuthArgsJSON])
	if errArgs != nil {
		return auth, errArgs
	}
	timeout := commandAuthDuration(attrs[cliproxyauth.AttrAuthTimeoutMS], defaultCommandAuthTimeout)
	refreshInterval := commandAuthDuration(attrs[cliproxyauth.AttrAuthRefreshIntervalMS], defaultCommandAuthRefreshInterval)

	runCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	resolvedCommand := resolveCommandAuthExecutable(command)
	cmd := exec.CommandContext(runCtx, resolvedCommand, args...)
	var stdout limitedBuffer
	var stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	errRun := cmd.Run()
	if errRun != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return auth, fmt.Errorf("command auth: command timed out after %s", timeout)
		}
		errText := strings.TrimSpace(stderr.String())
		if errText == "" {
			return auth, fmt.Errorf("command auth: command failed: %w", errRun)
		}
		return auth, fmt.Errorf("command auth: command failed: %w: %s", errRun, errText)
	}

	token, errToken := ParseCommandAuthBearerToken(stdout.Bytes())
	if errToken != nil {
		return auth, errToken
	}

	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	now := time.Now().UTC()
	updated.Metadata["access_token"] = token
	updated.LastRefreshedAt = now
	updated.NextRefreshAfter = now.Add(refreshInterval)
	updated.UpdatedAt = now
	return updated, nil
}

func resolveCommandAuthExecutable(command string) string {
	command = strings.TrimSpace(command)
	if command == "" || strings.ContainsAny(command, `/\`) {
		return command
	}
	if path, errLookPath := exec.LookPath(command); errLookPath == nil {
		return path
	}
	for _, dir := range commandAuthUserBinDirs() {
		path := filepath.Join(dir, command)
		if isExecutableFile(path) {
			return path
		}
		if runtime.GOOS == "windows" {
			for _, ext := range []string{".exe", ".cmd", ".bat"} {
				if isExecutableFile(path + ext) {
					return path + ext
				}
			}
		}
	}
	return command
}

func commandAuthUserBinDirs() []string {
	home := strings.TrimSpace(os.Getenv("HOME"))
	if home == "" {
		if userProfile := strings.TrimSpace(os.Getenv("USERPROFILE")); userProfile != "" {
			home = userProfile
		}
	}
	if home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, ".npm-global", "bin"),
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, "bin"),
	}
}

func isExecutableFile(path string) bool {
	info, errStat := os.Stat(path)
	if errStat != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode()&0o111 != 0
}

// ParseCommandAuthBearerToken extracts a bearer token from command stdout.
func ParseCommandAuthBearerToken(output []byte) (string, error) {
	var fallbackToken string
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		token := strings.TrimSpace(string(line))
		if token == "" {
			continue
		}
		if len(token) >= len("Bearer ") && strings.EqualFold(token[:len("Bearer ")], "Bearer ") {
			return strings.TrimSpace(token[len("Bearer "):]), nil
		}
		if fallbackToken == "" {
			fallbackToken = token
		}
	}
	if fallbackToken != "" {
		return fallbackToken, nil
	}
	return "", fmt.Errorf("command auth: command produced empty stdout")
}

func commandAuthAccessToken(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if token, ok := auth.Metadata["access_token"].(string); ok {
		return strings.TrimSpace(token)
	}
	return ""
}

func commandAuthArgs(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, fmt.Errorf("command auth: parse args: %w", err)
	}
	return args, nil
}

func commandAuthDuration(raw string, fallback time.Duration) time.Duration {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return fallback
	}
	return time.Duration(value) * time.Millisecond
}

type limitedBuffer struct {
	buf bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	accepted := len(p)
	remaining := commandAuthOutputLimit - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = b.buf.Write(p[:remaining])
		} else {
			_, _ = b.buf.Write(p)
		}
	}
	return accepted, nil
}

func (b *limitedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

func (b *limitedBuffer) String() string {
	return b.buf.String()
}
