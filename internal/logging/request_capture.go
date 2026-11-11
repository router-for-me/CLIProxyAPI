package logging

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

const (
	// MaxDeferredAPIRequestBodyBytes bounds total deferred upstream request bodies per HTTP request.
	MaxDeferredAPIRequestBodyBytes = 32 << 20 // 32 MiB
	// MaxDeferredAPIResponseBodyBytes bounds each upstream attempt's deferred response body.
	MaxDeferredAPIResponseBodyBytes = 64 << 10 // 64 KiB
	// MaxDeferredAPIResponseHeaderBytes bounds cloned response headers per upstream attempt.
	MaxDeferredAPIResponseHeaderBytes = 16 << 10 // 16 KiB
	// MaxDeferredAPIResponseErrorBytes bounds each recorded upstream error string.
	MaxDeferredAPIResponseErrorBytes = 16 << 10 // 16 KiB
	// MaxTotalDeferredAPIResponseBodyBytes bounds total deferred upstream response payload
	// (body chunks, headers, and error strings) across retries for a single HTTP request.
	MaxTotalDeferredAPIResponseBodyBytes = 512 << 10 // 512 KiB
	// MaxDeferredAPIResponseAttempts bounds the number of upstream response attempt slots retained per HTTP request.
	MaxDeferredAPIResponseAttempts = 16
	maxDeferredErrorsPerAttempt    = 8
	maxDeferredHeaderEntries       = 64
)

type deferredAPIRequestCaptureDisabledKey struct{}
type deferredAPICaptureKey struct{}

// WithDeferredAPIRequestCaptureDisabled skips error-only upstream request and response capture
// for this context and its descendants. Full request logging is unaffected.
// Connection-scoped contexts keep this policy even when asynchronous callbacks
// outlive the handler and its pooled Gin context is reused.
func WithDeferredAPIRequestCaptureDisabled(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if capture := DeferredAPICaptureFromContext(ctx); capture != nil {
		capture.Close()
	}
	return context.WithValue(ctx, deferredAPIRequestCaptureDisabledKey{}, true)
}

// DeferredAPIRequestCaptureDisabled reports whether error-only capture is disabled.
func DeferredAPIRequestCaptureDisabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	disabled, _ := ctx.Value(deferredAPIRequestCaptureDisabledKey{}).(bool)
	return disabled
}

// DeferredAPIResponse renders the upstream response attempt sections of an
// error log from details captured while full request logging is disabled.
type DeferredAPIResponse interface {
	// HasPayload reports whether any upstream response metadata, body, or error was captured.
	HasPayload() bool
	// RenderAPIResponse returns the formatted "=== API RESPONSE n ===" attempt
	// sections, or nil when no upstream response details were captured.
	RenderAPIResponse() []byte
}

// WithDeferredAPICapture attaches a request-scoped DeferredAPICapture to ctx so
// background stream producers record into the request's own synchronized holder
// without accessing a pooled Gin context.
func WithDeferredAPICapture(ctx context.Context, capture *DeferredAPICapture) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if capture == nil {
		return ctx
	}
	return context.WithValue(ctx, deferredAPICaptureKey{}, capture)
}

// DeferredAPICaptureFromContext returns the request-scoped DeferredAPICapture carried by ctx, if any.
func DeferredAPICaptureFromContext(ctx context.Context) *DeferredAPICapture {
	if ctx == nil {
		return nil
	}
	capture, _ := ctx.Value(deferredAPICaptureKey{}).(*DeferredAPICapture)
	return capture
}

// InheritDeferredCapture propagates error-only capture policy and any request-scoped
// DeferredAPICapture holder from src to dst.
func InheritDeferredCapture(dst, src context.Context) context.Context {
	if dst == nil {
		dst = context.Background()
	}
	if src == nil {
		return dst
	}
	if DeferredAPIRequestCaptureDisabled(dst) || DeferredAPIRequestCaptureDisabled(src) {
		return WithDeferredAPIRequestCaptureDisabled(dst)
	}
	if DeferredAPICaptureFromContext(dst) == nil {
		if capture := DeferredAPICaptureFromContext(src); capture != nil {
			dst = WithDeferredAPICapture(dst, capture)
		}
	}
	return dst
}

type deferredHeaderEntry struct {
	key   string
	value string
}

type deferredResponseAttempt struct {
	index            int
	capturedAt       time.Time
	status           int
	headers          []deferredHeaderEntry
	headersRecorded  bool
	headersTruncated bool
	body             []byte
	bodyHasContent   bool
	prevWasSSEEvent  bool
	bodyTruncated    bool
	errors           []string
	errorsTruncated  bool
	requestBodyBytes int
}

