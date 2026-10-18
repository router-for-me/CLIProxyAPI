package clawmaster

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func agentSettingsJSON(t *testing.T, env map[string]string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"env": env})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSettingsMayPointClaudeAtThisMachinesAgentOnly(t *testing.T) {
	dir := t.TempDir()
	ca := agentTestCA(t, dir)
	tokenPath := filepath.Join(dir, agentTokenName)
	token, err := agentToken(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	allow := agentSettingsAllowance(tokenPath, ca)
	if allow == nil {
		t.Fatal("no allowance with a readable token")
	}
	agentEnv := map[string]string{
		"HTTPS_PROXY":         "http://claw-master:" + token + "@127.0.0.1:8445",
		"NODE_EXTRA_CA_CERTS": ca,
		"NO_PROXY":            agentNoProxy,
	}
	if err := validateSettingsJSON(agentSettingsJSON(t, agentEnv), allow); err != nil {
		t.Fatalf("the agent's own settings were refused: %v", err)
	}
	if err := validateSettingsJSON(agentSettingsJSON(t, agentEnv), nil); err == nil {
		t.Fatal("the agent's settings were accepted without an allowance")
	}
	for name, env := range map[string]map[string]string{
		"another token":    {"HTTPS_PROXY": "http://claw-master:" + strings.Repeat("A", 26) + "@127.0.0.1:8445"},
		"another user":     {"HTTPS_PROXY": "http://someone:" + token + "@127.0.0.1:8445"},
		"no password":      {"HTTPS_PROXY": "http://claw-master@127.0.0.1:8445"},
		"https scheme":     {"HTTPS_PROXY": "https://claw-master:" + token + "@127.0.0.1:8445"},
		"not loopback":     {"HTTPS_PROXY": "http://claw-master:" + token + "@192.0.2.1:8445"},
		"host name":        {"HTTPS_PROXY": "http://claw-master:" + token + "@localhost:8445"},
		"no port":          {"HTTPS_PROXY": "http://claw-master:" + token + "@127.0.0.1"},
		"a path":           {"HTTPS_PROXY": "http://claw-master:" + token + "@127.0.0.1:8445/x"},
		"lowercase key":    {"https_proxy": "http://claw-master:" + token + "@127.0.0.1:8445"},
		"HTTP_PROXY":       {"HTTP_PROXY": "http://claw-master:" + token + "@127.0.0.1:8445"},
		"another CA":       {"NODE_EXTRA_CA_CERTS": filepath.Join(dir, "other.pem")},
		"a relative CA":    {"NODE_EXTRA_CA_CERTS": "ca.pem"},
		"a wider NO_PROXY": {"NO_PROXY": "*"},
		"an empty proxy":   {"HTTPS_PROXY": ""},
	} {
		if err := validateSettingsJSON(agentSettingsJSON(t, env), allow); err == nil {
			t.Errorf("%s: accepted %v", name, env)
		}
	}
	if agentSettingsAllowance(filepath.Join(dir, "missing.token"), ca) != nil {
		t.Fatal("an allowance without a token")
	}
}

type agentInstallTest struct {
	home, ca, listen string
	calls            [][]string
	opts             AgentInstallOptions
}

func newAgentInstallTest(t *testing.T, listening bool) *agentInstallTest {
	t.Helper()
	x := &agentInstallTest{home: t.TempDir()}
	config := filepath.Join(x.home, ".config", "claw-master")
	if err := os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	x.ca = agentTestCA(t, config)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	x.listen = listener.Addr().String()
	if listening { // stands in for the job launchd starts
		t.Cleanup(func() { _ = listener.Close() })
	} else {
		_ = listener.Close()
	}
	x.opts = AgentInstallOptions{
		Agent: AgentOptions{Listen: x.listen, Open: "127.0.0.1:8444", CAFile: x.ca},
		home:  x.home, binary: "/Applications/claw & master/claw-master", wait: 300 * time.Millisecond,
		launchctl: func(_ context.Context, args ...string) error {
			x.calls = append(x.calls, args)
			return nil
		},
	}
	return x
}

func (x *agentInstallTest) settingsPath() string {
	return filepath.Join(x.home, ".claude", "settings.json")
}

func readSettingsForTest(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	return settings
}

func TestAgentInstallRunsTheJobAndPointsSettingsAtIt(t *testing.T) {
	x := newAgentInstallTest(t, true)
	original := []byte(`{"model": "claude-test-model", "env": {"FOO": "bar"}, "permissions": {"allow": ["Bash(ls)"]}}`)
	if err := os.MkdirAll(filepath.Dir(x.settingsPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(x.settingsPath(), original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallAgent(t.Context(), x.opts); err != nil {
		t.Fatal(err)
	}

	uid := "gui/" + itoaForTest(os.Getuid())
	plistPath := filepath.Join(x.home, "Library", "LaunchAgents", agentLaunchLabel+".plist")
	want := [][]string{{"bootout", uid + "/" + agentLaunchLabel}, {"bootstrap", uid, plistPath}}
	if !reflect.DeepEqual(x.calls, want) {
		t.Fatalf("launchctl calls = %q, want %q", x.calls, want)
	}
	plist, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Strings []string `xml:"dict>array>string"`
	}
	if err := xml.Unmarshal(plist, &parsed); err != nil {
		t.Fatalf("the plist is not XML: %v\n%s", err, plist)
	}
	tokenPath := filepath.Join(filepath.Dir(x.ca), agentTokenName)
	wantArgs := []string{x.opts.binary, "agent", "--listen", x.listen, "--open", "127.0.0.1:8444", "--ca", x.ca,
		"--token-file", tokenPath, "--log-file", filepath.Join(x.home, "Library", "Logs", "claw-master-agent.log")}
	if !reflect.DeepEqual(parsed.Strings, wantArgs) {
		t.Fatalf("plist arguments = %q, want %q", parsed.Strings, wantArgs)
	}

	settings := readSettingsForTest(t, x.settingsPath())
	if settings["model"] != "claude-test-model" || settings["permissions"] == nil {
		t.Fatalf("other settings were lost: %v", settings)
	}
	env, _ := settings["env"].(map[string]any)
	lines, err := AgentEnvironment(x.opts.Agent)
	if err != nil {
		t.Fatal(err)
	}
	if env["FOO"] != "bar" || len(env) != 1+len(lines) {
		t.Fatalf("env = %v", env)
	}
	for _, line := range lines {
		key, value, _ := strings.Cut(line, "=")
		if env[key] != value {
			t.Fatalf("env[%s] = %v, want %q", key, env[key], value)
		}
	}
	if backup, err := os.ReadFile(x.settingsPath() + agentSettingsBackupSuffix); err != nil || string(backup) != string(original) {
		t.Fatalf("backup = %q, %v; want the original file", backup, err)
	}
	if info, err := os.Stat(x.settingsPath()); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("settings mode = %v, %v; want 0600 kept", info.Mode().Perm(), err)
	}

	// connect accepts these settings, because they lead to the pool; without the token it does not.
	if err := validateSettingsFile(x.settingsPath(), agentSettingsAllowance(tokenPath, x.ca)); err != nil {
		t.Fatalf("connect would refuse the installed settings: %v", err)
	}
	if err := validateSettingsFile(x.settingsPath(), nil); err == nil {
		t.Fatal("the installed settings pass without the agent allowance")
	}

	// A second install changes nothing in the settings.
	if err := InstallAgent(t.Context(), x.opts); err != nil {
		t.Fatal(err)
	}
	if again := readSettingsForTest(t, x.settingsPath()); !reflect.DeepEqual(again, settings) {
		t.Fatalf("a second install changed the settings: %v", again)
	}

	x.calls = nil
	if err := UninstallAgent(t.Context(), x.opts); err != nil {
		t.Fatal(err)
	}
	after := readSettingsForTest(t, x.settingsPath())
	if env, _ := after["env"].(map[string]any); len(env) != 1 || env["FOO"] != "bar" || after["model"] != "claude-test-model" {
		t.Fatalf("after uninstall settings = %v, want the original values", after)
	}
	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Fatalf("the plist is still there: %v", err)
	}
	if len(x.calls) != 1 || x.calls[0][0] != "bootout" {
		t.Fatalf("uninstall launchctl calls = %q", x.calls)
	}
}

func TestAgentInstallLeavesSettingsAloneWhenTheAgentDoesNotStart(t *testing.T) {
	x := newAgentInstallTest(t, false)
	if err := InstallAgent(t.Context(), x.opts); err == nil {
		t.Fatal("install succeeded with nothing listening")
	}
	if _, err := os.Stat(x.settingsPath()); !os.IsNotExist(err) {
		t.Fatalf("settings were written for an agent that is not running: %v", err)
	}
}

func TestAgentInstallRefusesSettingsItCannotMergeSafely(t *testing.T) {
	for name, setup := range map[string]func(path string) error{
		"not an object": func(path string) error { return os.WriteFile(path, []byte(`["x"]`), 0o600) },
		"not JSON":      func(path string) error { return os.WriteFile(path, []byte(`{"model":`), 0o600) },
		"env a string":  func(path string) error { return os.WriteFile(path, []byte(`{"env":"x"}`), 0o600) },
		"a symlink": func(path string) error {
			target := path + ".target"
			if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
				return err
			}
			return os.Symlink(target, path)
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := newAgentInstallTest(t, true)
			if err := os.MkdirAll(filepath.Dir(x.settingsPath()), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := setup(x.settingsPath()); err != nil {
				t.Fatal(err)
			}
			before, _ := os.Readlink(x.settingsPath())
			raw, _ := os.ReadFile(x.settingsPath())
			if err := InstallAgent(t.Context(), x.opts); err == nil {
				t.Fatal("install merged into it")
			}
			after, _ := os.Readlink(x.settingsPath())
			now, _ := os.ReadFile(x.settingsPath())
			if before != after || string(raw) != string(now) {
				t.Fatal("the settings file changed")
			}
		})
	}
}

