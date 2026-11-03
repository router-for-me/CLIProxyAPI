package responses

import (
	"os"
	"testing"
)

// TestMain runs the package with upstream.claude.tool-search on, as the
// tool_search tests expect; tests covering the off state switch it per test.
func TestMain(m *testing.M) {
	SetClientToolSearchEnabled(true)
	os.Exit(m.Run())
}

func setClientToolSearchEnabledForTest(t *testing.T, enabled bool) {
	t.Helper()
	previous := ClientToolSearchEnabled()
	SetClientToolSearchEnabled(enabled)
	t.Cleanup(func() { SetClientToolSearchEnabled(previous) })
}
