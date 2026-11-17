// Package launchdguard protects the developer's own proxx launchd agent from e2e tests: tests that touch
// launchd or install.sh must never load, unload or rewrite it.
package launchdguard

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// UserLabel is the label install.sh uses for a real install.
const UserLabel = "io.github.iamkunal9.proxx"

type state struct {
	loaded bool
	plist  []byte
	err    string
}

func snapshot() state {
	var st state
	out, err := exec.Command("launchctl", "print", "gui/"+strconv.Itoa(os.Getuid())+"/"+UserLabel).CombinedOutput()
	st.loaded = err == nil && bytes.Contains(out, []byte("state = "))
	home, errHome := os.UserHomeDir()
	if errHome != nil {
		st.err = errHome.Error()
		return st
	}
	st.plist, _ = os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", UserLabel+".plist"))
	return st
}

// Protect records the user's agent state now and fails the test if it differs when the test ends.
// Call it first, so its cleanup runs after every other cleanup (temp agents booted out).
func Protect(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		return
	}
	before := snapshot()
	t.Logf("launchd guard: %s loaded=%v before the test", UserLabel, before.loaded)
	t.Cleanup(func() {
		after := snapshot()
		if after.loaded != before.loaded {
			t.Errorf("launchd guard: the user's agent %s was loaded=%v before the test and loaded=%v after it", UserLabel, before.loaded, after.loaded)
		}
		if !bytes.Equal(after.plist, before.plist) {
			t.Errorf("launchd guard: the user's %s.plist changed during the test", UserLabel)
		}
		t.Logf("launchd guard: %s loaded=%v after the test (unchanged)", UserLabel, after.loaded)
	})
}

// RequireSafeLabel fails the test for a label or HOME that would touch the user's install.
func RequireSafeLabel(t *testing.T, label, home string) {
	t.Helper()
	if label == "" || label == UserLabel || !strings.HasPrefix(label, UserLabel+".") {
		t.Fatalf("unsafe launchd label %q: tests must use a unique %s.<suffix> label", label, UserLabel)
	}
	if realHome, err := os.UserHomeDir(); err == nil && home != "" {
		if filepath.Clean(home) == filepath.Clean(realHome) {
			t.Fatalf("unsafe HOME %q: tests must use a temp HOME", home)
		}
	}
}
