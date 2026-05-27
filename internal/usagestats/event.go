package usagestats

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

const (
	// apiKeyIDPrefix is prepended to the hashed API key for identification.
	apiKeyIDPrefix = "key_"
	// apiKeyIDHexLen is how many hex characters of the SHA-256 hash to keep.
	apiKeyIDHexLen = 16
)

// RecordToEvent converts a usage.Record into a sanitized Event suitable for
// persistent storage. It applies strict whitelist mapping and never copies
// sensitive fields such as raw API keys, tokens, cookies, prompts, messages,
// response bodies, headers, or full error bodies.
//
// Parameters:
//   - ctx: used to extract request ID and endpoint metadata
//   - record: the raw usage record from the pipeline
//   - matcher: optional price matcher for cost calculation; nil means unknown cost
func RecordToEvent(ctx context.Context, record coreusage.Record, matcher *PriceMatcher) Event {
	now := time.Now()
	requestedAt := record.RequestedAt
	if requestedAt.IsZero() {
		requestedAt = now
	}

	provider := safeString(record.Provider)
	model := safeString(record.Model)
	alias := safeString(record.Alias)
	if alias == "" {
		alias = model
	}
	authType := safeString(record.AuthType)
	authIndex := safeString(record.AuthIndex)
	authID := safeString(record.AuthID)
	reasoningEffort := safeString(record.ReasoningEffort)
	endpoint := safeString(resolveEndpointFromContext(ctx))
	requestID := safeString(resolveRequestIDFromContext(ctx))

	// Sanitize API key: never store raw key, only stable hash prefix.
	apiKeyID := SafeAPIKeyID(record.APIKey)

	// Sanitize source label: only use if it looks like a safe label (email,
	// project ID, file path), never a raw API key or token.
	sourceLabel := safeSourceLabel(record.Source)

	// Map failure to coarse error type only, never store full body.
	failed := record.Failed
	statusCode := record.Fail.StatusCode
	errorType := coarseErrorType(statusCode, failed)
	if !failed {
		statusCode = 0
	}

	// Token detail: whitelist copy only.
	inputTokens := record.Detail.InputTokens
	outputTokens := record.Detail.OutputTokens
	reasoningTokens := record.Detail.ReasoningTokens
	cachedTokens := record.Detail.CachedTokens
	cacheReadTokens := record.Detail.CacheReadTokens
	cacheCreationTokens := record.Detail.CacheCreationTokens
	totalTokens := record.Detail.TotalTokens
	if totalTokens == 0 {
		totalTokens = inputTokens + outputTokens + reasoningTokens
	}
	if totalTokens == 0 {
		totalTokens = inputTokens + outputTokens + reasoningTokens + cachedTokens
	}

	// Cost calculation.
	var costKnown bool
	var inputCostMicros, outputCostMicros, totalCostMicros int64
	if matcher != nil {
		if price, ok := matcher.Match(provider, model); ok {
			costKnown = true
			inputCostMicros, outputCostMicros, totalCostMicros = CalculateCost(price, inputTokens, outputTokens)
		}
	}

	return Event{
		RequestID:           requestID,
		RequestedAt:         requestedAt,
		CallType:            classifyCallType(endpoint),
		Provider:            provider,
		Model:               model,
		Alias:               alias,
		Endpoint:            endpoint,
		APIKeyID:            apiKeyID,
		AuthID:              authID,
		AuthIndex:           authIndex,
		AuthType:            authType,
		SourceLabel:         sourceLabel,
		ReasoningEffort:     reasoningEffort,
		Failed:              failed,
		StatusCode:          statusCode,
		ErrorType:           errorType,
		LatencyMs:           record.Latency.Milliseconds(),
		InputTokens:         inputTokens,
		OutputTokens:        outputTokens,
		ReasoningTokens:     reasoningTokens,
		CachedTokens:        cachedTokens,
		CacheReadTokens:     cacheReadTokens,
		CacheCreationTokens: cacheCreationTokens,
		TotalTokens:         totalTokens,
		CostKnown:           costKnown,
		InputCostMicros:     inputCostMicros,
		OutputCostMicros:    outputCostMicros,
		TotalCostMicros:     totalCostMicros,
	}
}

