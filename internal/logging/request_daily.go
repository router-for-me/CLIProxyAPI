package logging

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	requestLogDateLayout   = "2006-01-02"
	requestLogTypeNormal   = "request"
	requestLogTypeError    = "request-error"
	requestRecordStartLine = "=== REQUEST LOG RECORD ==="
	requestRecordEndLine   = "=== END REQUEST LOG RECORD ==="
	requestRetentionPeriod = time.Hour
	requestScanInitialBuf  = 64 * 1024
	requestScanMaxBuf      = 8 * 1024 * 1024
)

type requestRecordMeta struct {
	RequestID  string
	Timestamp  time.Time
	URL        string
	Method     string
	StatusCode int
	RecordType string
}

func (l *FileRequestLogger) Reconfigure(logsDir string, configDir string) {
	resolvedLogsDir := resolveRequestLogsDir(logsDir, configDir)

	l.writeMu.Lock()
	l.logsDir = resolvedLogsDir
	l.writeMu.Unlock()

	l.retentionMu.Lock()
	l.configDir = configDir
	l.retentionMu.Unlock()

	l.reloadRequestPolicy()
}

func (l *FileRequestLogger) reloadRequestPolicy() {
	policy, err := LoadFileLogPolicyFromDir(l.configDir)
	if err != nil {
		log.WithError(err).Warnf("logging: failed to load %s for request logger, keeping last valid/default policy", LoggingConfigFileName)
	}

	l.retentionMu.Lock()
	l.requestPolicy = policy.Request
	l.retentionMu.Unlock()

	l.restartRequestRetentionWorker()
}

func (l *FileRequestLogger) currentRequestPolicy() RequestLogPolicy {
	l.retentionMu.Lock()
	defer l.retentionMu.Unlock()
	return l.requestPolicy
}

func (l *FileRequestLogger) restartRequestRetentionWorker() {
	l.retentionMu.Lock()
	if l.retentionCancel != nil {
		l.retentionCancel()
		l.retentionCancel = nil
	}

	logsDir := l.logsDir
	policy := l.requestPolicy
	if strings.TrimSpace(logsDir) == "" {
		l.retentionMu.Unlock()
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	l.retentionCancel = cancel
	l.retentionMu.Unlock()

	go l.runRequestRetention(ctx, logsDir, policy)
}

func (l *FileRequestLogger) runRequestRetention(ctx context.Context, logsDir string, policy RequestLogPolicy) {
	cleanOnce := func() {
		if err := EnforceRequestLogRetention(logsDir, policy, time.Now()); err != nil {
			log.WithError(err).Warn("logging: failed to enforce request log retention")
		}
	}

	cleanOnce()
	ticker := time.NewTicker(requestRetentionPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanOnce()
		}
	}
}

func resolveRequestLogsDir(logsDir string, configDir string) string {
	if filepath.IsAbs(logsDir) {
		return logsDir
	}
	if strings.TrimSpace(configDir) == "" {
		return logsDir
	}
	return filepath.Join(configDir, logsDir)
}

func (l *FileRequestLogger) normalizeRequestID(requestID string) string {
	trimmed := strings.TrimSpace(requestID)
	if trimmed != "" {
		return trimmed
	}
	id := requestLogID.Add(1)
	return fmt.Sprintf("%d", id)
}

func (l *FileRequestLogger) recordFilePath(recordType string, ts time.Time) string {
	stamp := ts
	if stamp.IsZero() {
		stamp = time.Now()
	}
	return filepath.Join(l.logsDir, fmt.Sprintf("%s-%s.log", recordType, stamp.Format(requestLogDateLayout)))
}

func (l *FileRequestLogger) appendAggregatedRecord(filePath string, data []byte) error {
	l.writeMu.Lock()
	defer l.writeMu.Unlock()

	logFile, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if errClose := logFile.Close(); errClose != nil {
			log.WithError(errClose).Warn("failed to close request log file")
		}
	}()

	_, err = logFile.Write(data)
	return err
}

func writeRequestRecordEnvelope(w io.Writer, meta requestRecordMeta, inner func(io.Writer) error) error {
	timestamp := meta.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	if _, err := io.WriteString(w, requestRecordStartLine+"\n"); err != nil {
		return err
	}
	if _, err := io.WriteString(w, fmt.Sprintf("Request-ID: %s\n", meta.RequestID)); err != nil {
		return err
	}
	if _, err := io.WriteString(w, fmt.Sprintf("Timestamp: %s\n", timestamp.Format(time.RFC3339Nano))); err != nil {
		return err
	}
	if _, err := io.WriteString(w, fmt.Sprintf("URL: %s\n", meta.URL)); err != nil {
		return err
	}
	if _, err := io.WriteString(w, fmt.Sprintf("Method: %s\n", meta.Method)); err != nil {
		return err
	}
	if _, err := io.WriteString(w, fmt.Sprintf("Status: %d\n", meta.StatusCode)); err != nil {
		return err
	}
	if _, err := io.WriteString(w, fmt.Sprintf("Record-Type: %s\n\n", meta.RecordType)); err != nil {
		return err
	}

	if err := inner(w); err != nil {
		return err
	}

	if _, err := io.WriteString(w, "\n"+requestRecordEndLine+"\n\n"); err != nil {
		return err
	}
	return nil
}

