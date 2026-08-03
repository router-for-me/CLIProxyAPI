package llmreqlog

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/tidwall/gjson"
)

const ginThinkStatsKey = "llmreqlog_think_stats"

// thinkStats accumulates streamed think/thinking text length from the
// client-facing response. It intentionally ignores usage "thought"/reasoning
// token counters and Gemini thought:true parts.
type thinkStats struct {
	chars      atomic.Int64
	sawDelta   atomic.Bool
	hasThink   atomic.Bool
	lineBuf    []byte
	writeGuard atomic.Int32
}

func (s *thinkStats) flush() {
	if s == nil {
		return
	}
	for !s.writeGuard.CompareAndSwap(0, 1) {
	}
	defer s.writeGuard.Store(0)
	if len(s.lineBuf) == 0 {
		return
	}
	line := bytes.TrimSpace(s.lineBuf)
	s.lineBuf = s.lineBuf[:0]
	s.ingestLine(line)
}

func (s *thinkStats) snapshot() (has bool, length int64) {
	if s == nil {
		return false, 0
	}
	length = s.chars.Load()
	has = s.hasThink.Load() || length > 0
	return has, length
}

func (s *thinkStats) addText(text string) {
	if s == nil {
		return
	}
	if text == "" {
		return
	}
	s.hasThink.Store(true)
	s.chars.Add(int64(utf8.RuneCountInString(text)))
}

func (s *thinkStats) markDelta() {
	if s != nil {
		s.sawDelta.Store(true)
	}
}

func (s *thinkStats) ingest(chunk []byte) {
	if s == nil || len(chunk) == 0 {
		return
	}
	// Serialize ingest for this request writer.
	for !s.writeGuard.CompareAndSwap(0, 1) {
	}
	defer s.writeGuard.Store(0)

	data := chunk
	if len(s.lineBuf) > 0 {
		data = append(append([]byte{}, s.lineBuf...), chunk...)
		s.lineBuf = s.lineBuf[:0]
	}
	for {
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			if len(data) > 0 {
				// Cap residual buffer to avoid unbounded growth on binary noise.
				if len(data) > 1<<20 {
					data = data[len(data)-1<<20:]
				}
				s.lineBuf = append(s.lineBuf[:0], data...)
			}
			return
		}
		line := bytes.TrimSpace(data[:idx])
		data = data[idx+1:]
		s.ingestLine(line)
	}
}

func (s *thinkStats) ingestLine(line []byte) {
	if len(line) == 0 {
		return
	}
	payload := line
	if bytes.HasPrefix(payload, []byte("data:")) {
		payload = bytes.TrimSpace(payload[len("data:"):])
	}
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return
	}
	if !gjson.ValidBytes(payload) {
		// Non-SSE JSON responses may arrive as one blob without newlines.
		if bytes.HasPrefix(bytes.TrimSpace(line), []byte("{")) {
			s.ingestJSON(line)
		}
		return
	}
	s.ingestJSON(payload)
}

func (s *thinkStats) ingestJSON(raw []byte) {
	root := gjson.ParseBytes(raw)
	if !root.Exists() {
		return
	}

	// Claude Messages stream: thinking_delta carries interleaved think text.
	deltaType := root.Get("delta.type").String()
	if deltaType == "thinking_delta" {
		if text := root.Get("delta.thinking").String(); text != "" {
			s.markDelta()
			s.addText(text)
		}
		return
	}

	// Claude content_block_start / non-stream content[].type == thinking
	if root.Get("content_block.type").String() == "thinking" {
		if text := root.Get("content_block.thinking").String(); text != "" {
			s.addText(text)
		} else {
			s.hasThink.Store(true)
		}
	}
	if content := root.Get("content"); content.IsArray() {
		for _, part := range content.Array() {
			switch part.Get("type").String() {
			case "thinking":
				if text := part.Get("thinking").String(); text != "" {
					s.addText(text)
				} else {
					s.hasThink.Store(true)
				}
			}
		}
	}

	// OpenAI chat-completions: reasoning_content is the streamed think channel.
	if choices := root.Get("choices"); choices.IsArray() {
		for _, choice := range choices.Array() {
			if rc := choice.Get("delta.reasoning_content"); rc.Exists() {
				if text := extractReasoningText(rc); text != "" {
					s.markDelta()
					s.addText(text)
				}
			}
			if rc := choice.Get("message.reasoning_content"); rc.Exists() {
				if text := extractReasoningText(rc); text != "" {
					s.addText(text)
				}
			}
		}
	}

	// OpenAI / xAI Responses: interleaved think arrives as reasoning_text /
	// reasoning_summary_text deltas (not usage thought tokens).
	eventType := root.Get("type").String()
	switch eventType {
	case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
		if text := root.Get("delta").String(); text != "" {
			s.markDelta()
			s.addText(text)
		}
	case "response.reasoning_text.done", "response.reasoning_summary_text.done":
		// Prefer delta accumulation; use done text only if no deltas were seen.
		if !s.sawDelta.Load() {
			if text := firstNonEmptyJSONText(root, "text", "part.text"); text != "" {
				s.addText(text)
			}
		}
	case "response.output_item.done":
		if root.Get("item.type").String() == "reasoning" && !s.sawDelta.Load() {
			for _, part := range root.Get("item.summary").Array() {
				partType := part.Get("type").String()
				if partType == "summary_text" || partType == "reasoning_text" {
					s.addText(part.Get("text").String())
				}
			}
			for _, part := range root.Get("item.content").Array() {
				if part.Get("type").String() == "reasoning_text" {
					s.addText(part.Get("text").String())
				}
			}
		}
	}

	// Full Responses object (non-stream)
	if output := root.Get("output"); output.IsArray() && !s.sawDelta.Load() {
		for _, item := range output.Array() {
			if item.Get("type").String() != "reasoning" {
				continue
			}
			for _, part := range item.Get("summary").Array() {
				partType := part.Get("type").String()
				if partType == "summary_text" || partType == "reasoning_text" {
					s.addText(part.Get("text").String())
				}
			}
			for _, part := range item.Get("content").Array() {
				if part.Get("type").String() == "reasoning_text" {
					s.addText(part.Get("text").String())
				}
			}
		}
	}
}

