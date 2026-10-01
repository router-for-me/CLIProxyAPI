package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/claudemaster"
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
	t.Setenv(claudemaster.BackupAPIKeyEnvironment, "dedicated-key-canary")
	t.Setenv("CUSTOM_BACKUP_KEY", "custom-key-canary")
	t.Setenv("EMPTY_BACKUP_KEY", " \t")
	for _, tc := range []struct {
		input    string
		explicit bool
		key      string
		source   string
	}{
		{"", false, "dedicated-key-canary", claudemaster.BackupAPIKeyEnvironment},
		{"env:" + claudemaster.BackupAPIKeyEnvironment, true, "dedicated-key-canary", claudemaster.BackupAPIKeyEnvironment},
		{"env:CUSTOM_BACKUP_KEY", true, "custom-key-canary", "CUSTOM_BACKUP_KEY"},
	} {
		key, source, err := readBackupAPIKey(tc.input, tc.explicit)
		if err != nil || key != tc.key || source != tc.source {
			t.Fatalf("incorrect backup source selection for %q", tc.input)
		}
	}
	for _, name := range []string{"", "2INVALID", "KEY=secret-canary", "EMPTY_BACKUP_KEY", "UNSET_CLAUDE_MASTER_TEST_KEY"} {
		if name == "UNSET_CLAUDE_MASTER_TEST_KEY" {
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
	t.Setenv(claudemaster.BackupAPIKeyEnvironment, " \t")
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
		"if [ \"${CLAUDE_MASTER_BACKUP_API_KEY+set}\" = set ] || [ \"${ANTHROPIC_API_KEY+set}\" = set ]; then exit 7; fi ;; esac\n" +
		"printf '<%s>\\n' \"$@\"\n"
	for path, script := range map[string]string{
		launcher:                     launcherScript,
		filepath.Join(bin, "go"):     "#!/bin/sh\nexec /bin/cp \"$CLAUDE_MASTER_TEST_LAUNCHER\" \"$3\"\n",
		filepath.Join(bin, "claude"): "#!/bin/sh\nexit 0\n",
	} {
		if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"claude-primary"} {
		if err := os.MkdirAll(filepath.Join(dir, ".local", "share", "claude-master", "profiles", name, "current"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Use synthetic credentials only. The helper must pass the source identifier to
	// run, remove it from its check/login subprocesses, and never expose the value.
	t.Setenv("HOME", dir)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUDE_MASTER_TEST_LAUNCHER", launcher)
	t.Setenv("ANTHROPIC_API_KEY", "selected-key-canary")
	t.Setenv(claudemaster.BackupAPIKeyEnvironment, "dedicated-key-canary")
	cmd := exec.Command("bash", "../../try-claude-master.sh", "--backup-api-key", "env:ANTHROPIC_API_KEY", "--map", "incoming:target", "--model", "incoming", "--remote-control")
	cmd.Stdin = strings.NewReader("\n")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper invocation failed: %v: %s", err, output)
	}
	want := "<run>\n<claude-primary>\n<--next-profile>\n<claude-secondary>\n<--backup-api-key>\n<env:ANTHROPIC_API_KEY>\n<--map>\n<incoming:target>\n<-->\n<--model>\n<incoming>\n<--remote-control>\n"
	if !strings.Contains(string(output), "<login>\n<claude-secondary>\n") || !strings.Contains(string(output), want) || strings.Contains(string(output), "canary") {
		t.Fatalf("launcher/native arguments were not separated or credential was exposed: %s", output)
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
	current := filepath.Join(home, ".local", "share", "claude-master", "profiles", name, "current")
	authDir := filepath.Join(current, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(home, ".local"),
		filepath.Join(home, ".local", "share"),
		filepath.Join(home, ".local", "share", "claude-master"),
		filepath.Join(home, ".local", "share", "claude-master", "profiles"),
		filepath.Join(home, ".local", "share", "claude-master", "profiles", name),
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

func TestOpenRunProfilesPreservesRequestedOrderAndReleasesAllLocks(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	installRunProfileFixture(t, home, "alpha")
	installRunProfileFixture(t, home, "zeta")

	profiles, locks, err := openRunProfiles([]string{"zeta", "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || profiles[0].Name != "zeta" || profiles[1].Name != "alpha" {
		t.Fatalf("profile order changed: %#v", profiles)
	}
	if competing, errOpen := claudemaster.OpenProfile("alpha", false); errOpen == nil {
		_ = competing.Close()
		t.Fatal("series did not reserve every profile")
	}
	closeProfileLocks(locks)
	for _, name := range []string{"alpha", "zeta"} {
		lock, errOpen := claudemaster.OpenProfile(name, false)
		if errOpen != nil {
			t.Fatalf("profile %s remained locked: %v", name, errOpen)
		}
		_ = lock.Close()
	}
}

func TestOpenRunProfilesReleasesPartialLockSet(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	installRunProfileFixture(t, home, "alpha")
	if _, _, err := openRunProfiles([]string{"alpha", "missing"}); err == nil {
		t.Fatal("missing profile accepted")
	}
	lock, err := claudemaster.OpenProfile("alpha", false)
	if err != nil {
		t.Fatalf("partial acquisition leaked alpha lock: %v", err)
	}
	_ = lock.Close()
}
