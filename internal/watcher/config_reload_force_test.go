package watcher

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestConfigChangeForcesAuthRefresh(t *testing.T) {
	base := func() *config.Config { return &config.Config{} }
	if configChangeForcesAuthRefresh(nil, base()) || configChangeForcesAuthRefresh(base(), base()) {
		t.Fatal("no change must not force a refresh")
	}
	for name, change := range map[string]func(*config.Config){
		"live lists":   func(c *config.Config) { c.Models.LiveLists = true },
		"model prefix": func(c *config.Config) { c.ForceModelPrefix = true },
		"retry":        func(c *config.Config) { c.RequestRetry = 3 },
	} {
		changed := base()
		change(changed)
		if !configChangeForcesAuthRefresh(base(), changed) {
			t.Fatalf("%s change must force a refresh", name)
		}
	}
}
