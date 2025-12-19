// Package configloader provides SDK functions for loading configuration.
// This package re-exports configuration loading functions from internal/config,
// allowing SDK consumers to load configuration without directly importing internal packages.
//
// Note: The Config type is defined in internal/config. This package provides
// convenient access to configuration loading while avoiding import cycle issues
// with sdk/config (which defines SDKConfig).
package configloader

import (
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// Config is a type alias to internal/config.Config.
// This is the full application configuration struct.
type Config = internalconfig.Config

// TLSConfig is a type alias to internal/config.TLSConfig.
type TLSConfig = internalconfig.TLSConfig

// RemoteManagement is a type alias to internal/config.RemoteManagement.
type RemoteManagement = internalconfig.RemoteManagement

// QuotaExceeded is a type alias to internal/config.QuotaExceeded.
type QuotaExceeded = internalconfig.QuotaExceeded

// GeminiKey is a type alias to internal/config.GeminiKey.
type GeminiKey = internalconfig.GeminiKey

// CodexKey is a type alias to internal/config.CodexKey.
type CodexKey = internalconfig.CodexKey

// ClaudeKey is a type alias to internal/config.ClaudeKey.
type ClaudeKey = internalconfig.ClaudeKey

// ClaudeModel is a type alias to internal/config.ClaudeModel.
type ClaudeModel = internalconfig.ClaudeModel

// OpenAICompatibility is a type alias to internal/config.OpenAICompatibility.
type OpenAICompatibility = internalconfig.OpenAICompatibility

// OpenAICompatibilityAPIKey is a type alias to internal/config.OpenAICompatibilityAPIKey.
type OpenAICompatibilityAPIKey = internalconfig.OpenAICompatibilityAPIKey

// OpenAICompatibilityModel is a type alias to internal/config.OpenAICompatibilityModel.
type OpenAICompatibilityModel = internalconfig.OpenAICompatibilityModel

// VertexCompatKey is a type alias to internal/config.VertexCompatKey.
type VertexCompatKey = internalconfig.VertexCompatKey

// AmpCode is a type alias to internal/config.AmpCode.
type AmpCode = internalconfig.AmpCode

// AmpModelMapping is a type alias to internal/config.AmpModelMapping.
type AmpModelMapping = internalconfig.AmpModelMapping

// PayloadConfig is a type alias to internal/config.PayloadConfig.
type PayloadConfig = internalconfig.PayloadConfig

// PayloadRule is a type alias to internal/config.PayloadRule.
type PayloadRule = internalconfig.PayloadRule

// PayloadModelRule is a type alias to internal/config.PayloadModelRule.
type PayloadModelRule = internalconfig.PayloadModelRule

// LoadConfig reads a YAML configuration file from the given path,
// unmarshals it into a Config struct, applies environment variable overrides,
// and returns it.
//
// Parameters:
//   - configFile: The path to the YAML configuration file
//
// Returns:
//   - *Config: The loaded configuration
//   - error: An error if the configuration could not be loaded
//
// Example:
//
//	cfg, err := configloader.LoadConfig("config.yaml")
//	if err != nil {
//	    log.Fatal(err)
//	}
var LoadConfig = internalconfig.LoadConfig

// LoadConfigOptional reads YAML from configFile.
// If optional is true and the file is missing, it returns an empty Config.
var LoadConfigOptional = internalconfig.LoadConfigOptional
