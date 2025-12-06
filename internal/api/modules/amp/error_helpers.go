// error_helpers.go - Generic error classification utilities for proxy operations.
// This file is part of our fork-specific features and should never conflict with upstream.
// See FORK_MAINTENANCE.md for architecture details.
package amp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	log "github.com/sirupsen/logrus"
)

// ErrorType classifies proxy errors for appropriate logging and handling
type ErrorType string

const (
	ErrorTypeClientDisconnect ErrorType = "client_disconnect"
	ErrorTypeTimeout          ErrorType = "timeout"
	ErrorTypeNetworkTimeout   ErrorType = "network_timeout"
	ErrorTypeNetworkError     ErrorType = "network_error"
	ErrorTypeProxyError       ErrorType = "proxy_error"
	ErrorTypeUnknown          ErrorType = "unknown"
)

// ClassifyProxyError determines the category of a proxy error
func ClassifyProxyError(err error) ErrorType {
	if err == nil {
		return ErrorTypeUnknown
	}

	// Client disconnected (context canceled)
	if errors.Is(err, context.Canceled) {
		return ErrorTypeClientDisconnect
	}

	// Timeout errors
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorTypeTimeout
	}

	// URL/network errors
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return ErrorTypeNetworkTimeout
		}
		return ErrorTypeNetworkError
	}

	return ErrorTypeProxyError
}

// RequestContext contains debugging metadata extracted from a request
type RequestContext struct {
	RequestID   string
	ClientIP    string
	UserAgent   string
	ContentType string
	Method      string
	Path        string
}

// ExtractRequestContext extracts useful debugging context from an HTTP request
func ExtractRequestContext(req *http.Request) RequestContext {
	return RequestContext{
		RequestID:   req.Header.Get("X-Request-ID"),
		ClientIP:    req.RemoteAddr,
		UserAgent:   req.Header.Get("User-Agent"),
		ContentType: req.Header.Get("Content-Type"),
		Method:      req.Method,
		Path:        req.URL.Path,
	}
}

// GetOrGenerateRequestID returns existing request ID or generates a new one
func GetOrGenerateRequestID(req *http.Request) string {
	if id := req.Header.Get("X-Request-ID"); id != "" {
		return id
	}
	return fmt.Sprintf("req-%d", time.Now().UnixNano())
}

// LogProxyError logs a proxy error with appropriate level and context
func LogProxyError(proxyName string, req *http.Request, err error) {
	errorType := ClassifyProxyError(err)
	ctx := ExtractRequestContext(req)

	fields := log.Fields{
		"error_type": errorType,
		"method":     ctx.Method,
		"path":       ctx.Path,
		"client_ip":  ctx.ClientIP,
	}

	if ctx.RequestID != "" {
		fields["request_id"] = ctx.RequestID
	}

	// Use WARN for client-side issues, ERROR for upstream failures
	if errorType == ErrorTypeClientDisconnect {
		log.WithFields(fields).Warnf("%s: client disconnected during %s %s",
			proxyName, ctx.Method, ctx.Path)
	} else {
		fields["error"] = err.Error()
		log.WithFields(fields).Errorf("%s proxy error for %s %s: %v",
			proxyName, ctx.Method, ctx.Path, err)
	}
}
