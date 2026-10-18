package clawmaster

import (
	"bufio"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
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
	loaded           bool
	failBootout      bool
	failPrint        bool
	mu               sync.Mutex
	seen             []string // request lines another program on the port received
	opts             AgentInstallOptions
}

// newAgentInstallTest prepares a home with a CA. With agent, the listen address is served by a real
// agent (standing in for the job launchd starts); with other, by something that is not the agent.
func newAgentInstallTest(t *testing.T, serve string) *agentInstallTest {
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
	x.opts = AgentInstallOptions{
		Agent: AgentOptions{Listen: x.listen, Open: "127.0.0.1:8444", CAFile: x.ca},
		home:  x.home, binary: "/Applications/claw & master/claw-master", wait: 300 * time.Millisecond,
		launchctl: func(_ context.Context, args ...string) error {
			x.calls = append(x.calls, args)
			switch args[0] {
			case "print":
				if x.failPrint {
					return errors.New("launchctl: signal: interrupt")
				}
				if !x.loaded {
					return errLaunchdNoService
				}
			case "bootstrap":
				x.loaded = true
			case "bootout":
				if x.failBootout {
					return errors.New("operation not permitted")
				}
				x.loaded = false
			}
			return nil
		},
	}
	switch serve {
	case "agent":
		endpoint, err := resolveAgent(x.opts.Agent)
		if err != nil {
			t.Fatal(err)
		}
		endpoint.check = func(context.Context) error { return nil }
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- serveAgent(ctx, listener, endpoint, nil) }()
		t.Cleanup(func() { cancel(); <-done })
	case "other", "forged":
		// Another program: a plain web server, or one that answers the probe without the token.
		reply := "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"
		if serve == "forged" {
			reply = "HTTP/1.1 204 No Content\r\n" + agentProbeHeader + ": 1\r\n\r\n"
		}
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				var request strings.Builder
				reader := bufio.NewReader(conn)
				for {
					line, err := reader.ReadString('\n')
					request.WriteString(line)
					if err != nil || line == "\r\n" {
						break
					}
				}
				x.mu.Lock()
				x.seen = append(x.seen, request.String())
				x.mu.Unlock()
				_, _ = io.WriteString(conn, reply)
				_ = conn.Close()
			}
		}()
		t.Cleanup(func() { _ = listener.Close() })
	default:
		_ = listener.Close()
	}
	return x
}

func (x *agentInstallTest) settingsPath() string {
	return filepath.Join(x.home, ".claude", "settings.json")
}

