package management

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

const (
	defaultLogFileName      = "main.log"
	logScannerInitialBuffer = 64 * 1024
	logScannerMaxBuffer     = 8 * 1024 * 1024
	logCursorVersion        = 1
	logCursorFingerprintMax = 4 * 1024
)

// GetLogs returns log lines with optional incremental loading.
//
// The legacy timestamp path keeps line-count as the total scanned line count for
// compatibility. Cursor and tail reads avoid scanning older files, so line-count
// is the number of returned lines there. A cursor emitted by the legacy path
// points at the latest complete log boundary; combining after with limit is
// therefore tail semantics and does not replay lines trimmed by limit.
func (h *Handler) GetLogs(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}
	if !h.cfg.LoggingToFile {
		c.JSON(http.StatusBadRequest, gin.H{"error": "logging to file disabled"})
		return
	}

	logDir := h.logDirectory()
	if strings.TrimSpace(logDir) == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "log directory not configured"})
		return
	}

	rawCursor := strings.TrimSpace(c.Query("cursor"))
	files, err := h.collectLogFiles(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			cutoff := parseCutoff(c.Query("after"))
			latest := cutoff
			if rawCursor != "" {
				if cursor, errCursor := decodeLogCursor(rawCursor); errCursor == nil && cursor.LatestTimestamp > latest {
					latest = cursor.LatestTimestamp
				}
			}
			writeLogsResponse(c, []string{}, 0, latest, "", rawCursor != "")
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list log files: %v", err)})
		return
	}

	limit, errLimit := parseLimit(c.Query("limit"))
	if errLimit != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid limit: %v", errLimit)})
		return
	}

	cutoff := parseCutoff(c.Query("after"))
	if rawCursor != "" {
		result, reset, errCursor := readLogFilesFromCursor(logDir, files, rawCursor, limit)
		if errCursor != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to read log files: %v", errCursor)})
			return
		}
		if reset {
			result, errCursor = tailLogFiles(files, limit, result.latest)
			if errCursor != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to read log files: %v", errCursor)})
				return
			}
			writeLogsResponse(c, result.lines, len(result.lines), result.latest, result.nextCursor, true)
			return
		}
		writeLogsResponse(c, result.lines, len(result.lines), result.latest, result.nextCursor, false)
		return
	}

	if cutoff == 0 && limit > 0 {
		result, errTail := tailLogFiles(files, limit, 0)
		if errTail != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to read log files: %v", errTail)})
			return
		}
		writeLogsResponse(c, result.lines, len(result.lines), result.latest, result.nextCursor, false)
		return
	}

	acc := newLogAccumulator(cutoff, limit)
	for i := range files {
		if errProcess := acc.consumeFile(files[i]); errProcess != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to read log file: %v", errProcess)})
			return
		}
	}

	lines, total, latest := acc.result()
	if latest == 0 || latest < cutoff {
		latest = cutoff
	}
	nextCursor, errCursor := cursorForLatestLogFile(files, latest)
	if errCursor != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to prepare log cursor: %v", errCursor)})
		return
	}
	writeLogsResponse(c, lines, total, latest, nextCursor, false)
}

// DeleteLogs removes all rotated log files and truncates the active log.
func (h *Handler) DeleteLogs(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}
	if !h.cfg.LoggingToFile {
		c.JSON(http.StatusBadRequest, gin.H{"error": "logging to file disabled"})
		return
	}

	dir := h.logDirectory()
	if strings.TrimSpace(dir) == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "log directory not configured"})
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "log directory not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list log directory: %v", err)})
		return
	}

	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		fullPath := filepath.Join(dir, name)
		if name == defaultLogFileName {
			if errTrunc := os.Truncate(fullPath, 0); errTrunc != nil && !os.IsNotExist(errTrunc) {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to truncate log file: %v", errTrunc)})
				return
			}
			continue
		}
		if isRotatedLogFile(name) {
			if errRemove := os.Remove(fullPath); errRemove != nil && !os.IsNotExist(errRemove) {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to remove %s: %v", name, errRemove)})
				return
			}
			removed++
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Logs cleared successfully",
		"removed": removed,
	})
}

