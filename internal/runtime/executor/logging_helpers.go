package executor

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	apiAttemptsKey = "API_UPSTREAM_ATTEMPTS"
	apiRequestKey  = "API_REQUEST"
	apiResponseKey = "API_RESPONSE"

	// log field keys
	logKeyRequestID      = "request_id"
	logKeyIsStreaming    = "is_streaming"
	logKeyReqDurationSec = "request_duration_seconds"
	logKeyStrDurationSec = "stream_duration_seconds"
	logKeyInputTokens    = "input_tokens"
	logKeyOutputTokens   = "output_tokens"
	logKeyTotalTokens    = "total_tokens"
	logKeyTPSCompletion  = "tps_completion"
	logKeyTPSTotal       = "tps_total"
	logKeyMeasuredAt     = "measured_at"
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

	// TPS measurement fields
	requestedAt          time.Time
	firstOutputAt        time.Time
	lastOutputAt         time.Time
	inputTokens          int64
    outputTokens         int64
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
	requestedAt := time.Now()
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
		builder.WriteString(string(bytes.Clone(info.Body)))
	} else {
		builder.WriteString("<empty>")
	}
	builder.WriteString("\n\n")

	attempt := &upstreamAttempt{
		index:       index,
		request:     builder.String(),
		response:    &strings.Builder{},
		requestedAt: requestedAt,
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
	attempts, attempt := ensureAttempt(ginCtx)
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
		// mark first output time when body begins
		if attempt.firstOutputAt.IsZero() {
			attempt.firstOutputAt = time.Now()
		}
	}
	if attempt.bodyHasContent {
		attempt.response.WriteString("\n\n")
	}
	attempt.response.WriteString(string(data))
	attempt.bodyHasContent = true
	// update last output time for streaming window
	attempt.lastOutputAt = time.Now()

	updateAggregatedResponse(ginCtx, attempts)
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

	// Emit structured TPS metrics to global logger (log-only, not response)
	if len(attempts) > 0 {
		// Check dedicated TPS gate as well as general request log
		var cfg *config.Config
		if cfgAny, ok := ginCtx.Get("config"); ok {
			if c, ok2 := cfgAny.(*config.Config); ok2 {
				cfg = c
			}
		}
		if cfg != nil && !cfg.TPSLog {
			return
		}
		last := attempts[len(attempts)-1]
		// compute windows
		var (
			reqWindowSec float64
			streamWindowSec float64
		)
		if !last.requestedAt.IsZero() {
			reqWindowSec = time.Since(last.requestedAt).Seconds()
		}
		if !last.firstOutputAt.IsZero() && !last.lastOutputAt.IsZero() {
			streamWindowSec = last.lastOutputAt.Sub(last.firstOutputAt).Seconds()
		}
		// compute TPS with guards and round to 2 decimals
		var tpsCompletion float64
		if last.outputTokens <= 0 {
			// no outputs → define as 0.00 (explicit per spec)
			tpsCompletion = 0
		} else {
			window := streamWindowSec
			if window <= 0 {
				// fallback to request window for non-stream responses
				window = reqWindowSec
			}
			if window > 0 {
				tpsCompletion = round2(float64(last.outputTokens) / window)
			}
		}
        var tpsTotal float64
        if reqWindowSec > 0 && (last.inputTokens+last.outputTokens) > 0 {
            tpsTotal = round2(float64(last.inputTokens+last.outputTokens) / reqWindowSec)
        }
        // expose latest TPS values on Gin context; sample recording happens at request finalization
        ginCtx.Set("API_TPS_COMPLETION", tpsCompletion)
        ginCtx.Set("API_TPS_TOTAL", tpsTotal)
		// generate request id if missing
		rid := uuid.New().String()
		// Reuse global logging module (same formatter and outputs)
		log.WithFields(log.Fields{
			logKeyRequestID:      rid,
			logKeyIsStreaming:    !last.firstOutputAt.IsZero(),
			logKeyReqDurationSec: round2(reqWindowSec),
			logKeyStrDurationSec: round2(streamWindowSec),
			logKeyInputTokens:    last.inputTokens,
			logKeyOutputTokens:   last.outputTokens,
			logKeyTotalTokens:    last.inputTokens + last.outputTokens,
			logKeyTPSCompletion:  round2(tpsCompletion),
			logKeyTPSTotal:       round2(tpsTotal),
			logKeyMeasuredAt:     time.Now().Format(time.RFC3339Nano),
		}).Info("per-request-tps")
	}
}

func round2(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*100) / 100
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
