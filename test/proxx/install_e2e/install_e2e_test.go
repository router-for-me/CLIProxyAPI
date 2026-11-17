// Package installe2e runs install.sh for real on macOS, always with a temp HOME, temp PROXX_HOME and
// install dir, and unique PROXX_SERVICE_LABELs, so it can never touch the developer's own proxx agent
// (a guard asserts that agent's state is unchanged). Skipped unless PROXX_E2E=1 and PROXX_E2E_INSTALL=1:
//
//	PROXX_E2E=1 PROXX_E2E_INSTALL=1 go test ./test/proxx/install_e2e/ -v -count=1
//
// TestE2EInstallScriptService downloads the latest proxx release from GitHub.
package installe2e

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/test/proxx/launchdguard"
)

func requireInstallE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("PROXX_E2E") != "1" || os.Getenv("PROXX_E2E_INSTALL") != "1" {
		t.Skip("set PROXX_E2E=1 PROXX_E2E_INSTALL=1 to run the install.sh e2e")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("the install.sh e2e drives launchd (macOS)")
	}
}

func uniqueLabel(_ *testing.T, tag string) string {
	return fmt.Sprintf("%s.e2e-%s-%d-%d", launchdguard.UserLabel, tag, os.Getpid(), time.Now().UnixNano())
}

type sandbox struct {
	t          *testing.T
	root, home string
	proxxHome  string
	installDir string
	label      string
}

func newSandbox(t *testing.T, tag string) *sandbox {
	// /tmp-style short paths: launchd and ps output are easier to read, and HOME must not be the real one.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sb := &sandbox{t: t, root: root, home: filepath.Join(root, "home"), proxxHome: filepath.Join(root, "proxx-home"),
		installDir: filepath.Join(root, "bin"), label: uniqueLabel(t, tag)}
	for _, d := range []string{sb.home, sb.proxxHome} {
		if err = os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	launchdguard.RequireSafeLabel(t, sb.label, sb.home)
	return sb
}

func (sb *sandbox) plistPath(label string) string {
	return filepath.Join(sb.home, "Library", "LaunchAgents", label+".plist")
}

// run executes install.sh with a scrubbed environment: only PATH plus the sandbox variables.
func (sb *sandbox) run(label string, args ...string) (string, error) {
	sb.t.Helper()
	launchdguard.RequireSafeLabel(sb.t, label, sb.home)
	script, err := filepath.Abs("../../../install.sh")
	if err != nil {
		sb.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", append([]string{script}, args...)...)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + sb.home,
		"PROXX_HOME=" + sb.proxxHome,
		"PROXX_INSTALL_DIR=" + sb.installDir,
		"PROXX_SERVICE_LABEL=" + label,
	}
	cmd.Dir = sb.root
	out, err := cmd.CombinedOutput()
	sb.t.Logf("install.sh %s (label %s): err=%v\n%s", strings.Join(args, " "), label, err, out)
	return string(out), err
}

func domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func loaded(label string) (bool, string) {
	out, err := exec.Command("launchctl", "print", domain()+"/"+label).CombinedOutput()
	return err == nil, string(out)
}

func bootout(t *testing.T, label string) {
	if ok, _ := loaded(label); ok {
		out, err := exec.Command("launchctl", "bootout", domain()+"/"+label).CombinedOutput()
		t.Logf("cleanup: launchctl bootout %s: %v %s", label, err, out)
	}
}

// buildSleeper builds a tiny program that ignores its arguments and sleeps, to stand in for "another
// proxx install" in process and launchd checks.
func buildSleeper(t *testing.T, path string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(src, []byte("package main\n\nimport \"time\"\n\nfunc main() { time.Sleep(time.Hour) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", path, src)
	cmd.Env = append(os.Environ(), "GO111MODULE=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build sleeper: %v\n%s", err, out)
	}
}

