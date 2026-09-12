package claudemaster

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

const settingsByteLimit = 1024 * 1024

// ValidateNativeSettings is a read-only STARTUP preflight, not a filesystem or network fence.
// Native Claude can reload files, fetch new server policies, or change directories after this check.
// We preserve its settings/permissions/hooks instead of disabling their sources. Native MDM policies
// are not JSON files and cannot be covered by this bounded file validator.
func ValidateNativeSettings() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return errors.New("cannot locate native user settings")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return errors.New("cannot locate native project settings")
	}
	roots, err := nativeProjectRoots(cwd)
	if err != nil {
		return err
	}
	managed := "/etc/claude-code"
	if runtime.GOOS == "darwin" {
		managed = "/Library/Application Support/ClaudeCode"
	}
	return validateSettingsAt(home, roots, managed)
}

func validateSettingsAt(home string, roots []string, managed string) error {
	paths := []string{
		filepath.Join(home, ".claude", "settings.json"),
		// The last fetched policy is also a native settings source. Fresh policies still arrive later.
		filepath.Join(home, ".claude", "remote-settings.json"),
		filepath.Join(managed, "managed-settings.json"),
	}
	for _, root := range roots {
		paths = append(paths, filepath.Join(root, ".claude", "settings.json"), filepath.Join(root, ".claude", "settings.local.json"))
	}
	dropDir := filepath.Join(managed, "managed-settings.d")
	info, err := os.Lstat(dropDir)
	if err != nil && !os.IsNotExist(err) {
		return errors.New("cannot inspect managed settings drop-ins")
	}
	if err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("managed settings drop-ins must be a real directory")
		}
		entries, err := os.ReadDir(dropDir)
		if err != nil || len(entries) > 256 {
			return errors.New("cannot safely enumerate managed settings drop-ins")
		}
		for _, entry := range entries {
			// Match native's documented merge surface; hidden files and other extensions are ignored.
			if strings.HasPrefix(entry.Name(), ".") || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			paths = append(paths, filepath.Join(dropDir, entry.Name()))
		}
	}
	seen := map[string]bool{}
	for _, path := range paths {
		path = filepath.Clean(path)
		if seen[path] {
			continue
		}
		seen[path] = true
		if err := validateSettingsFile(path); err != nil {
			return err
		}
	}
	return nil
}

