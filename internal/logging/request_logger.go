// Package logging provides request logging functionality for the CLI Proxy API server.
// It handles capturing and storing detailed HTTP request and response data when enabled
// through configuration, supporting both regular and streaming responses.
package logging

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

var requestLogID atomic.Uint64

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
	//   - apiRequest: The API request data
	//   - apiResponse: The API response data
	//   - requestID: Optional request ID for log file naming
	//   - requestTimestamp: When the request was received
	//   - apiResponseTimestamp: When the API response was received
	//
	// Returns:
	//   - error: An error if logging fails, nil otherwise
	LogRequest(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, apiRequest, apiResponse []byte, apiResponseErrors []*interfaces.ErrorMessage, requestID string, requestTimestamp, apiResponseTimestamp time.Time) error

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

	// configDir is the directory where logging.ini is stored.
	configDir string

	// errorLogsMaxFiles limits the number of error log files retained.
	errorLogsMaxFiles int

	writeMu sync.Mutex

	retentionMu     sync.Mutex
	requestPolicy   RequestLogPolicy
	retentionCancel context.CancelFunc
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
	logger := &FileRequestLogger{
		enabled:           enabled,
		logsDir:           resolveRequestLogsDir(logsDir, configDir),
		configDir:         configDir,
		errorLogsMaxFiles: errorLogsMaxFiles,
	}
	logger.reloadRequestPolicy()
	return logger
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

// LogRequest logs a complete non-streaming request/response cycle to a file.
//
// Parameters:
//   - url: The request URL
//   - method: The HTTP method
//   - requestHeaders: The request headers
//   - body: The request body
//   - statusCode: The response status code
//   - responseHeaders: The response headers
//   - response: The raw response data
//   - apiRequest: The API request data
//   - apiResponse: The API response data
//   - requestID: Optional request ID for log file naming
//   - requestTimestamp: When the request was received
//   - apiResponseTimestamp: When the API response was received
//
// Returns:
//   - error: An error if logging fails, nil otherwise
func (l *FileRequestLogger) LogRequest(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, apiRequest, apiResponse []byte, apiResponseErrors []*interfaces.ErrorMessage, requestID string, requestTimestamp, apiResponseTimestamp time.Time) error {
	return l.logRequest(url, method, requestHeaders, body, statusCode, responseHeaders, response, apiRequest, apiResponse, apiResponseErrors, false, requestID, requestTimestamp, apiResponseTimestamp)
}

// LogRequestWithOptions logs a request with optional forced logging behavior.
// The force flag allows writing error logs even when regular request logging is disabled.
func (l *FileRequestLogger) LogRequestWithOptions(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, apiRequest, apiResponse []byte, apiResponseErrors []*interfaces.ErrorMessage, force bool, requestID string, requestTimestamp, apiResponseTimestamp time.Time) error {
	return l.logRequest(url, method, requestHeaders, body, statusCode, responseHeaders, response, apiRequest, apiResponse, apiResponseErrors, force, requestID, requestTimestamp, apiResponseTimestamp)
}

func (l *FileRequestLogger) logRequest(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, apiRequest, apiResponse []byte, apiResponseErrors []*interfaces.ErrorMessage, force bool, requestID string, requestTimestamp, apiResponseTimestamp time.Time) error {
	if !l.enabled && !force {
		return nil
	}
	requestID = l.normalizeRequestID(requestID)
	if requestTimestamp.IsZero() {
		requestTimestamp = time.Now()
	}

	// Ensure logs directory exists
	if errEnsure := l.ensureLogsDir(); errEnsure != nil {
		return fmt.Errorf("failed to create logs directory: %w", errEnsure)
	}

	recordType := requestLogTypeNormal
	if force && !l.enabled {
		recordType = requestLogTypeError
	}

	responseToWrite, decompressErr := l.decompressResponse(responseHeaders, response)
	if decompressErr != nil {
		// If decompression fails, continue with original response and annotate the log output.
		responseToWrite = response
	}

	record, errRecord := newRequestRecordBuffer(requestRecordMeta{
		RequestID:  requestID,
		Timestamp:  requestTimestamp,
		URL:        url,
		Method:     method,
		StatusCode: statusCode,
		RecordType: recordType,
	}, func(w io.Writer) error {
		return l.writeNonStreamingLog(
			w,
			url,
			method,
			requestHeaders,
			body,
			"",
			apiRequest,
			apiResponse,
			apiResponseErrors,
			statusCode,
			responseHeaders,
			responseToWrite,
			decompressErr,
			requestTimestamp,
			apiResponseTimestamp,
		)
	})
	if errRecord != nil {
		return fmt.Errorf("failed to write log file: %w", errRecord)
	}

	if errAppend := l.appendAggregatedRecord(l.recordFilePath(recordType, requestTimestamp), record); errAppend != nil {
		return fmt.Errorf("failed to append request log file: %w", errAppend)
	}

	if force && !l.enabled {
		if errCleanup := l.cleanupOldErrorLogs(); errCleanup != nil {
			log.WithError(errCleanup).Warn("failed to clean up old error logs")
		}
	}

	return nil
}

