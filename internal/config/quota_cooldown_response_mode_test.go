package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuotaCooldownResponseModeConfig(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
		wantErr   bool
	}{
		{raw: "routing:\n  cooldown:\n    quota-response-mode: ' Usage-Limit '\n", want: QuotaCooldownResponseModeUsageLimit},
		{raw: "port: 8317\n", want: ""},
		{raw: "routing:\n  cooldown:\n    quota-response-mode: sometimes\n", wantErr: true},
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(tc.raw), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(path)
		if tc.wantErr {
			if err == nil || !strings.Contains(err.Error(), "quota-response-mode") {
				t.Fatalf("%q: error = %v, want an invalid quota-response-mode error", tc.raw, err)
			}
			continue
		}
		if err != nil || cfg.QuotaCooldownResponseMode != tc.want {
			t.Fatalf("%q: mode = %q, err = %v, want %q", tc.raw, cfg.QuotaCooldownResponseMode, err, tc.want)
		}
		// An unset mode leaves the config the management API serves unchanged.
		if out, _ := json.Marshal(cfg); strings.Contains(string(out), "quota-cooldown-response-mode") != (tc.want != "") {
			t.Fatalf("%q: config JSON = %s", tc.raw, out)
		}
	}
}
