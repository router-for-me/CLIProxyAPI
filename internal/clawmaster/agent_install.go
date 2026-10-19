package clawmaster

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// agentLaunchLabel names the agent's launchd job and its plist.
const agentLaunchLabel = "dev.claw-master.agent"

// errLaunchdNoService is launchctl saying the job is not loaded (exit 113, "Could not find service").
var errLaunchdNoService = errors.New("launchd has no such service")

// AgentInstallOptions configures `agent install` and `agent uninstall`: the agent's own options, the
// log file it writes, and, for tests, the system, the home directory, the binary and how launchctl
// or schtasks is run.
type AgentInstallOptions struct {
	Agent   AgentOptions
	LogFile string // default: ~/Library/Logs/claw-master-agent.log; %LOCALAPPDATA%\claw-master\agent.log
	Out     io.Writer

	goos         string
	home         string
	localAppData string
	userSID      string
	binary       string
	launchctl    func(ctx context.Context, args ...string) error
	schtasks     func(ctx context.Context, args ...string) (string, error)
	wait         time.Duration // how long the new job has to answer (default 15s)
}

// agentInstallRecord is what InstallAgent changed, so UninstallAgent can undo exactly that without
// the agent's credentials: the entries it set in the settings env and what they replaced.
type agentInstallRecord struct {
	Settings string `json:"settings"`
	// Definition is the service's file: the launchd plist or the scheduled task's XML (the key
	// predates Windows).
	Definition string                     `json:"plist"`
	Set        map[string]string          `json:"set"`
	Previous   map[string]json.RawMessage `json:"previous,omitempty"`
	// Earlier are values earlier installs set, so uninstall still recognizes one a reinstall did not
	// get to replace.
	Earlier map[string][]string `json:"earlier,omitempty"`
}

