package usage

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	internallogging "github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const insertTimeout = 5 * time.Second

var statisticsEnabled atomic.Bool

func init() {
	statisticsEnabled.Store(true)
	coreusage.RegisterPlugin(NewLoggerPlugin())
}

type Recorder struct {
	mu    sync.RWMutex
	store Store
}

func NewRecorder(store Store) *Recorder {
	return &Recorder{store: store}
}

func (r *Recorder) SetStore(store Store) Store {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	previous := r.store
	r.store = store
	return previous
}

func (r *Recorder) Store() Store {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.store
}

var defaultRecorder = NewRecorder(nil)

type LoggerPlugin struct {
	recorder *Recorder
}

func NewLoggerPlugin() *LoggerPlugin { return &LoggerPlugin{recorder: defaultRecorder} }

func InitDefaultStore(path string) error {
	store, err := NewSQLiteStore(path)
	if err != nil {
		return err
	}
	replaceDefaultStore(store)
	return nil
}

func InitDefaultStoreInLogDir(logDir string) error {
	return InitDefaultStore(filepath.Join(logDir, "usage.db"))
}

func DefaultStore() Store { return defaultRecorder.Store() }

func SetDefaultStoreForTest(store Store) func() {
	previous := defaultRecorder.SetStore(store)
	return func() {
		defaultRecorder.SetStore(previous)
	}
}

func CloseDefaultStore() error {
	previous := defaultRecorder.SetStore(nil)
	if previous == nil {
		return nil
	}
	return previous.Close()
}

func replaceDefaultStore(store Store) {
	previous := defaultRecorder.SetStore(store)
	if previous == nil {
		return
	}
	if err := previous.Close(); err != nil {
		log.Warnf("usage: close previous store failed: %v", err)
	}
}

func SetStatisticsEnabled(enabled bool) { statisticsEnabled.Store(enabled) }

func StatisticsEnabled() bool { return statisticsEnabled.Load() }

func (p *LoggerPlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if !statisticsEnabled.Load() {
		return
	}
	if p == nil || p.recorder == nil {
		return
	}
	store := p.recorder.Store()
	if store == nil {
		return
	}
	insertCtx, cancel := context.WithTimeout(context.Background(), insertTimeout)
	defer cancel()
	if err := store.Insert(insertCtx, normalizeRecord(ctx, record)); err != nil {
		log.Warnf("usage: insert failed: %v", err)
	}
}

func normalizeRecord(ctx context.Context, record coreusage.Record) Record {
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	timestamp = timestamp.UTC()

	latencyMs := durationMs(record.Latency)
	firstByteMs := durationMs(record.FirstByteLatency)
	detail := record.Detail

	return Record{
		ID:                 uuid.NewString(),
		Timestamp:          timestamp,
		APIKey:             strings.TrimSpace(record.APIKey),
		Provider:           strings.TrimSpace(record.Provider),
		Model:              normalizeModel(record.Model),
		Source:             strings.TrimSpace(record.Source),
		AuthIndex:          strings.TrimSpace(record.AuthIndex),
		AuthType:           strings.TrimSpace(record.AuthType),
		Endpoint:           internallogging.GetEndpoint(ctx),
		RequestID:          internallogging.GetRequestID(ctx),
		LatencyMs:          latencyMs,
		FirstByteLatencyMs: firstByteMs,
		GenerationMs:       nonNegative(latencyMs - firstByteMs),
		ThinkingEffort:     strings.TrimSpace(record.ThinkingEffort),
		Tokens: TokenStats{
			InputTokens:     detail.InputTokens,
			OutputTokens:    detail.OutputTokens,
			ReasoningTokens: detail.ReasoningTokens,
			CachedTokens:    detail.CachedTokens,
			TotalTokens:     normalizeCoreTotal(detail),
		},
		Failed: resolveFailed(ctx, record),
	}
}

func resolveFailed(ctx context.Context, record coreusage.Record) bool {
	if record.Failed {
		return true
	}
	return internallogging.GetResponseStatus(ctx) >= 400
}

func durationMs(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}
	return duration.Milliseconds()
}

func normalizeCoreTotal(detail coreusage.Detail) int64 {
	if detail.TotalTokens != 0 {
		return detail.TotalTokens
	}
	total := detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
	if total != 0 {
		return total
	}
	return detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens + detail.CachedTokens
}
