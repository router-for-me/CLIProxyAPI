package redisqueue

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func init() {
	coreusage.RegisterPlugin(&usageQueuePlugin{})
}

type usageQueuePlugin struct{}

func (p *usageQueuePlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil {
		return
	}
	if !Enabled() || !UsageStatisticsEnabled() {
		return
	}

	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	modelName := strings.TrimSpace(record.Model)
	if modelName == "" {
		modelName = "unknown"
	}
	aliasName := strings.TrimSpace(record.Alias)
	if aliasName == "" {
		aliasName = modelName
	}
	provider := strings.TrimSpace(record.Provider)
	if provider == "" {
		provider = "unknown"
	}
	authType := strings.TrimSpace(record.AuthType)
	if authType == "" {
		authType = "unknown"
	}
	apiKey := strings.TrimSpace(record.APIKey)
	requestID := strings.TrimSpace(internallogging.GetRequestID(ctx))

	tokens := tokenStats{
		InputTokens:     record.Detail.InputTokens,
		OutputTokens:    record.Detail.OutputTokens,
		ReasoningTokens: record.Detail.ReasoningTokens,
		CachedTokens:    record.Detail.CachedTokens,
		TotalTokens:     record.Detail.TotalTokens,
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens + tokens.CachedTokens
	}

	latencyMs := record.Latency.Milliseconds()
	firstByteLatencyMs := record.FirstByteLatency.Milliseconds()
	if latencyMs < 0 {
		latencyMs = 0
	}
	if firstByteLatencyMs < 0 {
		firstByteLatencyMs = 0
	}
	generationMs := latencyMs - firstByteLatencyMs
	if generationMs < 0 {
		generationMs = 0
	}

	failed := record.Failed
	if !failed {
		failed = !resolveSuccess(ctx)
	}
	fail := resolveFail(ctx, record, failed)

	detail := requestDetail{
		Timestamp:          timestamp,
		LatencyMs:          latencyMs,
		FirstByteLatencyMs: firstByteLatencyMs,
		GenerationMs:       generationMs,
		Source:             record.Source,
		AuthIndex:          record.AuthIndex,
		ThinkingEffort:     record.ThinkingEffort,
		Tokens:             tokens,
		Failed:             failed,
		Fail:               fail,
	}

	payload, err := json.Marshal(queuedUsageDetail{
		requestDetail: detail,
		Provider:      provider,
		Model:         modelName,
		Alias:         aliasName,
		Endpoint:      resolveEndpoint(ctx),
		AuthType:      authType,
		APIKey:        apiKey,
		RequestID:     requestID,
	})
	if err != nil {
		return
	}
	Enqueue(payload)
}

type requestDetail struct {
	ID                 string     `json:"id"`
	Timestamp          time.Time  `json:"timestamp"`
	LatencyMs          int64      `json:"latency_ms"`
	FirstByteLatencyMs int64      `json:"first_byte_latency_ms"`
	GenerationMs       int64      `json:"generation_ms"`
	Source             string     `json:"source"`
	AuthIndex          string     `json:"auth_index"`
	ThinkingEffort     string     `json:"thinking_effort"`
	Tokens             tokenStats `json:"tokens"`
	Failed             bool       `json:"failed"`
	Fail               failDetail `json:"fail"`
}

type tokenStats struct {
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ReasoningTokens int64 `json:"reasoning_tokens"`
	CachedTokens    int64 `json:"cached_tokens"`
	TotalTokens     int64 `json:"total_tokens"`
}

type queuedUsageDetail struct {
	requestDetail
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Alias     string `json:"alias"`
	Endpoint  string `json:"endpoint"`
	AuthType  string `json:"auth_type"`
	APIKey    string `json:"api_key"`
	RequestID string `json:"request_id"`
}

type failDetail struct {
	StatusCode int    `json:"status_code"`
	Body       string `json:"body"`
}

func resolveFail(ctx context.Context, record coreusage.Record, failed bool) failDetail {
	fail := failDetail{
		StatusCode: record.Fail.StatusCode,
		Body:       strings.TrimSpace(record.Fail.Body),
	}
	if !failed {
		return failDetail{StatusCode: 200}
	}
	if fail.StatusCode <= 0 {
		fail.StatusCode = internallogging.GetResponseStatus(ctx)
	}
	if fail.StatusCode <= 0 {
		fail.StatusCode = 500
	}
	return fail
}

func resolveSuccess(ctx context.Context) bool {
	status := internallogging.GetResponseStatus(ctx)
	if status == 0 {
		return true
	}
	return status < httpStatusBadRequest
}

func resolveEndpoint(ctx context.Context) string {
	return strings.TrimSpace(internallogging.GetEndpoint(ctx))
}

const httpStatusBadRequest = 400
