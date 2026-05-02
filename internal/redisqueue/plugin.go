package redisqueue

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	internalusage "github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func init() {
	coreusage.RegisterPlugin(&usageQueuePlugin{})
}

type usageQueuePlugin struct{}

func (p *usageQueuePlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil {
		return
	}
	if !Enabled() || !internalusage.StatisticsEnabled() {
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

	tokens := internalusage.TokenStats{
		InputTokens:     record.Detail.InputTokens,
		OutputTokens:    record.Detail.OutputTokens,
		ReasoningTokens: record.Detail.ReasoningTokens,
		CachedTokens:    record.Detail.CachedTokens,
		TotalTokens:     record.Detail.TotalTokens,
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens + tokens.CachedTokens
	}

	failed := record.Failed
	if !failed {
		failed = !resolveSuccess(ctx)
	}

	detail := internalusage.RequestDetail{
		Timestamp: timestamp,
		LatencyMs: record.Latency.Milliseconds(),
		Source:    record.Source,
		AuthIndex: record.AuthIndex,
		Tokens:    tokens,
		Failed:    failed,
	}

	payload, err := json.Marshal(queuedUsageDetail{
		RequestDetail: detail,
		Provider:      provider,
		Model:         modelName,
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

type queuedUsageDetail struct {
	internalusage.RequestDetail
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Endpoint  string `json:"endpoint"`
	AuthType  string `json:"auth_type"`
	APIKey    string `json:"api_key"`
	RequestID string `json:"request_id"`
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
