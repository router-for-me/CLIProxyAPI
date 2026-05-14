package helps

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	apiAttemptsKey          = "API_UPSTREAM_ATTEMPTS"
	apiRequestKey           = "API_REQUEST"
	apiResponseKey          = "API_RESPONSE"
	apiWebsocketTimelineKey = "API_WEBSOCKET_TIMELINE"
	creditsUsedKey          = "__antigravity_credits_used__"

	// Context keys written regardless of RequestLog; available to plugins and middleware.
	UpstreamURLKey          = "upstream_url"
	UpstreamFirstUserMsgKey = "upstream_first_user_msg"
	UpstreamResponseTextKey = "upstream_response_text"
	UpstreamRawUsageKey     = "upstream_raw_usage"
)

// UpstreamRequestLog captures the outbound upstream request details for logging.
type UpstreamRequestLog struct {
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
	prevWasSSEEvent      bool
	errorWritten         bool
}

// RecordAPIRequest stores the upstream request metadata in Gin context for request logging.
func RecordAPIRequest(ctx context.Context, cfg *config.Config, info UpstreamRequestLog) {
	// Always-on: store the upstream URL and first user message for plugins.
	// Response and usage keys are reset here so retried attempts do not mix
	// data from a failed attempt with the final response.
	if ginCtx := ginContextFrom(ctx); ginCtx != nil {
		ginCtx.Set(UpstreamResponseTextKey, "")
		ginCtx.Set(UpstreamRawUsageKey, nil)
		if info.URL != "" {
			ginCtx.Set(UpstreamURLKey, info.URL)
		}
		if len(info.Body) > 0 {
			if msg := extractFirstUserText(info.Body); msg != "" {
				ginCtx.Set(UpstreamFirstUserMsgKey, msg)
			}
		}
	}

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
		builder.WriteString(string(info.Body))
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

// RecordAPIResponseMetadata captures upstream response status/header information for the latest attempt.
func RecordAPIResponseMetadata(ctx context.Context, cfg *config.Config, status int, headers http.Header) {
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

// RecordAPIResponseError adds an error entry for the latest attempt when no HTTP response is available.
func RecordAPIResponseError(ctx context.Context, cfg *config.Config, err error) {
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

// AppendAPIResponseChunk appends an upstream response chunk to Gin context for request logging.
func AppendAPIResponseChunk(ctx context.Context, cfg *config.Config, chunk []byte) {
	// Always-on: accumulate response text for plugins.
	if ginCtx := ginContextFrom(ctx); ginCtx != nil {
		accumulateResponseText(ginCtx, chunk)
	}

	if cfg == nil || !cfg.RequestLog {
		return
	}
	data := bytes.TrimSpace(chunk)
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
	}
	currentChunkIsSSEEvent := bytes.HasPrefix(data, []byte("event:"))
	currentChunkIsSSEData := bytes.HasPrefix(data, []byte("data:"))
	if attempt.bodyHasContent {
		separator := "\n\n"
		if attempt.prevWasSSEEvent && currentChunkIsSSEData {
			separator = "\n"
		}
		attempt.response.WriteString(separator)
	}
	attempt.response.WriteString(string(data))
	attempt.bodyHasContent = true
	attempt.prevWasSSEEvent = currentChunkIsSSEEvent

	updateAggregatedResponse(ginCtx, attempts)
}

// RecordAPIWebsocketRequest stores an upstream websocket request event in Gin context.
func RecordAPIWebsocketRequest(ctx context.Context, cfg *config.Config, info UpstreamRequestLog) {
	if cfg == nil || !cfg.RequestLog {
		return
	}
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return
	}

	builder := &strings.Builder{}
	builder.WriteString(fmt.Sprintf("Timestamp: %s\n", time.Now().Format(time.RFC3339Nano)))
	builder.WriteString("Event: api.websocket.request\n")
	if info.URL != "" {
		builder.WriteString(fmt.Sprintf("Upstream URL: %s\n", info.URL))
	}
	if auth := formatAuthInfo(info); auth != "" {
		builder.WriteString(fmt.Sprintf("Auth: %s\n", auth))
	}
	builder.WriteString("Headers:\n")
	writeHeaders(builder, info.Headers)
	builder.WriteString("\nBody:\n")
	if len(info.Body) > 0 {
		builder.Write(info.Body)
	} else {
		builder.WriteString("<empty>")
	}
	builder.WriteString("\n")

	appendAPIWebsocketTimeline(ginCtx, []byte(builder.String()))
}

// RecordAPIWebsocketHandshake stores the upstream websocket handshake response metadata.
func RecordAPIWebsocketHandshake(ctx context.Context, cfg *config.Config, status int, headers http.Header) {
	if cfg == nil || !cfg.RequestLog {
		return
	}
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return
	}

	builder := &strings.Builder{}
	builder.WriteString(fmt.Sprintf("Timestamp: %s\n", time.Now().Format(time.RFC3339Nano)))
	builder.WriteString("Event: api.websocket.handshake\n")
	if status > 0 {
		builder.WriteString(fmt.Sprintf("Status: %d\n", status))
	}
	builder.WriteString("Headers:\n")
	writeHeaders(builder, headers)
	builder.WriteString("\n")

	appendAPIWebsocketTimeline(ginCtx, []byte(builder.String()))
}

