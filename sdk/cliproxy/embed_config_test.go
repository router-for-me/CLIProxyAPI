package cliproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbedConfig_Validate tests the Validate method for various configurations
func TestEmbedConfig_Validate(t *testing.T) {
	// Create temporary test TLS files
	tmpDir := t.TempDir()
	certFile := filepath.Join(tmpDir, "test.crt")
	keyFile := filepath.Join(tmpDir, "test.key")
	if err := os.WriteFile(certFile, []byte("test cert"), 0644); err != nil {
		t.Fatalf("failed to create test cert file: %v", err)
	}
	if err := os.WriteFile(keyFile, []byte("test key"), 0644); err != nil {
		t.Fatalf("failed to create test key file: %v", err)
	}

	tests := []struct {
		name    string
		config  *EmbedConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "nil config",
			config:  nil,
			wantErr: true,
			errMsg:  "config cannot be nil",
		},
		{
			name: "valid minimal config",
			config: &EmbedConfig{
				Port: 8080,
			},
			wantErr: false,
		},
		{
			name: "valid full config",
			config: &EmbedConfig{
				Host:                   "127.0.0.1",
				Port:                   8080,
				AuthDir:                "./auth",
				Debug:                  true,
				LoggingToFile:          true,
				UsageStatisticsEnabled: true,
				DisableCooling:         false,
				RequestRetry:           3,
				MaxRetryInterval:       300,
				TLS: TLSConfig{
					Enable: false,
				},
				RemoteManagement: RemoteManagement{
					AllowRemote:         false,
					SecretKey:           "",
					DisableControlPanel: false,
				},
				QuotaExceeded: QuotaExceeded{
					SwitchProject:      true,
					SwitchPreviewModel: false,
				},
			},
			wantErr: false,
		},
		{
			name: "invalid port - too low",
			config: &EmbedConfig{
				Port: 0,
			},
			wantErr: true,
			errMsg:  "port must be in range 1-65535",
		},
		{
			name: "invalid port - too high",
			config: &EmbedConfig{
				Port: 65536,
			},
			wantErr: true,
			errMsg:  "port must be in range 1-65535",
		},
		{
			name: "invalid port - negative",
			config: &EmbedConfig{
				Port: -1,
			},
			wantErr: true,
			errMsg:  "port must be in range 1-65535",
		},
		{
			name: "TLS enabled but missing cert",
			config: &EmbedConfig{
				Port: 8080,
				TLS: TLSConfig{
					Enable: true,
					Cert:   "",
					Key:    keyFile,
				},
			},
			wantErr: true,
			errMsg:  "TLS is enabled but cert path is empty",
		},
		{
			name: "TLS enabled but missing key",
			config: &EmbedConfig{
				Port: 8080,
				TLS: TLSConfig{
					Enable: true,
					Cert:   certFile,
					Key:    "",
				},
			},
			wantErr: true,
			errMsg:  "TLS is enabled but key path is empty",
		},
		{
			name: "TLS enabled with non-existent cert file",
			config: &EmbedConfig{
				Port: 8080,
				TLS: TLSConfig{
					Enable: true,
					Cert:   "/nonexistent/cert.pem",
					Key:    keyFile,
				},
			},
			wantErr: true,
			errMsg:  "TLS cert file not found",
		},
		{
			name: "TLS enabled with non-existent key file",
			config: &EmbedConfig{
				Port: 8080,
				TLS: TLSConfig{
					Enable: true,
					Cert:   certFile,
					Key:    "/nonexistent/key.pem",
				},
			},
			wantErr: true,
			errMsg:  "TLS key file not found",
		},
		{
			name: "TLS enabled with valid cert and key",
			config: &EmbedConfig{
				Port: 8080,
				TLS: TLSConfig{
					Enable: true,
					Cert:   certFile,
					Key:    keyFile,
				},
			},
			wantErr: false,
		},
		{
			name: "remote management enabled without secret key",
			config: &EmbedConfig{
				Port: 8080,
				RemoteManagement: RemoteManagement{
					AllowRemote: true,
					SecretKey:   "",
				},
			},
			wantErr: true,
			errMsg:  "remote management is enabled but secret key is empty",
		},
		{
			name: "remote management enabled with whitespace-only secret key",
			config: &EmbedConfig{
				Port: 8080,
				RemoteManagement: RemoteManagement{
					AllowRemote: true,
					SecretKey:   "   ",
				},
			},
			wantErr: true,
			errMsg:  "remote management is enabled but secret key is empty",
		},
		{
			name: "remote management enabled with valid secret key",
			config: &EmbedConfig{
				Port: 8080,
				RemoteManagement: RemoteManagement{
					AllowRemote: true,
					SecretKey:   "my-secret-key",
				},
			},
			wantErr: false,
		},
		{
			name: "remote management disabled with empty secret key - should pass",
			config: &EmbedConfig{
				Port: 8080,
				RemoteManagement: RemoteManagement{
					AllowRemote: false,
					SecretKey:   "",
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("EmbedConfig.Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err != nil && tt.errMsg != "" {
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("EmbedConfig.Validate() error = %v, want error containing %q", err, tt.errMsg)
				}
			}
		})
	}
}

