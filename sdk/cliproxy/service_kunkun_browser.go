package cliproxy

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
)

// applyKunKunBrowser installs or clears the KunKun fingerprint-browser
// launcher used by OAuth authorization URL openings.
func (s *Service) applyKunKunBrowser(cfg *config.Config) {
	if cfg == nil || !cfg.KunKunBrowser.Enabled {
		browser.ClearLauncher()
		return
	}

	driverCfg := cfg.KunKunBrowser
	driverCfg.Normalize()
	if strings.TrimSpace(driverCfg.ProjectRoot) == "" {
		log.Warn("kunkun-browser is enabled without project-root; OAuth logins will use the system browser")
		browser.ClearLauncher()
		return
	}

	browser.SetLauncher(browser.NewKunKunDriver(driverCfg))
	log.WithFields(log.Fields{
		"driver_mode":  driverCfg.DriverMode,
		"project_root": driverCfg.ProjectRoot,
		"headless":     driverCfg.Headless,
		"profiles_dir": driverCfg.ProfilesDir,
	}).Info("KunKun fingerprint browser enabled for OAuth logins")
}

// closeKunKunBrowser removes the launcher during shutdown.
func (s *Service) closeKunKunBrowser() {
	browser.ClearLauncher()
}