// RecordAPIWebsocketUpgradeRejection stores a rejected websocket upgrade as an HTTP attempt.
func RecordAPIWebsocketUpgradeRejection(ctx context.Context, cfg *config.Config, info UpstreamRequestLog, status int, headers http.Header, body []byte) {
	if cfg == nil || !cfg.RequestLog {
		return
	}
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return
	}

	RecordAPIRequest(ctx, cfg, info)
	RecordAPIResponseMetadata(ctx, cfg, status, headers)
	AppendAPIResponseChunk(ctx, cfg, body)
}

// WebsocketUpgradeRequestURL converts a websocket URL back to its HTTP handshake URL for logging.
func WebsocketUpgradeRequestURL(rawURL string) string {
	trimmedURL := strings.TrimSpace(rawURL)
	if trimmedURL == "" {
		return ""
	}
	parsed, err := url.Parse(trimmedURL)
	if err != nil {
		return trimmedURL
	}
	switch strings.ToLower(parsed.Scheme) {
	case "ws":
		parsed.Scheme = "http"
	case "wss":
		parsed.Scheme = "https"
	}
	return parsed.String()
}

// AppendAPIWebsocketResponse stores an upstream websocket response frame in Gin context.
func AppendAPIWebsocketResponse(ctx context.Context, cfg *config.Config, payload []byte) {
	if cfg == nil || !cfg.RequestLog {
		return
	}
	data := bytes.TrimSpace(payload)
	if len(data) == 0 {
		return
	}
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return
	}
	markAPIResponseTimestamp(ginCtx)

	builder := &strings.Builder{}
	builder.WriteString(fmt.Sprintf("Timestamp: %s\n", time.Now().Format(time.RFC3339Nano)))
	builder.WriteString("Event: api.websocket.response\n")
	builder.Write(data)
	builder.WriteString("\n")

	appendAPIWebsocketTimeline(ginCtx, []byte(builder.String()))
}

// RecordAPIWebsocketError stores an upstream websocket error event in Gin context.
func RecordAPIWebsocketError(ctx context.Context, cfg *config.Config, stage string, err error) {
	if cfg == nil || !cfg.RequestLog || err == nil {
		return
	}
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return
	}
	markAPIResponseTimestamp(ginCtx)

	builder := &strings.Builder{}
	builder.WriteString(fmt.Sprintf("Timestamp: %s\n", time.Now().Format(time.RFC3339Nano)))
	builder.WriteString("Event: api.websocket.error\n")
	if trimmed := strings.TrimSpace(stage); trimmed != "" {
		builder.WriteString(fmt.Sprintf("Stage: %s\n", trimmed))
	}
	builder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))

	appendAPIWebsocketTimeline(ginCtx, []byte(builder.String()))
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

func appendAPIWebsocketTimeline(ginCtx *gin.Context, chunk []byte) {
	if ginCtx == nil {
		return
	}
	data := bytes.TrimSpace(chunk)
	if len(data) == 0 {
		return
	}
	if existing, exists := ginCtx.Get(apiWebsocketTimelineKey); exists {
		if existingBytes, ok := existing.([]byte); ok && len(existingBytes) > 0 {
			combined := make([]byte, 0, len(existingBytes)+len(data)+2)
			combined = append(combined, existingBytes...)
			if !bytes.HasSuffix(existingBytes, []byte("\n")) {
				combined = append(combined, '\n')
			}
			combined = append(combined, '\n')
			combined = append(combined, data...)
			ginCtx.Set(apiWebsocketTimelineKey, combined)
			return
		}
	}
	ginCtx.Set(apiWebsocketTimelineKey, bytes.Clone(data))
}