func (x *agentInstallTest) writeSettings(t *testing.T, raw string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(x.settingsPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(x.settingsPath(), []byte(raw), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(x.settingsPath(), mode); err != nil {
		t.Fatal(err)
	}
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
	x := newAgentInstallTest(t, "agent")
	original := `{"model": "claude-test-model", "env": {"FOO": "bar", "NO_PROXY": "intranet.example"}, "permissions": {"allow": ["Bash(ls)"]}}`
	x.writeSettings(t, original, 0o644)
	if err := InstallAgent(t.Context(), x.opts); err != nil {
		t.Fatal(err)
	}

	service := "gui/" + strconv.Itoa(os.Getuid()) + "/" + agentLaunchLabel
	plistPath := filepath.Join(x.home, "Library", "LaunchAgents", agentLaunchLabel+".plist")
	want := [][]string{{"print", service}, {"bootstrap", "gui/" + strconv.Itoa(os.Getuid()), plistPath}}
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
	logs := filepath.Join(x.home, "Library", "Logs")
	wantArgs := []string{x.opts.binary, "agent", "--listen", x.listen, "--open", "127.0.0.1:8444", "--ca", x.ca,
		"--log-file", filepath.Join(logs, "claw-master-agent.log")}
	if !reflect.DeepEqual(parsed.Strings, wantArgs) {
		t.Fatalf("plist arguments = %q, want %q", parsed.Strings, wantArgs)
	}
	if !strings.Contains(string(plist), filepath.Join(logs, "claw-master-agent.stderr.log")) {
		t.Fatalf("the plist does not capture standard error:\n%s", plist)
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
	for _, path := range []string{x.settingsPath(), x.settingsPath() + agentSettingsBackupSuffix} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, %v; want 0600, since it holds the token", path, info.Mode().Perm(), err)
		}
	}
	if backup, err := os.ReadFile(x.settingsPath() + agentSettingsBackupSuffix); err != nil || string(backup) != original {
		t.Fatalf("backup = %q, %v; want the original file", backup, err)
	}

	// connect accepts these settings, because they lead to the pool; without the allowance it does not.
	tokenPath := filepath.Join(filepath.Dir(x.ca), agentTokenName)
	if err := validateSettingsFile(x.settingsPath(), agentSettingsAllowance(tokenPath, x.ca)); err != nil {
		t.Fatalf("connect would refuse the installed settings: %v", err)
	}
	if err := validateSettingsFile(x.settingsPath(), nil); err == nil {
		t.Fatal("the installed settings pass without the agent allowance")
	}

	// A second install restarts the job and changes nothing in the settings.
	x.calls = nil
	if err := InstallAgent(t.Context(), x.opts); err != nil {
		t.Fatal(err)
	}
	if again := readSettingsForTest(t, x.settingsPath()); !reflect.DeepEqual(again, settings) {
		t.Fatalf("a second install changed the settings: %v", again)
	}
	if len(x.calls) != 3 || x.calls[1][0] != "bootout" {
		t.Fatalf("reinstall launchctl calls = %q, want print, bootout, bootstrap", x.calls)
	}

	// Uninstall needs no flags: it restores what the install replaced and removes what it added.
	if err := UninstallAgent(t.Context(), AgentInstallOptions{home: x.home, binary: "unused", launchctl: x.opts.launchctl}); err != nil {
		t.Fatal(err)
	}
	after := readSettingsForTest(t, x.settingsPath())
	if env, _ := after["env"].(map[string]any); len(env) != 2 || env["FOO"] != "bar" || env["NO_PROXY"] != "intranet.example" || after["model"] != "claude-test-model" {
		t.Fatalf("after uninstall settings = %v, want the original values", after)
	}
	if backup, err := os.ReadFile(x.settingsPath() + agentSettingsBackupSuffix); err != nil || string(backup) != original {
		t.Fatalf("uninstall replaced the backup: %q, %v", backup, err)
	}
	for _, path := range []string{plistPath, filepath.Join(x.home, "Library", "Application Support", "claw-master", "agent-install.json")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s is still there: %v", path, err)
		}
	}
	if x.loaded {
		t.Fatal("the job is still loaded")
	}
}

func TestAgentInstallWritesNoSettingsUnlessItsAgentAnswers(t *testing.T) {
	for _, serve := range []string{"nothing", "other", "forged"} {
		t.Run(serve, func(t *testing.T) {
			x := newAgentInstallTest(t, serve)
			if err := InstallAgent(t.Context(), x.opts); err == nil {
				t.Fatal("install succeeded without the agent answering")
			}
			if _, err := os.Stat(x.settingsPath()); !os.IsNotExist(err) {
				t.Fatalf("settings were written for an agent that is not running: %v", err)
			}
			token, err := agentToken(filepath.Join(filepath.Dir(x.ca), agentTokenName))
			if err != nil {
				t.Fatal(err)
			}
			x.mu.Lock()
			defer x.mu.Unlock()
			for _, request := range x.seen {
				if strings.Contains(request, token) {
					t.Fatalf("the probe sent the token to another program: %q", request)
				}
			}
		})
	}
}

func TestAgentInstallRecordsBeforeItChangesSettings(t *testing.T) {
	x := newAgentInstallTest(t, "agent")
	x.writeSettings(t, `{"model":"claude-test-model"}`, 0o600)
	// The record can be looked for but not written: its directory is read-only.
	records := filepath.Join(x.home, "Library", "Application Support", "claw-master")
	if err := os.MkdirAll(records, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(records, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(records, 0o700) })
	if err := InstallAgent(t.Context(), x.opts); err == nil {
		t.Fatal("install succeeded without its record")
	}
	if raw, _ := os.ReadFile(x.settingsPath()); string(raw) != `{"model":"claude-test-model"}` {
		t.Fatalf("settings changed although nothing records how to undo it: %s", raw)
	}
}

func TestAgentReinstallMakesTheSettingsPrivateAgain(t *testing.T) {
	x := newAgentInstallTest(t, "agent")
	x.writeSettings(t, `{"model":"claude-test-model"}`, 0o600)
	if err := InstallAgent(t.Context(), x.opts); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{x.settingsPath(), x.settingsPath() + agentSettingsBackupSuffix} {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := InstallAgent(t.Context(), x.opts); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{x.settingsPath(), x.settingsPath() + agentSettingsBackupSuffix} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, %v after reinstall; want 0600", path, info.Mode().Perm(), err)
		}
	}
}

