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

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func removeQueryValuesMatching(req *http.Request, key string, match string) {
	if req == nil || req.URL == nil || match == "" {
		return
	}

	q := req.URL.Query()
	values, ok := q[key]
	if !ok || len(values) == 0 {
		return
	}

	kept := make([]string, 0, len(values))
	for _, v := range values {
		if v == match {
			continue
		}
		kept = append(kept, v)
	}

	if len(kept) == 0 {
		q.Del(key)
	} else {
		q[key] = kept
	}
	req.URL.RawQuery = q.Encode()
}

// readCloser wraps a reader and forwards Close to a separate closer.
// Used to restore peeked bytes while preserving upstream body Close behavior.
type readCloser struct {
	r io.Reader
	c io.Closer
}

func (rc *readCloser) Read(p []byte) (int, error) { return rc.r.Read(p) }
func (rc *readCloser) Close() error               { return rc.c.Close() }

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

		// Remove client's Authorization header - it was only used for CLI Proxy API authentication
		// We will set our own Authorization using the configured upstream-api-key
		req.Header.Del("Authorization")
		req.Header.Del("X-Api-Key")
		req.Header.Del("X-Goog-Api-Key")

		// Remove query-based credentials if they match the authenticated client API key.
		// This prevents leaking client auth material to the Amp upstream while avoiding
		// breaking unrelated upstream query parameters.
		clientKey := getClientAPIKeyFromContext(req.Context())
		removeQueryValuesMatching(req, "key", clientKey)
		removeQueryValuesMatching(req, "auth_token", clientKey)

		// Preserve correlation headers for debugging
		if req.Header.Get("X-Request-ID") == "" {
			// Could generate one here if needed
		}

		// Note: We do NOT filter Anthropic-Beta headers in the proxy path
		// Users going through ampcode.com proxy are paying for the service and should get all features
		// including 1M context window (context-1m-2025-08-07)

		// Inject API key from secret source (only uses upstream-api-key from config)
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
			resp.Header.Del("Content-Encoding")                                          // No longer compressed
			resp.Header.Del("Content-Length")                                            // Remove stale compressed length
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
		// Client-side cancellations are common during polling; suppress logging in this case
		if errors.Is(err, context.Canceled) {
			return
		}
		log.Errorf("amp upstream proxy error for %s %s: %v", req.Method, req.URL.Path, err)
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

// proxyHandler converts httputil.ReverseProxy to gin.HandlerFunc
func proxyHandler(proxy *httputil.ReverseProxy) gin.HandlerFunc {
	return func(c *gin.Context) {
		proxy.ServeHTTP(c.Writer, c.Request)
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