func (a *deferredResponseAttempt) markCapturedAt() {
	if a.capturedAt.IsZero() {
		a.capturedAt = time.Now()
	}
}

func (a *deferredResponseAttempt) hasContent() bool {
	return a.status > 0 || a.headersRecorded || a.headersTruncated || a.bodyHasContent || a.bodyTruncated || len(a.errors) > 0 || a.errorsTruncated
}

// DeferredAPICapture retains bounded upstream request and response details for
// a single HTTP request while full request logging is disabled. All operations
// are synchronized and become no-ops after Close so asynchronous stream workers
// cannot race with Finalize or leak into a reused Gin context.
type DeferredAPICapture struct {
	mu              sync.Mutex
	requests        []DeferredAPIRequest
	requestBytes    int
	attempts        []*deferredResponseAttempt
	omittedAttempts int
	responseBytes   int
	closed          bool
}

// NewDeferredAPICapture constructs a fresh request-scoped DeferredAPICapture.
func NewDeferredAPICapture() *DeferredAPICapture {
	return &DeferredAPICapture{}
}

// Close marks the capture closed and releases buffered upstream payloads.
func (c *DeferredAPICapture) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.requests = nil
	c.requestBytes = 0
	c.attempts = nil
	c.omittedAttempts = 0
	c.responseBytes = 0
}

// IsClosed reports whether Close has been called.
func (c *DeferredAPICapture) IsClosed() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// RecordRequest stores a deferred upstream request builder within MaxDeferredAPIRequestBodyBytes
// and opens the paired upstream response attempt slot.
func (c *DeferredAPICapture) RecordRequest(body []byte, build func(index int, capturedAt time.Time, capturedBody []byte, bodyEmpty, bodyTruncated bool) DeferredAPIRequest) (DeferredAPIRequest, int, bool) {
	return c.recordRequest(body, false, build)
}

// RecordOrReplacePendingRequest replaces the trailing request builder when its paired
// response attempt has recorded no response details yet (for example when a websocket
// request is immediately rejected during its HTTP upgrade handshake), or appends a new
// request attempt otherwise.
func (c *DeferredAPICapture) RecordOrReplacePendingRequest(body []byte, build func(index int, capturedAt time.Time, capturedBody []byte, bodyEmpty, bodyTruncated bool) DeferredAPIRequest) (DeferredAPIRequest, int, bool, bool) {
	fn, bytesUsed, replaced, ok := c.recordRequestWithReplace(body, true, build)
	return fn, bytesUsed, replaced, ok
}

func (c *DeferredAPICapture) recordRequest(body []byte, replacePending bool, build func(index int, capturedAt time.Time, capturedBody []byte, bodyEmpty, bodyTruncated bool) DeferredAPIRequest) (DeferredAPIRequest, int, bool) {
	fn, bytesUsed, _, ok := c.recordRequestWithReplace(body, replacePending, build)
	return fn, bytesUsed, ok
}

func (c *DeferredAPICapture) recordRequestWithReplace(body []byte, replacePending bool, build func(index int, capturedAt time.Time, capturedBody []byte, bodyEmpty, bodyTruncated bool) DeferredAPIRequest) (DeferredAPIRequest, int, bool, bool) {
	if c == nil || build == nil {
		return nil, 0, false, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, 0, false, false
	}
	if replacePending && c.omittedAttempts == 0 && len(c.attempts) > 0 && len(c.requests) == len(c.attempts) {
		lastAttempt := c.attempts[len(c.attempts)-1]
		if lastAttempt != nil && !lastAttempt.hasContent() {
			c.requestBytes -= lastAttempt.requestBodyBytes
			if c.requestBytes < 0 {
				c.requestBytes = 0
			}
			capturedAt := time.Now()
			remaining := MaxDeferredAPIRequestBodyBytes - c.requestBytes
			if remaining < 0 {
				remaining = 0
			}
			captureLength := len(body)
			if captureLength > remaining {
				captureLength = remaining
			}
			capturedBody := bytes.Clone(body[:captureLength])
			bodyEmpty := len(body) == 0
			bodyTruncated := captureLength < len(body)
			c.requestBytes += captureLength
			lastAttempt.requestBodyBytes = captureLength
			fn := build(lastAttempt.index, capturedAt, capturedBody, bodyEmpty, bodyTruncated)
			c.requests[len(c.requests)-1] = fn
			return fn, c.requestBytes, true, true
		}
	}
	index := len(c.requests) + 1
	if len(c.requests) >= MaxDeferredAPIResponseAttempts {
		c.startAttemptLocked(index, 0)
		return nil, c.requestBytes, false, false
	}
	capturedAt := time.Now()
	remaining := MaxDeferredAPIRequestBodyBytes - c.requestBytes
	if remaining < 0 {
		remaining = 0
	}
	captureLength := len(body)
	if captureLength > remaining {
		captureLength = remaining
	}
	capturedBody := bytes.Clone(body[:captureLength])
	bodyEmpty := len(body) == 0
	bodyTruncated := captureLength < len(body)
	c.requestBytes += captureLength
	fn := build(index, capturedAt, capturedBody, bodyEmpty, bodyTruncated)
	c.requests = append(c.requests, fn)
	c.startAttemptLocked(index, captureLength)
	return fn, c.requestBytes, false, true
}

