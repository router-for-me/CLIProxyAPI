package management

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logqa"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

const (
	logQAMaxSessionScan   = 200_000
	logQADefaultLimit     = 50
	logQAMaxLimit         = 200
	logQAMaxDownloadFiles = 200
	logQAMaxDownloadBytes = 200 << 20 // 200 MiB total
)

// logQARunGate serializes management-triggered QA starts in this process.
var logQARunGate struct {
	mu      sync.Mutex
	started time.Time
}

type logQAFileConfig struct {
	WorkDir  string `yaml:"work-dir"`
	LogsRoot string `yaml:"logs-root"`
	Timezone string `yaml:"timezone"`
}

type logQASettings struct {
	workDir    string
	logsRoot   string
	timezone   string
	found      bool
	configPath string
}

func (h *Handler) resolveLogQASettings() logQASettings {
	settings := logQASettings{
		timezone: "Asia/Shanghai",
	}
	configDir := filepath.Dir(h.configFilePath)
	candidates := []string{
		filepath.Join(configDir, "log-qa.yaml"),
		"/CLIProxyAPI/log-qa.yaml",
	}
	if h.cfg != nil && strings.TrimSpace(h.cfg.AuthDir) != "" {
		candidates = append(candidates, filepath.Join(h.cfg.AuthDir, "log-qa.yaml"))
	}
	for _, path := range candidates {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cfg logQAFileConfig
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			continue
		}
		settings.found = true
		settings.configPath = path
		base := filepath.Dir(path)
		if strings.TrimSpace(cfg.WorkDir) != "" {
			settings.workDir = resolveMaybeRelative(base, cfg.WorkDir)
		}
		if strings.TrimSpace(cfg.LogsRoot) != "" {
			settings.logsRoot = resolveMaybeRelative(base, cfg.LogsRoot)
		}
		if strings.TrimSpace(cfg.Timezone) != "" {
			settings.timezone = cfg.Timezone
		}
		break
	}
	// Fallbacks when yaml missing or paths empty: Docker layout first, then auth-dir.
	if strings.TrimSpace(settings.workDir) == "" {
		if st, err := os.Stat("/CLIProxyAPI/logs/log-qa"); err == nil && st.IsDir() {
			settings.workDir = "/CLIProxyAPI/logs/log-qa"
		} else if h.cfg != nil && strings.TrimSpace(h.cfg.AuthDir) != "" {
			settings.workDir = filepath.Join(h.cfg.AuthDir, "log-qa")
		} else {
			settings.workDir = "/CLIProxyAPI/logs/log-qa"
		}
	}
	if strings.TrimSpace(settings.logsRoot) == "" {
		if st, err := os.Stat("/CLIProxyAPI/logs/keys"); err == nil && st.IsDir() {
			settings.logsRoot = "/CLIProxyAPI/logs/keys"
		} else if h.cfg != nil && strings.TrimSpace(h.cfg.AuthDir) != "" {
			settings.logsRoot = filepath.Join(h.cfg.AuthDir, "logs", "keys")
		} else {
			settings.logsRoot = "/CLIProxyAPI/logs/keys"
		}
	}
	return settings
}

func resolveMaybeRelative(base, value string) string {
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(base, value)
}

// GetLogQAStatus returns whether QA reports are available.
func (h *Handler) GetLogQAStatus(c *gin.Context) {
	settings := h.resolveLogQASettings()
	latest, errLatest := logqa.ReadLatestPointer(settings.workDir)
	running := logqa.IsRunning(settings.workDir) || logQARunInFlight()
	status := gin.H{
		"config_found":  settings.found,
		"config_path":   settings.configPath,
		"work_dir":      settings.workDir,
		"logs_root":     settings.logsRoot,
		"timezone":      settings.timezone,
		"latest_run_id": "",
		"has_report":    false,
		"running":       running,
		"message":       "尚无质检报告。请先运行 log-qa 服务或点击「立即质检」。",
	}
	if running {
		status["message"] = "质检进行中…"
	}
	if errLatest == nil && (latest.RunID != "" || latest.Dir != "") {
		status["has_report"] = true
		status["latest_run_id"] = firstNonEmptyStr(latest.RunID, latest.Dir)
		if !running {
			status["message"] = "正常"
		}
	}
	c.JSON(http.StatusOK, status)
}

