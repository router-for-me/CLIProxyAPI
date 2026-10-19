package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clawmaster"
)

func TestInvalidArgumentsStopBeforeProfileOrLogin(t *testing.T) {
	for _, args := range [][]string{
		nil, {"help"}, {"unknown", "profile"},
		{"login", "profile", "--provider", "unknown"},
		{"login", "profile", "--provider", "codex"},
		{"login", "profile", "--provider", "claude", "--model", "test"},
		{"login", "profile", "--provider", "claude", "--", "unexpected"},
		{"run", "profile", "--model", "test"}, {"run", "profile", "--provider", "claude"},
		{"run", "profile", "--unknown-secret=canary"},
		{"probe", "profile"}, {"probe", "profile", "--model", "test", "--", "unexpected"},
		{"probe", "profile", "--model", "test", "--diagnostics"},
		{"login", "profile", "--provider", "claude", "--diagnostics"},
		{"login", "profile", "--provider", "claude", "--next-profile", "other"},
		{"probe", "profile", "--model", "test", "--next-profile", "other"},
		{"run", "profile", "--next-profile", "profile"},
		{"run", "profile", "--next-profile", ""},
		{"run", "profile", "--next-profile", "../unsafe"},
		{"login", "profile", "--backup-api-key", "env:KEY"},
		{"probe", "profile", "--model", "test", "--backup-api-key", "env:KEY"},
		{"run", "profile", "--backup-api-key-env", "KEY"},
		{"login", "profile", "--map", "incoming:target"},
		{"probe", "profile", "--model", "test", "--map", "incoming:target"},
		{"run", "profile", "--map", "incoming"},
		{"run", "profile", "--map", ":target"},
		{"run", "profile", "--map", "incoming:"},
		{"run", "profile", "--map", "incoming:target:extra"},
		{"run", "profile", "--map", "incoming:target", "--map", "incoming:other"},
	} {
		code, err := run(args)
		if err == nil || code != 2 {
			t.Fatalf("invalid command accepted: %q, code=%d, err=%v", args, code, err)
		}
	}
}

func TestBackupAPIKeyReadSelectionAndSanitizedErrors(t *testing.T) {
	t.Setenv(clawmaster.BackupAPIKeyEnvironment, "dedicated-key-canary")
	t.Setenv("CUSTOM_BACKUP_KEY", "custom-key-canary")
	t.Setenv("EMPTY_BACKUP_KEY", " \t")
	for _, tc := range []struct {
		input    string
		explicit bool
		key      string
		source   string
	}{
		{"", false, "dedicated-key-canary", clawmaster.BackupAPIKeyEnvironment},
		{"env:" + clawmaster.BackupAPIKeyEnvironment, true, "dedicated-key-canary", clawmaster.BackupAPIKeyEnvironment},
		{"env:CUSTOM_BACKUP_KEY", true, "custom-key-canary", "CUSTOM_BACKUP_KEY"},
	} {
		key, source, err := readBackupAPIKey(tc.input, tc.explicit)
		if err != nil || key != tc.key || source != tc.source {
			t.Fatalf("incorrect backup source selection for %q", tc.input)
		}
	}
	for _, name := range []string{"", "2INVALID", "KEY=secret-canary", "EMPTY_BACKUP_KEY", "UNSET_CLAW_MASTER_TEST_KEY"} {
		if name == "UNSET_CLAW_MASTER_TEST_KEY" {
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		}
		key, source, err := readBackupAPIKey("env:"+name, true)
		if err == nil || key != "" || source != "" || strings.Contains(err.Error(), "canary") {
			t.Fatalf("invalid backup source accepted or leaked: %q", name)
		}
	}
	t.Setenv(clawmaster.BackupAPIKeyEnvironment, " \t")
	if key, source, err := readBackupAPIKey("", false); key != "" || source != "" || err != nil {
		t.Fatal("default empty backup environment did not preserve subscription-only mode")
	}
}