// HasRequests reports whether any deferred upstream requests were captured.
func (c *DeferredAPICapture) HasRequests() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed && len(c.requests) > 0
}

// RenderAPIRequests renders all captured upstream request sections.
func (c *DeferredAPICapture) RenderAPIRequests() []byte {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	requests := append([]DeferredAPIRequest(nil), c.requests...)
	omitted := c.omittedAttempts
	closed := c.closed
	c.mu.Unlock()
	if closed || len(requests) == 0 {
		return nil
	}
	var body bytes.Buffer
	for _, buildRequest := range requests {
		if buildRequest == nil {
			continue
		}
		body.Write(buildRequest())
	}
	if omitted > 0 {
		body.WriteString(fmt.Sprintf("[API REQUEST ATTEMPTS TRUNCATED: omitted %d additional attempt(s)]\n\n", omitted))
	}
	return body.Bytes()
}

func (c *DeferredAPICapture) startAttemptLocked(index int, requestBodyBytes int) {
	if len(c.attempts) >= MaxDeferredAPIResponseAttempts {
		c.omittedAttempts++
		return
	}
	if next := len(c.attempts) + 1; next > index {
		index = next
	}
	c.attempts = append(c.attempts, &deferredResponseAttempt{
		index:            index,
		requestBodyBytes: requestBodyBytes,
	})
}

func (c *DeferredAPICapture) latestAttemptLocked() *deferredResponseAttempt {
	if c.omittedAttempts > 0 {
		return nil
	}
	if len(c.attempts) == 0 {
		c.startAttemptLocked(1, 0)
	}
	if len(c.attempts) == 0 {
		return nil
	}
	return c.attempts[len(c.attempts)-1]
}

// RecordResponseMetadata records the HTTP status and bounded cloned headers for the latest attempt.
func (c *DeferredAPICapture) RecordResponseMetadata(status int, headers http.Header) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	attempt := c.latestAttemptLocked()
	if attempt == nil {
		return
	}
	attempt.markCapturedAt()
	if status > 0 && attempt.status == 0 {
		attempt.status = status
	}
	if !attempt.headersRecorded {
		attempt.headersRecorded = true
		if len(headers) > 0 {
			budget := min(MaxDeferredAPIResponseHeaderBytes, MaxTotalDeferredAPIResponseBodyBytes-c.responseBytes)
			cloned, used, truncated := cloneBoundedHeaders(headers, budget)
			attempt.headers = cloned
			attempt.headersTruncated = truncated
			c.responseBytes += used
		}
	}
}

// RecordResponseError appends a bounded transport or stream error to the latest attempt.
func (c *DeferredAPICapture) RecordResponseError(err error) {
	if c == nil || err == nil {
		return
	}
	raw := strings.TrimSpace(err.Error())
	if raw == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	attempt := c.latestAttemptLocked()
	if attempt == nil {
		return
	}
	attempt.markCapturedAt()
	if len(attempt.errors) >= maxDeferredErrorsPerAttempt {
		attempt.errorsTruncated = true
		return
	}
	budget := min(MaxDeferredAPIResponseErrorBytes, MaxTotalDeferredAPIResponseBodyBytes-c.responseBytes)
	if budget <= 0 {
		attempt.errorsTruncated = true
		return
	}
	if len(raw) > budget {
		cut := truncateUTF8Boundary(raw, budget)
		cloned := strings.Clone(raw[:cut])
		c.responseBytes += cut
		attempt.errors = append(attempt.errors, fmt.Sprintf("%s [API RESPONSE ERROR TRUNCATED: captured first %d bytes]", cloned, cut))
		return
	}
	cloned := strings.Clone(raw)
	c.responseBytes += len(cloned)
	attempt.errors = append(attempt.errors, cloned)
}

