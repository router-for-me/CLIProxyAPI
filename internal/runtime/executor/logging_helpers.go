package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

const (
	apiAttemptsKey = "API_UPSTREAM_ATTEMPTS"
	apiRequestKey  = "API_REQUEST"
	apiResponseKey = "API_RESPONSE"
)

// upstreamRequestLog captures the outbound upstream request details for logging.
type upstreamRequestLog struct {
	URL       string
	Method    string
	Headers   http.Header
	Body      []byte
	Provider  string
	AuthID    string
	AuthLabel string
	AuthType  string
	AuthValue string
}

type upstreamAttempt struct {
	index                int
	request              string
	response             *strings.Builder
	responseIntroWritten bool
	statusWritten        bool
	headersWritten       bool
	bodyStarted          bool
	bodyHasContent       bool
	errorWritten         bool
}

// recordAPIRequest stores the upstream request metadata in Gin context for request logging.
func recordAPIRequest(ctx context.Context, cfg *config.Config, info upstreamRequestLog) {
	if cfg == nil || !cfg.RequestLog {
		return
	}
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return
	}

	attempts := getAttempts(ginCtx)
	index := len(attempts) + 1

	builder := &strings.Builder{}
	builder.WriteString(fmt.Sprintf("=== API REQUEST %d ===\n", index))
	builder.WriteString(fmt.Sprintf("Timestamp: %s\n", time.Now().Format(time.RFC3339Nano)))
	if info.URL != "" {
		builder.WriteString(fmt.Sprintf("Upstream URL: %s\n", info.URL))
	} else {
		builder.WriteString("Upstream URL: <unknown>\n")
	}
	if info.Method != "" {
		builder.WriteString(fmt.Sprintf("HTTP Method: %s\n", info.Method))
	}
	if auth := formatAuthInfo(info); auth != "" {
		builder.WriteString(fmt.Sprintf("Auth: %s\n", auth))
	}
	builder.WriteString("\nHeaders:\n")
	writeHeaders(builder, info.Headers)
	builder.WriteString("\nBody:\n")
	if len(info.Body) > 0 {
		builder.WriteString(string(formatJSONBodyForLog(info.Body)))
	} else {
		builder.WriteString("<empty>")
	}
	builder.WriteString("\n\n")

	attempt := &upstreamAttempt{
		index:    index,
		request:  builder.String(),
		response: &strings.Builder{},
	}
	attempts = append(attempts, attempt)
	ginCtx.Set(apiAttemptsKey, attempts)
	updateAggregatedRequest(ginCtx, attempts)
}

// recordAPIResponseMetadata captures upstream response status/header information for the latest attempt.
func recordAPIResponseMetadata(ctx context.Context, cfg *config.Config, status int, headers http.Header) {
	if cfg == nil || !cfg.RequestLog {
		return
	}
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return
	}
	attempts, attempt := ensureAttempt(ginCtx)
	ensureResponseIntro(attempt)

	if status > 0 && !attempt.statusWritten {
		attempt.response.WriteString(fmt.Sprintf("Status: %d\n", status))
		attempt.statusWritten = true
	}
	if !attempt.headersWritten {
		attempt.response.WriteString("Headers:\n")
		writeHeaders(attempt.response, headers)
		attempt.headersWritten = true
		attempt.response.WriteString("\n")
	}

	updateAggregatedResponse(ginCtx, attempts)
}

// recordAPIResponseError adds an error entry for the latest attempt when no HTTP response is available.
func recordAPIResponseError(ctx context.Context, cfg *config.Config, err error) {
	if cfg == nil || !cfg.RequestLog || err == nil {
		return
	}
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return
	}
	attempts, attempt := ensureAttempt(ginCtx)
	ensureResponseIntro(attempt)

	if attempt.bodyStarted && !attempt.bodyHasContent {
		// Ensure body does not stay empty marker if error arrives first.
		attempt.bodyStarted = false
	}
	if attempt.errorWritten {
		attempt.response.WriteString("\n")
	}
	attempt.response.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
	attempt.errorWritten = true

	updateAggregatedResponse(ginCtx, attempts)
}