func OpenLogFile(path string) (io.ReadCloser, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(strings.ToLower(path), ".gz") {
		reader, err := gzip.NewReader(file)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		return &gzipReadCloser{Reader: reader, file: file}, nil
	}
	return file, nil
}

type gzipReadCloser struct {
	*gzip.Reader
	file *os.File
}

func (r *gzipReadCloser) Close() error {
	errReader := r.Reader.Close()
	errFile := r.file.Close()
	if errReader != nil {
		return errReader
	}
	return errFile
}

func EnforceRequestLogRetention(logsDir string, policy RequestLogPolicy, now time.Time) error {
	dir := strings.TrimSpace(logsDir)
	if dir == "" {
		return nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		fileDay, _, compressed, ok := parseDailyRequestLogName(name)
		if !ok {
			continue
		}
		ageDays := int(today.Sub(fileDay).Hours() / 24)
		fullPath := filepath.Join(dir, name)

		if policy.DeleteAfterDays > 0 && ageDays >= policy.DeleteAfterDays {
			if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}

		if compressed || policy.CompressAfterDays <= 0 || ageDays < policy.CompressAfterDays {
			continue
		}
		if err := gzipFile(fullPath); err != nil {
			return err
		}
	}

	return nil
}

func gzipFile(path string) error {
	in, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() {
		_ = in.Close()
	}()

	targetPath := path + ".gz"
	out, err := os.Create(targetPath)
	if err != nil {
		return err
	}

	gz := gzip.NewWriter(out)
	_, err = io.Copy(gz, in)
	closeErr := gz.Close()
	fileErr := out.Close()
	if err != nil {
		_ = os.Remove(targetPath)
		return err
	}
	if closeErr != nil {
		_ = os.Remove(targetPath)
		return closeErr
	}
	if fileErr != nil {
		_ = os.Remove(targetPath)
		return fileErr
	}
	return os.Remove(path)
}

func IsRequestLogFileName(name string) bool {
	_, _, _, ok := parseDailyRequestLogName(name)
	return ok
}

func IsRequestErrorLogFileName(name string) bool {
	_, recordType, _, ok := parseDailyRequestLogName(name)
	return ok && recordType == requestLogTypeError
}

func RequestLogOrder(name string) (int64, bool) {
	fileDay, _, compressed, ok := parseDailyRequestLogName(name)
	if !ok {
		return 0, false
	}
	order := fileDay.Unix()
	if !compressed {
		order += 1
	}
	return order, true
}

func ExtractRequestRecordByID(path string, requestID string) ([]byte, error) {
	reader, err := OpenLogFile(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = reader.Close()
	}()

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, requestScanInitialBuf), requestScanMaxBuf)

	var (
		current  bytes.Buffer
		inRecord bool
		matched  bool
	)

	for scanner.Scan() {
		line := scanner.Text()
		switch line {
		case requestRecordStartLine:
			current.Reset()
			inRecord = true
			matched = false
			current.WriteString(line)
			current.WriteByte('\n')
			continue
		case requestRecordEndLine:
			if !inRecord {
				continue
			}
			current.WriteString(line)
			current.WriteString("\n\n")
			if matched {
				return bytes.Clone(current.Bytes()), nil
			}
			inRecord = false
			continue
		}

		if !inRecord {
			continue
		}
		if strings.TrimSpace(line) == fmt.Sprintf("Request-ID: %s", requestID) {
			matched = true
		}
		current.WriteString(line)
		current.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, os.ErrNotExist
}

func parseDailyRequestLogName(name string) (time.Time, string, bool, bool) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return time.Time{}, "", false, false
	}

	compressed := strings.HasSuffix(strings.ToLower(trimmed), ".gz")
	base := trimmed
	if compressed {
		base = strings.TrimSuffix(base, ".gz")
	}
	if !strings.HasSuffix(strings.ToLower(base), ".log") {
		return time.Time{}, "", false, false
	}
	base = strings.TrimSuffix(base, ".log")

	recordType := ""
	datePart := ""
	switch {
	case strings.HasPrefix(base, requestLogTypeError+"-"):
		recordType = requestLogTypeError
		datePart = strings.TrimPrefix(base, requestLogTypeError+"-")
	case strings.HasPrefix(base, requestLogTypeNormal+"-"):
		recordType = requestLogTypeNormal
		datePart = strings.TrimPrefix(base, requestLogTypeNormal+"-")
	default:
		return time.Time{}, "", false, false
	}

	parsed, err := time.ParseInLocation(requestLogDateLayout, datePart, time.Local)
	if err != nil {
		return time.Time{}, "", false, false
	}
	return parsed, recordType, compressed, true
}

func newRequestRecordBuffer(meta requestRecordMeta, inner func(io.Writer) error) ([]byte, error) {
	var buffer bytes.Buffer
	if err := writeRequestRecordEnvelope(&buffer, meta, inner); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func closeRequestRetention(l *FileRequestLogger) {
	if l == nil {
		return
	}
	l.retentionMu.Lock()
	defer l.retentionMu.Unlock()
	if l.retentionCancel != nil {
		l.retentionCancel()
		l.retentionCancel = nil
	}
}
