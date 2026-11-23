package codex

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gofrs/flock"
)

func TestWithFileLockExcludesOtherProcesses(t *testing.T) {
	const pathEnv = "CLI_PROXY_TEST_CODEX_LOCK_PATH"
	const availableEnv = "CLI_PROXY_TEST_CODEX_LOCK_AVAILABLE"

	if path := os.Getenv(pathEnv); path != "" {
		lock := flock.New(path)
		t.Cleanup(func() {
			if errClose := lock.Close(); errClose != nil {
				t.Errorf("close lock: %v", errClose)
			}
		})
		got, errTry := lock.TryLock()
		if errTry != nil {
			t.Fatalf("try lock: %v", errTry)
		}
		if want := os.Getenv(availableEnv) == "true"; got != want {
			t.Fatalf("lock acquired = %t, want %t", got, want)
		}
		return
	}

	authPath := filepath.Join(t.TempDir(), "codex.json")
	probe := func(available bool) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestWithFileLockExcludesOtherProcesses$")
		cmd.Env = append(os.Environ(), pathEnv+"="+canonicalLockPath(authPath)+".lock", availableEnv+"="+strconv.FormatBool(available))
		if output, errRun := cmd.CombinedOutput(); errRun != nil {
			t.Fatalf("probe lock (available = %t): %v\n%s", available, errRun, output)
		}
	}
	if errLock := WithFileLock(authPath, func() error {
		probe(false)
		return nil
	}); errLock != nil {
		t.Fatal(errLock)
	}
	probe(true)
}
