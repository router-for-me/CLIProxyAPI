// Package cliproxy provides the core service implementation for the CLI Proxy API.
package cliproxy

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// EmbedConfig provides a public API for configuring CLIProxyAPI when embedding
// it in external Go applications. Unlike the internal config.Config type,
// EmbedConfig contains no internal package dependencies and can be safely
// imported and used by external projects.
//
// Use EmbedConfig with the Builder pattern for embedding:
//
//	svc, err := cliproxy.NewBuilder().
//	    WithEmbedConfig(&cliproxy.EmbedConfig{
//	        Host:    "127.0.0.1",
//	        Port:    8317,
//	        AuthDir: "./auth",
//	    }).
//	    WithConfigPath("./config.yaml").
//	    Build()
//
// EmbedConfig covers essential server configuration options. For provider-specific
// configurations (API keys, OAuth accounts, etc.), use WithConfigPath() to load
// provider settings from a YAML file.
type EmbedConfig struct {
	// Host is the network host/interface on which the API server will bind.
	// Default is empty ("") to bind all interfaces (IPv4 + IPv6).
	// Use "127.0.0.1" or "localhost" for local-only access.
	Host string

	// Port is the network port on which the API server will listen.
	// Valid range: 1-65535. When UnixSocket is set and Port is 0, the server
	// runs in Unix socket-only mode.
	Port int

	// UnixSocket specifies the path for a Unix domain socket.
	// When set, the server listens on this socket for local IPC.
	// If both UnixSocket and Port are set, the server runs in dual mode
	// (listening on both Unix socket AND TCP).
	// If only UnixSocket is set (Port is 0), the server runs socket-only.
	// Unix sockets are only supported on Linux and macOS; on Windows,
	// the server falls back to TCP-only mode with a warning.
	// Default: empty (TCP only, no Unix socket).
	// Example: "./auth/cliproxy.sock"
	UnixSocket string

	// AuthDir is the directory where authentication token files are stored.
	// Default is "./auth" if not specified.
	AuthDir string

	// Debug enables or disables debug-level logging and other debug features.
	// Default is false.
	Debug bool

	// LoggingToFile controls whether application logs are written to rotating files or stdout.
	// When true, logs are written to files with automatic rotation.
	// Default is false (logs to stdout).
	LoggingToFile bool

	// UsageStatisticsEnabled toggles in-memory usage aggregation.
	// When false, usage data is discarded.
	// Default is false.
	UsageStatisticsEnabled bool

	// DisableCooling disables quota cooldown scheduling when true.
	// Default is false.
	DisableCooling bool

	// RequestRetry defines the number of retry attempts when a request fails.
	// Default is 3 if not specified or if set to 0.
	RequestRetry int

	// MaxRetryInterval defines the maximum wait time in seconds before retrying
	// a cooled-down credential. Default is 300 seconds (5 minutes) if not specified or if set to 0.
	MaxRetryInterval int

	// TLS configures HTTPS server settings.
	// When TLS.Enable is true, both Cert and Key must be provided.
	TLS TLSConfig

	// RemoteManagement controls management API configuration.
	// Management API is localhost-only by default unless AllowRemote is true.
	RemoteManagement RemoteManagement

	// QuotaExceeded defines the behavior when API quota limits are exceeded.
	QuotaExceeded QuotaExceeded
}

// TLSConfig holds HTTPS server settings for enabling secure connections.
type TLSConfig struct {
	// Enable toggles HTTPS server mode.
	// When true, both Cert and Key must be provided.
	// Default is false (HTTP mode).
	Enable bool

	// Cert is the path to the TLS certificate file.
	// Required when Enable is true.
	Cert string

	// Key is the path to the TLS private key file.
	// Required when Enable is true.
	Key string
}

// RemoteManagement holds management API configuration options.
// The management API provides endpoints for configuration, logs, usage stats,
// and authentication file management.
type RemoteManagement struct {
	// AllowRemote toggles remote (non-localhost) access to management API.
	// When false (default), management endpoints are only accessible from localhost (127.0.0.1, ::1).
	// When true, management endpoints are accessible from any network interface.
	// Default is false for security.
	AllowRemote bool

	// SecretKey is the authentication key for accessing management API endpoints.
	// This key will be automatically hashed using bcrypt before storage.
	// Required when AllowRemote is true or when management API access is needed.
	SecretKey string

	// DisableControlPanel skips serving and syncing the bundled management UI when true.
	// The management UI provides a web-based interface for managing the proxy.
	// Default is false (UI is enabled).
	DisableControlPanel bool
}

// QuotaExceeded defines the behavior when API quota limits are exceeded.
// These options enable automatic failover mechanisms to maintain service availability.
type QuotaExceeded struct {
	// SwitchProject indicates whether to automatically switch to another project
	// when a quota is exceeded. This applies to provider accounts that support
	// multiple projects (e.g., Google Vertex AI).
	// Default is false.
	SwitchProject bool

	// SwitchPreviewModel indicates whether to automatically switch to a preview/beta model
	// when a quota is exceeded on the production model.
	// Default is false.
	SwitchPreviewModel bool
}

