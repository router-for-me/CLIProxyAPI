package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestApplyKunKunBrowserInstallsAndClearsLauncher(t *testing.T) {
	t.Cleanup(browser.ClearLauncher)
	browser.ClearLauncher()

	service := &Service{}
	service.applyKunKunBrowser(&config.Config{})
	if browser.HasLauncher() {
		t.Fatal("applyKunKunBrowser() installed a launcher for a disabled config")
	}

	service.applyKunKunBrowser(&config.Config{KunKunBrowser: config.KunKunBrowserConfig{
		Enabled:     true,
		ProjectRoot: t.TempDir(),
		PythonBin:   "uv",
	}})
	if !browser.HasLauncher() {
		t.Fatal("applyKunKunBrowser() did not install a launcher for an enabled config")
	}

	service.applyKunKunBrowser(&config.Config{})
	if browser.HasLauncher() {
		t.Fatal("applyKunKunBrowser() did not clear the launcher after disabling")
	}
}

func TestApplyKunKunBrowserRequiresProjectRoot(t *testing.T) {
	t.Cleanup(browser.ClearLauncher)
	browser.SetLauncher(browser.NewKunKunDriver(config.KunKunBrowserConfig{ProjectRoot: t.TempDir()}))

	service := &Service{}
	service.applyKunKunBrowser(&config.Config{KunKunBrowser: config.KunKunBrowserConfig{Enabled: true}})
	if browser.HasLauncher() {
		t.Fatal("applyKunKunBrowser() installed a launcher without project-root")
	}
}