func markAPIResponseTimestamp(ginCtx *gin.Context) {
	if ginCtx == nil {
		return
	}
	if _, exists := ginCtx.Get("API_RESPONSE_TIMESTAMP"); exists {
		return
	}
	ginCtx.Set("API_RESPONSE_TIMESTAMP", time.Now())
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

func formatAuthInfo(info UpstreamRequestLog) string {
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
		parts = append(parts, "type=oauth")
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

func SummarizeErrorBody(contentType string, body []byte) string {
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

	// Try to extract error message from JSON response
	if message := extractJSONErrorMessage(body); message != "" {
		return message
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

// extractFirstUserText returns the text of the first user message in a
// request body. Supports Anthropic /v1/messages and Gemini /generateContent
// formats. Returns empty string on any parse error.
func extractFirstUserText(body []byte) string {
	// Antigravity: request.contents[].parts[].text
	if contents := gjson.GetBytes(body, "request.contents"); contents.Exists() && contents.IsArray() {
		var text string
		contents.ForEach(func(_, item gjson.Result) bool {
			role := item.Get("role").String()
			if role != "" && role != "user" {
				return true
			}
			item.Get("parts").ForEach(func(_, part gjson.Result) bool {
				if t := strings.TrimSpace(part.Get("text").String()); t != "" {
					text = t
					return false
				}
				return true
			})
			return text == ""
		})
		if text != "" {
			const maxLen = 2000
			if len(text) > maxLen {
				text = text[:maxLen] + "..."
			}
			return text
		}
	}

	// Gemini: contents[].parts[].text (role=user or no role)
	if contents := gjson.GetBytes(body, "contents"); contents.Exists() && contents.IsArray() {
		var text string
		contents.ForEach(func(_, item gjson.Result) bool {
			role := item.Get("role").String()
			if role != "" && role != "user" {
				return true
			}
			item.Get("parts").ForEach(func(_, part gjson.Result) bool {
				if t := strings.TrimSpace(part.Get("text").String()); t != "" {
					text = t
					return false
				}
				return true
			})
			return text == ""
		})
		if text != "" {
			const maxLen = 2000
			if len(text) > maxLen {
				text = text[:maxLen] + "..."
			}
			return text
		}
	}

	// Responses API (Codex): input as string or input[].content[].text
	if input := gjson.GetBytes(body, "input"); input.Exists() {
		var text string
		if input.Type == gjson.String {
			text = strings.TrimSpace(input.String())
		} else if input.IsArray() {
			input.ForEach(func(_, item gjson.Result) bool {
				if role := item.Get("role").String(); role != "" && role != "user" {
					return true
				}
				item.Get("content").ForEach(func(_, block gjson.Result) bool {
					if block.Get("type").String() == "input_text" {
						if t := strings.TrimSpace(block.Get("text").String()); t != "" {
							text = t
							return false
						}
					}
					return true
				})
				return text == ""
			})
		}
		if text != "" {
			const maxLen = 2000
			if len(text) > maxLen {
				text = text[:maxLen] + "..."
			}
			return text
		}
	}

	// Anthropic/OpenAI: messages[].content
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return ""
	}
	var text string
	messages.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "user" {
			return true
		}
		content := msg.Get("content")
		if content.IsArray() {
			content.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "text" {
					t := strings.TrimSpace(block.Get("text").String())
					if t != "" {
						text = t
						return false
					}
				}
				return true
			})
		} else if content.Type == gjson.String {
			text = strings.TrimSpace(content.String())
		}
		return false
	})
	const maxLen = 2000
	if len(text) > maxLen {
		text = text[:maxLen] + "..."
	}
	return text
}