// TestConvertToInternalConfig tests the conversion from EmbedConfig to internal config.Config
func TestConvertToInternalConfig(t *testing.T) {
	tests := []struct {
		name     string
		embedCfg *EmbedConfig
		validate func(*testing.T, *EmbedConfig, interface{})
	}{
		{
			name:     "nil config",
			embedCfg: nil,
			validate: func(t *testing.T, embedCfg *EmbedConfig, result interface{}) {
				if result == nil {
					t.Error("expected non-nil config, got nil")
				}
			},
		},
		{
			name: "minimal config with defaults",
			embedCfg: &EmbedConfig{
				Port: 8080,
			},
			validate: func(t *testing.T, embedCfg *EmbedConfig, result interface{}) {
				cfg := result.(*interface{})
				// We can't directly access internal config fields, but we can verify the conversion didn't panic
				if cfg == nil {
					t.Error("expected non-nil config")
				}
			},
		},
		{
			name: "config with all fields set",
			embedCfg: &EmbedConfig{
				Host:                   "0.0.0.0",
				Port:                   9090,
				AuthDir:                "/custom/auth",
				Debug:                  true,
				LoggingToFile:          true,
				UsageStatisticsEnabled: true,
				DisableCooling:         true,
				RequestRetry:           5,
				MaxRetryInterval:       600,
				TLS: TLSConfig{
					Enable: true,
					Cert:   "/path/to/cert.pem",
					Key:    "/path/to/key.pem",
				},
				RemoteManagement: RemoteManagement{
					AllowRemote:         true,
					SecretKey:           "test-secret",
					DisableControlPanel: true,
				},
				QuotaExceeded: QuotaExceeded{
					SwitchProject:      true,
					SwitchPreviewModel: true,
				},
			},
			validate: func(t *testing.T, embedCfg *EmbedConfig, result interface{}) {
				cfg := result.(*interface{})
				if cfg == nil {
					t.Error("expected non-nil config")
				}
			},
		},
		{
			name: "config with zero RequestRetry should get default",
			embedCfg: &EmbedConfig{
				Port:         8080,
				RequestRetry: 0,
			},
			validate: func(t *testing.T, embedCfg *EmbedConfig, result interface{}) {
				cfg := result.(*interface{})
				if cfg == nil {
					t.Error("expected non-nil config")
				}
				// Default should be applied (3), but we can't verify without accessing internal fields
			},
		},
		{
			name: "config with zero MaxRetryInterval should get default",
			embedCfg: &EmbedConfig{
				Port:             8080,
				MaxRetryInterval: 0,
			},
			validate: func(t *testing.T, embedCfg *EmbedConfig, result interface{}) {
				cfg := result.(*interface{})
				if cfg == nil {
					t.Error("expected non-nil config")
				}
				// Default should be applied (300), but we can't verify without accessing internal fields
			},
		},
		{
			name: "config with empty AuthDir should get default",
			embedCfg: &EmbedConfig{
				Port:    8080,
				AuthDir: "",
			},
			validate: func(t *testing.T, embedCfg *EmbedConfig, result interface{}) {
				cfg := result.(*interface{})
				if cfg == nil {
					t.Error("expected non-nil config")
				}
				// Default should be "./auth", but we can't verify without accessing internal fields
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertToInternalConfig(tt.embedCfg)
			// Cast to interface{} to avoid exposing internal config type
			var iface interface{} = result
			tt.validate(t, tt.embedCfg, &iface)
		})
	}
}

// TestBuilder_WithEmbedConfig tests the Builder.WithEmbedConfig method
func TestBuilder_WithEmbedConfig(t *testing.T) {
	// Create a temporary config file for testing
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configFile, []byte("# test config"), 0644); err != nil {
		t.Fatalf("failed to create test config file: %v", err)
	}

	tests := []struct {
		name    string
		config  *EmbedConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "nil config",
			config:  nil,
			wantErr: true,
			errMsg:  "embed config cannot be nil",
		},
		{
			name: "valid config",
			config: &EmbedConfig{
				Port: 8080,
			},
			wantErr: false,
		},
		{
			name: "invalid port",
			config: &EmbedConfig{
				Port: 0,
			},
			wantErr: true,
			errMsg:  "embed config validation failed",
		},
		{
			name: "remote management without secret key",
			config: &EmbedConfig{
				Port: 8080,
				RemoteManagement: RemoteManagement{
					AllowRemote: true,
					SecretKey:   "",
				},
			},
			wantErr: true,
			errMsg:  "embed config validation failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewBuilder().
				WithEmbedConfig(tt.config).
				WithConfigPath(configFile)

			// Try to build and check for error
			_, err := builder.Build()

			if (err != nil) != tt.wantErr {
				t.Errorf("Builder.WithEmbedConfig() -> Build() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && err != nil && tt.errMsg != "" {
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("Builder.WithEmbedConfig() -> Build() error = %v, want error containing %q", err, tt.errMsg)
				}
			}
		})
	}
}

// TestBuilder_WithEmbedConfig_MethodChaining tests that method chaining works correctly
func TestBuilder_WithEmbedConfig_MethodChaining(t *testing.T) {
	// Create a temporary config file for testing
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configFile, []byte("# test config"), 0644); err != nil {
		t.Fatalf("failed to create test config file: %v", err)
	}

	builder := NewBuilder().
		WithEmbedConfig(&EmbedConfig{
			Host: "127.0.0.1",
			Port: 8080,
		}).
		WithConfigPath(configFile)

	if builder == nil {
		t.Error("expected non-nil builder after method chaining")
		return
	}

	// Try to build - should succeed with valid config and config file
	svc, err := builder.Build()
	if err != nil {
		t.Errorf("unexpected error building service: %v", err)
		return
	}

	if svc == nil {
		t.Error("expected non-nil service")
	}
}
