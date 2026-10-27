package config

import (
	"fmt"
	"os"
	"strings"
)

const (
	// KunKunDriverModeCLI launches KunKun through a child process.
	KunKunDriverModeCLI = "cli"

	// KunKunDriverModeService is reserved for a persistent KunKun service.
	// It is accepted by the configuration but not implemented yet.
	KunKunDriverModeService = "service"

	// DefaultKunKunDriverMode is used when driver-mode is omitted.
	DefaultKunKunDriverMode = KunKunDriverModeCLI

	// DefaultKunKunPythonBin is the default interpreter command.
	DefaultKunKunPythonBin = "uv"

	// DefaultKunKunProfilesDir stores per-profile browser data.
	DefaultKunKunProfilesDir = "./auths/kunkun_profiles"

	// DefaultKunKunDeviceMode is the fallback device mode.
	DefaultKunKunDeviceMode = "browser"

	// DefaultKunKunLaunchTimeoutSeconds bounds credential acquisition.
	DefaultKunKunLaunchTimeoutSeconds = 300
)

// NormalizeKunKunDriverMode returns the canonical driver mode.
// An empty value defaults to "cli".
func NormalizeKunKunDriverMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", KunKunDriverModeCLI:
		return KunKunDriverModeCLI
	case KunKunDriverModeService:
		return KunKunDriverModeService
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

// NormalizeKunKunDeviceMode returns the canonical device mode.
// An empty value defaults to "browser".
func NormalizeKunKunDeviceMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "browser":
		return "browser"
	case "wechat":
		return "wechat"
	case "qq":
		return "qq"
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

// Normalize applies defaults in place so callers can rely on non-empty values.
func (cfg *KunKunBrowserConfig) Normalize() {
	if cfg == nil {
		return
	}
	cfg.DriverMode = NormalizeKunKunDriverMode(cfg.DriverMode)
	cfg.ProjectRoot = strings.TrimSpace(cfg.ProjectRoot)
	if cfg.PythonBin = strings.TrimSpace(cfg.PythonBin); cfg.PythonBin == "" {
		cfg.PythonBin = DefaultKunKunPythonBin
	}
	if cfg.ProfilesDir = strings.TrimSpace(cfg.ProfilesDir); cfg.ProfilesDir == "" {
		cfg.ProfilesDir = DefaultKunKunProfilesDir
	}
	cfg.DefaultDeviceMode = NormalizeKunKunDeviceMode(cfg.DefaultDeviceMode)
	if cfg.LaunchTimeoutSeconds == 0 {
		cfg.LaunchTimeoutSeconds = DefaultKunKunLaunchTimeoutSeconds
	}
}

// NormalizeKunKunBrowser applies KunKun defaults to the full configuration.
func (cfg *Config) NormalizeKunKunBrowser() {
	if cfg == nil {
		return
	}
	cfg.KunKunBrowser.Normalize()
}

// ValidateKunKunBrowserConfig checks the KunKun bridge configuration.
// Project-root existence and positive timeouts are enforced only when enabled,
// because a disabled block must never block startup.
func ValidateKunKunBrowserConfig(cfg KunKunBrowserConfig) error {
	cfg.Normalize()
	if !cfg.Enabled {
		return nil
	}

	switch cfg.DriverMode {
	case KunKunDriverModeCLI, KunKunDriverModeService:
	default:
		return fmt.Errorf("kunkun-browser: unsupported driver-mode %q", cfg.DriverMode)
	}

	switch cfg.DefaultDeviceMode {
	case "browser", "wechat", "qq":
	default:
		return fmt.Errorf("kunkun-browser: unsupported default-device-mode %q", cfg.DefaultDeviceMode)
	}

	if cfg.ProjectRoot == "" {
		return fmt.Errorf("kunkun-browser: project-root is required when enabled")
	}
	info, errStat := os.Stat(cfg.ProjectRoot)
	if errStat != nil {
		return fmt.Errorf("kunkun-browser: project-root %q is not accessible: %w", cfg.ProjectRoot, errStat)
	}
	if !info.IsDir() {
		return fmt.Errorf("kunkun-browser: project-root %q is not a directory", cfg.ProjectRoot)
	}
	if cfg.LaunchTimeoutSeconds <= 0 {
		return fmt.Errorf("kunkun-browser: launch-timeout-seconds must be positive")
	}
	return nil
}
