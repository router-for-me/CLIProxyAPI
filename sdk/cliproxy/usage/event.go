package usage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

const AccountingEventSchemaVersion = 1

// AccountingEvent is the destination-independent, credential-safe attempt contract.
type AccountingEvent struct {
	SchemaVersion        int               `json:"schema_version"`
	ExecutionID          string            `json:"execution_id"`
	TraceID              string            `json:"trace_id,omitempty"`
	StartedAt            time.Time         `json:"started_at"`
	CompletedAt          time.Time         `json:"completed_at"`
	LatencyMs            int64             `json:"latency_ms"`
	Provider             string            `json:"provider"`
	ExecutorType         string            `json:"executor_type"`
	RequestedAlias       string            `json:"requested_alias"`
	ExecutedModel        string            `json:"executed_model"`
	ResponseModel        string            `json:"response_model,omitempty"`
	RequestedServiceTier string            `json:"requested_service_tier"`
	ReportedServiceTier  string            `json:"reported_service_tier,omitempty"`
	Stream               bool              `json:"stream"`
	Status               string            `json:"status"`
	StatusCode           int               `json:"status_code,omitempty"`
	AccountID            string            `json:"account_id,omitempty"`
	ClientKeyID          string            `json:"client_key_id,omitempty"`
	SessionID            string            `json:"session_id,omitempty"`
	ParentSessionID      string            `json:"parent_session_id,omitempty"`
	NodeKind             string            `json:"node_kind,omitempty"`
	IsFork               bool              `json:"is_fork,omitempty"`
	IsCompaction         bool              `json:"is_compaction,omitempty"`
	Tokens               *AccountingTokens `json:"tokens,omitempty"`
}

type AccountingTokens struct {
	Input         int64            `json:"input"`
	Output        int64            `json:"output"`
	Reasoning     int64            `json:"reasoning"`
	Cached        int64            `json:"cached"`
	CacheRead     int64            `json:"cache_read"`
	CacheCreation int64            `json:"cache_creation"`
	Total         int64            `json:"total"`
	Breakdown     TokenBreakdown   `json:"breakdown"`
	Evidence      map[string]int64 `json:"evidence,omitempty"`
}

// AccountingSink receives only the safe event, never the source record or request context.
type AccountingSink interface{ HandleAccountingEvent(AccountingEvent) }

type accountingPlugin struct{ sink AccountingSink }

// RegisterAccountingSink enables optional accounting independently of usage statistics.
func RegisterAccountingSink(name string, sink AccountingSink) {
	if sink != nil {
		RegisterNamedPlugin("accounting:"+name, &accountingPlugin{sink: sink})
	}
}

func (p *accountingPlugin) HandleUsage(ctx context.Context, record Record) {
	if !record.AdditionalModel {
		p.sink.HandleAccountingEvent(NewAccountingEvent(ctx, record))
	}
}

func NewAccountingEvent(ctx context.Context, record Record) AccountingEvent {
	meta := internallogging.GetClientRequestMetadata(ctx)
	event := AccountingEvent{
		SchemaVersion: AccountingEventSchemaVersion,
		ExecutionID:   record.RequestID,
		TraceID:       record.TraceID,
		StartedAt:     record.RequestedAt,
		CompletedAt:   record.RequestedAt.Add(record.Latency),
		LatencyMs:     record.Latency.Milliseconds(),

		Provider:             record.Provider,
		ExecutorType:         record.ExecutorType,
		RequestedAlias:       record.Alias,
		ExecutedModel:        record.Model,
		ResponseModel:        record.ResponseModel,
		RequestedServiceTier: record.ServiceTier,
		ReportedServiceTier:  record.ResponseServiceTier,
		Stream:               record.Stream,
		Status:               "succeeded",
		StatusCode:           record.Fail.StatusCode,

		AccountID:       accountingIdentifier("account", record.AuthID),
		ClientKeyID:     accountingIdentifier("client", record.APIKey),
		SessionID:       record.SessionID,
		ParentSessionID: record.ParentSessionID,
		NodeKind:        meta.NodeKind,
		IsFork:          meta.IsFork,
		IsCompaction:    meta.IsCompaction,
	}
	if record.ExecutedModel != "" {
		event.ExecutedModel = record.ExecutedModel
	}
	if event.RequestedAlias == "" {
		event.RequestedAlias = record.Model
	}
	if event.RequestedServiceTier == "" {
		event.RequestedServiceTier = record.RequestServiceTier
	}
	if event.RequestedServiceTier == "" {
		event.RequestedServiceTier = ServiceTierFromContext(ctx)
	}
	if event.SessionID == "" {
		event.SessionID = meta.SessionID
		event.ParentSessionID = meta.ParentSessionID
	}
	if record.Failed {
		event.Status = "failed"
	}
	if record.Interrupted {
		event.Status = "interrupted"
	}
	detail := record.Detail
	if detail.UsagePresent || detail.InputTokens != 0 || detail.OutputTokens != 0 || detail.TotalTokens != 0 || detail.ReasoningTokens != 0 || detail.CachedTokens != 0 || detail.CacheReadTokens != 0 || detail.CacheCreationTokens != 0 || detail.TokenBreakdown.TotalTokens != 0 {
		normalized := EnsureTokenBreakdownForProvider(detail, record.Provider, record.ExecutorType)
		event.Tokens = &AccountingTokens{
			Input:         detail.InputTokens,
			Output:        detail.OutputTokens,
			Reasoning:     detail.ReasoningTokens,
			Cached:        detail.CachedTokens,
			CacheRead:     detail.CacheReadTokens,
			CacheCreation: detail.CacheCreationTokens,
			Total:         detail.TotalTokens,
			Breakdown:     normalized.TokenBreakdown,
			Evidence:      SafeTokenEvidence(detail.TokenEvidence),
		}
	}
	return event
}

func accountingIdentifier(kind, value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(kind + "\x00" + value))
	return hex.EncodeToString(sum[:])
}
