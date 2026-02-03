// Package logging provides request logging functionality for the CLI Proxy API server.
package logging

import (
	"encoding/json"
	"strings"
	"time"
)

// RequestLog represents the complete structured log for a single request/response cycle.
// This is the root structure that gets serialized to JSON.
type RequestLog struct {
	Version   string              `json:"version"`
	Timestamp string              `json:"timestamp"`
	Summary   *RequestSummary     `json:"summary"`
	Protocol  *ProtocolTranslation `json:"protocol_translation,omitempty"`
	Request   *RequestInfo        `json:"request"`
	Upstream  *UpstreamInfo       `json:"upstream"`
	Response  *ResponseInfo       `json:"response"`
}

// RequestSummary provides a quick overview of the request for easy scanning.
type RequestSummary struct {
	RequestURL    string       `json:"request_url"`
	Method        string       `json:"method"`
	ClientModel   string       `json:"client_model"`
	UpstreamModel string       `json:"upstream_model,omitempty"`
	Status        int          `json:"status"`
	StatusText    string       `json:"status_text,omitempty"`
	DurationMs    int64        `json:"duration_ms"`
	TTFBMs        int64        `json:"ttfb_ms,omitempty"`
	RetryCount    int          `json:"retry_count"`
	Tokens        *TokenSummary `json:"tokens,omitempty"`
}

// TokenSummary aggregates token usage information.
type TokenSummary struct {
	Input          int `json:"input"`
	Output         int `json:"output"`
	Thinking       int `json:"thinking,omitempty"`
	Total          int `json:"total"`
	ThinkingBudget int `json:"thinking_budget,omitempty"`
}

// ProtocolTranslation shows how client parameters were transformed for upstream.
type ProtocolTranslation struct {
	ClientProtocol   string            `json:"client_protocol"`
	UpstreamProtocol string            `json:"upstream_protocol"`
	Transformations  map[string]string `json:"transformations,omitempty"`
}