// LogStreamingRequest initiates logging for a streaming request.
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
func (l *FileRequestLogger) LogStreamingRequest(url, method string, headers map[string][]string, body []byte, requestID string) (StreamingLogWriter, error) {
	if !l.enabled {
		return &NoOpStreamingLogWriter{}, nil
	}
	requestID = l.normalizeRequestID(requestID)

	// Ensure logs directory exists
	if err := l.ensureLogsDir(); err != nil {
		return nil, fmt.Errorf("failed to create logs directory: %w", err)
	}

	requestHeaders := make(map[string][]string, len(headers))
	for key, values := range headers {
		headerValues := make([]string, len(values))
		copy(headerValues, values)
		requestHeaders[key] = headerValues
	}

	// Create streaming writer
	writer := &FileStreamingLogWriter{
		logger:         l,
		recordType:     requestLogTypeNormal,
		requestID:      requestID,
		url:            url,
		method:         method,
		timestamp:      time.Now(),
		requestHeaders: requestHeaders,
	}

	return writer, nil
}

// ensureLogsDir creates the logs directory if it doesn't exist.
//
// Returns:
//   - error: An error if directory creation fails, nil otherwise
func (l *FileRequestLogger) ensureLogsDir() error {
	if _, err := os.Stat(l.logsDir); os.IsNotExist(err) {
		return os.MkdirAll(l.logsDir, 0755)
	}
	return nil
}

// generateFilename creates a sanitized filename from the URL path and current timestamp.
// Format: v1-responses-2025-12-23T195811-a1b2c3d4.log
//
// Parameters:
//   - url: The request URL
//   - requestID: Optional request ID to include in filename
//
// Returns:
//   - string: A sanitized filename for the log file
func (l *FileRequestLogger) generateFilename(url string, requestID ...string) string {
	// Extract path from URL
	path := url
	if strings.Contains(url, "?") {
		path = strings.Split(url, "?")[0]
	}

	// Remove leading slash
	if strings.HasPrefix(path, "/") {
		path = path[1:]
	}

	// Sanitize path for filename
	sanitized := l.sanitizeForFilename(path)

	// Add timestamp
	timestamp := time.Now().Format("2006-01-02T150405")

	// Use request ID if provided, otherwise use sequential ID
	var idPart string
	if len(requestID) > 0 && requestID[0] != "" {
		idPart = requestID[0]
	} else {
		id := requestLogID.Add(1)
		idPart = fmt.Sprintf("%d", id)
	}

	return fmt.Sprintf("%s-%s-%s.log", sanitized, timestamp, idPart)
}

// sanitizeForFilename replaces characters that are not safe for filenames.
//
// Parameters:
//   - path: The path to sanitize
//
// Returns:
//   - string: A sanitized filename
func (l *FileRequestLogger) sanitizeForFilename(path string) string {
	// Replace slashes with hyphens
	sanitized := strings.ReplaceAll(path, "/", "-")

	// Replace colons with hyphens
	sanitized = strings.ReplaceAll(sanitized, ":", "-")

	// Replace other problematic characters with hyphens
	reg := regexp.MustCompile(`[<>:"|?*\s]`)
	sanitized = reg.ReplaceAllString(sanitized, "-")

	// Remove multiple consecutive hyphens
	reg = regexp.MustCompile(`-+`)
	sanitized = reg.ReplaceAllString(sanitized, "-")

	// Remove leading/trailing hyphens
	sanitized = strings.Trim(sanitized, "-")

	// Handle empty result
	if sanitized == "" {
		sanitized = "root"
	}

	return sanitized
}