// GetRequestErrorLogs lists error request log files when RequestLog is disabled.
// It returns an empty list when RequestLog is enabled.
func (h *Handler) GetRequestErrorLogs(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}
	if h.cfg.RequestLog {
		c.JSON(http.StatusOK, gin.H{"files": []any{}})
		return
	}

	dir := h.logDirectory()
	if strings.TrimSpace(dir) == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "log directory not configured"})
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusOK, gin.H{"files": []any{}})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list request error logs: %v", err)})
		return
	}

	type errorLog struct {
		Name     string `json:"name"`
		Size     int64  `json:"size"`
		Modified int64  `json:"modified"`
	}

	files := make([]errorLog, 0, len(entries))
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
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to read log info for %s: %v", name, errInfo)})
			return
		}
		files = append(files, errorLog{
			Name:     name,
			Size:     info.Size(),
			Modified: info.ModTime().Unix(),
		})
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Modified > files[j].Modified })

	c.JSON(http.StatusOK, gin.H{"files": files})
}

// GetRequestLogByID finds and downloads a request log file by its request ID.
// The ID is matched against the suffix of log file names (format: *-{requestID}.log).
func (h *Handler) GetRequestLogByID(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}

	dir := h.logDirectory()
	if strings.TrimSpace(dir) == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "log directory not configured"})
		return
	}

	requestID := strings.TrimSpace(c.Param("id"))
	if requestID == "" {
		requestID = strings.TrimSpace(c.Query("id"))
	}
	if requestID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing request ID"})
		return
	}
	if strings.ContainsAny(requestID, "/\\") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request ID"})
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "log directory not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list log directory: %v", err)})
		return
	}

	suffix := "-" + requestID + ".log"
	var matchedFile string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, suffix) {
			matchedFile = name
			break
		}
	}

	if matchedFile == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "log file not found for the given request ID"})
		return
	}

	dirAbs, errAbs := filepath.Abs(dir)
	if errAbs != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to resolve log directory: %v", errAbs)})
		return
	}
	fullPath := filepath.Clean(filepath.Join(dirAbs, matchedFile))
	prefix := dirAbs + string(os.PathSeparator)
	if !strings.HasPrefix(fullPath, prefix) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid log file path"})
		return
	}

	info, errStat := os.Stat(fullPath)
	if errStat != nil {
		if os.IsNotExist(errStat) {
			c.JSON(http.StatusNotFound, gin.H{"error": "log file not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to read log file: %v", errStat)})
		return
	}
	if info.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid log file"})
		return
	}

	c.FileAttachment(fullPath, matchedFile)
}

// DownloadRequestErrorLog downloads a specific error request log file by name.
func (h *Handler) DownloadRequestErrorLog(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}

	dir := h.logDirectory()
	if strings.TrimSpace(dir) == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "log directory not configured"})
		return
	}

	name := strings.TrimSpace(c.Param("name"))
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid log file name"})
		return
	}
	if !strings.HasPrefix(name, "error-") || !strings.HasSuffix(name, ".log") {
		c.JSON(http.StatusNotFound, gin.H{"error": "log file not found"})
		return
	}

	dirAbs, errAbs := filepath.Abs(dir)
	if errAbs != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to resolve log directory: %v", errAbs)})
		return
	}
	fullPath := filepath.Clean(filepath.Join(dirAbs, name))
	prefix := dirAbs + string(os.PathSeparator)
	if !strings.HasPrefix(fullPath, prefix) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid log file path"})
		return
	}

	info, errStat := os.Stat(fullPath)
	if errStat != nil {
		if os.IsNotExist(errStat) {
			c.JSON(http.StatusNotFound, gin.H{"error": "log file not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to read log file: %v", errStat)})
		return
	}
	if info.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid log file"})
		return
	}

	c.FileAttachment(fullPath, name)
}

