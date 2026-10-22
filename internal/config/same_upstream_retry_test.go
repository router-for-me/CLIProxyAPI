package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSameUpstreamRetryConfig(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      int
	}{
		{"omitted", "routing: {retry: {request-retry: 0}}\n", 0},
		{"v8", "routing: {retry: {same-upstream-retry: 2}}\n", 2},
		{"legacy", "same-upstream-retry: 3\n", 3},
		{"negative", "routing: {retry: {same-upstream-retry: -1}}\n", 0},
		{"v8 precedence", "same-upstream-retry: 5\nrouting: {retry: {same-upstream-retry: 0}}\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, errParse := ParseConfigBytes([]byte(tc.raw))
			if errParse != nil || cfg.SameUpstreamRetry != tc.want {
				t.Fatalf("ParseConfigBytes() = %+v, %v, want retry %d", cfg, errParse, tc.want)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if errWrite := os.WriteFile(path, []byte(tc.raw), 0600); errWrite != nil {
				t.Fatal(errWrite)
			}
			cfg, errLoad := LoadConfig(path)
			if errLoad != nil || cfg.SameUpstreamRetry != tc.want {
				t.Fatalf("LoadConfig() = %+v, %v, want retry %d", cfg, errLoad, tc.want)
			}
			cfg.SameUpstreamRetry = 4
			if errSave := SaveConfigPreserveComments(path, cfg); errSave != nil {
				t.Fatal(errSave)
			}
			loaded, errReload := LoadConfig(path)
			if errReload != nil || loaded.SameUpstreamRetry != 4 {
				t.Fatalf("saved config = %+v, %v, want retry 4", loaded, errReload)
			}
		})
	}
}
