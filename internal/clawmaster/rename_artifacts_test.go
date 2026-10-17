package clawmaster

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The scripts and the reference deployment around the binary keep working for an install from
// before the rename (2026-10), as the binary does.

func repoFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", path))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestGymFindsProfilesAndSettingsFromBeforeTheRename(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	home := t.TempDir()
	run := func() string {
		cmd := exec.Command("bash", "-c", `source scripts/claw-master-gym/common.sh && gym_profile_root && printf '%s\n' "${CLAW_MASTER_GYM_STATE_DIR:-}"`)
		cmd.Dir = filepath.Join("..", "..")
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "CLAUDE_MASTER_GYM_STATE_DIR=/old/state"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return string(out)
	}
	if err := os.MkdirAll(filepath.Join(home, ".local", "share", "claude-master", "profiles"), 0o700); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".local", "share", "claude-master", "profiles") + "\n/old/state\n"
	if got := run(); got != want {
		t.Fatalf("with only the old directory and setting: %q, want %q", got, want)
	}
	if err := os.MkdirAll(filepath.Join(home, ".local", "share", "claw-master"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := run(); !strings.HasPrefix(got, filepath.Join(home, ".local", "share", "claw-master", "profiles")+"\n") {
		t.Fatalf("with the new directory: %q", got)
	}
}

func TestTryScriptChecksTheProfileDirectoryFromBeforeTheRename(t *testing.T) {
	script := repoFile(t, "try-claw-master.sh")
	if !strings.Contains(script, `profile_root="$HOME/.local/share/claude-master/profiles"`) {
		t.Fatal("try-claw-master.sh does not look in the profile directory from before the rename")
	}
}

func TestReferenceDeploymentDownloadsAPublishedRelease(t *testing.T) {
	vars := repoFile(t, "examples/claw-master-aws/variables.tf")
	tag := regexp.MustCompile(`(?s)variable "release_tag".*?default\s*=\s*"([^"]+)"`).FindStringSubmatch(vars)
	if tag == nil {
		t.Fatal("no default release_tag")
	}
	// Releases are published as claude-master-<commit> (before the rename) or claw-master-<commit>.
	if !regexp.MustCompile(`^(claude|claw)-master-[0-9a-f]{7,}$`).MatchString(tag[1]) {
		t.Fatalf("release_tag %q is not a published release name", tag[1])
	}
	bootstrap := repoFile(t, "examples/claw-master-aws/user-data.sh.tftpl")
	line := regexp.MustCompile(`(?m)^ASSET=.*$`).FindString(bootstrap)
	if line == "" || !strings.Contains(bootstrap, "releases/download/$TAG/$ASSET") {
		t.Fatal("the bootstrap does not download the asset named for the release")
	}
	// Terraform renders $${ as ${: run the line as the box would.
	out, err := exec.Command("bash", "-c", "TAG="+tag[1]+"; "+strings.ReplaceAll(line, "$${", "${")+`; printf %s "$ASSET"`).Output()
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimSuffix(tag[1], tag[1][strings.LastIndex(tag[1], "-"):]) + "-linux-arm64"; string(out) != want || want != "claude-master-linux-arm64" {
		t.Fatalf("asset %q, want %q: the published asset of the default release", out, want)
	}
}

func TestReferenceDeploymentMovesAnInstallFromBeforeTheRename(t *testing.T) {
	bootstrap := repoFile(t, "examples/claw-master-aws/user-data.sh.tftpl")
	migrate := strings.Index(bootstrap, "if id claude-master >/dev/null 2>&1 && ! id claw-master")
	create := strings.Index(bootstrap, "useradd --system")
	if migrate < 0 || create < 0 || migrate > create {
		t.Fatal("the bootstrap must move the old service account before it would create a new one")
	}
	for _, step := range []string{
		`usermod --login claw-master --home "$HOME_DIR" --move-home claude-master`,
		"systemctl stop 'claude-master-server@blue' 'claude-master-server@green'",
	} {
		if !strings.Contains(bootstrap[migrate:create], step) {
			t.Fatalf("the move of an old install lacks: %s", step)
		}
	}
	// The profiles stay where both the old pinned binary and a new one find them.
	if strings.Contains(bootstrap, `mv "$HOME_DIR/.local/share/claude-master"`) {
		t.Fatal("the bootstrap moves the profiles away from the old pinned binary")
	}
}

// The unit starts only when every login exists where the binary looks for it, by the binary's own
// rule (dataDir), for an old pinned release and a new one alike.
func TestReferenceDeploymentFindsLoginsWhereTheBinaryDoes(t *testing.T) {
	bootstrap := repoFile(t, "examples/claw-master-aws/user-data.sh.tftpl")
	unit := bootstrap[strings.Index(bootstrap, "claw-master-server@.service <<'EOF2'"):]
	unit = unit[:strings.Index(unit, "\nEOF2")]
	service := unit[strings.Index(unit, "[Service]"):]
	if !strings.Contains(service, "ExecCondition=/usr/local/bin/claw-master-has-logins") || strings.Contains(unit, "ConditionPathExists=") {
		t.Fatal("the unit must start on claw-master-has-logins in [Service], not on fixed login paths")
	}
	helper := regexp.MustCompile(`(?s)claw-master-profile-root <<'EOF2'\n(.*?)\nEOF2`).FindStringSubmatch(bootstrap)
	if helper == nil {
		t.Fatal("no claw-master-profile-root")
	}
	share := t.TempDir()
	root := func() string {
		script := strings.ReplaceAll(helper[1], "/var/lib/claw-master/.local/share", share)
		out, err := exec.Command("bash", "-c", script).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.MkdirAll(filepath.Join(share, "claude-master"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, want := root(), filepath.Join(dataDir(share), "profiles"); got != want {
		t.Fatalf("with only the old directory the helper says %q, the binary %q", got, want)
	}
	if err := os.MkdirAll(filepath.Join(share, "claw-master"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, want := root(), filepath.Join(dataDir(share), "profiles"); got != want {
		t.Fatalf("with both the helper says %q, the binary %q", got, want)
	}
	// The backup key under the name both an old and a new binary read (serve launches no Claude).
	if !strings.Contains(bootstrap, `export CLAUDE_MASTER_BACKUP_API_KEY="$key"`) {
		t.Fatal("the serve wrapper must export the backup key under the name the old pinned binary reads")
	}
}
