// Package config provides configuration types for the llmproxy server.
package config

import sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"

// Keep SDK types aligned with public SDK config to avoid split-type regressions.
type SDKConfig = sdkconfig.SDKConfig
type StreamingConfig = sdkconfig.StreamingConfig
