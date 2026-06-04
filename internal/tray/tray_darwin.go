//go:build darwin

package tray

import (
	"context"
	"fmt"
	"time"

	"github.com/atotto/clipboard"
	"github.com/getlantern/systray"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	log "github.com/sirupsen/logrus"
)

// Run starts the macOS menu bar app and blocks until the tray exits.
func Run(ctx context.Context, opts Options) error {
	if opts.Port <= 0 {
		return fmt.Errorf("tray: invalid port %d", opts.Port)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	app := &app{
		ctx:       ctx,
		opts:      opts,
		autoStart: newAutoStartManager(opts.AutoStart),
	}

	go func() {
		<-ctx.Done()
		systray.Quit()
	}()

	systray.Run(app.onReady, app.onExit)
	return nil
}

type app struct {
	ctx       context.Context
	opts      Options
	autoStart *autoStartManager
}

func (a *app) onReady() {
	if icon, err := trayIconPNG(a.opts.ManagementAssetPath); err == nil {
		systray.SetIcon(icon)
	} else {
		log.WithError(err).Warn("failed to generate tray icon")
		systray.SetTitle("CPA")
	}
	systray.SetTooltip("CLIProxyAPI")

	status := systray.AddMenuItem(fmt.Sprintf("Running on %s", a.opts.baseURL()), "")
	status.Disable()

	openPanel := systray.AddMenuItem("Open Management Panel", "Open the local CLIProxyAPI management panel")
	copyBaseURL := systray.AddMenuItem("Copy API Base URL", "Copy the local API base URL")
	copyManagementKey := systray.AddMenuItem("Copy Management Key", "Copy the local management key")
	if a.opts.managementPassword() == "" {
		copyManagementKey.Disable()
	}
	autoStart := systray.AddMenuItem("Enable Auto-start", "Run CLIProxyAPI on login")
	a.updateAutoStartMenu(autoStart)
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit CLIProxyAPI", "Stop CLIProxyAPI and close the tray")

	go a.handleMenu(openPanel, copyBaseURL, copyManagementKey, autoStart, quit)
}

func (a *app) onExit() {}

func (a *app) handleMenu(openPanel, copyBaseURL, copyManagementKey, autoStart, quit *systray.MenuItem) {
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-openPanel.ClickedCh:
			if err := browser.OpenURL(a.opts.managementURL()); err != nil {
				log.WithError(err).Warn("failed to open management panel from tray")
			}
		case <-copyBaseURL.ClickedCh:
			copyMenuValue(copyBaseURL, "Copy API Base URL", a.opts.baseURL())
		case <-copyManagementKey.ClickedCh:
			if password := a.opts.managementPassword(); password != "" {
				copyMenuValue(copyManagementKey, "Copy Management Key", password)
			}
		case <-autoStart.ClickedCh:
			a.toggleAutoStart(autoStart)
		case <-quit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

func (a *app) updateAutoStartMenu(item *systray.MenuItem) {
	if a.autoStart == nil || !a.autoStart.available() {
		item.SetTitle("Auto-start Unavailable")
		item.Uncheck()
		item.Disable()
		return
	}

	item.Enable()
	if a.autoStart.enabled() {
		item.SetTitle("Auto-start Enabled")
		item.Check()
	} else {
		item.SetTitle("Enable Auto-start")
		item.Uncheck()
	}
}

func (a *app) toggleAutoStart(item *systray.MenuItem) {
	if a.autoStart == nil {
		return
	}

	item.Disable()
	var err error
	if a.autoStart.enabled() {
		err = a.autoStart.disable()
	} else {
		err = a.autoStart.enable()
	}
	if err != nil {
		log.WithError(err).Warn("failed to toggle tray auto-start")
		item.SetTitle("Auto-start Failed")
		item.Uncheck()
		go func() {
			time.Sleep(1200 * time.Millisecond)
			a.updateAutoStartMenu(item)
		}()
		return
	}
	a.updateAutoStartMenu(item)
}

func copyMenuValue(item *systray.MenuItem, originalTitle string, value string) {
	if err := clipboard.WriteAll(value); err != nil {
		log.WithError(err).Warn("failed to copy tray value")
		item.SetTitle("Copy Failed")
	} else {
		item.SetTitle("Copied")
	}

	go func() {
		time.Sleep(1200 * time.Millisecond)
		item.SetTitle(originalTitle)
	}()
}
