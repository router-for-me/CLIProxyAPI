package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// UnmarshalYAML rejects typos rather than silently enabling a spending policy.
func (c *BankedResetConfig) UnmarshalYAML(node *yaml.Node) error {
	type plain BankedResetConfig
	var value plain
	if err := node.Decode(&value); err != nil {
		return err
	}
	switch value.Mode {
	case "", "off", "manual", "auto":
	default:
		return fmt.Errorf("banked-reset.mode must be off, manual or auto")
	}
	if value.UseBefore != "" {
		duration, err := time.ParseDuration(value.UseBefore)
		if err != nil || duration < 0 {
			return fmt.Errorf("banked-reset.use-before must be a non-negative duration")
		}
	}
	for _, provider := range value.Providers {
		if provider != "codex" && provider != "claude" {
			return fmt.Errorf("banked-reset.providers supports codex and claude")
		}
	}
	*c = BankedResetConfig(value)
	return nil
}
