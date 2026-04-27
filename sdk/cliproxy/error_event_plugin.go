package cliproxy

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/store/mongostate"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

var errorEventPluginWriteFailures atomic.Int64

func init() {
	coreusage.RegisterPlugin(NewErrorEventPlugin())
}

// ErrorEventPlugin persists structured failed request records.
type ErrorEventPlugin struct{}

// NewErrorEventPlugin creates a plugin for persisting failed request events.
func NewErrorEventPlugin() *ErrorEventPlugin { return &ErrorEventPlugin{} }

// HandleUsage writes failed usage records to MongoDB on a best-effort basis.
func (p *ErrorEventPlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil || !record.Failed {
		return
	}

	store := mongostate.GetGlobalErrorEventStore()
	if store == nil {
		return
	}

	occurredAt := record.RequestedAt.UTC()
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}

	normalizedModel := coreauth.NormalizeCircuitBreakerModelID(record.Model)
	if normalizedModel == "" {
		normalizedModel = strings.TrimSpace(record.Model)
	}

	maskedMessage, messageHash := coreauth.SanitizeErrorMessageForStore(record.ErrorMessage, coreauth.DefaultSanitizedErrorMessageMaxRunes)
	circuitCountable, circuitSkipReason := coreauth.IsCircuitCountableFailure(record.StatusCode, record.ErrorMessage)
	event := mongostate.ErrorEventRecord{
		CreatedAt:          time.Now().UTC(),
		OccurredAt:         occurredAt,
		Provider:           strings.TrimSpace(record.Provider),
		Model:              strings.TrimSpace(record.Model),
		NormalizedModel:    normalizedModel,
		Source:             strings.TrimSpace(record.Source),
		AuthID:             strings.TrimSpace(record.AuthID),
		AuthIndex:          strings.TrimSpace(record.AuthIndex),
		RequestID:          strings.TrimSpace(record.RequestID),
		RequestLogRef:      strings.TrimSpace(record.RequestLogRef),
		UpstreamRequestIDs: append([]string(nil), record.UpstreamRequestIDs...),
		AttemptCount:       clampNonNegative(record.AttemptCount),
		Failed:             true,
		FailureStage:       strings.TrimSpace(record.FailureStage),
		ErrorCode:          strings.TrimSpace(record.ErrorCode),
		ErrorMessageMasked: maskedMessage,
		ErrorMessageHash:   messageHash,
		StatusCode:         record.StatusCode,
		CircuitCountable:   circuitCountable,
		CircuitSkipReason:  circuitSkipReason,
	}
	if err := store.Insert(ctx, &event); err != nil {
		errorEventPluginWriteFailures.Add(1)
		log.WithError(err).Warnf(
			"usage: failed to persist error event (provider=%s auth=%s model=%s request_id=%s)",
			event.Provider,
			event.AuthID,
			event.NormalizedModel,
			event.RequestID,
		)
	}
}

func clampNonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}
