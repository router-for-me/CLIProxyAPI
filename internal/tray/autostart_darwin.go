//go:build darwin

package tray

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	defaultAutoStartLabel = "com.router-for-me.cliproxyapi.tray"
	autoStartLogPath      = "/tmp/cliproxyapi-tray.log"
	autoStartErrorLogPath = "/tmp/cliproxyapi-tray.error.log"
)

var launchctlPIDRE = regexp.MustCompile(`"?PID"?\s*=\s*(\d+)`)

type launchctlFunc func(args ...string) ([]byte, error)

type autoStartManager struct {
	label           string
	executablePath  string
	configPath      string
	localModel      bool
	homeAddr        string
	homePassword    string
	launchAgentsDir string
	runLaunchctl    launchctlFunc
	pid             int
}

func newAutoStartManager(opts AutoStartOptions) *autoStartManager {
	launchAgentsDir := ""
	if home, err := os.UserHomeDir(); err == nil {
		launchAgentsDir = filepath.Join(home, "Library", "LaunchAgents")
	}

	return &autoStartManager{
		label:           defaultAutoStartLabel,
		executablePath:  opts.ExecutablePath,
		configPath:      opts.ConfigPath,
		localModel:      opts.LocalModel,
		homeAddr:        opts.HomeAddr,
		homePassword:    opts.HomePassword,
		launchAgentsDir: launchAgentsDir,
		runLaunchctl:    defaultLaunchctl,
		pid:             os.Getpid(),
	}
}

func defaultLaunchctl(args ...string) ([]byte, error) {
	cmd := exec.Command("launchctl", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return output, fmt.Errorf("launchctl %s: %w", strings.Join(args, " "), err)
		}
		return output, fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, message)
	}
	return output, nil
}

func (m *autoStartManager) available() bool {
	if m == nil || strings.TrimSpace(m.launchAgentsDir) == "" {
		return false
	}
	if _, err := m.programArguments(); err != nil {
		return false
	}
	return true
}

func (m *autoStartManager) enabled() bool {
	if m == nil {
		return false
	}
	if _, err := os.Stat(m.plistPath()); err != nil {
		return false
	}
	_, err := m.launchctl("list", m.resolvedLabel())
	return err == nil
}

func (m *autoStartManager) enable() error {
	if m == nil {
		return errors.New("auto-start manager is not configured")
	}

	args, err := m.programArguments()
	if err != nil {
		return err
	}
	if strings.TrimSpace(m.launchAgentsDir) == "" {
		return errors.New("launch agents directory is empty")
	}
	plistPath := m.plistPath()
	if strings.TrimSpace(plistPath) == "" {
		return errors.New("launch agent path is empty")
	}

	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("create launch agents directory: %w", err)
	}
	if err := os.WriteFile(plistPath, []byte(m.plist(args)), 0o644); err != nil {
		return fmt.Errorf("write launch agent: %w", err)
	}

	if m.isAgentSelf() {
		return nil
	}

	_, _ = m.launchctl("unload", plistPath)
	if _, err := m.launchctl("load", "-w", plistPath); err != nil {
		return fmt.Errorf("load launch agent: %w", err)
	}
	return nil
}

func (m *autoStartManager) disable() error {
	if m == nil {
		return errors.New("auto-start manager is not configured")
	}
	if strings.TrimSpace(m.launchAgentsDir) == "" {
		return errors.New("launch agents directory is empty")
	}

	plistPath := m.plistPath()
	if !m.isAgentSelf() {
		_, _ = m.launchctl("unload", plistPath)
	}

	if err := os.Remove(plistPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove launch agent: %w", err)
	}
	return nil
}

func (m *autoStartManager) isAgentSelf() bool {
	if m == nil || m.pid <= 0 {
		return false
	}
	output, err := m.launchctl("list", m.resolvedLabel())
	if err != nil {
		return false
	}
	match := launchctlPIDRE.FindSubmatch(output)
	if match == nil {
		return false
	}
	pid, err := strconv.Atoi(string(match[1]))
	return err == nil && pid == m.pid
}

