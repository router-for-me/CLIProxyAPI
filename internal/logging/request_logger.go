// Package logging provides request logging functionality for the CLI Proxy API server.
// It handles capturing and storing detailed HTTP request and response data when enabled
// through configuration, supporting both regular and streaming responses.
package logging

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
)

const (
	WebsocketTimelineSourceContextKey    = "WEBSOCKET_TIMELINE_SOURCE"
	APIRequestSourceContextKey           = "API_REQUEST_SOURCE"
	DeferredAPIRequestContextKey         = "DEFERRED_API_REQUEST"
	APIResponseSourceContextKey          = "API_RESPONSE_SOURCE"
	APIResponseCapturedContextKey        = "API_RESPONSE_CAPTURED"
	APIWebsocketTimelineSourceContextKey = "API_WEBSOCKET_TIMELINE_SOURCE"
)

// DeferredAPIRequest builds an upstream request log only when an error log needs it.
type DeferredAPIRequest func() []byte

// RequestLogger defines the interface for logging HTTP requests and responses.
// It provides methods for logging both regular and streaming HTTP request/response cycles.
type RequestLogger interface {
	// LogRequest logs a complete non-streaming request/response cycle.
	//
	// Parameters:
	//   - url: The request URL
	//   - method: The HTTP method
	//   - requestHeaders: The request headers
	//   - body: The request body
	//   - statusCode: The response status code
	//   - responseHeaders: The response headers
	//   - response: The raw response data
	//   - websocketTimeline: Optional downstream websocket event timeline
	//   - apiRequest: The API request data
	//   - apiResponse: The API response data
	//   - apiWebsocketTimeline: Optional upstream websocket event timeline
	//   - requestID: Optional request ID for log file naming
	//   - requestTimestamp: When the request was received
	//   - apiResponseTimestamp: When the API response was received
	//
	// Returns:
	//   - error: An error if logging fails, nil otherwise
	LogRequest(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, websocketTimeline, apiRequest, apiResponse, apiWebsocketTimeline []byte, apiResponseErrors []*interfaces.ErrorMessage, requestID string, requestTimestamp, apiResponseTimestamp time.Time) error

	// LogStreamingRequest initiates logging for a streaming request and returns a writer for chunks.
	//
	// Parameters:
	//   - url: The request URL
	//   - method: The HTTP method
	//   - headers: The request headers
	//   - body: The request body
	//   - requestID: Optional request ID for log file naming
	//
	// Returns:
	//   - StreamingLogWriter: A writer for streaming response chunks
	//   - error: An error if logging initialization fails, nil otherwise
	LogStreamingRequest(url, method string, headers map[string][]string, body []byte, requestID string) (StreamingLogWriter, error)

	// IsEnabled returns whether request logging is currently enabled.
	//
	// Returns:
	//   - bool: True if logging is enabled, false otherwise
	IsEnabled() bool
}

// StreamingLogWriter handles real-time logging of streaming response chunks.
// It provides methods for writing streaming response data asynchronously.
type StreamingLogWriter interface {
	// WriteChunkAsync writes a response chunk asynchronously (non-blocking).
	//
	// Parameters:
	//   - chunk: The response chunk to write
	WriteChunkAsync(chunk []byte)

	// WriteStatus writes the response status and headers to the log.
	//
	// Parameters:
	//   - status: The response status code
	//   - headers: The response headers
	//
	// Returns:
	//   - error: An error if writing fails, nil otherwise
	WriteStatus(status int, headers map[string][]string) error

	// WriteAPIRequest writes the upstream API request details to the log.
	// This should be called before WriteStatus to maintain proper log ordering.
	//
	// Parameters:
	//   - apiRequest: The API request data (typically includes URL, headers, body sent upstream)
	//
	// Returns:
	//   - error: An error if writing fails, nil otherwise
	WriteAPIRequest(apiRequest []byte) error

	// WriteAPIResponse writes the upstream API response details to the log.
	// This should be called after the streaming response is complete.
	//
	// Parameters:
	//   - apiResponse: The API response data
	//
	// Returns:
	//   - error: An error if writing fails, nil otherwise
	WriteAPIResponse(apiResponse []byte) error

	// WriteAPIWebsocketTimeline writes the upstream websocket timeline to the log.
	// This should be called when upstream communication happened over websocket.
	//
	// Parameters:
	//   - apiWebsocketTimeline: The upstream websocket event timeline
	//
	// Returns:
	//   - error: An error if writing fails, nil otherwise
	WriteAPIWebsocketTimeline(apiWebsocketTimeline []byte) error

	// SetFirstChunkTimestamp sets the TTFB timestamp captured when first chunk was received.
	//
	// Parameters:
	//   - timestamp: The time when first response chunk was received
	SetFirstChunkTimestamp(timestamp time.Time)

	// Close finalizes the log file and cleans up resources.
	//
	// Returns:
	//   - error: An error if closing fails, nil otherwise
	Close() error
}

