package amp

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/api/util"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
)

// readCloser wraps a reader and forwards Close to a separate closer.
// Used to restore peeked bytes while preserving upstream body Close behavior.
type readCloser struct {
	r io.Reader
	c io.Closer
}

func (rc *readCloser) Read(p []byte) (int, error) { return rc.r.Read(p) }
func (rc *readCloser) Close() error               { return rc.c.Close() }

// classifyProxyError determines the category of proxy error for appropriate logging
func classifyProxyError(err error) string {
	if err == nil {
		return "unknown"
	}

	// Check for context cancellation (client disconnect)
	if errors.Is(err, context.Canceled) {
		return "client_disconnect"
	}

	// Check for timeout errors
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}

	// Check for URL errors (network issues, DNS, etc.)
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return "network_timeout"
		}
		return "network_error"
	}

	return "proxy_error"
}

// getRequestContext extracts useful debugging context from HTTP request
func getRequestContext(req *http.Request) map[string]interface{} {
	ctx := make(map[string]interface{})

	// Request ID for correlation
	if requestID := req.Header.Get("X-Request-ID"); requestID != "" {
		ctx["request_id"] = requestID
	}

	// Client information
	if clientIP := req.RemoteAddr; clientIP != "" {
		ctx["client_ip"] = clientIP
	}

	if userAgent := req.Header.Get("User-Agent"); userAgent != "" {
		ctx["user_agent"] = userAgent
	}

	// Content type for debugging
	if contentType := req.Header.Get("Content-Type"); contentType != "" {
		ctx["content_type"] = contentType
	}

	return ctx
}

// getOrGenerateRequestID gets existing request ID or generates a new one
func getOrGenerateRequestID(req *http.Request) string {
	if id := req.Header.Get("X-Request-ID"); id != "" {
		return id
	}
	// Simple timestamp-based ID (not UUID for performance)
	return fmt.Sprintf("req-%d", time.Now().UnixNano())
}

// createReverseProxy creates a reverse proxy handler for Amp upstream
// with automatic gzip decompression via ModifyResponse
func createReverseProxy(upstreamURL string, secretSource SecretSource) (*httputil.ReverseProxy, error) {
	parsed, err := url.Parse(upstreamURL)
	if err != nil {
		return nil, fmt.Errorf("invalid amp upstream url: %w", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(parsed)
	originalDirector := proxy.Director

	// Modify outgoing requests to inject API key and fix routing
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = parsed.Host

		// Preserve or generate correlation headers for distributed tracing
		requestID := getOrGenerateRequestID(req)
		req.Header.Set("X-Request-ID", requestID)

		// Note: We do NOT filter Anthropic-Beta headers in the proxy path
		// Users going through ampcode.com proxy are paying for the service and should get all features
		// including 1M context window (context-1m-2025-08-07)

		// Inject API key from secret source (precedence: config > env > file)
		if key, err := secretSource.Get(req.Context()); err == nil && key != "" {
			req.Header.Set("X-Api-Key", key)
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key))
		} else if err != nil {
			log.Warnf("amp secret source error (continuing without auth): %v", err)
		}
	}

	// Modify incoming responses to handle gzip without Content-Encoding
	// This addresses the same issue as inline handler gzip handling, but at the proxy level
	proxy.ModifyResponse = func(resp *http.Response) error {
		// Only process successful responses
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil
		}

		// Skip if already marked as gzip (Content-Encoding set)
		if resp.Header.Get("Content-Encoding") != "" {
			return nil
		}

		// Skip streaming responses (SSE, chunked)
		if isStreamingResponse(resp) {
			return nil
		}

		// Save reference to original upstream body for proper cleanup
		originalBody := resp.Body

		// Peek at first 2 bytes to detect gzip magic bytes
		header := make([]byte, 2)
		n, _ := io.ReadFull(originalBody, header)
		
		// Check for gzip magic bytes (0x1f 0x8b)
		// If n < 2, we didn't get enough bytes, so it's not gzip
		if n >= 2 && header[0] == 0x1f && header[1] == 0x8b {
			// It's gzip - read the rest of the body
			rest, err := io.ReadAll(originalBody)
			if err != nil {
				// Restore what we read and return original body (preserve Close behavior)
				resp.Body = &readCloser{
					r: io.MultiReader(bytes.NewReader(header[:n]), originalBody),
					c: originalBody,
				}
				return nil
			}
			
			// Reconstruct complete gzipped data
			gzippedData := append(header[:n], rest...)

			// Decompress
			gzipReader, err := gzip.NewReader(bytes.NewReader(gzippedData))
			if err != nil {
				log.Warnf("amp proxy: gzip header detected but decompress failed: %v", err)
				// Close original body and return in-memory copy
				_ = originalBody.Close()
				resp.Body = io.NopCloser(bytes.NewReader(gzippedData))
				return nil
			}

			decompressed, err := io.ReadAll(gzipReader)
			_ = gzipReader.Close()
			if err != nil {
				log.Warnf("amp proxy: gzip decompress error: %v", err)
				// Close original body and return in-memory copy
				_ = originalBody.Close()
				resp.Body = io.NopCloser(bytes.NewReader(gzippedData))
				return nil
			}

			// Close original body since we're replacing with in-memory decompressed content
			_ = originalBody.Close()

			// Replace body with decompressed content
			resp.Body = io.NopCloser(bytes.NewReader(decompressed))
			resp.ContentLength = int64(len(decompressed))

			// Update headers to reflect decompressed state
			resp.Header.Del("Content-Encoding")                                      // No longer compressed
			resp.Header.Del("Content-Length")                                        // Remove stale compressed length
			resp.Header.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10)) // Set decompressed length

			log.Debugf("amp proxy: decompressed gzip response (%d -> %d bytes)", len(gzippedData), len(decompressed))
		} else {
			// Not gzip - restore peeked bytes while preserving Close behavior
			// Handle edge cases: n might be 0, 1, or 2 depending on EOF
			resp.Body = &readCloser{
				r: io.MultiReader(bytes.NewReader(header[:n]), originalBody),
				c: originalBody,
			}
		}

		return nil
	}

	// Error handler for proxy failures
	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		errorType := classifyProxyError(err)
		requestCtx := getRequestContext(req)

		// Use WARN for expected client-side issues, ERROR for upstream failures
		if errorType == "client_disconnect" {
			log.WithFields(log.Fields{
				"error_type": errorType,
				"method":     req.Method,
				"path":       req.URL.Path,
				"context":    requestCtx,
			}).Warnf("amp upstream: client disconnected during %s %s", req.Method, req.URL.Path)
		} else {
			log.WithFields(log.Fields{
				"error_type": errorType,
				"method":     req.Method,
				"path":       req.URL.Path,
				"context":    requestCtx,
				"error":      err.Error(),
			}).Errorf("amp upstream proxy error for %s %s: %v", req.Method, req.URL.Path, err)
		}

		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusBadGateway)
		_, _ = rw.Write([]byte(`{"error":"amp_upstream_proxy_error","message":"Failed to reach Amp upstream"}`))
	}

	return proxy, nil
}