// Validate checks the configuration for correctness and returns an error if any
// required fields are missing or invalid values are detected.
//
// Validation rules:
//   - Port must be in range 0-65535 (0 is valid when UnixSocket is set)
//   - UnixSocket: If set, path must not be a directory
//   - At least one of Port or UnixSocket must be configured
//   - TLS: If enabled, both Cert and Key must be provided and files must exist
//   - RemoteManagement: If AllowRemote is true, SecretKey must be provided
//
// Returns:
//   - error: Validation error if configuration is invalid, nil otherwise
func (c *EmbedConfig) Validate() error {
	if c == nil {
		return fmt.Errorf("config cannot be nil")
	}

	// Validate Port (0 is valid when UnixSocket is set)
	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf("port must be in range 0-65535, got %d", c.Port)
	}

	// Validate UnixSocket path if provided
	if c.UnixSocket != "" {
		// Check if path is a directory (invalid)
		if info, err := os.Stat(c.UnixSocket); err == nil && info.IsDir() {
			return fmt.Errorf("unix socket path cannot be a directory: %s", c.UnixSocket)
		}
		// Validate path has a filename component
		if filepath.Base(c.UnixSocket) == "." || filepath.Base(c.UnixSocket) == ".." {
			return fmt.Errorf("unix socket path must include a filename: %s", c.UnixSocket)
		}
	}

	// At least one listener must be configured
	if c.Port == 0 && c.UnixSocket == "" {
		return fmt.Errorf("at least one of Port or UnixSocket must be configured")
	}

	// Warn about Windows incompatibility (validation passes, but warn at runtime)
	// This is handled at server startup, not validation

	// Validate TLS configuration
	if c.TLS.Enable {
		if c.TLS.Cert == "" {
			return fmt.Errorf("TLS is enabled but cert path is empty")
		}
		if c.TLS.Key == "" {
			return fmt.Errorf("TLS is enabled but key path is empty")
		}
		// Check if cert file exists
		if _, err := os.Stat(c.TLS.Cert); err != nil {
			return fmt.Errorf("TLS cert file not found: %w", err)
		}
		// Check if key file exists
		if _, err := os.Stat(c.TLS.Key); err != nil {
			return fmt.Errorf("TLS key file not found: %w", err)
		}
	}

	// Validate RemoteManagement configuration
	if c.RemoteManagement.AllowRemote {
		if strings.TrimSpace(c.RemoteManagement.SecretKey) == "" {
			return fmt.Errorf("remote management is enabled but secret key is empty")
		}
	}

	return nil
}

// convertToInternalConfig converts the public EmbedConfig to the internal config.Config type.
// This function applies default values for fields that are not set and performs the necessary
// type mapping to create a valid internal configuration.
//
// Default values applied:
//   - AuthDir: "./auth" if not specified
//   - RequestRetry: 3 if not specified or 0
//   - MaxRetryInterval: 300 seconds if not specified or 0
//   - Host: "" (bind all interfaces) if not specified
//
// Returns:
//   - *config.Config: The internal configuration ready for service initialization
func convertToInternalConfig(embedCfg *EmbedConfig) *config.Config {
	if embedCfg == nil {
		return &config.Config{}
	}

	// Apply defaults
	authDir := embedCfg.AuthDir
	if authDir == "" {
		authDir = "./auth"
	}

	requestRetry := embedCfg.RequestRetry
	if requestRetry == 0 {
		requestRetry = 3
	}

	maxRetryInterval := embedCfg.MaxRetryInterval
	if maxRetryInterval == 0 {
		maxRetryInterval = 300
	}

	// Handle Unix socket on Windows (fall back to empty, handled at server startup)
	unixSocket := embedCfg.UnixSocket
	if runtime.GOOS == "windows" && unixSocket != "" {
		// Windows doesn't support Unix sockets; clear the path.
		// Server will log a warning and fall back to TCP-only.
		unixSocket = ""
	}

	// Create internal config with field mapping
	cfg := &config.Config{
		Host:                   embedCfg.Host,
		Port:                   embedCfg.Port,
		UnixSocket:             unixSocket,
		AuthDir:                authDir,
		Debug:                  embedCfg.Debug,
		LoggingToFile:          embedCfg.LoggingToFile,
		UsageStatisticsEnabled: embedCfg.UsageStatisticsEnabled,
		DisableCooling:         embedCfg.DisableCooling,
		RequestRetry:           requestRetry,
		MaxRetryInterval:       maxRetryInterval,
		TLS: config.TLSConfig{
			Enable: embedCfg.TLS.Enable,
			Cert:   embedCfg.TLS.Cert,
			Key:    embedCfg.TLS.Key,
		},
		RemoteManagement: config.RemoteManagement{
			AllowRemote:         embedCfg.RemoteManagement.AllowRemote,
			SecretKey:           embedCfg.RemoteManagement.SecretKey,
			DisableControlPanel: embedCfg.RemoteManagement.DisableControlPanel,
		},
		QuotaExceeded: config.QuotaExceeded{
			SwitchProject:      embedCfg.QuotaExceeded.SwitchProject,
			SwitchPreviewModel: embedCfg.QuotaExceeded.SwitchPreviewModel,
		},
	}

	return cfg
}
