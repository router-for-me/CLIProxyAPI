// Package tray provides the platform system tray integration for CLIProxyAPI.
package tray

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUnsupported is returned when tray mode is not implemented for the current platform.
var ErrUnsupported = errors.New("system tray is not supported on this platform")

// Options configures the tray application.
type Options struct {
	Port                int
	ManagementPassword  string
	ManagementAssetPath string
	AutoStart           AutoStartOptions
}

// AutoStartOptions configures the macOS launch-at-login integration.
type AutoStartOptions struct {
	ExecutablePath string
	ConfigPath     string
	LocalModel     bool
	HomeAddr       string
	HomePassword   string
}

func (o Options) baseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", o.Port)
}

func (o Options) managementURL() string {
	return o.baseURL() + "/management.html"
}

func (o Options) managementPassword() string {
	return strings.TrimSpace(o.ManagementPassword)
}