func TestInvalidBackupEnvironmentStopsBeforeProfileWrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("EMPTY_BACKUP_KEY", "")
	for _, envName := range []string{"", "EMPTY_BACKUP_KEY", "KEY=secret-canary", "2INVALID"} {
		code, err := run([]string{"run", "new-profile", "--backup-api-key", "env:" + envName})
		if code != 2 || err == nil || strings.Contains(err.Error(), "secret-canary") {
			t.Fatalf("invalid backup argument accepted or leaked: code=%d err=%v", code, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".local")); !os.IsNotExist(err) {
		t.Fatal("invalid backup environment caused profile writes")
	}
}

func TestBackupAPIKeyFilesAreBoundedAndTrimmed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api-key.txt")
	if err := os.WriteFile(path, []byte(" \nfile-key-canary\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{path, "file:" + path} {
		key, envName, err := readBackupAPIKey(source, true)
		if err != nil || key != "file-key-canary" || envName != "" {
			t.Fatal("valid key file was not consumed only by the launcher")
		}
	}
	for _, content := range []string{" \r\n", strings.Repeat("x", backupAPIKeyByteLimit+1), "canary\nsecond-line", "canary spaced", "canary\x00", "non-ascii-Å"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if key, source, err := readBackupAPIKey(path, true); err == nil || key != "" || source != "" || strings.Contains(err.Error(), dir) {
			t.Fatal("empty or oversized key file accepted or path exposed")
		}
	}
	for _, source := range []string{"", "file:", "file:" + dir, filepath.Join(dir, "missing"), "sk-ant-not-a-literal-key-canary"} {
		key, envName, err := readBackupAPIKey(source, true)
		if err == nil || key != "" || envName != "" || strings.Contains(err.Error(), "canary") || strings.Contains(err.Error(), dir) {
			t.Fatal("invalid key source accepted or echoed")
		}
	}
}

func TestBackupAPIKeyInvalidCharactersStopBeforeProfileWrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, value := range []string{"canary\nsecond-line", "canary spaced", "non-ascii-Å", strings.Repeat("x", backupAPIKeyByteLimit+1)} {
		t.Setenv("CUSTOM_BACKUP_KEY", value)
		code, err := run([]string{"run", "new-profile", "--backup-api-key", "env:CUSTOM_BACKUP_KEY"})
		if code != 2 || err == nil || strings.Contains(err.Error(), "canary") {
			t.Fatal("malformed key caused startup or leaked its value")
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".local")); !os.IsNotExist(err) {
		t.Fatal("malformed key caused profile writes")
	}
}

