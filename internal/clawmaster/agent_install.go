package clawmaster

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// agentLaunchLabel names the agent's launchd job and its plist.
const agentLaunchLabel = "dev.claw-master.agent"

// AgentInstallOptions configures `agent install` and `agent uninstall`: the agent's own options, the
// log file it writes, and, for tests, the home directory, the binary and how launchctl is run.
type AgentInstallOptions struct {
	Agent   AgentOptions
	LogFile string // default: ~/Library/Logs/claw-master-agent.log
	Out     io.Writer

	home      string
	binary    string
	launchctl func(ctx context.Context, args ...string) error
	wait      time.Duration // how long the new job has to start listening (default 15s)
}

// InstallAgent makes every Claude Code on this Mac use the pool: it runs the agent as a launchd job
// that starts at login and is restarted if it exits, waits until it listens, and then sets the
// agent's proxy, CA and loopback exemption in ~/.claude/settings.json, which the CLI, the desktop
// app's Code tab and the IDE extensions all read. There is no fallback: while the agent or the
// server is down, Claude's requests fail.
func InstallAgent(ctx context.Context, opts AgentInstallOptions) error {
	if err := opts.defaults(); err != nil {
		return err
	}
	endpoint, err := resolveAgent(opts.Agent)
	if err != nil {
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
	args = append(args, "--token-file", endpoint.tokenPath, "--log-file", opts.LogFile)
	plist, err := agentPlist(args)
	if err != nil {
		return err
	}
	plistPath := opts.plistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %w", filepath.Dir(plistPath), err)
	}
	if err := os.MkdirAll(filepath.Dir(opts.LogFile), 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %w", filepath.Dir(opts.LogFile), err)
	}
	if err := writeFileAtomic(plistPath, plist, 0o644); err != nil {
		return err
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	_ = opts.launchctl(ctx, "bootout", domain+"/"+agentLaunchLabel) // not loaded yet is fine
	if err := opts.launchctl(ctx, "bootstrap", domain, plistPath); err != nil {
		return fmt.Errorf("launchctl could not start the agent: %w", err)
	}
	if err := waitAgentListening(ctx, endpoint.listen, opts.wait); err != nil {
		return fmt.Errorf("the agent did not start listening on %s (see %s): %w", endpoint.listen, opts.LogFile, err)
	}
	lines, err := AgentEnvironment(opts.Agent)
	if err != nil {
		return err
	}
	settingsPath := filepath.Join(opts.home, ".claude", "settings.json")
	changed, err := updateClaudeSettingsEnv(settingsPath, func(env map[string]json.RawMessage) {
		for _, line := range lines {
			key, value, _ := strings.Cut(line, "=")
			env[key], _ = json.Marshal(value)
		}
	})
	if err != nil {
		return err
	}
	if opts.Out != nil {
		fmt.Fprintf(opts.Out, "claw-master agent: running as launchd job %s on %s, logging to %s\n", agentLaunchLabel, endpoint.listen, opts.LogFile)
		if changed {
			fmt.Fprintf(opts.Out, "claw-master agent: every Claude Code on this Mac now uses the pool (%s env; previous file kept as %s)\n", settingsPath, settingsPath+agentSettingsBackupSuffix)
		}
		fmt.Fprintln(opts.Out, "claw-master agent: restart the Claude desktop app and running Claude sessions to pick it up")
	}
	return nil
}

// UninstallAgent stops the launchd job, removes its plist, and removes from ~/.claude/settings.json
// the entries InstallAgent set, leaving everything else as it is.
func UninstallAgent(ctx context.Context, opts AgentInstallOptions) error {
	if err := opts.defaults(); err != nil {
		return err
	}
	endpoint, err := resolveAgentPaths(opts.Agent)
	if err != nil {
		return err
	}
	settingsPath := filepath.Join(opts.home, ".claude", "settings.json")
	allow := agentSettingsAllowance(endpoint.tokenPath, endpoint.caPath)
	if allow != nil {
		if _, err := updateClaudeSettingsEnv(settingsPath, func(env map[string]json.RawMessage) {
			for key, raw := range env {
				var value string
				if json.Unmarshal(raw, &value) == nil && allow(key, value) {
					delete(env, key)
				}
			}
		}); err != nil {
			return err
		}
	}
	_ = opts.launchctl(ctx, "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+agentLaunchLabel)
	if err := os.Remove(opts.plistPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if opts.Out != nil {
		fmt.Fprintln(opts.Out, "claw-master agent: stopped and removed; Claude Code on this Mac uses its own login again")
	}
	return nil
}

func (opts *AgentInstallOptions) defaults() error {
	if opts.launchctl == nil {
		if runtime.GOOS != "darwin" {
			return errors.New("agent install is for macOS (launchd); elsewhere run `claw-master agent` as a service and set `claw-master agent env` in Claude's settings")
		}
		opts.launchctl = func(ctx context.Context, args ...string) error {
			out, err := exec.CommandContext(ctx, "launchctl", args...).CombinedOutput()
			if err != nil {
				return fmt.Errorf("%w: %s", err, bytes.TrimSpace(out))
			}
			return nil
		}
	}
	if opts.home == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return errors.New("cannot locate the home directory")
		}
		opts.home = home
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
	}
	logFile, err := filepath.Abs(opts.LogFile)
	if err != nil {
		return err
	}
	opts.LogFile = logFile
	return nil
}

func (opts *AgentInstallOptions) plistPath() string {
	return filepath.Join(opts.home, "Library", "LaunchAgents", agentLaunchLabel+".plist")
}

// agentPlist is a launchd job that runs args at login and again whenever it exits.
func agentPlist(args []string) ([]byte, error) {
	var b bytes.Buffer
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
		if err := xml.EscapeText(&b, []byte(arg)); err != nil {
			return nil, err
		}
		b.WriteString("</string>\n")
	}
	b.WriteString(`  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
</dict>
</plist>
`)
	return b.Bytes(), nil
}

// waitAgentListening waits, briefly, for the job launchd just started to accept connections.
func waitAgentListening(ctx context.Context, address string, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", address)
		if err == nil {
			return conn.Close()
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

const agentSettingsBackupSuffix = ".before-claw-master"

// updateClaudeSettingsEnv changes the env object of a Claude settings file, keeping every other
// setting. The file is replaced atomically, with its mode, after the previous content is copied
// beside it; it reports whether anything changed.
func updateClaudeSettingsEnv(path string, update func(env map[string]json.RawMessage)) (bool, error) {
	settings := map[string]json.RawMessage{}
	var original []byte
	mode := fs.FileMode(0o600)
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return false, fmt.Errorf("cannot read %s: %w", path, err)
	case !info.Mode().IsRegular() || info.Size() > settingsByteLimit:
		return false, fmt.Errorf("%s is not a bounded regular file; edit it by hand", path)
	default:
		mode = info.Mode().Perm()
		if original, err = os.ReadFile(path); err != nil {
			return false, fmt.Errorf("cannot read %s: %w", path, err)
		}
		if len(bytes.TrimSpace(original)) > 0 {
			if err := json.Unmarshal(original, &settings); err != nil || settings == nil {
				return false, fmt.Errorf("%s is not a JSON object; fix it before installing the agent", path)
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
	update(env)
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
	if original != nil {
		if err := writeFileAtomic(path+agentSettingsBackupSuffix, original, mode); err != nil {
			return false, err
		}
	}
	if err := writeFileAtomic(path, append(out, '\n'), mode); err != nil {
		return false, err
	}
	return true, nil
}

// writeFileAtomic replaces path with data: a reader sees the old file or the new one, never a part.
func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
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