func extractReasoningText(node gjson.Result) string {
	if !node.Exists() || node.Type == gjson.Null {
		return ""
	}
	if node.Type == gjson.String {
		return node.String()
	}
	if node.IsArray() {
		var builder strings.Builder
		for _, item := range node.Array() {
			switch item.Get("type").String() {
			case "summary_text", "reasoning_text", "text", "":
				builder.WriteString(item.Get("text").String())
			}
			if item.Type == gjson.String {
				builder.WriteString(item.String())
			}
		}
		return builder.String()
	}
	if text := node.Get("text").String(); text != "" {
		return text
	}
	return ""
}

func firstNonEmptyJSONText(root gjson.Result, paths ...string) string {
	for _, path := range paths {
		if text := root.Get(path).String(); strings.TrimSpace(text) != "" {
			return text
		}
	}
	return ""
}

type thinkResponseWriter struct {
	gin.ResponseWriter
	stats *thinkStats
}

func (w *thinkResponseWriter) Write(data []byte) (int, error) {
	if w.stats != nil {
		w.stats.ingest(data)
	}
	return w.ResponseWriter.Write(data)
}

func (w *thinkResponseWriter) WriteString(data string) (int, error) {
	if w.stats != nil {
		w.stats.ingest([]byte(data))
	}
	if ws, ok := w.ResponseWriter.(interface{ WriteString(string) (int, error) }); ok {
		return ws.WriteString(data)
	}
	return w.ResponseWriter.Write([]byte(data))
}

// ThinkSnifferMiddleware tees client responses and counts interleaved think text.
func ThinkSnifferMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if !shouldSniffThinkPath(path) {
			c.Next()
			return
		}
		stats := &thinkStats{}
		c.Set(ginThinkStatsKey, stats)
		c.Writer = &thinkResponseWriter{ResponseWriter: c.Writer, stats: stats}
		c.Next()
		stats.flush()
		has, length := stats.snapshot()
		if requestID := strings.TrimSpace(internallogging.GetGinRequestID(c)); requestID != "" {
			UpdateThinkByRequestID(requestID, has, length)
		}
	}
}

func shouldSniffThinkPath(path string) bool {
	if path == "" {
		return false
	}
	if strings.HasPrefix(path, "/v0/management") || strings.HasPrefix(path, "/management") {
		return false
	}
	if strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/v1beta/") {
		return true
	}
	// Common aliases
	switch path {
	case "/chat/completions", "/messages", "/responses":
		return true
	}
	return strings.Contains(path, "/chat/completions") ||
		strings.Contains(path, "/messages") ||
		strings.Contains(path, "/responses")
}

func thinkStatsFromContext(ctx context.Context) (has bool, length int64, ok bool) {
	if ctx == nil {
		return false, 0, false
	}
	ginCtx, castOK := ctx.Value("gin").(*gin.Context)
	if !castOK || ginCtx == nil {
		return false, 0, false
	}
	raw, exists := ginCtx.Get(ginThinkStatsKey)
	if !exists {
		return false, 0, false
	}
	stats, castOK := raw.(*thinkStats)
	if !castOK || stats == nil {
		return false, 0, false
	}
	has, length = stats.snapshot()
	return has, length, true
}
