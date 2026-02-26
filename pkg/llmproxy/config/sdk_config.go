// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing YAML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

import internalconfig "github.com/kooshapari/cliproxyapi-plusplus/v6/internal/config"

// SDKConfig is an alias to internal/config.SDKConfig.
type SDKConfig = internalconfig.SDKConfig

// StreamingConfig is an alias to internal/config.StreamingConfig.
type StreamingConfig = internalconfig.StreamingConfig