// FileRequestLogger implements RequestLogger using file-based storage.
// It provides file-based logging functionality for HTTP requests and responses.
type FileRequestLogger struct {
	// enabled indicates whether request logging is currently enabled.
	enabled bool

	// logsDir is the directory where log files are stored.
	logsDir string

	// errorLogsMaxFiles limits the number of error log files retained.
	errorLogsMaxFiles int

	homeEnabled bool

	// format stores the normalized log entry structure ("text" or "json").
	format atomic.Value
}

// NewFileRequestLogger creates a new file-based request logger.
//
// Parameters:
//   - enabled: Whether request logging should be enabled
//   - logsDir: The directory where log files should be stored (can be relative)
//   - configDir: The directory of the configuration file; when logsDir is
//     relative, it will be resolved relative to this directory
//   - errorLogsMaxFiles: Maximum number of error log files to retain (0 = no cleanup)
//
// Returns:
//   - *FileRequestLogger: A new file-based request logger instance
func NewFileRequestLogger(enabled bool, logsDir string, configDir string, errorLogsMaxFiles int) *FileRequestLogger {
	return NewFileRequestLoggerWithFormat(enabled, logsDir, configDir, errorLogsMaxFiles, "text")
}

// NewFileRequestLoggerWithFormat creates a file request logger with the selected output format.
func NewFileRequestLoggerWithFormat(enabled bool, logsDir string, configDir string, errorLogsMaxFiles int, format string) *FileRequestLogger {
	// Resolve logsDir relative to the configuration file directory when it's not absolute.
	if !filepath.IsAbs(logsDir) {
		// If configDir is provided, resolve logsDir relative to it.
		if configDir != "" {
			logsDir = filepath.Join(configDir, logsDir)
		}
	}
	logger := &FileRequestLogger{
		enabled:           enabled,
		logsDir:           logsDir,
		errorLogsMaxFiles: errorLogsMaxFiles,
		homeEnabled:       false,
	}
	logger.format.Store(normalizeRequestLogFormat(format))
	return logger
}

func normalizeRequestLogFormat(format string) string {
	if strings.EqualFold(strings.TrimSpace(format), "json") {
		return "json"
	}
	return "text"
}

// SetFormat updates the output format used for future request log entries.
func (l *FileRequestLogger) SetFormat(format string) {
	if l == nil {
		return
	}
	l.format.Store(normalizeRequestLogFormat(format))
}

func (l *FileRequestLogger) currentFormat() string {
	if l == nil {
		return "text"
	}
	if format, ok := l.format.Load().(string); ok {
		return format
	}
	return "text"
}

// RequestLogFormat returns the normalized format used for newly started requests.
func (l *FileRequestLogger) RequestLogFormat() string {
	return l.currentFormat()
}

// IsEnabled returns whether request logging is currently enabled.
//
// Returns:
//   - bool: True if logging is enabled, false otherwise
func (l *FileRequestLogger) IsEnabled() bool {
	return l.enabled
}

// SetEnabled updates the request logging enabled state.
// This method allows dynamic enabling/disabling of request logging.
//
// Parameters:
//   - enabled: Whether request logging should be enabled
func (l *FileRequestLogger) SetEnabled(enabled bool) {
	l.enabled = enabled
}

// SetErrorLogsMaxFiles updates the maximum number of error log files to retain.
func (l *FileRequestLogger) SetErrorLogsMaxFiles(maxFiles int) {
	l.errorLogsMaxFiles = maxFiles
}

// NewFileBodySource creates a temp-backed source under the request log directory.
func (l *FileRequestLogger) NewFileBodySource(prefix string) (*FileBodySource, error) {
	return l.NewFileBodySourceWithFormat(prefix, l.currentFormat())
}

// NewFileBodySourceWithFormat creates a source using a per-request format snapshot.
func (l *FileRequestLogger) NewFileBodySourceWithFormat(prefix, format string) (*FileBodySource, error) {
	if l == nil {
		return nil, fmt.Errorf("file request logger is nil")
	}
	if errEnsure := l.ensureLogsDir(); errEnsure != nil {
		return nil, errEnsure
	}
	format = normalizeRequestLogFormat(format)
	if format == "json" && jsonFileBodySourceLimited(prefix) {
		source, err := newLimitedFileBodySourceInDir(l.logsDir, prefix, maxJSONFileBackedSectionBytes)
		if source != nil {
			source.format = format
		}
		return source, err
	}
	source, err := NewFileBodySourceInDir(l.logsDir, prefix)
	if source != nil {
		source.format = format
	}
	return source, err
}

func jsonFileBodySourceLimited(prefix string) bool {
	switch strings.ToLower(strings.TrimSpace(prefix)) {
	case "api-request", "api-response", "websocket-timeline", "api-websocket-timeline":
		return true
	default:
		return false
	}
}