// PostLogQARun starts a one-shot QA pass asynchronously.
// The button/UI should poll GET /log-qa/status until running becomes false.
func (h *Handler) PostLogQARun(c *gin.Context) {
	settings := h.resolveLogQASettings()
	if logqa.IsRunning(settings.workDir) || !logQATryBeginRun() {
		c.JSON(http.StatusConflict, gin.H{
			"error":   "质检正在进行中，请稍后再试",
			"running": true,
		})
		return
	}

	cfg, errCfg := h.loadLogQAConfig(settings)
	if errCfg != nil {
		logQAEndRun()
		c.JSON(http.StatusBadRequest, gin.H{"error": errCfg.Error(), "running": false})
		return
	}
	// Manual trigger should evaluate what is currently on disk, including recent files.
	cfg.Scan.MinFileAge = 0
	cfg.Scan.SkipCurrentHour = false

	go func() {
		defer logQAEndRun()
		ctx := context.Background()
		service := logqa.NewService(cfg)
		log.WithFields(log.Fields{
			"logs_root": cfg.LogsRoot,
			"work_dir":  cfg.WorkDir,
		}).Info("management-triggered log-qa run started")
		if err := service.RunOnce(ctx); err != nil {
			log.WithError(err).Error("management-triggered log-qa run failed")
			return
		}
		log.Info("management-triggered log-qa run completed")
	}()

	c.JSON(http.StatusAccepted, gin.H{
		"ok":      true,
		"running": true,
		"message": "质检已开始",
	})
}

