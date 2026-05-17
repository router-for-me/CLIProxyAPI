// Package langfuse implements a usage.Plugin that forwards each upstream
// request record to Langfuse as a generation span.
//
// The plugin reads context keys written by the proxy runtime into the gin
// context (see sdk/cliproxy/usage constants):
//
//   - usage.CtxFirstUserMsg  - text of the first user message
//   - usage.CtxResponseText  - accumulated response text
//   - usage.CtxRawUsage      - map[string]int64 of token counts
//
// Configuration (environment variables):
//
//	LANGFUSE_BASE_URL   e.g. https://cloud.langfuse.com
//	LANGFUSE_PUBLIC_KEY pk-lf-...
//	LANGFUSE_SECRET_KEY sk-lf-...
package langfuse

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

// ginContextKey is the key CPA uses to store *gin.Context in request contexts.
const ginContextKey = "gin"

// workerCount is the number of goroutines sending events to Langfuse.
const workerCount = 4

// Plugin implements coreusage.Plugin.
type Plugin struct {
	client *Client
	queue  chan GenerationBody
}

// New creates a Plugin with explicit credentials.
func New(baseURL, publicKey, secretKey string) *Plugin {
	p := &Plugin{
		client: NewClient(baseURL, publicKey, secretKey),
		queue:  make(chan GenerationBody, 256),
	}
	for range workerCount {
		go p.worker()
	}
	return p
}

func (p *Plugin) worker() {
	for gen := range p.queue {
		if err := p.client.SendGeneration(context.Background(), gen); err != nil {
			log.Debugf("langfuse plugin: %v", err)
		}
	}
}

// NewFromEnv reads LANGFUSE_BASE_URL, LANGFUSE_PUBLIC_KEY and LANGFUSE_SECRET_KEY
// from the environment. Returns nil when any required variable is missing so the
// caller can skip registration without extra error handling:
//
//	if lf := langfuse.NewFromEnv(); lf != nil {
//	    coreusage.RegisterPlugin(lf)
//	}
func NewFromEnv() *Plugin {
	baseURL := strings.TrimRight(os.Getenv("LANGFUSE_BASE_URL"), "/")
	pk := os.Getenv("LANGFUSE_PUBLIC_KEY")
	sk := os.Getenv("LANGFUSE_SECRET_KEY")
	if baseURL == "" || pk == "" || sk == "" {
		return nil
	}
	return New(baseURL, pk, sk)
}

// HandleUsage satisfies coreusage.Plugin and is called after every upstream request.
func (p *Plugin) HandleUsage(ctx context.Context, rec coreusage.Record) {
	if p == nil || p.client == nil {
		return
	}

	gc := ginCtxFrom(ctx)

	// Prefer an existing request ID so the generation attaches to a parent
	// trace opened by an upstream gateway. Fall back to a fresh UUID.
	traceID := requestIDFrom(ctx, gc)
	if traceID == "" {
		traceID = uuid.New().String()
	}

	startTime := rec.RequestedAt
	if startTime.IsZero() {
		startTime = time.Now().Add(-rec.Latency)
	}
	endTime := startTime.Add(rec.Latency)

	level := "DEFAULT"
	statusMsg := ""
	if rec.Failed {
		level = "ERROR"
		statusMsg = fmt.Sprintf("upstream failed: status %d", rec.Fail.StatusCode)
	}

	gen := GenerationBody{
		ID:        uuid.New().String(),
		TraceID:   traceID,
		Name:      "cpa.upstream",
		StartTime: startTime,
		EndTime:   &endTime,
		Model:     rec.Model,
		Metadata:  buildMeta(gc, rec),
		Level:     level,
		StatusMessage: statusMsg,
	}

	if gc != nil {
		if v, ok := gc.Get(coreusage.CtxFirstUserMsg); ok {
			gen.Input = v
		}
		if v, ok := gc.Get(coreusage.CtxResponseText); ok {
			gen.Output = v
		}
	}

	gen.Usage, gen.UsageDetails = buildUsage(gc, rec)

	// Non-blocking send: drop the event if the queue is full rather than
	// letting a slow Langfuse endpoint stall the response path.
	select {
	case p.queue <- gen:
	default:
		log.Debugf("langfuse plugin: queue full, dropping event for trace %s", gen.TraceID)
	}
}

func buildMeta(gc *gin.Context, rec coreusage.Record) map[string]any {
	m := map[string]any{
		"provider":   rec.Provider,
		"auth_id":    rec.AuthID,
		"auth_index": rec.AuthIndex,
		"latency_ms": rec.Latency.Milliseconds(),
		"failed":     rec.Failed,
	}
	if gc == nil {
		return m
	}
	if v, ok := gc.Get(coreusage.CtxUpstreamURL); ok {
		m["upstream_url"] = v
	}
	if raw := rawUsage(gc); len(raw) > 0 {
		m["raw_usage"] = raw
	}
	return m
}

func buildUsage(gc *gin.Context, rec coreusage.Record) (*GenerationUsage, map[string]int64) {
	raw := rawUsage(gc)
	input := coalesce(raw, "input_tokens", rec.Detail.InputTokens)
	output := coalesce(raw, "output_tokens", rec.Detail.OutputTokens)
	cacheRead := coalesce(raw, "cache_read_input_tokens", rec.Detail.CacheReadTokens)
	reasoning := coalesce(raw, "reasoning_tokens", rec.Detail.ReasoningTokens)
	total := coalesce(raw, "total_tokens", rec.Detail.TotalTokens)
	if total == 0 {
		total = input + output + reasoning
	}

	if input == 0 && output == 0 && cacheRead == 0 && reasoning == 0 {
		return nil, nil
	}

	usage := &GenerationUsage{
		Input:          input,
		Output:         output,
		Total:          total,
		Unit:           "TOKENS",
		InputCacheRead: cacheRead,
	}
	details := map[string]int64{
		"input":  input,
		"output": output,
		"total":  total,
	}
	if cacheRead > 0 {
		details["cache_read_input_tokens"] = cacheRead
	}
	if reasoning > 0 {
		details["reasoning_tokens"] = reasoning
	}
	return usage, details
}

func rawUsage(gc *gin.Context) map[string]int64 {
	if gc == nil {
		return nil
	}
	v, ok := gc.Get(coreusage.CtxRawUsage)
	if !ok {
		return nil
	}
	m, _ := v.(map[string]int64)
	return m
}

func coalesce(raw map[string]int64, key string, fallback int64) int64 {
	if v, ok := raw[key]; ok {
		return v
	}
	return fallback
}

func ginCtxFrom(ctx context.Context) *gin.Context {
	if ctx == nil {
		return nil
	}
	gc, _ := ctx.Value(ginContextKey).(*gin.Context)
	return gc
}

func requestIDFrom(ctx context.Context, gc *gin.Context) string {
	if ctx != nil {
		if v, ok := ctx.Value("__request_id__").(string); ok && v != "" {
			return v
		}
	}
	if gc != nil {
		if v, ok := gc.Get("__request_id__"); ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}
