package management

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"gopkg.in/yaml.v3"
)

const (
	requestLogUsageDefaultTimezone   = "Asia/Shanghai"
	requestLogUsageMaxConfigBytes    = 1 << 20
	requestLogUsageMaxAuditBytes     = 256 << 20
	requestLogUsageMaxHistoryBytes   = 512 << 20
	requestLogUsageMaxAuditLineBytes = 8 << 20
	requestLogUsageMaxHistoryFiles   = 512
	requestLogUsageMaxPendingFiles   = 1_000_000
	requestLogUsageMaxParseErrors    = 100
)

type requestLogUsageUploaderConfig struct {
	LogsRoot string `yaml:"logs-root"`
	WorkDir  string `yaml:"work-dir"`
	Timezone string `yaml:"timezone"`
}

type requestLogUsageSettings struct {
	logsRoot string
	workDir  string
	timezone string
	location *time.Location
}

type requestLogUsageAuditModel struct {
	SourceCount int64 `json:"source_count"`
	SourceBytes int64 `json:"source_bytes"`
}

type requestLogUsageAuditKey struct {
	SourceCount int64                                `json:"source_count"`
	SourceBytes int64                                `json:"source_bytes"`
	Models      map[string]requestLogUsageAuditModel `json:"models"`
}

type requestLogUsageAuditRecord struct {
	Status     string                             `json:"status"`
	Hour       time.Time                          `json:"hour"`
	Provider   string                             `json:"provider"`
	KeyNames   map[string]requestLogUsageAuditKey `json:"key_names"`
	JSONLBytes int64                              `json:"jsonl_bytes"`
}

type requestLogUsageBatch struct {
	hour        time.Time
	provider    string
	statusRank  int
	sourceCount int64
	sourceBytes int64
	jsonlBytes  int64
	keyNames    map[string]requestLogUsageAuditKey
}

type requestLogUsageModel struct {
	Model       string `json:"model"`
	SourceCount int64  `json:"source_count"`
	SourceBytes int64  `json:"source_bytes"`
}

type requestLogUsageProviderUsage struct {
	Provider    string `json:"provider"`
	SourceCount int64  `json:"source_count"`
	SourceBytes int64  `json:"source_bytes"`
}

type requestLogUsageKey struct {
	KeyName      string                         `json:"key_name"`
	DisplayName  string                         `json:"display_name"`
	Configured   bool                           `json:"configured"`
	SourceCount  int64                          `json:"source_count"`
	SourceBytes  int64                          `json:"source_bytes"`
	PendingCount int64                          `json:"pending_count"`
	PendingBytes int64                          `json:"pending_bytes"`
	BatchCount   int                            `json:"batch_count"`
	FirstHour    string                         `json:"first_hour"`
	LastHour     string                         `json:"last_hour"`
	Providers    []requestLogUsageProviderUsage `json:"providers"`
	Models       []requestLogUsageModel         `json:"models"`
}

type requestLogUsageHourKey struct {
	KeyName     string                 `json:"key_name"`
	SourceCount int64                  `json:"source_count"`
	SourceBytes int64                  `json:"source_bytes"`
	Models      []requestLogUsageModel `json:"models"`
}

type requestLogUsageHour struct {
	Hour        string                   `json:"hour"`
	Provider    string                   `json:"provider"`
	SourceCount int64                    `json:"source_count"`
	SourceBytes int64                    `json:"source_bytes"`
	JSONLBytes  int64                    `json:"jsonl_bytes"`
	Keys        []requestLogUsageHourKey `json:"keys"`
}

type requestLogUsageDayKey struct {
	KeyName     string                         `json:"key_name"`
	SourceCount int64                          `json:"source_count"`
	SourceBytes int64                          `json:"source_bytes"`
	Providers   []requestLogUsageProviderUsage `json:"providers"`
	Models      []requestLogUsageModel         `json:"models"`
}

type requestLogUsageDay struct {
	Date        string                         `json:"date"`
	SourceCount int64                          `json:"source_count"`
	SourceBytes int64                          `json:"source_bytes"`
	JSONLBytes  int64                          `json:"jsonl_bytes"`
	Providers   []requestLogUsageProviderUsage `json:"providers"`
	Keys        []requestLogUsageDayKey        `json:"keys"`
}

type requestLogUsageTotals struct {
	SourceCount  int64 `json:"source_count"`
	SourceBytes  int64 `json:"source_bytes"`
	JSONLBytes   int64 `json:"jsonl_bytes"`
	PendingCount int64 `json:"pending_count"`
	PendingBytes int64 `json:"pending_bytes"`
	BatchCount   int   `json:"batch_count"`
	KeyCount     int   `json:"key_count"`
}