func itoaForTest(n int) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

// connect checks the native settings before it launches Claude: the agent's entries pass while the
// agent's token is there, and are refused like any proxy override once it is gone.
func TestConnectAcceptsSettingsThatPointAtThisMachinesAgent(t *testing.T) {
	certs, proxy := startServerWithOpenListener(t)
	dir := canonicalTestTempDir(t)
	home, work, bin := filepath.Join(dir, "home"), filepath.Join(dir, "work"), filepath.Join(dir, "bin")
	for _, d := range []string{filepath.Join(home, ".claude"), work, bin} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	lines, err := AgentEnvironment(AgentOptions{Listen: "127.0.0.1:8445", Open: proxy.OpenAddr(), CAFile: certs.caPath})
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, line := range lines {
		key, value, _ := strings.Cut(line, "=")
		env[key] = value
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), agentSettingsJSON(t, env), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Chdir(work)

	opts := ConnectOptions{Open: proxy.OpenAddr(), CAFile: certs.caPath}
	if code, err := Connect(t.Context(), opts, nil); err != nil || code != 0 {
		t.Fatalf("Connect with the agent's settings = %d, %v; want Claude to run", code, err)
	}
	if err := os.Remove(filepath.Join(filepath.Dir(certs.caPath), agentTokenName)); err != nil {
		t.Fatal(err)
	}
	if _, err := Connect(t.Context(), opts, nil); err == nil || !strings.Contains(err.Error(), "proxy") {
		t.Fatalf("Connect without the agent's token = %v; want the proxy override refused", err)
	}
}