// agentConflictingEnv are proxy settings that Claude would take over the ones the agent sets
// (Claude prefers the lowercase spelling) or that would exempt Anthropic from the proxy.
var agentConflictingEnv = []string{"https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "no_proxy"}

// InstallAgent makes every Claude Code on this Mac use the pool: it runs the agent as a launchd job
// that starts at login and is restarted if it exits, waits until that agent answers with its token,
// and then sets the agent's proxy, CA and loopback exemption in ~/.claude/settings.json, which the
// CLI, the desktop app's Code tab and the IDE extensions all read. There is no fallback: while the
// agent or the server is down, Claude's requests fail.
func InstallAgent(ctx context.Context, opts AgentInstallOptions) error {
	if err := opts.defaults(); err != nil {
		return err
	}
	if opts.Agent.TokenFile != "" {
		return errors.New("agent install keeps the token in its default place (agent.token beside --ca, or in --dir), where connect looks for it; leave out --token-file")
	}
	endpoint, err := resolveAgent(opts.Agent)
	if err != nil {
		return err
	}
	settingsPath := filepath.Join(opts.home, ".claude", "settings.json")
	if err := refuseConflictingSettings(settingsPath); err != nil {
		return err
	}
	args := []string{opts.binary, "agent", "--listen", endpoint.listen}
	if opts.Agent.Open != "" {
		args = append(args, "--open", opts.Agent.Open, "--ca", endpoint.caPath)
	} else {
		dir, err := filepath.Abs(opts.Agent.Dir)
		if err != nil {
			return err
		}
		args = append(args, "--server", opts.Agent.Server, "--dir", dir)
	}
	args = append(args, "--log-file", opts.LogFile)
	if opts.goos == "windows" {
		args = append(args, "--exit-with-parent") // Task Scheduler ends only the conhost it started
	}
	svc := opts.service()
	for _, dir := range []string{filepath.Dir(svc.definitionPath()), filepath.Dir(opts.LogFile)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("cannot create %s: %w", dir, err)
		}
	}
	if err := svc.write(args); err != nil {
		return err
	}
	if err := svc.stop(ctx); err != nil {
		return err
	}
	if err := svc.start(ctx); err != nil {
		return err
	}
	if err := waitAgentReady(ctx, endpoint.listen, endpoint.token, opts.wait); err != nil {
		return fmt.Errorf("the agent did not answer on %s (see %s): %w", endpoint.listen, opts.LogFile, err)
	}
	lines, err := AgentEnvironment(opts.Agent)
	if err != nil {
		return err
	}
	record := agentInstallRecord{Settings: settingsPath, Definition: svc.definitionPath(), Set: map[string]string{}, Previous: map[string]json.RawMessage{}, Earlier: map[string][]string{}}
	old, err := readAgentInstallRecord(opts.recordPath())
	reinstall := err == nil && old.Settings == settingsPath
	if reinstall {
		record.Previous = old.Previous // what the first install replaced
		for key, values := range old.Earlier {
			record.Earlier[key] = append(record.Earlier[key], values...)
		}
		for key, value := range old.Set {
			record.Earlier[key] = append(record.Earlier[key], value)
		}
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// The record is written before the settings, so whatever happens next, uninstall can undo it.
	if _, err := updateClaudeSettingsEnv(settingsPath, false, func(env map[string]json.RawMessage) error {
		for _, line := range lines {
			key, value, _ := strings.Cut(line, "=")
			if raw, ok := env[key]; ok && !reinstall {
				record.Previous[key] = raw
			}
			record.Set[key] = value
		}
		return nil
	}); err != nil {
		return err
	}
	if err := writeAgentInstallRecord(opts.recordPath(), record); err != nil {
		return err
	}
	changed, err := updateClaudeSettingsEnv(settingsPath, true, func(env map[string]json.RawMessage) error {
		for key, value := range record.Set {
			env[key], _ = json.Marshal(value)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, path := range []string{settingsPath, settingsPath + agentSettingsBackupSuffix} {
		if err := makePrivate(path); err != nil {
			return err
		}
	}
	if opts.Out != nil {
		fmt.Fprintf(opts.Out, "claw-master agent: running as %s on %s, logging to %s\n", svc.describe(), endpoint.listen, opts.LogFile)
		if changed {
			fmt.Fprintf(opts.Out, "claw-master agent: every Claude Code on this computer now uses the pool (%s env)\n", settingsPath)
		}
		fmt.Fprintln(opts.Out, "claw-master agent: restart the Claude desktop app and running Claude sessions to pick it up")
	}
	return nil
}

// UninstallAgent undoes what InstallAgent recorded: it stops the launchd job, puts back the settings
// entries the install replaced (removing those it added, if they still hold its values), and removes
// the plist. It needs none of the agent's flags or credentials.
func UninstallAgent(ctx context.Context, opts AgentInstallOptions) error {
	if err := opts.defaults(); err != nil {
		return err
	}
	record, err := readAgentInstallRecord(opts.recordPath())
	if errors.Is(err, fs.ErrNotExist) {
		return errors.New("no agent installation is recorded for this user")
	}
	if err != nil {
		return err
	}
	svc := opts.service()
	if err := svc.stop(ctx); err != nil {
		return err
	}
	if _, err := updateClaudeSettingsEnv(record.Settings, false, func(env map[string]json.RawMessage) error {
		for key, value := range record.Set {
			var current string
			if raw, ok := env[key]; !ok || json.Unmarshal(raw, &current) != nil || (current != value && !slices.Contains(record.Earlier[key], current)) {
				continue // changed since the install: leave it
			}
			if previous, ok := record.Previous[key]; ok {
				env[key] = previous
			} else {
				delete(env, key)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := svc.remove(ctx); err != nil {
		return err
	}
	if err := os.Remove(record.Definition); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Remove(opts.recordPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if opts.Out != nil {
		fmt.Fprintln(opts.Out, "claw-master agent: stopped and removed; Claude Code on this computer uses its own login again")
	}
	return nil
}

// refuseConflictingSettings refuses user settings whose env already routes Claude another way.
func refuseConflictingSettings(path string) error {
	_, err := updateClaudeSettingsEnv(path, false, func(env map[string]json.RawMessage) error {
		for _, key := range agentConflictingEnv {
			if _, ok := env[key]; ok {
				return fmt.Errorf("%s sets %s in env, which Claude would follow instead of the agent; remove it first", path, key)
			}
		}
		return nil
	})
	return err
}

func (opts *AgentInstallOptions) defaults() error {
	if opts.goos == "" {
		opts.goos = runtime.GOOS
	}
	if opts.home == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return errors.New("cannot locate the home directory")
		}
		opts.home = home
	}
	switch opts.goos {
	case "darwin":
		if opts.launchctl == nil {
			opts.launchctl = runLaunchctl
		}
	case "windows":
		if opts.schtasks == nil {
			opts.schtasks = runSchtasks
		}
		if opts.localAppData == "" {
			opts.localAppData = os.Getenv("LOCALAPPDATA")
		}
		if opts.localAppData == "" {
			opts.localAppData = filepath.Join(opts.home, "AppData", "Local")
		}
		if opts.userSID == "" {
			sid, err := currentUserSIDString()
			if err != nil {
				return fmt.Errorf("cannot tell this user's SID: %w", err)
			}
			opts.userSID = sid
		}
	default:
		return errors.New("agent install is for macOS (launchd) and Windows (Task Scheduler); elsewhere run `claw-master agent` as a service and set `claw-master agent env` in Claude's settings")
	}
	if opts.binary == "" {
		binary, err := os.Executable()
		if err != nil {
			return errors.New("cannot locate this claw-master binary")
		}
		if resolved, err := filepath.EvalSymlinks(binary); err == nil {
			binary = resolved
		}
		opts.binary = binary
	}
	if opts.wait == 0 {
		opts.wait = 15 * time.Second
	}
	if opts.LogFile == "" {
		opts.LogFile = filepath.Join(opts.home, "Library", "Logs", "claw-master-agent.log")
		if opts.goos == "windows" {
			opts.LogFile = filepath.Join(opts.localAppData, "claw-master", "agent.log")
		}
	}
	logFile, err := filepath.Abs(opts.LogFile)
	if err != nil {
		return err
	}
	opts.LogFile = logFile
	return nil
}

// service is how this system keeps the agent running.
func (opts *AgentInstallOptions) service() agentService {
	if opts.goos == "windows" {
		return scheduledTaskService{dir: filepath.Join(opts.localAppData, "claw-master"), userSID: opts.userSID, schtasks: opts.schtasks}
	}
	return launchdService{home: opts.home, logFile: opts.LogFile, launchctl: opts.launchctl}
}

func (opts *AgentInstallOptions) recordPath() string {
	if opts.goos == "windows" {
		return filepath.Join(opts.localAppData, "claw-master", "agent-install.json")
	}
	return filepath.Join(opts.home, "Library", "Application Support", "claw-master", "agent-install.json")
}

// runLaunchctl runs launchctl; a job that is not loaded is errLaunchdNoService.
func runLaunchctl(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, "launchctl", args...).CombinedOutput()
	if err == nil {
		return nil
	}
	var exited *exec.ExitError
	if ctx.Err() == nil && errors.As(err, &exited) && (exited.ExitCode() == 113 || bytes.Contains(out, []byte("Could not find service"))) {
		return errLaunchdNoService
	}
	return fmt.Errorf("%w: %s", err, bytes.TrimSpace(out))
}

// runSchtasks runs schtasks. Its messages are localized, so a missing task is told apart by asking
// for the task by name first: /query of one task fails exactly when there is no such task (or no
// right to see it, which is the same for this user's own task).
func runSchtasks(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "schtasks", args...).CombinedOutput()
	if err == nil {
		return string(out), nil
	}
	var exited *exec.ExitError
	if ctx.Err() == nil && errors.As(err, &exited) && exited.ExitCode() == 1 && len(args) > 0 && (args[0] == "/query" || args[0] == "/delete") {
		return "", errTaskNotFound
	}
	return "", fmt.Errorf("%w: %s", err, bytes.TrimSpace(out))
}

// agentPlist is a launchd job that runs args at login and again whenever it exits; what it prints
// before its logging starts (a port in use, unreadable credentials) goes to stderrPath.
func agentPlist(args []string, stderrPath string) ([]byte, error) {
	var b bytes.Buffer
	escape := func(s string) error { return xml.EscapeText(&b, []byte(s)) }
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + agentLaunchLabel + `</string>
  <key>ProgramArguments</key>
  <array>
`)
	for _, arg := range args {
		b.WriteString("    <string>")
		if err := escape(arg); err != nil {
			return nil, err
		}
		b.WriteString("</string>\n")
	}
	b.WriteString("  </array>\n  <key>StandardErrorPath</key><string>")
	if err := escape(stderrPath); err != nil {
		return nil, err
	}
	b.WriteString(`</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
</dict>
</plist>
`)
	return b.Bytes(), nil
}

// waitAgentReady waits, briefly, until this machine's agent (not just anything listening there)
// proves it holds the token, which is never sent.
func waitAgentReady(ctx context.Context, address, token string, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		err := probeAgent(ctx, address, token)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func probeAgent(ctx context.Context, address, token string) error {
	dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", address)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	nonce := rand.Text()
	if _, err := fmt.Fprintf(conn, "GET %s?nonce=%s HTTP/1.1\r\nHost: claw-master\r\nConnection: close\r\n\r\n", agentProbePath, nonce); err != nil {
		return err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	want := agentProbeAnswer(token, nonce)
	if resp.StatusCode != http.StatusNoContent || !hmac.Equal([]byte(resp.Header.Get(agentProbeHeader)), []byte(want)) {
		return fmt.Errorf("something else is listening there (answered %s)", resp.Status)
	}
	return nil
}

const agentSettingsBackupSuffix = ".before-claw-master"

// updateClaudeSettingsEnv changes the env object of a Claude settings file, keeping every other
// setting, and reports whether anything changed. The file is replaced atomically and made private,
// since the agent's entries carry its token; with backup, the content it had before the first
// install is kept beside it.
func updateClaudeSettingsEnv(path string, backup bool, update func(env map[string]json.RawMessage) error) (bool, error) {
	settings := map[string]json.RawMessage{}
	var original []byte
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return false, fmt.Errorf("cannot read %s: %w", path, err)
	case !info.Mode().IsRegular() || info.Size() > settingsByteLimit:
		return false, fmt.Errorf("%s is not a bounded regular file; edit it by hand", path)
	default:
		if original, err = os.ReadFile(path); err != nil {
			return false, fmt.Errorf("cannot read %s: %w", path, err)
		}
		if len(bytes.TrimSpace(original)) > 0 {
			if err := json.Unmarshal(original, &settings); err != nil || settings == nil {
				return false, fmt.Errorf("%s is not a JSON object; fix it first", path)
			}
		}
	}
	env := map[string]json.RawMessage{}
	if raw, ok := settings["env"]; ok {
		if err := json.Unmarshal(raw, &env); err != nil || env == nil {
			return false, fmt.Errorf("%s: env is not an object", path)
		}
	}
	before, _ := json.Marshal(env)
	if err := update(env); err != nil {
		return false, err
	}
	after, _ := json.Marshal(env)
	if bytes.Equal(before, after) {
		return false, nil
	}
	if len(env) == 0 {
		delete(settings, "env")
	} else {
		settings["env"] = after
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	if backup && original != nil {
		if _, err := os.Lstat(path + agentSettingsBackupSuffix); errors.Is(err, fs.ErrNotExist) {
			if err := writeFileAtomic(path+agentSettingsBackupSuffix, original, 0o600); err != nil {
				return false, err
			}
		}
	}
	if err := writeFileAtomic(path, append(out, '\n'), 0o600); err != nil {
		return false, err
	}
	return true, nil
}

// makePrivate makes an existing regular file readable by its owner only.
func makePrivate(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && !info.Mode().IsRegular()) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(path, info.Mode().Perm()&0o700); err != nil {
			return err
		}
	}
	return restrictToOwner(path)
}

func readAgentInstallRecord(path string) (agentInstallRecord, error) {
	var record agentInstallRecord
	raw, err := os.ReadFile(path)
	if err != nil {
		return record, err
	}
	if err := json.Unmarshal(raw, &record); err != nil || record.Settings == "" || record.Definition == "" {
		return record, fmt.Errorf("%s is not an agent installation record", path)
	}
	return record, nil
}

func writeAgentInstallRecord(path string, record agentInstallRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	out, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(out, '\n'), 0o600) // holds the token, like the settings
}

// writeFileAtomic replaces path with data: a reader sees the old file or the new one, never a part.
func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	create := func() (*os.File, error) { return os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*") }
	if mode&0o077 == 0 {
		create = func() (*os.File, error) { return createPrivateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-") }
	}
	temp, err := create()
	if err != nil {
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	_, errWrite := temp.Write(data)
	errChmod := temp.Chmod(mode)
	errSync := temp.Sync()
	errClose := temp.Close()
	if err := errors.Join(errWrite, errChmod, errSync, errClose); err != nil {
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	return nil
}