func (h *Handler) logDirectory() string {
	if h == nil {
		return ""
	}
	if h.logDir != "" {
		return h.logDir
	}
	return logging.ResolveLogDirectory(h.cfg)
}

func (h *Handler) collectLogFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		path  string
		order int64
	}
	cands := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == defaultLogFileName {
			cands = append(cands, candidate{path: filepath.Join(dir, name), order: 0})
			continue
		}
		if order, ok := rotationOrder(name); ok {
			cands = append(cands, candidate{path: filepath.Join(dir, name), order: order})
		}
	}
	if len(cands) == 0 {
		return []string{}, nil
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].order < cands[j].order })
	paths := make([]string, 0, len(cands))
	for i := len(cands) - 1; i >= 0; i-- {
		paths = append(paths, cands[i].path)
	}
	return paths, nil
}

type logAccumulator struct {
	cutoff  int64
	limit   int
	lines   []string
	total   int
	latest  int64
	include bool
}

func newLogAccumulator(cutoff int64, limit int) *logAccumulator {
	capacity := 256
	if limit > 0 && limit < capacity {
		capacity = limit
	}
	return &logAccumulator{
		cutoff: cutoff,
		limit:  limit,
		lines:  make([]string, 0, capacity),
	}
}

func (acc *logAccumulator) consumeFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() {
		_ = file.Close()
	}()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, logScannerInitialBuffer)
	scanner.Buffer(buf, logScannerMaxBuffer)
	for scanner.Scan() {
		acc.addLine(scanner.Text())
	}
	if errScan := scanner.Err(); errScan != nil {
		return errScan
	}
	return nil
}

func (acc *logAccumulator) addLine(raw string) {
	line := strings.TrimRight(raw, "\r")
	acc.total++
	ts := parseTimestamp(line)
	if ts > acc.latest {
		acc.latest = ts
	}
	if ts > 0 {
		acc.include = acc.cutoff == 0 || ts > acc.cutoff
		if acc.cutoff == 0 || acc.include {
			acc.append(line)
		}
		return
	}
	if acc.cutoff == 0 || acc.include {
		acc.append(line)
	}
}

func (acc *logAccumulator) append(line string) {
	acc.lines = append(acc.lines, line)
	if acc.limit > 0 && len(acc.lines) > acc.limit {
		acc.lines = acc.lines[len(acc.lines)-acc.limit:]
	}
}

func (acc *logAccumulator) result() ([]string, int, int64) {
	if acc.lines == nil {
		acc.lines = []string{}
	}
	return acc.lines, acc.total, acc.latest
}

type logCursor struct {
	Version         int    `json:"v"`
	File            string `json:"file"`
	Offset          int64  `json:"offset"`
	Size            int64  `json:"size"`
	ModTime         int64  `json:"modTime"`
	ModTimeUnixNano int64  `json:"modTimeUnixNano,omitempty"`
	LatestTimestamp int64  `json:"latestTimestamp"`
	Fingerprint     string `json:"fingerprint"`
}

type completeLogRead struct {
	lines     []string
	endOffset int64
	latest    int64
	hitLimit  bool
}

type logReadResult struct {
	lines      []string
	latest     int64
	nextCursor string
}

func writeLogsResponse(c *gin.Context, lines []string, lineCount int, latest int64, nextCursor string, cursorReset bool) {
	if lines == nil {
		lines = []string{}
	}
	payload := gin.H{
		"lines":            lines,
		"line-count":       lineCount,
		"latest-timestamp": latest,
		"next-cursor":      nextCursor,
	}
	if cursorReset {
		payload["cursor-reset"] = true
	}
	c.JSON(http.StatusOK, payload)
}

