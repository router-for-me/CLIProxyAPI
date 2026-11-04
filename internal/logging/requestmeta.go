package logging

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type endpointKey struct{}
type responseStatusKey struct{}
type responseHeadersKey struct{}
type clientRequestMetadataKey struct{}

// ClientRequestMetadata stores immutable downstream request metadata for asynchronous consumers.
type ClientRequestMetadata struct {
	ClientIP         string
	ResolvedClientIP string
	XForwardedFor    string
	UserAgent        string
	SessionID        string
	ParentSessionID  string
	NodeKind         string
	IsFork           bool
	IsCompaction     bool
}

type responseStatusHolder struct {
	status atomic.Int32
}

type responseHeadersHolder struct {
	mu      sync.RWMutex
	headers http.Header
	// receivedAt is when headers were last set or merged. Streamed results are
	// recorded only after the stream drains, so this is the moment the upstream
	// values were current, not the moment the result was recorded.
	receivedAt time.Time
}

// responseHeadersNow is replaced in tests.
var responseHeadersNow = time.Now

func WithEndpoint(ctx context.Context, endpoint string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, endpointKey{}, endpoint)
}

func GetEndpoint(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if endpoint, ok := ctx.Value(endpointKey{}).(string); ok {
		return endpoint
	}
	return ""
}

// WithClientRequestMetadata stores a snapshot of downstream request metadata in ctx.
func WithClientRequestMetadata(ctx context.Context, metadata ClientRequestMetadata) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, clientRequestMetadataKey{}, metadata)
}

// GetClientRequestMetadata returns downstream request metadata stored in ctx.
func GetClientRequestMetadata(ctx context.Context) ClientRequestMetadata {
	if ctx == nil {
		return ClientRequestMetadata{}
	}
	if metadata, ok := ctx.Value(clientRequestMetadataKey{}).(ClientRequestMetadata); ok {
		return metadata
	}
	return ClientRequestMetadata{}
}

func WithResponseStatusHolder(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if holder, ok := ctx.Value(responseStatusKey{}).(*responseStatusHolder); ok && holder != nil {
		return ctx
	}
	return context.WithValue(ctx, responseStatusKey{}, &responseStatusHolder{})
}

func WithResponseHeadersHolder(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if holder, ok := ctx.Value(responseHeadersKey{}).(*responseHeadersHolder); ok && holder != nil {
		return ctx
	}
	return context.WithValue(ctx, responseHeadersKey{}, &responseHeadersHolder{})
}

// WithFreshResponseHeadersHolder starts an isolated upstream response attempt.
// Unlike WithResponseHeadersHolder, it always shadows any holder inherited from
// the parent request so a later retry cannot observe headers from an earlier
// credential or model attempt.
func WithFreshResponseHeadersHolder(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, responseHeadersKey{}, &responseHeadersHolder{})
}

func SetResponseStatus(ctx context.Context, status int) {
	if ctx == nil || status <= 0 {
		return
	}
	holder, ok := ctx.Value(responseStatusKey{}).(*responseStatusHolder)
	if !ok || holder == nil {
		return
	}
	holder.status.Store(int32(status))
}

func SetResponseHeaders(ctx context.Context, headers http.Header) {
	if ctx == nil {
		return
	}
	holder, ok := ctx.Value(responseHeadersKey{}).(*responseHeadersHolder)
	if !ok || holder == nil {
		return
	}
	holder.mu.Lock()
	defer holder.mu.Unlock()
	holder.headers = cloneHTTPHeader(headers)
	holder.receivedAt = responseHeadersNow()
}

// MergeResponseHeaders adds headers observed after the initial HTTP response,
// such as quota metadata delivered in a websocket event.
func MergeResponseHeaders(ctx context.Context, headers http.Header) {
	if ctx == nil || len(headers) == 0 {
		return
	}
	holder, ok := ctx.Value(responseHeadersKey{}).(*responseHeadersHolder)
	if !ok || holder == nil {
		return
	}
	holder.mu.Lock()
	defer holder.mu.Unlock()
	if holder.headers == nil {
		holder.headers = make(http.Header, len(headers))
	}
	for key, values := range headers {
		canonicalKey := http.CanonicalHeaderKey(key)
		if canonicalKey == "" {
			continue
		}
		holder.headers[canonicalKey] = append([]string(nil), values...)
	}
	holder.receivedAt = responseHeadersNow()
}

func GetResponseStatus(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	holder, ok := ctx.Value(responseStatusKey{}).(*responseStatusHolder)
	if !ok || holder == nil {
		return 0
	}
	return int(holder.status.Load())
}

func GetResponseHeaders(ctx context.Context) http.Header {
	if ctx == nil {
		return nil
	}
	holder, ok := ctx.Value(responseHeadersKey{}).(*responseHeadersHolder)
	if !ok || holder == nil {
		return nil
	}
	holder.mu.RLock()
	defer holder.mu.RUnlock()
	return cloneHTTPHeader(holder.headers)
}

// GetResponseHeadersReceivedAt returns when the response headers in ctx were
// last set or merged, or the zero time when none were recorded.
func GetResponseHeadersReceivedAt(ctx context.Context) time.Time {
	if ctx == nil {
		return time.Time{}
	}
	holder, ok := ctx.Value(responseHeadersKey{}).(*responseHeadersHolder)
	if !ok || holder == nil {
		return time.Time{}
	}
	holder.mu.RLock()
	defer holder.mu.RUnlock()
	return holder.receivedAt
}

func cloneHTTPHeader(src http.Header) http.Header {
	if len(src) == 0 {
		return nil
	}
	dst := make(http.Header, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}
