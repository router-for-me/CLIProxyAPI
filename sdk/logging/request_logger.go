// Package logging re-exports request logging primitives for SDK consumers.
package logging

import internallogging "github.com/router-for-me/CLIProxyAPI/v6/internal/logging"

// RequestLogger defines the interface for logging HTTP requests and responses.
type RequestLogger = internallogging.RequestLogger

// StreamingLogWriter handles real-time logging of streaming response chunks.
type StreamingLogWriter = internallogging.StreamingLogWriter

// FileRequestLogger implements RequestLogger using file-based storage.
type FileRequestLogger = internallogging.FileRequestLogger

// NewFileRequestLogger creates a new file-based request logger.
func NewFileRequestLogger(enabled bool, logsDir string, configDir string) *FileRequestLogger {
	return internallogging.NewFileRequestLogger(enabled, logsDir, configDir)
}
