package logging

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	log "github.com/sirupsen/logrus"
)

const (
	defaultRequestSummaryRotation = 5 * time.Hour
	defaultRequestSummaryMaxFiles = 48
	requestSummaryPrefix          = "request-summary-"
)

type requestSummaryRecord struct {
	Timestamp              time.Time `json:"timestamp"`
	RequestID              string    `json:"request_id,omitempty"`
	Method                 string    `json:"method"`
	Path                   string    `json:"path"`
	Status                 int       `json:"status"`
	DurationMS             int64     `json:"duration_ms"`
	DownstreamTransport    string    `json:"downstream_transport,omitempty"`
	UpstreamTransport      string    `json:"upstream_transport,omitempty"`
	RequestBytes           int64     `json:"request_bytes"`
	ResponseBytes          int64     `json:"response_bytes"`
	UpstreamRequestBytes   int64     `json:"upstream_request_bytes,omitempty"`
	UpstreamResponseBytes  int64     `json:"upstream_response_bytes,omitempty"`
	WebsocketTimelineBytes int64     `json:"websocket_timeline_bytes,omitempty"`
	UpstreamWebsocketBytes int64     `json:"upstream_websocket_timeline_bytes,omitempty"`
}

func isFailedRequest(statusCode int, apiResponseErrors []*interfaces.ErrorMessage, force bool) bool {
	return force || statusCode >= 400 || len(apiResponseErrors) > 0
}

func fileSize(path string) int64 {
	if strings.TrimSpace(path) == "" {
		return 0
	}
	info, errStat := os.Stat(path)
	if errStat != nil || info.IsDir() {
		return 0
	}
	return info.Size()
}

func fileBodySourceSize(source *FileBodySource) int64 {
	if source == nil {
		return 0
	}
	var total int64
	for _, path := range source.Paths() {
		total += fileSize(path)
	}
	return total
}

func sectionSize(payload []byte, source *FileBodySource) int64 {
	return int64(len(payload)) + fileBodySourceSize(source)
}

func (l *FileRequestLogger) newRequestSummary(
	url, method, requestID string,
	statusCode int,
	requestTimestamp time.Time,
	requestBytes, responseBytes int64,
	downstreamTransport, upstreamTransport string,
	upstreamRequestBytes, upstreamResponseBytes, websocketTimelineBytes, upstreamWebsocketBytes int64,
) requestSummaryRecord {
	completedAt := l.currentTime()
	if requestTimestamp.IsZero() {
		requestTimestamp = completedAt
	}
	duration := completedAt.Sub(requestTimestamp)
	if duration < 0 {
		duration = 0
	}
	return requestSummaryRecord{
		Timestamp:              requestTimestamp,
		RequestID:              strings.TrimSpace(requestID),
		Method:                 method,
		Path:                   normalizeRequestSummaryPath(url),
		Status:                 statusCode,
		DurationMS:             duration.Milliseconds(),
		DownstreamTransport:    downstreamTransport,
		UpstreamTransport:      upstreamTransport,
		RequestBytes:           requestBytes,
		ResponseBytes:          responseBytes,
		UpstreamRequestBytes:   upstreamRequestBytes,
		UpstreamResponseBytes:  upstreamResponseBytes,
		WebsocketTimelineBytes: websocketTimelineBytes,
		UpstreamWebsocketBytes: upstreamWebsocketBytes,
	}
}

// normalizeRequestSummaryPath keeps request summaries useful for routing
// diagnostics without persisting query parameters, which commonly carry
// credentials, tokens, prompts, or other user-provided data.
func normalizeRequestSummaryPath(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if parsed, errParse := url.Parse(rawURL); errParse == nil {
		path := parsed.EscapedPath()
		if path == "" {
			path = "/"
		}
		return path
	}
	if delimiter := strings.IndexAny(rawURL, "?#"); delimiter >= 0 {
		rawURL = rawURL[:delimiter]
	}
	if rawURL == "" {
		return "/"
	}
	return rawURL
}

func summaryWindowFilename(timestamp time.Time, rotation time.Duration) string {
	if rotation <= 0 {
		rotation = defaultRequestSummaryRotation
	}
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	window := timestamp.UTC().UnixNano() / int64(rotation)
	windowStart := time.Unix(0, window*int64(rotation)).UTC()
	return fmt.Sprintf("%s%s.log", requestSummaryPrefix, windowStart.Format("20060102T150405Z"))
}

func (l *FileRequestLogger) writeSuccessSummary(record requestSummaryRecord) error {
	_, rotation, maxFiles := l.successSummaryPolicy()
	if errEnsure := l.ensureLogsDir(); errEnsure != nil {
		return fmt.Errorf("failed to create logs directory: %w", errEnsure)
	}

	filename := summaryWindowFilename(record.Timestamp, rotation)
	filePath := filepath.Join(l.logsDir, filename)

	l.summaryMu.Lock()
	defer l.summaryMu.Unlock()

	logFile, errOpen := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if errOpen != nil {
		return fmt.Errorf("failed to open request summary log: %w", errOpen)
	}
	encoder := json.NewEncoder(logFile)
	encoder.SetEscapeHTML(false)
	writeErr := encoder.Encode(record)
	if errClose := logFile.Close(); errClose != nil && writeErr == nil {
		writeErr = errClose
	}
	if writeErr != nil {
		return fmt.Errorf("failed to write request summary log: %w", writeErr)
	}
	cleanupKey := fmt.Sprintf("%s:%d", filename, maxFiles)
	if l.summaryCleanupKey != cleanupKey {
		if errCleanup := l.cleanupOldSummaryLogs(filename, maxFiles); errCleanup != nil {
			// Leave the key unset so the next successful request retries cleanup.
			log.WithError(errCleanup).Warn("failed to clean up old request summary logs")
		} else {
			l.summaryCleanupKey = cleanupKey
		}
	}
	return nil
}

func (l *FileRequestLogger) cleanupOldSummaryLogs(protectedName string, maxFiles int) error {
	if maxFiles <= 0 {
		return nil
	}
	entries, errRead := os.ReadDir(l.logsDir)
	if errRead != nil {
		return errRead
	}
	type summaryFile struct {
		name    string
		modTime time.Time
	}
	files := make([]summaryFile, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, requestSummaryPrefix) || !strings.HasSuffix(name, ".log") {
			continue
		}
		info, errInfo := entry.Info()
		if errInfo != nil {
			continue
		}
		files = append(files, summaryFile{name: name, modTime: info.ModTime()})
	}
	if len(files) <= maxFiles {
		return nil
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modTime.Equal(files[j].modTime) {
			return files[i].name > files[j].name
		}
		return files[i].modTime.After(files[j].modTime)
	})
	keep := make(map[string]struct{}, maxFiles)
	if protectedName != "" {
		for _, file := range files {
			if file.name == protectedName {
				keep[file.name] = struct{}{}
				break
			}
		}
	}
	for _, file := range files {
		if len(keep) >= maxFiles {
			break
		}
		keep[file.name] = struct{}{}
	}
	for _, file := range files {
		if _, ok := keep[file.name]; ok {
			continue
		}
		if errRemove := os.Remove(filepath.Join(l.logsDir, file.name)); errRemove != nil && !os.IsNotExist(errRemove) {
			log.WithError(errRemove).Warnf("failed to remove old request summary log: %s", file.name)
		}
	}
	return nil
}
