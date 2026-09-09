package precompact

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// CompactedHeader is set on the downstream response when compaction happened.
const CompactedHeader = "X-Cliproxy-Compacted"

// internalSourceValue mirrors handlers.modelExecutionInternalSource; requests
// carrying it are auxiliary calls and are never compacted.
const internalSourceValue = "plugin_host_model_callback"

// Interceptor applies pre-compaction to selected-auth requests.
type Interceptor struct {
	reg        *registry.ModelRegistry
	summarizer Summarizer
	cache      *Cache
}

// New builds an interceptor. The cache is sized from cfg once; every other
// setting is read per call from the config passed to Apply so hot reload applies.
func New(cfg config.PreCompactConfig, reg *registry.ModelRegistry, s Summarizer) *Interceptor {
	c := cfg.WithDefaults()
	return &Interceptor{
		reg:        reg,
		summarizer: s,
		cache:      NewCache(c.CacheMaxSessions, c.CacheTTLDuration()),
	}
}

// Result reports what Apply did.
type Result struct {
	Compacted  bool
	FromTokens int
	ToTokens   int
	CacheHit   bool
	AuxMillis  int64
}

// Apply returns a replacement body when the request exceeds the model budget.
// Any failure returns nil (forward the original) and is logged; it never blocks.
func (i *Interceptor) Apply(ctx context.Context, cfgIn config.PreCompactConfig, req cliproxyexecutor.RequestAfterAuthInterceptRequest) ([]byte, Result) {
	var res Result
	if i == nil {
		return nil, res
	}
	cfg := cfgIn.WithDefaults()
	if !cfg.Enabled || !Supported(req.SourceFormat) || len(req.Body) == 0 {
		return nil, res
	}
	if src, _ := req.Metadata["source"].(string); src == internalSourceValue {
		return nil, res
	}
	count, err := Count(req.SourceFormat, req.Model, req.Body)
	if err != nil {
		log.WithError(err).Debug("precompact: token count failed, forwarding original")
		return nil, res
	}
	window := ModelWindow(i.reg, req.Model)
	budget := Budget(window, cfg.Threshold, req.Body)
	res.FromTokens = count
	if count <= budget {
		return nil, res
	}
	sp, ok := SplitMessages(req.SourceFormat, req.Body, cfg.KeepRecentTurns)
	if !ok {
		log.Warnf("precompact: %d tokens > budget %d for %s but fewer than %d turns to keep; forwarding original", count, budget, req.Model, cfg.KeepRecentTurns)
		return nil, res
	}
	session := sessionKey(ctx, req)
	previous := ""
	newPart := sp.Middle
	if entry, hit := i.cache.Get(session); hit && entry.Covered <= len(sp.Middle) && HashMessages(sp.Middle[:entry.Covered]) == entry.PrefixHash {
		previous = entry.Summary
		newPart = sp.Middle[entry.Covered:]
		res.CacheHit = true
	}
	summary := previous
	if len(newPart) > 0 || summary == "" {
		if i.summarizer == nil {
			return nil, res
		}
		start := time.Now()
		out, errSum := i.summarizer.Summarize(ctx, cfg.AuxModel, previous, Transcript(newPart))
		res.AuxMillis = time.Since(start).Milliseconds()
		if errSum != nil || strings.TrimSpace(out) == "" {
			log.WithError(errSum).Warnf("precompact: summary failed for session=%s model=%s; forwarding original", session, req.Model)
			return nil, res
		}
		summary = out
		i.cache.Put(session, Entry{Covered: len(sp.Middle), PrefixHash: HashMessages(sp.Middle), Summary: summary})
	}
	body, errBuild := Rebuild(req.SourceFormat, req.Body, sp, summary)
	if errBuild != nil {
		log.WithError(errBuild).Warn("precompact: rebuild failed; forwarding original")
		return nil, res
	}
	to, _ := Count(req.SourceFormat, req.Model, body)
	res.ToTokens = to
	res.Compacted = true
	log.Infof("precompact: session=%s model=%s from_tokens=%d to_tokens=%d budget=%d window=%d aux_ms=%d cache_hit=%t middle_msgs=%d",
		session, req.Model, res.FromTokens, res.ToTokens, budget, window, res.AuxMillis, res.CacheHit, len(sp.Middle))
	setResponseHeader(ctx, res)
	return body, res
}

func sessionKey(ctx context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) string {
	if id := helps.ExtractClaudeCodeSessionID(ctx, req.Body, req.Headers); id != "" {
		return id
	}
	if id := helps.DerivedSessionID(req.Metadata); id != "" {
		return id
	}
	return ""
}

func setResponseHeader(ctx context.Context, res Result) {
	if ctx == nil {
		return
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Writer != nil && !ginCtx.Writer.Written() {
		ginCtx.Writer.Header().Set(CompactedHeader, headerValue(res))
	}
}

func headerValue(res Result) string {
	return itoa(res.FromTokens) + "->" + itoa(res.ToTokens)
}

func itoa(n int) string { return strconv.Itoa(n) }