// GetLogQASessionLogs downloads source .log files for a failed session as a zip.
func (h *Handler) GetLogQASessionLogs(c *gin.Context) {
	settings := h.resolveLogQASettings()
	sessionID := strings.TrimSpace(c.Query("session_id"))
	if sessionID == "" {
		sessionID = strings.TrimSpace(c.Param("session_id"))
	}
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing session_id"})
		return
	}
	if strings.ContainsAny(sessionID, "/\\") || strings.Contains(sessionID, "..") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid session_id"})
		return
	}

	runID := strings.TrimSpace(c.Query("run_id"))
	if runID == "" {
		latest, errLatest := logqa.ReadLatestPointer(settings.workDir)
		if errLatest != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "尚无质检报告"})
			return
		}
		runID = firstNonEmptyStr(latest.Dir, latest.RunID)
	}
	if err := validateLogQARunID(runID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	rec, errFind := findLogQASession(settings.workDir, runID, sessionID)
	if errFind != nil {
		if os.IsNotExist(errFind) {
			c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在或报告缺失"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": errFind.Error()})
		return
	}
	if rec.OK {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅支持下载质检失败会话的日志"})
		return
	}
	if len(rec.SourceFiles) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "该会话没有可下载的源日志路径"})
		return
	}

	logsRootAbs, errAbs := filepath.Abs(settings.logsRoot)
	if errAbs != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("resolve logs-root: %v", errAbs)})
		return
	}
	rootPrefix := logsRootAbs + string(os.PathSeparator)

	type fileEntry struct {
		rel  string
		path string
		size int64
	}
	entries := make([]fileEntry, 0, len(rec.SourceFiles))
	var totalBytes int64
	missing := make([]string, 0)
	for _, rel := range rec.SourceFiles {
		rel = filepath.ToSlash(strings.TrimSpace(rel))
		if rel == "" || strings.Contains(rel, "..") || strings.HasPrefix(rel, "/") {
			continue
		}
		// Expected layout: key_name/file.log
		if strings.Count(rel, "/") != 1 || !strings.HasSuffix(strings.ToLower(rel), ".log") {
			continue
		}
		full := filepath.Clean(filepath.Join(logsRootAbs, filepath.FromSlash(rel)))
		if !strings.HasPrefix(full, rootPrefix) {
			continue
		}
		info, errStat := os.Stat(full)
		if errStat != nil {
			if os.IsNotExist(errStat) {
				missing = append(missing, rel)
			}
			continue
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			continue
		}
		if len(entries) >= logQAMaxDownloadFiles {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": fmt.Sprintf("源日志文件数超过上限 %d", logQAMaxDownloadFiles),
			})
			return
		}
		if totalBytes+info.Size() > logQAMaxDownloadBytes {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": fmt.Sprintf("源日志总大小超过上限 %d bytes", logQAMaxDownloadBytes),
			})
			return
		}
		totalBytes += info.Size()
		entries = append(entries, fileEntry{rel: rel, path: full, size: info.Size()})
	}

	if len(entries) == 0 {
		msg := "源日志已不存在（可能已被 uploader 删除）"
		if len(missing) > 0 {
			msg = fmt.Sprintf("%s：%s", msg, strings.Join(missing, ", "))
		}
		c.JSON(http.StatusNotFound, gin.H{"error": msg, "missing": missing})
		return
	}

	safeName := sanitizeLogQAFilename(sessionID)
	filename := fmt.Sprintf("log-qa-fail-%s.zip", safeName)
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Status(http.StatusOK)

	zw := zip.NewWriter(c.Writer)
	defer func() {
		if err := zw.Close(); err != nil {
			log.WithError(err).Warn("close log-qa session zip failed")
		}
	}()

	// Include a small manifest for troubleshooting context.
	manifest := map[string]any{
		"session_id":    rec.SessionID,
		"run_id":        runID,
		"fail_reasons":  rec.FailReasons,
		"key_names":     rec.KeyNames,
		"source_files":  rec.SourceFiles,
		"included":      make([]string, 0, len(entries)),
		"missing":       missing,
		"prompt_rounds": rec.PromptRounds,
		"tool_calls":    rec.ToolCalls,
	}
	included := make([]string, 0, len(entries))
	for _, entry := range entries {
		included = append(included, entry.rel)
		if err := writeLogQAZipFile(zw, entry.rel, entry.path); err != nil {
			log.WithError(err).WithField("file", entry.rel).Warn("add log-qa zip entry failed")
			return
		}
	}
	manifest["included"] = included
	manifestBytes, errManifest := json.MarshalIndent(manifest, "", "  ")
	if errManifest == nil {
		w, errCreate := zw.Create("qa-manifest.json")
		if errCreate == nil {
			_, _ = w.Write(append(manifestBytes, '\n'))
		}
	}
}

// GetLogQASummary returns the latest or selected run summary.
func (h *Handler) GetLogQASummary(c *gin.Context) {
	settings := h.resolveLogQASettings()
	runID := strings.TrimSpace(c.Query("run_id"))
	summary, err := logqa.ReadSummary(settings.workDir, runID)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusOK, gin.H{
				"has_report": false,
				"message":    "尚无质检报告",
			})
			return
		}
		if strings.Contains(err.Error(), "invalid run_id") {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"has_report": false,
			"message":    err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"has_report": true,
		"summary":    summary,
	})
}

// GetLogQARuns lists recent report run directories.
func (h *Handler) GetLogQARuns(c *gin.Context) {
	settings := h.resolveLogQASettings()
	dir := filepath.Join(settings.workDir, "reports")
	entries, err := os.ReadDir(dir)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"runs": []any{}, "message": "报告目录不存在"})
		return
	}
	type runInfo struct {
		RunID string `json:"run_id"`
	}
	runs := make([]runInfo, 0)
	for _, e := range entries {
		if !e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		if strings.Contains(e.Name(), "..") {
			continue
		}
		runs = append(runs, runInfo{RunID: e.Name()})
	}
	// reverse chronological by name (UTC timestamp format sorts)
	for i, j := 0, len(runs)-1; i < j; i, j = i+1, j-1 {
		runs[i], runs[j] = runs[j], runs[i]
	}
	if len(runs) > 48 {
		runs = runs[:48]
	}
	c.JSON(http.StatusOK, gin.H{"runs": runs})
}

