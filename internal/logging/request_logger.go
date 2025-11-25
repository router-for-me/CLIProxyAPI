// Package logging provides request logging functionality for the CLI Proxy API server.
// It handles capturing and storing detailed HTTP request and response data when enabled
// through configuration, supporting both regular and streaming responses.
package logging

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

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
	//
	// Returns:
	//   - error: An error if logging fails, nil otherwise
	LogRequest(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, apiRequest, apiResponse []byte, apiResponseErrors []*interfaces.ErrorMessage) error

	// LogStreamingRequest initiates logging for a streaming request and returns a writer for chunks.
	//
	// Parameters:
	//   - url: The request URL
	//   - method: The HTTP method
	//   - headers: The request headers
	//   - body: The request body
	//
	// Returns:
	//   - StreamingLogWriter: A writer for streaming response chunks
	//   - error: An error if logging initialization fails, nil otherwise
	LogStreamingRequest(url, method string, headers map[string][]string, body []byte) (StreamingLogWriter, error)

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
}

// NewFileRequestLogger creates a new file-based request logger.
//
// Parameters:
//   - enabled: Whether request logging should be enabled
//   - logsDir: The directory where log files should be stored (can be relative)
//   - configDir: The directory of the configuration file; when logsDir is
//     relative, it will be resolved relative to this directory
//
// Returns:
//   - *FileRequestLogger: A new file-based request logger instance
func NewFileRequestLogger(enabled bool, logsDir string, configDir string) *FileRequestLogger {
	// Resolve logsDir relative to the configuration file directory when it's not absolute.
	if !filepath.IsAbs(logsDir) {
		// If configDir is provided, resolve logsDir relative to it.
		if configDir != "" {
			logsDir = filepath.Join(configDir, logsDir)
		}
	}
	return &FileRequestLogger{
		enabled: enabled,
		logsDir: logsDir,
	}
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
//
// Returns:
//   - error: An error if logging fails, nil otherwise
func (l *FileRequestLogger) LogRequest(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, apiRequest, apiResponse []byte, apiResponseErrors []*interfaces.ErrorMessage) error {
	return l.logRequest(url, method, requestHeaders, body, statusCode, responseHeaders, response, apiRequest, apiResponse, apiResponseErrors, false)
}

// LogRequestWithOptions logs a request with optional forced logging behavior.
// The force flag allows writing error logs even when regular request logging is disabled.
func (l *FileRequestLogger) LogRequestWithOptions(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, apiRequest, apiResponse []byte, apiResponseErrors []*interfaces.ErrorMessage, force bool) error {
	return l.logRequest(url, method, requestHeaders, body, statusCode, responseHeaders, response, apiRequest, apiResponse, apiResponseErrors, force)
}

func (l *FileRequestLogger) logRequest(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, apiRequest, apiResponse []byte, apiResponseErrors []*interfaces.ErrorMessage, force bool) error {
	if !l.enabled && !force {
		return nil
	}

	// Ensure logs directory exists
	if errEnsure := l.ensureLogsDir(); errEnsure != nil {
		return fmt.Errorf("failed to create logs directory: %w", errEnsure)
	}

	// Generate filename
	filename := l.generateFilename(url)
	if force && !l.enabled {
		filename = l.generateErrorFilename(url)
	}
	filePath := filepath.Join(l.logsDir, filename)

	// Decompress response if needed
	decompressedResponse, err := l.decompressResponse(responseHeaders, response)
	if err != nil {
		// If decompression fails, log the error but continue with original response
		decompressedResponse = append(response, []byte(fmt.Sprintf("\n[DECOMPRESSION ERROR: %v]", err))...)
	}

	// Create log content
	content := l.formatLogContent(url, method, requestHeaders, body, apiRequest, apiResponse, decompressedResponse, statusCode, responseHeaders, apiResponseErrors)

	// Write to file
	if err = os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write log file: %w", err)
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
//
// Returns:
//   - StreamingLogWriter: A writer for streaming response chunks
//   - error: An error if logging initialization fails, nil otherwise
func (l *FileRequestLogger) LogStreamingRequest(url, method string, headers map[string][]string, body []byte) (StreamingLogWriter, error) {
	if !l.enabled {
		return &NoOpStreamingLogWriter{}, nil
	}

	// Ensure logs directory exists
	if err := l.ensureLogsDir(); err != nil {
		return nil, fmt.Errorf("failed to create logs directory: %w", err)
	}

	// Generate filename
	filename := l.generateFilename(url)
	filePath := filepath.Join(l.logsDir, filename)

	// Create and open file
	file, err := os.Create(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create log file: %w", err)
	}

	// Write initial request information
	requestInfo := l.formatRequestInfo(url, method, headers, body)
	if _, err = file.WriteString(requestInfo); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("failed to write request info: %w", err)
	}

	// Create streaming writer
	writer := &FileStreamingLogWriter{
		file:      file,
		chunkChan: make(chan []byte, 100), // Buffered channel for async writes
		closeChan: make(chan struct{}),
		errorChan: make(chan error, 1),
	}

	// Start async writer goroutine
	go writer.asyncWriter()

	return writer, nil
}

// generateErrorFilename creates a filename with an error prefix to differentiate forced error logs.
func (l *FileRequestLogger) generateErrorFilename(url string) string {
	return fmt.Sprintf("error-%s", l.generateFilename(url))
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
//
// Parameters:
//   - url: The request URL
//
// Returns:
//   - string: A sanitized filename for the log file
func (l *FileRequestLogger) generateFilename(url string) string {
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
	timestamp := time.Now().Format("2006-01-02T150405-.000000000")
	timestamp = strings.Replace(timestamp, ".", "", -1)

	return fmt.Sprintf("%s-%s.log", sanitized, timestamp)
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

// cleanupOldErrorLogs keeps only the newest 10 forced error log files.
func (l *FileRequestLogger) cleanupOldErrorLogs() error {
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

	if len(files) <= 10 {
		return nil
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.After(files[j].modTime)
	})

	for _, file := range files[10:] {
		if errRemove := os.Remove(filepath.Join(l.logsDir, file.name)); errRemove != nil {
			log.WithError(errRemove).Warnf("failed to remove old error log: %s", file.name)
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
func (l *FileRequestLogger) formatLogContent(url, method string, headers map[string][]string, body, apiRequest, apiResponse, response []byte, status int, responseHeaders map[string][]string, apiResponseErrors []*interfaces.ErrorMessage) string {
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
	content.Write(response)
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
func (l *FileRequestLogger) formatRequestInfo(url, method string, headers map[string][]string, body []byte) string {
	var content strings.Builder

	content.WriteString("=== REQUEST INFO ===\n")
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

	content.WriteString("=== REQUEST BODY ===\n")
	content.Write(body)
	content.WriteString("\n\n")

	return content.String()
}

// FileStreamingLogWriter implements StreamingLogWriter for file-based streaming logs.
// It handles asynchronous writing of streaming response chunks to a file.
type FileStreamingLogWriter struct {
	// file is the file where log data is written.
	file *os.File

	// chunkChan is a channel for receiving response chunks to write.
	chunkChan chan []byte

	// closeChan is a channel for signaling when the writer is closed.
	closeChan chan struct{}

	// errorChan is a channel for reporting errors during writing.
	errorChan chan error

	// statusWritten indicates whether the response status has been written.
	statusWritten bool
}

// WriteChunkAsync writes a response chunk asynchronously (non-blocking).
//
// Parameters:
//   - chunk: The response chunk to write
func (w *FileStreamingLogWriter) WriteChunkAsync(chunk []byte) {
	if w.chunkChan == nil {
		return
	}

	// Make a copy of the chunk to avoid data races
	chunkCopy := make([]byte, len(chunk))
	copy(chunkCopy, chunk)

	// Non-blocking send
	select {
	case w.chunkChan <- chunkCopy:
	default:
		// Channel is full, skip this chunk to avoid blocking
	}
}

// WriteStatus writes the response status and headers to the log.
//
// Parameters:
//   - status: The response status code
//   - headers: The response headers
//
// Returns:
//   - error: An error if writing fails, nil otherwise
func (w *FileStreamingLogWriter) WriteStatus(status int, headers map[string][]string) error {
	if w.file == nil || w.statusWritten {
		return nil
	}

	var content strings.Builder
	content.WriteString("========================================\n")
	content.WriteString("=== RESPONSE ===\n")
	content.WriteString(fmt.Sprintf("Status: %d\n", status))

	for key, values := range headers {
		for _, value := range values {
			content.WriteString(fmt.Sprintf("%s: %s\n", key, value))
		}
	}
	content.WriteString("\n")

	_, err := w.file.WriteString(content.String())
	if err == nil {
		w.statusWritten = true
	}
	return err
}

// Close finalizes the log file and cleans up resources.
//
// Returns:
//   - error: An error if closing fails, nil otherwise
func (w *FileStreamingLogWriter) Close() error {
	if w.chunkChan != nil {
		close(w.chunkChan)
	}

	// Wait for async writer to finish
	if w.closeChan != nil {
		<-w.closeChan
		w.chunkChan = nil
	}

	if w.file != nil {
		return w.file.Close()
	}

	return nil
}

// asyncWriter runs in a goroutine to handle async chunk writing.
// It continuously reads chunks from the channel and writes them to the file.
func (w *FileStreamingLogWriter) asyncWriter() {
	defer close(w.closeChan)

	for chunk := range w.chunkChan {
		if w.file != nil {
			_, _ = w.file.Write(chunk)
		}
	}
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

// Close is a no-op implementation that does nothing and always returns nil.
//
// Returns:
//   - error: Always returns nil
func (w *NoOpStreamingLogWriter) Close() error { return nil }
