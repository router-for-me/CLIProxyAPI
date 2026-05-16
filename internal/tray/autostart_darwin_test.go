//go:build darwin

package tray

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestAutoStartEnableWritesLaunchAgent(t *testing.T) {
	dir := t.TempDir()
	executablePath := writeExecutable(t, dir)
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 18317\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var calls [][]string
	manager := testAutoStartManager(dir, executablePath, configPath, func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return nil, nil
	})
	manager.localModel = true

	if err := manager.enable(); err != nil {
		t.Fatalf("enable() error = %v", err)
	}

	plistData, err := os.ReadFile(manager.plistPath())
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	plist := string(plistData)
	for _, want := range []string{
		"<string>" + executablePath + "</string>",
		"<string>--tray</string>",
		"<string>--config</string>",
		"<string>" + configPath + "</string>",
		"<string>--local-model</string>",
		"<key>WorkingDirectory</key>",
		"<string>" + dir + "</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Fatalf("plist missing %q:\n%s", want, plist)
		}
	}
	if strings.Contains(plist, "password") {
		t.Fatalf("plist should not persist runtime passwords:\n%s", plist)
	}

	wantCalls := [][]string{
		{"list", manager.label},
		{"unload", manager.plistPath()},
		{"load", "-w", manager.plistPath()},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("launchctl calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestAutoStartEnabledRequiresPlistAndLaunchctlRegistration(t *testing.T) {
	dir := t.TempDir()
	executablePath := writeExecutable(t, dir)
	manager := testAutoStartManager(dir, executablePath, "", func(args ...string) ([]byte, error) {
		if reflect.DeepEqual(args, []string{"list", "com.router-for-me.cliproxyapi.tray.test"}) {
			return []byte(`{"PID" = 123;}`), nil
		}
		return nil, errors.New("unexpected launchctl call")
	})

	if manager.enabled() {
		t.Fatalf("enabled() = true without plist")
	}
	if err := os.WriteFile(manager.plistPath(), []byte("plist"), 0o644); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	if !manager.enabled() {
		t.Fatalf("enabled() = false with plist and launchctl registration")
	}
}

func TestAutoStartEnableWritesHomeLaunchAgentWithoutPassword(t *testing.T) {
	dir := t.TempDir()
	executablePath := writeExecutable(t, dir)
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 18317\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	manager := testAutoStartManager(dir, executablePath, configPath, func(args ...string) ([]byte, error) {
		return nil, nil
	})
	manager.homeAddr = "127.0.0.1:6379"

	if err := manager.enable(); err != nil {
		t.Fatalf("enable() error = %v", err)
	}

	plistData, err := os.ReadFile(manager.plistPath())
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	plist := string(plistData)
	for _, want := range []string{
		"<string>--home</string>",
		"<string>127.0.0.1:6379</string>",
		"<string>--config</string>",
		"<string>" + configPath + "</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Fatalf("plist missing %q:\n%s", want, plist)
		}
	}
	if strings.Contains(plist, "home-password") || strings.Contains(plist, "secret") {
		t.Fatalf("plist should not persist home passwords:\n%s", plist)
	}
}

func TestAutoStartUnavailableWhenHomePasswordWouldBePersisted(t *testing.T) {
	dir := t.TempDir()
	executablePath := writeExecutable(t, dir)
	manager := testAutoStartManager(dir, executablePath, "", func(args ...string) ([]byte, error) {
		t.Fatalf("launchctl should not be called when home password is configured")
		return nil, nil
	})
	manager.homeAddr = "127.0.0.1:6379"
	manager.homePassword = "secret"

	if manager.available() {
		t.Fatalf("available() = true with home password")
	}
	err := manager.enable()
	if err == nil || !strings.Contains(err.Error(), "home-password cannot be persisted") {
		t.Fatalf("enable() error = %v, want home-password persistence error", err)
	}
	if _, err := os.Stat(manager.plistPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plist should not be written when home password is configured: %v", err)
	}
}

func TestAutoStartDisableDoesNotUnloadSelf(t *testing.T) {
	dir := t.TempDir()
	executablePath := writeExecutable(t, dir)

	var calls [][]string
	manager := testAutoStartManager(dir, executablePath, "", func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return []byte(`{"PID" = 4242;}`), nil
	})
	manager.pid = 4242
	if err := os.WriteFile(manager.plistPath(), []byte("plist"), 0o644); err != nil {
		t.Fatalf("write plist: %v", err)
	}

	if err := manager.disable(); err != nil {
		t.Fatalf("disable() error = %v", err)
	}
	if _, err := os.Stat(manager.plistPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plist still exists after disable: %v", err)
	}

	wantCalls := [][]string{{"list", manager.label}}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("launchctl calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestAutoStartEnableRejectsMissingExecutable(t *testing.T) {
	dir := t.TempDir()
	manager := testAutoStartManager(dir, filepath.Join(dir, "missing"), "", func(args ...string) ([]byte, error) {
		t.Fatalf("launchctl should not be called for missing executable")
		return nil, nil
	})

	if err := manager.enable(); err == nil {
		t.Fatalf("enable() error = nil, want missing executable error")
	}
	if _, err := os.Stat(manager.plistPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plist should not be written for missing executable: %v", err)
	}
}

func TestAutoStartEnableReturnsLoadFailure(t *testing.T) {
	dir := t.TempDir()
	executablePath := writeExecutable(t, dir)
	manager := testAutoStartManager(dir, executablePath, "", func(args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "load" {
			return nil, errors.New("load failed")
		}
		return nil, nil
	})

	if err := manager.enable(); err == nil {
		t.Fatalf("enable() error = nil, want load failure")
	}
}

func TestAutoStartLaunchctlE2E(t *testing.T) {
	if os.Getenv("CLIPROXYAPI_TRAY_AUTOSTART_E2E") != "1" {
		t.Skip("set CLIPROXYAPI_TRAY_AUTOSTART_E2E=1 to exercise launchctl")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home directory: %v", err)
	}
	manager := &autoStartManager{
		label:           "com.router-for-me.cliproxyapi.tray.e2e." + strconv.Itoa(os.Getpid()),
		executablePath:  "/usr/bin/true",
		launchAgentsDir: filepath.Join(home, "Library", "LaunchAgents"),
		runLaunchctl:    defaultLaunchctl,
		pid:             os.Getpid(),
	}
	defer func() {
		if err := manager.disable(); err != nil {
			t.Logf("cleanup launch agent: %v", err)
		}
	}()

	if err := manager.enable(); err != nil {
		t.Fatalf("enable() error = %v", err)
	}
	if !manager.enabled() {
		t.Fatalf("enabled() = false after launchctl load")
	}
	if err := manager.disable(); err != nil {
		t.Fatalf("disable() error = %v", err)
	}
	if manager.enabled() {
		t.Fatalf("enabled() = true after disable")
	}
}

func writeExecutable(t *testing.T, dir string) string {
	t.Helper()
	executablePath := filepath.Join(dir, "cliproxyapi")
	if err := os.WriteFile(executablePath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	return executablePath
}

func testAutoStartManager(dir string, executablePath string, configPath string, run launchctlFunc) *autoStartManager {
	return &autoStartManager{
		label:           "com.router-for-me.cliproxyapi.tray.test",
		executablePath:  executablePath,
		configPath:      configPath,
		launchAgentsDir: dir,
		runLaunchctl:    run,
		pid:             1,
	}
}
