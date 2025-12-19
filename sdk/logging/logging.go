// Package logging provides SDK type aliases for request logging.
// This package re-exports types from internal/logging, allowing SDK consumers
// to use logging functionality without directly importing internal packages.
package logging

import (
	internallogging "github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
)

// RequestLogger is an interface for logging HTTP requests.
// This is a type alias to internal/logging.RequestLogger.
type RequestLogger = internallogging.RequestLogger

// NewFileRequestLogger creates a new file-based request logger.
// This delegates to internal/logging.NewFileRequestLogger.
//
// Parameters:
//   - enabled: Whether logging is enabled
//   - logsDir: Directory to write log files
//   - configDir: Configuration directory for relative path resolution
//
// Returns:
//   - *FileRequestLogger: A new file request logger instance
func NewFileRequestLogger(enabled bool, logsDir string, configDir string) *FileRequestLogger {
	return internallogging.NewFileRequestLogger(enabled, logsDir, configDir)
}

// FileRequestLogger is a type alias to internal/logging.FileRequestLogger.
type FileRequestLogger = internallogging.FileRequestLogger
