package cliproxy

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/bundledplugins"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
)

// installBundledPlugins copies plugins shipped with the binary into the plugins
// directory before the plugin host applies the config. Failures are logged and
// do not stop the service; the host then runs with what is already installed.
func installBundledPlugins(cfg *config.Config) {
	if cfg == nil || !cfg.Plugins.Enabled {
		return
	}
	installed, err := bundledplugins.Install(bundledplugins.SourceDir(), cfg.Plugins.Dir)
	for _, path := range installed {
		log.Infof("bundled plugin installed: %s", path)
	}
	if err != nil {
		log.Warnf("bundled plugins: %v", err)
	}
}
