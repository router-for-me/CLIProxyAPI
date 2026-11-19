package pluginhost

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/bundledplugins"
	log "github.com/sirupsen/logrus"
)

// installBundledPlugins copies plugins shipped with the binary into dir before
// ApplyConfig selects plugin files. Every load path goes through ApplyConfig,
// so the first load of a process already uses the bundled version, including
// after an upgrade that replaced it. Failures are logged; the host continues
// with what is installed.
func installBundledPlugins(dir string) {
	installed, err := bundledplugins.Install(bundledplugins.SourceDir(), dir)
	for _, path := range installed {
		log.Infof("bundled plugin installed: %s", path)
	}
	if err != nil {
		log.Warnf("bundled plugins: %v", err)
	}
}
