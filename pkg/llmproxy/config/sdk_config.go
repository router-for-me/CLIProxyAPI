// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing YAML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

import (
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

// SDKConfig is an alias to the SDK's SDKConfig, ensuring type compatibility
// across pkg/llmproxy/config and sdk/config.
type SDKConfig = sdkconfig.SDKConfig

// StreamingConfig is an alias to the SDK's StreamingConfig, ensuring type compatibility.
type StreamingConfig = sdkconfig.StreamingConfig