type requestLogUsageProviderSummary struct {
	Provider    string `json:"provider"`
	SourceCount int64  `json:"source_count"`
	SourceBytes int64  `json:"source_bytes"`
	JSONLBytes  int64  `json:"jsonl_bytes"`
	BatchCount  int    `json:"batch_count"`
}

type requestLogUsageResponse struct {
	Timezone           string                           `json:"timezone"`
	SourceBytesMeaning string                           `json:"source_bytes_meaning"`
	JSONLBytesMeaning  string                           `json:"jsonl_bytes_meaning"`
	Totals             requestLogUsageTotals            `json:"totals"`
	Providers          []requestLogUsageProviderSummary `json:"providers"`
	Keys               []requestLogUsageKey             `json:"keys"`
	Days               []requestLogUsageDay             `json:"days"`
	Hours              []requestLogUsageHour            `json:"hours"`
	ParseErrors        []string                         `json:"parse_errors"`
}

type requestLogUsageAggregate struct {
	configured   bool
	displayName  string
	sourceCount  int64
	sourceBytes  int64
	pendingCount int64
	pendingBytes int64
	batchCount   int
	firstHour    time.Time
	lastHour     time.Time
	providers    map[string]requestLogUsageAuditModel
	models       map[string]requestLogUsageAuditModel
}

type requestLogUsagePending struct {
	count int64
	bytes int64
}

type requestLogUsageDayAggregate struct {
	sourceCount  int64
	sourceBytes  int64
	jsonlBytes   int64
	providers    map[string]requestLogUsageAuditModel
	keys         map[string]requestLogUsageAuditKey
	keyProviders map[string]map[string]requestLogUsageAuditModel
}

type requestLogUsageConfiguredKey struct {
	keyName     string
	displayName string
}

type requestLogUsageHistoryFile struct {
	name string
	size int64
}

type requestLogUsageErrors struct {
	items     []string
	truncated bool
}

func (e *requestLogUsageErrors) add(message string) {
	message = strings.TrimSpace(message)
	if message == "" || e.truncated {
		return
	}
	if len(e.items) >= requestLogUsageMaxParseErrors {
		e.items = append(e.items, "additional parse errors omitted")
		e.truncated = true
		return
	}
	e.items = append(e.items, message)
}

// GetRequestLogUsage returns persisted per-client request-log volume and pending local files.
func (h *Handler) GetRequestLogUsage(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}

	h.mu.Lock()
	cfg := h.cfg
	configFilePath := h.configFilePath
	if cfg == nil {
		h.mu.Unlock()
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}
	authDir := cfg.AuthDir
	configuredAPIKeys := append([]string(nil), cfg.APIKeys...)
	configuredNames := append([]string(nil), cfg.APIKeyNames...)
	h.mu.Unlock()
	configuredKeys := requestLogUsageConfiguredKeys(configuredAPIKeys, configuredNames)

	c.Header("Cache-Control", "no-store")
	parseErrors := &requestLogUsageErrors{items: make([]string, 0)}
	settings := resolveRequestLogUsageSettings(configFilePath, authDir, parseErrors)
	historyBatches := make(map[string]requestLogUsageBatch)
	activeBatches := make(map[string]requestLogUsageBatch)
	readRequestLogUsageHistory(settings.workDir, settings.location, historyBatches, parseErrors)
	readRequestLogUsageAudit(filepath.Join(settings.workDir, "audit.jsonl"), "audit.jsonl", settings.location, activeBatches, parseErrors)
	for hourKey, batch := range activeBatches {
		historyBatches[hourKey] = batch
	}

	pending := readRequestLogUsagePending(settings.logsRoot, parseErrors)
	response := buildRequestLogUsageResponse(settings, configuredKeys, historyBatches, pending, parseErrors.items)
	c.JSON(http.StatusOK, response)
}