// cleanupOldErrorLogs keeps only the newest errorLogsMaxFiles forced error log files.
func (l *FileRequestLogger) cleanupOldErrorLogs() error {
	if l.errorLogsMaxFiles <= 0 {
		return nil
	}

	entries, errRead := os.ReadDir(l.logsDir)
	if errRead != nil {
		return errRead
	}

	type logFile struct {
		name    string
		modTime time.Time
	}

	var files []logFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "error-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		info, errInfo := entry.Info()
		if errInfo != nil {
			log.WithError(errInfo).Warn("failed to read error log info")
			continue
		}
		files = append(files, logFile{name: name, modTime: info.ModTime()})
	}

	if len(files) <= l.errorLogsMaxFiles {
		return nil
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.After(files[j].modTime)
	})

	for _, file := range files[l.errorLogsMaxFiles:] {
		if errRemove := os.Remove(filepath.Join(l.logsDir, file.name)); errRemove != nil {
			log.WithError(errRemove).Warnf("failed to remove old error log: %s", file.name)
		}
	}

	return nil
}

func (l *FileRequestLogger) writeNonStreamingLog(
	w io.Writer,
	url, method string,
	requestHeaders map[string][]string,
	_ []byte,
	_ string,
	apiRequest []byte,
	apiResponse []byte,
	apiResponseErrors []*interfaces.ErrorMessage,
	statusCode int,
	responseHeaders map[string][]string,
	_ []byte,
	decompressErr error,
	requestTimestamp time.Time,
	apiResponseTimestamp time.Time,
) error {
	if requestTimestamp.IsZero() {
		requestTimestamp = time.Now()
	}
	if errWrite := writeRequestInfoWithBody(w, url, method, requestHeaders, nil, "", requestTimestamp); errWrite != nil {
		return errWrite
	}
	if errWrite := writeAPISection(w, "=== API REQUEST ===\n", "=== API REQUEST", apiRequest, time.Time{}); errWrite != nil {
		return errWrite
	}
	if errWrite := writeAPIErrorResponses(w, apiResponseErrors); errWrite != nil {
		return errWrite
	}
	if errWrite := writeAPISection(w, "=== API RESPONSE ===\n", "=== API RESPONSE", apiResponse, apiResponseTimestamp); errWrite != nil {
		return errWrite
	}
	return writeResponseSection(w, statusCode, true, responseHeaders, nil, decompressErr, true)
}

func writeRequestInfoWithBody(
	w io.Writer,
	url, method string,
	headers map[string][]string,
	_ []byte,
	_ string,
	timestamp time.Time,
) error {
	if _, errWrite := io.WriteString(w, "=== REQUEST INFO ===\n"); errWrite != nil {
		return errWrite
	}
	if _, errWrite := io.WriteString(w, fmt.Sprintf("Version: %s\n", buildinfo.Version)); errWrite != nil {
		return errWrite
	}
	if _, errWrite := io.WriteString(w, fmt.Sprintf("URL: %s\n", url)); errWrite != nil {
		return errWrite
	}
	if _, errWrite := io.WriteString(w, fmt.Sprintf("Method: %s\n", method)); errWrite != nil {
		return errWrite
	}
	if _, errWrite := io.WriteString(w, fmt.Sprintf("Timestamp: %s\n", timestamp.Format(time.RFC3339Nano))); errWrite != nil {
		return errWrite
	}
	if _, errWrite := io.WriteString(w, "\n"); errWrite != nil {
		return errWrite
	}

	if _, errWrite := io.WriteString(w, "=== HEADERS ===\n"); errWrite != nil {
		return errWrite
	}
	for key, values := range headers {
		for _, value := range values {
			masked := util.MaskSensitiveHeaderValue(key, value)
			if _, errWrite := io.WriteString(w, fmt.Sprintf("%s: %s\n", key, masked)); errWrite != nil {
				return errWrite
			}
		}
	}
	if _, errWrite := io.WriteString(w, "\n"); errWrite != nil {
		return errWrite
	}
	if _, errWrite := io.WriteString(w, "Request Body: <filtered>\n\n"); errWrite != nil {
		return errWrite
	}
	return nil
}

