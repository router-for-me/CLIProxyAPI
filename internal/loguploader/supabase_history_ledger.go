package loguploader

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

const (
	maxSupabaseHistoryAuditBytes     int64 = 256 << 20
	maxSupabaseHistoryTotalBytes     int64 = 512 << 20
	maxSupabaseHistoryAuditLineBytes       = 8 << 20
	maxSupabaseHistoryFiles                = 512
)

type supabaseHistoryLedger struct {
	Records []auditRecord
	Summary supabaseHistoryLedgerSummary
}

type supabaseHistoryLedgerSummary struct {
	DuplicateRecords int   `json:"duplicate_records"`
	TruncatedTails   int   `json:"truncated_tails"`
	SourceCount      int64 `json:"source_count"`
	SourceBytes      int64 `json:"source_bytes"`
	JSONLBytes       int64 `json:"jsonl_bytes"`
	CompressedBytes  int64 `json:"compressed_bytes"`
}

func readSupabaseHistoryLedger(workDir string, location *time.Location) (supabaseHistoryLedger, error) {
	if location == nil {
		return supabaseHistoryLedger{}, fmt.Errorf("history ledger timezone is unavailable")
	}
	files, errFiles := supabaseHistoryLedgerFiles(workDir)
	if errFiles != nil {
		return supabaseHistoryLedger{}, errFiles
	}

	ledger := supabaseHistoryLedger{}
	recordsByObject := make(map[string]auditRecord)
	for _, file := range files {
		records, truncatedTail, errRead := readSupabaseHistoryAuditFile(file.path, file.active, file.size, location)
		if errRead != nil {
			return supabaseHistoryLedger{}, errRead
		}
		if truncatedTail {
			ledger.Summary.TruncatedTails++
		}
		for _, record := range records {
			previous, duplicate := recordsByObject[record.ObjectKey]
			if duplicate {
				merged, compatible := reconcileSupabaseHistoryRecords(previous, record)
				if !compatible {
					return supabaseHistoryLedger{}, fmt.Errorf("history ledger contains conflicting successful records")
				}
				recordsByObject[record.ObjectKey] = merged
				ledger.Summary.DuplicateRecords++
				continue
			}
			recordsByObject[record.ObjectKey] = record
		}
	}

	ledger.Records = make([]auditRecord, 0, len(recordsByObject))
	for _, record := range recordsByObject {
		var errAdd error
		ledger.Summary.SourceCount, errAdd = addSafeJSONInteger(ledger.Summary.SourceCount, int64(record.SourceCount))
		if errAdd != nil {
			return supabaseHistoryLedger{}, fmt.Errorf("history ledger source_count total exceeds the safe integer range")
		}
		ledger.Summary.SourceBytes, errAdd = addSafeJSONInteger(ledger.Summary.SourceBytes, record.SourceBytes)
		if errAdd != nil {
			return supabaseHistoryLedger{}, fmt.Errorf("history ledger source_bytes total exceeds the safe integer range")
		}
		ledger.Summary.JSONLBytes, errAdd = addSafeJSONInteger(ledger.Summary.JSONLBytes, record.JSONLBytes)
		if errAdd != nil {
			return supabaseHistoryLedger{}, fmt.Errorf("history ledger jsonl_bytes total exceeds the safe integer range")
		}
		ledger.Summary.CompressedBytes, errAdd = addSafeJSONInteger(ledger.Summary.CompressedBytes, record.CompressedBytes)
		if errAdd != nil {
			return supabaseHistoryLedger{}, fmt.Errorf("history ledger compressed_bytes total exceeds the safe integer range")
		}
		ledger.Records = append(ledger.Records, record)
	}
	sort.Slice(ledger.Records, func(i, j int) bool {
		if !ledger.Records[i].Hour.Equal(ledger.Records[j].Hour) {
			return ledger.Records[i].Hour.Before(ledger.Records[j].Hour)
		}
		if ledger.Records[i].Provider != ledger.Records[j].Provider {
			return ledger.Records[i].Provider < ledger.Records[j].Provider
		}
		return ledger.Records[i].ObjectKey < ledger.Records[j].ObjectKey
	})
	return ledger, nil
}