// AppendResponseChunk appends a bounded response chunk to the latest attempt.
func (c *DeferredAPICapture) AppendResponseChunk(chunk []byte) {
	if c == nil {
		return
	}
	data := bytes.TrimSpace(chunk)
	if len(data) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	attempt := c.latestAttemptLocked()
	if attempt == nil {
		return
	}
	attempt.markCapturedAt()

	currentChunkIsSSEEvent := bytes.HasPrefix(data, []byte("event:"))
	currentChunkIsSSEData := bytes.HasPrefix(data, []byte("data:"))

	attemptRemaining := MaxDeferredAPIResponseBodyBytes - len(attempt.body)
	totalRemaining := MaxTotalDeferredAPIResponseBodyBytes - c.responseBytes
	remaining := min(attemptRemaining, totalRemaining)
	if remaining <= 0 {
		attempt.bodyTruncated = true
		return
	}

	if attempt.bodyHasContent {
		separator := "\n\n"
		if attempt.prevWasSSEEvent && currentChunkIsSSEData {
			separator = "\n"
		}
		if len(separator) >= remaining {
			attempt.bodyTruncated = true
			return
		}
		attempt.body = append(attempt.body, separator...)
		c.responseBytes += len(separator)
		remaining -= len(separator)
	}

	writeLength := len(data)
	if writeLength > remaining {
		writeLength = remaining
		attempt.bodyTruncated = true
	}
	if attempt.body == nil {
		attempt.body = bytes.Clone(data[:writeLength])
	} else {
		attempt.body = append(attempt.body, data[:writeLength]...)
	}
	c.responseBytes += writeLength
	attempt.bodyHasContent = true
	attempt.prevWasSSEEvent = currentChunkIsSSEEvent && !attempt.bodyTruncated
}

// HasPayload reports whether any attempt recorded upstream response details.
func (c *DeferredAPICapture) HasPayload() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	return c.hasPayloadLocked()
}

func (c *DeferredAPICapture) hasPayloadLocked() bool {
	for _, attempt := range c.attempts {
		if attempt != nil && attempt.hasContent() {
			return true
		}
	}
	return false
}

// RenderAPIResponse formats captured upstream response attempts for error logs.
func (c *DeferredAPICapture) RenderAPIResponse() []byte {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || !c.hasPayloadLocked() {
		return nil
	}
	var builder strings.Builder
	for _, attempt := range c.attempts {
		if attempt == nil || !attempt.hasContent() {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		capturedAt := attempt.capturedAt
		if capturedAt.IsZero() {
			capturedAt = time.Now()
		}
		builder.WriteString(fmt.Sprintf("=== API RESPONSE %d ===\n", attempt.index))
		builder.WriteString(fmt.Sprintf("Timestamp: %s\n\n", capturedAt.Format(time.RFC3339Nano)))
		if attempt.status > 0 {
			builder.WriteString(fmt.Sprintf("Status: %d\n", attempt.status))
		}
		if attempt.headersRecorded || attempt.headersTruncated || attempt.bodyHasContent || attempt.bodyTruncated {
			builder.WriteString("Headers:\n")
			writeMaskedHeaders(&builder, attempt.headers, attempt.headersTruncated)
			builder.WriteByte('\n')
		}
		if attempt.bodyHasContent || attempt.bodyTruncated {
			builder.WriteString("Body:\n")
			builder.Write(attempt.body)
			if attempt.bodyTruncated {
				if len(attempt.body) > 0 && attempt.body[len(attempt.body)-1] != '\n' {
					builder.WriteByte('\n')
				}
				builder.WriteString(fmt.Sprintf("[API RESPONSE BODY TRUNCATED: captured first %d bytes]", len(attempt.body)))
			}
			if len(attempt.body) == 0 || attempt.body[len(attempt.body)-1] != '\n' || attempt.bodyTruncated {
				builder.WriteByte('\n')
			}
			if len(attempt.errors) > 0 || attempt.errorsTruncated {
				builder.WriteByte('\n')
			}
		}
		for i, errText := range attempt.errors {
			if i > 0 {
				builder.WriteByte('\n')
			}
			builder.WriteString(fmt.Sprintf("Error: %s\n", errText))
		}
		if attempt.errorsTruncated {
			if len(attempt.errors) > 0 {
				builder.WriteByte('\n')
			}
			builder.WriteString("[API RESPONSE ERRORS TRUNCATED]\n")
		}
	}
	if c.omittedAttempts > 0 && builder.Len() > 0 {
		builder.WriteString(fmt.Sprintf("\n[API RESPONSE ATTEMPTS TRUNCATED: omitted %d additional attempt(s)]\n", c.omittedAttempts))
	}
	if builder.Len() == 0 {
		return nil
	}
	return []byte(builder.String())
}

// AttemptCount returns the number of recorded response attempt slots.
func (c *DeferredAPICapture) AttemptCount() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.attempts)
}

