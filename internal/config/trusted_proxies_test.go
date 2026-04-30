package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLoadConfigOptional_TrustedProxies(t *testing.T) {
	testCases := []struct {
		name    string
		yaml    string
		want    []string
		wantNil bool
	}{
		{
			name:    "omitted",
			yaml:    "port: 8317\n",
			wantNil: true,
		},
		{
			name: "custom trims and drops empty entries",
			yaml: `
trusted-proxies:
  - "  10.0.0.0/8  "
  - ""
  - "  192.168.1.10  "
`,
			want: []string{"10.0.0.0/8", "192.168.1.10"},
		},
		{
			name: "explicit empty",
			yaml: "trusted-proxies: []\n",
			want: []string{},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(configPath, []byte(tc.yaml), 0o600); err != nil {
				t.Fatalf("failed to write config: %v", err)
			}

			cfg, err := LoadConfigOptional(configPath, false)
			if err != nil {
				t.Fatalf("LoadConfigOptional() error = %v", err)
			}

			if tc.wantNil {
				if cfg.TrustedProxies != nil {
					t.Fatalf("TrustedProxies = %#v, want nil", cfg.TrustedProxies)
				}
				return
			}
			if !slices.Equal(cfg.TrustedProxies, tc.want) {
				t.Fatalf("TrustedProxies = %#v, want %#v", cfg.TrustedProxies, tc.want)
			}
		})
	}
}
