package usage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// UsageEvent is the stable DTO emitted by usage event publishers.
type UsageEvent struct {
	Version         int    `json:"version"`
	RequestID       string `json:"request_id"`
	APIKeyHash      string `json:"api_key_hash"`
	APIKeyPreview   string `json:"api_key_preview"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	Endpoint        string `json:"endpoint"`
	Source          string `json:"source"`
	AuthIndex       string `json:"auth_index"`
	Success         bool   `json:"success"`
	Failed          bool   `json:"failed"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	ReasoningTokens int64  `json:"reasoning_tokens"`
	CachedTokens    int64  `json:"cached_tokens"`
	TotalTokens     int64  `json:"total_tokens"`
	LatencyMs       int64  `json:"latency_ms"`
	RequestedAt     string `json:"requested_at"`
}

func newUsageEvent(ctx context.Context, record coreusage.Record) UsageEvent {
	requestedAt := record.RequestedAt
	if requestedAt.IsZero() {
		requestedAt = time.Now()
	}

	inputTokens, outputTokens, reasoningTokens, cachedTokens, totalTokens := normaliseEventTokens(record.Detail)
	failed := record.Failed

	model := strings.TrimSpace(record.Model)
	if model == "" {
		model = "unknown"
	}

	return UsageEvent{
		Version:         1,
		RequestID:       resolveEventRequestID(ctx),
		APIKeyHash:      hashAPIKey(record.APIKey),
		APIKeyPreview:   previewAPIKey(record.APIKey),
		Provider:        strings.TrimSpace(record.Provider),
		Model:           model,
		Endpoint:        resolveEventEndpoint(ctx),
		Source:          normaliseEventSource(record.Source, record.APIKey),
		AuthIndex:       strings.TrimSpace(record.AuthIndex),
		Success:         !failed,
		Failed:          failed,
		InputTokens:     inputTokens,
		OutputTokens:    outputTokens,
		ReasoningTokens: reasoningTokens,
		CachedTokens:    cachedTokens,
		TotalTokens:     totalTokens,
		LatencyMs:       normaliseLatency(record.Latency),
		RequestedAt:     requestedAt.Format(time.RFC3339Nano),
	}
}

func hashAPIKey(apiKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(apiKey)))
	return hex.EncodeToString(sum[:])
}

func resolveEventRequestID(ctx context.Context) string {
	if ginCtx := eventGinContext(ctx); ginCtx != nil {
		if requestID := strings.TrimSpace(logging.GetGinRequestID(ginCtx)); requestID != "" {
			return requestID
		}
	}
	if requestID := strings.TrimSpace(logging.GetRequestID(ctx)); requestID != "" {
		return requestID
	}
	return "usage_" + logging.GenerateRequestID() + "_" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

func resolveEventEndpoint(ctx context.Context) string {
	ginCtx := eventGinContext(ctx)
	if ginCtx == nil {
		return ""
	}
	if path := ginCtx.FullPath(); path != "" {
		return path
	}
	if ginCtx.Request == nil || ginCtx.Request.URL == nil {
		return ""
	}
	return ginCtx.Request.URL.Path
}

func normaliseEventTokens(detail coreusage.Detail) (int64, int64, int64, int64, int64) {
	inputTokens := normaliseEventToken(detail.InputTokens)
	outputTokens := normaliseEventToken(detail.OutputTokens)
	reasoningTokens := normaliseEventToken(detail.ReasoningTokens)
	cachedTokens := normaliseEventToken(detail.CachedTokens)
	totalTokens := normaliseEventToken(detail.TotalTokens)
	if totalTokens == 0 {
		totalTokens = inputTokens + outputTokens + reasoningTokens
	}
	return inputTokens, outputTokens, reasoningTokens, cachedTokens, totalTokens
}

func normaliseEventToken(token int64) int64 {
	if token < 0 {
		return 0
	}
	return token
}

func normaliseLatency(latency time.Duration) int64 {
	if latency < 0 {
		return 0
	}
	return latency.Milliseconds()
}

func previewAPIKey(apiKey string) string {
	return redactEventSecret(apiKey)
}

func normaliseEventSource(source, apiKey string) string {
	trimmedSource := strings.TrimSpace(source)
	if trimmedSource == "" {
		return ""
	}
	if trimmedSource == strings.TrimSpace(apiKey) {
		return redactEventSecret(trimmedSource)
	}
	redactedSource := redactEmbeddedEventSourceSecrets(trimmedSource, apiKey)
	if looksLikeEventSecret(redactedSource) {
		return redactEventSecret(redactedSource)
	}
	return redactedSource
}

func redactEventSecret(value string) string {
	preview := util.HideAPIKey(value)
	if strings.TrimSpace(value) != "" && preview == value {
		return "***"
	}
	return preview
}

func redactEmbeddedEventSourceSecrets(source, apiKey string) string {
	trimmedAPIKey := strings.TrimSpace(apiKey)
	if trimmedAPIKey != "" {
		source = strings.ReplaceAll(source, trimmedAPIKey, redactEventSecret(trimmedAPIKey))
	}

	var builder strings.Builder
	tokenStart := -1
	flushToken := func(end int) {
		if tokenStart < 0 {
			return
		}
		token := source[tokenStart:end]
		if looksLikeEventSecret(token) {
			builder.WriteString(redactEventSecret(token))
		} else {
			builder.WriteString(token)
		}
		tokenStart = -1
	}

	for i, r := range source {
		if isEventSecretTokenRune(r) {
			if tokenStart < 0 {
				tokenStart = i
			}
			continue
		}
		flushToken(i)
		builder.WriteRune(r)
	}
	flushToken(len(source))
	return builder.String()
}

func isEventSecretTokenRune(r rune) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		r == '-' ||
		r == '_' ||
		r == '.'
}

func looksLikeEventSecret(value string) bool {
	lowerValue := strings.ToLower(value)
	if strings.HasPrefix(lowerValue, "sk-") || strings.HasPrefix(lowerValue, "codex_") {
		return true
	}
	if len(value) < 32 || strings.Contains(value, "@") {
		return false
	}

	hasLetter := false
	hasDigit := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			hasLetter = true
		case r >= 'A' && r <= 'Z':
			hasLetter = true
		case r >= '0' && r <= '9':
			hasDigit = true
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return hasLetter && hasDigit
}

func eventGinContext(ctx context.Context) *gin.Context {
	if ctx == nil {
		return nil
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok {
		return nil
	}
	return ginCtx
}