// isStreamingResponse detects if the response is streaming (SSE only)
// Note: We only treat text/event-stream as streaming. Chunked transfer encoding
// is a transport-level detail and doesn't mean we can't decompress the full response.
// Many JSON APIs use chunked encoding for normal responses.
func isStreamingResponse(resp *http.Response) bool {
	contentType := resp.Header.Get("Content-Type")

	// Only Server-Sent Events are true streaming responses
	if strings.Contains(contentType, "text/event-stream") {
		return true
	}

	return false
}

// handleProxyAbort safely wraps reverse proxy ServeHTTP calls to handle
// client disconnects gracefully. http.ErrAbortHandler is expected when
// clients cancel streaming requests.
func handleProxyAbort(c *gin.Context, proxyFn func()) {
	defer func() {
		if rec := recover(); rec != nil {
			if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				log.Debugf("client disconnected during streaming: %s %s",
					c.Request.Method, c.Request.URL.Path)
				c.Abort()  // Stop further handler processing
				return
			}
			// Re-panic real errors so Gin's Recovery handles them
			panic(rec)
		}
	}()
	proxyFn()
}

// proxyHandler converts httputil.ReverseProxy to gin.HandlerFunc
func proxyHandler(proxy *httputil.ReverseProxy) gin.HandlerFunc {
	return func(c *gin.Context) {
		handleProxyAbort(c, func() {
			proxy.ServeHTTP(c.Writer, c.Request)
		})
	}
}

// filterBetaFeatures removes a specific beta feature from comma-separated list
func filterBetaFeatures(header, featureToRemove string) string {
	features := strings.Split(header, ",")
	filtered := make([]string, 0, len(features))

	for _, feature := range features {
		trimmed := strings.TrimSpace(feature)
		if trimmed != "" && trimmed != featureToRemove {
			filtered = append(filtered, trimmed)
		}
	}

	return strings.Join(filtered, ",")
}

// createLiteLLMProxy creates a reverse proxy handler for LiteLLM
func createLiteLLMProxy(baseURL, apiKey string, cfg *config.Config) (*httputil.ReverseProxy, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid litellm base url: %w", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(parsed)
	originalDirector := proxy.Director

	// Modify outgoing requests to inject API key and fix path
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = parsed.Host

		// Preserve or generate correlation headers for distributed tracing
		requestID := getOrGenerateRequestID(req)
		req.Header.Set("X-Request-ID", requestID)

		// Strip /api/provider/{provider} prefix and normalize path for LiteLLM
		// Handles Vertex AI Gemini format transformation and model name mappings
		originalPath := req.URL.Path
		path := req.URL.Path

		// First strip the /api/provider/{provider} prefix if present
		if strings.HasPrefix(path, "/api/provider/") {
			parts := strings.SplitN(path, "/", 5) // ["", "api", "provider", "{provider}", "rest..."]
			if len(parts) >= 5 {
				path = "/" + parts[4] // Stripped path
			} else if len(parts) == 4 {
				path = "/" // Just root if no path after provider
			}
		}

		// Apply LiteLLM-specific path transformations (Vertex AI -> standard Gemini format)
		req.URL.Path = util.RewritePathForLiteLLM(path, cfg)

		// Inject LiteLLM API key if provided
		if apiKey != "" {
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))
		}

		log.Debugf("litellm proxy: forwarding %s %s (original: %s)", req.Method, req.URL.Path, originalPath)
	}

	// Error handler for proxy failures
	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		errorType := classifyProxyError(err)
		requestCtx := getRequestContext(req)

		// Use WARN for expected client-side issues, ERROR for upstream failures
		if errorType == "client_disconnect" {
			log.WithFields(log.Fields{
				"error_type": errorType,
				"method":     req.Method,
				"path":       req.URL.Path,
				"context":    requestCtx,
			}).Warnf("litellm proxy: client disconnected during %s %s", req.Method, req.URL.Path)
		} else {
			log.WithFields(log.Fields{
				"error_type": errorType,
				"method":     req.Method,
				"path":       req.URL.Path,
				"context":    requestCtx,
				"error":      err.Error(),
			}).Errorf("litellm proxy error for %s %s: %v", req.Method, req.URL.Path, err)
		}

		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusBadGateway)
		_, _ = rw.Write([]byte(`{"error":"litellm_proxy_error","message":"Failed to reach LiteLLM proxy"}`))
	}

	return proxy, nil
}