func TestAgentUninstallChangesNothingWhenLaunchdCannotSay(t *testing.T) {
	x := newAgentInstallTest(t, "agent")
	if err := InstallAgent(t.Context(), x.opts); err != nil {
		t.Fatal(err)
	}
	installed, err := os.ReadFile(x.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	x.failPrint = true
	if err := UninstallAgent(t.Context(), x.opts); err == nil {
		t.Fatal("uninstall succeeded although launchctl could not say whether the job runs")
	}
	if now, _ := os.ReadFile(x.settingsPath()); string(now) != string(installed) {
		t.Fatal("uninstall changed the settings")
	}
}

func TestAgentInstallRefusesWhatItCannotDoSafely(t *testing.T) {
	cases := map[string]struct {
		settings string
		symlink  bool
		token    bool
	}{
		"not an object":       {settings: `["x"]`},
		"not JSON":            {settings: `{"model":`},
		"env a string":        {settings: `{"env":"x"}`},
		"a lowercase proxy":   {settings: `{"env":{"https_proxy":"http://proxy.example:3128"}}`},
		"an all-scheme proxy": {settings: `{"env":{"ALL_PROXY":"http://proxy.example:3128"}}`},
		"a symlink":           {symlink: true},
		"a custom token file": {settings: `{}`, token: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			x := newAgentInstallTest(t, "agent")
			if c.symlink {
				target := filepath.Join(x.home, "elsewhere.json")
				if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(x.settingsPath()), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, x.settingsPath()); err != nil {
					t.Fatal(err)
				}
			} else {
				x.writeSettings(t, c.settings, 0o600)
			}
			if c.token {
				x.opts.Agent.TokenFile = filepath.Join(x.home, "custom.token")
			}
			before, _ := os.Readlink(x.settingsPath())
			raw, _ := os.ReadFile(x.settingsPath())
			err := InstallAgent(t.Context(), x.opts)
			if err == nil {
				t.Fatal("install went ahead")
			}
			if c.token && !strings.Contains(err.Error(), "--token-file") {
				t.Fatalf("install failed for another reason: %v", err)
			}
			if len(x.calls) != 0 {
				t.Fatalf("install touched launchd before refusing: %q", x.calls)
			}
			after, _ := os.Readlink(x.settingsPath())
			now, _ := os.ReadFile(x.settingsPath())
			if before != after || string(raw) != string(now) {
				t.Fatal("the settings file changed")
			}
		})
	}
}

func TestAgentUninstallChangesNothingWhileTheJobWillNotStop(t *testing.T) {
	x := newAgentInstallTest(t, "agent")
	if err := InstallAgent(t.Context(), x.opts); err != nil {
		t.Fatal(err)
	}
	installed, err := os.ReadFile(x.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	x.failBootout = true
	if err := UninstallAgent(t.Context(), x.opts); err == nil {
		t.Fatal("uninstall succeeded while the job would not stop")
	}
	if now, _ := os.ReadFile(x.settingsPath()); string(now) != string(installed) {
		t.Fatal("uninstall changed the settings around a running agent")
	}
	if _, err := os.Stat(filepath.Join(x.home, "Library", "LaunchAgents", agentLaunchLabel+".plist")); err != nil {
		t.Fatalf("uninstall removed the plist of a running job: %v", err)
	}
}

func TestAgentAnswersItsProbeOnlyWithItsToken(t *testing.T) {
	x := newAgentInstallTest(t, "agent")
	token, err := agentToken(filepath.Join(filepath.Dir(x.ca), agentTokenName))
	if err != nil {
		t.Fatal(err)
	}
	if err := probeAgent(t.Context(), x.listen, token); err != nil {
		t.Fatalf("the agent did not answer its probe: %v", err)
	}
	if err := probeAgent(t.Context(), x.listen, strings.Repeat("A", 26)); err == nil {
		t.Fatal("the agent answered a probe with another token")
	}
	// A launch's forwarder is not an agent.
	launch := startForwarderForTest(t, forwarderNoUpstream(t))
	if err := probeAgent(t.Context(), launch.listener.Addr().String(), launch.token); err == nil {
		t.Fatal("a launch forwarder answered the agent probe")
	}
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
