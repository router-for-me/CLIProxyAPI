package diff

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestBuildConfigChangeDetailsCooldownStatusCode(t *testing.T) {
	oldCfg := &config.Config{CooldownStatusCode: 0}
	newCfg := &config.Config{CooldownStatusCode: 529}

	details := BuildConfigChangeDetails(oldCfg, newCfg)
	expectContains(t, details, "cooldown-status-code: 0 -> 529")
}