// RequestInfo captures the incoming client request details.
type RequestInfo struct {
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

// UpstreamInfo captures all upstream request/response data.
type UpstreamInfo struct {
	Attempts []*UpstreamAttempt `json:"attempts"`
}

// UpstreamAttempt represents a single upstream request attempt (may retry).
type UpstreamAttempt struct {
	Index      int               `json:"index"`
	Timestamp  string            `json:"timestamp"`
	URL        string            `json:"url"`
	Method     string            `json:"method"`
	Auth       *AuthInfo         `json:"auth,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       json.RawMessage   `json:"body,omitempty"`
	Response   *UpstreamResponse `json:"response,omitempty"`
	Error      string            `json:"error,omitempty"`
	DurationMs int64             `json:"duration_ms,omitempty"`
}

// AuthInfo captures authentication details for an upstream request.
type AuthInfo struct {
	Provider string `json:"provider,omitempty"`
	AuthID   string `json:"auth_id,omitempty"`
	Label    string `json:"label,omitempty"`
	Type     string `json:"type,omitempty"`
}

// UpstreamResponse captures the upstream API response.
type UpstreamResponse struct {
	Timestamp  string                `json:"timestamp,omitempty"`
	Status     int                   `json:"status"`
	Headers    map[string]string     `json:"headers,omitempty"`
	Content    *StreamingContent     `json:"content,omitempty"`
	RawBody    json.RawMessage       `json:"raw_body,omitempty"`
	TokenUsage *TokenSummary         `json:"token_usage,omitempty"`
}

// StreamingContent holds merged streaming response content.
type StreamingContent struct {
	ThinkingText   string `json:"thinking_text,omitempty"`
	ThinkingChunks int    `json:"thinking_chunks,omitempty"`
	ThinkingChars  int    `json:"thinking_chars,omitempty"`
	ResponseText   string `json:"response_text,omitempty"`
	ResponseChunks int    `json:"response_chunks,omitempty"`
	ResponseChars  int    `json:"response_chars,omitempty"`
}

// ResponseInfo captures the final response sent to the client.
type ResponseInfo struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	// Note: Response body is typically streaming and merged in UpstreamResponse.Content
}

// StreamingContentAggregator collects and merges streaming chunks.
type StreamingContentAggregator struct {
	thinkingBuilder  strings.Builder
	responseBuilder  strings.Builder
	thinkingChunks   int
	responseChunks   int
}

// NewStreamingContentAggregator creates a new aggregator instance.
func NewStreamingContentAggregator() *StreamingContentAggregator {
	return &StreamingContentAggregator{}
}

// AddThinkingChunk adds a thinking content chunk.
func (a *StreamingContentAggregator) AddThinkingChunk(text string) {
	a.thinkingBuilder.WriteString(text)
	a.thinkingChunks++
}

// AddResponseChunk adds a response content chunk.
func (a *StreamingContentAggregator) AddResponseChunk(text string) {
	a.responseBuilder.WriteString(text)
	a.responseChunks++
}

// ToStreamingContent returns the aggregated content.
func (a *StreamingContentAggregator) ToStreamingContent() *StreamingContent {
	thinkingText := a.thinkingBuilder.String()
	responseText := a.responseBuilder.String()
	
	if thinkingText == "" && responseText == "" {
		return nil
	}
	
	return &StreamingContent{
		ThinkingText:   thinkingText,
		ThinkingChunks: a.thinkingChunks,
		ThinkingChars:  len(thinkingText),
		ResponseText:   responseText,
		ResponseChunks: a.responseChunks,
		ResponseChars:  len(responseText),
	}
}

// RequestLogBuilder helps construct a RequestLog incrementally.
type RequestLogBuilder struct {
	log       *RequestLog
	startTime time.Time
	ttfbTime  time.Time
}

// NewRequestLogBuilder creates a new builder.
func NewRequestLogBuilder(version string) *RequestLogBuilder {
	now := time.Now()
	return &RequestLogBuilder{
		log: &RequestLog{
			Version:   version,
			Timestamp: now.Format(time.RFC3339Nano),
			Summary:   &RequestSummary{},
			Upstream:  &UpstreamInfo{Attempts: make([]*UpstreamAttempt, 0)},
		},
		startTime: now,
	}
}

// SetRequest sets the request info.
func (b *RequestLogBuilder) SetRequest(url, method string, headers map[string]string, body json.RawMessage) {
	b.log.Summary.RequestURL = url
	b.log.Summary.Method = method
	b.log.Request = &RequestInfo{
		Headers: headers,
		Body:    body,
	}
}

// AddUpstreamAttempt adds an upstream attempt.
func (b *RequestLogBuilder) AddUpstreamAttempt(attempt *UpstreamAttempt) {
	b.log.Upstream.Attempts = append(b.log.Upstream.Attempts, attempt)
	b.log.Summary.RetryCount = len(b.log.Upstream.Attempts) - 1
}

// SetTTFB records the time of first byte.
func (b *RequestLogBuilder) SetTTFB(t time.Time) {
	b.ttfbTime = t
	if !b.startTime.IsZero() && !t.IsZero() {
		b.log.Summary.TTFBMs = t.Sub(b.startTime).Milliseconds()
	}
}

// SetResponse sets the final response info.
func (b *RequestLogBuilder) SetResponse(status int, headers map[string]string) {
	b.log.Summary.Status = status
	b.log.Summary.StatusText = statusText(status)
	b.log.Response = &ResponseInfo{
		Status:  status,
		Headers: headers,
	}
}

// SetProtocolTranslation sets the protocol translation info.
func (b *RequestLogBuilder) SetProtocolTranslation(clientProto, upstreamProto string, transforms map[string]string) {
	b.log.Protocol = &ProtocolTranslation{
		ClientProtocol:   clientProto,
		UpstreamProtocol: upstreamProto,
		Transformations:  transforms,
	}
}

// SetModels sets the model names.
func (b *RequestLogBuilder) SetModels(clientModel, upstreamModel string) {
	b.log.Summary.ClientModel = clientModel
	b.log.Summary.UpstreamModel = upstreamModel
}

// SetTokens sets the token summary.
func (b *RequestLogBuilder) SetTokens(input, output, thinking, thinkingBudget int) {
	b.log.Summary.Tokens = &TokenSummary{
		Input:          input,
		Output:         output,
		Thinking:       thinking,
		Total:          input + output,
		ThinkingBudget: thinkingBudget,
	}
}

// Finalize completes the log and calculates duration.
func (b *RequestLogBuilder) Finalize() *RequestLog {
	now := time.Now()
	b.log.Summary.DurationMs = now.Sub(b.startTime).Milliseconds()
	return b.log
}

// ToJSON serializes the log to pretty-printed JSON.
func (b *RequestLogBuilder) ToJSON() ([]byte, error) {
	return json.MarshalIndent(b.log, "", "  ")
}

func statusText(code int) string {
	switch code {
	case 200:
		return "OK"
	case 201:
		return "Created"
	case 400:
		return "Bad Request"
	case 401:
		return "Unauthorized"
	case 403:
		return "Forbidden"
	case 404:
		return "Not Found"
	case 429:
		return "Too Many Requests"
	case 500:
		return "Internal Server Error"
	case 502:
		return "Bad Gateway"
	case 503:
		return "Service Unavailable"
	default:
		return ""
	}
}