func writeAPISection(w io.Writer, sectionHeader string, sectionPrefix string, payload []byte, timestamp time.Time) error {
	if len(payload) == 0 {
		return nil
	}

	if bytes.HasPrefix(payload, []byte(sectionPrefix)) {
		if _, errWrite := w.Write(payload); errWrite != nil {
			return errWrite
		}
		if !bytes.HasSuffix(payload, []byte("\n")) {
			if _, errWrite := io.WriteString(w, "\n"); errWrite != nil {
				return errWrite
			}
		}
	} else {
		if _, errWrite := io.WriteString(w, sectionHeader); errWrite != nil {
			return errWrite
		}
		if !timestamp.IsZero() {
			if _, errWrite := io.WriteString(w, fmt.Sprintf("Timestamp: %s\n", timestamp.Format(time.RFC3339Nano))); errWrite != nil {
				return errWrite
			}
		}
		if _, errWrite := w.Write(payload); errWrite != nil {
			return errWrite
		}
		if _, errWrite := io.WriteString(w, "\n"); errWrite != nil {
			return errWrite
		}
	}

	if _, errWrite := io.WriteString(w, "\n"); errWrite != nil {
		return errWrite
	}
	return nil
}

func writeAPIErrorResponses(w io.Writer, apiResponseErrors []*interfaces.ErrorMessage) error {
	for i := 0; i < len(apiResponseErrors); i++ {
		if apiResponseErrors[i] == nil {
			continue
		}
		if _, errWrite := io.WriteString(w, "=== API ERROR RESPONSE ===\n"); errWrite != nil {
			return errWrite
		}
		if _, errWrite := io.WriteString(w, fmt.Sprintf("HTTP Status: %d\n", apiResponseErrors[i].StatusCode)); errWrite != nil {
			return errWrite
		}
		if apiResponseErrors[i].Error != nil {
			if _, errWrite := io.WriteString(w, apiResponseErrors[i].Error.Error()); errWrite != nil {
				return errWrite
			}
		}
		if _, errWrite := io.WriteString(w, "\n\n"); errWrite != nil {
			return errWrite
		}
	}
	return nil
}

func writeResponseSection(w io.Writer, statusCode int, statusWritten bool, responseHeaders map[string][]string, _ io.Reader, decompressErr error, trailingNewline bool) error {
	if _, errWrite := io.WriteString(w, "=== RESPONSE ===\n"); errWrite != nil {
		return errWrite
	}
	if statusWritten {
		if _, errWrite := io.WriteString(w, fmt.Sprintf("Status: %d\n", statusCode)); errWrite != nil {
			return errWrite
		}
	}

	if responseHeaders != nil {
		for key, values := range responseHeaders {
			for _, value := range values {
				if _, errWrite := io.WriteString(w, fmt.Sprintf("%s: %s\n", key, value)); errWrite != nil {
					return errWrite
				}
			}
		}
	}

	if _, errWrite := io.WriteString(w, "\n"); errWrite != nil {
		return errWrite
	}
	if _, errWrite := io.WriteString(w, "Response Body: <filtered>"); errWrite != nil {
		return errWrite
	}
	if decompressErr != nil {
		if _, errWrite := io.WriteString(w, fmt.Sprintf("\n[DECOMPRESSION ERROR: %v]", decompressErr)); errWrite != nil {
			return errWrite
		}
	}

	if trailingNewline {
		if _, errWrite := io.WriteString(w, "\n"); errWrite != nil {
			return errWrite
		}
	}
	return nil
}