// appendAPIResponseChunk appends an upstream response chunk to Gin context for request logging.
func appendAPIResponseChunk(ctx context.Context, cfg *config.Config, chunk []byte) {
	if cfg == nil || !cfg.RequestLog {
		return
	}
	data := bytes.TrimSpace(bytes.Clone(chunk))
	if len(data) == 0 {
		return
	}
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return
	}
	_, attempt := ensureAttempt(ginCtx)
	ensureResponseIntro(attempt)

	if !attempt.headersWritten {
		attempt.response.WriteString("Headers:\n")
		writeHeaders(attempt.response, nil)
		attempt.headersWritten = true
		attempt.response.WriteString("\n")
	}
	if !attempt.bodyStarted {
		attempt.response.WriteString("Body:\n")
		attempt.bodyStarted = true
	}
	if attempt.bodyHasContent {
		attempt.response.WriteString("\n\n")
	}
	attempt.response.WriteString(string(data))
	attempt.bodyHasContent = true
}

func ginContextFrom(ctx context.Context) *gin.Context {
	ginCtx, _ := ctx.Value("gin").(*gin.Context)
	return ginCtx
}

func getAttempts(ginCtx *gin.Context) []*upstreamAttempt {
	if ginCtx == nil {
		return nil
	}
	if value, exists := ginCtx.Get(apiAttemptsKey); exists {
		if attempts, ok := value.([]*upstreamAttempt); ok {
			return attempts
		}
	}
	return nil
}

func ensureAttempt(ginCtx *gin.Context) ([]*upstreamAttempt, *upstreamAttempt) {
	attempts := getAttempts(ginCtx)
	if len(attempts) == 0 {
		attempt := &upstreamAttempt{
			index:    1,
			request:  "=== API REQUEST 1 ===\n<missing>\n\n",
			response: &strings.Builder{},
		}
		attempts = []*upstreamAttempt{attempt}
		ginCtx.Set(apiAttemptsKey, attempts)
		updateAggregatedRequest(ginCtx, attempts)
	}
	return attempts, attempts[len(attempts)-1]
}

func ensureResponseIntro(attempt *upstreamAttempt) {
	if attempt == nil || attempt.response == nil || attempt.responseIntroWritten {
		return
	}
	attempt.response.WriteString(fmt.Sprintf("=== API RESPONSE %d ===\n", attempt.index))
	attempt.response.WriteString(fmt.Sprintf("Timestamp: %s\n", time.Now().Format(time.RFC3339Nano)))
	attempt.response.WriteString("\n")
	attempt.responseIntroWritten = true
}

func updateAggregatedRequest(ginCtx *gin.Context, attempts []*upstreamAttempt) {
	if ginCtx == nil {
		return
	}
	var builder strings.Builder
	for _, attempt := range attempts {
		builder.WriteString(attempt.request)
	}
	ginCtx.Set(apiRequestKey, []byte(builder.String()))
}

func updateAggregatedResponse(ginCtx *gin.Context, attempts []*upstreamAttempt) {
	if ginCtx == nil {
		return
	}
	var builder strings.Builder
	for idx, attempt := range attempts {
		if attempt == nil || attempt.response == nil {
			continue
		}
		responseText := attempt.response.String()
		if responseText == "" {
			continue
		}
		builder.WriteString(responseText)
		if !strings.HasSuffix(responseText, "\n") {
			builder.WriteString("\n")
		}
		if idx < len(attempts)-1 {
			builder.WriteString("\n")
		}
	}
	ginCtx.Set(apiResponseKey, []byte(builder.String()))
}

func writeHeaders(builder *strings.Builder, headers http.Header) {
	if builder == nil {
		return
	}
	if len(headers) == 0 {
		builder.WriteString("<none>\n")
		return
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		values := headers[key]
		if len(values) == 0 {
			builder.WriteString(fmt.Sprintf("%s:\n", key))
			continue
		}
		for _, value := range values {
			masked := util.MaskSensitiveHeaderValue(key, value)
			builder.WriteString(fmt.Sprintf("%s: %s\n", key, masked))
		}
	}
}