// accumulateResponseText appends text content from SSE chunks or a full JSON
// body into UpstreamResponseTextKey and usage counters into UpstreamRawUsageKey.
func accumulateResponseText(ginCtx *gin.Context, chunk []byte) {
	chunk = bytes.TrimSpace(chunk)
	if len(chunk) == 0 {
		return
	}
	const maxLen = 4000
	// Non-streaming: full JSON body.
	if bytes.HasPrefix(chunk, []byte("{")) {
		accumulateUsage(ginCtx, chunk)
		// Anthropic: walk content blocks to find the first text block.
		// content[0].text misses responses where a thinking/tool block
		// comes first; content.#.text returns a JSON array, not a string.
		var text string
		gjson.GetBytes(chunk, "content").ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "text" {
				if t := strings.TrimSpace(block.Get("text").String()); t != "" {
					text = t
					return false
				}
			}
			return true
		})
		// OpenAI-compatible: choices[0].message.content
		if text == "" {
			text = gjson.GetBytes(chunk, "choices.0.message.content").String()
		}
		// Gemini/Vertex non-streaming: candidates[0].content.parts[0].text
		if text == "" {
			text = gjson.GetBytes(chunk, "candidates.0.content.parts.0.text").String()
		}
		// Antigravity wrapper: response.candidates[0].content.parts[0].text
		if text == "" {
			text = gjson.GetBytes(chunk, "response.candidates.0.content.parts.0.text").String()
		}
		// Codex Responses API: response.completed event - response.output[].content[].text
		if text == "" {
			gjson.GetBytes(chunk, "response.output").ForEach(func(_, item gjson.Result) bool {
				item.Get("content").ForEach(func(_, block gjson.Result) bool {
					if block.Get("type").String() == "output_text" {
						if t := strings.TrimSpace(block.Get("text").String()); t != "" {
							text = t
							return false
						}
					}
					return true
				})
				return text == ""
			})
		}
		if text != "" {
			if len(text) > maxLen {
				text = text[:maxLen] + "..."
			}
			ginCtx.Set(UpstreamResponseTextKey, text)
		}
		return
	}
	// Streaming SSE: one TCP chunk may carry multiple events.
	// Read current value once and write back only if changed.
	var current string
	if v, ok := ginCtx.Get(UpstreamResponseTextKey); ok {
		if s, ok := v.(string); ok {
			current = s
		}
	}
	updated := false
	sawOutputTextDelta := false
	for _, line := range bytes.Split(chunk, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		accumulateUsage(ginCtx, data)
		// Anthropic: delta.text
		delta := gjson.GetBytes(data, "delta.text").String()
		if delta == "" {
			// OpenAI-compatible: choices[0].delta.content
			delta = gjson.GetBytes(data, "choices.0.delta.content").String()
		}
		if delta == "" {
			// Responses API (Codex): response.output_text.delta
			if gjson.GetBytes(data, "type").String() == "response.output_text.delta" {
				delta = gjson.GetBytes(data, "delta").String()
				if delta != "" {
					sawOutputTextDelta = true
				}
			}
		}
		if delta == "" && !sawOutputTextDelta {
			// Responses API (Codex): response.output_item.done - final text in item.content[].text.
			// Only use when no output_text.delta events were seen; otherwise the
			// full text is already accumulated from deltas and this would duplicate it.
			if gjson.GetBytes(data, "type").String() == "response.output_item.done" {
				gjson.GetBytes(data, "item.content").ForEach(func(_, block gjson.Result) bool {
					if block.Get("type").String() == "output_text" {
						if t := strings.TrimSpace(block.Get("text").String()); t != "" {
							delta = t
							return false
						}
					}
					return true
				})
			}
		}
		if delta == "" {
			// Gemini SSE (native): candidates[0].content.parts[0].text
			delta = gjson.GetBytes(data, "candidates.0.content.parts.0.text").String()
		}
		if delta == "" {
			// Gemini SSE (Antigravity wrapper): response.candidates[0].content.parts[0].text
			delta = gjson.GetBytes(data, "response.candidates.0.content.parts.0.text").String()
		}
		if delta == "" {
			continue
		}
		if len(current) < maxLen {
			current += delta
			if len(current) > maxLen {
				current = current[:maxLen] + "..."
			}
			updated = true
		}
	}
	if updated {
		ginCtx.Set(UpstreamResponseTextKey, current)
	}
}