func writePlist(t *testing.T, path, label string, args ...string) {
	t.Helper()
	var b strings.Builder
	for _, a := range args {
		fmt.Fprintf(&b, "<string>%s</string>", a)
	}
	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key><array>%s</array>
	<key>KeepAlive</key><true/>
</dict></plist>
`, label, b.String())
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestE2EInstallScriptSafety checks (without network) that install.sh never stops, unloads or waits on
// anything but its own binary and its own service file.
func TestE2EInstallScriptSafety(t *testing.T) {
	requireInstallE2E(t)
	launchdguard.Protect(t)
	sb := newSandbox(t, "safety")
	bin := filepath.Join(sb.installDir, "proxx")

	// 1. A process of ANOTHER install whose path contains this install's path ("<x>/<bin> --config")
	// must not be matched by the stop wait (the old `pgrep -f "$BIN --config"` substring match did).
	decoyBin := filepath.Join(sb.root, "other") + bin
	buildSleeper(t, decoyBin)
	decoy := exec.Command(decoyBin, "--config", filepath.Join(sb.root, "other.yaml"))
	if err := decoy.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = decoy.Process.Kill(); _, _ = decoy.Process.Wait() })
	start := time.Now()
	if _, err := sb.run(sb.label, "--uninstall"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("uninstall waited %s: the stop wait matched another install's process", d)
	}
	if err := decoy.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("decoy process of another install was killed: %v", err)
	}
	t.Logf("own_pids: uninstall of %s finished in %s while %s --config ran", bin, time.Since(start).Round(time.Millisecond), decoyBin)

	// 2. A service file for the same label that runs ANOTHER binary is never booted out, removed or
	// overwritten.
	foreignLabel := uniqueLabel(t, "foreign")
	plist := sb.plistPath(foreignLabel)
	writePlist(t, plist, foreignLabel, decoyBin, "--config", filepath.Join(sb.root, "foreign.yaml"))
	if out, err := exec.Command("launchctl", "bootstrap", domain(), plist).CombinedOutput(); err != nil {
		t.Fatalf("bootstrap decoy agent: %v %s", err, out)
	}
	t.Cleanup(func() { bootout(t, foreignLabel) })
	want, _ := os.ReadFile(plist)

	out, err := sb.run(foreignLabel, "--uninstall")
	if err != nil || !strings.Contains(out, "leaving") {
		t.Fatalf("uninstall with a foreign service file: err=%v out=%s", err, out)
	}
	out, err = sb.run(foreignLabel) // service install
	if err == nil || !strings.Contains(out, "runs another proxx binary") {
		t.Fatalf("service install over a foreign service file must refuse: err=%v out=%s", err, out)
	}
	if ok, _ := loaded(foreignLabel); !ok {
		t.Fatal("the foreign agent was unloaded")
	}
	if got, _ := os.ReadFile(plist); string(got) != string(want) {
		t.Fatal("the foreign plist was changed")
	}
	t.Logf("foreign service file %s: left loaded and unchanged by --uninstall and by a service install (refused)", foreignLabel)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// TestE2EInstallScriptService installs the latest release with --no-service and as a launchd agent
// (temp label), upgrades it in place, and uninstalls it.
func TestE2EInstallScriptService(t *testing.T) {
	requireInstallE2E(t)
	launchdguard.Protect(t)
	sb := newSandbox(t, "service")
	t.Cleanup(func() { bootout(t, sb.label) })
	port := freePort(t)
	cfg := fmt.Sprintf("config-version: 8\nserver:\n  host: 127.0.0.1\n  port: %d\nmanagement:\n  secret-key: \"x\"\n  disable-control-panel: true\n", port)
	if err := os.WriteFile(filepath.Join(sb.proxxHome, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := sb.run(sb.label, "--no-service")
	if err != nil || !strings.Contains(out, "Updates:") {
		t.Fatalf("--no-service: err=%v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(sb.proxxHome, ".install-method")); strings.TrimSpace(string(data)) != "script" {
		t.Fatalf(".install-method = %q, want script", data)
	}
	if ok, _ := loaded(sb.label); ok {
		t.Fatal("--no-service loaded a service")
	}

	healthy := func() bool {
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			resp, errGet := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
			if errGet == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return true
				}
			}
			time.Sleep(300 * time.Millisecond)
		}
		return false
	}
	pidOf := func() string {
		_, info := loaded(sb.label)
		for _, line := range strings.Split(info, "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "pid = "); ok {
				return v
			}
		}
		return ""
	}

	if _, err = sb.run(sb.label); err != nil {
		t.Fatalf("service install: %v", err)
	}
	plistData, _ := os.ReadFile(sb.plistPath(sb.label))
	if !strings.Contains(string(plistData), "<key>ExitTimeOut</key><integer>0</integer>") {
		t.Fatalf("plist has no ExitTimeOut 0:\n%s", plistData)
	}
	_, info := loaded(sb.label)
	if !strings.Contains(info, "exit timeout = 0") {
		t.Fatalf("launchd does not report exit timeout = 0:\n%s", info)
	}
	if !healthy() {
		t.Fatal("service not healthy after install")
	}
	pid1 := pidOf()

	if _, err = sb.run(sb.label); err != nil { // re-run = upgrade path: stops its own service, reinstalls
		t.Fatalf("re-install: %v", err)
	}
	if !healthy() {
		t.Fatal("service not healthy after re-install")
	}
	pid2 := pidOf()
	if pid1 == "" || pid2 == "" || pid1 == pid2 {
		t.Fatalf("re-install did not restart its own service: pid %q -> %q", pid1, pid2)
	}

	if _, err = sb.run(sb.label, "--uninstall"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if ok, _ := loaded(sb.label); ok {
		t.Fatal("uninstall left the agent loaded")
	}
	if _, errStat := os.Stat(sb.plistPath(sb.label)); errStat == nil {
		t.Fatal("uninstall left the plist")
	}
	t.Logf("install.sh: --no-service ok (.install-method=script); service install ok (ExitTimeOut 0, launchd exit timeout = 0, /healthz 200, pid %s); re-install restarted it (pid %s); uninstall removed it", pid1, pid2)
}