func formatAuthInfo(info upstreamRequestLog) string {
	var parts []string
	if trimmed := strings.TrimSpace(info.Provider); trimmed != "" {
		parts = append(parts, fmt.Sprintf("provider=%s", trimmed))
	}
	if trimmed := strings.TrimSpace(info.AuthID); trimmed != "" {
		parts = append(parts, fmt.Sprintf("auth_id=%s", trimmed))
	}
	if trimmed := strings.TrimSpace(info.AuthLabel); trimmed != "" {
		parts = append(parts, fmt.Sprintf("label=%s", trimmed))
	}

	authType := strings.ToLower(strings.TrimSpace(info.AuthType))
	authValue := strings.TrimSpace(info.AuthValue)
	switch authType {
	case "api_key":
		if authValue != "" {
			parts = append(parts, fmt.Sprintf("type=api_key value=%s", util.HideAPIKey(authValue)))
		} else {
			parts = append(parts, "type=api_key")
		}
	case "oauth":
		if authValue != "" {
			parts = append(parts, fmt.Sprintf("type=oauth account=%s", authValue))
		} else {
			parts = append(parts, "type=oauth")
		}
	default:
		if authType != "" {
			if authValue != "" {
				parts = append(parts, fmt.Sprintf("type=%s value=%s", authType, authValue))
			} else {
				parts = append(parts, fmt.Sprintf("type=%s", authType))
			}
		}
	}

	return strings.Join(parts, ", ")
}

func summarizeErrorBody(contentType string, body []byte) string {
	isHTML := strings.Contains(strings.ToLower(contentType), "text/html")
	if !isHTML {
		trimmed := bytes.TrimSpace(bytes.ToLower(body))
		if bytes.HasPrefix(trimmed, []byte("<!doctype html")) || bytes.HasPrefix(trimmed, []byte("<html")) {
			isHTML = true
		}
	}
	if isHTML {
		if title := extractHTMLTitle(body); title != "" {
			return title
		}
		return "[html body omitted]"
	}
	return string(body)
}

func extractHTMLTitle(body []byte) string {
	lower := bytes.ToLower(body)
	start := bytes.Index(lower, []byte("<title"))
	if start == -1 {
		return ""
	}
	gt := bytes.IndexByte(lower[start:], '>')
	if gt == -1 {
		return ""
	}
	start += gt + 1
	end := bytes.Index(lower[start:], []byte("</title>"))
	if end == -1 {
		return ""
	}
	title := string(body[start : start+end])
	title = html.UnescapeString(title)
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	return strings.Join(strings.Fields(title), " ")
}

// Constants for log formatting
const (
	// logTruncateHeadLength is the number of characters to show at the start of truncated strings
	logTruncateHeadLength = 50
	// logTruncateTailLength is the number of characters to show at the end of truncated strings
	logTruncateTailLength = 50
)

// formatJSONBodyForLog formats JSON body for logging with pretty-printing
// and truncation of long string values.
func formatJSONBodyForLog(body []byte) []byte {
	if len(body) == 0 {
		return body
	}

	// Check if body looks like JSON (starts with { or [)
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return body
	}

	// Try to parse and re-marshal with indentation
	var jsonData interface{}
	if err := json.Unmarshal(body, &jsonData); err != nil {
		// If parsing fails, just return original
		return body
	}

	// Truncate long strings in the JSON data
	truncatedData := truncateLongStringsInJSON(jsonData)

	// Marshal with indentation for readability
	formatted, err := json.MarshalIndent(truncatedData, "", "  ")
	if err != nil {
		return body
	}

	return formatted
}

// truncateLongStringsInJSON recursively traverses JSON data and truncates string values
// that exceed the combined head and tail length.
func truncateLongStringsInJSON(data interface{}) interface{} {
	switch v := data.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{})
		for key, value := range v {
			result[key] = truncateLongStringsInJSON(value)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(v))
		for i, value := range v {
			result[i] = truncateLongStringsInJSON(value)
		}
		return result
	case string:
		return truncateLogString(v)
	default:
		return data
	}
}

// truncateLogString truncates a string if it exceeds the combined head and tail length.
func truncateLogString(s string) string {
	// Use rune count for proper Unicode handling
	runes := []rune(s)
	minLengthToTruncate := logTruncateHeadLength + logTruncateTailLength
	if len(runes) <= minLengthToTruncate {
		return s
	}

	truncatedCount := len(runes) - logTruncateHeadLength - logTruncateTailLength
	head := string(runes[:logTruncateHeadLength])
	tail := string(runes[len(runes)-logTruncateTailLength:])

	return fmt.Sprintf("%s...[%d chars truncated]...%s", head, truncatedCount, tail)
}