func tailLogFiles(files []string, limit int, fallbackLatest int64) (logReadResult, error) {
	result := logReadResult{
		lines:  []string{},
		latest: fallbackLatest,
	}
	for i := len(files) - 1; i >= 0; i-- {
		remaining := 0
		if limit > 0 {
			remaining = limit - len(result.lines)
			if remaining <= 0 {
				break
			}
		}
		read, errRead := readTailLogLines(files[i], remaining)
		if errRead != nil {
			if errors.Is(errRead, os.ErrNotExist) {
				continue
			}
			return logReadResult{}, errRead
		}
		if len(read.lines) == 0 {
			continue
		}
		result.lines = append(append([]string{}, read.lines...), result.lines...)
		if read.latest > result.latest {
			result.latest = read.latest
		}
	}
	nextCursor, errCursor := cursorForLatestLogFile(files, result.latest)
	if errCursor != nil {
		return logReadResult{}, errCursor
	}
	result.nextCursor = nextCursor
	return result, nil
}

func readTailLogLines(path string, limit int) (completeLogRead, error) {
	boundary, errBoundary := completeLogBoundary(path)
	if errBoundary != nil {
		return completeLogRead{}, errBoundary
	}
	if boundary == 0 {
		return completeLogRead{lines: []string{}}, nil
	}
	start, errStart := tailStartOffset(path, boundary, limit)
	if errStart != nil {
		return completeLogRead{}, errStart
	}
	return readCompleteLogLines(path, start, boundary, limit)
}

func tailStartOffset(path string, boundary int64, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return 0, errOpen
	}
	defer func() {
		_ = file.Close()
	}()
	buf := make([]byte, 32*1024)
	pos := boundary
	lineBreaks := 0
	for pos > 0 {
		chunk := minInt64(int64(len(buf)), pos)
		pos -= chunk
		n, errRead := file.ReadAt(buf[:chunk], pos)
		if errRead != nil && errRead != io.EOF {
			return 0, errRead
		}
		if n <= 0 {
			continue
		}
		data := buf[:n]
		for len(data) > 0 {
			idx := bytes.LastIndexByte(data, '\n')
			if idx < 0 {
				break
			}
			lineBreaks++
			if lineBreaks > limit {
				return pos + int64(idx) + 1, nil
			}
			data = data[:idx]
		}
	}
	return 0, nil
}

func cursorForLatestLogFile(files []string, latest int64) (string, error) {
	for i := len(files) - 1; i >= 0; i-- {
		boundary, errBoundary := completeLogBoundary(files[i])
		if errBoundary != nil {
			if errors.Is(errBoundary, os.ErrNotExist) {
				continue
			}
			return "", errBoundary
		}
		cursor, errCursor := newLogCursor(files[i], boundary, latest)
		if errCursor != nil {
			if errors.Is(errCursor, os.ErrNotExist) {
				continue
			}
			return "", errCursor
		}
		return cursor, nil
	}
	return "", nil
}

func readLogFilesFromCursor(logDir string, files []string, raw string, limit int) (logReadResult, bool, error) {
	cursor, errDecode := decodeLogCursor(raw)
	if errDecode != nil {
		return logReadResult{lines: []string{}}, true, nil
	}
	result := logReadResult{
		lines:      []string{},
		latest:     cursor.LatestTimestamp,
		nextCursor: raw,
	}
	if _, errPath := safeLogFilePath(logDir, cursor.File); errPath != nil {
		return result, true, nil
	}
	startIndex, found, errLocate := locateLogCursorFile(files, cursor)
	if errLocate != nil {
		return result, false, errLocate
	}
	if !found {
		return result, true, nil
	}

	currentCursorPath := files[startIndex]
	currentCursorOffset := cursor.Offset
	advanced := false
	for i := startIndex; i < len(files); i++ {
		remaining := 0
		if limit > 0 {
			remaining = limit - len(result.lines)
			if remaining <= 0 {
				break
			}
		}
		offset := int64(0)
		if i == startIndex {
			offset = cursor.Offset
		}
		read, errRead := readCompleteLogLines(files[i], offset, -1, remaining)
		if errRead != nil {
			if errors.Is(errRead, os.ErrNotExist) {
				return result, true, nil
			}
			return result, false, errRead
		}
		if len(read.lines) > 0 {
			result.lines = append(result.lines, read.lines...)
			if read.latest > result.latest {
				result.latest = read.latest
			}
			currentCursorPath = files[i]
			currentCursorOffset = read.endOffset
			advanced = true
		}
		if read.hitLimit {
			break
		}
	}
	if !advanced {
		return result, false, nil
	}

	nextCursor, errCursor := newLogCursor(currentCursorPath, currentCursorOffset, result.latest)
	if errCursor != nil {
		if errors.Is(errCursor, os.ErrNotExist) {
			return result, true, nil
		}
		return result, false, errCursor
	}
	result.nextCursor = nextCursor
	return result, false, nil
}

