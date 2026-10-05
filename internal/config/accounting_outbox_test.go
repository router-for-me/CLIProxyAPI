package config

import (
	"gopkg.in/yaml.v3"
	"testing"
)

func TestAccountingOutboxConfiguration(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte("observability:\n  accounting-outbox:\n    enabled: true\n    data-path: /data/accounting\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.AccountingOutbox.Enabled || cfg.AccountingOutbox.DataPath != "/data/accounting" {
		t.Fatalf("%+v", cfg.AccountingOutbox)
	}
	defaults := cfg.AccountingOutbox.Defaults()
	if defaults.QueueCapacity != 512 || defaults.MaxDiskMB != 256 || defaults.MaxEvents != 100000 || defaults.RetentionHours != 168 || defaults.ShutdownSeconds != 5 {
		t.Fatalf("%+v", defaults)
	}
	for _, input := range []string{"enabled: true", "queue-capacity: -1", "max-disk-mb: 7", "max-events: -1", "retention-hours: -1", "shutdown-seconds: 31"} {
		cfg = Config{}
		if err := yaml.Unmarshal([]byte("observability:\n  accounting-outbox:\n    "+input+"\n"), &cfg); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}