// GetLogQASessions streams session rows from a report with filters.
func (h *Handler) GetLogQASessions(c *gin.Context) {
	settings := h.resolveLogQASettings()
	runID := strings.TrimSpace(c.Query("run_id"))
	if runID == "" {
		latest, err := logqa.ReadLatestPointer(settings.workDir)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"sessions": []any{}, "total": 0, "has_report": false})
			return
		}
		runID = firstNonEmptyStr(latest.Dir, latest.RunID)
	}
	if err := validateLogQARunID(runID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	statusFilter := strings.ToLower(strings.TrimSpace(c.DefaultQuery("status", "all")))
	eligibilityFilter := strings.ToLower(strings.TrimSpace(c.Query("eligibility")))
	reasonFilter := strings.TrimSpace(c.Query("reason"))
	q := strings.ToLower(strings.TrimSpace(c.Query("q")))
	limit := logQADefaultLimit
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > logQAMaxLimit {
		limit = logQAMaxLimit
	}
	offset := 0
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			offset = n
		}
	}

	path := filepath.Join(settings.workDir, "reports", runID, "session_qa.jsonl")
	file, err := os.Open(path)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"sessions": []any{}, "total": 0, "has_report": false, "run_id": runID})
		return
	}
	defer func() { _ = file.Close() }()

	matched := make([]logqa.SessionRecord, 0, limit)
	totalMatched := 0
	scanned := 0
	scanner := bufio.NewScanner(file)
	// large lines possible
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 16*1024*1024)

	for scanner.Scan() {
		scanned++
		if scanned > logQAMaxSessionScan {
			break
		}
		line := scanner.Bytes()
		if len(bytesTrimSpace(line)) == 0 {
			continue
		}
		var rec logqa.SessionRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		// Recompute so older reports (eligibility tied to overall ok) match session-gate.
		rec.UploadEligibility = logqa.ComputeUploadEligibility(rec.SessionID, sessionHasRealID(rec), rec.FailReasons)
		if statusFilter == "pass" && !rec.OK {
			continue
		}
		if statusFilter == "fail" && rec.OK {
			continue
		}
		if eligibilityFilter != "" && eligibilityFilter != "all" && rec.UploadEligibility != eligibilityFilter {
			continue
		}
		if reasonFilter != "" && !sessionHasReason(rec, reasonFilter) {
			continue
		}
		if q != "" && !sessionMatchesQuery(rec, q) {
			continue
		}
		if totalMatched >= offset && len(matched) < limit {
			matched = append(matched, rec)
		}
		totalMatched++
	}

	c.JSON(http.StatusOK, gin.H{
		"has_report": true,
		"run_id":     runID,
		"sessions":   matched,
		"total":      totalMatched,
		"limit":      limit,
		"offset":     offset,
	})
}

func sessionHasRealID(rec logqa.SessionRecord) bool {
	if strings.TrimSpace(rec.SessionID) == "" || strings.HasPrefix(rec.SessionID, "unknown:") {
		return false
	}
	for _, reason := range rec.FailReasons {
		if reason == "empty_session_id" {
			return false
		}
	}
	return true
}

func sessionHasReason(rec logqa.SessionRecord, reason string) bool {
	reason = strings.ToLower(reason)
	for _, r := range rec.FailReasons {
		if strings.Contains(strings.ToLower(r), reason) {
			return true
		}
	}
	// also accept bucket names
	switch reason {
	case "prompt_rounds":
		for _, r := range rec.FailReasons {
			if strings.HasPrefix(r, "prompt_rounds") {
				return true
			}
		}
	case "no_tool_call":
		for _, r := range rec.FailReasons {
			if r == "no_tool_call" {
				return true
			}
		}
	case "duplicate_assistant":
		for _, r := range rec.FailReasons {
			if strings.HasPrefix(r, "duplicate_assistant") {
				return true
			}
		}
	}
	return false
}