func resolveRequestLogUsageSettings(configFilePath, authDir string, parseErrors *requestLogUsageErrors) requestLogUsageSettings {
	configDir := ""
	if strings.TrimSpace(configFilePath) != "" {
		absoluteConfigPath, errAbsolute := filepath.Abs(configFilePath)
		if errAbsolute != nil {
			parseErrors.add(fmt.Sprintf("resolve server config path: %v", errAbsolute))
		} else {
			configDir = filepath.Dir(absoluteConfigPath)
		}
	}

	resolvedAuthDir, errAuthDir := util.ResolveAuthDir(strings.TrimSpace(authDir))
	if errAuthDir != nil {
		parseErrors.add(fmt.Sprintf("resolve auth directory: %v", errAuthDir))
		resolvedAuthDir = strings.TrimSpace(authDir)
	}
	if strings.TrimSpace(resolvedAuthDir) == "" {
		resolvedAuthDir = "."
	}
	if !filepath.IsAbs(resolvedAuthDir) {
		absoluteAuthDir, errAbsolute := filepath.Abs(resolvedAuthDir)
		if errAbsolute != nil {
			parseErrors.add(fmt.Sprintf("resolve auth directory: %v", errAbsolute))
		} else {
			resolvedAuthDir = absoluteAuthDir
		}
	}
	resolvedAuthDir = filepath.Clean(resolvedAuthDir)

	settings := requestLogUsageSettings{
		logsRoot: filepath.Join(resolvedAuthDir, "logs", "keys"),
		workDir:  filepath.Join(resolvedAuthDir, "log-uploader"),
		timezone: requestLogUsageDefaultTimezone,
	}
	if configDir != "" {
		uploaderConfigPath := filepath.Join(configDir, "log-uploader.yaml")
		loaded, exists := readRequestLogUsageUploaderConfig(uploaderConfigPath, parseErrors)
		if exists {
			if value := strings.TrimSpace(loaded.LogsRoot); value != "" {
				settings.logsRoot = resolveRequestLogUsagePath(configDir, value, "logs-root", settings.logsRoot, parseErrors)
			}
			if value := strings.TrimSpace(loaded.WorkDir); value != "" {
				settings.workDir = resolveRequestLogUsagePath(configDir, value, "work-dir", settings.workDir, parseErrors)
			}
			if value := strings.TrimSpace(loaded.Timezone); value != "" {
				settings.timezone = value
			}
		}
	}

	location, errLocation := time.LoadLocation(settings.timezone)
	if errLocation != nil {
		parseErrors.add(fmt.Sprintf("invalid log uploader timezone %q", settings.timezone))
		settings.timezone = requestLogUsageDefaultTimezone
		location, errLocation = time.LoadLocation(settings.timezone)
		if errLocation != nil {
			settings.timezone = "UTC"
			location = time.UTC
		}
	}
	settings.location = location
	return settings
}

func readRequestLogUsageUploaderConfig(path string, parseErrors *requestLogUsageErrors) (requestLogUsageUploaderConfig, bool) {
	info, errStat := os.Lstat(path)
	if errors.Is(errStat, os.ErrNotExist) {
		return requestLogUsageUploaderConfig{}, false
	}
	if errStat != nil {
		parseErrors.add(fmt.Sprintf("read log-uploader.yaml metadata: %v", errStat))
		return requestLogUsageUploaderConfig{}, false
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		parseErrors.add("log-uploader.yaml is not a regular file")
		return requestLogUsageUploaderConfig{}, false
	}
	if info.Size() > requestLogUsageMaxConfigBytes {
		parseErrors.add("log-uploader.yaml exceeds the safe size limit")
		return requestLogUsageUploaderConfig{}, false
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		parseErrors.add(fmt.Sprintf("read log-uploader.yaml: %v", errRead))
		return requestLogUsageUploaderConfig{}, false
	}
	var cfg requestLogUsageUploaderConfig
	if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
		parseErrors.add(fmt.Sprintf("parse log-uploader.yaml: %v", errUnmarshal))
		return requestLogUsageUploaderConfig{}, false
	}
	return cfg, true
}

func resolveRequestLogUsagePath(baseDir, value, field, fallback string, parseErrors *requestLogUsageErrors) string {
	path := filepath.Clean(value)
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	absolutePath, errAbsolute := filepath.Abs(path)
	if errAbsolute != nil {
		parseErrors.add(fmt.Sprintf("resolve log uploader %s: %v", field, errAbsolute))
		return fallback
	}
	absolutePath = filepath.Clean(absolutePath)
	if absolutePath == filepath.VolumeName(absolutePath)+string(os.PathSeparator) {
		parseErrors.add(fmt.Sprintf("log uploader %s cannot be a filesystem root", field))
		return fallback
	}
	return absolutePath
}