// SafeAPIKeyID returns a non-secret stable identifier from a raw API key.
// Returns empty string if the key is empty or whitespace-only.
func SafeAPIKeyID(rawKey string) string {
	rawKey = strings.TrimSpace(rawKey)
	if rawKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(rawKey))
	hexStr := hex.EncodeToString(sum[:])
	if len(hexStr) > apiKeyIDHexLen {
		hexStr = hexStr[:apiKeyIDHexLen]
	}
	return apiKeyIDPrefix + hexStr
}

// safeSourceLabel returns a display-safe source label.
// If the source looks like it could be a raw API key (long base64/hex with no
// @ sign), it returns empty string instead.
func safeSourceLabel(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	// If it contains @, it's likely an email — safe to store.
	if strings.Contains(source, "@") {
		return source
	}
	// If it looks like a file path (contains / or \), it's safe.
	if strings.Contains(source, "/") || strings.Contains(source, "\\") {
		return source
	}
	// If it's a project ID pattern (alphanumeric with - or _), and not too long,
	// allow it as safe label.
	if isLikelySafeLabel(source) {
		return source
	}
	// Everything else (could be API key, token, etc.) is not safe.
	return ""
}

// isLikelySafeLabel checks if the source looks like a non-secret identifier.
func isLikelySafeLabel(s string) bool {
	if len(s) > 128 {
		return false
	}
	// Simple heuristic: if it has @ it's email (handled above).
	// If it contains only alphanumeric, dash, underscore, dot, and spaces,
	// and is not too long, consider it safe.
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' || r == ' ' || r == '(' || r == ')') {
			return false
		}
	}
	return true
}

// coarseErrorType maps an HTTP status code to a coarse error classification.
// It never returns the raw error body.
func coarseErrorType(statusCode int, failed bool) string {
	if !failed {
		return ""
	}
	switch {
	case statusCode <= 0:
		return "unknown_error"
	case statusCode >= 500:
		return fmt.Sprintf("http_%d", statusCode)
	case statusCode >= 400:
		return fmt.Sprintf("http_%d", statusCode)
	default:
		return "unknown_error"
	}
}

func safeString(s string) string {
	return strings.TrimSpace(s)
}

func resolveEndpointFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	return strings.TrimSpace(internallogging.GetEndpoint(ctx))
}

func resolveRequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	return strings.TrimSpace(internallogging.GetRequestID(ctx))
}

// classifyCallType derives a short call type label from the request endpoint path.
// e.g. "POST /v1/chat/completions" -> "chat",
// "POST /v1/images/generations" -> "images", etc.
func classifyCallType(endpoint string) string {
	// Strip method prefix ("POST ", "GET ", etc.)
	path := endpoint
	if idx := strings.Index(endpoint, " "); idx >= 0 {
		path = endpoint[idx+1:]
	}
	path = strings.TrimSpace(path)

	// Normalize: strip leading version prefix like /v1/
	normalized := strings.TrimPrefix(path, "/v1/")
	normalized = strings.TrimPrefix(normalized, "/v1")

	// Match known API patterns.
	switch {
	case strings.HasPrefix(normalized, "chat/completions"):
		return "chat"
	case strings.HasPrefix(normalized, "completions"):
		return "completions"
	case strings.HasPrefix(normalized, "messages/count_tokens"):
		return "count_tokens"
	case strings.HasPrefix(normalized, "messages"):
		return "chat" // Claude messages endpoint is chat
	case strings.HasPrefix(normalized, "responses"):
		return "responses"
	case strings.HasPrefix(normalized, "images"):
		return "images"
	case strings.HasPrefix(normalized, "videos"):
		return "videos"
	case strings.HasPrefix(normalized, "embeddings"):
		return "embeddings"
	case strings.HasPrefix(normalized, "audio/"):
		return "audio"
	case strings.HasPrefix(normalized, "models"):
		return "models"
	default:
		if normalized != "" {
			return "other"
		}
		return ""
	}
}