func validateSettingsFile(path string) error {
	// Refuse links in the relevant settings directory itself; do not follow a different source than
	// the path inspected here. Ordinary symlinked working directories are resolved before discovery.
	parent, err := os.Lstat(filepath.Dir(path))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 {
		return errors.New("native settings directory is unreadable or linked; inspect it before launching")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return errors.New("native settings file is unreadable or linked; inspect it before launching")
	}
	f := os.NewFile(uintptr(fd), "native settings")
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > settingsByteLimit {
		return errors.New("native settings file is not a bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, settingsByteLimit+1))
	if err != nil || len(raw) > settingsByteLimit {
		return errors.New("cannot safely read native settings")
	}
	return validateSettingsJSON(raw)
}

func validateSettingsJSON(raw []byte) error {
	var settings map[string]json.RawMessage
	if json.Unmarshal(raw, &settings) != nil || settings == nil {
		return errors.New("native settings must be valid JSON objects before launching")
	}
	// Helpers can acquire API credentials or return a policy after this preflight. Do not execute them
	// to guess their output. Merely reject the configured source without printing its command or values.
	for _, key := range []string{"apiKeyHelper", "policyHelper", "forceLoginMethod", "forceLoginOrgUUID", "awsAuthRefresh", "awsCredentialExport"} {
		if _, ok := settings[key]; ok {
			return errors.New("native settings configure a credential, login, or policy helper incompatible with the selected master routing")
		}
	}
	if rawEnv, exists := settings["env"]; exists {
		var env map[string]json.RawMessage
		if json.Unmarshal(rawEnv, &env) != nil || env == nil {
			return errors.New("native settings env must be an object")
		}
		for key, value := range env {
			// Even an empty proxy value can undo the wrapper. Presence is rejected, not just truthiness.
			if forbiddenSettingsEnv(key) {
				return errors.New("native settings override provider, credential, proxy, or process routing; remove that override before launching")
			}
			var text string
			if json.Unmarshal(value, &text) != nil {
				return errors.New("native settings env values must be strings")
			}
		}
	}
	return nil
}

func forbiddenSettingsEnv(name string) bool {
	key := strings.ToUpper(name)
	if forbiddenProviderEnv(key) {
		return true
	}
	switch key {
	case "HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY", "NODE_EXTRA_CA_CERTS",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "BUN_OPTIONS", "BUN_CONFIG_NO_PROXY", "BUN_CONFIG_VERBOSE_FETCH",
		"HOME", "XDG_CONFIG_HOME", "APPDATA", "USERPROFILE", "PATH",
		"CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST",
		"CLAUDE_CODE_CLIENT_CERT", "CLAUDE_CODE_CLIENT_KEY", "CLAUDE_CODE_CLIENT_KEY_PASSPHRASE":
		return true
	}
	return false
}

func forbiddenProviderEnv(name string) bool {
	switch name {
	case "ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_CUSTOM_HEADERS",
		"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR",
		"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_MANTLE", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_USE_ANTHROPIC_AWS",
		"ANTHROPIC_FOUNDRY_BASE_URL", "ANTHROPIC_FOUNDRY_RESOURCE", "ANTHROPIC_FOUNDRY_API_KEY", "ANTHROPIC_FOUNDRY_AUTH_TOKEN",
		"ANTHROPIC_BEDROCK_BASE_URL", "ANTHROPIC_BEDROCK_MANTLE_BASE_URL", "ANTHROPIC_VERTEX_BASE_URL",
		"ANTHROPIC_PROFILE", "ANTHROPIC_CONFIG_DIR", "ANTHROPIC_FEDERATION_RULE_ID", "ANTHROPIC_IDENTITY_TOKEN",
		"CLAUDE_CONFIG_DIR", "NODE_OPTIONS", "NODE_TLS_REJECT_UNAUTHORIZED":
		return true
	}
	return false
}

func nativeProjectRoots(cwd string) ([]string, error) {
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return nil, errors.New("cannot resolve project settings directory")
	}
	roots := []string{resolved}
	root, err := boundedGitOutput(resolved, "rev-parse", "--show-toplevel")
	if err != nil {
		// A directory outside Git has only the starting-directory project settings. If a Git marker
		// is present, failure is ambiguous (ownership/configuration), so never silently miss its roots.
		for dir := resolved; ; dir = filepath.Dir(dir) {
			if _, statErr := os.Lstat(filepath.Join(dir, ".git")); statErr == nil || !os.IsNotExist(statErr) {
				return nil, errors.New("cannot resolve repository settings; inspect Git configuration and ownership")
			}
			if dir == filepath.Dir(dir) {
				break
			}
		}
		return roots, nil
	}
	root = strings.TrimSpace(root)
	if !filepath.IsAbs(root) {
		return nil, errors.New("Git returned an invalid settings root")
	}
	roots = append(roots, root)
	worktrees, err := boundedGitOutput(resolved, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, errors.New("cannot resolve main checkout settings")
	}
	first, _, _ := strings.Cut(worktrees, "\x00")
	mainRoot, found := strings.CutPrefix(first, "worktree ")
	if !found || !filepath.IsAbs(mainRoot) {
		return nil, errors.New("Git returned an invalid main checkout")
	}
	return append(roots, mainRoot), nil
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 32*1024 {
		return 0, errors.New("Git settings discovery exceeded its output bound")
	}
	return b.Buffer.Write(p)
}

func boundedGitOutput(cwd string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", cwd}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	var output boundedOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", errors.New("Git settings discovery failed")
	}
	return output.String(), nil
}