// formatLogContent creates the complete log content for non-streaming requests.
//
// Parameters:
//   - url: The request URL
//   - method: The HTTP method
//   - headers: The request headers
//   - body: The request body
//   - apiRequest: The API request data
//   - apiResponse: The API response data
//   - response: The raw response data
//   - status: The response status code
//   - responseHeaders: The response headers
//
// Returns:
//   - string: The formatted log content
func (l *FileRequestLogger) formatLogContent(url, method string, headers map[string][]string, body, apiRequest, apiResponse, _ []byte, status int, responseHeaders map[string][]string, apiResponseErrors []*interfaces.ErrorMessage) string {
	var content strings.Builder

	// Request info
	content.WriteString(l.formatRequestInfo(url, method, headers, body))

	if len(apiRequest) > 0 {
		if bytes.HasPrefix(apiRequest, []byte("=== API REQUEST")) {
			content.Write(apiRequest)
			if !bytes.HasSuffix(apiRequest, []byte("\n")) {
				content.WriteString("\n")
			}
		} else {
			content.WriteString("=== API REQUEST ===\n")
			content.Write(apiRequest)
			content.WriteString("\n")
		}
		content.WriteString("\n")
	}

	for i := 0; i < len(apiResponseErrors); i++ {
		content.WriteString("=== API ERROR RESPONSE ===\n")
		content.WriteString(fmt.Sprintf("HTTP Status: %d\n", apiResponseErrors[i].StatusCode))
		content.WriteString(apiResponseErrors[i].Error.Error())
		content.WriteString("\n\n")
	}

	if len(apiResponse) > 0 {
		if bytes.HasPrefix(apiResponse, []byte("=== API RESPONSE")) {
			content.Write(apiResponse)
			if !bytes.HasSuffix(apiResponse, []byte("\n")) {
				content.WriteString("\n")
			}
		} else {
			content.WriteString("=== API RESPONSE ===\n")
			content.Write(apiResponse)
			content.WriteString("\n")
		}
		content.WriteString("\n")
	}

	// Response section
	content.WriteString("=== RESPONSE ===\n")
	content.WriteString(fmt.Sprintf("Status: %d\n", status))

	if responseHeaders != nil {
		for key, values := range responseHeaders {
			for _, value := range values {
				content.WriteString(fmt.Sprintf("%s: %s\n", key, value))
			}
		}
	}

	content.WriteString("\n")
	content.WriteString("Response Body: <filtered>")
	content.WriteString("\n")

	return content.String()
}

// decompressResponse decompresses response data based on Content-Encoding header.
//
// Parameters:
//   - responseHeaders: The response headers
//   - response: The response data to decompress
//
// Returns:
//   - []byte: The decompressed response data
//   - error: An error if decompression fails, nil otherwise
func (l *FileRequestLogger) decompressResponse(responseHeaders map[string][]string, response []byte) ([]byte, error) {
	if responseHeaders == nil || len(response) == 0 {
		return response, nil
	}

	// Check Content-Encoding header
	var contentEncoding string
	for key, values := range responseHeaders {
		if strings.ToLower(key) == "content-encoding" && len(values) > 0 {
			contentEncoding = strings.ToLower(values[0])
			break
		}
	}

	switch contentEncoding {
	case "gzip":
		return l.decompressGzip(response)
	case "deflate":
		return l.decompressDeflate(response)
	case "br":
		return l.decompressBrotli(response)
	case "zstd":
		return l.decompressZstd(response)
	default:
		// No compression or unsupported compression
		return response, nil
	}
}

// decompressGzip decompresses gzip-encoded data.
//
// Parameters:
//   - data: The gzip-encoded data to decompress
//
// Returns:
//   - []byte: The decompressed data
//   - error: An error if decompression fails, nil otherwise
func (l *FileRequestLogger) decompressGzip(data []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer func() {
		if errClose := reader.Close(); errClose != nil {
			log.WithError(errClose).Warn("failed to close gzip reader in request logger")
		}
	}()

	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress gzip data: %w", err)
	}

	return decompressed, nil
}

// decompressDeflate decompresses deflate-encoded data.
//
// Parameters:
//   - data: The deflate-encoded data to decompress
//
// Returns:
//   - []byte: The decompressed data
//   - error: An error if decompression fails, nil otherwise
func (l *FileRequestLogger) decompressDeflate(data []byte) ([]byte, error) {
	reader := flate.NewReader(bytes.NewReader(data))
	defer func() {
		if errClose := reader.Close(); errClose != nil {
			log.WithError(errClose).Warn("failed to close deflate reader in request logger")
		}
	}()

	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress deflate data: %w", err)
	}

	return decompressed, nil
}

