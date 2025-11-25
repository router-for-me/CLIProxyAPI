package management

import (
	"bufio"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

const (
	defaultLogFileName      = "main.log"
	logScannerInitialBuffer = 64 * 1024
	logScannerMaxBuffer     = 8 * 1024 * 1024
)

// GetLogs returns log lines with optional incremental loading.
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

	files, err := h.collectLogFiles(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			cutoff := parseCutoff(c.Query("after"))
			c.JSON(http.StatusOK, gin.H{
				"lines":            []string{},
				"line-count":       0,
				"latest-timestamp": cutoff,
			})
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
	acc := newLogAccumulator(cutoff, limit)
	for i := range files {
		if errProcess := acc.consumeFile(files[i]); errProcess != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to read log file %s: %v", files[i], errProcess)})
			return
		}
	}

	lines, total, latest := acc.result()
	if latest == 0 || latest < cutoff {
		latest = cutoff
	}
	c.JSON(http.StatusOK, gin.H{
		"lines":            lines,
		"line-count":       total,
		"latest-timestamp": latest,
	})
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
	if base := util.WritablePath(); base != "" {
		return filepath.Join(base, "logs")
	}
	if h.configFilePath != "" {
		dir := filepath.Dir(h.configFilePath)
		if dir != "" && dir != "." {
			return filepath.Join(dir, "logs")
		}
	}
	return "logs"
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
