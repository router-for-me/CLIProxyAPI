package usagestats

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coresession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

// authTypeOAuth is the auth kind whose usage source is an account identity
// (an email or project) rather than an upstream API key.
const authTypeOAuth = "oauth"

var (
	current     atomic.Pointer[Store]
	configureMu sync.Mutex
	// recordMu is held for reading while a record is written, so a store is
	// never closed underneath an insert.
	recordMu sync.RWMutex
)

func init() {
	coreusage.RegisterPlugin(recorder{})
}

// Configure opens the usage store at path when enabled, and closes it otherwise.
// It is safe to call repeatedly, for example on every config reload.
func Configure(enabled bool, path string) error {
	configureMu.Lock()
	defer configureMu.Unlock()

	existing := current.Load()
	if enabled && existing != nil && existing.Path() == path {
		return nil
	}
	var replacement *Store
	if enabled {
		// Open the replacement first: when it cannot be opened, recording
		// carries on in the store that already works.
		store, errOpen := Open(path)
		if errOpen != nil {
			return errOpen
		}
		replacement = store
	}
	current.Store(replacement)
	if existing != nil {
		// Wait for writes that already hold the retired store.
		recordMu.Lock()
		errClose := existing.Close()
		recordMu.Unlock()
		if errClose != nil {
			log.Warnf("usage statistics: close store: %v", errClose)
		}
	}
	if replacement != nil {
		log.Infof("usage statistics: recording to %s", path)
	}
	return nil
}

// Shutdown closes the usage store.
func Shutdown() error {
	return Configure(false, "")
}

// Current returns the open usage store, or nil when recording is disabled.
func Current() *Store {
	return current.Load()
}

// Enabled reports whether usage records are being persisted.
func Enabled() bool {
	return current.Load() != nil
}

// recorder persists every usage record published by the proxy runtime.
type recorder struct{}

func (recorder) HandleUsage(ctx context.Context, record coreusage.Record) {
	recordMu.RLock()
	defer recordMu.RUnlock()
	store := current.Load()
	if store == nil {
		return
	}
	row := rowFromRecord(ctx, record)
	// The request context is usually already cancelled by the time usage is
	// published, so storage runs on its own context.
	storeCtx := context.Background()
	if row.WorkingDir == "" && row.SessionID != "" {
		dir, errLookup := store.WorkingDirForSession(storeCtx, row.SessionID, row.ClientKey)
		if errLookup != nil {
			log.Warnf("usage statistics: %v", errLookup)
		}
		row.WorkingDir = dir
	}
	row.Project = ProjectFromWorkingDir(row.WorkingDir)
	if errInsert := store.Insert(storeCtx, row); errInsert != nil {
		log.Warnf("usage statistics: %v", errInsert)
	}
	for _, sample := range quotaSamplesFromRow(row, record.ResponseHeaders) {
		if errSample := store.RecordQuotaSample(storeCtx, sample); errSample != nil {
			log.Warnf("usage statistics: %v", errSample)
		}
	}
}

// rowFromRecord flattens a usage record and its request context into a row.
func rowFromRecord(ctx context.Context, record coreusage.Record) Row {
	meta := internallogging.GetClientRequestMetadata(ctx)

	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	failed := record.Failed
	if !failed {
		failed = internallogging.GetResponseStatus(ctx) >= http.StatusBadRequest
	}
	statusCode := http.StatusOK
	if status := internallogging.GetResponseStatus(ctx); !failed && status >= http.StatusOK {
		statusCode = status
	}
	if failed {
		statusCode = record.Fail.StatusCode
		if statusCode <= 0 {
			statusCode = internallogging.GetResponseStatus(ctx)
		}
		if statusCode <= 0 {
			statusCode = http.StatusInternalServerError
		}
	}

	sessionID := strings.TrimSpace(record.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(meta.SessionID)
	}

	clientIP := strings.TrimSpace(meta.ResolvedClientIP)
	if clientIP == "" {
		clientIP = strings.TrimSpace(meta.ClientIP)
	}

	authType := strings.TrimSpace(record.AuthType)
	clientKey := strings.TrimSpace(record.APIKey)
	account := strings.TrimSpace(record.Source)
	// For key-based credentials the source is the upstream API key itself, and
	// an OAuth credential without an account identity falls back to the client
	// key. Only a real account identity is stored unmasked.
	if !strings.EqualFold(authType, authTypeOAuth) || account == clientKey {
		account = util.HideAPIKey(account)
	}

	detail := coreusage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType)
	tokens := detail.TokenBreakdown

	row := Row{
		Timestamp:       timestamp,
		RequestID:       strings.TrimSpace(record.RequestID),
		TraceID:         firstNonEmpty(record.TraceID, internallogging.GetRequestID(ctx)),
		SessionID:       coresession.NormalizeToCanonicalUUID(sessionID),
		Provider:        strings.TrimSpace(record.Provider),
		ExecutorType:    strings.TrimSpace(record.ExecutorType),
		Model:           strings.TrimSpace(record.Model),
		Alias:           firstNonEmpty(record.Alias, record.Model),
		ResponseModel:   strings.TrimSpace(record.ResponseModel),
		Endpoint:        strings.TrimSpace(internallogging.GetEndpoint(ctx)),
		AuthIndex:       strings.TrimSpace(record.AuthIndex),
		AuthType:        authType,
		Account:         account,
		ClientKey:       util.HideAPIKey(clientKey),
		ClientIP:        clientIP,
		UserAgent:       strings.TrimSpace(meta.UserAgent),
		WorkingDir:      strings.TrimSpace(meta.WorkingDir),
		ReasoningEffort: firstNonEmpty(record.ReasoningEffort, coreusage.ReasoningEffortFromContext(ctx)),
		ServiceTier:     serviceTierOf(ctx, record),
		Stream:          record.Stream || coreusage.StreamFromContext(ctx),
		Generate:        coreusage.GenerateEnabled(record.Generate),
		Failed:          failed,
		StatusCode:      statusCode,
		LatencyMs:       record.Latency.Milliseconds(),
		TTFTMs:          record.TTFT.Milliseconds(),

		InputTokens:        tokens.Input.UncachedTokens,
		CacheReadTokens:    tokens.Input.CacheReadTokens,
		CacheWriteTokens:   tokens.Input.CacheWriteTokens,
		CacheWrite5mTokens: int64Value(detail.CacheCreation5mTokens),
		CacheWrite1hTokens: int64Value(detail.CacheCreation1hTokens),
		OutputTokens:       tokens.Output.NonReasoningTokens,
		ReasoningTokens:    tokens.Output.ReasoningTokens,
		UnclassifiedTokens: tokens.UnclassifiedTokens,
		TotalTokens:        tokens.TotalTokens,
		WebSearchRequests:  int64Value(detail.WebSearchRequests),
	}
	if failed {
		row.FailCode = strings.TrimSpace(record.Fail.Code)
	}
	if detail.CostDetails != nil && detail.CostDetails.Cost != nil {
		cost := *detail.CostDetails.Cost
		row.UpstreamCostUSD = &cost
	}
	return row
}

// serviceTierOf returns the tier the request was actually served at: the one
// the upstream reported, else the one sent upstream, else the one the client
// asked for. A client that asks for "auto" is usually served "standard".
func serviceTierOf(ctx context.Context, record coreusage.Record) string {
	return firstNonEmpty(
		record.ResponseServiceTier,
		record.Detail.ResponseServiceTier,
		record.EffectiveServiceTier,
		record.ServiceTier,
		record.RequestServiceTier,
		coreusage.ServiceTierFromContext(ctx),
	)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func int64Value(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}
