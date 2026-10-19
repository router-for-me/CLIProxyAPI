package cliproxy

import (
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestSessionAffinityPersistencePathRejectsAuthDirJSON(t *testing.T) {
	authDir := t.TempDir()
	cases := []struct {
		name, path, want string
	}{
		{"unset", "", ""},
		{"outside auth dir", filepath.Join(t.TempDir(), "session-affinity.json"), "keep"},
		{"auth dir json", filepath.Join(authDir, "session-affinity.json"), ""},
		{"auth dir non-json", filepath.Join(authDir, "session-affinity.snapshot"), "keep"},
		{"auth dir subdirectory", filepath.Join(authDir, "state", "session-affinity.json"), "keep"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.AuthDir = authDir
			cfg.Routing.SessionAffinityPersistencePath = tc.path
			want := tc.want
			if want == "keep" {
				want = tc.path
			}
			if got := sessionAffinityPersistencePath(cfg); got != want {
				t.Fatalf("sessionAffinityPersistencePath(%q) = %q, want %q", tc.path, got, want)
			}
		})
	}
}
