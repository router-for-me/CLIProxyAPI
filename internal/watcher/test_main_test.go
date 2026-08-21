package watcher

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
)

// TestMain isolates the real `cmdc login` credential file from the watcher
// test suite. The CLI resolver is pointed at a non-existent path by default so
// existing tests never observe the developer's real credential.
func TestMain(m *testing.M) {
	orig := synthesizer.SetCommandCodeCLIAuthPathFnForTest(func() (string, error) {
		return filepath.Join(os.TempDir(), "cmdc-test-no-auth.json"), nil
	})
	code := m.Run()
	synthesizer.SetCommandCodeCLIAuthPathFnForTest(orig)
	os.Exit(code)
}