func (m *autoStartManager) programArguments() ([]string, error) {
	executablePath := strings.TrimSpace(m.executablePath)
	if executablePath == "" {
		return nil, errors.New("executable path is empty")
	}
	absoluteExecutablePath, err := filepath.Abs(executablePath)
	if err != nil {
		return nil, fmt.Errorf("resolve executable path: %w", err)
	}
	if info, err := os.Stat(absoluteExecutablePath); err != nil {
		return nil, fmt.Errorf("inspect executable path: %w", err)
	} else if info.IsDir() {
		return nil, fmt.Errorf("executable path is a directory: %s", absoluteExecutablePath)
	}

	args := []string{absoluteExecutablePath, "--tray"}
	if homeAddr := strings.TrimSpace(m.homeAddr); homeAddr != "" {
		if strings.TrimSpace(m.homePassword) != "" {
			return nil, errors.New("home-password cannot be persisted in auto-start LaunchAgent")
		}
		args = append(args, "--home", homeAddr)
	}
	if configPath := strings.TrimSpace(m.configPath); configPath != "" {
		absoluteConfigPath, err := filepath.Abs(configPath)
		if err != nil {
			return nil, fmt.Errorf("resolve config path: %w", err)
		}
		args = append(args, "--config", absoluteConfigPath)
	}
	if m.localModel {
		args = append(args, "--local-model")
	}
	return args, nil
}

func (m *autoStartManager) plistPath() string {
	if m == nil {
		return ""
	}
	return filepath.Join(strings.TrimSpace(m.launchAgentsDir), m.resolvedLabel()+".plist")
}

func (m *autoStartManager) resolvedLabel() string {
	if m == nil || strings.TrimSpace(m.label) == "" {
		return defaultAutoStartLabel
	}
	return strings.TrimSpace(m.label)
}

func (m *autoStartManager) launchctl(args ...string) ([]byte, error) {
	if m != nil && m.runLaunchctl != nil {
		return m.runLaunchctl(args...)
	}
	return defaultLaunchctl(args...)
}

func (m *autoStartManager) plist(programArguments []string) string {
	var b strings.Builder
	label := m.resolvedLabel()
	workingDirectory := m.workingDirectory()
	launchPath := m.launchPath(programArguments[0])

	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	b.WriteString("<dict>\n")
	writePlistString(&b, "Label", label)
	b.WriteString("    <key>ProgramArguments</key>\n")
	b.WriteString("    <array>\n")
	for _, arg := range programArguments {
		b.WriteString("        <string>")
		b.WriteString(escapeXML(arg))
		b.WriteString("</string>\n")
	}
	b.WriteString("    </array>\n")
	if workingDirectory != "" {
		writePlistString(&b, "WorkingDirectory", workingDirectory)
	}
	b.WriteString("    <key>EnvironmentVariables</key>\n")
	b.WriteString("    <dict>\n")
	writePlistString(&b, "PATH", launchPath)
	b.WriteString("    </dict>\n")
	b.WriteString("    <key>RunAtLoad</key>\n")
	b.WriteString("    <true/>\n")
	b.WriteString("    <key>KeepAlive</key>\n")
	b.WriteString("    <false/>\n")
	writePlistString(&b, "StandardOutPath", autoStartLogPath)
	writePlistString(&b, "StandardErrorPath", autoStartErrorLogPath)
	b.WriteString("</dict>\n")
	b.WriteString("</plist>\n")
	return b.String()
}

func (m *autoStartManager) workingDirectory() string {
	configPath := strings.TrimSpace(m.configPath)
	if configPath != "" {
		if absoluteConfigPath, err := filepath.Abs(configPath); err == nil {
			return filepath.Dir(absoluteConfigPath)
		}
	}
	executablePath := strings.TrimSpace(m.executablePath)
	if executablePath != "" {
		if absoluteExecutablePath, err := filepath.Abs(executablePath); err == nil {
			return filepath.Dir(absoluteExecutablePath)
		}
	}
	return ""
}

func (m *autoStartManager) launchPath(executablePath string) string {
	parts := []string{}
	if executableDir := filepath.Dir(executablePath); executableDir != "." && executableDir != "" {
		parts = append(parts, executableDir)
	}
	parts = append(parts, "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin")
	return strings.Join(dedupeStrings(parts), ":")
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func writePlistString(b *strings.Builder, key string, value string) {
	b.WriteString("    <key>")
	b.WriteString(escapeXML(key))
	b.WriteString("</key>\n")
	b.WriteString("    <string>")
	b.WriteString(escapeXML(value))
	b.WriteString("</string>\n")
}

func escapeXML(value string) string {
	var buf bytes.Buffer
	if err := xml.EscapeText(&buf, []byte(value)); err != nil {
		return value
	}
	return buf.String()
}