func locateLogCursorFile(files []string, cursor logCursor) (int, bool, error) {
	nameToIndex := make(map[string]int, len(files))
	for i := range files {
		nameToIndex[filepath.Base(files[i])] = i
	}
	deferEmptyMainMatch := false
	if index, ok := nameToIndex[cursor.File]; ok {
		matches, truncated, errMatch := logFileMatchesCursor(files[index], cursor)
		if errMatch != nil {
			if errors.Is(errMatch, os.ErrNotExist) {
				return 0, false, nil
			}
			return 0, false, errMatch
		}
		if matches && !truncated {
			if shouldDeferEmptyMainCursorToRotated(files, cursor) {
				deferEmptyMainMatch = true
			} else if shouldResetAmbiguousEmptyMainCursor(files, index, cursor) {
				return 0, false, nil
			} else {
				return index, true, nil
			}
		}
	}

	if cursor.File != defaultLogFileName || (cursor.Offset == 0 && cursor.Size == 0 && !deferEmptyMainMatch) {
		return 0, false, nil
	}
	for i := len(files) - 1; i >= 0; i-- {
		if filepath.Base(files[i]) == defaultLogFileName {
			continue
		}
		if cursor.Offset == 0 && cursor.Size == 0 && !logFileChangedAfterCursor(files[i], cursor) {
			continue
		}
		matches, truncated, errMatch := logFileMatchesCursor(files[i], cursor)
		if errMatch != nil {
			if errors.Is(errMatch, os.ErrNotExist) {
				continue
			}
			return 0, false, errMatch
		}
		if truncated {
			continue
		}
		if matches {
			return i, true, nil
		}
	}
	return 0, false, nil
}

func shouldDeferEmptyMainCursorToRotated(files []string, cursor logCursor) bool {
	if cursor.File != defaultLogFileName || cursor.Offset != 0 || cursor.Size != 0 {
		return false
	}
	for i := range files {
		if filepath.Base(files[i]) == defaultLogFileName {
			continue
		}
		if logFileChangedAfterCursor(files[i], cursor) {
			return true
		}
	}
	return false
}

func shouldResetAmbiguousEmptyMainCursor(files []string, mainIndex int, cursor logCursor) bool {
	if cursor.File != defaultLogFileName || cursor.Offset != 0 || cursor.Size != 0 {
		return false
	}
	info, errStat := os.Stat(files[mainIndex])
	if errStat != nil || info.IsDir() {
		return false
	}
	if info.Size() == cursor.Size && info.ModTime().UnixNano() == cursorModTimeUnixNano(cursor) {
		return false
	}
	for i := range files {
		if i == mainIndex || filepath.Base(files[i]) == defaultLogFileName {
			continue
		}
		rotatedInfo, errRotated := os.Stat(files[i])
		if errRotated != nil || rotatedInfo.IsDir() || rotatedInfo.Size() == 0 {
			continue
		}
		if !logFileChangedAfterCursor(files[i], cursor) {
			return true
		}
	}
	return false
}

func logFileChangedAfterCursor(path string, cursor logCursor) bool {
	info, errStat := os.Stat(path)
	if errStat != nil || info.IsDir() || info.Size() == 0 {
		return false
	}
	return info.ModTime().UnixNano() > cursorModTimeUnixNano(cursor)
}