func readRequestLogUsageHistory(workDir string, location *time.Location, batches map[string]requestLogUsageBatch, parseErrors *requestLogUsageErrors) {
	historyDir := filepath.Join(workDir, "history")
	entries, errRead := os.ReadDir(historyDir)
	if errors.Is(errRead, os.ErrNotExist) {
		return
	}
	if errRead != nil {
		parseErrors.add(fmt.Sprintf("read history directory: %v", errRead))
		return
	}

	files := make([]requestLogUsageHistoryFile, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		info, errInfo := entry.Info()
		if errInfo != nil {
			parseErrors.add(fmt.Sprintf("read history/%s metadata: %v", entry.Name(), errInfo))
			continue
		}
		if info.Mode().IsRegular() {
			files = append(files, requestLogUsageHistoryFile{name: entry.Name(), size: info.Size()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	if len(files) > requestLogUsageMaxHistoryFiles {
		parseErrors.add(fmt.Sprintf("history contains more than %d JSONL files; remaining files were ignored", requestLogUsageMaxHistoryFiles))
		files = files[:requestLogUsageMaxHistoryFiles]
	}
	var totalBytes int64
	for _, file := range files {
		label := filepath.ToSlash(filepath.Join("history", file.name))
		if file.size > requestLogUsageMaxAuditBytes {
			parseErrors.add(fmt.Sprintf("%s exceeds the safe size limit", label))
			continue
		}
		if file.size < 0 || totalBytes > requestLogUsageMaxHistoryBytes-file.size {
			parseErrors.add(fmt.Sprintf("history exceeds the %d-byte total scan limit; remaining files were ignored", requestLogUsageMaxHistoryBytes))
			break
		}
		totalBytes += file.size
		readRequestLogUsageAudit(filepath.Join(historyDir, file.name), label, location, batches, parseErrors)
	}
}

func readRequestLogUsageAudit(path, label string, location *time.Location, batches map[string]requestLogUsageBatch, parseErrors *requestLogUsageErrors) {
	info, errStat := os.Lstat(path)
	if errors.Is(errStat, os.ErrNotExist) {
		return
	}
	if errStat != nil {
		parseErrors.add(fmt.Sprintf("read %s metadata: %v", label, errStat))
		return
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		parseErrors.add(fmt.Sprintf("%s is not a regular file", label))
		return
	}
	if info.Size() > requestLogUsageMaxAuditBytes {
		parseErrors.add(fmt.Sprintf("%s exceeds the safe size limit", label))
		return
	}

	file, errOpen := os.Open(path)
	if errOpen != nil {
		parseErrors.add(fmt.Sprintf("open %s: %v", label, errOpen))
		return
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			parseErrors.add(fmt.Sprintf("close %s: %v", label, errClose))
		}
	}()

	endsWithNewline := true
	if info.Size() > 0 {
		last := []byte{0}
		if _, errReadAt := file.ReadAt(last, info.Size()-1); errReadAt == nil {
			endsWithNewline = last[0] == '\n'
		}
		if _, errSeek := file.Seek(0, io.SeekStart); errSeek != nil {
			parseErrors.add(fmt.Sprintf("seek %s: %v", label, errSeek))
			return
		}
	}

	scanner := bufio.NewScanner(io.LimitReader(file, info.Size()))
	scanner.Buffer(make([]byte, 64*1024), requestLogUsageMaxAuditLineBytes)
	lineNumber := 0
	deferredLastError := ""
	for scanner.Scan() {
		if deferredLastError != "" {
			parseErrors.add(deferredLastError)
			deferredLastError = ""
		}
		lineNumber++
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var record requestLogUsageAuditRecord
		if errUnmarshal := json.Unmarshal(line, &record); errUnmarshal != nil {
			deferredLastError = fmt.Sprintf("parse %s line %d: %v", label, lineNumber, errUnmarshal)
			continue
		}
		batch, include, errNormalize := normalizeRequestLogUsageBatch(record, location)
		if errNormalize != nil {
			deferredLastError = fmt.Sprintf("parse %s line %d: %v", label, lineNumber, errNormalize)
			continue
		}
		if !include {
			continue
		}
		hourKey := batch.hour.Format(time.RFC3339) + ":" + batch.provider
		previous, exists := batches[hourKey]
		if !exists {
			batches[hourKey] = batch
		} else if batch.statusRank > previous.statusRank {
			// Higher-status record replaces lower-status (e.g. "uploaded" supersedes "uploaded_cleanup_pending").
			batches[hourKey] = batch
		} else if batch.statusRank == previous.statusRank {
			// Same hour and same status means multiple parts of a split archive;
			// accumulate source counts and key-level details instead of overwriting.
			previous.sourceCount += batch.sourceCount
			previous.sourceBytes += batch.sourceBytes
			previous.jsonlBytes += batch.jsonlBytes
			for keyName, key := range batch.keyNames {
				existing, found := previous.keyNames[keyName]
				if !found {
					previous.keyNames[keyName] = key
					continue
				}
				existing.SourceCount += key.SourceCount
				existing.SourceBytes += key.SourceBytes
				for modelName, model := range key.Models {
					em := existing.Models[modelName]
					em.SourceCount += model.SourceCount
					em.SourceBytes += model.SourceBytes
					existing.Models[modelName] = em
				}
				previous.keyNames[keyName] = existing
			}
			batches[hourKey] = previous
		}
	}
	if deferredLastError != "" && endsWithNewline {
		parseErrors.add(deferredLastError)
	}
	if errScan := scanner.Err(); errScan != nil {
		parseErrors.add(fmt.Sprintf("scan %s: %v", label, errScan))
	}
}

func normalizeRequestLogUsageBatch(record requestLogUsageAuditRecord, location *time.Location) (requestLogUsageBatch, bool, error) {
	statusRank := 0
	switch strings.TrimSpace(record.Status) {
	case "uploaded":
		statusRank = 2
	case "uploaded_cleanup_pending":
		statusRank = 1
	default:
		return requestLogUsageBatch{}, false, nil
	}
	if record.Hour.IsZero() {
		return requestLogUsageBatch{}, false, errors.New("uploaded audit record has no hour")
	}
	if len(record.KeyNames) == 0 {
		return requestLogUsageBatch{}, false, nil
	}

	localHour := record.Hour.In(location)
	localHour = time.Date(localHour.Year(), localHour.Month(), localHour.Day(), localHour.Hour(), 0, 0, 0, location)
	provider := strings.TrimSpace(record.Provider)
	if provider == "" {
		provider = "codex"
	}
	batch := requestLogUsageBatch{
		hour:       localHour,
		provider:   provider,
		statusRank: statusRank,
		jsonlBytes: record.JSONLBytes,
		keyNames:   make(map[string]requestLogUsageAuditKey),
	}
	for rawKeyName, rawKey := range record.KeyNames {
		keyName := strings.TrimSpace(rawKeyName)
		if keyName == "" {
			return requestLogUsageBatch{}, false, errors.New("uploaded audit record has an empty key_name")
		}
		if rawKey.SourceCount < 0 || rawKey.SourceBytes < 0 {
			return requestLogUsageBatch{}, false, fmt.Errorf("key_name %q has negative totals", keyName)
		}
		key := batch.keyNames[keyName]
		if !safeRequestLogUsageAdd(&key.SourceCount, rawKey.SourceCount) || !safeRequestLogUsageAdd(&key.SourceBytes, rawKey.SourceBytes) {
			return requestLogUsageBatch{}, false, fmt.Errorf("key_name %q totals overflow", keyName)
		}
		if key.Models == nil {
			key.Models = make(map[string]requestLogUsageAuditModel)
		}
		for rawModelName, rawModel := range rawKey.Models {
			modelName := strings.TrimSpace(rawModelName)
			if modelName == "" || rawModel.SourceCount < 0 || rawModel.SourceBytes < 0 {
				return requestLogUsageBatch{}, false, fmt.Errorf("key_name %q has invalid model totals", keyName)
			}
			model := key.Models[modelName]
			if !safeRequestLogUsageAdd(&model.SourceCount, rawModel.SourceCount) || !safeRequestLogUsageAdd(&model.SourceBytes, rawModel.SourceBytes) {
				return requestLogUsageBatch{}, false, fmt.Errorf("key_name %q model %q totals overflow", keyName, modelName)
			}
			key.Models[modelName] = model
		}
		batch.keyNames[keyName] = key
	}
	for _, key := range batch.keyNames {
		if !safeRequestLogUsageAdd(&batch.sourceCount, key.SourceCount) || !safeRequestLogUsageAdd(&batch.sourceBytes, key.SourceBytes) {
			return requestLogUsageBatch{}, false, errors.New("uploaded audit record totals overflow")
		}
	}
	return batch, true, nil
}

func readRequestLogUsagePending(logsRoot string, parseErrors *requestLogUsageErrors) map[string]requestLogUsagePending {
	pending := make(map[string]requestLogUsagePending)
	entries, errRead := os.ReadDir(logsRoot)
	if errors.Is(errRead, os.ErrNotExist) {
		return pending
	}
	if errRead != nil {
		parseErrors.add(fmt.Sprintf("read pending log root: %v", errRead))
		return pending
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	filesSeen := 0
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			continue
		}
		keyName := strings.TrimSpace(entry.Name())
		if keyName == "" {
			continue
		}
		keyDir := filepath.Join(logsRoot, entry.Name())
		files, errFiles := os.ReadDir(keyDir)
		if errors.Is(errFiles, os.ErrNotExist) {
			continue
		}
		if errFiles != nil {
			parseErrors.add(fmt.Sprintf("read pending key directory %q: %v", keyName, errFiles))
			continue
		}
		for _, file := range files {
			if filesSeen >= requestLogUsageMaxPendingFiles {
				parseErrors.add(fmt.Sprintf("pending logs exceed the %d file safety limit", requestLogUsageMaxPendingFiles))
				return pending
			}
			if file.Type()&os.ModeSymlink != 0 || file.IsDir() || filepath.Ext(file.Name()) != ".log" {
				continue
			}
			info, errInfo := file.Info()
			if errors.Is(errInfo, os.ErrNotExist) {
				continue
			}
			if errInfo != nil {
				parseErrors.add(fmt.Sprintf("stat pending log for %q: %v", keyName, errInfo))
				continue
			}
			if !info.Mode().IsRegular() {
				continue
			}
			if info.Size() <= 0 {
				continue
			}
			filesSeen++
			value := pending[keyName]
			if !safeRequestLogUsageAdd(&value.count, 1) || !safeRequestLogUsageAdd(&value.bytes, info.Size()) {
				parseErrors.add(fmt.Sprintf("pending totals overflow for key_name %q", keyName))
				continue
			}
			pending[keyName] = value
		}
	}
	return pending
}

func buildRequestLogUsageResponse(settings requestLogUsageSettings, configuredKeys []requestLogUsageConfiguredKey, batches map[string]requestLogUsageBatch, pending map[string]requestLogUsagePending, parseErrors []string) requestLogUsageResponse {
	aggregates := make(map[string]*requestLogUsageAggregate)
	for _, configuredKey := range configuredKeys {
		aggregate := ensureRequestLogUsageAggregate(aggregates, configuredKey.keyName)
		aggregate.configured = true
		if aggregate.displayName == "" {
			aggregate.displayName = configuredKey.displayName
		}
	}

	hourKeys := make([]string, 0, len(batches))
	for hourKey := range batches {
		hourKeys = append(hourKeys, hourKey)
	}
	sort.Strings(hourKeys)
	hours := make([]requestLogUsageHour, 0, len(hourKeys))
	dailyAggregates := make(map[string]*requestLogUsageDayAggregate)
	location := settings.location
	if location == nil {
		location = time.UTC
	}
	totals := requestLogUsageTotals{BatchCount: len(hourKeys)}
	for _, hourKey := range hourKeys {
		batch := batches[hourKey]
		provider := batch.provider
		if provider == "" {
			provider = "codex"
		}
		hour := requestLogUsageHour{
			Hour:        batch.hour.Format(time.RFC3339),
			Provider:    provider,
			SourceCount: batch.sourceCount,
			SourceBytes: batch.sourceBytes,
			JSONLBytes:  batch.jsonlBytes,
			Keys:        make([]requestLogUsageHourKey, 0, len(batch.keyNames)),
		}
		_ = safeRequestLogUsageAdd(&totals.SourceCount, batch.sourceCount)
		_ = safeRequestLogUsageAdd(&totals.SourceBytes, batch.sourceBytes)
		_ = safeRequestLogUsageAdd(&totals.JSONLBytes, batch.jsonlBytes)
		dayKey := batch.hour.In(location).Format(time.DateOnly)
		dayAggregate := dailyAggregates[dayKey]
		if dayAggregate == nil {
			dayAggregate = &requestLogUsageDayAggregate{
				providers:    make(map[string]requestLogUsageAuditModel),
				keys:         make(map[string]requestLogUsageAuditKey),
				keyProviders: make(map[string]map[string]requestLogUsageAuditModel),
			}
			dailyAggregates[dayKey] = dayAggregate
		}
		_ = safeRequestLogUsageAdd(&dayAggregate.sourceCount, batch.sourceCount)
		_ = safeRequestLogUsageAdd(&dayAggregate.sourceBytes, batch.sourceBytes)
		_ = safeRequestLogUsageAdd(&dayAggregate.jsonlBytes, batch.jsonlBytes)
		dayProvider := dayAggregate.providers[provider]
		_ = safeRequestLogUsageAdd(&dayProvider.SourceCount, batch.sourceCount)
		_ = safeRequestLogUsageAdd(&dayProvider.SourceBytes, batch.sourceBytes)
		dayAggregate.providers[provider] = dayProvider

		batchKeyNames := make([]string, 0, len(batch.keyNames))
		for keyName := range batch.keyNames {
			batchKeyNames = append(batchKeyNames, keyName)
		}
		sort.Strings(batchKeyNames)
		for _, keyName := range batchKeyNames {
			key := batch.keyNames[keyName]
			hour.Keys = append(hour.Keys, requestLogUsageHourKey{
				KeyName:     keyName,
				SourceCount: key.SourceCount,
				SourceBytes: key.SourceBytes,
				Models:      requestLogUsageModels(key.Models),
			})
			aggregate := ensureRequestLogUsageAggregate(aggregates, keyName)
			_ = safeRequestLogUsageAdd(&aggregate.sourceCount, key.SourceCount)
			_ = safeRequestLogUsageAdd(&aggregate.sourceBytes, key.SourceBytes)
			aggregate.batchCount++
			if aggregate.firstHour.IsZero() || batch.hour.Before(aggregate.firstHour) {
				aggregate.firstHour = batch.hour
			}
			if aggregate.lastHour.IsZero() || batch.hour.After(aggregate.lastHour) {
				aggregate.lastHour = batch.hour
			}
			keyProvider := aggregate.providers[provider]
			_ = safeRequestLogUsageAdd(&keyProvider.SourceCount, key.SourceCount)
			_ = safeRequestLogUsageAdd(&keyProvider.SourceBytes, key.SourceBytes)
			aggregate.providers[provider] = keyProvider
			for modelName, model := range key.Models {
				current := aggregate.models[modelName]
				_ = safeRequestLogUsageAdd(&current.SourceCount, model.SourceCount)
				_ = safeRequestLogUsageAdd(&current.SourceBytes, model.SourceBytes)
				aggregate.models[modelName] = current
			}

			dailyKey := dayAggregate.keys[keyName]
			_ = safeRequestLogUsageAdd(&dailyKey.SourceCount, key.SourceCount)
			_ = safeRequestLogUsageAdd(&dailyKey.SourceBytes, key.SourceBytes)
			if dailyKey.Models == nil {
				dailyKey.Models = make(map[string]requestLogUsageAuditModel)
			}
			for modelName, model := range key.Models {
				current := dailyKey.Models[modelName]
				_ = safeRequestLogUsageAdd(&current.SourceCount, model.SourceCount)
				_ = safeRequestLogUsageAdd(&current.SourceBytes, model.SourceBytes)
				dailyKey.Models[modelName] = current
			}
			dayAggregate.keys[keyName] = dailyKey
			dayKeyProviders := dayAggregate.keyProviders[keyName]
			if dayKeyProviders == nil {
				dayKeyProviders = make(map[string]requestLogUsageAuditModel)
				dayAggregate.keyProviders[keyName] = dayKeyProviders
			}
			dayKeyProvider := dayKeyProviders[provider]
			_ = safeRequestLogUsageAdd(&dayKeyProvider.SourceCount, key.SourceCount)
			_ = safeRequestLogUsageAdd(&dayKeyProvider.SourceBytes, key.SourceBytes)
			dayKeyProviders[provider] = dayKeyProvider
		}
		hours = append(hours, hour)
	}

	dayKeys := make([]string, 0, len(dailyAggregates))
	for dayKey := range dailyAggregates {
		dayKeys = append(dayKeys, dayKey)
	}
	sort.Strings(dayKeys)
	days := make([]requestLogUsageDay, 0, len(dayKeys))
	for _, dayKey := range dayKeys {
		aggregate := dailyAggregates[dayKey]
		day := requestLogUsageDay{
			Date:        dayKey,
			SourceCount: aggregate.sourceCount,
			SourceBytes: aggregate.sourceBytes,
			JSONLBytes:  aggregate.jsonlBytes,
			Providers:   requestLogUsageProviderUsages(aggregate.providers),
			Keys:        make([]requestLogUsageDayKey, 0, len(aggregate.keys)),
		}
		keyNames := make([]string, 0, len(aggregate.keys))
		for keyName := range aggregate.keys {
			keyNames = append(keyNames, keyName)
		}
		sort.Strings(keyNames)
		for _, keyName := range keyNames {
			key := aggregate.keys[keyName]
			day.Keys = append(day.Keys, requestLogUsageDayKey{
				KeyName:     keyName,
				SourceCount: key.SourceCount,
				SourceBytes: key.SourceBytes,
				Providers:   requestLogUsageProviderUsages(aggregate.keyProviders[keyName]),
				Models:      requestLogUsageModels(key.Models),
			})
		}
		days = append(days, day)
	}

	for keyName, value := range pending {
		aggregate := ensureRequestLogUsageAggregate(aggregates, keyName)
		aggregate.pendingCount = value.count
		aggregate.pendingBytes = value.bytes
		_ = safeRequestLogUsageAdd(&totals.PendingCount, value.count)
		_ = safeRequestLogUsageAdd(&totals.PendingBytes, value.bytes)
	}

	keyNames := make([]string, 0, len(aggregates))
	for keyName := range aggregates {
		keyNames = append(keyNames, keyName)
	}
	sort.Strings(keyNames)
	keys := make([]requestLogUsageKey, 0, len(keyNames))
	for _, keyName := range keyNames {
		aggregate := aggregates[keyName]
		key := requestLogUsageKey{
			KeyName:      keyName,
			DisplayName:  keyName,
			Configured:   aggregate.configured,
			SourceCount:  aggregate.sourceCount,
			SourceBytes:  aggregate.sourceBytes,
			PendingCount: aggregate.pendingCount,
			PendingBytes: aggregate.pendingBytes,
			BatchCount:   aggregate.batchCount,
			Providers:    requestLogUsageProviderUsages(aggregate.providers),
			Models:       requestLogUsageModels(aggregate.models),
		}
		if aggregate.displayName != "" {
			key.DisplayName = aggregate.displayName
		}
		if !aggregate.firstHour.IsZero() {
			key.FirstHour = aggregate.firstHour.Format(time.RFC3339)
			key.LastHour = aggregate.lastHour.Format(time.RFC3339)
		}
		keys = append(keys, key)
	}
	totals.KeyCount = len(keys)

	// Aggregate per-provider statistics.
	providerAggregates := make(map[string]*requestLogUsageProviderSummary)
	for _, hourKey := range hourKeys {
		batch := batches[hourKey]
		provider := batch.provider
		if provider == "" {
			provider = "codex"
		}
		summary := providerAggregates[provider]
		if summary == nil {
			summary = &requestLogUsageProviderSummary{Provider: provider}
			providerAggregates[provider] = summary
		}
		_ = safeRequestLogUsageAdd(&summary.SourceCount, batch.sourceCount)
		_ = safeRequestLogUsageAdd(&summary.SourceBytes, batch.sourceBytes)
		_ = safeRequestLogUsageAdd(&summary.JSONLBytes, batch.jsonlBytes)
		summary.BatchCount++
	}
	providerNames := make([]string, 0, len(providerAggregates))
	for name := range providerAggregates {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)
	providers := make([]requestLogUsageProviderSummary, 0, len(providerNames))
	for _, name := range providerNames {
		providers = append(providers, *providerAggregates[name])
	}

	if parseErrors == nil {
		parseErrors = make([]string, 0)
	}
	return requestLogUsageResponse{
		Timezone:           settings.timezone,
		SourceBytesMeaning: "complete raw .log file bytes before JSONL conversion and compression",
		JSONLBytesMeaning:  "normalized JSONL bytes after conversion, before zstd compression (actual content uploaded to TOS)",
		Totals:             totals,
		Providers:          providers,
		Keys:               keys,
		Days:               days,
		Hours:              hours,
		ParseErrors:        parseErrors,
	}
}

func requestLogUsageConfiguredKeys(apiKeys, names []string) []requestLogUsageConfiguredKey {
	configured := make([]requestLogUsageConfiguredKey, 0, len(apiKeys))
	for index, apiKey := range apiKeys {
		apiKey = strings.TrimSpace(apiKey)
		if apiKey == "" {
			continue
		}
		displayName := ""
		if index < len(names) {
			displayName = strings.TrimSpace(names[index])
		}
		keyName := logging.SanitizeAPIKeyName(displayName)
		if keyName == "" {
			keyName = logging.APIKeyLogDirectory(apiKey)
			displayName = keyName
		}
		if keyName == "" {
			continue
		}
		configured = append(configured, requestLogUsageConfiguredKey{keyName: keyName, displayName: displayName})
	}
	return configured
}

func ensureRequestLogUsageAggregate(aggregates map[string]*requestLogUsageAggregate, keyName string) *requestLogUsageAggregate {
	aggregate := aggregates[keyName]
	if aggregate == nil {
		aggregate = &requestLogUsageAggregate{
			providers: make(map[string]requestLogUsageAuditModel),
			models:    make(map[string]requestLogUsageAuditModel),
		}
		aggregates[keyName] = aggregate
	}
	return aggregate
}

func requestLogUsageProviderUsages(providers map[string]requestLogUsageAuditModel) []requestLogUsageProviderUsage {
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]requestLogUsageProviderUsage, 0, len(names))
	for _, name := range names {
		usage := providers[name]
		out = append(out, requestLogUsageProviderUsage{Provider: name, SourceCount: usage.SourceCount, SourceBytes: usage.SourceBytes})
	}
	return out
}

func requestLogUsageModels(models map[string]requestLogUsageAuditModel) []requestLogUsageModel {
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]requestLogUsageModel, 0, len(names))
	for _, name := range names {
		model := models[name]
		out = append(out, requestLogUsageModel{Model: name, SourceCount: model.SourceCount, SourceBytes: model.SourceBytes})
	}
	return out
}

func safeRequestLogUsageAdd(target *int64, value int64) bool {
	if value < 0 || *target > math.MaxInt64-value {
		return false
	}
	*target += value
	return true
}
