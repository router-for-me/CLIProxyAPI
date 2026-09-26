package synthesizer

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain isolates the real `cmdc login` credential file from the rest of the
// synthesizer test suite. The CLI resolver is pointed at a non-existent path by
// default so existing tests never observe the developer's real credential.
// Tests that exercise CLI credential import override the resolver themselves.
func TestMain(m *testing.M) {
	orig := commandCodeCLIAuthPathFn
	commandCodeCLIAuthPathFn = func() (string, error) {
		return filepath.Join(os.TempDir(), "cmdc-test-no-auth.json"), nil
	}
	code := m.Run()
	commandCodeCLIAuthPathFn = orig
	os.Exit(code)
}
