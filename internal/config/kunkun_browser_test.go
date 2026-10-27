package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateKunKunBrowserConfig(t *testing.T) {
	projectRoot := t.TempDir()
	filePath := filepath.Join(projectRoot, "main.py")
	if errWrite := os.WriteFile(filePath, []byte("print('ok')\n"), 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}

	tests := []struct {
		name    string
		cfg     KunKunBrowserConfig
		wantErr string
	}{
		{
			name: "disabled ignores missing project root",
			cfg:  KunKunBrowserConfig{Enabled: false, DriverMode: "unsupported"},
		},
		{
			name: "enabled valid cli",
			cfg: KunKunBrowserConfig{
				Enabled:     true,
				DriverMode:  KunKunDriverModeCLI,
				ProjectRoot: projectRoot,
			},
		},
		{
			name: "enabled valid service structure",
			cfg: KunKunBrowserConfig{
				Enabled:     true,
				DriverMode:  KunKunDriverModeService,
				ProjectRoot: projectRoot,
			},
		},
		{
			name: "enabled missing project root",
			cfg: KunKunBrowserConfig{
				Enabled:    true,
				DriverMode: KunKunDriverModeCLI,
			},
			wantErr: "project-root is required",
		},
		{
			name: "enabled project root is a file",
			cfg: KunKunBrowserConfig{
				Enabled:     true,
				DriverMode:  KunKunDriverModeCLI,
				ProjectRoot: filePath,
			},
			wantErr: "not a directory",
		},
		{
			name: "enabled unsupported driver mode",
			cfg: KunKunBrowserConfig{
				Enabled:     true,
				DriverMode:  "http",
				ProjectRoot: projectRoot,
			},
			wantErr: "unsupported driver-mode",
		},
		{
			name: "enabled unsupported device mode",
			cfg: KunKunBrowserConfig{
				Enabled:           true,
				DriverMode:        KunKunDriverModeCLI,
				ProjectRoot:       projectRoot,
				DefaultDeviceMode: "mobile",
			},
			wantErr: "unsupported default-device-mode",
		},
		{
			name: "enabled negative timeout",
			cfg: KunKunBrowserConfig{
				Enabled:              true,
				DriverMode:           KunKunDriverModeCLI,
				ProjectRoot:          projectRoot,
				LaunchTimeoutSeconds: -1,
			},
			wantErr: "launch-timeout-seconds must be positive",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errValidate := ValidateKunKunBrowserConfig(tc.cfg)
			if tc.wantErr == "" {
				if errValidate != nil {
					t.Fatalf("ValidateKunKunBrowserConfig() error = %v, want nil", errValidate)
				}
				return
			}
			if errValidate == nil || !strings.Contains(errValidate.Error(), tc.wantErr) {
				t.Fatalf("ValidateKunKunBrowserConfig() error = %v, want containing %q", errValidate, tc.wantErr)
			}
		})
	}
}

func TestParseConfigBytesKunKunBrowserDefaults(t *testing.T) {
	projectRoot := t.TempDir()
	payload := "kunkun-browser:\n  enabled: true\n  project-root: " + projectRoot + "\n"

	cfg, errParse := ParseConfigBytes([]byte(payload))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	kunkun := cfg.KunKunBrowser
	if !kunkun.Enabled {
		t.Fatal("KunKunBrowser.Enabled = false, want true")
	}
	if kunkun.DriverMode != KunKunDriverModeCLI {
		t.Fatalf("DriverMode = %q, want %q", kunkun.DriverMode, KunKunDriverModeCLI)
	}
	if kunkun.PythonBin != DefaultKunKunPythonBin {
		t.Fatalf("PythonBin = %q, want %q", kunkun.PythonBin, DefaultKunKunPythonBin)
	}
	if kunkun.ProfilesDir != DefaultKunKunProfilesDir {
		t.Fatalf("ProfilesDir = %q, want %q", kunkun.ProfilesDir, DefaultKunKunProfilesDir)
	}
	if kunkun.DefaultDeviceMode != DefaultKunKunDeviceMode {
		t.Fatalf("DefaultDeviceMode = %q, want %q", kunkun.DefaultDeviceMode, DefaultKunKunDeviceMode)
	}
	if kunkun.LaunchTimeoutSeconds != DefaultKunKunLaunchTimeoutSeconds {
		t.Fatalf("LaunchTimeoutSeconds = %d, want %d", kunkun.LaunchTimeoutSeconds, DefaultKunKunLaunchTimeoutSeconds)
	}
}

func TestParseConfigBytesKunKunBrowserRejectsMissingProjectRoot(t *testing.T) {
	payload := "kunkun-browser:\n  enabled: true\n"
	_, errParse := ParseConfigBytes([]byte(payload))
	if errParse == nil || !strings.Contains(errParse.Error(), "project-root is required") {
		t.Fatalf("ParseConfigBytes() error = %v, want project-root error", errParse)
	}
}

func TestLoadConfigOptionalKunKunBrowserDisabledSkipsValidation(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	payload := "kunkun-browser:\n  enabled: false\n  driver-mode: unsupported\n  project-root: /does/not/exist\n"
	if errWrite := os.WriteFile(configPath, []byte(payload), 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}

	cfg, errLoad := LoadConfigOptional(configPath, false)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	if cfg.KunKunBrowser.Enabled {
		t.Fatal("KunKunBrowser.Enabled = true, want false")
	}
	if cfg.KunKunBrowser.PythonBin != DefaultKunKunPythonBin {
		t.Fatalf("PythonBin = %q, want default when disabled", cfg.KunKunBrowser.PythonBin)
	}
}

func TestNormalizeKunKunBrowser(t *testing.T) {
	cfg := &Config{KunKunBrowser: KunKunBrowserConfig{
		DriverMode:           " CLIENT ",
		PythonBin:            " ",
		ProfilesDir:          "",
		DefaultDeviceMode:    "WEIXIN",
		LaunchTimeoutSeconds: -5,
	}}
	cfg.NormalizeKunKunBrowser()

	if cfg.KunKunBrowser.DriverMode != "client" {
		t.Fatalf("DriverMode = %q, want client", cfg.KunKunBrowser.DriverMode)
	}
	if cfg.KunKunBrowser.PythonBin != DefaultKunKunPythonBin {
		t.Fatalf("PythonBin = %q, want default", cfg.KunKunBrowser.PythonBin)
	}
	if cfg.KunKunBrowser.ProfilesDir != DefaultKunKunProfilesDir {
		t.Fatalf("ProfilesDir = %q, want default", cfg.KunKunBrowser.ProfilesDir)
	}
	if cfg.KunKunBrowser.DefaultDeviceMode != "weixin" {
		t.Fatalf("DefaultDeviceMode = %q, want normalized weixin", cfg.KunKunBrowser.DefaultDeviceMode)
	}
	if cfg.KunKunBrowser.LaunchTimeoutSeconds != -5 {
		t.Fatalf("LaunchTimeoutSeconds = %d, want -5 preserved for validation", cfg.KunKunBrowser.LaunchTimeoutSeconds)
	}
}