func sessionMatchesQuery(rec logqa.SessionRecord, q string) bool {
	if strings.Contains(strings.ToLower(rec.SessionID), q) {
		return true
	}
	if strings.Contains(strings.ToLower(rec.Title), q) {
		return true
	}
	for _, t := range rec.ThreadIDs {
		if strings.Contains(strings.ToLower(t), q) {
			return true
		}
	}
	for _, k := range rec.KeyNames {
		if strings.Contains(strings.ToLower(k), q) {
			return true
		}
	}
	for _, p := range rec.SamplePrompts {
		if strings.Contains(strings.ToLower(p), q) {
			return true
		}
	}
	return false
}

func validateLogQARunID(runID string) error {
	if runID == "" || strings.Contains(runID, "..") || strings.ContainsAny(runID, `/\`) {
		return fmt.Errorf("invalid run_id")
	}
	return nil
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

func logQARunInFlight() bool {
	logQARunGate.mu.Lock()
	defer logQARunGate.mu.Unlock()
	return !logQARunGate.started.IsZero()
}

func logQATryBeginRun() bool {
	logQARunGate.mu.Lock()
	defer logQARunGate.mu.Unlock()
	if !logQARunGate.started.IsZero() {
		// Safety: if the flag stuck for >2h, allow a new start.
		if time.Since(logQARunGate.started) < 2*time.Hour {
			return false
		}
	}
	logQARunGate.started = time.Now()
	return true
}

func logQAEndRun() {
	logQARunGate.mu.Lock()
	defer logQARunGate.mu.Unlock()
	logQARunGate.started = time.Time{}
}

func (h *Handler) loadLogQAConfig(settings logQASettings) (logqa.Config, error) {
	if settings.found && strings.TrimSpace(settings.configPath) != "" {
		cfg, err := logqa.LoadConfig(settings.configPath)
		if err != nil {
			return logqa.Config{}, fmt.Errorf("load log-qa.yaml: %w", err)
		}
		return cfg, nil
	}
	cfg, err := logqa.ConfigFromPaths(settings.logsRoot, settings.workDir, settings.timezone)
	if err != nil {
		return logqa.Config{}, fmt.Errorf("build default log-qa config: %w", err)
	}
	// Manual runs should include recent files; defaults skip hot/current-hour files
	// which is fine for scheduled service, but still useful here as safety.
	return cfg, nil
}

func findLogQASession(workDir, runID, sessionID string) (logqa.SessionRecord, error) {
	path := filepath.Join(workDir, "reports", runID, "session_qa.jsonl")
	file, err := os.Open(path)
	if err != nil {
		return logqa.SessionRecord{}, err
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 16*1024*1024)
	scanned := 0
	for scanner.Scan() {
		scanned++
		if scanned > logQAMaxSessionScan {
			break
		}
		line := scanner.Bytes()
		if len(bytesTrimSpace(line)) == 0 {
			continue
		}
		var rec logqa.SessionRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.SessionID == sessionID {
			return rec, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return logqa.SessionRecord{}, err
	}
	return logqa.SessionRecord{}, os.ErrNotExist
}

func writeLogQAZipFile(zw *zip.Writer, rel, fullPath string) error {
	src, err := os.Open(fullPath)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()

	// Keep key_name/file.log structure inside the archive.
	w, errCreate := zw.Create(filepath.ToSlash(rel))
	if errCreate != nil {
		return errCreate
	}
	_, errCopy := io.Copy(w, src)
	return errCopy
}

func sanitizeLogQAFilename(sessionID string) string {
	var b strings.Builder
	for _, r := range sessionID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return "session"
	}
	if len(out) > 80 {
		return out[:80]
	}
	return out
}
