//go:build !windows

package clawmaster

import (
	"os"
	"path/filepath"
	"testing"
)

// loosenForTest lets others read path (and enter it, if it is a directory).
func loosenForTest(t *testing.T, path string) {
	t.Helper()
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		mode = 0o755
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// writeFakeClaude puts a `claude` that exits 0 in bin.
func writeFakeClaude(t *testing.T, bin string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func systemPathForTest() string { return "/usr/bin:/bin" }

// makeReadOnlyForTest lets nobody create or change entries in dir until the returned func runs.
func makeReadOnlyForTest(t *testing.T, dir string) func() {
	t.Helper()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	return func() { _ = os.Chmod(dir, 0o700) }
}

// writeGymShim puts a `claude` in bin that runs this test binary's gym helper.
func writeGymShim(t *testing.T, bin, binaryEnv string) {
	t.Helper()
	shim := "#!/bin/sh\nexec \"$" + binaryEnv + "\" -test.run='^TestForwarderGymHelper$' -test.count=1 -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(shim), 0o700); err != nil {
		t.Fatal(err)
	}
}

// setHomeForTest makes home the user's home directory for this test.
func setHomeForTest(t *testing.T, home string) { t.Setenv("HOME", home) }

// requirePrivateFile fails unless path is a regular file with mode exactly 0600.
func requirePrivateFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("%s is %v, want a regular file with mode 0600", path, info.Mode())
	}
}
