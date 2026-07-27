package logging

import (
	"bytes"
	"fmt"
	"os"
	"time"

	log "github.com/sirupsen/logrus"
)

// FileStreamingLogWriter implements StreamingLogWriter for file-based streaming logs.
// It spools streaming response chunks to a temporary file to avoid retaining large responses in memory.
// The final log file is assembled when Close is called.
type FileStreamingLogWriter struct {
	// logFilePath is the final log file path.
	logFilePath string

	// url is the request URL (masked upstream in middleware).
	url string

	// method is the HTTP method.
	method string

	// timestamp is captured when the streaming log is initialized.
	timestamp time.Time

	// requestHeaders stores the request headers.
	requestHeaders map[string][]string

	// requestBodyPath is a temporary file path holding the request body.
	requestBodyPath string

	// responseBodyPath is a temporary file path holding the streaming response body.
	responseBodyPath string

	// responseBodyFile is the temp file where chunks are appended by the async writer.
	responseBodyFile *os.File

	// chunkChan is a channel for receiving response chunks to spool.
	chunkChan chan []byte

	// closeChan is a channel for signaling when the writer is closed.
	closeChan chan struct{}

	// errorChan is a channel for reporting errors during writing.
	errorChan chan error

	// responseStatus stores the HTTP status code.
	responseStatus int

	// statusWritten indicates whether a non-zero status was recorded.
	statusWritten bool

	// responseHeaders stores the response headers.
	responseHeaders map[string][]string

	// apiRequest stores the upstream API request data.
	apiRequest []byte

	// apiRequestSource stores file-backed upstream API request data.
	apiRequestSource *FileBodySource

	// apiResponse stores the upstream API response data.
	apiResponse []byte

	// apiResponseSource stores file-backed upstream API response data.
	apiResponseSource *FileBodySource

	// apiWebsocketTimeline stores the upstream websocket event timeline.
	apiWebsocketTimeline []byte

	// apiResponseTimestamp captures when the API response was received.
	apiResponseTimestamp time.Time
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

// WriteAPIRequestSource buffers a file-backed upstream API request for final writing.
func (w *FileStreamingLogWriter) WriteAPIRequestSource(apiRequestSource *FileBodySource) error {
	if apiRequestSource == nil || !apiRequestSource.HasPayload() {
		return nil
	}
	w.apiRequestSource = apiRequestSource
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

// WriteAPIResponseSource buffers a file-backed upstream API response for final writing.
func (w *FileStreamingLogWriter) WriteAPIResponseSource(apiResponseSource *FileBodySource) error {
	if apiResponseSource == nil || !apiResponseSource.HasPayload() {
		return nil
	}
	w.apiResponseSource = apiResponseSource
	return nil
}

// WriteAPIWebsocketTimeline buffers the upstream websocket timeline for later writing.
//
// Parameters:
//   - apiWebsocketTimeline: The upstream websocket event timeline
//
// Returns:
//   - error: Always returns nil (buffering cannot fail)
func (w *FileStreamingLogWriter) WriteAPIWebsocketTimeline(apiWebsocketTimeline []byte) error {
	if len(apiWebsocketTimeline) == 0 {
		return nil
	}
	w.apiWebsocketTimeline = bytes.Clone(apiWebsocketTimeline)
	return nil
}

func (w *FileStreamingLogWriter) SetFirstChunkTimestamp(timestamp time.Time) {
	if !timestamp.IsZero() {
		w.apiResponseTimestamp = timestamp
	}
}

// Close finalizes the log file and cleans up resources.
// It writes all buffered data to the file in the correct order:
// API WEBSOCKET TIMELINE -> API REQUEST -> API RESPONSE -> RESPONSE (status, headers, body chunks)
//
// Returns:
//   - error: An error if closing fails, nil otherwise
func (w *FileStreamingLogWriter) Close() error {
	if w.chunkChan != nil {
		close(w.chunkChan)
	}

	// Wait for async writer to finish spooling chunks
	if w.closeChan != nil {
		<-w.closeChan
		w.chunkChan = nil
	}

	select {
	case errWrite := <-w.errorChan:
		w.cleanupTempFiles()
		return errWrite
	default:
	}

	if w.logFilePath == "" {
		w.cleanupTempFiles()
		return nil
	}

	logFile, errOpen := os.OpenFile(w.logFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if errOpen != nil {
		w.cleanupTempFiles()
		return fmt.Errorf("failed to create log file: %w", errOpen)
	}

	writeErr := w.writeFinalLog(logFile)
	if errClose := logFile.Close(); errClose != nil {
		log.WithError(errClose).Warn("failed to close request log file")
		if writeErr == nil {
			writeErr = errClose
		}
	}

	w.cleanupTempFiles()
	return writeErr
}

// asyncWriter runs in a goroutine to buffer chunks from the channel.
// It continuously reads chunks from the channel and appends them to a temp file for later assembly.
func (w *FileStreamingLogWriter) asyncWriter() {
	defer close(w.closeChan)

	for chunk := range w.chunkChan {
		if w.responseBodyFile == nil {
			continue
		}
		if _, errWrite := w.responseBodyFile.Write(chunk); errWrite != nil {
			select {
			case w.errorChan <- errWrite:
			default:
			}
			if errClose := w.responseBodyFile.Close(); errClose != nil {
				select {
				case w.errorChan <- errClose:
				default:
				}
			}
			w.responseBodyFile = nil
		}
	}

	if w.responseBodyFile == nil {
		return
	}
	if errClose := w.responseBodyFile.Close(); errClose != nil {
		select {
		case w.errorChan <- errClose:
		default:
		}
	}
	w.responseBodyFile = nil
}

func (w *FileStreamingLogWriter) writeFinalLog(logFile *os.File) error {
	if errWrite := writeRequestInfoWithBody(logFile, w.url, w.method, w.requestHeaders, nil, w.requestBodyPath, w.timestamp, "http", inferUpstreamTransport(w.apiRequest, w.apiRequestSource, w.apiResponse, w.apiResponseSource, w.apiWebsocketTimeline, nil, nil), true); errWrite != nil {
		return errWrite
	}
	if errWrite := writeAPISection(logFile, "=== API WEBSOCKET TIMELINE ===\n", "=== API WEBSOCKET TIMELINE", w.apiWebsocketTimeline, time.Time{}); errWrite != nil {
		return errWrite
	}
	if errWrite := writePreformattedAPISectionWithSource(logFile, "=== API REQUEST ===\n", "=== API REQUEST", w.apiRequest, w.apiRequestSource, time.Time{}); errWrite != nil {
		return errWrite
	}
	if errWrite := writePreformattedAPISectionWithSource(logFile, "=== API RESPONSE ===\n", "=== API RESPONSE", w.apiResponse, w.apiResponseSource, w.apiResponseTimestamp); errWrite != nil {
		return errWrite
	}

	responseBodyFile, errOpen := os.Open(w.responseBodyPath)
	if errOpen != nil {
		return errOpen
	}
	defer func() {
		if errClose := responseBodyFile.Close(); errClose != nil {
			log.WithError(errClose).Warn("failed to close response body temp file")
		}
	}()

	return writeResponseSection(logFile, w.responseStatus, w.statusWritten, w.responseHeaders, responseBodyFile, nil, false)
}

func (w *FileStreamingLogWriter) cleanupTempFiles() {
	if w.requestBodyPath != "" {
		if errRemove := os.Remove(w.requestBodyPath); errRemove != nil {
			log.WithError(errRemove).Warn("failed to remove request body temp file")
		}
		w.requestBodyPath = ""
	}

	if w.responseBodyPath != "" {
		if errRemove := os.Remove(w.responseBodyPath); errRemove != nil {
			log.WithError(errRemove).Warn("failed to remove response body temp file")
		}
		w.responseBodyPath = ""
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

// WriteAPIWebsocketTimeline is a no-op implementation that does nothing and always returns nil.
//
// Parameters:
//   - apiWebsocketTimeline: The upstream websocket event timeline (ignored)
//
// Returns:
//   - error: Always returns nil
func (w *NoOpStreamingLogWriter) WriteAPIWebsocketTimeline(_ []byte) error {
	return nil
}

func (w *NoOpStreamingLogWriter) SetFirstChunkTimestamp(_ time.Time) {}

// Close is a no-op implementation that does nothing and always returns nil.
//
// Returns:
//   - error: Always returns nil
func (w *NoOpStreamingLogWriter) Close() error { return nil }