// AttemptCapturedAt returns the captured response timestamp for the given 0-based attempt slot.
func (c *DeferredAPICapture) AttemptCapturedAt(i int) time.Time {
	if c == nil {
		return time.Time{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if i < 0 || i >= len(c.attempts) || c.attempts[i] == nil {
		return time.Time{}
	}
	return c.attempts[i].capturedAt
}

// AttemptBodyStats returns the captured body length, slice capacity, and truncation flag for the given 0-based attempt slot.
func (c *DeferredAPICapture) AttemptBodyStats(i int) (length int, capacity int, truncated bool) {
	if c == nil {
		return 0, 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if i < 0 || i >= len(c.attempts) || c.attempts[i] == nil {
		return 0, 0, false
	}
	attempt := c.attempts[i]
	return len(attempt.body), cap(attempt.body), attempt.bodyTruncated
}

// TotalResponseBodyBytes returns the total captured upstream response bytes across attempts.
func (c *DeferredAPICapture) TotalResponseBodyBytes() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.responseBytes
}

func cloneBoundedHeaders(headers http.Header, budget int) ([]deferredHeaderEntry, int, bool) {
	if len(headers) == 0 {
		return nil, 0, false
	}
	if budget <= 0 {
		return nil, 0, true
	}
	truncated := len(headers) > maxDeferredHeaderEntries
	limitKeys := min(len(headers), maxDeferredHeaderEntries)
	keys := make([]string, 0, limitKeys)
	for key := range headers {
		pos := sort.SearchStrings(keys, key)
		if len(keys) < limitKeys {
			keys = append(keys, "")
			for i := len(keys) - 1; i > pos; i-- {
				keys[i] = keys[i-1]
			}
			keys[pos] = key
		} else if pos < limitKeys {
			for i := limitKeys - 1; i > pos; i-- {
				keys[i] = keys[i-1]
			}
			keys[pos] = key
		}
	}

	entries := make([]deferredHeaderEntry, 0, len(keys))
	used := 0
	stop := false
	for _, key := range keys {
		if len(entries) >= maxDeferredHeaderEntries || len(key) > budget-used {
			truncated = true
			break
		}
		clonedKey := strings.Clone(key)
		values := headers[key]
		if len(values) == 0 {
			entries = append(entries, deferredHeaderEntry{key: clonedKey})
			used += len(clonedKey)
			continue
		}
		for _, value := range values {
			if len(entries) >= maxDeferredHeaderEntries {
				truncated = true
				stop = true
				break
			}
			masked := util.MaskSensitiveHeaderValue(clonedKey, value)
			remaining := budget - used - len(clonedKey)
			if remaining <= 0 {
				truncated = true
				stop = true
				break
			}
			if len(masked) > remaining {
				cut := truncateUTF8Boundary(masked, remaining)
				entries = append(entries, deferredHeaderEntry{
					key:   clonedKey,
					value: strings.Clone(masked[:cut]),
				})
				used += len(clonedKey) + cut
				truncated = true
				stop = true
				break
			}
			entries = append(entries, deferredHeaderEntry{
				key:   clonedKey,
				value: strings.Clone(masked),
			})
			used += len(clonedKey) + len(masked)
		}
		if stop {
			break
		}
	}
	return entries, used, truncated
}

func truncateUTF8Boundary(s string, maxBytes int) int {
	if maxBytes <= 0 {
		return 0
	}
	if len(s) <= maxBytes {
		return len(s)
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	if maxBytes == 0 {
		return 1
	}
	return maxBytes
}

func writeMaskedHeaders(builder *strings.Builder, headers []deferredHeaderEntry, truncated bool) {
	if builder == nil {
		return
	}
	if len(headers) == 0 {
		if truncated {
			builder.WriteString("[API RESPONSE HEADERS TRUNCATED]\n")
			return
		}
		builder.WriteString("<none>\n")
		return
	}
	for _, entry := range headers {
		if entry.value == "" {
			builder.WriteString(fmt.Sprintf("%s:\n", entry.key))
			continue
		}
		builder.WriteString(fmt.Sprintf("%s: %s\n", entry.key, entry.value))
	}
	if truncated {
		builder.WriteString("[API RESPONSE HEADERS TRUNCATED]\n")
	}
}
