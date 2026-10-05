package config

import (
	"fmt"
	"strings"
)

type AccountingOutboxConfig struct {
	Enabled         bool   `yaml:"enabled" json:"enabled"`
	DataPath        string `yaml:"data-path" json:"data-path"`
	QueueCapacity   int    `yaml:"queue-capacity" json:"queue-capacity"`
	MaxDiskMB       int    `yaml:"max-disk-mb" json:"max-disk-mb"`
	MaxEvents       int    `yaml:"max-events" json:"max-events"`
	RetentionHours  int    `yaml:"retention-hours" json:"retention-hours"`
	ShutdownSeconds int    `yaml:"shutdown-seconds" json:"shutdown-seconds"`
}

func (c AccountingOutboxConfig) Defaults() AccountingOutboxConfig {
	if c.QueueCapacity == 0 {
		c.QueueCapacity = 512
	}
	if c.MaxDiskMB == 0 {
		c.MaxDiskMB = 256
	}
	if c.MaxEvents == 0 {
		c.MaxEvents = 100000
	}
	if c.RetentionHours == 0 {
		c.RetentionHours = 168
	}
	if c.ShutdownSeconds == 0 {
		c.ShutdownSeconds = 5
	}
	return c
}

func (c AccountingOutboxConfig) Validate() error {
	c = c.Defaults()
	if c.Enabled && strings.TrimSpace(c.DataPath) == "" {
		return fmt.Errorf("observability.accounting-outbox.data-path is required when enabled")
	}
	if c.QueueCapacity < 1 || c.QueueCapacity > 4096 || c.MaxDiskMB < 8 || c.MaxDiskMB > 4096 || c.MaxEvents < 1 || c.MaxEvents > 1000000 || c.RetentionHours < 1 || c.RetentionHours > 8760 || c.ShutdownSeconds < 1 || c.ShutdownSeconds > 30 {
		return fmt.Errorf("invalid observability.accounting-outbox limits")
	}
	return nil
}