func logFileMatchesCursor(path string, cursor logCursor) (bool, bool, error) {
	info, errStat := os.Stat(path)
	if errStat != nil {
		return false, false, errStat
	}
	if info.IsDir() {
		return false, false, fmt.Errorf("invalid log file")
	}
	if info.Size() < cursor.Offset {
		return false, true, nil
	}
	boundary := cursorFingerprintBoundary(cursor)
	if info.Size() < boundary {
		return false, true, nil
	}
	fingerprint, errFingerprint := logFileFingerprint(path, boundary)
	if errFingerprint != nil {
		return false, false, errFingerprint
	}
	return fingerprint == cursor.Fingerprint, false, nil
}

func encodeLogCursor(cursor logCursor) (string, error) {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeLogCursor(raw string) (logCursor, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return logCursor{}, fmt.Errorf("empty cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		data, err = base64.URLEncoding.DecodeString(value)
	}
	if err != nil {
		return logCursor{}, fmt.Errorf("invalid cursor encoding")
	}
	var cursor logCursor
	if errUnmarshal := json.Unmarshal(data, &cursor); errUnmarshal != nil {
		return logCursor{}, fmt.Errorf("invalid cursor payload")
	}
	if errValidate := validateLogCursor(cursor); errValidate != nil {
		return logCursor{}, errValidate
	}
	return cursor, nil
}

func validateLogCursor(cursor logCursor) error {
	if cursor.Version != logCursorVersion {
		return fmt.Errorf("unsupported cursor version")
	}
	if !isAllowedLogCursorFile(cursor.File) {
		return fmt.Errorf("invalid cursor file")
	}
	if cursor.Offset < 0 || cursor.Size < 0 || cursor.ModTime < 0 || cursor.LatestTimestamp < 0 {
		return fmt.Errorf("invalid cursor position")
	}
	if strings.TrimSpace(cursor.Fingerprint) == "" {
		return fmt.Errorf("invalid cursor fingerprint")
	}
	return nil
}

func isAllowedLogCursorFile(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, `/\`) {
		return false
	}
	if filepath.Base(name) != name {
		return false
	}
	return name == defaultLogFileName || isRotatedLogFile(name)
}

func safeLogFilePath(logDir, name string) (string, error) {
	if !isAllowedLogCursorFile(name) {
		return "", fmt.Errorf("invalid log file")
	}
	dirAbs, errAbs := filepath.Abs(logDir)
	if errAbs != nil {
		return "", fmt.Errorf("resolve log directory: %w", errAbs)
	}
	dirAbs = filepath.Clean(dirAbs)
	fullPath := filepath.Clean(filepath.Join(dirAbs, name))
	rel, errRel := filepath.Rel(dirAbs, fullPath)
	if errRel != nil {
		return "", fmt.Errorf("resolve log file: %w", errRel)
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", fmt.Errorf("invalid log file")
	}
	return fullPath, nil
}

func newLogCursor(path string, offset, latest int64) (string, error) {
	info, errStat := os.Stat(path)
	if errStat != nil {
		return "", errStat
	}
	if info.IsDir() {
		return "", fmt.Errorf("invalid log file")
	}
	if offset < 0 || offset > info.Size() {
		return "", fmt.Errorf("invalid cursor offset")
	}
	fingerprintCursor := logCursor{
		Offset: offset,
		Size:   info.Size(),
	}
	fingerprint, errFingerprint := logFileFingerprint(path, cursorFingerprintBoundary(fingerprintCursor))
	if errFingerprint != nil {
		return "", errFingerprint
	}
	return encodeLogCursor(logCursor{
		Version:         logCursorVersion,
		File:            filepath.Base(path),
		Offset:          offset,
		Size:            info.Size(),
		ModTime:         info.ModTime().Unix(),
		ModTimeUnixNano: info.ModTime().UnixNano(),
		LatestTimestamp: latest,
		Fingerprint:     fingerprint,
	})
}

func cursorFingerprintBoundary(cursor logCursor) int64 {
	if cursor.Offset == 0 && cursor.Size > 0 {
		return cursor.Size
	}
	return cursor.Offset
}

func cursorModTimeUnixNano(cursor logCursor) int64 {
	if cursor.ModTimeUnixNano > 0 {
		return cursor.ModTimeUnixNano
	}
	return cursor.ModTime * int64(time.Second)
}

func logFileFingerprint(path string, boundary int64) (string, error) {
	if boundary < 0 {
		return "", fmt.Errorf("invalid fingerprint boundary")
	}
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return "", errOpen
	}
	defer func() {
		_ = file.Close()
	}()
	info, errStat := file.Stat()
	if errStat != nil {
		return "", errStat
	}
	if info.IsDir() {
		return "", fmt.Errorf("invalid log file")
	}
	if boundary > info.Size() {
		return "", fmt.Errorf("invalid fingerprint boundary")
	}

	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "log-cursor-v1:%d:", boundary)
	firstLen := minInt64(boundary, logCursorFingerprintMax)
	if errRead := writeFileRange(hash, file, 0, firstLen); errRead != nil {
		return "", errRead
	}
	tailLen := minInt64(boundary, logCursorFingerprintMax)
	tailStart := boundary - tailLen
	_, _ = fmt.Fprintf(hash, ":%d:", tailStart)
	if errRead := writeFileRange(hash, file, tailStart, tailLen); errRead != nil {
		return "", errRead
	}
	sum := hash.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(sum[:12]), nil
}

func writeFileRange(dst io.Writer, file *os.File, start, length int64) error {
	if length <= 0 {
		return nil
	}
	buf := make([]byte, 32*1024)
	pos := start
	remaining := length
	for remaining > 0 {
		chunk := minInt64(int64(len(buf)), remaining)
		n, errRead := file.ReadAt(buf[:chunk], pos)
		if n > 0 {
			if _, errWrite := dst.Write(buf[:n]); errWrite != nil {
				return errWrite
			}
			pos += int64(n)
			remaining -= int64(n)
		}
		if errRead != nil {
			if errRead == io.EOF && remaining == 0 {
				return nil
			}
			return errRead
		}
	}
	return nil
}

func readCompleteLogLines(path string, offset, maxOffset int64, limit int) (completeLogRead, error) {
	if offset < 0 {
		return completeLogRead{}, fmt.Errorf("invalid log offset")
	}
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return completeLogRead{}, errOpen
	}
	defer func() {
		_ = file.Close()
	}()
	info, errStat := file.Stat()
	if errStat != nil {
		return completeLogRead{}, errStat
	}
	if info.IsDir() {
		return completeLogRead{}, fmt.Errorf("invalid log file")
	}
	size := info.Size()
	if maxOffset < 0 || maxOffset > size {
		maxOffset = size
	}
	if offset > maxOffset {
		return completeLogRead{}, fmt.Errorf("invalid log offset")
	}

	reader := io.NewSectionReader(file, offset, maxOffset-offset)
	result := completeLogRead{
		lines:     []string{},
		endOffset: offset,
	}
	currentOffset := offset
	buf := make([]byte, 32*1024)
	line := make([]byte, 0, logScannerInitialBuffer)
	for {
		n, errRead := reader.Read(buf)
		if n > 0 {
			data := buf[:n]
			for len(data) > 0 {
				idx := bytes.IndexByte(data, '\n')
				if idx < 0 {
					if len(line)+len(data) > logScannerMaxBuffer {
						return completeLogRead{}, fmt.Errorf("log line exceeds %d bytes", logScannerMaxBuffer)
					}
					line = append(line, data...)
					currentOffset += int64(len(data))
					break
				}

				segment := data[:idx]
				if len(line)+len(segment) > logScannerMaxBuffer {
					return completeLogRead{}, fmt.Errorf("log line exceeds %d bytes", logScannerMaxBuffer)
				}
				line = append(line, segment...)
				currentOffset += int64(idx) + 1
				text := strings.TrimRight(string(line), "\r")
				result.lines = append(result.lines, text)
				result.endOffset = currentOffset
				if ts := parseTimestamp(text); ts > result.latest {
					result.latest = ts
				}
				line = line[:0]
				if limit > 0 && len(result.lines) >= limit {
					result.hitLimit = true
					return result, nil
				}
				data = data[idx+1:]
			}
		}
		if errRead == io.EOF {
			break
		}
		if errRead != nil {
			return completeLogRead{}, errRead
		}
	}
	return result, nil
}

func completeLogBoundary(path string) (int64, error) {
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return 0, errOpen
	}
	defer func() {
		_ = file.Close()
	}()
	info, errStat := file.Stat()
	if errStat != nil {
		return 0, errStat
	}
	if info.IsDir() {
		return 0, fmt.Errorf("invalid log file")
	}
	size := info.Size()
	if size == 0 {
		return 0, nil
	}
	buf := make([]byte, 32*1024)
	pos := size
	for pos > 0 {
		chunk := minInt64(int64(len(buf)), pos)
		pos -= chunk
		n, errRead := file.ReadAt(buf[:chunk], pos)
		if errRead != nil && errRead != io.EOF {
			return 0, errRead
		}
		if n <= 0 {
			continue
		}
		if idx := bytes.LastIndexByte(buf[:n], '\n'); idx >= 0 {
			return pos + int64(idx) + 1, nil
		}
	}
	return 0, nil
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func parseCutoff(raw string) int64 {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0
	}
	ts, err := strconv.ParseInt(value, 10, 64)
	if err != nil || ts <= 0 {
		return 0
	}
	return ts
}

func parseLimit(raw string) (int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("must be a positive integer")
	}
	if limit <= 0 {
		return 0, fmt.Errorf("must be greater than zero")
	}
	return limit, nil
}

func parseTimestamp(line string) int64 {
	if strings.HasPrefix(line, "[") {
		line = line[1:]
	}
	if len(line) < 19 {
		return 0
	}
	candidate := line[:19]
	t, err := time.ParseInLocation("2006-01-02 15:04:05", candidate, time.Local)
	if err != nil {
		return 0
	}
	return t.Unix()
}

func isRotatedLogFile(name string) bool {
	if _, ok := rotationOrder(name); ok {
		return true
	}
	return false
}

func rotationOrder(name string) (int64, bool) {
	if order, ok := numericRotationOrder(name); ok {
		return order, true
	}
	if order, ok := timestampRotationOrder(name); ok {
		return order, true
	}
	return 0, false
}

func numericRotationOrder(name string) (int64, bool) {
	if !strings.HasPrefix(name, defaultLogFileName+".") {
		return 0, false
	}
	suffix := strings.TrimPrefix(name, defaultLogFileName+".")
	if suffix == "" {
		return 0, false
	}
	n, err := strconv.Atoi(suffix)
	if err != nil {
		return 0, false
	}
	return int64(n), true
}

func timestampRotationOrder(name string) (int64, bool) {
	ext := filepath.Ext(defaultLogFileName)
	base := strings.TrimSuffix(defaultLogFileName, ext)
	if base == "" {
		return 0, false
	}
	prefix := base + "-"
	if !strings.HasPrefix(name, prefix) {
		return 0, false
	}
	clean := strings.TrimPrefix(name, prefix)
	if strings.HasSuffix(clean, ".gz") {
		clean = strings.TrimSuffix(clean, ".gz")
	}
	if ext != "" {
		if !strings.HasSuffix(clean, ext) {
			return 0, false
		}
		clean = strings.TrimSuffix(clean, ext)
	}
	if clean == "" {
		return 0, false
	}
	if idx := strings.IndexByte(clean, '.'); idx != -1 {
		clean = clean[:idx]
	}
	parsed, err := time.ParseInLocation("2006-01-02T15-04-05", clean, time.Local)
	if err != nil {
		return 0, false
	}
	return math.MaxInt64 - parsed.Unix(), true
}
