//go:build !windows

// Unix only: SIGHUP, bash helpers, and profiles (login, run, serve), which Windows does not support.

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clawmaster"
)

func TestLauncherSignalHelper(t *testing.T) {
	if os.Getenv("CLAW_MASTER_SIGNAL_TEST") != "1" {
		return
	}
	ctx, stop := launcherSignalContext()
	defer stop()
	fmt.Println("ready")
	<-ctx.Done()
	if err := os.WriteFile(os.Getenv("CLAW_MASTER_SIGNAL_MARKER"), []byte("graceful shutdown"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLauncherSIGHUPRunsGracefulShutdown(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "closed")
	child := exec.CommandContext(t.Context(), executable, "-test.run=^TestLauncherSignalHelper$")
	child.Env = []string{"CLAW_MASTER_SIGNAL_TEST=1", "CLAW_MASTER_SIGNAL_MARKER=" + marker}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill() })
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || ready != "ready\n" {
		t.Fatalf("signal helper did not start: %q %v", ready, err)
	}
	if err := child.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatalf("SIGHUP bypassed graceful shutdown: %v", err)
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "graceful shutdown" {
		t.Fatal("SIGHUP did not finish shutdown before exit")
	}
}

func TestTryHelperResolvesRelativeBackupFilesBeforeChangingDirectory(t *testing.T) {
	helper, err := filepath.Abs("../../try-claw-master.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--backup-api-key", "key.txt"}, {"--backup-api-key", "file:key.txt"}, {"--backup-api-key=key.txt"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			launcher := filepath.Join(dir, "launcher")
			launcherScript := "#!/bin/sh\ncase \"$1\" in check|login) exit 0 ;; esac\n" +
				"while [ \"$#\" -gt 0 ]; do\nif [ \"$1\" = --backup-api-key ]; then\nshift\n" +
				"path=${1#file:}\n[ -r \"$path\" ] || exit 9\nprintf 'FILE_OK\\n'\nexit 0\nfi\nshift\ndone\nexit 10\n"
			for path, script := range map[string]string{
				launcher:                     launcherScript,
				filepath.Join(bin, "go"):     "#!/bin/sh\nexec /bin/cp \"$CLAW_MASTER_TEST_LAUNCHER\" \"$3\"\n",
				filepath.Join(bin, "claude"): "#!/bin/sh\nexit 0\n",
			} {
				if err := os.WriteFile(path, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"claude-primary", "claude-secondary"} {
				if err := os.MkdirAll(filepath.Join(dir, ".local", "share", "claw-master", "profiles", name, "current"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "key.txt"), []byte("synthetic-not-a-key"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", dir)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("CLAW_MASTER_TEST_LAUNCHER", launcher)
			t.Chdir(dir)
			output, err := exec.Command("bash", append([]string{helper}, args...)...).CombinedOutput()
			if err != nil || !strings.Contains(string(output), "FILE_OK") || strings.Contains(string(output), "synthetic-not-a-key") {
				t.Fatalf("relative file was not consumed without exposing its value: %v %s", err, output)
			}
		})
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
	// Runs share a profile (several Claude Code windows, one login); a login needs it alone.
	other, errShared := clawmaster.OpenProfile("alpha", false)
	if errShared != nil {
		t.Fatalf("a second run could not share the profile: %v", errShared)
	}
	_ = other.Close()
	if competing, errOpen := clawmaster.OpenProfile("alpha", true); errOpen == nil {
		_ = competing.Close()
		t.Fatal("a login took a profile that a run is using")
	}
	closeProfileLocks(locks)
	for _, name := range []string{"alpha", "zeta"} {
		lock, errOpen := clawmaster.OpenProfile(name, false)
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
	lock, err := clawmaster.OpenProfile("alpha", false)
	if err != nil {
		t.Fatalf("partial acquisition leaked alpha lock: %v", err)
	}
	_ = lock.Close()
}
