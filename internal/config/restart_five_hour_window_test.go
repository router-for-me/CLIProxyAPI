package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestartFiveHourWindowLayouts(t *testing.T) {
	cases := map[string]string{
		"legacy": "port: 8317\nrestart-five-hour-window: false\n",
		"v8":     "config-version: 8\nserver:\n  port: 8317\noauth:\n  restart-five-hour-window: false\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, err := ParseConfigBytes([]byte(body))
			if err != nil {
				t.Fatalf("ParseConfigBytes: %v", err)
			}
			if cfg.RestartFiveHourWindow {
				t.Fatal("ParseConfigBytes: restart-five-hour-window should be false")
			}

			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err = LoadConfig(path)
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if cfg.RestartFiveHourWindow {
				t.Fatal("LoadConfig: restart-five-hour-window should be false")
			}
			if err := SaveConfigPreserveComments(path, cfg, true); err != nil {
				t.Fatalf("SaveConfigPreserveComments: %v", err)
			}
			saved, _ := os.ReadFile(path)
			if strings.Contains(string(saved), "# restart-five-hour-window") {
				t.Fatalf("restart-five-hour-window was commented out:\n%s", saved)
			}
			if !strings.Contains(string(saved), "oauth:") {
				t.Fatalf("expected v8 layout:\n%s", saved)
			}
			cfg, err = LoadConfig(path)
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
			if cfg.RestartFiveHourWindow {
				t.Fatalf("reload: restart-five-hour-window should be false:\n%s", saved)
			}
		})
	}
	cfg, err := ParseConfigBytes([]byte("config-version: 8\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RestartFiveHourWindow {
		t.Fatal("restart-five-hour-window should default to true")
	}
}
