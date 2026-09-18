// Package usagestats persists a per-request usage record to a local, append-only
// JSON-lines file so historical token/request consumption can be queried by day,
// model, and account. It is a passive subscriber on the existing usage.Manager
// plugin bus; it does not affect request handling.
package usagestats

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

const (
	pluginName    = "usagestats"
	subDir        = "usage-stats"
	retentionDays = 90
	dirPerm       = 0o755
	filePerm      = 0o644

	dateLayout = "2006-01-02"
)

var (
	registerOnce sync.Once

	enabledFlag     atomic.Bool
	dirVal          atomic.Value // string
	lastCleanupDate atomic.Value // string
)

// entry is the on-disk JSON-lines record shape.
type entry struct {
	Timestamp         time.Time                `json:"timestamp"`
	Provider          string                   `json:"provider"`
	ExecutorType      string                   `json:"executor_type"`
	Model             string                   `json:"model"`
	Alias             string                   `json:"alias"`
	Account           string                   `json:"account"`
	AuthIndex         string                   `json:"auth_index,omitempty"`
	AuthType          string                   `json:"auth_type"`
	RequestID         string                   `json:"request_id,omitempty"`
	Failed            bool                     `json:"failed"`
	StatusCode        int                      `json:"status_code"`
	LatencyMs         int64                    `json:"latency_ms"`
	Stream            bool                     `json:"stream,omitempty"`
	TtftMs            int64                    `json:"ttft_ms,omitempty"`
	ErrorMessage      string                   `json:"error_message,omitempty"`
	AccountingVersion int                      `json:"accounting_version"`
	TokenBreakdown    coreusage.TokenBreakdown `json:"token_breakdown"`
}

type recordPlugin struct{}

// Dir returns the usage-stats subdirectory under the given logs directory.
func Dir(logsDir string) string {
	return filepath.Join(strings.TrimSpace(logsDir), subDir)
}

// Configure enables or disables persistent usage-stats writing and sets the
// target logs directory. It is safe to call repeatedly (e.g. on config reload).
// Registration of the underlying usage.Plugin happens at most once per process;
// the enabled flag alone gates whether records are actually written.
func Configure(enabled bool, logsDir string) {
	registerOnce.Do(func() {
		coreusage.RegisterNamedPlugin(pluginName, &recordPlugin{})
	})

	dir := Dir(logsDir)
	dirVal.Store(dir)
	enabledFlag.Store(enabled)

	if enabled && dir != "" {
		cleanupOldFiles(dir, time.Now().UTC())
	}
}

func currentDir() string {
	v := dirVal.Load()
	if v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

func (p *recordPlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if !enabledFlag.Load() {
		return
	}
	dir := currentDir()
	if dir == "" {
		return
	}

	ts := record.RequestedAt
	if ts.IsZero() {
		ts = time.Now()
	}
	ts = ts.UTC()

	detail := coreusage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType)

	failed := record.Failed
	if !failed {
		failed = !resolveSuccess(ctx)
	}
	statusCode := record.Fail.StatusCode
	if failed {
		if statusCode <= 0 {
			statusCode = internallogging.GetResponseStatus(ctx)
		}
		if statusCode <= 0 {
			statusCode = 500
		}
	} else {
		statusCode = 200
	}

	account := strings.TrimSpace(record.AuthID)
	if account == "" {
		account = strings.TrimSpace(record.AuthIndex)
	}
	if account == "" {
		account = "unknown"
	}

	alias := strings.TrimSpace(record.Alias)
	if alias == "" {
		alias = strings.TrimSpace(record.Model)
	}
	if alias == "" {
		alias = "unknown"
	}

	var ttftMs int64
	if record.TTFT > 0 {
		ttftMs = record.TTFT.Milliseconds()
	}
	errMsg := strings.TrimSpace(record.Fail.Body)
	if len(errMsg) > 256 {
		errMsg = errMsg[:256]
	}

	e := entry{
		Timestamp:         ts,
		Provider:          nonEmpty(record.Provider, "unknown"),
		ExecutorType:      nonEmpty(record.ExecutorType, "unknown"),
		Model:             nonEmpty(record.Model, "unknown"),
		Alias:             alias,
		Account:           account,
		AuthIndex:         strings.TrimSpace(record.AuthIndex),
		AuthType:          nonEmpty(record.AuthType, "unknown"),
		RequestID:         strings.TrimSpace(internallogging.GetRequestID(ctx)),
		Failed:            failed,
		StatusCode:        statusCode,
		LatencyMs:         record.Latency.Milliseconds(),
		Stream:            record.Stream,
		TtftMs:            ttftMs,
		ErrorMessage:      errMsg,
		AccountingVersion: coreusage.TokenAccountingSchemaVersion,
		TokenBreakdown:    detail.TokenBreakdown,
	}

	line, errMarshal := json.Marshal(e)
	if errMarshal != nil {
		log.Errorf("usagestats: marshal record failed: %v", errMarshal)
		return
	}
	line = append(line, '\n')

	if errWrite := appendLine(dir, ts, line); errWrite != nil {
		log.Errorf("usagestats: write record failed: %v", errWrite)
	}

	maybeCleanup(dir, ts)
}

func nonEmpty(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

// resolveSuccess mirrors internal/redisqueue/plugin.go's failure inference so
// records without an explicit Record.Failed still get a correct status_code.
func resolveSuccess(ctx context.Context) bool {
	status := internallogging.GetResponseStatus(ctx)
	if status == 0 {
		return true
	}
	return status < 400
}

func appendLine(dir string, ts time.Time, line []byte) error {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("usagestats: create dir %s: %w", dir, err)
	}
	path := filepath.Join(dir, fileName(ts))
	f, errOpen := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, filePerm)
	if errOpen != nil {
		return fmt.Errorf("usagestats: open %s: %w", path, errOpen)
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.Errorf("usagestats: close %s failed: %v", path, errClose)
		}
	}()
	if _, errWrite := f.Write(line); errWrite != nil {
		return fmt.Errorf("usagestats: write %s: %w", path, errWrite)
	}
	return nil
}

func fileName(ts time.Time) string {
	return "usage-" + ts.Format(dateLayout) + ".jsonl"
}

// maybeCleanup prunes files older than retentionDays at most once per UTC day.
func maybeCleanup(dir string, ts time.Time) {
	today := ts.Format(dateLayout)
	if prev, _ := lastCleanupDate.Load().(string); prev == today {
		return
	}
	lastCleanupDate.Store(today)
	cleanupOldFiles(dir, ts)
}

func cleanupOldFiles(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := now.AddDate(0, 0, -retentionDays)
	for _, dirEntry := range entries {
		if dirEntry.IsDir() {
			continue
		}
		date, ok := parseFileDate(dirEntry.Name())
		if !ok {
			continue
		}
		if date.Before(cutoff) {
			path := filepath.Join(dir, dirEntry.Name())
			if errRemove := os.Remove(path); errRemove != nil {
				log.Warnf("usagestats: failed to prune %s: %v", path, errRemove)
			}
		}
	}
}

func parseFileDate(name string) (time.Time, bool) {
	if !strings.HasPrefix(name, "usage-") || !strings.HasSuffix(name, ".jsonl") {
		return time.Time{}, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(name, "usage-"), ".jsonl")
	t, err := time.Parse(dateLayout, raw)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