type supabaseHistoryAuditFile struct {
	path   string
	active bool
	size   int64
}

func supabaseHistoryLedgerFiles(workDir string) ([]supabaseHistoryAuditFile, error) {
	files := make([]supabaseHistoryAuditFile, 0)
	historyDir := filepath.Join(workDir, "history")
	historyInfo, errHistoryInfo := os.Lstat(historyDir)
	if errHistoryInfo == nil {
		if historyInfo.Mode()&os.ModeSymlink != 0 || !historyInfo.IsDir() {
			return nil, fmt.Errorf("history ledger directory is not a safe directory")
		}
		entries, errReadDir := os.ReadDir(historyDir)
		if errReadDir != nil {
			return nil, fmt.Errorf("read history ledger directory")
		}
		for _, entry := range entries {
			if filepath.Ext(entry.Name()) != ".jsonl" {
				continue
			}
			path := filepath.Join(historyDir, entry.Name())
			info, errInfo := os.Lstat(path)
			if errInfo != nil {
				return nil, fmt.Errorf("read history ledger file metadata")
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("history ledger contains a symbolic link")
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("history ledger contains a non-regular JSONL file")
			}
			files = append(files, supabaseHistoryAuditFile{path: path, size: info.Size()})
		}
	} else if !errors.Is(errHistoryInfo, os.ErrNotExist) {
		return nil, fmt.Errorf("read history ledger directory metadata")
	}
	if len(files) > maxSupabaseHistoryFiles {
		return nil, fmt.Errorf("history ledger contains more than %d JSONL files", maxSupabaseHistoryFiles)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })

	activePath := filepath.Join(workDir, "audit.jsonl")
	activeInfo, errActiveInfo := os.Lstat(activePath)
	if errActiveInfo == nil {
		if activeInfo.Mode()&os.ModeSymlink != 0 || !activeInfo.Mode().IsRegular() {
			return nil, fmt.Errorf("active history ledger is not a safe regular file")
		}
		files = append(files, supabaseHistoryAuditFile{path: activePath, active: true, size: activeInfo.Size()})
	} else if !errors.Is(errActiveInfo, os.ErrNotExist) {
		return nil, fmt.Errorf("read active history ledger metadata")
	}

	var totalBytes int64
	for _, file := range files {
		if file.size < 0 || file.size > maxSupabaseHistoryAuditBytes {
			return nil, fmt.Errorf("history ledger file exceeds the safe size limit")
		}
		if totalBytes > maxSupabaseHistoryTotalBytes-file.size {
			return nil, fmt.Errorf("history ledger exceeds the safe total size limit")
		}
		totalBytes += file.size
	}
	return files, nil
}