// decompressBrotli decompresses brotli-encoded data.
//
// Parameters:
//   - data: The brotli-encoded data to decompress
//
// Returns:
//   - []byte: The decompressed data
//   - error: An error if decompression fails, nil otherwise
func (l *FileRequestLogger) decompressBrotli(data []byte) ([]byte, error) {
	reader := brotli.NewReader(bytes.NewReader(data))

	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress brotli data: %w", err)
	}

	return decompressed, nil
}

// decompressZstd decompresses zstd-encoded data.
//
// Parameters:
//   - data: The zstd-encoded data to decompress
//
// Returns:
//   - []byte: The decompressed data
//   - error: An error if decompression fails, nil otherwise
func (l *FileRequestLogger) decompressZstd(data []byte) ([]byte, error) {
	decoder, err := zstd.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create zstd reader: %w", err)
	}
	defer decoder.Close()

	decompressed, err := io.ReadAll(decoder)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress zstd data: %w", err)
	}

	return decompressed, nil
}

// formatRequestInfo creates the request information section of the log.
//
// Parameters:
//   - url: The request URL
//   - method: The HTTP method
//   - headers: The request headers
//   - body: The request body
//
// Returns:
//   - string: The formatted request information
func (l *FileRequestLogger) formatRequestInfo(url, method string, headers map[string][]string, _ []byte) string {
	var content strings.Builder

	content.WriteString("=== REQUEST INFO ===\n")
	content.WriteString(fmt.Sprintf("Version: %s\n", buildinfo.Version))
	content.WriteString(fmt.Sprintf("URL: %s\n", url))
	content.WriteString(fmt.Sprintf("Method: %s\n", method))
	content.WriteString(fmt.Sprintf("Timestamp: %s\n", time.Now().Format(time.RFC3339Nano)))
	content.WriteString("\n")

	content.WriteString("=== HEADERS ===\n")
	for key, values := range headers {
		for _, value := range values {
			masked := util.MaskSensitiveHeaderValue(key, value)
			content.WriteString(fmt.Sprintf("%s: %s\n", key, masked))
		}
	}
	content.WriteString("\n")

	content.WriteString("Request Body: <filtered>")
	content.WriteString("\n\n")

	return content.String()
}

// FileStreamingLogWriter implements StreamingLogWriter for file-based streaming logs.
// Streaming response payloads are intentionally filtered from request logs.
// The final log file is assembled with request/response metadata when Close is called.
type FileStreamingLogWriter struct {
	logger *FileRequestLogger

	requestID  string
	recordType string

	// url is the request URL (masked upstream in middleware).
	url string

	// method is the HTTP method.
	method string

	// timestamp is captured when the streaming log is initialized.
	timestamp time.Time

	// requestHeaders stores the request headers.
	requestHeaders map[string][]string

	// responseStatus stores the HTTP status code.
	responseStatus int

	// statusWritten indicates whether a non-zero status was recorded.
	statusWritten bool

	// responseHeaders stores the response headers.
	responseHeaders map[string][]string

	// apiRequest stores the upstream API request data.
	apiRequest []byte

	// apiResponse stores the upstream API response data.
	apiResponse []byte

	// apiResponseTimestamp captures when the API response was received.
	apiResponseTimestamp time.Time
}

// WriteChunkAsync intentionally ignores payload chunks.
//
// Parameters:
//   - chunk: The response chunk (filtered from logs)
func (w *FileStreamingLogWriter) WriteChunkAsync(chunk []byte) {
	_ = chunk
}

// WriteStatus buffers the response status and headers for later writing.
//
// Parameters:
//   - status: The response status code
//   - headers: The response headers
//
// Returns:
//   - error: Always returns nil (buffering cannot fail)
func (w *FileStreamingLogWriter) WriteStatus(status int, headers map[string][]string) error {
	if status == 0 {
		return nil
	}

	w.responseStatus = status
	if headers != nil {
		w.responseHeaders = make(map[string][]string, len(headers))
		for key, values := range headers {
			headerValues := make([]string, len(values))
			copy(headerValues, values)
			w.responseHeaders[key] = headerValues
		}
	}
	w.statusWritten = true
	return nil
}