func accumulateUsage(ginCtx *gin.Context, data []byte) {
	// Responses API: response.completed event carries usage under response.usage.
	usage := gjson.GetBytes(data, "response.usage")
	if !usage.Exists() {
		usage = gjson.GetBytes(data, "usage")
	}
	// Claude streaming: message_start event carries usage under message.usage.
	if !usage.Exists() {
		usage = gjson.GetBytes(data, "message.usage")
	}
	if !usage.Exists() {
		// Check Gemini usageMetadata path before giving up.
		if !gjson.GetBytes(data, "usageMetadata").Exists() &&
			!gjson.GetBytes(data, "response.usageMetadata").Exists() {
			return
		}
	}
	current := map[string]int64{}
	if v, ok := ginCtx.Get(UpstreamRawUsageKey); ok {
		if m, ok := v.(map[string]int64); ok {
			for k, val := range m {
				current[k] = val
			}
		}
	}
	setMax := func(key string) {
		if r := usage.Get(key); r.Exists() {
			if v := r.Int(); v > current[key] {
				current[key] = v
			}
		}
	}
	for _, key := range []string{
		// Anthropic
		"input_tokens", "output_tokens",
		"cache_read_input_tokens", "cache_creation_input_tokens",
		"reasoning_tokens", "total_tokens",
		// OpenAI-compatible
		"prompt_tokens", "completion_tokens",
	} {
		setMax(key)
	}
	// OpenAI nested details: prompt_tokens_details.cached_tokens,
	// output_tokens_details.reasoning_tokens, etc.
	if r := usage.Get("prompt_tokens_details.cached_tokens"); r.Exists() {
		if v := r.Int(); v > current["cache_read_input_tokens"] {
			current["cache_read_input_tokens"] = v
		}
	}
	if r := usage.Get("output_tokens_details.reasoning_tokens"); r.Exists() {
		if v := r.Int(); v > current["reasoning_tokens"] {
			current["reasoning_tokens"] = v
		}
	}
	// Derive total_tokens from input + output when the upstream omits it or
	// when a late SSE event raises output_tokens without updating total_tokens.
	// reasoning_tokens is a subset of output_tokens/completion_tokens, so it
	// must NOT be added again here lest we double-count it.
	if derived := current["input_tokens"] + current["output_tokens"] +
		current["prompt_tokens"] + current["completion_tokens"]; derived > current["total_tokens"] {
		current["total_tokens"] = derived
	}

	// Antigravity wrapper: response.usageMetadata
	if meta := gjson.GetBytes(data, "response.usageMetadata"); meta.Exists() {
		setFromPath := func(key, path string) {
			if r := meta.Get(path); r.Exists() {
				if v := r.Int(); v > current[key] {
					current[key] = v
				}
			}
		}
		setFromPath("prompt_tokens", "promptTokenCount")
		setFromPath("completion_tokens", "candidatesTokenCount")
		setFromPath("total_tokens", "totalTokenCount")
	}

	// Gemini usageMetadata lives at the top level, not under "usage".
	if meta := gjson.GetBytes(data, "usageMetadata"); meta.Exists() {
		setFromPath := func(key, path string) {
			if r := meta.Get(path); r.Exists() {
				if v := r.Int(); v > current[key] {
					current[key] = v
				}
			}
		}
		setFromPath("prompt_tokens", "promptTokenCount")
		setFromPath("completion_tokens", "candidatesTokenCount")
		setFromPath("total_tokens", "totalTokenCount")
	}

	ginCtx.Set(UpstreamRawUsageKey, current)
}

// extractJSONErrorMessage attempts to extract error.message from JSON error responses
func extractJSONErrorMessage(body []byte) string {
	result := gjson.GetBytes(body, "error.message")
	if result.Exists() && result.String() != "" {
		return result.String()
	}
	return ""
}

// logWithRequestID returns a logrus Entry with request_id field populated from context.
// If no request ID is found in context, it returns the standard logger.
func LogWithRequestID(ctx context.Context) *log.Entry {
	if ctx == nil {
		return log.NewEntry(log.StandardLogger())
	}
	requestID := logging.GetRequestID(ctx)
	if requestID == "" {
		return log.NewEntry(log.StandardLogger())
	}
	return log.WithField("request_id", requestID)
}

// MarkCreditsUsed flags the request as having used AI credits for billing.
func MarkCreditsUsed(ctx context.Context) {
	ginCtx := ginContextFrom(ctx)
	if ginCtx != nil {
		ginCtx.Set(creditsUsedKey, true)
	}
}

// CreditsUsed returns true if the request used AI credits.
func CreditsUsed(ctx context.Context) bool {
	ginCtx := ginContextFrom(ctx)
	if ginCtx != nil {
		if val, exists := ginCtx.Get(creditsUsedKey); exists {
			if b, ok := val.(bool); ok {
				return b
			}
		}
	}
	return false
}