func readSupabaseHistoryAuditFile(path string, active bool, expectedSize int64, location *time.Location) (records []auditRecord, truncatedTail bool, readErr error) {
	pathInfo, errPathInfo := os.Lstat(path)
	if errPathInfo != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() || pathInfo.Size() != expectedSize {
		return nil, false, fmt.Errorf("history ledger file changed after preflight")
	}
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return nil, false, fmt.Errorf("open history ledger file")
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			readErr = errors.Join(readErr, fmt.Errorf("close history ledger file"))
		}
	}()
	info, errStat := file.Stat()
	if errStat != nil {
		return nil, false, fmt.Errorf("stat history ledger file")
	}
	if !os.SameFile(pathInfo, info) || info.Size() != expectedSize || info.Size() > maxSupabaseHistoryAuditBytes {
		return nil, false, fmt.Errorf("history ledger file changed after preflight")
	}
	endsWithNewline := true
	if info.Size() > 0 {
		last := []byte{0}
		if _, errReadAt := file.ReadAt(last, info.Size()-1); errReadAt != nil {
			return nil, false, fmt.Errorf("inspect history ledger final line")
		}
		endsWithNewline = last[0] == '\n'
		if !endsWithNewline && !active {
			return nil, false, fmt.Errorf("history ledger has an incomplete final line")
		}
		if _, errSeek := file.Seek(0, io.SeekStart); errSeek != nil {
			return nil, false, fmt.Errorf("seek history ledger file")
		}
	}

	scanner := bufio.NewScanner(io.LimitReader(file, maxSupabaseHistoryAuditBytes+1))
	scanner.Buffer(make([]byte, 64*1024), maxSupabaseHistoryAuditLineBytes)
	var pendingLine []byte
	lineNumber := 0
	processPending := func() error {
		if pendingLine == nil {
			return nil
		}
		record, include, errNormalize := normalizeSupabaseHistoryAuditLine(pendingLine, location)
		pendingLine = nil
		if errNormalize != nil {
			return fmt.Errorf("history ledger line %d is invalid", lineNumber)
		}
		if include {
			records = append(records, record)
		}
		return nil
	}
	for scanner.Scan() {
		if errPending := processPending(); errPending != nil {
			return nil, false, errPending
		}
		lineNumber++
		pendingLine = append(pendingLine[:0], scanner.Bytes()...)
	}
	if errScan := scanner.Err(); errScan != nil {
		return nil, false, fmt.Errorf("scan history ledger file")
	}
	finalInfo, errFinalStat := file.Stat()
	if errFinalStat != nil || finalInfo.Size() != info.Size() || !finalInfo.ModTime().Equal(info.ModTime()) {
		return nil, false, fmt.Errorf("history ledger file changed during preflight")
	}
	if !endsWithNewline && active {
		return records, pendingLine != nil, nil
	}
	if errPending := processPending(); errPending != nil {
		return nil, false, errPending
	}
	return records, false, nil
}