// WriteAPIRequest buffers the upstream API request details for later writing.
//
// Parameters:
//   - apiRequest: The API request data (typically includes URL, headers, body sent upstream)
//
// Returns:
//   - error: Always returns nil (buffering cannot fail)
func (w *FileStreamingLogWriter) WriteAPIRequest(apiRequest []byte) error {
	if len(apiRequest) == 0 {
		return nil
	}
	w.apiRequest = bytes.Clone(apiRequest)
	return nil
}

// WriteAPIResponse buffers the upstream API response details for later writing.
//
// Parameters:
//   - apiResponse: The API response data
//
// Returns:
//   - error: Always returns nil (buffering cannot fail)
func (w *FileStreamingLogWriter) WriteAPIResponse(apiResponse []byte) error {
	if len(apiResponse) == 0 {
		return nil
	}
	w.apiResponse = bytes.Clone(apiResponse)
	return nil
}

func (w *FileStreamingLogWriter) SetFirstChunkTimestamp(timestamp time.Time) {
	if !timestamp.IsZero() {
		w.apiResponseTimestamp = timestamp
	}
}

// Close finalizes the log file and cleans up resources.
// It writes all buffered data to the file in the correct order:
// API REQUEST -> API RESPONSE -> RESPONSE (status, headers, filtered body)
//
// Returns:
//   - error: An error if closing fails, nil otherwise
func (w *FileStreamingLogWriter) Close() error {
	if w.logger == nil {
		return nil
	}

	record, errRecord := w.writeFinalLog()
	if errRecord != nil {
		return errRecord
	}
	return w.logger.appendAggregatedRecord(w.logger.recordFilePath(w.recordType, w.timestamp), record)
}

func (w *FileStreamingLogWriter) writeFinalLog() ([]byte, error) {
	return newRequestRecordBuffer(requestRecordMeta{
		RequestID:  w.requestID,
		Timestamp:  w.timestamp,
		URL:        w.url,
		Method:     w.method,
		StatusCode: w.responseStatus,
		RecordType: w.recordType,
	}, func(dst io.Writer) error {
		if errWrite := writeRequestInfoWithBody(dst, w.url, w.method, w.requestHeaders, nil, "", w.timestamp); errWrite != nil {
			return errWrite
		}
		if errWrite := writeAPISection(dst, "=== API REQUEST ===\n", "=== API REQUEST", w.apiRequest, time.Time{}); errWrite != nil {
			return errWrite
		}
		if errWrite := writeAPISection(dst, "=== API RESPONSE ===\n", "=== API RESPONSE", w.apiResponse, w.apiResponseTimestamp); errWrite != nil {
			return errWrite
		}
		return writeResponseSection(dst, w.responseStatus, w.statusWritten, w.responseHeaders, nil, nil, false)
	})
}

// NoOpStreamingLogWriter is a no-operation implementation for when logging is disabled.
// It implements the StreamingLogWriter interface but performs no actual logging operations.
type NoOpStreamingLogWriter struct{}

// WriteChunkAsync is a no-op implementation that does nothing.
//
// Parameters:
//   - chunk: The response chunk (ignored)
func (w *NoOpStreamingLogWriter) WriteChunkAsync(_ []byte) {}

// WriteStatus is a no-op implementation that does nothing and always returns nil.
//
// Parameters:
//   - status: The response status code (ignored)
//   - headers: The response headers (ignored)
//
// Returns:
//   - error: Always returns nil
func (w *NoOpStreamingLogWriter) WriteStatus(_ int, _ map[string][]string) error {
	return nil
}

// WriteAPIRequest is a no-op implementation that does nothing and always returns nil.
//
// Parameters:
//   - apiRequest: The API request data (ignored)
//
// Returns:
//   - error: Always returns nil
func (w *NoOpStreamingLogWriter) WriteAPIRequest(_ []byte) error {
	return nil
}

// WriteAPIResponse is a no-op implementation that does nothing and always returns nil.
//
// Parameters:
//   - apiResponse: The API response data (ignored)
//
// Returns:
//   - error: Always returns nil
func (w *NoOpStreamingLogWriter) WriteAPIResponse(_ []byte) error {
	return nil
}

func (w *NoOpStreamingLogWriter) SetFirstChunkTimestamp(_ time.Time) {}

// Close is a no-op implementation that does nothing and always returns nil.
//
// Returns:
//   - error: Always returns nil
func (w *NoOpStreamingLogWriter) Close() error { return nil }