func TestTryHelperSplitsLauncherOptionsAndFiltersLoginHelpers(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("helper requires bash")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(dir, "launcher")
	launcherScript := "#!/bin/sh\n" +
		"case \"$1\" in check|login)\n" +
		"if [ \"${CLAW_MASTER_BACKUP_API_KEY+set}\" = set ] || [ \"${ANTHROPIC_API_KEY+set}\" = set ]; then exit 7; fi ;; esac\n" +
		"printf '<%s>\\n' \"$@\"\n"
	for path, script := range map[string]string{
		launcher:                     launcherScript,
		filepath.Join(bin, "go"):     "#!/bin/sh\nexec /bin/cp \"$CLAW_MASTER_TEST_LAUNCHER\" \"$3\"\n",
		filepath.Join(bin, "claude"): "#!/bin/sh\nexit 0\n",
	} {
		if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"claude-primary"} {
		if err := os.MkdirAll(filepath.Join(dir, ".local", "share", "claw-master", "profiles", name, "current"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Use synthetic credentials only. The helper must pass the source identifier to
	// run, remove it from its check/login subprocesses, and never expose the value.
	t.Setenv("HOME", dir)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAW_MASTER_TEST_LAUNCHER", launcher)
	t.Setenv("ANTHROPIC_API_KEY", "selected-key-canary")
	t.Setenv(clawmaster.BackupAPIKeyEnvironment, "dedicated-key-canary")
	cmd := exec.Command("bash", "../../try-claw-master.sh", "--backup-api-key", "env:ANTHROPIC_API_KEY", "--map", "incoming:target", "--model", "incoming", "--remote-control")
	cmd.Stdin = strings.NewReader("\n")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper invocation failed: %v: %s", err, output)
	}
	want := "<run>\n<claude-primary>\n<--next-profile>\n<claude-secondary>\n<--backup-api-key>\n<env:ANTHROPIC_API_KEY>\n<--map>\n<incoming:target>\n<-->\n<--model>\n<incoming>\n<--remote-control>\n"
	if !strings.Contains(string(output), "<login>\n<claude-secondary>\n") || !strings.Contains(string(output), want) || strings.Contains(string(output), "canary") {
		t.Fatalf("launcher/native arguments were not separated or credential was exposed: %s", output)
	}
	// No launcher overrides leaves an empty argument array. Bash 3 with nounset
	// must still launch the normal subscription session and its default flag.
	if err := os.Unsetenv("ANTHROPIC_API_KEY"); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("bash", "../../try-claw-master.sh")
	cmd.Stdin = strings.NewReader("\n")
	output, err = cmd.CombinedOutput()
	want = "<run>\n<claude-primary>\n<--next-profile>\n<claude-secondary>\n<-->\n<--remote-control>\n"
	if err != nil || !strings.Contains(string(output), want) || strings.Contains(string(output), "canary") {
		t.Fatalf("default helper launch failed or exposed credentials: %v %s", err, output)
	}
}

func TestModelMapExactPairs(t *testing.T) {
	var mappings modelMapFlag
	for _, value := range []string{" source : target ", "target:third"} {
		if err := mappings.Set(value); err != nil {
			t.Fatal(err)
		}
	}
	if mappings["source"] != "target" || mappings["target"] != "third" || mappings.String() != "source:target,target:third" {
		t.Fatal("model mappings were normalized beyond trimming or ordered nondeterministically")
	}
	for _, value := range []string{"", "source:duplicate", "empty: ", " :target", "a:b:c"} {
		if err := mappings.Set(value); err == nil {
			t.Fatalf("invalid mapping accepted: %q", value)
		}
	}
}

func TestRunModelSelectionBelongsToNativeClaude(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, args := range [][]string{
		{"run", "missing"},
		{"run", "missing", "--", "--model", "sonnet", "--fallback-model", "haiku"},
	} {
		code, err := run(args)
		if err == nil || code != 1 {
			t.Fatalf("run arguments did not reach profile loading: %q, code=%d, err=%v", args, code, err)
		}
	}
}

func installRunProfileFixture(t *testing.T, home, name string) {
	t.Helper()
	current := filepath.Join(home, ".local", "share", "claw-master", "profiles", name, "current")
	authDir := filepath.Join(current, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(home, ".local"),
		filepath.Join(home, ".local", "share"),
		filepath.Join(home, ".local", "share", "claw-master"),
		filepath.Join(home, ".local", "share", "claw-master", "profiles"),
		filepath.Join(home, ".local", "share", "claw-master", "profiles", name),
		current,
		authDir,
	} {
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(authDir, "synthetic.json"), []byte(`{"type":"claude"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(current, "profile.json"), []byte(`{"provider":"claude","auth_id":"synthetic.json"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A server that cannot start must exit non-zero: systemd's Restart=on-failure and any deploy
// script judge it by the exit status alone.
func TestServeThatCannotStartExitsNonZero(t *testing.T) {
	home := canonicalHome(t)
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	installRunProfileFixture(t, home, "alpha")
	code, err := run([]string{"serve", "alpha", "--listen", "8.8.8.8:8443", "--state-dir", filepath.Join(home, "state")})
	if err == nil || code == 0 {
		t.Fatalf("serve with a public listen address returned code %d, error %v", code, err)
	}
}

// canonicalHome is a private temp directory with symlinks resolved: macOS places TempDir below /var,
// a symlink to /private/var, and profile paths with symlinks are (rightly) refused.
func canonicalHome(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// The open (no certificate) listener must never be offered on anything but loopback.
func TestServeRefusesANonLoopbackOpenListener(t *testing.T) {
	home := canonicalHome(t)
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	installRunProfileFixture(t, home, "alpha")
	code, err := run([]string{"serve", "alpha", "--listen", "127.0.0.1:0", "--open-loopback", "10.0.0.10:8444", "--state-dir", filepath.Join(home, "state")})
	if err == nil || code == 0 {
		t.Fatalf("a non-loopback --open-loopback returned code %d, error %v", code, err)
	}
}

// run's stderr is Claude's terminal, so logging there must go to a file.
func TestRunLoggingNeedsAFileAndServeLevelsAreValidated(t *testing.T) {
	home := canonicalHome(t)
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	installRunProfileFixture(t, home, "alpha")
	code, err := run([]string{"run", "alpha", "--log-level", "info", "--", "--version"})
	if err == nil || code != 2 || !strings.Contains(err.Error(), "--log-file") {
		t.Fatalf("run --log-level info without a file: code=%d err=%v", code, err)
	}
	code, err = run([]string{"serve", "alpha", "--listen", "127.0.0.1:0", "--state-dir", filepath.Join(home, "state"), "--log-level", "loud"})
	if err == nil || code != 2 {
		t.Fatalf("an unknown level was accepted: code=%d err=%v", code, err)
	}
}

// Several servers exporting to one backend must not merge into one series: each carries an instance.
func TestMetricInstanceIsTheFlagElseTheHostname(t *testing.T) {
	hostname := func() (string, error) { return "example-host\n", nil }
	if got := metricInstance(" server-b ", hostname); got != "server-b" {
		t.Errorf("--instance server-b gave %q", got)
	}
	if got := metricInstance("", hostname); got != "example-host" {
		t.Errorf("no --instance gave %q, want the hostname", got)
	}
	if got := metricInstance("", func() (string, error) { return "", errors.New("no hostname") }); got != "" {
		t.Errorf("with no hostname the instance is %q, want none", got)
	}
}

// --instance is a flag of serve and run; an unknown flag would fail before the later checks.
func TestServeAndRunAcceptTheInstanceFlag(t *testing.T) {
	home := canonicalHome(t)
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	installRunProfileFixture(t, home, "alpha")
	code, err := run([]string{"run", "alpha", "--instance", "server-b", "--log-level", "info", "--", "--version"})
	if err == nil || code != 2 || !strings.Contains(err.Error(), "--log-file") {
		t.Fatalf("run --instance: code=%d err=%v; want the later --log-file check", code, err)
	}
	code, err = run([]string{"serve", "alpha", "--instance", "server-b", "--listen", "127.0.0.1:0", "--state-dir", filepath.Join(home, "state"), "--log-level", "loud"})
	if err == nil || code != 2 || strings.Contains(err.Error(), "instance") {
		t.Fatalf("serve --instance: code=%d err=%v; want the later --log-level check", code, err)
	}
}

// --balanced and --drain-timeout are flags of serve; an unknown flag would fail before the later checks.
func TestServeAcceptsTheBalancerFlags(t *testing.T) {
	home := canonicalHome(t)
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	installRunProfileFixture(t, home, "alpha")
	code, err := run([]string{"serve", "alpha", "--balanced", "--drain-timeout", "10m", "--listen", "127.0.0.1:0", "--state-dir", filepath.Join(home, "state"), "--log-level", "loud"})
	if err == nil || code != 2 || strings.Contains(err.Error(), "launcher arguments") {
		t.Fatalf("serve --balanced --drain-timeout: code=%d err=%v; want the later --log-level check", code, err)
	}
	if code, err := run([]string{"serve", "alpha", "--drain-timeout", "soon"}); err == nil || code != 2 {
		t.Fatalf("a malformed --drain-timeout must be refused: code=%d err=%v", code, err)
	}
}

func TestBackupAPIKeyFallsBackToItsNameBeforeTheRename(t *testing.T) {
	t.Setenv(clawmaster.BackupAPIKeyEnvironment, "")
	t.Setenv(clawmaster.LegacyBackupAPIKeyEnvironment, "old-name-key")
	key, source, err := readBackupAPIKey("", false)
	if err != nil || key != "old-name-key" || source != clawmaster.LegacyBackupAPIKeyEnvironment {
		t.Fatalf("the old name's key was not read: source %q, err %v", source, err)
	}
	t.Setenv(clawmaster.BackupAPIKeyEnvironment, "new-name-key")
	key, source, err = readBackupAPIKey("", false)
	if err != nil || key != "new-name-key" || source != clawmaster.BackupAPIKeyEnvironment {
		t.Fatalf("the new name did not win: source %q, err %v", source, err)
	}
}