func normalizeSupabaseHistoryAuditLine(raw []byte, location *time.Location) (auditRecord, bool, error) {
	if strings.TrimSpace(string(raw)) == "" {
		return auditRecord{}, false, nil
	}
	var record auditRecord
	if errUnmarshal := jsonUnmarshalAuditRecord(raw, &record); errUnmarshal != nil {
		return auditRecord{}, false, errUnmarshal
	}
	if !isSupabaseHistorySuccessStatus(strings.TrimSpace(record.Status)) {
		return auditRecord{}, false, nil
	}
	if record.Hour.IsZero() || !record.Hour.Equal(record.Hour.Truncate(time.Hour)) {
		return auditRecord{}, false, fmt.Errorf("invalid hour")
	}
	record.Hour = record.Hour.In(location)
	provider := strings.TrimSpace(record.Provider)
	if provider != "" && !isSupabaseProvider(provider) {
		return auditRecord{}, false, fmt.Errorf("invalid provider")
	}
	record.Provider = provider
	if errObjectKey := validateSupabaseObjectKey(record.ObjectKey); errObjectKey != nil {
		return auditRecord{}, false, fmt.Errorf("invalid object identity")
	}
	if record.SupabaseEventID != "" && !supabaseEventIDPattern.MatchString(record.SupabaseEventID) {
		return auditRecord{}, false, fmt.Errorf("invalid managed event identity")
	}
	if record.SourceCount < 0 || int64(record.SourceCount) > maxSafeJSONInteger {
		return auditRecord{}, false, fmt.Errorf("invalid source_count")
	}
	for _, value := range []int64{record.SourceBytes, record.JSONLBytes, record.CompressedBytes} {
		if value < 0 || value > maxSafeJSONInteger {
			return auditRecord{}, false, fmt.Errorf("invalid byte total")
		}
	}
	if len(record.KeyNames) == 0 || len(record.KeyNames) > maxSupabaseUsageRows {
		return auditRecord{}, false, fmt.Errorf("invalid usage rows")
	}
	var sourceCount, sourceBytes int64
	for keyName, key := range record.KeyNames {
		if errKeyName := validateSupabaseKeyName(keyName); errKeyName != nil {
			return auditRecord{}, false, fmt.Errorf("invalid key name")
		}
		if key.SourceCount < 0 || int64(key.SourceCount) > maxSafeJSONInteger || key.SourceBytes < 0 || key.SourceBytes > maxSafeJSONInteger {
			return auditRecord{}, false, fmt.Errorf("invalid key totals")
		}
		var modelSourceCount, modelSourceBytes int64
		var errAdd error
		sourceCount, errAdd = addSafeJSONInteger(sourceCount, int64(key.SourceCount))
		if errAdd != nil {
			return auditRecord{}, false, fmt.Errorf("key source_count totals overflow")
		}
		sourceBytes, errAdd = addSafeJSONInteger(sourceBytes, key.SourceBytes)
		if errAdd != nil {
			return auditRecord{}, false, fmt.Errorf("key source_bytes totals overflow")
		}
		for modelName, model := range key.Models {
			if strings.TrimSpace(modelName) == "" || model.SourceCount < 0 || model.SourceBytes < 0 ||
				int64(model.SourceCount) > maxSafeJSONInteger || model.SourceBytes > maxSafeJSONInteger {
				return auditRecord{}, false, fmt.Errorf("invalid model totals")
			}
			if record.Provider != "" {
				continue
			}
			modelSourceCount, errAdd = addSafeJSONInteger(modelSourceCount, int64(model.SourceCount))
			if errAdd != nil {
				return auditRecord{}, false, fmt.Errorf("model source_count totals overflow")
			}
			modelSourceBytes, errAdd = addSafeJSONInteger(modelSourceBytes, model.SourceBytes)
			if errAdd != nil {
				return auditRecord{}, false, fmt.Errorf("model source_bytes totals overflow")
			}
		}
		if record.Provider == "" && (modelSourceCount != int64(key.SourceCount) || modelSourceBytes != key.SourceBytes) {
			return auditRecord{}, false, fmt.Errorf("model totals do not match key totals")
		}
	}
	if sourceCount != int64(record.SourceCount) || sourceBytes != record.SourceBytes {
		return auditRecord{}, false, fmt.Errorf("usage totals do not match batch totals")
	}
	return record, true, nil
}

func jsonUnmarshalAuditRecord(raw []byte, record *auditRecord) error {
	if errUnmarshal := json.Unmarshal(raw, record); errUnmarshal != nil {
		return fmt.Errorf("invalid JSON")
	}
	return nil
}

func isSupabaseHistorySuccessStatus(status string) bool {
	switch status {
	case "uploaded", "uploaded_cleanup_pending", "uploaded_delete_pending", "uploaded_archive_delete_pending":
		return true
	default:
		return false
	}
}

func reconcileSupabaseHistoryRecords(previous, current auditRecord) (auditRecord, bool) {
	previousComparable := previous
	currentComparable := current
	previousComparable.Timestamp = time.Time{}
	currentComparable.Timestamp = time.Time{}
	previousComparable.Status = ""
	currentComparable.Status = ""
	previousComparable.ArchivePath = ""
	currentComparable.ArchivePath = ""
	previousComparable.DeletedSources = 0
	currentComparable.DeletedSources = 0
	previousComparable.Error = ""
	currentComparable.Error = ""
	previousEventID := previousComparable.SupabaseEventID
	currentEventID := currentComparable.SupabaseEventID
	previousComparable.SupabaseEventID = ""
	currentComparable.SupabaseEventID = ""
	if !reflect.DeepEqual(previousComparable, currentComparable) {
		return auditRecord{}, false
	}
	if previousEventID != "" && currentEventID != "" && previousEventID != currentEventID {
		return auditRecord{}, false
	}
	if currentEventID != "" {
		previous.SupabaseEventID = currentEventID
	}
	return previous, true
}
